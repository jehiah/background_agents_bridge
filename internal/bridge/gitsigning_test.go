package bridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKvS2ZNPXQ1kEXEZ4hVBb0nWm5NLPjSpQx0mQ1Aa2Ecm"

// signingBridge returns a bridge pointed at a control plane that answers the
// commit-signing endpoint with body, plus a HOME the git config lands in.
func signingBridge(t *testing.T, status int, body string) (*AgentBridge, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/sessions/ses_1/commit-signing" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	b := testBridge()
	b.controlPlaneURL = server.URL
	b.sessionID = "ses_1"
	b.authToken = "tok"
	return b, home
}

// installFakeSigner puts an oi-git-sign on $PATH and returns its path.
func installFakeSigner(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "oi-git-sign")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

// globalConfig reads one git config value from home's config, "" when unset.
func globalConfig(t *testing.T, home, key string) string {
	t.Helper()
	command := exec.Command("git", "config", "--global", "--get", key)
	command.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "" // not set
		}
		t.Fatalf("git config --get %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

func TestConfigureGitIdentityWithoutSigning(t *testing.T) {
	b, home := signingBridge(t, http.StatusOK, `{"enabled":false}`)

	// Agent-only attribution still needs an identity, or git refuses to commit.
	if err := b.configureGitIdentity(t.Context(), nil, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	if got := globalConfig(t, home, "user.name"); got != fallbackGitUser.Name {
		t.Errorf("user.name = %q, want %q", got, fallbackGitUser.Name)
	}

	if err := b.configureGitIdentity(t.Context(), &GitUser{Name: "Jane", Email: "jane@example.com"}, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	if got := globalConfig(t, home, "user.email"); got != "jane@example.com" {
		t.Errorf("user.email = %q", got)
	}
	if got := globalConfig(t, home, "commit.gpgsign"); got != "" {
		t.Errorf("commit.gpgsign = %q, want unset when signing is off", got)
	}
}

func TestConfigureGitIdentityInstallsSigning(t *testing.T) {
	signer := installFakeSigner(t)
	b, home := signingBridge(t, http.StatusOK,
		`{"enabled":true,"committerName":"Open-Inspect","committerEmail":"bot@example.com",
		  "publicKey":"`+testPublicKey+`"}`)

	if err := b.configureGitIdentity(t.Context(), &GitUser{Name: "Jane", Email: "jane@example.com"}, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	// The user authors the commit; the machine identity commits and signs it.
	for key, want := range map[string]string{
		"author.name":     "Jane",
		"author.email":    "jane@example.com",
		"user.name":       "Jane",
		"committer.name":  "Open-Inspect",
		"committer.email": "bot@example.com",
		"gpg.format":      "ssh",
		"gpg.ssh.program": signer,
		"user.signingkey": "key::" + testPublicKey,
		"commit.gpgsign":  "true",
	} {
		if got := globalConfig(t, home, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// Without a trusted user the commit is authored by the signer, so the
	// signature and the author agree.
	if err := b.configureGitIdentity(t.Context(), nil, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	if got := globalConfig(t, home, "author.name"); got != "Open-Inspect" {
		t.Errorf("agent-only author.name = %q", got)
	}
}

func TestConfigureGitIdentityClearsStaleSigning(t *testing.T) {
	installFakeSigner(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	enabled := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if enabled {
			_, _ = w.Write([]byte(`{"enabled":true,"committerName":"Open-Inspect",
				"committerEmail":"bot@example.com","publicKey":"` + testPublicKey + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"enabled":false}`))
	}))
	defer server.Close()

	b := testBridge()
	b.controlPlaneURL, b.sessionID, b.authToken = server.URL, "ses_1", "tok"

	if err := b.configureGitIdentity(t.Context(), nil, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	// Signing is turned off mid-session: nothing may keep pointing git at the
	// signer, or every later commit fails.
	enabled = false
	if err := b.configureGitIdentity(t.Context(), nil, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	for _, key := range signingConfigKeys {
		if got := globalConfig(t, home, key); got != "" {
			t.Errorf("%s = %q, want unset", key, got)
		}
	}
	if got := globalConfig(t, home, "user.name"); got != fallbackGitUser.Name {
		t.Errorf("user.name = %q, want the agent identity", got)
	}
}

func TestConfigureGitIdentityUnsupportedControlPlane(t *testing.T) {
	b, home := signingBridge(t, http.StatusNotFound, "")

	// A control plane that predates delegated signing answers 404. That is not
	// an error here: the prompt runs, unsigned.
	if err := b.configureGitIdentity(t.Context(), &GitUser{Name: "Jane", Email: "jane@example.com"}, fallbackGitUser); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	if got := globalConfig(t, home, "user.name"); got != "Jane" {
		t.Errorf("user.name = %q", got)
	}
}

func TestConfigureGitIdentityFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server_error", http.StatusInternalServerError, ""},
		{"unparseable", http.StatusOK, "not json"},
		{"invalid_public_key", http.StatusOK,
			`{"enabled":true,"committerName":"Bot","committerEmail":"bot@example.com","publicKey":"ssh-rsa AAAA"}`},
		{"invalid_committer_email", http.StatusOK,
			`{"enabled":true,"committerName":"Bot","committerEmail":"nope","publicKey":"` + testPublicKey + `"}`},
		{"blank_committer_name", http.StatusOK,
			`{"enabled":true,"committerName":"  ","committerEmail":"bot@example.com","publicKey":"` + testPublicKey + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installFakeSigner(t)
			b, _ := signingBridge(t, tc.status, tc.body)
			// Committing under an identity we could not verify is worse than
			// refusing the prompt.
			if err := b.configureGitIdentity(t.Context(), nil, fallbackGitUser); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestConfigureGitIdentityRequiresInstalledSigner(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b, _ := signingBridge(t, http.StatusOK,
		`{"enabled":true,"committerName":"Bot","committerEmail":"bot@example.com","publicKey":"`+testPublicKey+`"}`)

	// Signing is configured but the shim is missing: fail rather than silently
	// producing unsigned commits.
	err := b.configureGitIdentity(t.Context(), nil, fallbackGitUser)
	if err == nil || !strings.Contains(err.Error(), "oi-git-sign") {
		t.Errorf("err = %v, want it to name the missing signer", err)
	}
}

func TestConfigureGitIdentityUsesConfiguredAgentIdentity(t *testing.T) {
	b, home := signingBridge(t, http.StatusOK, `{"enabled":false}`)
	setGlobalGitConfig(t, "openinspect.name", "Acme Bot")
	setGlobalGitConfig(t, "openinspect.email", "bot@acme.test")

	// The deployment named its own bot, so an unattributed commit is authored
	// as that bot instead of the built-in OpenInspect identity.
	if err := b.configureGitIdentity(t.Context(), nil, b.agentGitUser(t.Context())); err != nil {
		t.Fatalf("configureGitIdentity: %v", err)
	}
	if got := globalConfig(t, home, "user.name"); got != "Acme Bot" {
		t.Errorf("user.name = %q", got)
	}
	if got := globalConfig(t, home, "user.email"); got != "bot@acme.test" {
		t.Errorf("user.email = %q", got)
	}
}
