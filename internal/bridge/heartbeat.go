package bridge

import (
	"context"
	"time"
)

// heartbeatLoop sends periodic heartbeat events and WebSocket pings for the life
// of a single connection. A failed ping forces the connection closed so the read
// loop returns and the bridge reconnects.
func (b *AgentBridge) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			conn := b.getConn()
			if conn == nil {
				continue
			}
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				b.log.Warn("bridge.heartbeat_ping_failed", "exc", err)
				_ = conn.CloseNow()
				return
			}
			b.sendEvent(heartbeatEvent("ready"))
		}
	}
}
