package sandbox

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestInstall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	// Redirect git's system + global config to temp files so the test never
	// touches the real machine config (git honors these env overrides).
	sysCfg := filepath.Join(home, "system.gitconfig")
	globCfg := filepath.Join(home, "global.gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", sysCfg)
	t.Setenv("GIT_CONFIG_GLOBAL", globCfg)

	// A tool this bridge wrote under its old name, plus a file it did not write.
	toolsPath := filepath.Join(home, ".config", "opencode", "tools")
	if err := os.MkdirAll(toolsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(toolsPath, "spawn-task.js")
	foreign := filepath.Join(toolsPath, "someone-elses.js")
	if err := os.WriteFile(renamed, []byte(generatedHeader+"// stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("// hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	Install(discardLogger())

	// The renamed tool is gone — left in place it would advertise a name
	// `bridge tool` no longer answers to — and the foreign file survives.
	if _, err := os.Stat(renamed); !os.IsNotExist(err) {
		t.Errorf("stale tool file survived install (%v)", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("install removed a file it did not write: %v", err)
	}

	// Tools were written.
	for _, name := range ToolNames() {
		p := filepath.Join(toolsPath, name+".js")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected tool file %s: %v", p, err)
		}
	}

	// Credential helper was registered and points back at this binary.
	out, err := exec.Command("git", "config", "--system", "--get", "credential.helper").Output()
	if err != nil {
		t.Fatalf("git config get: %v", err)
	}
	helper := strings.TrimSpace(string(out))
	exe, _ := os.Executable()
	want := "!" + exe + " git-credential"
	if helper != want {
		t.Fatalf("credential.helper = %q, want %q", helper, want)
	}
}
