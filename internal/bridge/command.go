package bridge

import "context"

// handleCommand dispatches a control-plane command. The long-running prompt
// command runs in its own goroutine (with a context that survives WebSocket
// reconnects) so the read loop stays responsive; all other commands run inline.
func (b *AgentBridge) handleCommand(ctx context.Context, cmd *command) {
	b.log.Debug("bridge.command_received", "cmd_type", cmd.Type)

	switch cmd.Type {
	case "prompt":
		b.startPrompt(cmd)
	case "stop":
		b.handleStop(ctx)
	case "snapshot":
		b.handleSnapshot()
	case "shutdown":
		b.handleShutdown()
	case "git_sync_complete":
		b.gitSyncOnce.Do(func() { close(b.gitSyncDoneC) })
	case "push":
		b.handlePush(ctx, cmd)
	case "ack":
		if cmd.AckID != "" {
			b.ackReceived(cmd.AckID)
		}
	default:
		b.log.Debug("bridge.unknown_command", "cmd_type", cmd.Type)
	}
}

// startPrompt launches the prompt handler on a detached context so it is not
// cancelled when the current WebSocket connection drops. Mirrors the Python
// behavior of not adding the prompt task to the per-connection task set.
func (b *AgentBridge) startPrompt(cmd *command) {
	messageID := cmd.msgID()

	promptCtx, cancel := context.WithCancel(b.rootCtx)
	b.setPromptCancel(cancel)

	go func() {
		defer cancel()
		err := b.handlePrompt(promptCtx, cmd)

		// Clear the stored cancel only if it is still ours.
		b.promptMu.Lock()
		if b.cancelPromptF != nil {
			b.cancelPromptF = nil
		}
		b.promptMu.Unlock()

		if err != nil {
			// handlePrompt already emits execution_complete on the paths it
			// controls; this covers cancellation/unexpected errors.
			if promptCtx.Err() != nil {
				b.sendEvent(executionCompleteEvent(messageID, false, "Task was cancelled"))
			} else {
				b.sendEvent(executionCompleteEvent(messageID, false, err.Error()))
			}
		}
	}()
}

// handleStop cancels the in-flight prompt and best-effort aborts OpenCode.
func (b *AgentBridge) handleStop(ctx context.Context) {
	b.log.Info("bridge.stop")
	b.cancelPrompt()
	b.requestOpencodeStop(ctx, "command")
}

// handleSnapshot signals readiness for a filesystem snapshot.
func (b *AgentBridge) handleSnapshot() {
	b.log.Info("bridge.snapshot_prepare")
	b.sendEvent(snapshotReadyEvent(b.getOpencodeSessionID()))
}

// handleShutdown cancels the in-flight prompt and triggers graceful shutdown.
func (b *AgentBridge) handleShutdown() {
	b.log.Info("bridge.shutdown_requested")
	b.cancelPrompt()
	b.cancel()
}
