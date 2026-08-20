package skills

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// DefaultBundledSkillsPath is where the image bakes in the skills that ship with
// the sandbox runtime. Managed skills may not shadow them.
const DefaultBundledSkillsPath = "/app/sandbox_runtime/skills"

// discoveryPaths are the per-directory roots OpenCode scans for skills. A
// managed skill name that also appears under one of these is ambiguous.
var discoveryPaths = [...]string{".opencode/skills", ".claude/skills", ".agents/skills"}

// Swap state lives next to the destination so every rename is same-filesystem.
const (
	stagingName = ".managed-skills-staging"
	backupName  = ".managed-skills-backup"
	journalName = ".managed-skills-swap"
)

// fetcher is the fetch half of the client, so tests can substitute one.
type fetcher interface {
	FetchInstallation(ctx context.Context) ([]byte, error)
}

// Materializer installs a fetched installation DTO into the platform-owned
// global skills directory.
type Materializer struct {
	Client      fetcher
	Destination string
	Log         *slog.Logger
	// BundledSkillsPath is the image-baked skills tree checked for name
	// collisions; NewMaterializer defaults it to DefaultBundledSkillsPath.
	BundledSkillsPath string
}

// NewMaterializer builds a Materializer writing to destination (the global
// OpenCode config dir's skills/).
func NewMaterializer(client fetcher, destination string, log *slog.Logger) *Materializer {
	return &Materializer{
		Client:            client,
		Destination:       destination,
		Log:               log,
		BundledSkillsPath: DefaultBundledSkillsPath,
	}
}

// Materialize fetches, validates, collision-checks, and installs skills. It must
// complete before OpenCode starts; any error is fatal to boot, since a partially
// trusted skills tree is worse than none.
func (m *Materializer) Materialize(ctx context.Context, repositories []repomanifest.Entry, workdir string) error {
	raw, err := m.Client.FetchInstallation(ctx)
	if err != nil {
		return asManagedError(err)
	}
	installation, err := ValidateInstallation(raw)
	if err != nil {
		return asManagedError(err)
	}
	if err := m.checkCollisions(installation, repositories, workdir); err != nil {
		return asManagedError(err)
	}
	if err := m.install(installation); err != nil {
		return asManagedError(err)
	}
	m.Log.Info("managed_skills.materialized",
		"manifest_sha256", installation.ManifestSHA256,
		"skill_count", len(installation.Skills),
	)
	return nil
}

// asManagedError keeps a coded *Error as-is and labels anything else
// install_failed, so callers always see a stable code.
func asManagedError(err error) error {
	if managed, ok := errors.AsType[*Error](err); ok {
		return managed
	}
	return wrapError("install_failed", err, "failed to install managed skills: %v", err)
}

// checkCollisions rejects ambiguous names across every OpenCode skill discovery
// root. Installing a managed skill that shadows (or is shadowed by) a repo- or
// image-provided skill would silently change which one the agent runs.
func (m *Materializer) checkCollisions(installation *Installation, repositories []repomanifest.Entry, workdir string) error {
	selected := make(map[string]bool, len(installation.Skills))
	for _, skill := range installation.Skills {
		selected[skill.Name] = true
	}
	for _, root := range m.collisionRoots(repositories, workdir) {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // not a directory, or unreadable — nothing to collide with
		}
		for _, entry := range entries {
			child := filepath.Join(root, entry.Name())
			if info, err := os.Stat(child); err != nil || !info.IsDir() {
				continue
			}
			var collisions []string
			for name := range skillNames(child) {
				if selected[name] {
					collisions = append(collisions, name)
				}
			}
			if len(collisions) > 0 {
				slices.Sort(collisions)
				return newError("name_collision", "managed skill %q collides with discovered skill at %s", collisions[0], child)
			}
		}
	}
	return nil
}

