package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// stubCP starts a control-plane stub and points the env-based config at it.
// HOME is redirected to a temp dir so the credential cache writes there rather
// than into the developer's real home.
func stubCP(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("CONTROL_PLANE_URL", srv.URL)
	t.Setenv("SANDBOX_AUTH_TOKEN", "test-token")
	t.Setenv("SESSION_ID", "sess-1")
	t.Setenv("OPENCODE_PORT", "1") // present so config.Resolve skips metadata
	t.Setenv("HOME", t.TempDir())
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

// TestGitCredentialCachesUntilExpiry verifies that a fresh response with a
// future expires_at_epoch_ms is written to the on-disk cache and reused on the
// next call (no second control-plane request).
func TestGitCredentialCachesUntilExpiry(t *testing.T) {
	var calls atomic.Int32
	expiresMs := time.Now().Add(10 * time.Minute).UnixMilli()
	stubCP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"username":"x-access-token","password":"ghs_cached","expires_at_epoch_ms":` +
			itoa(expiresMs) + `}`))
	})

	for i := range 2 {
		var out bytes.Buffer
		if err := GitCredential("get", strings.NewReader("host=github.com\n\n"), &out); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if want := "username=x-access-token\npassword=ghs_cached\n"; out.String() != want {
			t.Fatalf("call %d output = %q, want %q", i, out.String(), want)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("control plane called %d times, want 1 (cache should serve second call)", got)
	}

	// Confirm the cache file lives where the user expects.
	path := filepath.Join(os.Getenv("HOME"), ".credentials/scm-creds.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not written at %s: %v", path, err)
	}
}

// TestGitCredentialRefetchesExpired verifies that an entry whose expiry is in
// the past (or within the safety buffer) is discarded and refetched.
func TestGitCredentialRefetchesExpired(t *testing.T) {
	var calls atomic.Int32
	stubCP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		future := time.Now().Add(time.Hour).UnixMilli()
		_, _ = w.Write([]byte(`{"username":"x-access-token","password":"ghs_fresh","expires_at_epoch_ms":` +
			itoa(future) + `}`))
	})

	// Seed an expired entry into the cache directly.
	dir := filepath.Join(os.Getenv("HOME"), ".credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := map[string]controlplane.Credentials{
		"github.com": {Username: "x-access-token", Password: "ghs_stale", ExpiresAtEpochMs: 1},
	}
	b, _ := json.Marshal(cache)
	if err := os.WriteFile(filepath.Join(dir, "scm-creds.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := GitCredential("get", strings.NewReader("host=github.com\n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if want := "username=x-access-token\npassword=ghs_fresh\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("control plane calls = %d, want 1", got)
	}
}

// TestCredExpired covers the boundary cases of the expiry check directly.
func TestCredExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name string
		ms   int64
		want bool
	}{
		{"no expiry", 0, true},
		{"already past", now.Add(-time.Second).UnixMilli(), true},
		{"inside buffer", now.Add(10 * time.Second).UnixMilli(), true},
		{"safely future", now.Add(10 * time.Minute).UnixMilli(), false},
	}
	for _, tc := range cases {
		got := credExpired(controlplane.Credentials{ExpiresAtEpochMs: tc.ms}, now)
		if got != tc.want {
			t.Errorf("%s: credExpired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// itoa is a tiny base-10 int64 formatter so the test doesn't pull in strconv
// purely for two call sites.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
