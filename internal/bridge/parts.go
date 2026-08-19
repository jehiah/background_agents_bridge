package bridge

import (
	"fmt"
	"strings"
)

// Anthropic extended-thinking budgets by reasoning effort. "max" uses 31,999 —
// the streaming API maximum; "high" uses a balanced 16,000.
var anthropicThinkingBudgets = map[string]int{
	"high": 16000,
	"max":  31999,
}

// anthropicAdaptiveThinkingFamilies are the Claude model families whose current
// members take adaptive thinking + outputConfig.effort rather than a fixed token
// budget. Upstream keeps an exact-id allowlist; matching the family prefix
// instead means a new Opus/Sonnet/Fable works the day it ships, which is the
// common case. Members that predate adaptive thinking are named below.
var anthropicAdaptiveThinkingFamilies = []string{
	"claude-fable-",
	"claude-opus-",
	"claude-sonnet-",
}

// anthropicFixedThinkingModels are family members that do not support adaptive
// thinking and must keep the fixed budget: everything before Opus 4.6 and
// Sonnet 4.6. Newer ids are matched by family, so this list only ever shrinks.
var anthropicFixedThinkingModels = map[string]bool{
	"claude-opus-4":     true,
	"claude-opus-4-1":   true,
	"claude-opus-4-5":   true,
	"claude-sonnet-4":   true,
	"claude-sonnet-4-5": true,
}

// usesAdaptiveThinking reports whether modelID takes the adaptive thinking
// options. A trailing snapshot date is ignored so "claude-sonnet-4-5-20250929"
// is judged as "claude-sonnet-4-5".
func usesAdaptiveThinking(modelID string) bool {
	modelID = withoutSnapshotDate(modelID)
	if anthropicFixedThinkingModels[modelID] {
		return false
	}
	for _, family := range anthropicAdaptiveThinkingFamilies {
		if strings.HasPrefix(modelID, family) {
			return true
		}
	}
	return false
}

// withoutSnapshotDate drops a trailing -YYYYMMDD release stamp, if present.
func withoutSnapshotDate(modelID string) string {
	i := strings.LastIndex(modelID, "-")
	if i < 0 {
		return modelID
	}
	suffix := modelID[i+1:]
	if len(suffix) != 8 {
		return modelID
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return modelID
		}
	}
	return modelID[:i]
}

var anthropicAdaptiveEfforts = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// buildPromptRequestBody builds the OpenCode prompt_async request body. The
// shape (parts/messageID/model.options) matches the Python bridge exactly so
// OpenCode treats requests identically.
func buildPromptRequestBody(content, model, opencodeMessageID, reasoningEffort string, fileParts []map[string]any) map[string]any {
	parts := make([]any, 0, 1+len(fileParts))
	parts = append(parts, map[string]any{"type": "text", "text": content})
	for _, fp := range fileParts {
		parts = append(parts, fp)
	}
	body := map[string]any{"parts": parts}
	if opencodeMessageID != "" {
		body["messageID"] = opencodeMessageID
	}
	if model == "" {
		return body
	}

	providerID, modelID := "anthropic", model
	if i := strings.Index(model, "/"); i >= 0 {
		providerID, modelID = model[:i], model[i+1:]
	}
	spec := map[string]any{"providerID": providerID, "modelID": modelID}

	if reasoningEffort != "" {
		switch providerID {
		case "anthropic", "google-vertex", "google-vertex-anthropic":
			if usesAdaptiveThinking(modelID) {
				opts := map[string]any{"thinking": map[string]any{"type": "adaptive"}}
				if anthropicAdaptiveEfforts[reasoningEffort] {
					opts["outputConfig"] = map[string]any{"effort": reasoningEffort}
				}
				spec["options"] = opts
			} else if budget, ok := anthropicThinkingBudgets[reasoningEffort]; ok {
				spec["options"] = map[string]any{
					"thinking": map[string]any{"type": "enabled", "budgetTokens": budget},
				}
			}
		case "openai":
			spec["options"] = map[string]any{
				"reasoningEffort":  reasoningEffort,
				"reasoningSummary": "auto",
			}
		}
	}

	body["model"] = spec
	return body
}

// --- generic map navigation helpers for OpenCode SSE payloads ---------------

func gstr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func gmap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	mm, _ := m[key].(map[string]any)
	return mm
}

// truthy mirrors Python truthiness for the values OpenCode sends (nil, empty
// string/map, false, zero are falsy).
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	case float64:
		return t != 0
	default:
		return true
	}
}

// isEmpty reports whether a tool input value should be treated as "no input"
// (Python `not tool_input`).
func isEmpty(v any) bool {
	return !truthy(v)
}

// extractErrorMessage pulls a human-readable message from an OpenCode NamedError
// shape: {"name": "...", "data": {"message": "..."}}.
func extractErrorMessage(e any) string {
	m, ok := e.(map[string]any)
	if !ok {
		if e == nil {
			return ""
		}
		return toStr(e)
	}
	if data, ok := m["data"].(map[string]any); ok {
		if msg, ok := data["message"]; ok {
			return toStr(msg)
		}
	}
	msg := m["message"]
	if !truthy(msg) {
		msg = m["name"]
	}
	if !truthy(msg) {
		return ""
	}
	return toStr(msg)
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}
