package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tmaxmax/go-sse"
)

// sseEvent is the JSON payload OpenCode sends in each SSE "data:" field. Its
// shape varies by Type; the dispatcher navigates Properties with gstr/gmap.
type sseEvent struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// pendingPart is a message part received before its assistant message was
// authorized; it is replayed once the message id is allowed.
type pendingPart struct {
	part  map[string]any
	delta any
}

// streamState holds per-prompt correlation state for a single SSE stream. It is
// owned by one goroutine and needs no locking.
type streamState struct {
	messageID         string
	opencodeMessageID string
	opencodeSessionID string

	cumulativeText         map[string]string
	emittedToolStates      map[string]bool
	allowedAssistantMsgIDs map[string]bool
	pendingParts           map[string][]pendingPart
	pendingPartsTotal      int
	pendingDropLogged      bool
	trackedChildSessionIDs map[string]bool
	compactionOccurred     bool
}

// streamOpencodeResponse drives a single prompt: it opens the OpenCode event
// stream, posts the prompt, and forwards correlated assistant output via emit.
// It returns nil on normal completion (session idle, or a session error already
// surfaced via emit), ctx.Err() on cancellation, or an error on timeout / SSE
// failure (which the caller turns into an execution_complete error).
func (b *AgentBridge) streamOpencodeResponse(
	ctx context.Context,
	messageID, content, model, reasoningEffort string,
	emit func(event),
) error {
	ocSessionID := b.getOpencodeSessionID()
	if ocSessionID == "" {
		return fmt.Errorf("opencode session not initialized")
	}

	ocMsgID, err := b.ids.ascending("message")
	if err != nil {
		return err
	}
	body := buildPromptRequestBody(content, model, ocMsgID, reasoningEffort)

	s := &streamState{
		messageID:              messageID,
		opencodeMessageID:      ocMsgID,
		opencodeSessionID:      ocSessionID,
		cumulativeText:         map[string]string{},
		emittedToolStates:      map[string]bool{},
		allowedAssistantMsgIDs: map[string]bool{},
		pendingParts:           map[string][]pendingPart{},
		trackedChildSessionIDs: map[string]bool{},
	}

	// The SSE read is bounded by an inactivity deadline: a timer cancels sseCtx
	// if no chunk arrives within sseInactivityTimeout, and is reset per chunk.
	sseCtx, sseCancel := context.WithCancel(ctx)
	defer sseCancel()
	var inactivity atomic.Bool
	timer := time.AfterFunc(b.sseInactivityTimeout, func() {
		inactivity.Store(true)
		sseCancel()
	})
	defer timer.Stop()

	req, err := http.NewRequestWithContext(sseCtx, http.MethodGet, b.opencodeBaseURL+"/event", nil)
	if err != nil {
		return err
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		if inactivity.Load() {
			return b.onStreamInactivity(ctx, s, emit)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SSE read error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE connection failed: %d", resp.StatusCode)
	}

	// Post the prompt only once we are listening for its events.
	if err := b.postPrompt(ctx, ocSessionID, body); err != nil {
		return err
	}

	startWall := time.Now()
	promptStart := time.Now()

	var finishErr, readErr error
	for ev, err := range sse.Read(resp.Body, &sse.ReadConfig{MaxEventSize: sseMaxEventSize}) {
		if err != nil {
			readErr = err
			break
		}
		// Any event (including OpenCode's server.heartbeat) counts as activity.
		timer.Reset(b.sseInactivityTimeout)
		if ev.Data == "" {
			continue
		}
		var payload sseEvent
		if json.Unmarshal([]byte(ev.Data), &payload) != nil {
			b.log.Debug("bridge.sse_parse_error")
			continue
		}

		stop, herr := b.dispatchSSE(ctx, s, payload, emit)
		if herr != nil {
			finishErr = herr
			break
		}
		if stop {
			break
		}
		if time.Since(promptStart) > promptMaxDuration {
			b.log.Error("bridge.prompt_max_duration_timeout",
				"timeout_ms", promptMaxDuration.Milliseconds(),
				"elapsed_ms", time.Since(startWall).Milliseconds(),
				"message_id", messageID,
			)
			b.requestOpencodeStop(ctx, "prompt_max_duration_timeout")
			b.fetchFinalMessageState(ctx, s, emit)
			finishErr = fmt.Errorf("prompt exceeded max duration of %.0fs", promptMaxDuration.Seconds())
			break
		}
	}

	switch {
	case finishErr != nil:
		return finishErr
	case inactivity.Load():
		return b.onStreamInactivity(ctx, s, emit)
	case readErr != nil && ctx.Err() != nil:
		return ctx.Err()
	case readErr != nil:
		return fmt.Errorf("SSE read error: %w", readErr)
	default:
		return nil
	}
}

// onStreamInactivity handles an SSE inactivity timeout: abort OpenCode, flush any
// final state, and return a descriptive error.
func (b *AgentBridge) onStreamInactivity(ctx context.Context, s *streamState, emit func(event)) error {
	b.log.Error("bridge.sse_inactivity_timeout",
		"timeout_ms", b.sseInactivityTimeout.Milliseconds(),
		"message_id", s.messageID,
	)
	b.requestOpencodeStop(ctx, "inactivity_timeout")
	b.fetchFinalMessageState(ctx, s, emit)
	return fmt.Errorf("SSE stream inactive for %.0fs (no data received)", b.sseInactivityTimeout.Seconds())
}

// postPrompt submits the prompt to OpenCode's async endpoint.
func (b *AgentBridge) postPrompt(ctx context.Context, ocSessionID string, body map[string]any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, opencodeRequestTimeout)
	defer cancel()

	url := b.opencodeBaseURL + "/session/" + ocSessionID + "/prompt_async"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		b.log.Error("bridge.prompt_request_error", "status_code", resp.StatusCode, "error_body", string(errBody))
		return fmt.Errorf("async prompt failed: %d - %s", resp.StatusCode, errBody)
	}
	return nil
}

