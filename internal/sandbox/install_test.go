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

	Install(discardLogger())

	// Tools were written.
	toolsPath := filepath.Join(home, ".config", "opencode", "tools")
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
