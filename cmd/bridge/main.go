// Command bridge is the sandbox-side agent bridge connecting a local OpenCode
// instance to the background-agents control plane. It is a Go port of the
// upstream Python bridge.
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
)

func main() {
	sandboxID := flag.String("sandbox-id", "", "Sandbox ID")
	sessionID := flag.String("session-id", "", "Session ID for WebSocket connection")
	controlPlane := flag.String("control-plane", "", "Control plane URL")
	token := flag.String("token", "", "Auth token")
	opencodePort := flag.Int("opencode-port", 4096, "OpenCode port")
	flag.Parse()

	var missing []string
	for name, v := range map[string]string{
		"--sandbox-id":    *sandboxID,
		"--session-id":    *sessionID,
		"--control-plane": *controlPlane,
		"--token":         *token,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing required flags: %s\n", strings.Join(missing, ", "))
		flag.Usage()
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel(),
	})).With(
		"service", "sandbox",
		"component", "bridge",
		"sandbox_id", *sandboxID,
		"session_id", *sessionID,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b := bridge.New(*sandboxID, *sessionID, *controlPlane, *token, *opencodePort, logger)
	if err := b.Run(ctx); err != nil {
		logger.Error("bridge.exit", "exc", err)
		os.Exit(1)
	}
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
