package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/coder/websocket"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// connectAndRun establishes a control-plane connection and processes commands
// until the connection closes or the context is cancelled. It returns
// errSessionTerminated for handshake rejections that must not be retried.
func (b *AgentBridge) connectAndRun(ctx context.Context) error {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+b.authToken)
	hdr.Set("X-Sandbox-ID", b.sandboxID)

	dialCtx, dialCancel := context.WithTimeout(ctx, httpConnectTimeout)
	conn, resp, err := websocket.Dial(dialCtx, b.wsURL(), &websocket.DialOptions{HTTPHeader: hdr})
	dialCancel()
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
				return fmt.Errorf("%w (HTTP %d)", errSessionTerminated, resp.StatusCode)
			}
		}
		return err
	}
	conn.SetReadLimit(wsReadLimit)

	b.setConn(conn)
	b.markConnected()
	// Backstop disconnect log for every exit path that did not already log one
	// (context cancellation, a read error that is not a close frame, a failure
	// while announcing readiness). Runs last, after the socket is closed.
	defer func() {
		reason := "connection_closed"
		if ctx.Err() != nil {
			reason = "shutdown_requested"
		}
		b.logDisconnect(reason, slog.LevelInfo)
	}()
	defer b.clearConn()
	defer func() { _ = conn.CloseNow() }()

	b.log.Info("bridge.connect", append([]any{"outcome", "success"}, b.aggregateFields()...)...)

	// Announce readiness, then replay anything buffered/unacked across the gap.
	b.sendEvent(readyEvent(b.getOpencodeSessionID(), repomanifest.Load(b.repoManifestPath)))
	b.drainBootWarnings()
	justFlushed := b.flushEventBuffer()
	b.flushPendingAcks(justFlushed)

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go b.heartbeatLoop(hbCtx)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if code := websocket.CloseStatus(err); code != -1 {
				level := slog.LevelWarn
				if code == websocket.StatusNormalClosure || code == websocket.StatusGoingAway {
					level = slog.LevelInfo
				}
				b.logDisconnect("connection_closed", level, "ws_close_code", int(code))
			}
			return err
		}
		var cmd command
		if err := json.Unmarshal(data, &cmd); err != nil {
			b.log.Warn("bridge.invalid_message", "exc", err)
			continue
		}
		b.handleCommand(ctx, &cmd)
	}
}

// drainBootWarnings forwards supervisor boot warnings queued before the bridge
// existed. The supervisor appends {scope, message, repoOwner?, repoName?} JSON
// lines (see bootWarningsPath); each becomes a `warning` sandbox event. The file
// is consumed exactly once — reconnects must not replay it. Mirrors
// _drain_boot_warnings.
func (b *AgentBridge) drainBootWarnings() {
	raw, err := os.ReadFile(b.bootWarningsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			b.log.Warn("bridge.boot_warnings_read_failed", "exc", err)
		}
		return
	}
	// Consume exactly once, before parsing, so a malformed file cannot replay.
	if err := os.Remove(b.bootWarningsPath); err != nil && !os.IsNotExist(err) {
		b.log.Warn("bridge.boot_warnings_read_failed", "exc", err)
		return
	}

	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if msg, ok := entry["message"].(string); !ok || msg == "" {
			continue
		}
		b.sendEvent(warningEvent(entry))
	}
}