// dispatchSSE processes a single OpenCode event, emitting any resulting bridge
// events. It returns stop=true when the stream should end (session idle, or a
// parent session error already emitted).
func (b *AgentBridge) dispatchSSE(ctx context.Context, s *streamState, ev sseEvent, emit func(event)) (bool, error) {
	etype := ev.Type
	props := ev.Properties

	switch etype {
	case "server.connected", "server.heartbeat":
		return false, nil
	case "session.created":
		info := gmap(props, "info")
		childID := gstr(info, "id")
		if childID != "" && gstr(info, "parentID") == s.opencodeSessionID {
			s.trackedChildSessionIDs[childID] = true
			b.log.Info("bridge.child_session_detected", "child_session_id", childID, "source", "session.created")
		}
		return false, nil
	}

	if te := b.sessionTitleEventFromSSE(s, etype, props); te != nil {
		emit(te)
	}
	if etype == "session.updated" {
		return false, nil
	}

	eventSessionID := gstr(props, "sessionID")
	if eventSessionID == "" {
		eventSessionID = gstr(gmap(props, "part"), "sessionID")
	}
	isChild := s.trackedChildSessionIDs[eventSessionID]
	if eventSessionID != "" && eventSessionID != s.opencodeSessionID && !isChild {
		return false, nil
	}

	switch etype {
	case "message.updated":
		s.handleMessageUpdated(props, emit)
	case "message.part.updated":
		s.handlePartUpdated(b, props, emit)
	case "session.idle":
		if gstr(props, "sessionID") == s.opencodeSessionID {
			b.fetchFinalMessageState(ctx, s, emit)
			return true, nil
		}
	case "session.status":
		if gstr(props, "sessionID") == s.opencodeSessionID && gstr(gmap(props, "status"), "type") == "idle" {
			b.fetchFinalMessageState(ctx, s, emit)
			return true, nil
		}
	case "session.error":
		esid := gstr(props, "sessionID")
		if esid == s.opencodeSessionID {
			msg := extractErrorMessage(props["error"])
			if msg == "" {
				msg = "Unknown error"
			}
			b.log.Error("bridge.session_error", "error_msg", msg)
			emit(errorEvent(msg, s.messageID))
			return true, nil
		}
		if s.trackedChildSessionIDs[esid] {
			msg := extractErrorMessage(props["error"])
			if msg == "" {
				msg = "Sub-task error"
			}
			b.log.Error("bridge.child_session_error", "error_msg", msg, "child_session_id", esid)
			e := errorEvent(msg, s.messageID)
			e["isSubtask"] = true
			emit(e)
			// Parent stream continues.
		}
	case "session.compacted":
		if gstr(props, "sessionID") == s.opencodeSessionID {
			s.compactionOccurred = true
			b.log.Info("bridge.session_compacted", "message_id", s.messageID)
		}
	}
	return false, nil
}

