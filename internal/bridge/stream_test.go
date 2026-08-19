package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// testStreamState is the production state seeded with the ids the tests use.
func testStreamState() *streamState {
	return newStreamState("cp-msg", "msg_user", "ses_parent")
}

// TestStreamTextAndToolCorrelation covers the core happy path: an assistant
// message is authorized by parentID, then its text and tool parts are forwarded
// with the control-plane messageId.
func TestStreamTextAndToolCorrelation(t *testing.T) {
	b := testBridge()
	s := testStreamState()
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
	s := testStreamState()
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
	s := testStreamState()
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
	s := testStreamState()
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
	s := testStreamState()
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
	s := testStreamState()
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
// isSubtask=true while child text tokens are suppressed, and that activity seen
// before the parent's task part is held until that part names its Task call.
func TestStreamChildSubtask(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "session.created", map[string]any{
		"info": map[string]any{"id": "ses_child", "parentID": "ses_parent"},
	}, c.emit)
	if !s.childActivity.isTracked("ses_child") {
		t.Fatal("child session not tracked")
	}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_c", "sessionID": "ses_child", "role": "assistant"},
	}, c.emit)

	// Child text is suppressed.
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "ct", "messageID": "msg_c", "sessionID": "ses_child", "text": "secret"},
	}, c.emit)
	// Child tool waits: nothing yet says which Task call owns this child.
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "grep", "callID": "cc", "messageID": "msg_c", "sessionID": "ses_child",
			"state": map[string]any{"status": "completed", "input": map[string]any{"q": "x"}},
		},
	}, c.emit)
	if len(c.events) != 0 {
		t.Fatalf("expected child activity to be held, got %v", c.types())
	}

	// The parent's task part names the child, releasing the held activity.
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "task", "callID": "tc1", "messageID": "msg_p", "sessionID": "ses_parent",
			"metadata": map[string]any{"sessionId": "ses_child"},
			"state":    map[string]any{"status": "running", "input": map[string]any{"prompt": "go"}},
		},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"tool_call"}) {
		t.Fatalf("expected only subtask tool_call, got %v", got)
	}
	e := c.events[0]
	if e["isSubtask"] != true || e["childSessionId"] != "ses_child" || e["taskCallId"] != "tc1" {
		t.Errorf("subtask tool_call not correlated: %+v", e)
	}
}

// TestStreamTaskToolCarriesChildSession verifies the parent's own task tool_call
// is tagged with the child session it spawned.
func TestStreamTaskToolCarriesChildSession(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}
	s.allowedAssistantMsgIDs["msg_p"] = true

	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "task", "callID": "tc1", "messageID": "msg_p", "sessionID": "ses_parent",
			"metadata": map[string]any{"sessionId": "ses_child"},
			"state":    map[string]any{"status": "running", "input": map[string]any{"prompt": "go"}},
		},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"tool_call"}) {
		t.Fatalf("expected task tool_call, got %v", got)
	}
	if c.events[0]["childSessionId"] != "ses_child" || c.events[0]["isSubtask"] != nil {
		t.Errorf("parent task tool_call wrong: %+v", c.events[0])
	}
}

// TestStreamChildErrorHeldUntilTask verifies a child error that arrives before
// the task part is nested under it once known, and that an uncorrelated one is
// still emitted when the stream ends.
func TestStreamChildErrorHeldUntilTask(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "session.created", map[string]any{
		"info": map[string]any{"id": "ses_child", "parentID": "ses_parent"},
	}, c.emit)
	feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_child",
		"error":     map[string]any{"data": map[string]any{"message": "boom"}},
	}, c.emit)
	if len(c.events) != 0 {
		t.Fatalf("expected child error to be held, got %v", c.types())
	}

	// No task part ever arrives: the end-of-stream flush emits it uncorrelated.
	s.flushChildActivity(c.emit)
	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected flushed error, got %v", got)
	}
	e := c.events[0]
	if e["error"] != "boom" || e["isSubtask"] != true || e["childSessionId"] != "ses_child" {
		t.Errorf("flushed child error wrong: %+v", e)
	}
	if _, ok := e["taskCallId"]; ok {
		t.Errorf("uncorrelated error should carry no taskCallId: %+v", e)
	}
}

