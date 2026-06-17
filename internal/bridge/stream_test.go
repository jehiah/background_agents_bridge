package bridge

import (
	"context"
	"testing"
)

// collector accumulates emitted events for assertions.
type collector struct{ events []event }

func (c *collector) emit(e event) { c.events = append(c.events, e) }

func (c *collector) types() []string {
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i], _ = e["type"].(string)
	}
	return out
}

// feed pushes one OpenCode SSE event through the dispatcher.
func feed(t *testing.T, b *AgentBridge, s *streamState, etype string, props map[string]any, emit func(event)) bool {
	t.Helper()
	stop, err := b.dispatchSSE(context.Background(), s, sseEvent{Type: etype, Properties: props}, emit)
	if err != nil {
		t.Fatalf("dispatchSSE(%s): %v", etype, err)
	}
	return stop
}

func newStreamState() *streamState {
	return &streamState{
		messageID:              "cp-msg",
		opencodeMessageID:      "msg_user",
		opencodeSessionID:      "ses_parent",
		cumulativeText:         map[string]string{},
		emittedToolStates:      map[string]bool{},
		allowedAssistantMsgIDs: map[string]bool{},
		userMessageIDs:         map[string]bool{"msg_user": true},
		pendingParts:           map[string][]pendingPart{},
		trackedChildSessionIDs: map[string]bool{},
	}
}

// TestStreamTextAndToolCorrelation covers the core happy path: an assistant
// message is authorized by parentID, then its text and tool parts are forwarded
// with the control-plane messageId.
func TestStreamTextAndToolCorrelation(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{
			"id": "msg_a", "sessionID": "ses_parent", "parentID": "msg_user", "role": "assistant",
		},
	}, c.emit)

	feed(t, b, s, "message.part.updated", map[string]any{
		"part":  map[string]any{"type": "text", "id": "p1", "messageID": "msg_a", "sessionID": "ses_parent", "text": "Hello"},
		"delta": "",
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part":  map[string]any{"type": "text", "id": "p1", "messageID": "msg_a", "sessionID": "ses_parent"},
		"delta": " world",
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "bash", "callID": "c1", "id": "pt", "messageID": "msg_a", "sessionID": "ses_parent",
			"state": map[string]any{"status": "completed", "input": map[string]any{"command": "ls"}, "output": "file"},
		},
	}, c.emit)

	want := []string{"token", "token", "tool_call"}
	if got := c.types(); !equalStrings(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if c.events[0]["content"] != "Hello" || c.events[1]["content"] != "Hello world" {
		t.Errorf("cumulative text wrong: %q then %q", c.events[0]["content"], c.events[1]["content"])
	}
	if c.events[2]["messageId"] != "cp-msg" || c.events[2]["tool"] != "bash" {
		t.Errorf("tool_call wrong: %+v", c.events[2])
	}
}

// TestStreamToolDedup verifies a (session, callID, status) is emitted only once.
func TestStreamToolDedup(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}
	s.allowedAssistantMsgIDs["msg_a"] = true

	toolEvent := map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "bash", "callID": "c1", "messageID": "msg_a", "sessionID": "ses_parent",
			"state": map[string]any{"status": "completed", "input": map[string]any{"x": 1}},
		},
	}
	feed(t, b, s, "message.part.updated", toolEvent, c.emit)
	feed(t, b, s, "message.part.updated", toolEvent, c.emit)

	if len(c.events) != 1 {
		t.Fatalf("expected 1 tool_call after dedup, got %d", len(c.events))
	}
}

// TestStreamPendingPartFlush verifies parts arriving before authorization are
// buffered and replayed once the message is authorized.
func TestStreamPendingPartFlush(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	// Part for msg_b arrives before its message.updated — should buffer.
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "p9", "messageID": "msg_b", "sessionID": "ses_parent", "text": "buffered"},
	}, c.emit)
	if len(c.events) != 0 {
		t.Fatalf("expected buffering (no emit), got %v", c.types())
	}
	if s.pendingPartsTotal != 1 {
		t.Fatalf("pendingPartsTotal = %d, want 1", s.pendingPartsTotal)
	}

	// Authorize msg_b — buffered part should flush.
	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_b", "sessionID": "ses_parent", "parentID": "msg_user", "role": "assistant"},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"token"}) {
		t.Fatalf("expected flushed token, got %v", got)
	}
	if c.events[0]["content"] != "buffered" {
		t.Errorf("flushed content = %q", c.events[0]["content"])
	}
	if s.pendingPartsTotal != 0 {
		t.Errorf("pendingPartsTotal after flush = %d, want 0", s.pendingPartsTotal)
	}
}

