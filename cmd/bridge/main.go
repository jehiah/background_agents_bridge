// Command bridge is the sandbox side of background-agents: everything in the
// sandbox that talks to the control plane. It runs in one of several modes:
//
//	bridge connect [flags]              connect a local OpenCode to the control
//	                                    plane over a WebSocket (the long-running
//	                                    service) and self-install the helpers below
//	bridge git-credential <get|...>     git credential helper (brokers SCM tokens)
//	bridge tool <name>                  execute one OpenCode tool (args JSON on
//	                                    stdin, result on stdout)
//	bridge install                      self-install the credential helper + tools
//
// A subcommand is always required.
//
// The connect mode is a Go port of the upstream Python bridge.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jehiah/background_agents_bridge/internal/bridge"
	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/sandbox"
)

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
		fmt.Fprintln(os.Stderr, "usage: bridge <connect|git-credential|tool|install> [flags] ...")
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
	case "connect":
		runConnect(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, "usage: bridge <connect|git-credential|tool|install> [flags] ...")
		os.Exit(2)
	}
}

// runConnect runs the long-lived control-plane bridge, after self-installing the
// git credential helper and OpenCode tools.
func runConnect(argv []string) {
	fs := newFlagSet("connect")
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
