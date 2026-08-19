package bridge

// maxPendingChildActivity bounds the child activity held back while waiting for
// the Task call that owns it. A child session that never advertises its task
// would otherwise buffer without limit.
const maxPendingChildActivity = 2000

// pendingChildActivity is child-session output withheld until the Task call it
// belongs to is known: either an assistant message (messageID set, its parts
// already buffered in streamState.pendingParts) or a session error (errMsg).
//
// canCorrelate is captured when the activity is queued: false means the child's
// task had already closed, so the activity must not be attributed to whatever
// task a later resume of the same child session gets bound to.
type pendingChildActivity struct {
	childSessionID string
	messageID      string
	errMsg         string
	isErr          bool
	canCorrelate   bool
}

// messageDisposition is what the correlator decided about a child assistant
// message: forward it now, hold it until its task shows up, or drop it because
// the pending buffer is full.
type messageDisposition int

const (
	messageAuthorized messageDisposition = iota
	messageQueued
	messageDropped
)

// childActivityCorrelator owns the Task-call ↔ child-session mapping and the
// pending activity for one prompt. It holds no bridge state and emits nothing;
// the caller turns its decisions into events.
type childActivityCorrelator struct {
	tracked map[string]bool
	// sessionTaskCallIDs / taskCallSessionIDs are the live binding in both
	// directions; closedSessionTaskCallIDs remembers the binding of a task that
	// has finished, so late activity keeps its owner.
	sessionTaskCallIDs       map[string]string
	taskCallSessionIDs       map[string]string
	closedSessionTaskCallIDs map[string]string
	messageTaskCallIDs       map[string]string
	pending                  []pendingChildActivity
	dropLogged               bool
}

func newChildActivityCorrelator() *childActivityCorrelator {
	return &childActivityCorrelator{
		tracked:                  map[string]bool{},
		sessionTaskCallIDs:       map[string]string{},
		taskCallSessionIDs:       map[string]string{},
		closedSessionTaskCallIDs: map[string]string{},
		messageTaskCallIDs:       map[string]string{},
	}
}

// track records a child session, reporting whether it is newly seen.
func (c *childActivityCorrelator) track(childSessionID string) bool {
	isNew := !c.tracked[childSessionID]
	c.tracked[childSessionID] = true
	return isNew
}

func (c *childActivityCorrelator) isTracked(childSessionID string) bool {
	return childSessionID != "" && c.tracked[childSessionID]
}

// associate binds a child session to the Task call that spawned it, reporting
// whether the child session is newly seen. A rebind (task resume) clears the
// closed record so the new task owns subsequent activity.
func (c *childActivityCorrelator) associate(childSessionID, taskCallID string) bool {
	isNew := c.track(childSessionID)
	c.sessionTaskCallIDs[childSessionID] = taskCallID
	c.taskCallSessionIDs[taskCallID] = childSessionID
	delete(c.closedSessionTaskCallIDs, childSessionID)
	return isNew
}

// activeTask returns the Task call currently running the child session.
func (c *childActivityCorrelator) activeTask(childSessionID string) string {
	return c.sessionTaskCallIDs[childSessionID]
}

// taskForActivity is activeTask, falling back to the task that most recently
// closed for this child: activity that trails a completed Task still belongs
// under it.
func (c *childActivityCorrelator) taskForActivity(childSessionID string) string {
	if id := c.activeTask(childSessionID); id != "" {
		return id
	}
	return c.closedSessionTaskCallIDs[childSessionID]
}

func (c *childActivityCorrelator) childForTask(taskCallID string) string {
	return c.taskCallSessionIDs[taskCallID]
}

func (c *childActivityCorrelator) taskForMessage(messageID string) string {
	return c.messageTaskCallIDs[messageID]
}

// authorizeOrQueueMessage decides what to do with a child assistant message.
// Once a message is authorized its task binding is remembered, so later parts
// of the same message keep the same owner.
func (c *childActivityCorrelator) authorizeOrQueueMessage(childSessionID, messageID string) messageDisposition {
	if _, ok := c.messageTaskCallIDs[messageID]; ok {
		return messageAuthorized
	}
	if taskCallID := c.taskForActivity(childSessionID); taskCallID != "" {
		c.messageTaskCallIDs[messageID] = taskCallID
		return messageAuthorized
	}
	for _, a := range c.pending {
		if !a.isErr && a.messageID == messageID {
			return messageQueued
		}
	}
	if len(c.pending) >= maxPendingChildActivity {
		return messageDropped
	}
	c.pending = append(c.pending, pendingChildActivity{
		childSessionID: childSessionID,
		messageID:      messageID,
		canCorrelate:   !c.isClosed(childSessionID),
	})
	return messageQueued
}

// queueError holds a child session error until its Task call is known,
// reporting false when the pending buffer is full.
func (c *childActivityCorrelator) queueError(childSessionID, errMsg string) bool {
	if len(c.pending) >= maxPendingChildActivity {
		return false
	}
	c.pending = append(c.pending, pendingChildActivity{
		childSessionID: childSessionID,
		errMsg:         errMsg,
		isErr:          true,
		canCorrelate:   !c.isClosed(childSessionID),
	})
	return true
}

func (c *childActivityCorrelator) isClosed(childSessionID string) bool {
	_, ok := c.closedSessionTaskCallIDs[childSessionID]
	return ok
}

// release returns the pending activity for a child session that just gained a
// Task call, binding each queued message to it. Activity queued after the
// child's previous task closed stays pending: it belongs to no task, and this
// one is not it.
func (c *childActivityCorrelator) release(childSessionID string) []pendingChildActivity {
	var released, remaining []pendingChildActivity
	for _, a := range c.pending {
		if a.childSessionID != childSessionID || !a.canCorrelate {
			remaining = append(remaining, a)
			continue
		}
		if !a.isErr {
			if taskCallID := c.activeTask(childSessionID); taskCallID != "" {
				c.messageTaskCallIDs[a.messageID] = taskCallID
			}
		}
		released = append(released, a)
	}
	c.pending = remaining
	return released
}

// flush drains everything still pending, for emission without a Task call.
func (c *childActivityCorrelator) flush() []pendingChildActivity {
	pending := c.pending
	c.pending = nil
	return pending
}

// taskForPending is the Task call to stamp on a released or flushed activity.
func (c *childActivityCorrelator) taskForPending(a pendingChildActivity) string {
	if !a.canCorrelate {
		return ""
	}
	return c.activeTask(a.childSessionID)
}

// closeTask records that a Task call finished. The binding moves to the closed
// map rather than disappearing, so trailing activity is still attributable.
func (c *childActivityCorrelator) closeTask(taskCallID string) {
	childSessionID := c.childForTask(taskCallID)
	if childSessionID == "" || c.activeTask(childSessionID) != taskCallID {
		return
	}
	delete(c.sessionTaskCallIDs, childSessionID)
	c.closedSessionTaskCallIDs[childSessionID] = taskCallID
}

// shouldLogDrop reports true once per prompt, keeping the overflow warning from
// repeating for every dropped item.
func (c *childActivityCorrelator) shouldLogDrop() bool {
	if c.dropLogged {
		return false
	}
	c.dropLogged = true
	return true
}
