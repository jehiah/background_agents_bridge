// Command bridge is the sandbox side of background-agents: everything in the
// sandbox that talks to the control plane. It runs in one of several modes:
//
//	bridge connect-opencode [flags]     connect a local OpenCode to the control
//	                                    plane over a WebSocket (the long-running
//	                                    service); on startup it self-installs the
//	                                    git-credential and tool helpers below and
//	                                    waits for the opencode TCP port to accept
//	                                    connections before dialing the bridge
//	bridge run-opencode [flags]         run `opencode serve` as a subprocess and
//	                                    then chain into connect-opencode so a
//	                                    single command supervises both
//	bridge git-credential <get|...>     git credential helper (brokers SCM tokens)
//	bridge tool <name>                  execute one OpenCode tool (args JSON on
//	                                    stdin, result on stdout)
//	bridge install                      self-install the credential helper + tools
//
// A subcommand is always required.
//
// The connect-opencode mode is a Go port of the upstream Python bridge.
//
// main.go is just arg parsing and dispatch — the actual work lives in
// connect.go (the WebSocket bridge), opencode.go (the opencode subprocess),
// and sandbox.go (the short-lived sandbox-side helpers).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/sandbox"
	"golang.org/x/sync/errgroup"
)

const usageLine = "usage: bridge <connect-opencode|run-opencode|git-credential|tool|install> [flags] ..."

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
		runRunOpencode(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, usageLine)
		os.Exit(2)
	}
}

// runConnect parses connect-opencode flags and hands off to runBridge.
func runConnect(argv []string) {
	fs := newFlagSet("connect-opencode")
	var f config.Flags
	connectFlags(fs, &f)
	_ = fs.Parse(argv)

	cfg := config.Resolve(f)
	if !requireConnectFields(fs, cfg) {
		os.Exit(2)
	}

	logger := connectLogger(cfg)
	sandbox.Install(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runBridge(ctx, cfg, "", logger); err != nil {
		logger.Error("bridge.exit", "exc", err)
		os.Exit(1)
	}
}

// runRunOpencode parses run-opencode flags and runs opencode + the bridge
// concurrently. Either side exiting cancels the other.
func runRunOpencode(argv []string) {
	fs := newFlagSet("run-opencode")
	var f config.Flags
	connectFlags(fs, &f)
	var workDir, opencodeBin, hostname string
	fs.StringVar(&workDir, "workdir", "", "working directory for opencode (default /workspace/$REPO_NAME, or /workspace)")
	fs.StringVar(&opencodeBin, "opencode-bin", "opencode", "path to the opencode binary")
	fs.StringVar(&hostname, "hostname", "127.0.0.1", "hostname opencode binds to")
	_ = fs.Parse(argv)

	cfg := config.Resolve(f)
	if !requireConnectFields(fs, cfg) {
		os.Exit(2)
	}
	if workDir == "" {
		workDir = defaultWorkdir()
	}

	opencodeLog := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})).With(
		"service", "sandbox", "component", "opencode",
	)
	bridgeLog := connectLogger(cfg)
	sandbox.Install(bridgeLog)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// errgroup cancels its context as soon as either side returns (with or
	// without an error), so the survivor unwinds promptly. Wait() returns the
	// first non-nil error.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return runOpencodeProcess(gctx, cfg, workDir, opencodeBin, hostname, opencodeLog)
	})
	g.Go(func() error {
		return runBridge(gctx, cfg, hostname, bridgeLog)
	})

	// A context.Canceled bubbling up from gctx is a graceful shutdown (signal
	// or the other goroutine exiting cleanly), not a failure.
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
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