// TestStreamChildErrorAfterTaskCompletes verifies an error that trails a
// finished task is emitted at once, nested under that task, rather than being
// held for a Task call that will never come.
func TestStreamChildErrorAfterTaskCompletes(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "tool", "tool": "task", "callID": "tc1", "messageID": "msg_p", "sessionID": "ses_parent",
			"metadata": map[string]any{"sessionId": "ses_child"},
			"state":    map[string]any{"status": "completed", "input": map[string]any{"prompt": "go"}},
		},
	}, c.emit)
	feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_child",
		"error":     map[string]any{"data": map[string]any{"message": "late boom"}},
	}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected the child error, got %v", got)
	}
	e := c.events[0]
	if e["error"] != "late boom" || e["childSessionId"] != "ses_child" || e["taskCallId"] != "tc1" {
		t.Errorf("late child error not nested: %+v", e)
	}
}

// TestStreamParentErrorStops verifies a parent session error emits an error
// event and stops the stream.
func TestStreamParentErrorStops(t *testing.T) {
	b := testBridge()
	s := testStreamState()
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
	s := testStreamState()
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
	s := testStreamState()
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

// overflowError is the shape OpenCode sends when the context limit is hit.
func overflowError(message string) map[string]any {
	return map[string]any{
		"name": "ContextOverflowError",
		"data": map[string]any{"message": message},
	}
}

// TestStreamContextOverflowCompacts verifies a parent context overflow is an
// announcement, not a failure: with automatic compaction the stream must stay
// open and the prompt must finish clean.
func TestStreamContextOverflowCompacts(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	if stop := feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_parent",
		"error":     overflowError("context window exceeded"),
	}, c.emit); stop {
		t.Fatal("context overflow ended the stream")
	}
	if s.pendingOverflowError != "context window exceeded" {
		t.Errorf("pendingOverflowError = %q", s.pendingOverflowError)
	}

	feed(t, b, s, "session.compacted", map[string]any{"sessionID": "ses_parent"}, c.emit)
	if s.pendingOverflowError != "" {
		t.Errorf("compaction left pendingOverflowError = %q", s.pendingOverflowError)
	}

	// Work resumes on a rewritten message chain, then the session goes idle.
	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{"id": "msg_post", "sessionID": "ses_parent", "parentID": "changed", "role": "assistant"},
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{"type": "text", "id": "p1", "messageID": "msg_post", "sessionID": "ses_parent", "text": "after"},
	}, c.emit)
	if stop := feed(t, b, s, "session.idle", map[string]any{"sessionID": "ses_parent"}, c.emit); !stop {
		t.Fatal("expected stop=true on parent idle")
	}

	if got := c.types(); !equalStrings(got, []string{"token"}) {
		t.Fatalf("expected only the post-compaction token, got %v", got)
	}
}

// TestStreamUnrecoveredContextOverflowFails verifies the swallowed overflow is
// surfaced when the promised compaction never arrives — reporting success for a
// prompt that produced nothing would be worse than the original error.
func TestStreamUnrecoveredContextOverflowFails(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_parent",
		"error":     overflowError("context window exceeded"),
	}, c.emit)
	if stop := feed(t, b, s, "session.status", map[string]any{
		"sessionID": "ses_parent",
		"status":    map[string]any{"type": "idle"},
	}, c.emit); !stop {
		t.Fatal("expected stop=true on parent idle status")
	}

	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected the overflow surfaced as an error, got %v", got)
	}
	if c.events[0]["error"] != "context window exceeded" {
		t.Errorf("error message = %q", c.events[0]["error"])
	}
}

// TestStreamOverflowNotRepeatedAfterError verifies an overflow followed by a
// real error reports the real error once, not both.
func TestStreamOverflowNotRepeatedAfterError(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_parent",
		"error":     overflowError("context window exceeded"),
	}, c.emit)
	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{
			"id": "msg_a", "sessionID": "ses_parent", "parentID": "msg_user", "role": "assistant",
			"error": map[string]any{"data": map[string]any{"message": "kaboom"}},
		},
	}, c.emit)
	feed(t, b, s, "session.idle", map[string]any{"sessionID": "ses_parent"}, c.emit)

	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected one error, got %v", got)
	}
	if c.events[0]["error"] != "kaboom" {
		t.Errorf("error message = %q", c.events[0]["error"])
	}
}

