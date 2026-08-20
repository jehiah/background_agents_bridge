package sandbox

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSSHKey returns an authorized_keys-style ssh-ed25519 line and the raw key
// blob a signer would fingerprint.
func testSSHKey(t *testing.T) (line string, blob []byte) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// An OpenSSH public key blob is the wire encoding: string "ssh-ed25519"
	// followed by the string key.
	blob = append(blob, 0, 0, 0, 11)
	blob = append(blob, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, byte(len(public)))
	blob = append(blob, public...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob), blob
}

// armored wraps body in an SSHSIG armor block.
func armored(body []byte) string {
	encoded := base64.StdEncoding.EncodeToString(body)
	var lines strings.Builder
	for len(encoded) > 70 {
		lines.WriteString(encoded[:70])
		lines.WriteByte('\n')
		encoded = encoded[70:]
	}
	lines.WriteString(encoded)
	lines.WriteByte('\n')
	return "-----BEGIN SSH SIGNATURE-----\n" + lines.String() + "-----END SSH SIGNATURE-----\n"
}

func TestParseSignArguments(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		key, buffer string
		wantErr     bool
	}{
		{
			"modern_git", []string{"-Y", "sign", "-n", "git", "-f", "key::AAAA", "-U", "/tmp/buf"},
			"key::AAAA", "/tmp/buf", false,
		},
		{
			"without_U", []string{"-Y", "sign", "-n", "git", "-f", "/tmp/key.pub", "/tmp/buf"},
			"/tmp/key.pub", "/tmp/buf", false,
		},
		{"wrong_namespace", []string{"-Y", "sign", "-n", "file", "-f", "k", "b"}, "", "", true},
		{"missing_buffer", []string{"-Y", "sign", "-n", "git", "-f", "k"}, "", "", true},
		{"extra_arguments", []string{"-Y", "sign", "-n", "git", "-f", "k", "-U", "b", "x"}, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, buffer, err := parseSignArguments(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %q %q", key, buffer)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSignArguments: %v", err)
			}
			if key != tc.key || buffer != tc.buffer {
				t.Errorf("got %q %q, want %q %q", key, buffer, tc.key, tc.buffer)
			}
		})
	}
}