// collisionRoots lists every directory OpenCode would discover skills in: the
// bundled tree, then each discovery path under the workdir, each checkout, and
// $HOME. The destination itself is skipped — it is what we are replacing.
func (m *Materializer) collisionRoots(repositories []repomanifest.Entry, workdir string) []string {
	roots := []string{m.BundledSkillsPath}
	bases := []string{workdir}
	for _, repository := range repositories {
		bases = append(bases, repository.Path)
	}
	if home, err := os.UserHomeDir(); err == nil {
		bases = append(bases, home)
	}

	destination := filepath.Clean(m.Destination)
	seen := map[string]bool{}
	for _, base := range bases {
		if base == "" {
			continue
		}
		for _, relative := range discoveryPaths {
			root := filepath.Join(base, relative)
			if root == destination || seen[root] {
				continue
			}
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}

// skillNames returns the names a discovered skill directory answers to: its
// directory name plus the name declared in its SKILL.md frontmatter. An
// unreadable or malformed SKILL.md contributes nothing rather than failing boot.
func skillNames(skillDir string) map[string]bool {
	names := map[string]bool{filepath.Base(skillDir): true}
	path := filepath.Join(skillDir, "SKILL.md")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return names
	}
	file, err := os.Open(path)
	if err != nil {
		return names
	}
	defer func() { _ = file.Close() }()
	// The frontmatter is at the top; a bounded read keeps a huge file cheap.
	buffer, err := io.ReadAll(io.LimitReader(file, 65536))
	if err != nil && len(buffer) == 0 {
		return names
	}
	content := string(buffer)
	if !strings.HasPrefix(content, "---\n") {
		return names
	}
	for _, line := range splitLines(content)[1:] {
		if line == "---" {
			break
		}
		if !utf8.ValidString(line) {
			break // undecodable frontmatter: keep the directory name only
		}
		if match := yamlNameRE.FindStringSubmatch(line); match != nil {
			if name := firstNonEmpty(match[1:]); skillNameRE.MatchString(name) {
				names[name] = true
			}
			break
		}
	}
	return names
}

// install replaces the complete managed tree using a recoverable
// same-filesystem swap: stage a fresh tree, mark the swap durable, move the
// current tree aside, rename staging into place, then drop the backup. The
// journal must be durable before the destination moves, so an interrupted swap
// is always recoverable on the next boot.
func (m *Materializer) install(installation *Installation) error {
	parent := filepath.Dir(m.Destination)
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return err
	}
	staging := filepath.Join(parent, stagingName)
	backup := filepath.Join(parent, backupName)
	journal := filepath.Join(parent, journalName)
	if err := m.repairInterruptedSwap(staging, backup, journal); err != nil {
		return err
	}
	if info, err := os.Lstat(m.Destination); err == nil && !info.IsDir() {
		return newError("install_failed", "managed skills destination is not a directory")
	}

	if err := os.Mkdir(staging, 0o700); err != nil {
		return err
	}
	if err := m.swapIn(installation, staging, backup, journal, parent); err != nil {
		// Put the previous tree back if the destination is gone, then clear the
		// half-finished state so the next boot starts clean.
		if !exists(m.Destination) && exists(backup) {
			_ = os.Rename(backup, m.Destination)
		}
		_ = removePath(staging)
		_ = os.Remove(journal)
		return err
	}
	return nil
}

// swapIn writes the staged tree and performs the rename dance. Its caller owns
// rollback.
func (m *Materializer) swapIn(installation *Installation, staging, backup, journal, parent string) error {
	// Deterministic order keeps the write sequence reproducible across boots.
	skills := slices.Clone(installation.Skills)
	slices.SortFunc(skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	for _, skill := range skills {
		skillDir := filepath.Join(staging, skill.Name)
		if err := os.Mkdir(skillDir, 0o700); err != nil {
			return err
		}
		files := slices.Clone(skill.Files)
		slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
		for _, file := range files {
			if err := writeSkillFile(filepath.Join(skillDir, filepath.FromSlash(file.Path)), file); err != nil {
				return err
			}
		}
	}
	if err := writeJournal(journal); err != nil {
		return err
	}
	if exists(m.Destination) {
		if err := os.Rename(m.Destination, backup); err != nil {
			return err
		}
		if err := fsyncDirectory(parent); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, m.Destination); err != nil {
		return err
	}
	if err := fsyncDirectory(parent); err != nil {
		return err
	}
	if err := removePath(backup); err != nil {
		return err
	}
	if err := os.Remove(journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	return fsyncDirectory(parent)
}

// repairInterruptedSwap restores the last complete tree, or finishes cleanup,
// after a swap that was cut short. With no journal there is nothing to recover,
// so leftovers are simply discarded.
func (m *Materializer) repairInterruptedSwap(staging, backup, journal string) error {
	if !exists(journal) {
		if err := removePath(staging); err != nil {
			return err
		}
		return removePath(backup)
	}
	// The destination existing means the new tree already landed; otherwise the
	// backup is the last complete tree and must go back.
	if exists(m.Destination) {
		if err := removePath(backup); err != nil {
			return err
		}
	} else if exists(backup) {
		if err := os.Rename(backup, m.Destination); err != nil {
			return err
		}
	}
	if err := removePath(staging); err != nil {
		return err
	}
	if err := os.Remove(journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	return fsyncDirectory(filepath.Dir(m.Destination))
}

// writeSkillFile creates one file exclusively (no follow, no overwrite), fsyncs
// it, and verifies the installed bytes against the validated digest before
// setting the final read-only (or read+execute) mode.
func writeSkillFile(path string, file File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o400)
	if file.Executable {
		mode = 0o500
	}
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	if _, err := handle.WriteString(file.Content); err != nil {
		return err
	}
	if err := handle.Sync(); err != nil {
		return err
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256Hex(installed) != file.SHA256 {
		return newError("install_failed", "installed SHA-256 mismatch for %s", file.Path)
	}
	// The create mode is subject to umask, so set the final mode explicitly.
	return handle.Chmod(mode)
}

// writeJournal creates the durable swap marker: write a temp file, fsync it,
// rename it into place, then fsync the directory so the marker survives a crash.
func writeJournal(journal string) error {
	directory := filepath.Dir(journal)
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(journal)+".*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, journal); err != nil {
		return err
	}
	return fsyncDirectory(directory)
}

// removePath deletes a real directory recursively, or unlinks anything else
// (including a symlink to a directory). A missing path is not an error.
func removePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// exists reports whether path is present without following a final symlink.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// fsyncDirectory flushes a directory's entries so a rename is durable.
func fsyncDirectory(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}