// TestStreamUnrelatedParentIgnored verifies assistant messages whose parentID
// doesn't match our user message are not authorized.
func TestStreamUnrelatedParentIgnored(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_x", "sessionID": "ses_parent", "parentID": "someone_else", "role": "assistant"},
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "p1", "messageID": "msg_x", "sessionID": "ses_parent", "text": "nope"},
	}, c.emit)

	// The part buffers (msg_x never authorized) and nothing is emitted.
	if len(c.events) != 0 {
		t.Fatalf("expected no emitted events, got %v", c.types())
	}
}

// TestStreamTracksActualUserMessageID verifies that when OpenCode regenerates
// the user message ID, assistant messages parented to the actual user ID are
// still authorized.
func TestStreamTracksActualUserMessageID(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	const actualUserID = "msg_actual_user"

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": actualUserID, "sessionID": "ses_parent", "role": "user"},
	}, c.emit)
	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_a", "sessionID": "ses_parent", "parentID": actualUserID, "role": "assistant"},
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "p1", "messageID": "msg_a", "sessionID": "ses_parent", "text": "Hello via actual parent"},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"token"}) {
		t.Fatalf("expected token forwarded, got %v", got)
	}
	if c.events[0]["content"] != "Hello via actual parent" {
		t.Errorf("content = %q", c.events[0]["content"])
	}
}

// TestStreamCompactionAuthorizes verifies that after compaction, non-summary
// assistant messages are authorized even without a parentID match.
func TestStreamCompactionAuthorizes(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	feed(t, b, s, "session.compacted", map[string]any{"sessionID": "ses_parent"}, c.emit)
	if !s.compactionOccurred {
		t.Fatal("compactionOccurred not set")
	}
	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_post", "sessionID": "ses_parent", "parentID": "changed", "role": "assistant"},
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "p1", "messageID": "msg_post", "sessionID": "ses_parent", "text": "after"},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"token"}) {
		t.Fatalf("expected token after compaction, got %v", got)
	}
}

// TestStreamChildSubtask verifies child-session tool parts are forwarded with
// isSubtask=true while child text tokens are suppressed.
func TestStreamChildSubtask(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	feed(t, b, s, "session.created", map[string]any{
		"info": map[string]any{"id": "ses_child", "parentID": "ses_parent"},
	}, c.emit)
	if !s.trackedChildSessionIDs["ses_child"] {
		t.Fatal("child session not tracked")
	}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_c", "sessionID": "ses_child", "role": "assistant"},
	}, c.emit)

	// Child text is suppressed.
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "ct", "messageID": "msg_c", "sessionID": "ses_child", "text": "secret"},
	}, c.emit)
	// Child tool is forwarded with isSubtask=true.
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "grep", "callID": "cc", "messageID": "msg_c", "sessionID": "ses_child",
			"state": map[string]any{"status": "completed", "input": map[string]any{"q": "x"}},
		},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"tool_call"}) {
		t.Fatalf("expected only subtask tool_call, got %v", got)
	}
	if c.events[0]["isSubtask"] != true {
		t.Errorf("expected isSubtask=true, got %+v", c.events[0])
	}
}

// TestStreamParentErrorStops verifies a parent session error emits an error
// event and stops the stream.
func TestStreamParentErrorStops(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	stop := feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_parent",
		"error":     map[string]any{"data": map[string]any{"message": "kaboom"}},
	}, c.emit)

	if !stop {
		t.Fatal("expected stop=true on parent session error")
	}
	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected error event, got %v", got)
	}
	if c.events[0]["error"] != "kaboom" {
		t.Errorf("error message = %q", c.events[0]["error"])
	}
}

// TestStreamSessionTitle verifies a non-default title is forwarded once.
func TestStreamSessionTitle(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	props := map[string]any{
		"sessionID": "ses_parent",
		"info":      map[string]any{"id": "ses_parent", "title": "Fix the bug"},
	}
	feed(t, b, s, "session.updated", props, c.emit)
	feed(t, b, s, "session.updated", props, c.emit) // duplicate, should not re-emit

	if got := c.types(); !equalStrings(got, []string{"session_title"}) {
		t.Fatalf("expected single session_title, got %v", got)
	}
	if c.events[0]["title"] != "Fix the bug" {
		t.Errorf("title = %q", c.events[0]["title"])
	}
}

// TestStreamDefaultTitleSuppressed verifies OpenCode's auto-generated titles are
// not forwarded.
func TestStreamDefaultTitleSuppressed(t *testing.T) {
	b := testBridge()
	s := newStreamState()
	c := &collector{}

	feed(t, b, s, "session.updated", map[string]any{
		"sessionID": "ses_parent",
		"info":      map[string]any{"id": "ses_parent", "title": "New Session - 2026-06-11T01:02:03.456Z"},
	}, c.emit)

	if len(c.events) != 0 {
		t.Fatalf("expected default title suppressed, got %v", c.types())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
