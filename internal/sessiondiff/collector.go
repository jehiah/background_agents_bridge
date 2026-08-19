package sessiondiff

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// Git tree entry mode for a submodule gitlink, and the placeholder mode for a
// side of the diff where no entry exists.
const (
	submoduleMode = "160000"
	absentMode    = "000000"
)

// statusByLetter maps `git diff --raw` status letters. Copy records carry an
// old path just like renames, so both land on "renamed".
var statusByLetter = map[byte]string{
	'A': statusAdded,
	'M': statusModified,
	'D': statusDeleted,
	'T': statusTypeChanged,
	'U': statusUnmerged,
	'R': statusRenamed,
	'C': statusRenamed,
}

// collector captures one repository. It is created per repository so the git
// helpers can carry the checkout's identity into error messages.
type collector struct {
	dir    string
	name   string // "owner/name", for error messages
	limits Limits
	env    []string
}

// changedPath is one path reported as changed, before its patch is fetched.
type changedPath struct {
	status    string
	path      string
	oldPath   string
	untracked bool
}

// key identifies a change in the metadata maps. An empty oldPath means "no
// rename source", matching Python's None key.
func (c changedPath) key() pathKey { return pathKey{old: c.oldPath, path: c.path} }

type pathKey struct{ old, path string }

// trackedMetadata is the mode/blob information from a --raw record.
type trackedMetadata struct {
	oldMode, newMode string
	oldSHA, newSHA   string
}

// lineStats are added/deleted line counts; nil marks binary content.
type lineStats struct{ additions, deletions *int }

// repositoryCapture is one repository's net checkout changes relative to its
// immutable baseline.
type repositoryCapture struct {
	headSHA          string
	files            []File
	truncated        bool
	omittedFileCount int
	// patchBytes is the total size of the renderable patches, which the caller
	// deducts from the session-wide budget.
	patchBytes int
}

// CollectBundle captures every session repository into one bounded upload
// bundle. A repository that cannot be captured becomes an "unavailable" entry
// rather than failing the bundle, so a broken checkout never hides the others.
// Port of collect_session_diff_bundle.
func CollectBundle(ctx context.Context, repositories []repomanifest.Entry, triggerMessageID *string, capturedAt int64, limits Limits) (*Bundle, error) {
	remainingFiles := limits.MaxFiles
	remainingPatchBytes := limits.MaxCaptureBytes

	bundle := &Bundle{
		Version:          Version,
		TriggerMessageID: triggerMessageID,
		CapturedAt:       capturedAt,
		Repositories:     make([]Repository, 0, len(repositories)),
	}
	for position, repository := range repositories {
		if repository.BaseSHA == "" {
			return nil, errorf("Session start baseline is unavailable")
		}
		entryLimits := limits
		entryLimits.MaxFiles = remainingFiles
		entryLimits.MaxCaptureBytes = remainingPatchBytes

		capture, err := collectRepository(ctx, repository, entryLimits)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			bundle.Repositories = append(bundle.Repositories, Repository{
				Status:    "unavailable",
				Position:  position,
				RepoOwner: repository.Owner,
				RepoName:  repository.Name,
				BaseSHA:   repository.BaseSHA,
				Error:     orFallback(captureMessage(err), "Repository diff unavailable"),
				Files:     []File{},
			})
			continue
		}

		remainingFiles = max(0, remainingFiles-len(capture.files))
		remainingPatchBytes = max(0, remainingPatchBytes-capture.patchBytes)
		truncated, omitted := capture.truncated, capture.omittedFileCount
		bundle.Repositories = append(bundle.Repositories, Repository{
			Status:           "ready",
			Position:         position,
			RepoOwner:        repository.Owner,
			RepoName:         repository.Name,
			BaseSHA:          repository.BaseSHA,
			HeadSHA:          capture.headSHA,
			Truncated:        &truncated,
			OmittedFileCount: &omitted,
			Files:            capture.files,
		})
	}

	if err := boundEncodedBundle(bundle, limits.MaxBundleBytes); err != nil {
		return nil, err
	}
	return bundle, nil
}

