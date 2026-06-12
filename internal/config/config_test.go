package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveFlagWins(t *testing.T) {
	t.Setenv("SANDBOX_ID", "env-sandbox")
	t.Setenv("CONTROL_PLANE_URL", "https://env.example")
	t.Setenv("SANDBOX_AUTH_TOKEN", "env-token")
	t.Setenv("SESSION_ID", "env-session")

	got := Resolve(Flags{
		SandboxID:       "flag-sandbox",
		SessionID:       "flag-session",
		ControlPlaneURL: "https://flag.example",
		AuthToken:       "flag-token",
		OpencodePort:    5000,
	})

	want := Resolved{
		SandboxID:       "flag-sandbox",
		SessionID:       "flag-session",
		ControlPlaneURL: "https://flag.example",
		AuthToken:       "flag-token",
		OpencodePort:    5000,
	}
	if got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestResolveEnvFallback(t *testing.T) {
	t.Setenv("SANDBOX_ID", "env-sandbox")
	t.Setenv("CONTROL_PLANE_URL", "https://env.example")
	t.Setenv("SANDBOX_AUTH_TOKEN", "env-token")
	t.Setenv("SESSION_ID", "env-session")
	t.Setenv("OPENCODE_PORT", "4242")

	got := Resolve(Flags{})
	want := Resolved{
		SandboxID:       "env-sandbox",
		SessionID:       "env-session",
		ControlPlaneURL: "https://env.example",
		AuthToken:       "env-token",
		OpencodePort:    4242,
	}
	if got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestResolveSessionConfig(t *testing.T) {
	t.Setenv("SANDBOX_ID", "s")
	t.Setenv("CONTROL_PLANE_URL", "https://x")
	t.Setenv("SANDBOX_AUTH_TOKEN", "t")
	t.Setenv("OPENCODE_PORT", "1")
	// SESSION_ID unset; SESSION_CONFIG carries the id like the opencode tools.
	t.Setenv("SESSION_CONFIG", `{"sessionId":"from-config"}`)

	if got := Resolve(Flags{}).SessionID; got != "from-config" {
		t.Fatalf("SessionID = %q, want %q", got, "from-config")
	}

	t.Setenv("SESSION_CONFIG", `{"session_id":"snake-config"}`)
	if got := Resolve(Flags{}).SessionID; got != "snake-config" {
		t.Fatalf("SessionID (snake) = %q, want %q", got, "snake-config")
	}
}

func TestResolveDefaultPort(t *testing.T) {
	t.Setenv("SANDBOX_ID", "s")
	t.Setenv("SESSION_ID", "x")
	t.Setenv("CONTROL_PLANE_URL", "https://x")
	t.Setenv("SANDBOX_AUTH_TOKEN", "t")
	// No OPENCODE_PORT, but everything else present so no metadata probe runs.
	if got := Resolve(Flags{}).OpencodePort; got != DefaultOpencodePort {
		t.Fatalf("OpencodePort = %d, want %d", got, DefaultOpencodePort)
	}
}

func TestResolveMetadataFallback(t *testing.T) {
	attrs := map[string]string{
		"sandbox_id":         "meta-sandbox",
		"session_id":         "meta-session",
		"control_plane_url":  "https://meta.example",
		"sandbox_auth_token": "meta-token",
		"opencode_port":      "7000",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Metadata-Flavor", "Google")
		key := strings.TrimPrefix(r.URL.Path, "/computeMetadata/v1/instance/attributes/")
		if v, ok := attrs[key]; ok {
			_, _ = w.Write([]byte(v))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))

	got := Resolve(Flags{})
	want := Resolved{
		SandboxID:       "meta-sandbox",
		SessionID:       "meta-session",
		ControlPlaneURL: "https://meta.example",
		AuthToken:       "meta-token",
		OpencodePort:    7000,
	}
	if got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestMissing(t *testing.T) {
	r := Resolved{SandboxID: "s", ControlPlaneURL: "u"}
	got := r.Missing("sandbox_id", "session_id", "control_plane_url", "sandbox_auth_token")
	want := []string{"session_id", "sandbox_auth_token"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
}
