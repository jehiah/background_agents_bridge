package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	cumulativeText    map[string]string
	emittedToolStates map[string]bool
	pendingParts      map[string][]pendingPart
	pendingPartsTotal int
	pendingDropLogged bool

	// attribution owns which OpenCode messages belong to this prompt.
	attribution *messageAttribution

	// childActivity owns which child sessions belong to this prompt, which Task
	// call spawned each one, and any child output still waiting for that Task
	// call to be announced.
	childActivity *childActivityCorrelator

	// emittedErrorMessages deduplicates the parent error event, which can arrive
	// as both a message.updated error and a session.error.
	emittedErrorMessages map[string]bool
	// pendingOverflowError holds a context-overflow announcement that was
	// swallowed because compaction should follow; session.compacted clears it.
	// Still set at idle means the promised compaction never happened.
	pendingOverflowError string
}

// contextOverflowErrorName is the OpenCode error that announces automatic
// compaction rather than a failure.
const contextOverflowErrorName = "ContextOverflowError"

// isContextOverflow reports whether an OpenCode NamedError is the context-limit
// announcement.
func isContextOverflow(e any) bool {
	m, ok := e.(map[string]any)
	return ok && gstr(m, "name") == contextOverflowErrorName
}

// newStreamState seeds the per-prompt correlation state. The prompt's own user
// message id is pre-authorized so the assistant reply it parents is accepted.
func newStreamState(messageID, ocMsgID, ocSessionID string, startedAt time.Time) *streamState {
	return &streamState{
		messageID:            messageID,
		opencodeMessageID:    ocMsgID,
		opencodeSessionID:    ocSessionID,
		cumulativeText:       map[string]string{},
		emittedToolStates:    map[string]bool{},
		pendingParts:         map[string][]pendingPart{},
		attribution:          newMessageAttribution(ocMsgID, startedAt),
		childActivity:        newChildActivityCorrelator(),
		emittedErrorMessages: map[string]bool{},
	}
}

