package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/config"
)

const (
	// maxSigningPayloadBytes bounds the unsigned commit buffer git hands us.
	maxSigningPayloadBytes = 1024 * 1024
	// maxSigningResponseBytes bounds the armored signature we accept back.
	maxSigningResponseBytes = 16 * 1024
	// maxPublicKeyBytes bounds the public key reference git points us at.
	maxPublicKeyBytes = 16 * 1024

	signingRequestTimeout = 30 * time.Second

	// stockSSHKeygenPath is the real ssh-keygen, which handles every invocation
	// that is not a signing request (verification, principal lookup, ...).
	stockSSHKeygenPath = "/usr/bin/ssh-keygen"
)

// signatureArmorRE matches an OpenSSH signature armor block exactly, so a
// truncated or otherwise malformed response is never written to disk as a
// signature.
var signatureArmorRE = regexp.MustCompile(
	`\A-----BEGIN SSH SIGNATURE-----\n((?:[A-Za-z0-9+/]+={0,2}\n)+)-----END SSH SIGNATURE-----\n\z`)

// GitSign is the sandbox side of delegated commit signing, invoked by git
// through the oi-git-sign shim in place of ssh-keygen.
//
// The signing key never enters the sandbox: this reads the unsigned commit
// buffer git wrote, sends it to the control plane along with the fingerprint of
// the configured public key, and writes the returned signature next to the
// buffer. Anything that is not a `-Y sign` request is handed to the real
// ssh-keygen, so verification still works locally.
//
// Errors are deliberately bounded phrases — git surfaces them to the agent, and
// they must not leak the commit, the response, or the auth token.
func GitSign(args []string) error {
	if len(args) < 2 || args[0] != "-Y" || args[1] != "sign" {
		return execStockSSHKeygen(args)
	}

	keyReference, bufferPath, err := parseSignArguments(args)
	if err != nil {
		return err
	}
	signaturePath := bufferPath + ".sig"
	if err := os.Remove(signaturePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("Unable to prepare commit signature output") //nolint:staticcheck // ST1005: user-facing git error
	}

	cfg := config.Resolve(config.Flags{})
	if cfg.ControlPlaneURL == "" || cfg.AuthToken == "" || cfg.SessionID == "" {
		return errors.New("Commit signing session configuration is unavailable") //nolint:staticcheck // ST1005: user-facing git error
	}

	publicKeyBlob, err := readPublicKeyBlob(keyReference)
	if err != nil {
		return err
	}
	payload, err := readBoundedFile(bufferPath, maxSigningPayloadBytes, "commit payload")
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return errors.New("Commit signing payload is empty") //nolint:staticcheck // ST1005: user-facing git error
	}

	armor, err := requestSignature(cfg, fingerprint(publicKeyBlob), payload)
	if err != nil {
		return err
	}
	return writeSignatureFile(signaturePath, armor)
}

// execStockSSHKeygen replaces this process with the real ssh-keygen so git sees
// the exact behavior it expects for non-signing invocations.
func execStockSSHKeygen(args []string) error {
	argv := append([]string{stockSSHKeygenPath}, args...)
	if err := syscall.Exec(stockSSHKeygenPath, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", stockSSHKeygenPath, err)
	}
	return nil // unreachable: a successful Exec does not return
}

// parseSignArguments accepts the two shapes git uses to request a signature and
// returns the public key reference and the path of the buffer to sign.
func parseSignArguments(args []string) (keyReference, bufferPath string, err error) {
	unsupported := errors.New("Unsupported Git SSH signing invocation") //nolint:staticcheck // ST1005: user-facing git error
	if len(args) < 6 || args[2] != "-n" || args[3] != "git" || args[4] != "-f" {
		return "", "", unsupported
	}
	switch {
	case len(args) == 8 && args[6] == "-U":
		return args[5], args[7], nil
	case len(args) == 7:
		// Older git versions omit -U and may pass the key:: literal through.
		return args[5], args[6], nil
	default:
		return "", "", unsupported
	}
}

