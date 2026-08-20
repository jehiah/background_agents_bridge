package bridge

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestSnapshotReserve covers the split of a sandbox lifetime into prompt budget
// and snapshot reserve: the flat maximum for a long-lived sandbox, and a
// proportional slice for a short one, which the maximum would otherwise consume.
func TestSnapshotReserve(t *testing.T) {
	cases := []struct {
		lifetime, reserve time.Duration
	}{
		{7200 * time.Second, 900 * time.Second},  // the default: prompts get 6300s
		{14400 * time.Second, 900 * time.Second}, // still the flat maximum
		{600 * time.Second, 150 * time.Second},   // proportional: prompts get 450s
	}
	for _, tc := range cases {
		if got := snapshotReserve(tc.lifetime); got != tc.reserve {
			t.Errorf("snapshotReserve(%s) = %s, want %s", tc.lifetime, got, tc.reserve)
		}
	}
	if promptMaxDuration+promptCleanupTimeout > sandboxLifetime {
		t.Errorf("prompt budget %s + cleanup %s outlives the sandbox (%s)",
			promptMaxDuration, promptCleanupTimeout, sandboxLifetime)
	}
}

// roundTripFunc serves the bridge's OpenCode calls without a socket, so the
// prompt deadline can be exercised at its real length under synctest's fake
// clock. A handler that returns nil hangs until the request is cancelled, the
// way a real transport waits on a server that never answers.
type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	if resp := f(r); resp != nil {
		return resp, nil
	}
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// silentStream is an event stream that delivers one event and then goes quiet,
// ending only when the request is cancelled — as the real transport does.
func silentStream(r *http.Request) *http.Response {
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write([]byte("data: {\"type\":\"server.connected\"}\n\n"))
		<-r.Context().Done()
		_ = writer.CloseWithError(r.Context().Err())
	}()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}

// TestPromptDeadlineCoversStreamSetup covers a prompt that produces nothing: the
// deadline has to fire while the bridge is still opening the event stream, or
// waiting on a stream that opened and said nothing. Neither reaches a per-event
// check, which is why the deadline is absolute.
func TestPromptDeadlineCoversStreamSetup(t *testing.T) {
	cases := []struct {
		name, hang  string
		wantErr     string
		wantElapsed time.Duration
		wantAbort   bool
	}{
		{"sse_handshake", "/event", "prompt exceeded max duration", promptMaxDuration, true},
		{"silent_stream", "", "prompt exceeded max duration", promptMaxDuration, true},
		// Upstream needed the deadline to cover the prompt POST because that call
		// was unbounded; here it already carries opencodeRequestTimeout, so a hung
		// POST fails on its own long before the prompt deadline. The setup still
		// cannot hang, which is the property that matters.
		{"prompt_post", "/prompt_async", "context deadline exceeded", opencodeRequestTimeout, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				aborts := 0
				b := testBridge()
				b.opencodeBaseURL = "http://opencode.test"
				b.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
					switch {
					case tc.hang != "" && strings.HasSuffix(r.URL.Path, tc.hang):
						return nil // hangs until cancelled
					case r.URL.Path == "/event":
						return silentStream(r)
					case strings.HasSuffix(r.URL.Path, "/abort"):
						aborts++
						return jsonResponse(`{}`)
					case strings.HasSuffix(r.URL.Path, "/message"):
						return jsonResponse(`[]`)
					default:
						return jsonResponse(`{}`)
					}
				})}
				// Far longer than the prompt deadline, so the deadline is what
				// fires: silence would otherwise trip the inactivity timeout first.
				b.sseInactivityTimeout = 2 * promptMaxDuration
				b.setOpencodeSessionID("ses_test")

				start := time.Now()
				err := b.streamOpencodeResponse(t.Context(), "cp-msg", "hi", "", "", nil, (&collector{}).emit)

				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got error %v, want %q", err, tc.wantErr)
				}
				// Cleanup answers immediately here, so the timeout is the whole wait.
				if elapsed := time.Since(start); elapsed != tc.wantElapsed {
					t.Errorf("prompt ran %s, want exactly %s", elapsed, tc.wantElapsed)
				}
				if (aborts > 0) != tc.wantAbort {
					t.Errorf("OpenCode stop requests = %d, want any = %v", aborts, tc.wantAbort)
				}
			})
		})
	}
}

// TestPromptTimeoutBoundsCleanup covers the abort and final-state fetch hanging
// after a prompt times out. That cleanup runs inside the sandbox's snapshot
// reserve, so it cannot wait on the same silence that caused the timeout.
//
// The budget is shortened below opencodeRequestTimeout because each cleanup
// request carries that timeout of its own; only a smaller budget shows that the
// overall bound, not the per-request one, is what ends the wait.
func TestPromptTimeoutBoundsCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cleanup := opencodeRequestTimeout / 2
		defer func(old time.Duration) { promptCleanupTimeout = old }(promptCleanupTimeout)
		promptCleanupTimeout = cleanup

		b := testBridge()
		b.opencodeBaseURL = "http://opencode.test"
		b.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			switch {
			case r.URL.Path == "/event":
				return silentStream(r)
			case strings.HasSuffix(r.URL.Path, "/prompt_async"):
				return jsonResponse(`{}`)
			default:
				return nil // /abort and /message both hang
			}
		})}
		b.sseInactivityTimeout = 2 * promptMaxDuration
		b.setOpencodeSessionID("ses_test")

		start := time.Now()
		err := b.streamOpencodeResponse(t.Context(), "cp-msg", "hi", "", "", nil, (&collector{}).emit)

		if err == nil || !strings.Contains(err.Error(), "prompt exceeded max duration") {
			t.Fatalf("got error %v, want the max-duration message", err)
		}
		if elapsed := time.Since(start); elapsed != promptMaxDuration+cleanup {
			t.Errorf("prompt ran %s, want %s (deadline plus the cleanup bound)",
				elapsed, promptMaxDuration+cleanup)
		}
	})
}
