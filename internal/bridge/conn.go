package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
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
	defer b.clearConn()
	defer func() { _ = conn.CloseNow() }()

	b.log.Info("bridge.connect", "outcome", "success")

	// Announce readiness, then replay anything buffered/unacked across the gap.
	b.sendEvent(readyEvent(b.getOpencodeSessionID()))
	justFlushed := b.flushEventBuffer()
	b.flushPendingAcks(justFlushed)

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go b.heartbeatLoop(hbCtx)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
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