// collectRepository collects one repository's net checkout state relative to
// its baseline commit. Port of collect_repository_diff.
func collectRepository(ctx context.Context, repository repomanifest.Entry, limits Limits) (*repositoryCapture, error) {
	c := &collector{
		dir:    repository.Path,
		name:   repository.FullName(),
		limits: limits,
		env:    gitEnv(),
	}
	if info, err := os.Stat(c.dir); err != nil || !info.IsDir() {
		return nil, errorf("Repository checkout is missing: %s", c.dir)
	}
	baseSHA := repository.BaseSHA

	// The baseline must still be reachable; a shallow fetch or a rewritten
	// history makes every subsequent diff a lie.
	if _, err := c.runGit(ctx, c.dir, gitOptions{maxStdout: 4096}, "cat-file", "-e", baseSHA+"^{commit}"); err != nil {
		return nil, err
	}
	headRaw, err := c.runGit(ctx, c.dir, gitOptions{maxStdout: 4096}, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	headSHA := strings.TrimSpace(string(headRaw))

	tracked, trackedMeta, err := c.trackedChanges(ctx, baseSHA)
	if err != nil {
		return nil, metadataLimitError(err)
	}
	untrackedRaw, err := c.runGit(ctx, c.dir,
		gitOptions{maxStdout: limits.MaxMetadataBytes},
		"ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, metadataLimitError(err)
	}
	trackedStats, err := c.trackedLineStats(ctx, baseSHA)
	if err != nil {
		return nil, metadataLimitError(err)
	}
	unmerged, err := c.unmergedPaths(ctx)
	if err != nil {
		return nil, metadataLimitError(err)
	}

	runtimePaths := c.runtimeGitExcludes(ctx, c.dir)
	var untracked []changedPath
	untrackedPaths := map[string]bool{}
	for _, raw := range splitNUL(untrackedRaw) {
		path := decodePath(raw)
		if isRuntimeExcluded(path, runtimePaths) {
			continue
		}
		untracked = append(untracked, changedPath{status: statusAdded, path: path, untracked: true})
		untrackedPaths[path] = true
	}

	// A path can be deleted in the index and present as an untracked file at
	// the same time (e.g. after `git rm --cached`). That is one change, not two.
	overlayPaths := map[string]bool{}
	for _, change := range tracked {
		if change.status == statusDeleted && untrackedPaths[change.path] {
			overlayPaths[change.path] = true
		}
	}

	allChanges := make([]changedPath, 0, len(tracked)+len(untracked))
	for _, change := range tracked {
		switch {
		case overlayPaths[change.path]:
			change.status = statusModified
		case unmerged[change.path]:
			change.status = statusUnmerged
		}
		allChanges = append(allChanges, change)
	}
	for _, change := range untracked {
		if !overlayPaths[change.path] {
			allChanges = append(allChanges, change)
		}
	}

	selected := allChanges
	if limits.MaxFiles < len(selected) {
		selected = selected[:max(0, limits.MaxFiles)]
	}

	capture := &repositoryCapture{
		headSHA:          headSHA,
		files:            make([]File, 0, len(selected)),
		truncated:        len(allChanges) > len(selected),
		omittedFileCount: len(allChanges) - len(selected),
	}
	remainingCaptureBytes := limits.MaxCaptureBytes
	for _, change := range selected {
		isOverlay := overlayPaths[change.path]
		file, patchBytes, err := c.captureFile(ctx, change, captureContext{
			baseSHA:               baseSHA,
			trackedStats:          trackedStats,
			trackedMeta:           trackedMeta,
			isOverlay:             isOverlay,
			isUntracked:           !isOverlay && change.untracked,
			remainingCaptureBytes: remainingCaptureBytes,
		})
		if err != nil {
			return nil, err
		}
		remainingCaptureBytes -= patchBytes
		capture.patchBytes += patchBytes
		capture.files = append(capture.files, file)
	}
	return capture, nil
}

// metadataLimitError converts an output-ceiling breach on a metadata command
// into the user-visible capture failure.
func metadataLimitError(err error) error {
	if errors.Is(err, errOutputTooLarge) {
		return errorf("Repository change metadata exceeded its memory limit")
	}
	return err
}

// captureContext carries the per-repository state one file capture needs.
type captureContext struct {
	baseSHA               string
	trackedStats          map[pathKey]lineStats
	trackedMeta           map[pathKey]trackedMetadata
	isOverlay             bool
	isUntracked           bool
	remainingCaptureBytes int
}

// captureFile captures one changed path's metadata and, when renderable, its
// patch. It returns the file and the byte size of any patch it kept. Port of
// _capture_file.
func (c *collector) captureFile(ctx context.Context, change changedPath, cc captureContext) (File, int, error) {
	stats, err := c.changeLineStats(ctx, change, cc)
	if err != nil {
		return File{}, 0, err
	}

	file := File{
		ID:        randomUUID(),
		Path:      change.path,
		Status:    change.status,
		OldPath:   change.oldPath,
		Additions: stats.additions,
		Deletions: stats.deletions,
	}

	metadata, hasMetadata := cc.trackedMeta[change.key()]
	if hasMetadata && metadata.oldMode != metadata.newMode {
		if metadata.oldMode != absentMode {
			file.OldMode = metadata.oldMode
		}
		if metadata.newMode != absentMode {
			file.NewMode = metadata.newMode
		}
	}

	if hasMetadata && (metadata.oldMode == submoduleMode || metadata.newMode == submoduleMode) {
		oldSubmodule, newSubmodule, err := c.submoduleSHAs(ctx, change, metadata)
		if err != nil {
			return File{}, 0, err
		}
		file.Status = statusSubmodule
		file.RenderState = renderMetadataOnly
		file.OldSubmoduleSHA = oldSubmodule
		file.NewSubmoduleSHA = newSubmodule
		return file, 0, nil
	}

	switch {
	case stats.additions == nil || stats.deletions == nil:
		file.RenderState = renderBinary
	case cc.isOverlay:
		// Two contradictory patches for one path would be worse than none.
		file.RenderState = renderMetadataOnly
	default:
		rendered, err := c.renderedPatch(ctx, change, cc, stats)
		if err != nil {
			return File{}, 0, err
		}
		file.RenderState = rendered.renderState
		file.Patch = rendered.patch
		if rendered.renderState == renderRenderable {
			return file, len(rendered.patch), nil
		}
	}
	return file, 0, nil
}

// renderedPatch is the outcome of rendering one file's patch against the
// capture limits.
type renderedPatch struct {
	renderState string
	patch       string
}

// renderedPatch fetches one file's patch and decides its render state. Port of
// _rendered_patch.
func (c *collector) renderedPatch(ctx context.Context, change changedPath, cc captureContext, stats lineStats) (renderedPatch, error) {
	var raw []byte
	var err error
	if cc.isUntracked {
		raw, err = c.untrackedPatch(ctx, change.path)
	} else {
		raw, err = c.trackedPatch(ctx, cc.baseSHA, change)
	}
	if errors.Is(err, errOutputTooLarge) {
		return renderedPatch{renderState: renderTooLarge}, nil
	}
	if err != nil {
		return renderedPatch{}, err
	}

	// The upload carries the normalized UTF-8 text, so the limits must describe
	// those exact bytes rather than git's potentially non-UTF-8 stdout.
	text := decodeUTF8Replace(raw)
	size := len(text)
	if !cc.isUntracked && *stats.additions == 0 && *stats.deletions == 0 && !strings.Contains(text, "\n@@") {
		return renderedPatch{renderState: renderMetadataOnly}, nil
	}
	if size > c.limits.MaxPatchBytes || size > cc.remainingCaptureBytes {
		return renderedPatch{renderState: renderTooLarge}, nil
	}
	if len(raw) == 0 {
		return renderedPatch{renderState: renderMetadataOnly}, nil
	}
	return renderedPatch{renderState: renderRenderable, patch: text}, nil
}

// --- git plumbing ------------------------------------------------------------

// trackedChanges reads the --raw record for every tracked change.
func (c *collector) trackedChanges(ctx context.Context, baseSHA string) ([]changedPath, map[pathKey]trackedMetadata, error) {
	raw, err := c.runGit(ctx, c.dir, gitOptions{maxStdout: c.limits.MaxMetadataBytes},
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--raw", "-z", "--no-abbrev", "--find-renames", baseSHA)
	if err != nil {
		return nil, nil, err
	}
	return parseRawChanges(raw)
}

// parseRawChanges parses `git diff --raw -z` output. Port of _parse_raw_changes.
func parseRawChanges(raw []byte) ([]changedPath, map[pathKey]trackedMetadata, error) {
	fields := splitNUL(raw)
	changes := []changedPath{}
	metadata := map[pathKey]trackedMetadata{}
	for i := 0; i < len(fields); {
		header := bytes.Fields(fields[i])
		i++
		if len(header) != 5 || !bytes.HasPrefix(header[0], []byte(":")) {
			return nil, nil, errorf("Malformed Git raw diff record")
		}
		code := string(header[4])
		var letter byte
		if code != "" {
			letter = code[0]
		}

		var oldPath, path string
		if letter == 'R' || letter == 'C' {
			if i+1 >= len(fields) {
				return nil, nil, errorf("Malformed Git raw rename record")
			}
			oldPath, path = decodePath(fields[i]), decodePath(fields[i+1])
			i += 2
		} else {
			if i >= len(fields) {
				return nil, nil, errorf("Malformed Git raw diff record")
			}
			path = decodePath(fields[i])
			i++
		}

		status, ok := statusByLetter[letter]
		if !ok {
			return nil, nil, errorf("Unsupported Git status letter: %q", string(letter))
		}
		change := changedPath{status: status, path: path, oldPath: oldPath}
		changes = append(changes, change)
		metadata[change.key()] = trackedMetadata{
			oldMode: string(header[0][1:]),
			newMode: string(header[1]),
			oldSHA:  string(header[2]),
			newSHA:  string(header[3]),
		}
	}
	return changes, metadata, nil
}

// trackedLineStats reads added/deleted counts for every tracked change.
func (c *collector) trackedLineStats(ctx context.Context, baseSHA string) (map[pathKey]lineStats, error) {
	raw, err := c.runGit(ctx, c.dir, gitOptions{maxStdout: c.limits.MaxMetadataBytes},
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--numstat", "-z", "--find-renames", baseSHA)
	if err != nil {
		return nil, err
	}
	return parseNumstat(raw)
}

// parseNumstat parses `git diff --numstat -z` output, where a rename record
// spills its two paths into the following NUL-separated fields. Port of
// _parse_numstat.
func parseNumstat(raw []byte) (map[pathKey]lineStats, error) {
	fields := splitNUL(raw)
	stats := map[pathKey]lineStats{}
	for i := 0; i < len(fields); {
		columns := bytes.SplitN(fields[i], []byte("\t"), 3)
		i++
		if len(columns) != 3 {
			return nil, errorf("Malformed Git numstat record")
		}
		parsed, err := parseStatColumns(columns[0], columns[1])
		if err != nil {
			return nil, err
		}
		if len(columns[2]) > 0 {
			stats[pathKey{path: decodePath(columns[2])}] = parsed
			continue
		}
		if i+1 >= len(fields) {
			return nil, errorf("Malformed Git rename numstat record")
		}
		stats[pathKey{old: decodePath(fields[i]), path: decodePath(fields[i+1])}] = parsed
		i += 2
	}
	return stats, nil
}

// parseStatColumns reads one numstat pair; "-" on either side marks binary
// content, which the wire represents as null counts.
func parseStatColumns(additions, deletions []byte) (lineStats, error) {
	if string(additions) == "-" || string(deletions) == "-" {
		return lineStats{}, nil
	}
	a, err := strconv.Atoi(string(additions))
	if err != nil {
		return lineStats{}, errorf("Malformed Git numstat record")
	}
	d, err := strconv.Atoi(string(deletions))
	if err != nil {
		return lineStats{}, errorf("Malformed Git numstat record")
	}
	return lineStats{additions: &a, deletions: &d}, nil
}

// unmergedPaths lists paths with conflict entries in the index.
func (c *collector) unmergedPaths(ctx context.Context) (map[string]bool, error) {
	raw, err := c.runGit(ctx, c.dir, gitOptions{maxStdout: c.limits.MaxMetadataBytes},
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, field := range splitNUL(raw) {
		paths[decodePath(field)] = true
	}
	return paths, nil
}

// trackedPatch renders one tracked path's patch against the baseline. The
// unified context is effectively unlimited so the client can render whole-file
// views without a second round trip.
func (c *collector) trackedPatch(ctx context.Context, baseSHA string, change changedPath) ([]byte, error) {
	args := []string{
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--full-index", "--find-renames", "--unified=1000000", baseSHA, "--",
	}
	if change.oldPath != "" {
		args = append(args, change.oldPath)
	}
	args = append(args, change.path)
	return c.runGit(ctx, c.dir, gitOptions{maxStdout: c.limits.MaxPatchBytes}, args...)
}

// untrackedPatch renders an untracked file as an addition. --no-index exits 1
// when it finds differences, which is the expected case here.
func (c *collector) untrackedPatch(ctx context.Context, path string) ([]byte, error) {
	return c.runGit(ctx, c.dir,
		gitOptions{maxStdout: c.limits.MaxPatchBytes, acceptedCodes: []int{0, 1}},
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--no-index", "--full-index", "--unified=1000000", "--", os.DevNull, path)
}

// untrackedStats counts the lines an untracked file adds.
func (c *collector) untrackedStats(ctx context.Context, path string) (lineStats, error) {
	raw, err := c.runGit(ctx, c.dir,
		gitOptions{maxStdout: 64 * 1024, acceptedCodes: []int{0, 1}},
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--no-index", "--numstat", "--", os.DevNull, path)
	if err != nil {
		return lineStats{}, err
	}
	line, _, _ := bytes.Cut(raw, []byte("\n"))
	columns := bytes.SplitN(line, []byte("\t"), 3)
	if len(columns) < 2 {
		zero := 0
		zeroToo := 0
		return lineStats{additions: &zero, deletions: &zeroToo}, nil
	}
	return parseStatColumns(columns[0], columns[1])
}

// changeLineStats returns the added/deleted counts for one change. Port of
// _change_line_stats.
func (c *collector) changeLineStats(ctx context.Context, change changedPath, cc captureContext) (lineStats, error) {
	if cc.isOverlay {
		// The index deletion and the working-tree file both count.
		tracked, ok := cc.trackedStats[change.key()]
		if !ok {
			tracked = zeroStats()
		}
		untracked, err := c.untrackedStats(ctx, change.path)
		if err != nil {
			return lineStats{}, err
		}
		return lineStats{
			additions: addCounts(tracked.additions, untracked.additions),
			deletions: addCounts(tracked.deletions, untracked.deletions),
		}, nil
	}
	if cc.isUntracked {
		return c.untrackedStats(ctx, change.path)
	}
	if stats, ok := cc.trackedStats[change.key()]; ok {
		return stats, nil
	}
	return zeroStats(), nil
}

// submoduleSHAs resolves the old/new commit pointers for a submodule entry.
// Git reports the all-zero placeholder for a dirty, uncommitted pointer, in
// which case the submodule's checked-out HEAD is the truthful value. Port of
// _submodule_shas.
func (c *collector) submoduleSHAs(ctx context.Context, change changedPath, metadata trackedMetadata) (string, string, error) {
	var oldSHA, newSHA string
	if metadata.oldMode == submoduleMode && !isAllZero(metadata.oldSHA) {
		oldSHA = metadata.oldSHA
	}
	if metadata.newMode == submoduleMode && !isAllZero(metadata.newSHA) {
		newSHA = metadata.newSHA
	}
	if metadata.newMode == submoduleMode && newSHA == "" {
		resolved, err := c.submoduleHead(ctx, change.path)
		if err != nil {
			return "", "", err
		}
		newSHA = resolved
	}
	return oldSHA, newSHA, nil
}

// submoduleHead reads a submodule's checked-out HEAD, rejecting anything that
// is not a plain object name.
func (c *collector) submoduleHead(ctx context.Context, path string) (string, error) {
	raw, err := c.runGit(ctx, c.dir, gitOptions{maxStdout: 128}, "-C", path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if !repomanifest.IsObjectName(value) {
		return "", errorf("Invalid submodule HEAD for %s", path)
	}
	return value, nil
}

// --- small helpers -----------------------------------------------------------

// splitNUL splits NUL-separated git output, dropping the trailing empty field.
func splitNUL(raw []byte) [][]byte {
	fields := bytes.Split(raw, []byte{0})
	if len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	return fields
}

// decodePath keeps git's bytes verbatim; a path that is not valid UTF-8 is
// still a usable map key and JSON-encodes with replacement characters.
func decodePath(raw []byte) string { return string(raw) }

func zeroStats() lineStats {
	a, d := 0, 0
	return lineStats{additions: &a, deletions: &d}
}

// addCounts sums two counts, propagating "binary" (nil).
func addCounts(a, b *int) *int {
	if a == nil || b == nil {
		return nil
	}
	sum := *a + *b
	return &sum
}

func isAllZero(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

func orFallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
