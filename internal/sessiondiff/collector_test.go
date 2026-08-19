package sessiondiff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// testRepo is a real git checkout; the collector is mostly git plumbing, so
// the tests exercise it against actual repositories rather than fakes.
type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "--initial-branch=main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+r.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *testRepo) write(path, contents string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything and commits, returning the new commit's SHA.
func (r *testRepo) commit(message string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", message)
	return r.git("rev-parse", "HEAD")
}

func (r *testRepo) entry(baseSHA string) repomanifest.Entry {
	return repomanifest.Entry{Owner: "acme", Name: "web", Path: r.dir, BaseSHA: baseSHA}
}

// collect runs a capture over one repository with the default limits.
func collect(t *testing.T, entries []repomanifest.Entry, limits Limits) *Bundle {
	t.Helper()
	bundle, err := CollectBundle(t.Context(), entries, nil, 1700000000000, limits)
	if err != nil {
		t.Fatalf("CollectBundle: %v", err)
	}
	return bundle
}

// fileByPath finds a captured file, failing the test when it is absent.
func fileByPath(t *testing.T, repository Repository, path string) File {
	t.Helper()
	for _, file := range repository.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("no captured file for %q (have %d files)", path, len(repository.Files))
	return File{}
}

func TestCollectBundleCapturesWorkingState(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("keep.txt", "unchanged\n")
	repo.write("edit.txt", "one\n")
	repo.write("gone.txt", "bye\n")
	repo.write("old-name.txt", "same contents, new name\n")
	base := repo.commit("base")

	// Every kind of change the viewer renders differently, including one made
	// by a commit (the baseline is the session start, not HEAD).
	repo.write("edit.txt", "one\ntwo\n")
	repo.commit("committed edit")
	os.Remove(filepath.Join(repo.dir, "gone.txt"))
	repo.git("rm", "--quiet", "gone.txt")
	repo.git("mv", "old-name.txt", "new-name.txt")
	repo.write("added.txt", "brand new\n")

	bundle := collect(t, []repomanifest.Entry{repo.entry(base)}, DefaultLimits())

	if bundle.Version != Version || bundle.CapturedAt != 1700000000000 || bundle.TriggerMessageID != nil {
		t.Errorf("bundle envelope = %+v", *bundle)
	}
	if len(bundle.Repositories) != 1 {
		t.Fatalf("want 1 repository, got %d", len(bundle.Repositories))
	}
	repository := bundle.Repositories[0]
	if repository.Status != "ready" || repository.BaseSHA != base || repository.HeadSHA == base {
		t.Errorf("repository = %+v (head should have advanced past base)", repository)
	}
	if got := len(repository.Files); got != 4 {
		t.Fatalf("want 4 changed files, got %d: %+v", got, repository.Files)
	}

	edited := fileByPath(t, repository, "edit.txt")
	if edited.Status != statusModified || edited.RenderState != renderRenderable {
		t.Errorf("edit.txt = %+v", edited)
	}
	if *edited.Additions != 1 || *edited.Deletions != 0 {
		t.Errorf("edit.txt stats = +%d/-%d", *edited.Additions, *edited.Deletions)
	}
	if !strings.Contains(edited.Patch, "+two") {
		t.Errorf("edit.txt patch = %q", edited.Patch)
	}
	if got := fileByPath(t, repository, "gone.txt").Status; got != statusDeleted {
		t.Errorf("gone.txt status = %q", got)
	}
	if got := fileByPath(t, repository, "added.txt"); got.Status != statusAdded || got.Patch == "" {
		t.Errorf("added.txt = %+v", got)
	}
	renamed := fileByPath(t, repository, "new-name.txt")
	if renamed.Status != statusRenamed || renamed.OldPath != "old-name.txt" {
		t.Errorf("rename = %+v", renamed)
	}
	// keep.txt is unchanged and must not appear at all.
	for _, file := range repository.Files {
		if file.Path == "keep.txt" {
			t.Error("unchanged file was captured")
		}
		if file.ID == "" {
			t.Errorf("%s has no id", file.Path)
		}
	}
}

