package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// gitConfigTimeout bounds each `git config` invocation during install.
const gitConfigTimeout = 10 * time.Second

// Install wires the bridge into the sandbox so OpenCode and git use it. It is
// run on `connect` startup (and exposed as `bridge install`):
//
//  1. registers the bridge as git's credential helper, replacing the standalone
//     shell helper — credentials are brokered fresh per git op, never cached;
//  2. writes the OpenCode tool definitions into ~/.config/opencode/tools/.
//
// Every step is best-effort: failures are logged but do not stop the bridge from
// connecting.
func Install(log *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		log.Error("install.executable_path_error", "exc", err)
		return
	}

	installCredentialHelper(log, exe)
	installTools(log, exe)
}

// installCredentialHelper points git's credential.helper at `<exe> git-credential`.
// The leading "!" makes git treat the value as a shell command and append the
// operation (get/store/erase) as the final argument. System scope is preferred
// (matches the previous provisioner setup); on failure it falls back to global.
func installCredentialHelper(log *slog.Logger, exe string) {
	helper := "!" + exe + " git-credential"

	if err := gitConfig("--system", "credential.helper", helper); err == nil {
		log.Info("install.credential_helper", "scope", "system")
		return
	} else { //nolint:revive // log the system failure before falling back to global
		log.Warn("install.credential_helper_system_failed", "detail", err.Error())
	}
	if err := gitConfig("--global", "credential.helper", helper); err != nil {
		log.Error("install.credential_helper_error", "exc", err)
		return
	}
	log.Info("install.credential_helper", "scope", "global")
}

func gitConfig(scope, key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitConfigTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", scope, key, value)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git config %s %s: %v: %s", scope, key, err, out)
	}
	return nil
}

// installTools writes a generated .js for each registered tool into the OpenCode
// global tools directory (~/.config/opencode/tools/).
func installTools(log *slog.Logger, exe string) {
	dir, err := toolsDir()
	if err != nil {
		log.Error("install.tools_dir_error", "exc", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Error("install.tools_mkdir_error", "exc", err, "dir", dir)
		return
	}

	for _, def := range toolDefs {
		path := filepath.Join(dir, fileNameFor(def.name))
		if err := os.WriteFile(path, []byte(generateToolJS(def, exe)), 0o644); err != nil {
			log.Error("install.tool_write_error", "exc", err, "tool", def.name, "path", path)
			continue
		}
		log.Info("install.tool", "tool", def.name, "path", path)
	}
}

// toolsDir resolves ~/.config/opencode/tools, honoring $HOME.
func toolsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "tools"), nil
}
