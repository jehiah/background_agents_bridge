package bridge

import "testing"

// TestAttributionAcceptsParentedAssistant covers the ordinary case: the reply
// parented by this prompt's user message is output.
func TestAttributionAcceptsParentedAssistant(t *testing.T) {
	a := newMessageAttribution("msg_user")

	if got := a.assistantDisposition("msg_a", "msg_user", false); got != assistantOutput {
		t.Fatalf("disposition = %v, want output", got)
	}
	if !a.isAssistantAllowed("msg_a") {
		t.Error("accepting a message did not record the authorization")
	}
}

// TestAttributionRejectsUnrelatedAssistant verifies a reply to someone else's
// user message is not this prompt's, absent compaction.
func TestAttributionRejectsUnrelatedAssistant(t *testing.T) {
	a := newMessageAttribution("msg_user")

	if got := a.assistantDisposition("msg_x", "someone_else", false); got != assistantReject {
		t.Fatalf("disposition = %v, want reject", got)
	}
	if a.isAssistantAllowed("msg_x") {
		t.Error("rejected message was authorized")
	}
}

// TestAttributionLearnsRegeneratedUserMessageID verifies OpenCode regenerating
// the id we handed it still correlates: the actual id is learned from the
// stream and the reply it parents is accepted.
func TestAttributionLearnsRegeneratedUserMessageID(t *testing.T) {
	a := newMessageAttribution("msg_user")

	if !a.addUserMessage("msg_actual") {
		t.Error("addUserMessage should report a newly discovered id")
	}
	if a.addUserMessage("msg_actual") {
		t.Error("addUserMessage should report a repeat id as not new")
	}
	if got := a.assistantDisposition("msg_a", "msg_actual", false); got != assistantOutput {
		t.Fatalf("disposition = %v, want output", got)
	}
}

// TestAttributionCompactionSummaryIsErrorOnly verifies a summary correlated to
// this prompt yields its error but never its text, while an uncorrelated
// summary is rejected outright.
func TestAttributionCompactionSummaryIsErrorOnly(t *testing.T) {
	a := newMessageAttribution("msg_user")

	if got := a.assistantDisposition("msg_summary", "msg_user", true); got != assistantErrorOnly {
		t.Fatalf("correlated summary = %v, want errorOnly", got)
	}
	if a.isAssistantAllowed("msg_summary") {
		t.Error("summary text was authorized for output")
	}
	// A later update of the same summary stays error-only rather than
	// becoming output.
	if got := a.assistantDisposition("msg_summary", "other", true); got != assistantErrorOnly {
		t.Errorf("re-seen summary = %v, want errorOnly", got)
	}
	if got := a.assistantDisposition("msg_other_summary", "someone_else", true); got != assistantReject {
		t.Errorf("uncorrelated summary = %v, want reject", got)
	}
}

// TestAttributionCompactionFallbackScope verifies the post-compaction fallback
// claims only messages created after this prompt's user message: before
// compaction nothing is claimed, and afterwards a prior turn stays out.
func TestAttributionCompactionFallbackScope(t *testing.T) {
	promptID := ocMessageID(promptTSMs, 2, 'p')
	priorID := ocMessageID(promptTSMs-60_000, 1, 'q')
	laterID := ocMessageID(promptTSMs+5_000, 1, 'r')
	a := newMessageAttribution(promptID)

	if got := a.assistantDisposition(laterID, "changed", false); got != assistantReject {
		t.Fatalf("pre-compaction disposition = %v, want reject", got)
	}
	a.markCompacted()
	if got := a.assistantDisposition(priorID, "old_user", false); got != assistantReject {
		t.Errorf("prior-turn disposition = %v, want reject", got)
	}
	if got := a.assistantDisposition(laterID, "changed", false); got != assistantOutput {
		t.Errorf("post-prompt disposition = %v, want output", got)
	}
}

// TestAttributionAlreadyAllowedStaysAllowed verifies a message authorized
// out-of-band — a child message released by its Task call — is still output
// when it later comes back through the disposition check with a parentID that
// matches nothing.
func TestAttributionAlreadyAllowedStaysAllowed(t *testing.T) {
	a := newMessageAttribution("msg_user")
	a.allowAssistant("msg_child")

	if got := a.assistantDisposition("msg_child", "unrelated", false); got != assistantOutput {
		t.Fatalf("disposition = %v, want output", got)
	}
}
