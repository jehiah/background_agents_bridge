package bridge

import (
	"testing"
	"time"
)

// at returns a pointer to the creation time offset from promptStart (the
// boundary the compaction fallback orders against), for the created argument of
// assistantDisposition.
func at(offset time.Duration) *time.Time {
	t := promptStart.Add(offset)
	return &t
}

// TestAttributionAcceptsParentedAssistant covers the ordinary case: the reply
// parented by this prompt's user message is output.
func TestAttributionAcceptsParentedAssistant(t *testing.T) {
	a := newMessageAttribution("msg_user", promptStart)

	if got := a.assistantDisposition("msg_a", "msg_user", false, at(time.Second)); got != assistantOutput {
		t.Fatalf("disposition = %v, want output", got)
	}
	if !a.isAssistantAllowed("msg_a") {
		t.Error("accepting a message did not record the authorization")
	}
}

// TestAttributionRejectsUnrelatedAssistant verifies a reply to someone else's
// user message is not this prompt's, absent compaction — even though it was
// created after this prompt started.
func TestAttributionRejectsUnrelatedAssistant(t *testing.T) {
	a := newMessageAttribution("msg_user", promptStart)

	if got := a.assistantDisposition("msg_x", "someone_else", false, at(time.Second)); got != assistantReject {
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
	a := newMessageAttribution("msg_user", promptStart)

	if !a.addUserMessage("msg_actual") {
		t.Error("addUserMessage should report a newly discovered id")
	}
	if a.addUserMessage("msg_actual") {
		t.Error("addUserMessage should report a repeat id as not new")
	}
	if got := a.assistantDisposition("msg_a", "msg_actual", false, at(time.Second)); got != assistantOutput {
		t.Fatalf("disposition = %v, want output", got)
	}
}

// TestAttributionCompactionSummaryIsErrorOnly verifies a summary correlated to
// this prompt yields its error but never its text, while an uncorrelated
// summary is rejected outright.
func TestAttributionCompactionSummaryIsErrorOnly(t *testing.T) {
	a := newMessageAttribution("msg_user", promptStart)

	if got := a.assistantDisposition("msg_summary", "msg_user", true, at(time.Second)); got != assistantErrorOnly {
		t.Fatalf("correlated summary = %v, want errorOnly", got)
	}
	if a.isAssistantAllowed("msg_summary") {
		t.Error("summary text was authorized for output")
	}
	// A later update of the same summary stays error-only rather than
	// becoming output.
	if got := a.assistantDisposition("msg_summary", "other", true, at(2*time.Second)); got != assistantErrorOnly {
		t.Errorf("re-seen summary = %v, want errorOnly", got)
	}
	if got := a.assistantDisposition("msg_other_summary", "someone_else", true, at(time.Second)); got != assistantReject {
		t.Errorf("uncorrelated summary = %v, want reject", got)
	}
}

// TestAttributionCompactionFallbackScope verifies the post-compaction fallback
// claims only messages created after this prompt started: before compaction
// nothing is claimed, and afterwards a prior turn stays out.
func TestAttributionCompactionFallbackScope(t *testing.T) {
	a := newMessageAttribution("msg_user", promptStart)

	if got := a.assistantDisposition("msg_later", "changed", false, at(5*time.Second)); got != assistantReject {
		t.Fatalf("pre-compaction disposition = %v, want reject", got)
	}
	a.markCompacted()
	if got := a.assistantDisposition("msg_prior", "old_user", false, at(-time.Minute)); got != assistantReject {
		t.Errorf("prior-turn disposition = %v, want reject", got)
	}
	if got := a.assistantDisposition("msg_later", "changed", false, at(5*time.Second)); got != assistantOutput {
		t.Errorf("post-prompt disposition = %v, want output", got)
	}
}

// TestAttributionCompactionFallbackBoundary verifies the two edges of the
// fallback's ordering rule: a message stamped in the boundary millisecond
// itself is not claimed (it cannot be ours — the boundary is taken before the
// prompt is posted), and neither is one OpenCode reported with no usable
// timestamp, since ordering it is impossible.
func TestAttributionCompactionFallbackBoundary(t *testing.T) {
	a := newMessageAttribution("msg_user", promptStart)
	a.markCompacted()

	if got := a.assistantDisposition("msg_same_ms", "changed", false, at(0)); got != assistantReject {
		t.Errorf("boundary-millisecond disposition = %v, want reject", got)
	}
	if got := a.assistantDisposition("msg_no_time", "changed", false, nil); got != assistantReject {
		t.Errorf("untimestamped disposition = %v, want reject", got)
	}
	// Sub-millisecond precision on the boundary does not smuggle a message in:
	// both sides are compared as whole milliseconds.
	a2 := newMessageAttribution("msg_user", promptStart.Add(400*time.Microsecond))
	a2.markCompacted()
	if got := a2.assistantDisposition("msg_same_ms", "changed", false, at(0)); got != assistantReject {
		t.Errorf("sub-millisecond boundary disposition = %v, want reject", got)
	}
}

// TestAttributionAlreadyAllowedStaysAllowed verifies a message authorized
// out-of-band — a child message released by its Task call — is still output
// when it later comes back through the disposition check with a parentID that
// matches nothing.
func TestAttributionAlreadyAllowedStaysAllowed(t *testing.T) {
	a := newMessageAttribution("msg_user", promptStart)
	a.allowAssistant("msg_child")

	if got := a.assistantDisposition("msg_child", "unrelated", false, nil); got != assistantOutput {
		t.Fatalf("disposition = %v, want output", got)
	}
}