// TestStreamChildContextOverflowIgnored verifies a child session's overflow is
// dropped: it recovers through the same compaction, and surfacing it would fail
// the whole prompt.
func TestStreamChildContextOverflowIgnored(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "session.created", map[string]any{
		"info": map[string]any{"id": "ses_child", "parentID": "ses_parent"},
	}, c.emit)
	if stop := feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_child",
		"error":     overflowError("context window exceeded"),
	}, c.emit); stop {
		t.Fatal("child context overflow ended the stream")
	}
	if len(c.events) != 0 {
		t.Fatalf("expected no events, got %v", c.types())
	}
}

// TestStreamParentErrorDeduped verifies the same failure arriving as both a
// message error and a session error reaches the control plane once.
func TestStreamParentErrorDeduped(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{
			"id": "msg_a", "sessionID": "ses_parent", "parentID": "msg_user", "role": "assistant",
			"error": map[string]any{"data": map[string]any{"message": "kaboom"}},
		},
	}, c.emit)
	if stop := feed(t, b, s, "session.error", map[string]any{
		"sessionID": "ses_parent",
		"error":     map[string]any{"data": map[string]any{"message": "kaboom"}},
	}, c.emit); !stop {
		t.Fatal("expected stop=true on parent session error")
	}

	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected a single error, got %v", got)
	}
}

// TestStreamCompactionSummaryNotForwarded verifies the compaction summary is
// never assistant output. Its parentID is the compaction user message, which
// matches, so parentID alone cannot exclude it — but an error on it still
// belongs to the prompt.
func TestStreamCompactionSummaryNotForwarded(t *testing.T) {
	b := testBridge()
	s := testStreamState()
	c := &collector{}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{
			"id": "msg_summary", "sessionID": "ses_parent", "parentID": "msg_user",
			"role": "assistant", "summary": true,
		},
	}, c.emit)
	feed(t, b, s, "message.part.updated", map[string]any{
		"part": map[string]any{
			"type": "text", "id": "p1", "messageID": "msg_summary",
			"sessionID": "ses_parent", "text": "internal context",
		},
	}, c.emit)

	if s.allowedAssistantMsgIDs["msg_summary"] {
		t.Error("compaction summary was authorized as assistant output")
	}
	if !s.correlatedCompactionSummaryIDs["msg_summary"] {
		t.Error("compaction summary was not correlated to the prompt")
	}
	if len(c.events) != 0 {
		t.Fatalf("expected the summary text suppressed, got %v", c.types())
	}

	feed(t, b, s, "message.updated", map[string]any{
		"info": map[string]any{
			"id": "msg_summary", "sessionID": "ses_parent", "parentID": "msg_user",
			"role": "assistant", "summary": true,
			"error": map[string]any{"data": map[string]any{"message": "compaction failed"}},
		},
	}, c.emit)
	if got := c.types(); !equalStrings(got, []string{"error"}) {
		t.Fatalf("expected the summary's error surfaced, got %v", got)
	}
}

// TestFetchFinalMessageStateSkipsCompactionSummary verifies the reconciliation
// pass applies the same rule as the live stream: after compaction every
// assistant message is in scope except the summary itself.
func TestFetchFinalMessageStateSkipsCompactionSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"info": map[string]any{
					"id": "msg_summary", "role": "assistant", "sessionID": "ses_parent",
					"parentID": "msg_user", "summary": true,
				},
				"parts": []map[string]any{{"type": "text", "id": "p1", "text": "internal context"}},
			},
			{
				"info": map[string]any{
					"id": "msg_post", "role": "assistant", "sessionID": "ses_parent", "parentID": "changed",
				},
				"parts": []map[string]any{{"type": "text", "id": "p2", "text": "after"}},
			},
		})
	}))
	defer server.Close()

	b := testBridge()
	b.opencodeBaseURL = server.URL
	b.httpClient = server.Client()
	b.setOpencodeSessionID("ses_parent")

	s := testStreamState()
	s.compactionOccurred = true
	c := &collector{}
	b.fetchFinalMessageState(t.Context(), s, c.emit)

	if got := c.types(); !equalStrings(got, []string{"token"}) {
		t.Fatalf("expected one token, got %v", got)
	}
	if c.events[0]["content"] != "after" {
		t.Errorf("token = %+v, want the post-compaction text", c.events[0])
	}
}
