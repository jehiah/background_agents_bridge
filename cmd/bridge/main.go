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
	"strconv"
	"strings"
	"syscall"

	"github.com/jehiah/background_agents_bridge/internal/bridge"
	"github.com/jehiah/background_agents_bridge/internal/gcpmeta"
)

// defaultOpencodePort is used when neither the flag nor metadata supply a port.
const defaultOpencodePort = 4096

func main() {
	sandboxID := flag.String("sandbox-id", "", "Sandbox ID")
	sessionID := flag.String("session-id", "", "Session ID for WebSocket connection")
	controlPlane := flag.String("control-plane", "", "Control plane URL")
	token := flag.String("control-plane-token", "", "Bearer auth token for the control-plane WebSocket")
	opencodePort := flag.Int("opencode-port", 0, "OpenCode port (default 4096)")
	flag.Parse()

	// Empty flags fall back to GCE instance attributes of the same name.
	resolveFromMetadata(map[string]*string{
		"sandbox-id":          sandboxID,
		"session-id":          sessionID,
		"control-plane":       controlPlane,
		"control-plane-token": token,
	}, opencodePort)

	var missing []string
	for name, v := range map[string]string{
		"--sandbox-id":          *sandboxID,
		"--session-id":          *sessionID,
		"--control-plane":       *controlPlane,
		"--control-plane-token": *token,
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

// resolveFromMetadata fills any empty string flag and a zero port from GCE
// instance attributes keyed by flag name. It probes the metadata server only
// when something is missing, stops on the first transport error (returning
// promptly when not running on GCE), and treats absent attributes as unset.
func resolveFromMetadata(stringFlags map[string]*string, opencodePort *int) {
	anyEmpty := *opencodePort == 0
	for _, p := range stringFlags {
		if *p == "" {
			anyEmpty = true
		}
	}

	if anyEmpty {
		mc := gcpmeta.NewClient()
		ctx := context.Background()

		reachable := true
		for key, p := range stringFlags {
			if *p != "" {
				continue
			}
			v, err := mc.InstanceAttribute(ctx, key)
			if err != nil {
				fmt.Fprintf(os.Stderr, "metadata lookup %q failed: %v\n", key, err)
				reachable = false
				break // likely not on GCE; stop probing
			}
			if v != "" {
				*p = v
			}
		}

		if reachable && *opencodePort == 0 {
			if v, err := mc.InstanceAttribute(ctx, "opencode-port"); err == nil && v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					*opencodePort = n
				}
			}
		}
	}

	if *opencodePort == 0 {
		*opencodePort = defaultOpencodePort
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