func TestCollectBundleBinaryAndModeChanges(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("blob.bin", "\x00\x01binary\x00")
	repo.write("script.sh", "#!/bin/sh\n")
	base := repo.commit("base")

	repo.write("blob.bin", "\x00\x02changed\x00")
	if err := os.Chmod(filepath.Join(repo.dir, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	repository := collect(t, []repomanifest.Entry{repo.entry(base)}, DefaultLimits()).Repositories[0]

	blob := fileByPath(t, repository, "blob.bin")
	if blob.RenderState != renderBinary || blob.Additions != nil || blob.Deletions != nil {
		t.Errorf("binary file = %+v (counts must be null)", blob)
	}
	if blob.Patch != "" {
		t.Errorf("binary file carried a patch: %q", blob.Patch)
	}
	script := fileByPath(t, repository, "script.sh")
	if script.OldMode != "100644" || script.NewMode != "100755" {
		t.Errorf("mode change = %q -> %q", script.OldMode, script.NewMode)
	}
}

func TestCollectBundleSkipsRuntimeExcludes(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("keep.txt", "x\n")
	base := repo.commit("base")

	// The runtime's own assets are untracked but are not the agent's work.
	repo.write(".git/info/exclude", excludeBeginMarker+"\n/.oi-runtime/\n/AGENTS.md\n"+excludeEndMarker+"\n")
	repo.write(".oi-runtime/tool.js", "runtime\n")
	repo.write("AGENTS.md", "generated\n")
	repo.write("mine.txt", "agent work\n")

	repository := collect(t, []repomanifest.Entry{repo.entry(base)}, DefaultLimits()).Repositories[0]

	if len(repository.Files) != 1 || repository.Files[0].Path != "mine.txt" {
		t.Errorf("want only mine.txt, got %+v", repository.Files)
	}
}

func TestCollectBundleTruncatesToFileLimit(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("seed.txt", "x\n")
	base := repo.commit("base")
	for i := range 5 {
		repo.write(string(rune('a'+i))+".txt", "new\n")
	}

	limits := DefaultLimits()
	limits.MaxFiles = 2
	repository := collect(t, []repomanifest.Entry{repo.entry(base)}, limits).Repositories[0]

	if len(repository.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(repository.Files))
	}
	if repository.Truncated == nil || !*repository.Truncated {
		t.Error("truncated flag not set")
	}
	if repository.OmittedFileCount == nil || *repository.OmittedFileCount != 3 {
		t.Errorf("omittedFileCount = %v, want 3", repository.OmittedFileCount)
	}
}

func TestCollectBundleDropsOversizePatch(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("big.txt", "x\n")
	base := repo.commit("base")
	repo.write("big.txt", strings.Repeat("a line of text\n", 500))

	limits := DefaultLimits()
	limits.MaxPatchBytes = 256
	repository := collect(t, []repomanifest.Entry{repo.entry(base)}, limits).Repositories[0]

	big := fileByPath(t, repository, "big.txt")
	// The change is still reported, with counts; only the patch is withheld.
	if big.RenderState != renderTooLarge || big.Patch != "" {
		t.Errorf("oversize file = %+v", big)
	}
	if big.Additions == nil || *big.Additions == 0 {
		t.Errorf("oversize file lost its line counts: %+v", big)
	}
}

func TestCollectBundleUnavailableRepository(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "x\n")
	base := repo.commit("base")

	missing := repomanifest.Entry{
		Owner: "acme", Name: "gone",
		Path:    filepath.Join(t.TempDir(), "absent"),
		BaseSHA: base,
	}
	// A baseline git cannot resolve is a capture failure, not an empty diff.
	unreachable := repo.entry("0123456789abcdef0123456789abcdef01234567")
	unreachable.Name = "unreachable"

	bundle := collect(t, []repomanifest.Entry{repo.entry(base), missing, unreachable}, DefaultLimits())

	if len(bundle.Repositories) != 3 {
		t.Fatalf("want 3 repositories, got %d", len(bundle.Repositories))
	}
	if bundle.Repositories[0].Status != "ready" {
		t.Errorf("first repository = %+v", bundle.Repositories[0])
	}
	for _, index := range []int{1, 2} {
		repository := bundle.Repositories[index]
		if repository.Status != "unavailable" || repository.Error == "" {
			t.Errorf("repository %d = %+v, want unavailable with an error", index, repository)
		}
		if repository.Position != index {
			t.Errorf("repository %d has position %d", index, repository.Position)
		}
	}
}

func TestCollectBundleRequiresBaseline(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "x\n")
	repo.commit("base")

	// Without a baseline there is nothing trustworthy to diff against, so the
	// whole bundle fails rather than silently reporting no changes.
	_, err := CollectBundle(t.Context(), []repomanifest.Entry{repo.entry("")}, nil, 1, DefaultLimits())
	if err == nil {
		t.Fatal("want an error for a repository without a baseline")
	}
	if !strings.Contains(err.Error(), "baseline") {
		t.Errorf("error = %v", err)
	}
}

func TestCollectBundleOverlaidDeleteAndUntracked(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("both.txt", "tracked\n")
	base := repo.commit("base")

	// Removed from the index but present in the working tree: one change, not
	// a delete plus an add, and with no patch (the two would contradict).
	repo.git("rm", "--cached", "--quiet", "both.txt")
	repo.write("both.txt", "untracked now\n")

	repository := collect(t, []repomanifest.Entry{repo.entry(base)}, DefaultLimits()).Repositories[0]

	if len(repository.Files) != 1 {
		t.Fatalf("want 1 file, got %+v", repository.Files)
	}
	file := repository.Files[0]
	if file.Status != statusModified || file.RenderState != renderMetadataOnly || file.Patch != "" {
		t.Errorf("overlaid path = %+v", file)
	}
}