// streamOpencodeResponse drives a single prompt: it opens the OpenCode event
// stream, posts the prompt, and forwards correlated assistant output via emit.
// It returns nil on normal completion (session idle, or a session error already
// surfaced via emit), ctx.Err() on cancellation, or an error on timeout / SSE
// failure (which the caller turns into an execution_complete error).
func (b *AgentBridge) streamOpencodeResponse(
	ctx context.Context,
	messageID, content, model, reasoningEffort string,
	fileParts []map[string]any,
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
	body := buildPromptRequestBody(content, model, ocMsgID, reasoningEffort, fileParts)

	// startWall is taken before the prompt is posted, so nothing OpenCode creates
	// for this prompt can predate it. It also bounds the whole prompt with an
	// absolute deadline covering the SSE handshake, the prompt POST, and every
	// event wait — not just the gaps between events. A stream that goes silent
	// (or never opens) has to trip it, so it cannot be a check made after each
	// event arrives.
	startWall := time.Now()

	s := newStreamState(messageID, ocMsgID, ocSessionID, startWall)

	promptCtx, promptCancel := context.WithTimeout(ctx, promptMaxDuration)
	defer promptCancel()

	// The SSE read is bounded by an inactivity deadline too: a timer cancels
	// sseCtx if no chunk arrives within sseInactivityTimeout, and is reset per
	// chunk. sseCtx descends from promptCtx, so the deadline cancels it as well.
	sseCtx, sseCancel := context.WithCancel(promptCtx)
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
		if promptDeadlineExceeded(ctx, promptCtx) {
			return b.onPromptDeadline(ctx, s, emit, startWall)
		}
		if inactivity.Load() {
			return b.onStreamInactivity(ctx, s, emit)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return b.onStreamTransportError(ctx, s, emit, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE connection failed: %d", resp.StatusCode)
	}

	// Post the prompt only once we are listening for its events.
	if err := b.postPrompt(promptCtx, ocSessionID, body); err != nil {
		if promptDeadlineExceeded(ctx, promptCtx) {
			return b.onPromptDeadline(ctx, s, emit, startWall)
		}
		return err
	}

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
		// Audit raw session.updated events to debug missing session_title.
		if strings.Contains(ev.Data, "session.updated") {
			b.log.Debug("bridge.sse_raw_event", "etype", "session.updated", "raw", ev.Data)
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
	}

	// Child activity that never learned its Task call still belongs in the
	// transcript, so every exit from the stream emits it uncorrelated rather
	// than dropping it.
	s.flushChildActivity(emit)

	switch {
	case finishErr != nil:
		return finishErr
	case promptDeadlineExceeded(ctx, promptCtx):
		return b.onPromptDeadline(ctx, s, emit, startWall)
	case inactivity.Load():
		return b.onStreamInactivity(ctx, s, emit)
	case readErr != nil && ctx.Err() != nil:
		return ctx.Err()
	case readErr != nil:
		return b.onStreamTransportError(ctx, s, emit, readErr)
	default:
		return nil
	}
}

// promptDeadlineExceeded reports whether the prompt ran out of its own budget,
// as opposed to the bridge shutting down: a cancelled parent expires promptCtx
// too, and that is a shutdown, not a timeout.
func promptDeadlineExceeded(ctx, promptCtx context.Context) bool {
	return ctx.Err() == nil && errors.Is(promptCtx.Err(), context.DeadlineExceeded)
}

// onPromptDeadline handles a prompt exceeding its maximum duration: abort
// OpenCode, flush whatever it persisted, and fail with a stable message.
//
// The cleanup is bounded by promptCleanupTimeout because it is what stands
// between the deadline and the end of the sandbox lifetime — an abort or a
// final-state fetch that hangs (the same silence that caused the timeout) would
// otherwise eat the reserve the snapshot needs. Its context comes from the
// parent, since the prompt's own is already expired.
func (b *AgentBridge) onPromptDeadline(ctx context.Context, s *streamState, emit func(event), startWall time.Time) error {
	b.log.Error("bridge.prompt_max_duration_timeout",
		"timeout_ms", promptMaxDuration.Milliseconds(),
		"elapsed_ms", time.Since(startWall).Milliseconds(),
		"message_id", s.messageID,
	)
	cleanupCtx, cancel := context.WithTimeout(ctx, promptCleanupTimeout)
	defer cancel()
	b.requestOpencodeStop(cleanupCtx, "prompt_max_duration_timeout")
	b.fetchFinalMessageState(cleanupCtx, s, emit)
	if errors.Is(cleanupCtx.Err(), context.DeadlineExceeded) {
		b.log.Error("bridge.prompt_timeout_cleanup_timeout",
			"timeout_ms", promptCleanupTimeout.Milliseconds(),
			"message_id", s.messageID,
		)
	}
	return fmt.Errorf("prompt exceeded max duration of %.0fs", promptMaxDuration.Seconds())
}

// onStreamTransportError handles the event stream dropping mid-response: flush
// whatever OpenCode already persisted for this prompt, then fail with a stable
// message. Without the flush, a disconnect after the assistant had produced text
// loses that text — OpenCode has it, but the tokens never reach the user.
//
// The returned message deliberately omits the transport detail (it is logged
// instead): it reaches the user via execution_complete, where a raw read error
// says nothing useful. Port of the httpx.TransportError handler in bridge.py
// (upstream #1009).
func (b *AgentBridge) onStreamTransportError(ctx context.Context, s *streamState, emit func(event), cause error) error {
	b.log.Error("bridge.sse_transport_error", "exc", cause, "message_id", s.messageID)
	b.fetchFinalMessageState(ctx, s, emit)
	return &sseDisconnectError{cause: cause}
}

// sseDisconnectError is the user-facing verdict for a dropped event stream; the
// transport cause stays reachable through errors.Is/As.
type sseDisconnectError struct{ cause error }

func (e *sseDisconnectError) Error() string {
	return "OpenCode event stream disconnected before completion; partial output was preserved when available."
}

func (e *sseDisconnectError) Unwrap() error { return e.cause }

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
		if childID != "" && gstr(info, "parentID") == s.opencodeSessionID && s.childActivity.track(childID) {
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
	isChild := s.childActivity.isTracked(eventSessionID)
	if eventSessionID != "" && eventSessionID != s.opencodeSessionID && !isChild {
		return false, nil
	}

	switch etype {
	case "message.updated":
		s.handleMessageUpdated(b, props, emit)
	case "message.part.updated":
		s.handlePartUpdated(b, props, emit)
	case "session.idle":
		if gstr(props, "sessionID") == s.opencodeSessionID {
			b.fetchFinalMessageState(ctx, s, emit)
			b.emitUnrecoveredOverflow(s, emit)
			return true, nil
		}
	case "session.status":
		if gstr(props, "sessionID") == s.opencodeSessionID && gstr(gmap(props, "status"), "type") == "idle" {
			b.fetchFinalMessageState(ctx, s, emit)
			b.emitUnrecoveredOverflow(s, emit)
			return true, nil
		}
	case "session.error":
		esid := gstr(props, "sessionID")
		if esid == s.opencodeSessionID {
			// With OpenCode's automatic compaction, a context overflow announces
			// recovery rather than failure: session.compacted and more assistant
			// work follow it. Swallow it, but remember it, so an idle that never
			// compacted still fails the prompt.
			if isContextOverflow(props["error"]) {
				s.pendingOverflowError = orDefault(extractErrorMessage(props["error"]), "Context overflow")
				b.log.Info("bridge.context_overflow_compacting", "error_msg", s.pendingOverflowError)
				return false, nil
			}
			e := s.parentErrorEventOnce(props["error"])
			b.log.Error("bridge.session_error",
				"error_msg", extractErrorMessage(props["error"]), "deduped", e == nil)
			if e != nil {
				emit(e)
			}
			return true, nil
		}
		if s.childActivity.isTracked(esid) {
			// Child sessions recover through the same automatic compaction, so
			// surfacing this would fail the whole prompt spuriously.
			if isContextOverflow(props["error"]) {
				b.log.Info("bridge.child_context_overflow_compacting",
					"error_msg", extractErrorMessage(props["error"]), "child_session_id", esid)
				return false, nil
			}
			msg := extractErrorMessage(props["error"])
			if msg == "" {
				msg = "Sub-task error"
			}
			b.log.Error("bridge.child_session_error", "error_msg", msg, "child_session_id", esid)
			// A child can fail before the parent's task part announced it, or
			// after that task completed. Hold the error until the Task call is
			// known so it nests under the right one; the flush at stream end is
			// the backstop.
			taskCallID := s.childActivity.taskForActivity(esid)
			if taskCallID == "" {
				if !s.childActivity.queueError(esid, msg) {
					b.logPendingChildDrop(s)
				}
				return false, nil
			}
			emit(s.childErrorEvent(esid, msg, taskCallID))
			// Parent stream continues.
		}
	case "session.compacted":
		if gstr(props, "sessionID") == s.opencodeSessionID {
			s.attribution.markCompacted()
			s.pendingOverflowError = ""
			b.log.Info("bridge.session_compacted", "message_id", s.messageID)
			emit(contextCompactedEvent(s.messageID))
		}
	}
	return false, nil
}

// emitUnrecoveredOverflow fails the prompt when a swallowed context overflow's
// promised compaction never came. Swallowing the announcement is only safe
// because compaction normally follows it; if the session goes idle without
// compacting and without any error emitted, reporting silent success would hide
// a prompt that produced nothing.
func (b *AgentBridge) emitUnrecoveredOverflow(s *streamState, emit func(event)) {
	if s.pendingOverflowError == "" || len(s.emittedErrorMessages) > 0 {
		return
	}
	b.log.Error("bridge.context_overflow_unrecovered", "error_msg", s.pendingOverflowError)
	s.emittedErrorMessages[s.pendingOverflowError] = true
	emit(errorEvent(s.pendingOverflowError, s.messageID))
}

// parentErrorEventOnce builds the parent error event for err, or nil when an
// error with the same message was already emitted. The same failure reaches the
// bridge as both a message.updated error and a session.error, and the control
// plane should see it once.
func (s *streamState) parentErrorEventOnce(err any) event {
	msg := extractErrorMessage(err)
	if msg == "" {
		msg = "Unknown error"
	}
	if s.emittedErrorMessages[msg] {
		return nil
	}
	s.emittedErrorMessages[msg] = true
	return errorEvent(msg, s.messageID)
}

// handleMessageUpdated authorizes assistant messages (parent or child), surfaces
// message-level errors, and replays any parts buffered before authorization.
func (s *streamState) handleMessageUpdated(b *AgentBridge, props map[string]any, emit func(event)) {
	info := gmap(props, "info")
	msgSessionID := gstr(info, "sessionID")
	ocMsgID := gstr(info, "id")
	role := gstr(info, "role")

	if msgSessionID == s.opencodeSessionID {
		if role == "user" && ocMsgID != "" {
			s.attribution.addUserMessage(ocMsgID)
		}
		if role == "assistant" && ocMsgID != "" {
			disposition := s.attribution.assistantDisposition(
				ocMsgID, gstr(info, "parentID"), info["summary"] == true, messageCreatedTime(info))
			// A message carrying an error still belongs to this prompt when it
			// is one of our compaction summaries, even though its text never
			// reaches the transcript. Emitting here puts the error in order with
			// the surrounding token and step events.
			if disposition != assistantReject && truthy(info["error"]) {
				if e := s.parentErrorEventOnce(info["error"]); e != nil {
					b.log.Error("bridge.message_error", "error_msg", e["error"], "oc_msg_id", ocMsgID)
					emit(e)
				}
			}
			if disposition == assistantOutput {
				for _, pp := range s.popPending(ocMsgID) {
					for _, e := range s.handlePart(pp.part, pp.delta, false) {
						emit(e)
					}
				}
			}
		}
		return
	}

	if s.childActivity.isTracked(msgSessionID) && role == "assistant" && ocMsgID != "" {
		// Forwarding the message before its Task call is known would strand its
		// events outside the task they belong to, so hold them instead.
		switch s.childActivity.authorizeOrQueueMessage(msgSessionID, ocMsgID) {
		case messageAuthorized:
			s.drainChildMessage(ocMsgID, emit)
		case messageQueued:
		case messageDropped:
			b.logPendingChildDrop(s)
		}
	}
}

// drainChildMessage authorizes a child assistant message and replays whatever
// parts of it were buffered while it was unauthorized.
func (s *streamState) drainChildMessage(ocMsgID string, emit func(event)) {
	s.attribution.allowAssistant(ocMsgID)
	for _, pp := range s.popPending(ocMsgID) {
		for _, e := range s.handlePart(pp.part, pp.delta, true) {
			emit(e)
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

	// A correlated child is one this part just bound to a Task call: its held
	// activity can be released as soon as the part itself is handled.
	var correlatedChildSID string
	if gstr(part, "tool") == "task" && partSessionID == s.opencodeSessionID {
		// The child session id rides on the tool state, not the part: a
		// foreground task publishes it only once the call completes, which is
		// after the child's own activity has streamed.
		childSID := gstr(gmap(gmap(part, "state"), "metadata"), "sessionId")
		taskCallID := gstr(part, "callID")
		if childSID != "" {
			var isNew bool
			if taskCallID != "" {
				isNew = s.childActivity.associate(childSID, taskCallID)
				correlatedChildSID = childSID
			} else {
				isNew = s.childActivity.track(childSID)
			}
			if isNew {
				b.log.Info("bridge.child_session_detected", "child_session_id", childSID, "source", "task_metadata")
			}
		}
	}

	if s.attribution.isAssistantAllowed(ocMsgID) {
		isSubtask := s.childActivity.isTracked(partSessionID)
		for _, e := range s.handlePart(part, delta, isSubtask) {
			emit(e)
		}
	} else if ocMsgID != "" {
		s.bufferPart(ocMsgID, part, delta)
	}

	if correlatedChildSID != "" {
		s.releaseChildActivity(correlatedChildSID, emit)
	}

	// A finished task stops owning new activity, but keeps the activity already
	// attributed to it.
	if gstr(part, "tool") == "task" {
		if status := gstr(gmap(part, "state"), "status"); status == "completed" || status == "error" {
			s.childActivity.closeTask(gstr(part, "callID"))
		}
	}
}

// releaseChildActivity emits the activity held for a child session that just
// gained a Task call.
func (s *streamState) releaseChildActivity(childSessionID string, emit func(event)) {
	for _, a := range s.childActivity.release(childSessionID) {
		s.emitPendingChildActivity(a, emit)
	}
}

// flushChildActivity emits everything still held, correlated where possible.
func (s *streamState) flushChildActivity(emit func(event)) {
	for _, a := range s.childActivity.flush() {
		s.emitPendingChildActivity(a, emit)
	}
}

func (s *streamState) emitPendingChildActivity(a pendingChildActivity, emit func(event)) {
	if a.isErr {
		emit(s.childErrorEvent(a.childSessionID, a.errMsg, s.childActivity.taskForPending(a)))
		return
	}
	s.drainChildMessage(a.messageID, emit)
}

// childErrorEvent builds the sub-task error event, tagged with the child
// session and, when known, the Task call it nests under.
func (s *streamState) childErrorEvent(childSessionID, msg, taskCallID string) event {
	e := errorEvent(msg, s.messageID)
	e["isSubtask"] = true
	e["childSessionId"] = childSessionID
	if taskCallID != "" {
		e["taskCallId"] = taskCallID
	}
	return e
}

// logPendingChildDrop warns once that held child activity is being discarded.
func (b *AgentBridge) logPendingChildDrop(s *streamState) {
	if !s.childActivity.shouldLogDrop() {
		return
	}
	b.log.Warn("bridge.pending_child_activity_dropped",
		"message_id", s.messageID, "limit", maxPendingChildActivity)
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
			callID := gstr(part, "callID")
			// The child session is part of the dedupe key: a task part re-emitted
			// once its child is known carries new information.
			childSessionID := s.childActivity.childForTask(callID)
			if gstr(part, "tool") == "task" && childSessionID != "" {
				te["childSessionId"] = childSessionID
			}
			toolKey := "tool:" + gstr(part, "sessionID") + ":" + callID + ":" + gstr(state, "status") + ":" + childSessionID
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
		childSessionID := gstr(part, "sessionID")
		taskCallID := s.childActivity.taskForMessage(gstr(part, "messageID"))
		for _, e := range events {
			e["isSubtask"] = true
			if childSessionID == "" {
				continue
			}
			e["childSessionId"] = childSessionID
			if taskCallID != "" {
				e["taskCallId"] = taskCallID
			}
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
		// The same attribution rules as the live stream: a compaction summary is
		// never accepted here (its text is internal context), and the
		// post-compaction fallback is limited to messages created after this
		// prompt started, since this list is the whole session history and a prior
		// turn's text would otherwise overwrite this prompt's answer.
		msgID := gstr(info, "id")
		disposition := s.attribution.assistantDisposition(
			msgID, gstr(info, "parentID"), info["summary"] == true, messageCreatedTime(info))
		if disposition != assistantOutput {
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
		b.log.Info("session_title.skip", "reason", "no_info", "opencode_session_id", s.opencodeSessionID, "props", props)
		return nil
	}
	sid := gstr(props, "sessionID")
	if sid == "" {
		sid = gstr(info, "id")
	}
	if sid != s.opencodeSessionID {
		b.log.Info("session_title.skip", "reason", "session_id_mismatch", "event_session_id", sid, "opencode_session_id", s.opencodeSessionID, "props", props)
		return nil
	}

	rawTitle := gstr(info, "title")
	title := normalizeForwardableTitle(rawTitle)
	if title == "" {
		b.log.Info("session_title.skip", "reason", "empty_or_default_title", "raw_title", rawTitle, "opencode_session_id", sid, "props", props)
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if title == b.lastForwardedSessionTitle {
		b.log.Info("session_title.skip", "reason", "duplicate", "title", title, "opencode_session_id", sid, "props", props)
		return nil
	}
	b.lastForwardedSessionTitle = title
	b.log.Info("session_title.forward", "title", title, "opencode_session_id", sid, "props", props)
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
