package bridge

import "time"

// event is an outbound message to the control plane. It is modeled as a plain
// map so the generic buffering / ACK machinery can treat all events uniformly
// and so on-the-wire JSON matches the Python bridge exactly (no struct field
// would silently appear or disappear). Constructors below are the single source
// of truth for field names.
type event map[string]any

// GitUser is a git identity used for commit attribution.
type GitUser struct {
	Name  string
	Email string
}

// fallbackGitUser matches the co-author trailer used in shared/git.ts when a
// prompt author has no SCM name/email configured.
var fallbackGitUser = GitUser{Name: "OpenInspect", Email: "open-inspect@noreply.github.com"}

// nowUnix returns the current time as fractional Unix seconds, matching
// Python's time.time().
func nowUnix() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// nullable returns s, or nil (JSON null) when s is empty. Several events carry
// a field that is present-but-null when unset (e.g. opencodeSessionId).
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- outbound event constructors ---------------------------------------------
//
// None of these set sandboxId or timestamp; sendEvent stamps those (and ackId
// for critical events) just before transmission, matching _send_event.

func readyEvent(opencodeSessionID string) event {
	return event{"type": "ready", "opencodeSessionId": nullable(opencodeSessionID)}
}

func heartbeatEvent(status string) event {
	return event{"type": "heartbeat", "status": status, "timestamp": nowUnix()}
}

func tokenEvent(content, messageID string) event {
	return event{"type": "token", "content": content, "messageId": messageID}
}

func toolCallEvent(tool string, args any, callID, status string, output any, messageID string) event {
	return event{
		"type":      "tool_call",
		"tool":      tool,
		"args":      args,
		"callId":    callID,
		"status":    status,
		"output":    output,
		"messageId": messageID,
	}
}

func stepStartEvent(messageID string) event {
	return event{"type": "step_start", "messageId": messageID}
}

func stepFinishEvent(cost, tokens, reason any, messageID string) event {
	return event{
		"type":      "step_finish",
		"cost":      cost,
		"tokens":    tokens,
		"reason":    reason,
		"messageId": messageID,
	}
}

func sessionTitleEvent(title string) event {
	return event{"type": "session_title", "title": title}
}

func errorEvent(errMsg, messageID string) event {
	return event{"type": "error", "error": errMsg, "messageId": messageID}
}

func executionCompleteEvent(messageID string, success bool, errMsg string) event {
	e := event{"type": "execution_complete", "messageId": messageID, "success": success}
	if errMsg != "" {
		e["error"] = errMsg
	}
	return e
}

func snapshotReadyEvent(opencodeSessionID string) event {
	return event{"type": "snapshot_ready", "opencodeSessionId": nullable(opencodeSessionID)}
}

func pushCompleteEvent(branch string) event {
	return event{"type": "push_complete", "branchName": branch, "timestamp": nowUnix()}
}

// pushErrorEvent builds a push_error. branchName is included only when
// withBranch is true (the "no repository" case omits it, matching Python).
func pushErrorEvent(errMsg, branch string, withBranch bool) event {
	e := event{"type": "push_error", "error": errMsg, "timestamp": nowUnix()}
	if withBranch {
		e["branchName"] = branch
	}
	return e
}

// --- inbound commands --------------------------------------------------------

// command is a message received from the control plane. Fields cover every
// command variant; unused ones stay zero-valued.
type command struct {
	Type            string        `json:"type"`
	MessageID       string        `json:"messageId"`
	MessageIDSnake  string        `json:"message_id"`
	Content         string        `json:"content"`
	Model           string        `json:"model"`
	ReasoningEffort string        `json:"reasoningEffort"`
	Author          commandAuthor `json:"author"`
	PushSpec        *pushSpec     `json:"pushSpec"`
	AckID           string        `json:"ackId"`
}

type commandAuthor struct {
	SCMName  string `json:"scmName"`
	SCMEmail string `json:"scmEmail"`
}

type pushSpec struct {
	TargetBranch      string `json:"targetBranch"`
	Refspec           string `json:"refspec"`
	RemoteURL         string `json:"remoteUrl"`
	RedactedRemoteURL string `json:"redactedRemoteUrl"`
	Force             bool   `json:"force"`
}

// msgID returns the prompt message ID, accepting either camelCase or snake_case
// and falling back to "unknown", matching the Python lookup order.
func (c *command) msgID() string {
	if c.MessageID != "" {
		return c.MessageID
	}
	if c.MessageIDSnake != "" {
		return c.MessageIDSnake
	}
	return "unknown"
}
