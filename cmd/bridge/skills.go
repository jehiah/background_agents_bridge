package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
	"github.com/jehiah/background_agents_bridge/internal/skills"
)

// materializeManagedSkills installs the session's control-plane-managed skills
// before opencode starts, so opencode discovers a complete, verified tree and
// later restarts never re-fetch. Port of the supervisor's
// `await self.managed_skills.materialize(...)` step plus its composition in
// entrypoint.build_supervisor.
//
// Managed skills are opt-in per deployment: with no control-plane URL or session
// id there is no endpoint to ask, and boot proceeds without them. Any other
// failure is fatal — opencode must not run against a partially installed or
// unverified skills tree.
func materializeManagedSkills(ctx context.Context, cfg config.Resolved, workDir string, logger *slog.Logger) error {
	if cfg.ControlPlaneURL == "" || cfg.SessionID == "" {
		logger.Info("managed_skills.skipped", "reason", "endpoint_not_configured")
		return nil
	}
	destination := filepath.Join(opencodeGlobalConfigDir(), "skills")
	client := skills.NewClient(cfg.ControlPlaneURL, cfg.SessionID, cfg.AuthToken)
	materializer := skills.NewMaterializer(client, destination, logger)
	return materializer.Materialize(ctx, repomanifest.Load(repomanifest.ManifestPath), workDir)
}

// opencodeGlobalConfigDir resolves OpenCode's global config directory using its
// xdg-basedir rules: $OPENCODE_CONFIG_DIR wins, then $XDG_CONFIG_HOME/opencode,
// then ~/.config/opencode.
func opencodeGlobalConfigDir() string {
	if override := os.Getenv("OPENCODE_CONFIG_DIR"); override != "" {
		return override
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "opencode")
}
