package sandbox

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubCP starts a control-plane stub and points the env-based config at it.
func stubCP(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("CONTROL_PLANE_URL", srv.URL)
	t.Setenv("SANDBOX_AUTH_TOKEN", "test-token")
	t.Setenv("SESSION_ID", "sess-1")
	t.Setenv("OPENCODE_PORT", "1") // present so config.Resolve skips metadata
	return srv
}

func TestParseCredentialAttrs(t *testing.T) {
	in := "protocol=https\nhost=github.com\npath=foo/bar\n\nignored=after-blank\n"
	got := parseCredentialAttrs(strings.NewReader(in))
	if got["host"] != "github.com" || got["protocol"] != "https" || got["path"] != "foo/bar" {
		t.Fatalf("parseCredentialAttrs = %#v", got)
	}
	if _, ok := got["ignored"]; ok {
		t.Fatalf("attrs past the blank line should be ignored: %#v", got)
	}
}

func TestGitCredentialGet(t *testing.T) {
	stubCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/sessions/sess-1/scm-credentials" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("host"); got != "github.com" {
			t.Errorf("host = %s", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth = %s", got)
		}
		_, _ = w.Write([]byte(`{"username":"x-access-token","password":"ghs_secret"}`))
	})

	var out bytes.Buffer
	in := strings.NewReader("protocol=https\nhost=github.com\n\n")
	if err := GitCredential("get", in, &out); err != nil {
		t.Fatalf("GitCredential: %v", err)
	}
	want := "username=x-access-token\npassword=ghs_secret\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestGitCredentialTokenFallbackAndDefaultUser(t *testing.T) {
	stubCP(t, func(w http.ResponseWriter, _ *http.Request) {
		// Only a token; username should default and password fall back to token.
		_, _ = w.Write([]byte(`{"token":"ghs_tok"}`))
	})

	var out bytes.Buffer
	if err := GitCredential("get", strings.NewReader("host=github.com\n\n"), &out); err != nil {
		t.Fatalf("GitCredential: %v", err)
	}
	want := "username=x-access-token\npassword=ghs_tok\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestGitCredentialStoreEraseNoop(t *testing.T) {
	called := false
	stubCP(t, func(http.ResponseWriter, *http.Request) { called = true })
	for _, op := range []string{"store", "erase", ""} {
		var out bytes.Buffer
		if err := GitCredential(op, strings.NewReader("host=github.com\n\n"), &out); err != nil {
			t.Fatalf("GitCredential(%q): %v", op, err)
		}
		if out.Len() != 0 {
			t.Fatalf("op %q produced output %q", op, out.String())
		}
	}
	if called {
		t.Fatal("store/erase must not call the control plane")
	}
}
