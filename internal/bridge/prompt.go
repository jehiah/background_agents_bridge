package bridge

import (
	"context"
	"fmt"
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

	// Attribute commits to the prompt author, falling back to the agent, and
	// refresh the delegated signing configuration while we are at it.
	author, err := promptGitAuthor(cmd.Author)
	if err == nil {
		err = b.configureGitIdentity(ctx, author)
	}
	if err != nil {
		outcome = "error"
		b.log.Error("prompt.error", "exc", err, "message_id", messageID)
		b.sendEvent(executionCompleteEvent(messageID, false, err.Error()))
		return nil
	}

	if b.getOpencodeSessionID() == "" {
		if err := b.createOpencodeSession(ctx); err != nil {
			outcome = "error"
			b.log.Error("prompt.error", "exc", err, "message_id", messageID)
			b.sendEvent(executionCompleteEvent(messageID, false, err.Error()))
			return nil
		}
	}

	// Hydrate session image attachments into OpenCode file parts so the agent can
	// see them. Invalid entries are skipped with a user-facing warning; download
	// failures drop the individual attachment (also warned) without failing the
	// prompt.
	resolved, rejected := parseSessionImageAttachments(cmd.Attachments)
	if rejected > 0 {
		b.log.Warn("prompt.invalid_attachments", "message_id", messageID, "rejected_count", rejected)
		b.sendMediaWarning(fmt.Sprintf("%d invalid attachment(s) were skipped.", rejected))
	}
	fileParts := buildFileParts(b.processAttachments(ctx, resolved))

	hadError := false
	emittedOutput := false
	var errMsg string
	emit := func(e event) {
		switch t, _ := e["type"].(string); t {
		case "error":
			hadError = true
			if m, ok := e["error"].(string); ok {
				errMsg = m
			}
		case "token", "tool_call", "step_finish":
			emittedOutput = true
		}
		b.sendEvent(e)
	}

	if err := b.streamOpencodeResponse(ctx, messageID, cmd.Content, model, reasoningEffort, fileParts, emit); err != nil {
		outcome = "error"
		if ctx.Err() != nil {
			return ctx.Err() // cancelled: startPrompt emits the completion
		}
		b.log.Error("prompt.error", "exc", err, "message_id", messageID)
		b.sendEvent(executionCompleteEvent(messageID, false, err.Error()))
		return nil
	}

	if !hadError && !emittedOutput {
		hadError = true
		errMsg = "OpenCode completed without emitting assistant output."
		b.log.Error("prompt.no_output",
			"message_id", messageID,
			"model", model,
			"reasoning_effort", reasoningEffort,
		)
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
