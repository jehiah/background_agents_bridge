package bridge

import (
	"context"
	"time"
)

// handlePrompt configures git identity, ensures an OpenCode session, streams the
// response, and emits a terminal execution_complete event.
//
// It returns ctx.Err() only when cancelled (the caller emits the "cancelled"
// completion); on every other path it emits execution_complete itself and
// returns nil. This mirrors the Python handler, where CancelledError is the only
// path that bypasses the in-handler completion.
func (b *AgentBridge) handlePrompt(ctx context.Context, cmd *command) error {
	messageID := cmd.msgID()
	model := cmd.Model
	reasoningEffort := cmd.ReasoningEffort
	start := time.Now()
	outcome := "success"

	b.log.Info("prompt.start", "message_id", messageID, "model", model, "reasoning_effort", reasoningEffort)
	defer func() {
		b.log.Info("prompt.run",
			"message_id", messageID,
			"model", model,
			"reasoning_effort", reasoningEffort,
			"outcome", outcome,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}()

	// Attribute commits to the prompt author, falling back to the default.
	b.configureGitIdentity(ctx, GitUser{
		Name:  orDefault(cmd.Author.SCMName, fallbackGitUser.Name),
		Email: orDefault(cmd.Author.SCMEmail, fallbackGitUser.Email),
	})

	if b.getOpencodeSessionID() == "" {
		if err := b.createOpencodeSession(ctx); err != nil {
			outcome = "error"
			b.log.Error("prompt.error", "exc", err, "message_id", messageID)
			b.sendEvent(executionCompleteEvent(messageID, false, err.Error()))
			return nil
		}
	}

	hadError := false
	var errMsg string
	emit := func(e event) {
		if t, _ := e["type"].(string); t == "error" {
			hadError = true
			if m, ok := e["error"].(string); ok {
				errMsg = m
			}
		}
		b.sendEvent(e)
	}

	err := b.streamOpencodeResponse(ctx, messageID, cmd.Content, model, reasoningEffort, emit)
	if err != nil {
		outcome = "error"
		if ctx.Err() != nil {
			return ctx.Err() // cancelled: startPrompt emits the completion
		}
		b.log.Error("prompt.error", "exc", err, "message_id", messageID)
		b.sendEvent(executionCompleteEvent(messageID, false, err.Error()))
		return nil
	}

	if hadError {
		outcome = "error"
	}
	b.sendEvent(executionCompleteEvent(messageID, !hadError, errMsg))
	return nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
