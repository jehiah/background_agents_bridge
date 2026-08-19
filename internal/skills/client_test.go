package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientUsesSessionURLAndSandboxBearerAuth(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RequestURI is the unescaped-on-the-wire form, so a session id
		// containing "/" must show up percent-encoded.
		gotPath = r.RequestURI
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"schemaVersion":1}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/", "session/one", "sandbox-token")
	raw, err := client.FetchInstallation(t.Context())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(raw) != `{"schemaVersion":1}` {
		t.Fatalf("got body %q", raw)
	}
	if gotPath != "/sessions/session%2Fone/sandbox-skills" {
		t.Fatalf("got path %q", gotPath)
	}
	if gotAuth != "Bearer sandbox-token" {
		t.Fatalf("got auth %q", gotAuth)
	}
}

func TestClientRetriesTransientFetchFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "session", "token")
	raw, err := client.FetchInstallation(t.Context())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(raw) != "ok" || attempts != 3 {
		t.Fatalf("got body %q after %d attempts, want %q after 3", raw, attempts, "ok")
	}
}

func TestClientDoesNotRetryClientErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client := NewClient(server.URL, "session", "token")
	_, err := client.FetchInstallation(t.Context())
	if ErrorCode(err) != "fetch_failed" {
		t.Fatalf("got %v (code %q), want fetch_failed", err, ErrorCode(err))
	}
	if attempts != 1 {
		t.Fatalf("got %d attempts, want 1", attempts)
	}
}

func TestClientGivesUpAfterRepeatedFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, "session", "token")
	_, err := client.FetchInstallation(t.Context())
	if ErrorCode(err) != "fetch_failed" {
		t.Fatalf("got %v (code %q), want fetch_failed", err, ErrorCode(err))
	}
	if attempts != requestAttempts {
		t.Fatalf("got %d attempts, want %d", attempts, requestAttempts)
	}
}

func TestClientRejectsOversizedResponseWithoutRetrying(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// One byte past the cap is enough to be refused; write it in chunks so
		// the body is streamed rather than buffered whole by the test server.
		chunk := strings.Repeat("x", 1<<20)
		for written := 0; written <= MaxManagedSkillResponseBytes; written += len(chunk) {
			w.Write([]byte(chunk))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "session", "token")
	_, err := client.FetchInstallation(t.Context())
	if ErrorCode(err) != "installation_too_large" {
		t.Fatalf("got %v (code %q), want installation_too_large", err, ErrorCode(err))
	}
	if attempts != 1 {
		t.Fatalf("got %d attempts, want 1", attempts)
	}
}

func TestClientFetchStopsOnCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := NewClient(server.URL, "session", "token")
	if _, err := client.FetchInstallation(ctx); ErrorCode(err) != "fetch_failed" {
		t.Fatalf("got %v (code %q), want fetch_failed", err, ErrorCode(err))
	}
}
