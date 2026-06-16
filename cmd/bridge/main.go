// Command bridge is the sandbox side of background-agents: everything in the
// sandbox that talks to the control plane. It runs in one of several modes:
//
//	bridge connect-opencode [flags]     connect a local OpenCode to the control
//	                                    plane over a WebSocket (the long-running
//	                                    service); on startup it self-installs the
//	                                    git-credential and tool helpers below
//	bridge run-opencode [flags]         run `opencode serve` as a subprocess,
//	                                    streaming its stdout/stderr through
//	bridge git-credential <get|...>     git credential helper (brokers SCM tokens)
//	bridge tool <name>                  execute one OpenCode tool (args JSON on
//	                                    stdin, result on stdout)
//	bridge install                      self-install the credential helper + tools
//
// A subcommand is always required.
//
// The connect-opencode mode is a Go port of the upstream Python bridge.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/bridge"
	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/sandbox"
)

const usageLine = "usage: bridge <connect-opencode|run-opencode|git-credential|tool|install> [flags] ..."

// defaultOpencodeConfig is used when OPENCODE_CONFIG_CONTENT is unset: blanket
// allow, no MCP servers. Provisioners normally supply a richer config.
const defaultOpencodeConfig = `{"permission":{"*":"allow"}}`

// newFlagSet builds a flag set that exits on error and prints usage under the
// given subcommand name.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: bridge %s [flags]\n", name)
		fs.PrintDefaults()
	}
	return fs
}

func main() {
	// Dispatch on the first argument, which must name a subcommand.
	args := os.Args[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, usageLine)
		os.Exit(2)
	}
	cmd := args[0]
	args = args[1:]

	switch cmd {
	case "git-credential":
		runGitCredential(args)
	case "tool":
		runTool(args)
	case "install":
		runInstall()
	case "connect-opencode":
		runConnect(args)
	case "run-opencode":
		runOpencode(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, usageLine)
		os.Exit(2)
	}
}

// runConnect runs the long-lived control-plane bridge, after self-installing the
// git credential helper and OpenCode tools.
func runConnect(argv []string) {
	fs := newFlagSet("connect-opencode")
	var f config.Flags
	fs.StringVar(&f.SandboxID, "sandbox-id", "", "Sandbox ID")
	fs.StringVar(&f.SessionID, "session-id", "", "Session ID for WebSocket connection")
	fs.StringVar(&f.ControlPlaneURL, "control-plane-url", "", "Control plane URL")
	fs.StringVar(&f.AuthToken, "sandbox-auth-token", "", "Bearer auth token for the control-plane WebSocket")
	fs.IntVar(&f.OpencodePort, "opencode-port", 0, "OpenCode port (default 4096)")
	_ = fs.Parse(argv)

	cfg := config.Resolve(f)

	if missing := cfg.Missing("sandbox_id", "session_id", "control_plane_url", "sandbox_auth_token"); len(missing) > 0 {
		for i, m := range missing {
			missing[i] = "--" + m
		}
		fmt.Fprintf(os.Stderr, "missing required flags: %s\n", strings.Join(missing, ", "))
		fs.Usage()
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel(),
	})).With(
		"service", "sandbox",
		"component", "bridge",
		"sandbox_id", cfg.SandboxID,
		"session_id", cfg.SessionID,
	)

	// Self-install the credential helper + tools (best effort; logged inside).
	sandbox.Install(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b := bridge.New(cfg.SandboxID, cfg.SessionID, cfg.ControlPlaneURL, cfg.AuthToken, cfg.OpencodePort, logger)
	if err := b.Run(ctx); err != nil {
		logger.Error("bridge.exit", "exc", err)
		os.Exit(1)
	}
}

// runOpencode launches `opencode serve` as a child process, streaming its
// stdout/stderr through ours. It replaces the standalone opencode.service so a
// provisioner only manages one long-lived process (the bridge), while still
// giving operators direct visibility into opencode's logs.
//
// Configuration:
//   - port: --opencode-port flag, else OPENCODE_PORT env / metadata, else 4096.
//   - workdir: --workdir flag, else /workspace/$REPO_NAME, else /workspace.
//   - OPENCODE_CONFIG_CONTENT: passed through from the environment; if unset, a
//     default `{"permission":{"*":"allow"}}` is supplied so opencode still boots.
//   - OPENCODE_CLIENT=serve is asserted to identify this client to opencode.
func runOpencode(argv []string) {
	fs := newFlagSet("run-opencode")
	var f config.Flags
	fs.IntVar(&f.OpencodePort, "opencode-port", 0, "OpenCode port (default 4096)")
	var workDir string
	fs.StringVar(&workDir, "workdir", "", "working directory for opencode (default /workspace/$REPO_NAME, or /workspace)")
	var opencodeBin string
	fs.StringVar(&opencodeBin, "opencode-bin", "opencode", "path to the opencode binary")
	var hostname string
	fs.StringVar(&hostname, "hostname", "127.0.0.1", "hostname opencode binds to")
	_ = fs.Parse(argv)

	cfg := config.Resolve(f)

	if workDir == "" {
		workDir = "/workspace"
		if repo := os.Getenv("REPO_NAME"); repo != "" {
			candidate := "/workspace/" + repo
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				workDir = candidate
			}
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})).With(
		"service", "sandbox", "component", "opencode",
	)

	env := os.Environ()
	if os.Getenv("OPENCODE_CONFIG_CONTENT") == "" {
		env = append(env, "OPENCODE_CONFIG_CONTENT="+defaultOpencodeConfig)
		logger.Info("opencode.config.default")
	}
	env = append(env, "OPENCODE_CLIENT=serve")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	// A non-nil err after the context is cancelled (we sent SIGTERM) is a
	// graceful shutdown, not a failure — exit 0 so systemd/supervisors don't
	// flag it as a crash.
	if err != nil && ctx.Err() == nil {
		logger.Error("opencode.exit", "exc", err)
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
	logger.Info("opencode.exit", "code", 0)
}

// runGitCredential serves the git credential-helper protocol. git passes the
// operation (get/store/erase) as the final argument.
func runGitCredential(argv []string) {
	op := ""
	if len(argv) > 0 {
		op = argv[0]
	}
	if err := sandbox.GitCredential(op, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "git-credential: %v\n", err)
		os.Exit(1)
	}
}

// runTool executes a single OpenCode tool call.
func runTool(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: bridge tool <name>")
		os.Exit(2)
	}
	if err := sandbox.RunTool(argv[0], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "tool: %v\n", err)
		os.Exit(1)
	}
}

// runInstall performs the self-install without connecting (useful for testing /
// provisioning).
func runInstall() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})).With(
		"service", "sandbox", "component", "bridge",
	)
	sandbox.Install(logger)
}

// logLevel reads BRIDGE_LOG_LEVEL (debug|info|warn|error), defaulting to info.
func logLevel() slog.Level {
	switch strings.ToLower(os.Getenv("BRIDGE_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
