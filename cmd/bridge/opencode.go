package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/config"
)

// defaultOpencodeConfig is used when OPENCODE_CONFIG_CONTENT is unset: blanket
// allow, no MCP servers. Provisioners normally supply a richer config.
const defaultOpencodeConfig = `{"permission":{"*":"allow"}}`

// defaultWorkdir picks the working directory for opencode when --workdir is
// not given: /workspace/$REPO_NAME if it exists, otherwise /workspace.
func defaultWorkdir() string {
	if repo := os.Getenv("REPO_NAME"); repo != "" {
		candidate := "/workspace/" + repo
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	return "/workspace"
}

// runOpencodeProcess launches `opencode serve` and waits for it to exit.
// Configuration:
//   - port: cfg.OpencodePort (already resolved from flag / env / metadata).
//   - workdir: caller-supplied (defaults set by runRunOpencode).
//   - OPENCODE_CONFIG_CONTENT: passed through from the environment; if unset, a
//     default `{"permission":{"*":"allow"}}` is supplied so opencode still boots.
//   - OPENCODE_CLIENT=serve is asserted to identify this client to opencode.
func runOpencodeProcess(ctx context.Context, cfg config.Resolved, workDir, opencodeBin, hostname string, logger *slog.Logger) error {
	env := os.Environ()
	if os.Getenv("OPENCODE_CONFIG_CONTENT") == "" {
		env = append(env, "OPENCODE_CONFIG_CONTENT="+defaultOpencodeConfig)
		logger.Info("opencode.config.default")
	}
	env = append(env, "OPENCODE_CLIENT=serve")

	cmd := exec.CommandContext(ctx, opencodeBin, "serve",
		"--print-logs",
		"--port", strconv.Itoa(cfg.OpencodePort),
		"--hostname", hostname,
	)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// On shutdown, send SIGTERM first and give opencode a moment to exit cleanly
	// before the context-cancel SIGKILL kicks in.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	logger.Info("opencode.start", "bin", opencodeBin, "port", cfg.OpencodePort, "workdir", workDir)
	err := cmd.Run()
	// A non-nil err after the context was cancelled is the expected SIGTERM
	// from cmd.Cancel — log it at info (not as a failure) and don't propagate.
	if err != nil && ctx.Err() != nil {
		logger.Info("opencode.exit", "exc", err)
		return nil
	}
	if err != nil {
		logger.Error("opencode.exit", "exc", err)
		return err
	}
	logger.Info("opencode.exit", "code", 0)
	return nil
}