// handleMessageUpdated authorizes assistant messages (parent or child) and
// replays any parts buffered before authorization.
func (s *streamState) handleMessageUpdated(props map[string]any, emit func(event)) {
	info := gmap(props, "info")
	msgSessionID := gstr(info, "sessionID")
	ocMsgID := gstr(info, "id")
	role := gstr(info, "role")

	if msgSessionID == s.opencodeSessionID {
		parentMatches := gstr(info, "parentID") == s.opencodeMessageID
		isCompactionSummary := info["summary"] == true
		if role == "assistant" && ocMsgID != "" {
			if parentMatches || (s.compactionOccurred && !isCompactionSummary) {
				s.allowedAssistantMsgIDs[ocMsgID] = true
				for _, pp := range s.popPending(ocMsgID) {
					for _, e := range s.handlePart(pp.part, pp.delta, false) {
						emit(e)
					}
				}
			}
		}
		return
	}

	if s.trackedChildSessionIDs[msgSessionID] && role == "assistant" && ocMsgID != "" {
		s.allowedAssistantMsgIDs[ocMsgID] = true
		for _, pp := range s.popPending(ocMsgID) {
			for _, e := range s.handlePart(pp.part, pp.delta, true) {
				emit(e)
			}
		}
	}
}

// handlePartUpdated forwards a streamed part for authorized assistant messages,
// or buffers it until its message is authorized. It also discovers child
// sessions advertised in task-tool metadata.
func (s *streamState) handlePartUpdated(b *AgentBridge, props map[string]any, emit func(event)) {
	part := gmap(props, "part")
	delta := props["delta"]
	ocMsgID := gstr(part, "messageID")
	partSessionID := gstr(part, "sessionID")

	if gstr(part, "tool") == "task" && partSessionID == s.opencodeSessionID {
		childSID := gstr(gmap(part, "metadata"), "sessionId")
		if childSID != "" && !s.trackedChildSessionIDs[childSID] {
			s.trackedChildSessionIDs[childSID] = true
			b.log.Info("bridge.child_session_detected", "child_session_id", childSID, "source", "task_metadata")
		}
	}

	if s.allowedAssistantMsgIDs[ocMsgID] {
		isSubtask := s.trackedChildSessionIDs[partSessionID]
		for _, e := range s.handlePart(part, delta, isSubtask) {
			emit(e)
		}
	} else if ocMsgID != "" {
		s.bufferPart(ocMsgID, part, delta)
	}
}

// handlePart transforms one OpenCode part into zero or more bridge events.
func (s *streamState) handlePart(part map[string]any, delta any, isSubtask bool) []event {
	var events []event
	switch gstr(part, "type") {
	case "text":
		if isSubtask {
			return events // child text tokens are not forwarded
		}
		partID := gstr(part, "id")
		if d, ok := delta.(string); ok && d != "" {
			s.cumulativeText[partID] += d
		} else {
			s.cumulativeText[partID] = gstr(part, "text")
		}
		if s.cumulativeText[partID] != "" {
			events = append(events, tokenEvent(s.cumulativeText[partID], s.messageID))
		}
	case "tool":
		if te, ok := s.transformTool(part); ok {
			state := gmap(part, "state")
			toolKey := "tool:" + gstr(part, "sessionID") + ":" + gstr(part, "callID") + ":" + gstr(state, "status")
			if !s.emittedToolStates[toolKey] {
				s.emittedToolStates[toolKey] = true
				events = append(events, te)
			}
		}
	case "step-start":
		events = append(events, stepStartEvent(s.messageID))
	case "step-finish":
		events = append(events, stepFinishEvent(part["cost"], part["tokens"], part["reason"], s.messageID))
	}

	if isSubtask {
		for _, e := range events {
			e["isSubtask"] = true
		}
	}
	return events
}

