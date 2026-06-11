package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
)

// sendEvent stamps and transmits an event to the control plane, buffering it if
// the connection is unavailable. Critical events are assigned an ackId and
// tracked until acknowledged. Mirrors Python's _send_event.
func (b *AgentBridge) sendEvent(e event) {
	etype, _ := e["type"].(string)
	e["sandboxId"] = b.sandboxID
	if _, ok := e["timestamp"]; !ok {
		e["timestamp"] = nowUnix()
	}

	critical := criticalEventTypes[etype]
	if critical {
		if _, ok := e["ackId"]; !ok {
			e["ackId"] = makeAckID(e)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn == nil {
		b.bufferEventLocked(e)
		return
	}
	if err := b.writeLocked(e); err != nil {
		b.log.Warn("bridge.send_error", "event_type", etype, "exc", err)
		b.bufferEventLocked(e)
		return
	}
	if critical {
		if id, ok := e["ackId"].(string); ok {
			b.pendingAcks[id] = e
		}
	}
}

// writeLocked marshals and writes an event on the current connection. Callers
// must hold b.mu, which serializes writes (coder/websocket permits only one
// concurrent writer).
func (b *AgentBridge) writeLocked(e event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(b.rootCtx, httpDefaultTimeout)
	defer cancel()
	return b.conn.Write(ctx, websocket.MessageText, data)
}

// bufferEventLocked stores an event for delivery after reconnect. When the
// buffer is full it evicts the oldest non-critical event (or the oldest event
// if all are critical). Callers must hold b.mu.
func (b *AgentBridge) bufferEventLocked(e event) {
	if len(b.eventBuffer) >= maxEventBufferSize {
		evicted := false
		for i, buf := range b.eventBuffer {
			t, _ := buf["type"].(string)
			if !criticalEventTypes[t] {
				b.eventBuffer = append(b.eventBuffer[:i], b.eventBuffer[i+1:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			b.eventBuffer = b.eventBuffer[1:]
		}
	}
	b.eventBuffer = append(b.eventBuffer, e)
	b.log.Debug("bridge.event_buffered", "event_type", e["type"], "buffer_size", len(b.eventBuffer))
}

// makeAckID returns a deterministic ack ID for a critical event:
// "{type}:{messageId}" when a messageId is present, else "{type}:{randomHex}".
// Deterministic IDs give natural dedup on the control-plane side.
func makeAckID(e event) string {
	etype, _ := e["type"].(string)
	if mid, ok := e["messageId"].(string); ok && mid != "" {
		return etype + ":" + mid
	}
	return etype + ":" + randomHex(8)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is not expected; fall back to a timestamp so the
		// ack ID is still unique enough for dedup.
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}

// flushEventBuffer re-sends buffered events after reconnect. It returns the set
// of ackIds added to pendingAcks during the flush so flushPendingAcks can skip
// them (avoiding a double send on the same reconnect).
func (b *AgentBridge) flushEventBuffer() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	justAdded := make(map[string]bool)
	if len(b.eventBuffer) == 0 {
		return justAdded
	}

	b.log.Info("bridge.flush_buffer_start", "buffer_size", len(b.eventBuffer))
	flushed := 0
	for len(b.eventBuffer) > 0 {
		if b.conn == nil {
			break
		}
		e := b.eventBuffer[0]
		if err := b.writeLocked(e); err != nil {
			b.log.Warn("bridge.flush_send_error", "exc", err)
			break
		}
		b.eventBuffer = b.eventBuffer[1:]
		flushed++
		t, _ := e["type"].(string)
		if criticalEventTypes[t] {
			if id, ok := e["ackId"].(string); ok {
				b.pendingAcks[id] = e
				justAdded[id] = true
			}
		}
	}
	b.log.Info("bridge.flush_buffer_complete", "flushed", flushed, "remaining", len(b.eventBuffer))
	return justAdded
}

// flushPendingAcks re-sends unacknowledged critical events on a new connection.
// Events remain in pendingAcks until the control plane sends an ack command.
func (b *AgentBridge) flushPendingAcks(skip map[string]bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pendingAcks) == 0 {
		return
	}
	b.log.Info("bridge.flush_pending_acks_start", "count", len(b.pendingAcks))
	resent := 0
	for id, e := range b.pendingAcks {
		if skip[id] {
			continue
		}
		if b.conn == nil {
			break
		}
		if err := b.writeLocked(e); err != nil {
			b.log.Warn("bridge.flush_pending_ack_error", "ack_id", id, "exc", err)
			break
		}
		resent++
	}
	b.log.Info("bridge.flush_pending_acks_complete", "resent", resent, "total", len(b.pendingAcks))
}

// ackReceived removes an acknowledged event from the pending set.
func (b *AgentBridge) ackReceived(ackID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pendingAcks[ackID]; ok {
		delete(b.pendingAcks, ackID)
		b.log.Debug("bridge.ack_received", "ack_id", ackID)
	}
}
