// Package config resolves the sandbox identity and control-plane coordinates
// that every bridge mode needs (connect, git-credential, tool, install).
//
// Each field is filled by precedence: an explicit flag wins, then a process
// environment variable, then a GCE instance attribute. This lets the long-lived
// `connect` service pass everything as flags while the short-lived modes spawned
// by git/opencode rely on the inherited service environment (with a metadata
// fallback when neither is present).
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/jehiah/background_agents_bridge/internal/gcpmeta"
)

// DefaultOpencodePort is used when neither flag, env, nor metadata supplies one.
const DefaultOpencodePort = 4096

// Resolved holds the fully resolved configuration.
type Resolved struct {
	SandboxID       string
	SessionID       string
	ControlPlaneURL string
	AuthToken       string
	OpencodePort    int
}

// Flags carries the values parsed from the command line. Empty strings (and a
// zero OpencodePort) are treated as "unset" and fall through to env/metadata.
type Flags struct {
	SandboxID       string
	SessionID       string
	ControlPlaneURL string
	AuthToken       string
	OpencodePort    int
}

// field describes how one string value maps onto an env var and a metadata key.
type field struct {
	ptr      *string
	flag     string
	env      string
	metadata string
}

// Resolve fills a Resolved from flags, then environment, then GCE metadata.
//
// The metadata server is probed only when something is still missing after
// flags+env, and probing stops on the first transport error (so it returns
// promptly when not running on GCE).
func Resolve(f Flags) Resolved {
	r := Resolved(f)

	fields := []field{
		{&r.SandboxID, f.SandboxID, "SANDBOX_ID", "sandbox-id"},
		{&r.SessionID, f.SessionID, "SESSION_ID", "session-id"},
		{&r.ControlPlaneURL, f.ControlPlaneURL, "CONTROL_PLANE_URL", "control-plane"},
		{&r.AuthToken, f.AuthToken, "SANDBOX_AUTH_TOKEN", "control-plane-token"},
	}

	// Env fallback for any empty string field.
	for _, fld := range fields {
		if *fld.ptr == "" {
			if v := os.Getenv(fld.env); v != "" {
				*fld.ptr = v
			}
		}
	}
	// SESSION_CONFIG is a JSON blob the opencode tools read; honor its sessionId
	// when SESSION_ID itself is absent (matches tools/_bridge-client.js).
	if r.SessionID == "" {
		r.SessionID = sessionIDFromConfig(os.Getenv("SESSION_CONFIG"))
	}
	if r.OpencodePort == 0 {
		if n, err := strconv.Atoi(os.Getenv("OPENCODE_PORT")); err == nil && n > 0 {
			r.OpencodePort = n
		}
	}

	resolveFromMetadata(fields, &r.OpencodePort)

	if r.OpencodePort == 0 {
		r.OpencodePort = DefaultOpencodePort
	}
	return r
}

// resolveFromMetadata fills any still-empty field from GCE instance attributes.
// It probes only when something is missing and stops on the first transport
// error (likely not on GCE).
func resolveFromMetadata(fields []field, opencodePort *int) {
	anyEmpty := *opencodePort == 0
	for _, fld := range fields {
		if *fld.ptr == "" {
			anyEmpty = true
		}
	}
	if !anyEmpty {
		return
	}

	mc := gcpmeta.NewClient()
	ctx := context.Background()

	reachable := true
	for _, fld := range fields {
		if *fld.ptr != "" {
			continue
		}
		v, err := mc.InstanceAttribute(ctx, fld.metadata)
		if err != nil {
			fmt.Fprintf(os.Stderr, "metadata lookup %q failed: %v\n", fld.metadata, err)
			reachable = false
			break // likely not on GCE; stop probing
		}
		if v != "" {
			*fld.ptr = v
		}
	}

	if reachable && *opencodePort == 0 {
		if v, err := mc.InstanceAttribute(ctx, "opencode-port"); err == nil && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*opencodePort = n
			}
		}
	}
}

// sessionIDFromConfig extracts sessionId/session_id from the SESSION_CONFIG JSON.
func sessionIDFromConfig(raw string) string {
	if raw == "" {
		return ""
	}
	var cfg struct {
		SessionID      string `json:"sessionId"`
		SessionIDSnake string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return ""
	}
	if cfg.SessionID != "" {
		return cfg.SessionID
	}
	return cfg.SessionIDSnake
}

// Missing returns the human-readable names of required fields that are still
// empty. OpencodePort is not required (it defaults). The names match the connect
// flags so error messages stay familiar.
func (r Resolved) Missing(required ...string) []string {
	have := map[string]string{
		"sandbox-id":          r.SandboxID,
		"session-id":          r.SessionID,
		"control-plane":       r.ControlPlaneURL,
		"control-plane-token": r.AuthToken,
	}
	var missing []string
	for _, name := range required {
		if have[name] == "" {
			missing = append(missing, name)
		}
	}
	return missing
}