// transformTool builds a tool_call event from a tool part, skipping pending
// states that carry no input yet.
func (s *streamState) transformTool(part map[string]any) (event, bool) {
	state := gmap(part, "state")
	status := gstr(state, "status")
	rawInput := state["input"]
	if (status == "pending" || status == "") && isEmpty(rawInput) {
		return nil, false
	}
	args := rawInput
	if args == nil {
		args = map[string]any{}
	}
	output := state["output"]
	if output == nil {
		output = ""
	}
	return toolCallEvent(gstr(part, "tool"), args, gstr(part, "callID"), status, output, s.messageID), true
}

func (s *streamState) popPending(msgID string) []pendingPart {
	pp := s.pendingParts[msgID]
	if len(pp) > 0 {
		delete(s.pendingParts, msgID)
		s.pendingPartsTotal -= len(pp)
	}
	return pp
}

func (s *streamState) bufferPart(msgID string, part map[string]any, delta any) {
	if s.pendingPartsTotal >= maxPendingPartEvents {
		if !s.pendingDropLogged {
			s.pendingDropLogged = true
		}
		return
	}
	s.pendingParts[msgID] = append(s.pendingParts[msgID], pendingPart{part: part, delta: delta})
	s.pendingPartsTotal++
}

// fetchFinalMessageState fetches the final message list after the session goes
// idle, emitting any text longer than what was already streamed (guards against
// SSE event-ordering gaps).
func (b *AgentBridge) fetchFinalMessageState(ctx context.Context, s *streamState, emit func(event)) {
	msgs, err := b.listMessages(ctx)
	if err != nil {
		b.log.Warn("bridge.final_state_fetch_error", "exc", err)
		return
	}
	for _, msg := range msgs {
		info := gmap(msg, "info")
		if gstr(info, "role") != "assistant" {
			continue
		}
		msgID := gstr(info, "id")
		parentMatches := gstr(info, "parentID") == s.opencodeMessageID
		inTracked := s.allowedAssistantMsgIDs[msgID]
		isCompactionSummary := info["summary"] == true
		if !parentMatches && !inTracked && (!s.compactionOccurred || isCompactionSummary) {
			continue
		}
		parts, _ := msg["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			if gstr(part, "type") != "text" {
				continue
			}
			partID := gstr(part, "id")
			text := gstr(part, "text")
			if len(text) > len(s.cumulativeText[partID]) {
				s.cumulativeText[partID] = text
				emit(tokenEvent(text, s.messageID))
			}
		}
	}
}

// sessionTitleEventFromSSE returns a session_title event for a session.updated
// event carrying a new, non-default title (deduplicated across the bridge).
func (b *AgentBridge) sessionTitleEventFromSSE(s *streamState, etype string, props map[string]any) event {
	if etype != "session.updated" {
		return nil
	}
	info := gmap(props, "info")
	if info == nil {
		return nil
	}
	sid := gstr(props, "sessionID")
	if sid == "" {
		sid = gstr(info, "id")
	}
	if sid != s.opencodeSessionID {
		return nil
	}

	title := normalizeForwardableTitle(gstr(info, "title"))
	if title == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if title == b.lastForwardedSessionTitle {
		return nil
	}
	b.lastForwardedSessionTitle = title
	return sessionTitleEvent(title)
}

// normalizeForwardableTitle trims a title and discards empty or auto-generated
// default titles.
func normalizeForwardableTitle(title string) string {
	t := strings.TrimSpace(title)
	if t == "" || opencodeDefaultTitleRE.MatchString(t) {
		return ""
	}
	return t
}