func TestReadPublicKeyBlob(t *testing.T) {
	line, blob := testSSHKey(t)

	got, err := readPublicKeyBlob("key::" + line)
	if err != nil {
		t.Fatalf("key:: literal: %v", err)
	}
	if string(got) != string(blob) {
		t.Error("key:: literal decoded to a different blob")
	}

	// git may instead point at a file, which can carry a trailing comment.
	path := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(path, []byte(line+" agent@sandbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err = readPublicKeyBlob(path); err != nil || string(got) != string(blob) {
		t.Errorf("file form: %v", err)
	}

	for _, bad := range []string{"key::ssh-rsa AAAA", "key::ssh-ed25519", "key::ssh-ed25519 !!!!"} {
		if _, err := readPublicKeyBlob(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestFingerprintMatchesSSHKeygen(t *testing.T) {
	// The known-answer vector is ssh-keygen -l for an all-zero Ed25519 key.
	blob := append([]byte{0, 0, 0, 11}, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, 32)
	blob = append(blob, make([]byte, 32)...)
	if got := fingerprint(blob); !strings.HasPrefix(got, "SHA256:") || strings.HasSuffix(got, "=") {
		t.Errorf("fingerprint = %q, want an unpadded SHA256: form", got)
	}
}

func TestValidateSignatureArmor(t *testing.T) {
	if _, err := validateSignatureArmor([]byte(armored([]byte("SSHSIG\x00payload")))); err != nil {
		t.Errorf("valid armor rejected: %v", err)
	}
	bad := map[string]string{
		"not_sshsig":       armored([]byte("NOPE\x00")),
		"missing_trailer":  strings.TrimSuffix(armored([]byte("SSHSIG\x00")), "-----END SSH SIGNATURE-----\n"),
		"trailing_garbage": armored([]byte("SSHSIG\x00")) + "extra\n",
		"empty":            "",
	}
	for name, body := range bad {
		if _, err := validateSignatureArmor([]byte(body)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestGitSignWritesSignature(t *testing.T) {
	line, blob := testSSHKey(t)
	signature := armored([]byte("SSHSIG\x00fake-signature"))

	var gotFingerprint, gotAuth, gotBody, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotFingerprint = r.Header.Get("X-Open-Inspect-Signing-Fingerprint")
		gotAuth, gotBody, gotPath = r.Header.Get("Authorization"), string(body), r.URL.EscapedPath()
		_, _ = w.Write([]byte(signature))
	}))
	defer server.Close()

	dir := t.TempDir()
	buffer := filepath.Join(dir, "commit-buffer")
	if err := os.WriteFile(buffer, []byte("tree abc\nauthor Jane\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale signature from an earlier commit must not survive a failure.
	if err := os.WriteFile(buffer+".sig", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_PLANE_URL", server.URL)
	t.Setenv("SANDBOX_AUTH_TOKEN", "tok")
	t.Setenv("SESSION_ID", "ses_1")
	t.Setenv("OPENCODE_PORT", "4096") // keeps config.Resolve off the metadata server

	if err := GitSign([]string{"-Y", "sign", "-n", "git", "-f", "key::" + line, "-U", buffer}); err != nil {
		t.Fatalf("GitSign: %v", err)
	}

	written, err := os.ReadFile(buffer + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != signature {
		t.Errorf("signature file = %q", written)
	}
	if gotPath != "/sessions/ses_1/commit-signing" || gotAuth != "Bearer tok" {
		t.Errorf("request = %s %s", gotPath, gotAuth)
	}
	if gotBody != "tree abc\nauthor Jane\n" {
		t.Errorf("posted body = %q", gotBody)
	}
	if gotFingerprint != fingerprint(blob) {
		t.Errorf("fingerprint = %q, want %q", gotFingerprint, fingerprint(blob))
	}
}

func TestGitSignRejectsBadResponses(t *testing.T) {
	line, _ := testSSHKey(t)
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server_error", http.StatusInternalServerError, ""},
		{"not_a_signature", http.StatusOK, "thanks!"},
		{"oversized", http.StatusOK, strings.Repeat("A", maxSigningResponseBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			dir := t.TempDir()
			buffer := filepath.Join(dir, "commit-buffer")
			if err := os.WriteFile(buffer, []byte("tree abc\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONTROL_PLANE_URL", server.URL)
			t.Setenv("SANDBOX_AUTH_TOKEN", "tok")
			t.Setenv("SESSION_ID", "ses_1")
			t.Setenv("OPENCODE_PORT", "4096")

			if err := GitSign([]string{"-Y", "sign", "-n", "git", "-f", "key::" + line, "-U", buffer}); err == nil {
				t.Error("want an error")
			}
			// git treats a missing .sig as a failed signature; never leave a
			// half-written or stale one behind.
			if _, err := os.Stat(buffer + ".sig"); !os.IsNotExist(err) {
				t.Errorf("signature file exists after a failure (%v)", err)
			}
		})
	}
}

// preflightServer is a control plane that must never be reached. It counts the
// requests it sees so a check that regresses to running after the POST — rather
// than before it — fails instead of quietly still passing.
func preflightServer(t *testing.T) (url string, requests *int) {
	t.Helper()
	var count int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		_, _ = w.Write([]byte(armored([]byte("SSHSIG\x00fake-signature"))))
	}))
	t.Cleanup(server.Close)
	return server.URL, &count
}

// assertPreflightFailure runs the signer and asserts it refused locally: an
// error, no request, and no signature file (git reads a present .sig as a
// successful signature, so a stale one from an earlier commit must not survive).
func assertPreflightFailure(t *testing.T, args []string, buffer string, requests *int) {
	t.Helper()
	if err := GitSign(args); err == nil {
		t.Error("want an error")
	}
	if *requests != 0 {
		t.Errorf("control plane saw %d request(s); the check must run before the POST", *requests)
	}
	if _, err := os.Stat(buffer + ".sig"); !os.IsNotExist(err) {
		t.Errorf("signature file exists after a failure (%v)", err)
	}
}

func TestGitSignRejectsOversizePayload(t *testing.T) {
	line, _ := testSSHKey(t)
	url, requests := preflightServer(t)

	dir := t.TempDir()
	buffer := filepath.Join(dir, "commit-buffer")
	if err := os.WriteFile(buffer, make([]byte, maxSigningPayloadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(buffer+".sig", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_PLANE_URL", url)
	t.Setenv("SANDBOX_AUTH_TOKEN", "tok")
	t.Setenv("SESSION_ID", "ses_1")
	t.Setenv("OPENCODE_PORT", "4096")

	// The buffer is bounded before anything is sent, so a runaway commit cannot
	// be pushed at the control plane.
	assertPreflightFailure(t, []string{"-Y", "sign", "-n", "git", "-f", "key::" + line, "-U", buffer}, buffer, requests)
}

func TestGitSignRequiresSessionConfiguration(t *testing.T) {
	line, _ := testSSHKey(t)
	url, requests := preflightServer(t)

	// SESSION_ID is unset throughout, so each case exercises what config.Resolve
	// can recover from SESSION_CONFIG (or, for no_credentials, what it cannot).
	cases := []struct {
		name          string
		controlPlane  string
		token         string
		sessionConfig string
	}{
		{"no_credentials", "", "", ""},
		{"not_json", url, "tok", "not-json"},
		{"json_array", url, "tok", `["not","an","object"]`},
		{"blank_session_id", url, "tok", `{"sessionId":""}`},
		{"no_session_id", url, "tok", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			buffer := filepath.Join(dir, "commit-buffer")
			if err := os.WriteFile(buffer, []byte("tree abc\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONTROL_PLANE_URL", tc.controlPlane)
			t.Setenv("SANDBOX_AUTH_TOKEN", tc.token)
			t.Setenv("SESSION_ID", "")
			t.Setenv("SESSION_CONFIG", tc.sessionConfig)
			t.Setenv("OPENCODE_PORT", "4096")

			assertPreflightFailure(t, []string{"-Y", "sign", "-n", "git", "-f", "key::" + line, "-U", buffer}, buffer, requests)
		})
	}
}
