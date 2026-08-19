package skills

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// stubClient serves fixed installation bytes (or a fixed error).
type stubClient struct {
	raw []byte
	err error
}

func (c stubClient) FetchInstallation(context.Context) ([]byte, error) { return c.raw, c.err }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestMaterializer wires a materializer over a document, pointing the bundled
// tree at bundled (pass a missing path when collisions are not under test).
func newTestMaterializer(t *testing.T, document map[string]any, destination, bundled string) *Materializer {
	t.Helper()
	materializer := NewMaterializer(stubClient{raw: encode(t, document)}, destination, discardLogger())
	materializer.BundledSkillsPath = bundled
	return materializer
}

func writeSkillDir(t *testing.T, dir, frontmatter string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(frontmatter), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestMaterializeReplacesDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "config", "opencode", "skills")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	materializer := newTestMaterializer(t, installationDocument("managed", "SKILL.md", ""),
		destination, filepath.Join(root, "missing-bundled"))

	if err := materializer.Materialize(t.Context(), nil, filepath.Join(root, "workspace")); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(destination, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file survived the swap: %v", err)
	}
	installed := filepath.Join(destination, "managed", "SKILL.md")
	content, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if want := "name: managed"; !strings.Contains(string(content), want) {
		t.Fatalf("installed content %q missing %q", content, want)
	}
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("stat installed skill: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o400 {
		t.Fatalf("got mode %o, want 400", mode)
	}
	// No swap state should be left behind for the next boot to repair.
	for _, name := range []string{stagingName, backupName, journalName} {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(destination), name)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", name, err)
		}
	}
}

func TestMaterializeInstallsExecutableScriptsAsReadExecute(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "skills")
	document := installationDocument("managed", "SKILL.md", "")
	script := hashedFileEntry("scripts/run.sh", "#!/bin/sh\necho hi\n")
	script["executable"] = true
	setSkillFiles(document, append(skillFiles(document), script))
	materializer := newTestMaterializer(t, document, destination, filepath.Join(root, "missing-bundled"))

	if err := materializer.Materialize(t.Context(), nil, root); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	info, err := os.Stat(filepath.Join(destination, "managed", "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o500 {
		t.Fatalf("got mode %o, want 500", mode)
	}
}

func TestMaterializeRejectsBundledNameCollision(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled")
	writeSkillDir(t, filepath.Join(bundled, "conflict"), "---\nname: conflict\n---\n")
	destination := filepath.Join(root, "global", "skills")
	materializer := newTestMaterializer(t, installationDocument("conflict", "SKILL.md", ""), destination, bundled)

	err := materializer.Materialize(t.Context(), nil, filepath.Join(root, "workspace"))
	if ErrorCode(err) != "name_collision" {
		t.Fatalf("got %v (code %q), want name_collision", err, ErrorCode(err))
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination created despite the collision: %v", statErr)
	}
}

func TestMaterializeRejectsCollisionInRepositoryAndWorkdirDiscoveryPaths(t *testing.T) {
	for _, discovery := range discoveryPaths {
		t.Run(discovery, func(t *testing.T) {
			root := t.TempDir()
			repository := filepath.Join(root, "workspace", "repo")
			// The colliding directory is named differently; only the frontmatter
			// name matches, which is what OpenCode would resolve.
			writeSkillDir(t, filepath.Join(repository, discovery, "renamed"), "---\nname: managed\n---\n")
			materializer := newTestMaterializer(t, installationDocument("managed", "SKILL.md", ""),
				filepath.Join(root, "global", "skills"), filepath.Join(root, "missing-bundled"))

			repositories := []repomanifest.Entry{{Owner: "owner", Name: "repo", Path: repository}}
			err := materializer.Materialize(t.Context(), repositories, filepath.Join(root, "workspace"))
			if ErrorCode(err) != "name_collision" {
				t.Fatalf("got %v (code %q), want name_collision", err, ErrorCode(err))
			}
		})
	}
}

func TestMaterializeIgnoresInvalidUTF8DuringCollisionScan(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled")
	writeSkillDir(t, filepath.Join(bundled, "unrelated"), "")
	if err := os.WriteFile(filepath.Join(bundled, "unrelated", "SKILL.md"), []byte("---\nname: \xff\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	destination := filepath.Join(root, "global", "skills")
	materializer := newTestMaterializer(t, installationDocument("managed", "SKILL.md", ""), destination, bundled)

	if err := materializer.Materialize(t.Context(), nil, filepath.Join(root, "workspace")); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "managed", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
}

func TestMaterializeSkipsTheDestinationItselfWhenScanning(t *testing.T) {
	// The destination is a discovery root under $HOME in production; a stale
	// managed skill of the same name there must not block a reinstall.
	root := t.TempDir()
	t.Setenv("HOME", root)
	destination := filepath.Join(root, ".config", "opencode", "skills")
	writeSkillDir(t, filepath.Join(destination, "managed"), "---\nname: managed\n---\n")
	materializer := newTestMaterializer(t, installationDocument("managed", "SKILL.md", ""),
		destination, filepath.Join(root, "missing-bundled"))

	if err := materializer.Materialize(t.Context(), nil, filepath.Join(root, "workspace")); err != nil {
		t.Fatalf("materialize: %v", err)
	}
}

func TestMaterializePropagatesFetchAndValidationFailures(t *testing.T) {
	root := t.TempDir()
	materializer := NewMaterializer(
		stubClient{raw: []byte("not json")},
		filepath.Join(root, "skills"),
		discardLogger(),
	)
	materializer.BundledSkillsPath = filepath.Join(root, "missing-bundled")

	err := materializer.Materialize(t.Context(), nil, root)
	if ErrorCode(err) != "installation_invalid" {
		t.Fatalf("got %v (code %q), want installation_invalid", err, ErrorCode(err))
	}
}

func TestMaterializeRejectsNonDirectoryDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "skills")
	if err := os.WriteFile(destination, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write destination file: %v", err)
	}
	materializer := newTestMaterializer(t, installationDocument("managed", "SKILL.md", ""),
		destination, filepath.Join(root, "missing-bundled"))

	err := materializer.Materialize(t.Context(), nil, root)
	if ErrorCode(err) != "install_failed" {
		t.Fatalf("got %v (code %q), want install_failed", err, ErrorCode(err))
	}
}

