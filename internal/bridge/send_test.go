package bridge

import (
	"strconv"
	"strings"
	"testing"
)

func TestMakeAckID(t *testing.T) {
	if got := makeAckID(event{"type": "execution_complete", "messageId": "m1"}); got != "execution_complete:m1" {
		t.Errorf("with messageId = %q", got)
	}
	got := makeAckID(event{"type": "snapshot_ready"})
	if !strings.HasPrefix(got, "snapshot_ready:") {
		t.Fatalf("missing prefix: %q", got)
	}
	if hexPart := strings.TrimPrefix(got, "snapshot_ready:"); len(hexPart) != 16 {
		t.Errorf("random hex len = %d (%q), want 16", len(hexPart), hexPart)
	}
}

// TestBufferEvictionNonCritical: when full, the oldest non-critical event is
// evicted first.
func TestBufferEvictionNonCritical(t *testing.T) {
	b := testBridge()
	for i := 0; i < maxEventBufferSize; i++ {
		b.bufferEventLocked(tokenEvent(strconv.Itoa(i), "m"))
	}
	b.bufferEventLocked(tokenEvent("newest", "m"))

	if len(b.eventBuffer) != maxEventBufferSize {
		t.Fatalf("buffer size = %d, want %d", len(b.eventBuffer), maxEventBufferSize)
	}
	if b.eventBuffer[0]["content"] != "1" {
		t.Errorf("oldest after eviction = %q, want %q", b.eventBuffer[0]["content"], "1")
	}
	if b.eventBuffer[len(b.eventBuffer)-1]["content"] != "newest" {
		t.Errorf("newest not appended: %q", b.eventBuffer[len(b.eventBuffer)-1]["content"])
	}
}

// TestBufferEvictionKeepsCritical: a critical event is preserved while the
// oldest non-critical one is evicted.
func TestBufferEvictionKeepsCritical(t *testing.T) {
	b := testBridge()
	b.bufferEventLocked(event{"type": "execution_complete", "messageId": "crit"}) // index 0, critical
	for i := 0; i < maxEventBufferSize-1; i++ {
		b.bufferEventLocked(tokenEvent(strconv.Itoa(i), "m"))
	}
	b.bufferEventLocked(tokenEvent("newest", "m")) // triggers eviction

	if len(b.eventBuffer) != maxEventBufferSize {
		t.Fatalf("buffer size = %d, want %d", len(b.eventBuffer), maxEventBufferSize)
	}
	if b.eventBuffer[0]["type"] != "execution_complete" {
		t.Errorf("critical event was evicted; head = %+v", b.eventBuffer[0])
	}
}

// TestSendEventBuffersWhenDisconnected: with no connection, events are buffered
// and stamped (sandboxId, timestamp, and ackId for critical events).
func TestSendEventBuffersWhenDisconnected(t *testing.T) {
	b := testBridge()
	b.sendEvent(executionCompleteEvent("m1", true, ""))

	if len(b.eventBuffer) != 1 {
		t.Fatalf("expected 1 buffered event, got %d", len(b.eventBuffer))
	}
	e := b.eventBuffer[0]
	if e["sandboxId"] != "sb-test" {
		t.Errorf("sandboxId not stamped: %+v", e)
	}
	if _, ok := e["timestamp"]; !ok {
		t.Error("timestamp not stamped")
	}
	if e["ackId"] != "execution_complete:m1" {
		t.Errorf("ackId = %v, want execution_complete:m1", e["ackId"])
	}
}

func TestAckReceived(t *testing.T) {
	b := testBridge()
	b.pendingAcks["execution_complete:m1"] = event{"type": "execution_complete"}
	b.ackReceived("execution_complete:m1")
	if _, ok := b.pendingAcks["execution_complete:m1"]; ok {
		t.Error("ack not removed from pending set")
	}
}
