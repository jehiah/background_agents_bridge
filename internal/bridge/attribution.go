package bridge

import "time"

// assistantDisposition is what a prompt may do with one assistant message.
type assistantDisposition int

const (
	// assistantReject: the message is not this prompt's, in any respect.
	assistantReject assistantDisposition = iota
	// assistantErrorOnly: a compaction summary correlated to this prompt. Its
	// text is internal context and never reaches the transcript, but an error
	// on it is this prompt's failure.
	assistantErrorOnly
	// assistantOutput: the message is this prompt's assistant output.
	assistantOutput
)

// messageAttribution owns which OpenCode messages belong to one prompt. The
// rules are subtle — parentID correlation, compaction rewriting the chain,
// summaries that match by parentID but are not output — and the same question
// is asked from both the live stream and the final-state reconciliation pass,
// so they live in one place rather than being restated at each call site.
type messageAttribution struct {
	promptUserMessageID string
	// promptStarted is the boundary the compaction fallback orders against.
	// OpenCode stamps every message with time.created on the same clock, so the
	// two are comparable.
	promptStarted time.Time

	userMessageIDs            map[string]bool
	allowedAssistantMessageID map[string]bool
	correlatedSummaryIDs      map[string]bool
	compactionOccurred        bool
}

func newMessageAttribution(promptUserMessageID string, promptStarted time.Time) *messageAttribution {
	return &messageAttribution{
		promptUserMessageID:       promptUserMessageID,
		promptStarted:             promptStarted,
		userMessageIDs:            map[string]bool{promptUserMessageID: true},
		allowedAssistantMessageID: map[string]bool{},
		correlatedSummaryIDs:      map[string]bool{},
	}
}

// addUserMessage records a user message id for this prompt, reporting whether it
// was new. OpenCode can regenerate the id it was handed, so the actual one has
// to be learned from the stream.
func (a *messageAttribution) addUserMessage(messageID string) bool {
	isNew := !a.userMessageIDs[messageID]
	a.userMessageIDs[messageID] = true
	return isNew
}

func (a *messageAttribution) parentMatches(parentID string) bool {
	return a.userMessageIDs[parentID]
}

func (a *messageAttribution) allowAssistant(messageID string) {
	a.allowedAssistantMessageID[messageID] = true
}

func (a *messageAttribution) isAssistantAllowed(messageID string) bool {
	return a.allowedAssistantMessageID[messageID]
}

func (a *messageAttribution) markCompacted() { a.compactionOccurred = true }

// assistantDisposition classifies one assistant message and, when the answer is
// assistantOutput, records the authorization so later parts of the same message
// are forwarded without re-deciding.
// created is the message's own creation time, or nil when OpenCode did not
// report a usable one.
func (a *messageAttribution) assistantDisposition(
	messageID, parentID string, isSummary bool, created *time.Time,
) assistantDisposition {
	parentMatches := a.parentMatches(parentID)
	if isSummary && parentMatches {
		a.correlatedSummaryIDs[messageID] = true
	}
	if a.correlatedSummaryIDs[messageID] {
		return assistantErrorOnly
	}
	if isSummary {
		return assistantReject
	}
	if parentMatches || a.isAssistantAllowed(messageID) || a.compactionFallbackAccepts(created) {
		a.allowAssistant(messageID)
		return assistantOutput
	}
	return assistantReject
}

// compactionFallbackAccepts reports whether the post-compaction fallback may
// claim an assistant message. Compaction rewrites the message chain, so parentID
// correlation stops matching and acceptance falls back to unparented assistant
// messages. OpenCode also reports the session's full history (over SSE and from
// the message-list API), so the fallback has to be scoped to messages created
// during this prompt or prior turns' output would be replayed as current output.
//
// The ordering is by creation time, not by message id: OpenCode ids encode a
// 48-bit truncation of their creation time, so they stop sorting monotonically
// every ~795 days, and across such a rollover an earlier turn's messages compare
// greater than this prompt's. A message with no timestamp is rejected rather
// than risk that replay.
//
// The comparison is strict because the boundary is truncated to whole
// milliseconds: a prior turn's message created earlier within the boundary
// millisecond would otherwise be claimed. Nothing this prompt produces can share
// that millisecond — the boundary is taken before the prompt is posted, and this
// fallback only runs after a compaction and a model round trip.
func (a *messageAttribution) compactionFallbackAccepts(created *time.Time) bool {
	if !a.compactionOccurred || created == nil {
		return false
	}
	return created.UnixMilli() > a.promptStarted.UnixMilli()
}
