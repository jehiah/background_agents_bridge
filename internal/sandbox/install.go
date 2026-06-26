package sandbox

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// ghWrapperTemplate is the `gh` wrapper script with {{ .Exe }} substituted for
// the bridge binary path. Kept as a real .sh file so editors lint it; it is not
// run as-is.
//
//go:embed gh_wrapper.sh
var ghWrapperTemplate string

// gitConfigTimeout bounds each `git config` invocation during install.
const gitConfigTimeout = 10 * time.Second

// Install wires the bridge into the sandbox so OpenCode and git use it. It is
// run on `connect-opencode` startup (and exposed as `bridge install`):
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
	// The OpenCode tool shim spawns the bridge by this path from an arbitrary
	// working directory, so it must be absolute. os.Executable does not always
	// guarantee that; resolve it and bail if we can't.
	exe, err = absExe(exe)
	if err != nil {
		log.Error("install.executable_resolve_error", "exc", err)
		return
	}

	installCredentialHelper(log, exe)
	installGHWrapper(log, exe)
	installTools(log, exe)
}

// installGHWrapper drops a `gh` shell wrapper into the first directory on $PATH
// so that invoking `gh` silently provisions a GitHub token. The wrapper asks the
// bridge for a token (`<exe> git-credential gh-token`), exports it as
// GITHUB_TOKEN, removes its own directory from $PATH so `command -v gh` resolves
// the real binary, and execs it.
func installGHWrapper(log *slog.Logger, exe string) {
	dir := firstPathDir()
	if dir == "" {
		log.Warn("install.gh_wrapper_no_path")
		return
	}
	script, err := ghWrapperScript(exe)
	if err != nil {
		log.Error("install.gh_wrapper_render_error", "exc", err)
		return
	}
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		log.Error("install.gh_wrapper_error", "exc", err, "path", path)
		return
	}
	log.Info("install.gh_wrapper", "path", path)
}

// firstPathDir returns the first non-empty entry in $PATH, or "" if PATH is
// unset/empty.
func firstPathDir() string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" {
			return dir
		}
	}
	return ""
}

// ghWrapperScript renders the `gh` wrapper from gh_wrapper.sh. exe must be an
// absolute path to the bridge binary. The path is substituted unquoted, so a
// path containing whitespace is rejected rather than producing a broken script.
func ghWrapperScript(exe string) (string, error) {
	if strings.ContainsAny(exe, " \t\n") {
		return "", fmt.Errorf("bridge path %q contains whitespace", exe)
	}
	tmpl, err := template.New("gh_wrapper").Parse(ghWrapperTemplate)
	if err != nil {
		return "", fmt.Errorf("parse gh wrapper template: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, struct{ Exe string }{exe}); err != nil {
		return "", fmt.Errorf("render gh wrapper: %w", err)
	}
	return b.String(), nil
}

// absExe returns an absolute path to the bridge executable. os.Executable may
// return a relative path or a bare command name (resolved via $PATH); the tool
// shim needs a fully-qualified path because it spawns the bridge from a
// different working directory.
func absExe(exe string) (string, error) {
	if filepath.IsAbs(exe) {
		return exe, nil
	}
	// A bare name (no separator) must be looked up on $PATH; a relative path
	// (e.g. "./bridge") is found directly by LookPath, then made absolute.
	resolved, err := exec.LookPath(exe)
	if err != nil {
		return "", fmt.Errorf("resolve bridge executable %q: %w", exe, err)
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve bridge executable %q: %w", exe, err)
	}
	return abs, nil
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

	for _, name := range toolDefNames() {
		js, err := generateToolJS(name, exe)
		if err != nil {
			log.Error("install.tool_render_error", "exc", err, "tool", name)
			continue
		}
		path := filepath.Join(dir, fileNameFor(name))
		if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
			log.Error("install.tool_write_error", "exc", err, "tool", name, "path", path)
			continue
		}
		log.Info("install.tool", "tool", name, "path", path)
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
