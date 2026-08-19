package bridge

import (
	"fmt"
	"testing"
)

// TestChildActivityCorrelatesAndKeepsOwnership verifies a message queued before
// its Task call is bound to it on release, and keeps that owner after the task
// closes.
func TestChildActivityCorrelatesAndKeepsOwnership(t *testing.T) {
	c := newChildActivityCorrelator()
	c.track("child-1")

	if d := c.authorizeOrQueueMessage("child-1", "message-1"); d != messageQueued {
		t.Fatalf("disposition = %v, want queued", d)
	}
	c.associate("child-1", "task-1")

	released := c.release("child-1")
	if len(released) != 1 || released[0].messageID != "message-1" || !released[0].canCorrelate {
		t.Fatalf("release = %+v", released)
	}
	c.closeTask("task-1")

	if got := c.taskForMessage("message-1"); got != "task-1" {
		t.Errorf("taskForMessage = %q, want task-1", got)
	}
	// A closed task still owns activity that trails it.
	if d := c.authorizeOrQueueMessage("child-1", "message-2"); d != messageAuthorized {
		t.Errorf("trailing message disposition = %v, want authorized", d)
	}
}

// TestChildActivityNotReassignedToResumedTask verifies activity queued after a
// task closed is never attributed to the next task the same child gets bound
// to: it belonged to neither, so it is emitted uncorrelated.
func TestChildActivityNotReassignedToResumedTask(t *testing.T) {
	c := newChildActivityCorrelator()
	c.associate("child-1", "task-1")
	c.closeTask("task-1")
	if !c.queueError("child-1", "late error") {
		t.Fatal("queueError = false")
	}
	c.associate("child-1", "task-2")

	if released := c.release("child-1"); len(released) != 0 {
		t.Fatalf("release = %+v, want nothing", released)
	}
	pending := c.flush()
	if len(pending) != 1 || pending[0].errMsg != "late error" || pending[0].canCorrelate {
		t.Fatalf("flush = %+v", pending)
	}
	if got := c.taskForPending(pending[0]); got != "" {
		t.Errorf("taskForPending = %q, want empty", got)
	}
}

// TestChildActivityBoundedAndDropLoggedOnce verifies the pending buffer is
// capped and the overflow warning fires once per prompt.
func TestChildActivityBoundedAndDropLoggedOnce(t *testing.T) {
	c := newChildActivityCorrelator()
	for i := range maxPendingChildActivity {
		if !c.queueError("child-1", fmt.Sprintf("error-%d", i)) {
			t.Fatalf("queueError(%d) = false below the limit", i)
		}
	}
	if c.queueError("child-1", "overflow") {
		t.Error("queueError past the limit = true")
	}
	if d := c.authorizeOrQueueMessage("child-1", "message-1"); d != messageDropped {
		t.Errorf("disposition past the limit = %v, want dropped", d)
	}
	if !c.shouldLogDrop() {
		t.Error("first shouldLogDrop = false")
	}
	if c.shouldLogDrop() {
		t.Error("second shouldLogDrop = true")
	}
}
