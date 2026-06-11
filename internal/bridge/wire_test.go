package bridge

import (
	"encoding/json"
	"testing"
)

// marshalStable serializes an event after dropping non-deterministic fields so
// golden comparisons are stable. encoding/json sorts map keys, so output is
// deterministic for the remaining fields.
func marshalStable(t *testing.T, e event) string {
	t.Helper()
	delete(e, "timestamp")
	delete(e, "sandboxId")
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestEventWireFormat locks the exact JSON shape of every outbound event so the
// Go bridge stays byte-compatible with the Python control-plane contract.
func TestEventWireFormat(t *testing.T) {
	cases := []struct {
		name string
		ev   event
		want string
	}{
		{"ready_unset", readyEvent(""), `{"opencodeSessionId":null,"type":"ready"}`},
		{"ready_set", readyEvent("ses_x"), `{"opencodeSessionId":"ses_x","type":"ready"}`},
		{"heartbeat", heartbeatEvent("ready"), `{"status":"ready","type":"heartbeat"}`},
		{"token", tokenEvent("hi", "m1"), `{"content":"hi","messageId":"m1","type":"token"}`},
		{
			"tool_call",
			toolCallEvent("bash", map[string]any{"cmd": "ls"}, "c1", "completed", "out", "m1"),
			`{"args":{"cmd":"ls"},"callId":"c1","messageId":"m1","output":"out","status":"completed","tool":"bash","type":"tool_call"}`,
		},
		{"step_start", stepStartEvent("m1"), `{"messageId":"m1","type":"step_start"}`},
		{
			"step_finish_null",
			stepFinishEvent(nil, nil, nil, "m1"),
			`{"cost":null,"messageId":"m1","reason":null,"tokens":null,"type":"step_finish"}`,
		},
		{"session_title", sessionTitleEvent("T"), `{"title":"T","type":"session_title"}`},
		{"error", errorEvent("e", "m1"), `{"error":"e","messageId":"m1","type":"error"}`},
		{
			"exec_complete_ok",
			executionCompleteEvent("m1", true, ""),
			`{"messageId":"m1","success":true,"type":"execution_complete"}`,
		},
		{
			"exec_complete_err",
			executionCompleteEvent("m1", false, "boom"),
			`{"error":"boom","messageId":"m1","success":false,"type":"execution_complete"}`,
		},
		{"snapshot_ready", snapshotReadyEvent("ses"), `{"opencodeSessionId":"ses","type":"snapshot_ready"}`},
		{"push_complete", pushCompleteEvent("b"), `{"branchName":"b","type":"push_complete"}`},
		{"push_error_branch", pushErrorEvent("err", "b", true), `{"branchName":"b","error":"err","type":"push_error"}`},
		{"push_error_nobranch", pushErrorEvent("err", "", false), `{"error":"err","type":"push_error"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marshalStable(t, tc.ev)
			if got != tc.want {
				t.Errorf("wire mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestCommandUnmarshal(t *testing.T) {
	raw := `{"type":"prompt","messageId":"m1","content":"hello","model":"anthropic/claude-opus-4-8",
		"reasoningEffort":"high","author":{"scmName":"Jane","scmEmail":"jane@example.com"}}`
	var cmd command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cmd.Type != "prompt" || cmd.msgID() != "m1" || cmd.Content != "hello" {
		t.Errorf("unexpected: %+v", cmd)
	}
	if cmd.Author.SCMName != "Jane" || cmd.Author.SCMEmail != "jane@example.com" {
		t.Errorf("author: %+v", cmd.Author)
	}
}

func TestCommandMsgIDFallback(t *testing.T) {
	snake := command{MessageIDSnake: "snake"}
	if snake.msgID() != "snake" {
		t.Errorf("snake fallback: got %q", snake.msgID())
	}
	empty := command{}
	if empty.msgID() != "unknown" {
		t.Errorf("unknown fallback: got %q", empty.msgID())
	}
}