func TestBoundEncodedBundleShedsLargestPatchFirst(t *testing.T) {
	small := strings.Repeat("s", 100)
	large := strings.Repeat("l", 4000)
	bundle := &Bundle{
		Version: Version,
		Repositories: []Repository{{
			Status: "ready",
			Files: []File{
				{ID: "1", Path: "small", RenderState: renderRenderable, Patch: small},
				{ID: "2", Path: "large", RenderState: renderRenderable, Patch: large},
			},
		}},
	}
	if err := boundEncodedBundle(bundle, 2000); err != nil {
		t.Fatalf("boundEncodedBundle: %v", err)
	}
	files := bundle.Repositories[0].Files
	if files[1].Patch != "" || files[1].RenderState != renderTooLarge {
		t.Errorf("large patch survived: %+v", files[1])
	}
	if files[0].Patch != small {
		t.Errorf("small patch was dropped unnecessarily: %+v", files[0])
	}
}

func TestBoundEncodedBundleShedsFilesWhenMetadataAlone(t *testing.T) {
	files := make([]File, 200)
	for i := range files {
		files[i] = File{ID: randomUUID(), Path: strings.Repeat("p", 50), RenderState: renderMetadataOnly}
	}
	truncated, omitted := false, 0
	bundle := &Bundle{
		Version:      Version,
		Repositories: []Repository{{Status: "ready", Files: files, Truncated: &truncated, OmittedFileCount: &omitted}},
	}
	if err := boundEncodedBundle(bundle, 4000); err != nil {
		t.Fatalf("boundEncodedBundle: %v", err)
	}
	repository := bundle.Repositories[0]
	if len(repository.Files) >= 200 {
		t.Fatalf("no files were shed: %d", len(repository.Files))
	}
	if !*repository.Truncated || *repository.OmittedFileCount != 200-len(repository.Files) {
		t.Errorf("truncation not recorded: truncated=%v omitted=%d files=%d",
			*repository.Truncated, *repository.OmittedFileCount, len(repository.Files))
	}
	if len(encodeBundle(bundle)) > 4000 {
		t.Errorf("bundle still over budget: %d bytes", len(encodeBundle(bundle)))
	}
}

func TestParseNumstatRename(t *testing.T) {
	// A rename record leaves the path column empty and spills the old and new
	// paths into the two following NUL-separated fields.
	raw := []byte("1\t2\ta.txt\x003\t4\t\x00old.txt\x00new.txt\x00-\t-\tbin\x00")
	stats, err := parseNumstat(raw)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	plain, ok := stats[pathKey{path: "a.txt"}]
	if !ok || *plain.additions != 1 || *plain.deletions != 2 {
		t.Errorf("plain record = %+v", plain)
	}
	renamed, ok := stats[pathKey{old: "old.txt", path: "new.txt"}]
	if !ok || *renamed.additions != 3 || *renamed.deletions != 4 {
		t.Errorf("rename record = %+v", renamed)
	}
	if binary, ok := stats[pathKey{path: "bin"}]; !ok || binary.additions != nil {
		t.Errorf("binary record = %+v", binary)
	}
}

func TestDecodeUTF8Replace(t *testing.T) {
	if got := decodeUTF8Replace([]byte("ok \xff\xfe")); got != "ok ��" {
		t.Errorf("decodeUTF8Replace = %q", got)
	}
}

func TestRunGitRejectsOversizeOutput(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("big.txt", strings.Repeat("x", 4096))
	repo.commit("base")

	c := &collector{dir: repo.dir, name: "acme/web", limits: DefaultLimits(), env: gitEnv()}
	_, err := c.runGit(context.Background(), repo.dir, gitOptions{maxStdout: 16}, "--no-pager", "show", "HEAD")
	if err == nil || !strings.Contains(err.Error(), errOutputTooLarge.Error()) {
		t.Fatalf("want errOutputTooLarge, got %v", err)
	}
}

func TestRunGitReportsFailure(t *testing.T) {
	c := &collector{dir: t.TempDir(), name: "acme/web", limits: DefaultLimits(), env: gitEnv()}
	_, err := c.runGit(context.Background(), c.dir, gitOptions{maxStdout: 4096}, "rev-parse", "HEAD")
	if err == nil {
		t.Fatal("want an error outside a repository")
	}
	if !strings.Contains(captureMessage(err), "acme/web") {
		t.Errorf("error should name the repository: %q", captureMessage(err))
	}
}
