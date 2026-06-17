package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/bridge"
	"github.com/jehiah/background_agents_bridge/internal/config"
)

// connectFlags adds the flags shared by connect-opencode and run-opencode.
func connectFlags(fs *flag.FlagSet, f *config.Flags) {
	fs.StringVar(&f.SandboxID, "sandbox-id", "", "Sandbox ID")
	fs.StringVar(&f.SessionID, "session-id", "", "Session ID for WebSocket connection")
	fs.StringVar(&f.ControlPlaneURL, "control-plane-url", "", "Control plane URL")
	fs.StringVar(&f.AuthToken, "sandbox-auth-token", "", "Bearer auth token for the control-plane WebSocket")
	fs.IntVar(&f.OpencodePort, "opencode-port", 0, "OpenCode port (default 4096)")
	fs.DurationVar(&f.OpencodeWait, "opencode-wait", 30*time.Second,
		"duration to wait for the opencode TCP port to accept connections before starting the bridge")
}

// requireConnectFields reports the missing required fields to stderr and
// returns false if any are missing.
func requireConnectFields(fs *flag.FlagSet, cfg config.Resolved) bool {
	missing := cfg.Missing("sandbox_id", "session_id", "control_plane_url", "sandbox_auth_token")
	if len(missing) == 0 {
		return true
	}
	for i, m := range missing {
		missing[i] = "--" + m
	}
	fmt.Fprintf(os.Stderr, "missing required flags: %s\n", strings.Join(missing, ", "))
	fs.Usage()
	return false
}

// connectLogger builds the structured logger used by the bridge component.
func connectLogger(cfg config.Resolved) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})).With(
		"service", "sandbox",
		"component", "bridge",
		"sandbox_id", cfg.SandboxID,
		"session_id", cfg.SessionID,
	)
}

// runBridge waits for the opencode port to accept connections (up to waitDuration)
// and then runs the WebSocket bridge to completion. host is the address used
// for the readiness probe; pass "" for the default 127.0.0.1 (preferred over
// "localhost" to avoid a DNS lookup, and matches where the bridge dials).
func runBridge(ctx context.Context, cfg config.Resolved, host string, logger *slog.Logger) error {
	if cfg.OpencodeWait > 0 {
		if host == "" {
			host = "127.0.0.1"
		}
		if err := waitForOpencodePort(ctx, host, cfg.OpencodePort, cfg.OpencodeWait, logger); err != nil {
			return err
		}
	}
	b := bridge.New(cfg.SandboxID, cfg.SessionID, cfg.ControlPlaneURL, cfg.AuthToken, cfg.OpencodePort, logger)
	return b.Run(ctx)
}

// waitForOpencodePort polls a TCP connect to host:port until it succeeds, the
// context is cancelled, or the timeout elapses.
func waitForOpencodePort(ctx context.Context, host string, port int, timeout time.Duration, logger *slog.Logger) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	logger.Info("opencode.wait", "addr", addr, "timeout_sec", timeout.Seconds())
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			logger.Info("opencode.wait.ready", "addr", addr)
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("opencode port %d not reachable after %s: %w", port, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
