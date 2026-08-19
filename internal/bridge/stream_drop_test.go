package bridge

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamTransportDropPreservesFinalOutput covers a mid-response disconnect:
// the event stream dies after a partial text part, and OpenCode holds the
// complete text. The prompt must fail with the stable disconnect message *and*
// still forward the text OpenCode persisted. Port of
// test_transport_drop_preserves_final_output (upstream #1009).
func TestStreamTransportDropPreservesFinalOutput(t *testing.T) {
	finalText := "Partial response"
	// The bridge generates the prompt's OpenCode message id, so the fake server
	// learns it from the prompt POST and uses it as the assistant reply's
	// parentID — that is what authorizes the reply, streaming or recovered.
	promptID := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			writeSSEEvents(w, map[string]any{"type": "server.connected"})
			parent := <-promptID
			promptID <- parent
			writeSSEEvents(w,
				map[string]any{"type": "message.updated", "properties": map[string]any{
					"info": map[string]any{
						"id": "oc-msg-1", "sessionID": "ses_test", "parentID": parent, "role": "assistant",
					},
				}},
				map[string]any{"type": "message.part.updated", "properties": map[string]any{
					"part": map[string]any{
						"type": "text", "id": "p1", "messageID": "oc-msg-1",
						"sessionID": "ses_test", "text": "Partial",
					},
				}},
			)
			// Drop the connection mid-stream, without a terminating event.
			panic(http.ErrAbortHandler)
		case strings.HasSuffix(r.URL.Path, "/prompt_async"):
			promptID <- promptMessageID(r)
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/message"):
			parent := <-promptID
			promptID <- parent
			json.NewEncoder(w).Encode([]map[string]any{{
				"info": map[string]any{
					"id": "oc-msg-1", "role": "assistant", "parentID": parent, "sessionID": "ses_test",
				},
				"parts": []map[string]any{{"type": "text", "id": "p1", "text": finalText}},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	// The abrupt close is the point of the test; keep it out of the test log.
	server.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)

	b := testBridge()
	b.opencodeBaseURL = server.URL
	b.httpClient = server.Client()
	b.sseInactivityTimeout = 30 * time.Second
	b.setOpencodeSessionID("ses_test")

	c := &collector{}
	err := b.streamOpencodeResponse(t.Context(), "cp-msg", "hi", "", "", nil, c.emit)

	if err == nil || !strings.Contains(err.Error(), "OpenCode event stream disconnected") {
		t.Fatalf("got error %v, want the stable disconnect message", err)
	}
	// The raw transport failure stays out of the user-facing message.
	if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "read error") {
		t.Errorf("transport detail leaked into %q", err)
	}
	last := lastTokenContent(c)
	if last != finalText {
		t.Fatalf("last token content = %q, want %q (final state not recovered)", last, finalText)
	}
}

// TestStreamTransportDropWithoutRecoverableOutput covers the same drop when
// OpenCode has nothing persisted: the prompt still fails cleanly, with no tokens.
func TestStreamTransportDropWithoutRecoverableOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			writeSSEEvents(w, map[string]any{"type": "server.connected"})
			panic(http.ErrAbortHandler)
		case strings.HasSuffix(r.URL.Path, "/prompt_async"):
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/message"):
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	server.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)

	b := testBridge()
	b.opencodeBaseURL = server.URL
	b.httpClient = server.Client()
	b.sseInactivityTimeout = 30 * time.Second
	b.setOpencodeSessionID("ses_test")

	c := &collector{}
	err := b.streamOpencodeResponse(t.Context(), "cp-msg", "hi", "", "", nil, c.emit)

	if err == nil || !strings.Contains(err.Error(), "OpenCode event stream disconnected") {
		t.Fatalf("got error %v, want the stable disconnect message", err)
	}
	if len(c.events) != 0 {
		t.Fatalf("got events %v, want none", c.types())
	}
}

// promptMessageID reads the OpenCode message id out of a prompt_async body.
func promptMessageID(r *http.Request) string {
	var body struct {
		MessageID string `json:"messageID"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	return body.MessageID
}

// writeSSEEvents writes each payload as one SSE data frame and flushes.
func writeSSEEvents(w http.ResponseWriter, payloads ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range payloads {
		data, _ := json.Marshal(payload)
		w.Write([]byte("data: " + string(data) + "\n\n"))
	}
	w.(http.Flusher).Flush()
}

// lastTokenContent returns the content of the last token event, or "".
func lastTokenContent(c *collector) string {
	content := ""
	for _, e := range c.events {
		if e["type"] == "token" {
			content, _ = e["content"].(string)
		}
	}
	return content
}
