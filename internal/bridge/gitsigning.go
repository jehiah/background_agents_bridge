package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/sandbox"
)

// Commit signing is delegated: the control plane holds the private key and
// signs commit buffers on request, and the sandbox only learns the committer
// identity and the public key. This file installs that configuration into git;
// the signing request itself is `bridge git-sign` (internal/sandbox/gitsign.go).

const signingConfigFetchTimeout = 30 * time.Second

// signingConfigKeys are the git settings this port owns. They are unset as a
// group when signing is off, so a session never inherits a stale signer.
var signingConfigKeys = []string{
	"author.name",
	"author.email",
	"committer.name",
	"committer.email",
	"gpg.format",
	"gpg.ssh.program",
	"user.signingkey",
	"commit.gpgsign",
}

var (
	committerEmailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	publicKeyRE      = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]+={0,2}$`)
)

// signingConfig is the control plane's per-session commit-signing configuration.
type signingConfig struct {
	Enabled        bool   `json:"enabled"`
	CommitterName  string `json:"committerName"`
	CommitterEmail string `json:"committerEmail"`
	PublicKey      string `json:"publicKey"`
}

// validate rejects a configuration this sandbox cannot faithfully install.
// Bounds match upstream's model so both runtimes refuse the same payloads.
func (c signingConfig) validate() error {
	invalid := errors.New("Invalid commit signing configuration") //nolint:staticcheck // ST1005: wire-compatible message
	if !c.Enabled {
		return nil
	}
	if n := len(c.CommitterName); n < 1 || n > 256 || strings.TrimSpace(c.CommitterName) == "" {
		return invalid
	}
	if n := len(c.CommitterEmail); n < 3 || n > 320 || !committerEmailRE.MatchString(c.CommitterEmail) {
		return invalid
	}
	if !publicKeyRE.MatchString(c.PublicKey) {
		return invalid
	}
	return nil
}

// fetchSigningConfig asks the control plane how (and whether) to sign. A 404
// means the control plane predates delegated signing, which is reported as
// "disabled" rather than as an error — unlike upstream, this port has to keep
// working against a control plane that has not shipped #1030.
func (b *AgentBridge) fetchSigningConfig(ctx context.Context) (signingConfig, error) {
	unavailable := errors.New("Commit signing configuration unavailable") //nolint:staticcheck // ST1005: wire-compatible message

	ctx, cancel := context.WithTimeout(ctx, signingConfigFetchTimeout)
	defer cancel()

	endpoint := strings.TrimSuffix(b.controlPlaneURL, "/") +
		"/sessions/" + url.PathEscape(b.sessionID) + "/commit-signing"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return signingConfig{}, unavailable
	}
	request.Header.Set("Authorization", "Bearer "+b.authToken)

	response, err := b.httpClient.Do(request)
	if err != nil {
		return signingConfig{}, unavailable
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotFound {
		b.log.Debug("git.signing_unsupported")
		return signingConfig{Enabled: false}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return signingConfig{}, unavailable
	}

	var config signingConfig
	if err := json.NewDecoder(io.LimitReader(response.Body, maxSigningConfigBytes)).Decode(&config); err != nil {
		return signingConfig{}, errors.New("Invalid commit signing configuration") //nolint:staticcheck // ST1005: wire-compatible message
	}
	if err := config.validate(); err != nil {
		return signingConfig{}, err
	}
	return config, nil
}

// maxSigningConfigBytes bounds the configuration document; it holds three short
// strings.
const maxSigningConfigBytes = 64 * 1024

// applySigningConfig installs the signing settings and the prompt's author
// identity into git. A nil author is agent-only attribution: commits are
// authored by the configured committer when signing is on, and by agent — the
// agent's own identity, see agentGitUser — when it is off.
//
// Every call reconciles the full set of owned keys rather than skipping work
// when the configuration is unchanged. What is installed can drift underneath
// us — the agent has a shell and can edit git config — so "we already wrote
// this" is not evidence that it is still there.
//
// Everything is written with `git config --global` — see configureGitIdentity
// for why this port does not write per-checkout config.
func (b *AgentBridge) applySigningConfig(ctx context.Context, config signingConfig, author *GitUser, agent GitUser) error {
	b.signingMu.Lock()
	defer b.signingMu.Unlock()

	if !config.Enabled {
		for _, key := range signingConfigKeys {
			if err := b.unsetGitConfig(ctx, key); err != nil {
				return err
			}
		}
		effective := agent
		if author != nil {
			effective = *author
		}
		if err := b.setGitConfig(ctx, map[string]string{
			"user.name":  effective.Name,
			"user.email": effective.Email,
		}); err != nil {
			return err
		}
		return nil
	}

	signer, ok := sandbox.SignerPath()
	if !ok {
		// Refusing is the safe failure: the alternative is a session that
		// silently produces unsigned commits under a signing deployment.
		return fmt.Errorf("commit signing is enabled but %s is not installed", sandbox.GitSignerCommand)
	}

	if err := b.setGitConfig(ctx, map[string]string{
		"committer.name":  config.CommitterName,
		"committer.email": config.CommitterEmail,
		"gpg.format":      "ssh",
		"gpg.ssh.program": signer,
		"user.signingkey": "key::" + config.PublicKey,
		"commit.gpgsign":  "true",
	}); err != nil {
		return err
	}

	// Without a trusted user to attribute to, the machine identity authors the
	// commit too, so the signature and the author agree.
	effective := GitUser{Name: config.CommitterName, Email: config.CommitterEmail}
	if author != nil {
		effective = *author
	}
	if err := b.setGitConfig(ctx, map[string]string{
		"author.name":  effective.Name,
		"author.email": effective.Email,
		"user.name":    effective.Name,
		"user.email":   effective.Email,
	}); err != nil {
		return err
	}
	return nil
}

// setGitConfig writes keys in a stable order so failures are reproducible.
// --replace-all collapses a key that already holds several values into the one
// we want; a plain write against a multivalued key fails instead, which would
// leave the drift it was meant to repair in place.
func (b *AgentBridge) setGitConfig(ctx context.Context, values map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if err := b.runGitConfig(ctx, key, "--replace-all", key, values[key]); err != nil {
			return err
		}
	}
	return nil
}

// unsetGitConfig removes a key, tolerating "not set" (git exit code 5).
func (b *AgentBridge) unsetGitConfig(ctx context.Context, key string) error {
	err := b.runGitConfig(ctx, key, "--unset-all", key)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 5 {
		return nil
	}
	return err
}

// runGitConfig runs one `git config --global` invocation. key names it for the
// error message; the value is never included, so a config error cannot echo
// identity or key material into the logs.
func (b *AgentBridge) runGitConfig(ctx context.Context, key string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, gitConfigTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, "git", append([]string{"config", "--global"}, args...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("git config --global %s: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
