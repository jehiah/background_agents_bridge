package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jehiah/background_agents_bridge/internal/config"
)

func TestOpencodeGlobalConfigDir(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit override", map[string]string{"OPENCODE_CONFIG_DIR": "/custom"}, "/custom"},
		{"xdg base", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg/opencode"},
		{"home default", map[string]string{"HOME": "/home/agent"}, "/home/agent/.config/opencode"},
		{"override wins over xdg", map[string]string{
			"OPENCODE_CONFIG_DIR": "/custom",
			"XDG_CONFIG_HOME":     "/xdg",
		}, "/custom"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OPENCODE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("HOME", "/home/agent")
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if got := opencodeGlobalConfigDir(); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestMaterializeManagedSkillsSkipsWithoutEndpoint(t *testing.T) {
	// No control-plane URL or session id means managed skills are not enabled
	// for this deployment; boot proceeds and nothing is written.
	root := t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", root)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	for _, cfg := range []config.Resolved{
		{SessionID: "session-1"},
		{ControlPlaneURL: "https://control.example"},
	} {
		if err := materializeManagedSkills(t.Context(), cfg, root, logger); err != nil {
			t.Fatalf("materialize with %+v: %v", cfg, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "skills")); !os.IsNotExist(err) {
		t.Fatalf("skills directory created: %v", err)
	}
}