func TestRepairInterruptedSwapRestoresBackup(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "skills")
	staging := filepath.Join(root, stagingName)
	backup := filepath.Join(root, backupName)
	journal := filepath.Join(root, journalName)
	mkdirs(t, staging, backup)
	writeFile(t, filepath.Join(backup, "previous"), "ok")
	writeFile(t, journal, "")
	materializer := NewMaterializer(stubClient{}, destination, discardLogger())

	if err := materializer.repairInterruptedSwap(staging, backup, journal); err != nil {
		t.Fatalf("repair: %v", err)
	}

	if got := readFile(t, filepath.Join(destination, "previous")); got != "ok" {
		t.Fatalf("got %q, want the backup contents restored", got)
	}
	assertMissing(t, staging, journal, backup)
}

func TestRepairInterruptedSwapAfterDestinationInstall(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "skills")
	staging := filepath.Join(root, stagingName)
	backup := filepath.Join(root, backupName)
	journal := filepath.Join(root, journalName)
	mkdirs(t, destination, staging, backup)
	writeFile(t, filepath.Join(destination, "current"), "new")
	writeFile(t, filepath.Join(backup, "previous"), "old")
	writeFile(t, journal, "")
	materializer := NewMaterializer(stubClient{}, destination, discardLogger())

	if err := materializer.repairInterruptedSwap(staging, backup, journal); err != nil {
		t.Fatalf("repair: %v", err)
	}

	if got := readFile(t, filepath.Join(destination, "current")); got != "new" {
		t.Fatalf("got %q, want the installed tree kept", got)
	}
	assertMissing(t, staging, journal, backup)
}

func TestRepairInterruptedSwapWithoutJournalDiscardsLeftovers(t *testing.T) {
	// No journal means no swap was in flight, so leftovers are stale scratch
	// state — not a tree anyone is waiting on.
	root := t.TempDir()
	destination := filepath.Join(root, "skills")
	staging := filepath.Join(root, stagingName)
	backup := filepath.Join(root, backupName)
	mkdirs(t, staging, backup)
	writeFile(t, filepath.Join(backup, "previous"), "old")
	materializer := NewMaterializer(stubClient{}, destination, discardLogger())

	if err := materializer.repairInterruptedSwap(staging, backup, filepath.Join(root, journalName)); err != nil {
		t.Fatalf("repair: %v", err)
	}

	assertMissing(t, staging, backup, destination)
}

func TestMaterializeRepairsBeforeInstalling(t *testing.T) {
	// A boot that follows an interrupted swap installs cleanly: the leftover
	// staging directory must not make os.Mkdir fail.
	root := t.TempDir()
	destination := filepath.Join(root, "skills")
	mkdirs(t, filepath.Join(root, stagingName), filepath.Join(root, backupName))
	writeFile(t, filepath.Join(root, backupName, "previous"), "old")
	writeFile(t, filepath.Join(root, journalName), "")
	materializer := newTestMaterializer(t, installationDocument("managed", "SKILL.md", ""),
		destination, filepath.Join(root, "missing-bundled"))

	if err := materializer.Materialize(t.Context(), nil, root); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destination, "managed", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	if got := filepath.Join(destination, "previous"); exists(got) {
		t.Fatalf("restored backup survived the new install at %s", got)
	}
}

func TestSkillNamesIncludesDirectoryAndFrontmatterName(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "directory-name")
	writeSkillDir(t, skillDir, "---\ndescription: x\nname: frontmatter-name\n---\n")

	names := skillNames(skillDir)
	for _, want := range []string{"directory-name", "frontmatter-name"} {
		if !names[want] {
			t.Fatalf("got %v, want %q included", names, want)
		}
	}
}

func TestSkillNamesIgnoresMissingOrMalformedFrontmatter(t *testing.T) {
	root := t.TempDir()
	tests := map[string]string{
		"no-skill-file":  "",
		"no-frontmatter": "# heading\nname: other\n",
		"unclosed-name":  "---\n---\nname: other\n",
		"bad-name":       "---\nname: Not A Skill Name\n---\n",
	}
	for dir, content := range tests {
		t.Run(dir, func(t *testing.T) {
			skillDir := filepath.Join(root, dir)
			if content == "" {
				mkdirs(t, skillDir)
			} else {
				writeSkillDir(t, skillDir, content)
			}
			names := skillNames(skillDir)
			if len(names) != 1 || !names[dir] {
				t.Fatalf("got %v, want only the directory name", names)
			}
		})
	}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func assertMissing(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
}
