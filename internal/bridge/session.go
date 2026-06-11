package bridge

import (
	"context"
	"os"
	"strings"
)

// loadSessionID loads a persisted OpenCode session ID, validating it against the
// running OpenCode instance. An invalid or missing ID leaves the bridge with no
// session (a new one is created on the next prompt).
func (b *AgentBridge) loadSessionID(ctx context.Context) {
	data, err := os.ReadFile(b.sessionIDFile)
	if err != nil {
		return // no persisted session
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return
	}
	b.setOpencodeSessionID(id)
	b.log.Info("opencode.session.ensure", "opencode_session_id", id, "action", "loaded")

	if !b.opencodeSessionExists(ctx, id) {
		b.log.Info("opencode.session.invalid", "opencode_session_id", id)
		b.setOpencodeSessionID("")
	}
}

// saveSessionID persists the active OpenCode session ID for restart recovery.
func (b *AgentBridge) saveSessionID() {
	id := b.getOpencodeSessionID()
	if id == "" {
		return
	}
	if err := os.WriteFile(b.sessionIDFile, []byte(id), 0o644); err != nil {
		b.log.Error("opencode.session.save_error", "exc", err)
	}
}
