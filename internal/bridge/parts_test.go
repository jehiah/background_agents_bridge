package bridge

import (
	"encoding/json"
	"testing"
)

func TestBuildPromptRequestBody(t *testing.T) {
	cases := []struct {
		name            string
		model           string
		reasoningEffort string
		want            string
	}{
		{
			"no_model", "", "",
			`{"messageID":"msg_x","parts":[{"text":"hi","type":"text"}]}`,
		},
		{
			"anthropic_adaptive", "anthropic/claude-opus-4-8", "high",
			`{"messageID":"msg_x","model":{"modelID":"claude-opus-4-8","options":{"outputConfig":{"effort":"high"},"thinking":{"type":"adaptive"}},"providerID":"anthropic"},"parts":[{"text":"hi","type":"text"}]}`,
		},
		{
			"anthropic_budget", "claude-haiku-4-5", "high",
			`{"messageID":"msg_x","model":{"modelID":"claude-haiku-4-5","options":{"thinking":{"budgetTokens":16000,"type":"enabled"}},"providerID":"anthropic"},"parts":[{"text":"hi","type":"text"}]}`,
		},
		{
			"openai", "openai/gpt-5", "medium",
			`{"messageID":"msg_x","model":{"modelID":"gpt-5","options":{"reasoningEffort":"medium","reasoningSummary":"auto"},"providerID":"openai"},"parts":[{"text":"hi","type":"text"}]}`,
		},
		{
			"model_no_effort", "anthropic/claude-opus-4-8", "",
			`{"messageID":"msg_x","model":{"modelID":"claude-opus-4-8","providerID":"anthropic"},"parts":[{"text":"hi","type":"text"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildPromptRequestBody("hi", tc.model, "msg_x", tc.reasoningEffort)
			got, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("body mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestExtractErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"named_data", map[string]any{"data": map[string]any{"message": "boom"}}, "boom"},
		{"name_fallback", map[string]any{"name": "E", "data": map[string]any{}}, "E"},
		{"message_field", map[string]any{"message": "x"}, "x"},
		{"empty_message_uses_name", map[string]any{"message": "", "name": "N"}, "N"},
		{"plain_string", "plain", "plain"},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractErrorMessage(tc.in); got != tc.want {
				t.Errorf("extractErrorMessage(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTransformTool(t *testing.T) {
	s := newStreamState()

	// Pending with no input is skipped.
	if _, ok := s.transformTool(map[string]any{
		"tool": "bash", "callID": "c", "state": map[string]any{"status": "pending"},
	}); ok {
		t.Error("pending tool with no input should be skipped")
	}

	// Completed with input becomes a tool_call; missing output defaults to "".
	ev, ok := s.transformTool(map[string]any{
		"tool": "bash", "callID": "c1",
		"state": map[string]any{"status": "completed", "input": map[string]any{"command": "ls"}},
	})
	if !ok {
		t.Fatal("expected tool_call")
	}
	if ev["output"] != "" || ev["status"] != "completed" || ev["tool"] != "bash" {
		t.Errorf("tool_call fields wrong: %+v", ev)
	}
}
