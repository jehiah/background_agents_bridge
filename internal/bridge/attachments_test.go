package bridge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestParseSessionImageAttachments(t *testing.T) {
	valid := map[string]any{"attachmentId": "abc-123", "name": "shot.png", "mimeType": "image/png"}

	cases := []struct {
		name         string
		value        any
		wantAccepted int
		wantRejected int
	}{
		{"nil", nil, 0, 0},
		{"not_a_list", "oops", 0, 1},
		{"one_valid", []any{valid}, 1, 0},
		{"bad_mime", []any{map[string]any{"attachmentId": "a", "name": "x", "mimeType": "image/svg+xml"}}, 0, 1},
		{"bad_id", []any{map[string]any{"attachmentId": "a/b", "name": "x", "mimeType": "image/png"}}, 0, 1},
		{"empty_name", []any{map[string]any{"attachmentId": "a", "name": "", "mimeType": "image/png"}}, 0, 1},
		{"not_object", []any{"nope"}, 0, 1},
		{
			"over_cap",
			[]any{valid, valid, valid, valid, valid, valid, valid, valid}, // 8 → cap 6
			6, 2,
		},
		{"mixed", []any{valid, "junk", valid}, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rejected := parseSessionImageAttachments(tc.value)
			if len(got) != tc.wantAccepted {
				t.Errorf("accepted = %d, want %d", len(got), tc.wantAccepted)
			}
			if rejected != tc.wantRejected {
				t.Errorf("rejected = %d, want %d", rejected, tc.wantRejected)
			}
		})
	}
}

func TestBuildFileParts(t *testing.T) {
	parts := buildFileParts([]hydratedAttachment{
		{name: "a.png", mimeType: "image/png", content: "QUJD"},
	})
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	p := parts[0]
	if p["type"] != "file" || p["mime"] != "image/png" || p["filename"] != "a.png" ||
		p["url"] != "data:image/png;base64,QUJD" {
		t.Errorf("unexpected file part: %+v", p)
	}
}

// TestBuildPromptRequestBodyFileParts locks the part ordering: the text part is
// always first, file parts follow.
func TestBuildPromptRequestBodyFileParts(t *testing.T) {
	fileParts := buildFileParts([]hydratedAttachment{{name: "a.png", mimeType: "image/png", content: "QUJD"}})
	body := buildPromptRequestBody("hello", "", "msg_x", "", fileParts)
	got, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"messageID":"msg_x","parts":[{"text":"hello","type":"text"},{"filename":"a.png","mime":"image/png","type":"file","url":"data:image/png;base64,QUJD"}]}`
	if string(got) != want {
		t.Errorf("body mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestProcessAttachments hydrates the good attachment, drops the failed one, and
// surfaces a media warning for the failure — sending the bearer token to the
// session-scoped attachments URL.
func TestProcessAttachments(t *testing.T) {
	img := []byte("\x89PNG-fake-bytes")

	var mu sync.Mutex
	var lastAuth, lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastAuth, lastPath = r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/good") {
			_, _ = w.Write(img)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := testBridge()
	b.controlPlaneURL = srv.URL
	b.sessionID = "sess"
	b.authToken = "tok"
	b.httpClient = &http.Client{}

	out := b.processAttachments(t.Context(), []resolvedAttachment{
		{attachmentID: "good", name: "a.png", mimeType: "image/png"},
		{attachmentID: "bad", name: "b.png", mimeType: "image/png"},
	})

	if len(out) != 1 {
		t.Fatalf("expected 1 hydrated attachment, got %d", len(out))
	}
	if out[0].name != "a.png" || out[0].content != base64.StdEncoding.EncodeToString(img) {
		t.Errorf("unexpected hydrated attachment: %+v", out[0])
	}

	mu.Lock()
	auth, path := lastAuth, lastPath
	mu.Unlock()
	if auth != "Bearer tok" {
		t.Errorf("authorization = %q, want %q", auth, "Bearer tok")
	}
	if !strings.HasPrefix(path, "/sessions/sess/attachments/") {
		t.Errorf("path = %q, want session-scoped attachments URL", path)
	}

	// The failed download surfaced exactly one media warning.
	if len(b.eventBuffer) != 1 {
		t.Fatalf("expected 1 buffered warning, got %d: %+v", len(b.eventBuffer), b.eventBuffer)
	}
	if e := b.eventBuffer[0]; e["type"] != "warning" || e["scope"] != "media" {
		t.Errorf("unexpected warning event: %+v", e)
	}
}

// TestDownloadAttachmentRejectsOversize verifies the hard size cap.
func TestDownloadAttachmentRejectsOversize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxAttachmentBytes+10))
	}))
	defer srv.Close()

	b := testBridge()
	b.controlPlaneURL = srv.URL
	b.sessionID = "sess"
	b.authToken = "tok"
	b.httpClient = &http.Client{}

	if _, err := b.downloadAttachment(t.Context(), "big"); err == nil {
		t.Error("expected oversize download to be rejected")
	}
}