// readPublicKeyBlob resolves git's public key argument — either a `key::`
// literal or a path — to the raw Ed25519 key blob.
func readPublicKeyBlob(keyReference string) ([]byte, error) {
	invalid := errors.New("Invalid Git signing public key") //nolint:staticcheck // ST1005: user-facing git error

	var raw []byte
	if literal, ok := strings.CutPrefix(keyReference, "key::"); ok {
		if len(literal) > maxPublicKeyBytes {
			return nil, errors.New("Public key is too large") //nolint:staticcheck // ST1005: user-facing git error
		}
		raw = []byte(literal)
	} else {
		var err error
		if raw, err = readBoundedFile(keyReference, maxPublicKeyBytes, "public key"); err != nil {
			return nil, err
		}
	}

	fields := strings.Fields(string(raw))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return nil, invalid
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) == 0 {
		return nil, invalid
	}
	return blob, nil
}

// fingerprint renders the key blob the way ssh-keygen -l does, which is how the
// control plane identifies which configured key to sign with.
func fingerprint(publicKeyBlob []byte) string {
	digest := sha256.Sum256(publicKeyBlob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
}

func readBoundedFile(path string, limit int, description string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Unable to read %s", description) //nolint:staticcheck // ST1005: surfaced to the agent
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("Unable to read %s", description) //nolint:staticcheck // ST1005: surfaced to the agent
	}
	if len(content) > limit {
		return nil, fmt.Errorf("%s is too large", strings.ToUpper(description[:1])+description[1:]) //nolint:staticcheck // ST1005: surfaced to the agent
	}
	return content, nil
}

// requestSignature posts the unsigned buffer to the control plane and returns
// the validated armored signature.
func requestSignature(cfg config.Resolved, keyFingerprint string, payload []byte) ([]byte, error) {
	failed := errors.New("Control-plane signing request failed") //nolint:staticcheck // ST1005: user-facing git error

	ctx, cancel := context.WithTimeout(context.Background(), signingRequestTimeout)
	defer cancel()

	endpoint := strings.TrimSuffix(cfg.ControlPlaneURL, "/") +
		"/sessions/" + url.PathEscape(cfg.SessionID) + "/commit-signing"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, failed
	}
	request.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Open-Inspect-Signing-Fingerprint", keyFingerprint)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, failed
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSigningResponseBytes+1))
	if err != nil {
		return nil, failed
	}
	if len(body) > maxSigningResponseBytes {
		return nil, errors.New("Control-plane signing response is too large") //nolint:staticcheck // ST1005: user-facing git error
	}
	if response.StatusCode != http.StatusOK {
		return nil, failed
	}
	return validateSignatureArmor(body)
}

// validateSignatureArmor rejects anything that is not a complete, base64-clean
// SSHSIG block.
func validateSignatureArmor(body []byte) ([]byte, error) {
	invalid := errors.New("Invalid commit signing response") //nolint:staticcheck // ST1005: user-facing git error

	match := signatureArmorRE.FindSubmatch(body)
	if match == nil {
		return nil, invalid
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(match[1]), "\n", ""))
	if err != nil || !bytes.HasPrefix(decoded, []byte("SSHSIG")) {
		return nil, invalid
	}
	return body, nil
}

// writeSignatureFile publishes the signature atomically, so git never reads a
// partially written one.
func writeSignatureFile(path string, content []byte) error {
	failed := errors.New("Unable to write commit signature") //nolint:staticcheck // ST1005: user-facing git error

	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return failed
	}
	defer func() { _ = os.Remove(temporary.Name()) }() // no-op once the rename below succeeds

	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return failed
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return failed
	}
	if err := temporary.Close(); err != nil {
		return failed
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return failed
	}
	return nil
}

// SignerPath returns the absolute path of the installed oi-git-sign shim, which
// is what `gpg.ssh.program` has to point at.
func SignerPath() (string, bool) {
	if path, err := exec.LookPath(GitSignerCommand); err == nil {
		if abs, err := filepath.Abs(path); err == nil {
			return abs, true
		}
	}
	return "", false
}
