package bridge

import (
	"maps"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

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

// readyEvent announces the handshake. repositories tells the control plane
// which checkouts have a session diff baseline (and what it is), so the viewer
// knows what the sandbox will be diffing against; an entry without a baseline
// is omitted rather than sent as null.
func readyEvent(opencodeSessionID string, repositories []repomanifest.Entry) event {
	entries := []any{}
	for position, repository := range repositories {
		if repository.BaseSHA == "" {
			continue
		}
		entries = append(entries, map[string]any{
			"position":  position,
			"repoOwner": repository.Owner,
			"repoName":  repository.Name,
			"baseSha":   repository.BaseSHA,
		})
	}
	return event{
		"type":              "ready",
		"opencodeSessionId": nullable(opencodeSessionID),
		"repositories":      entries,
	}
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

// warningEvent forwards a supervisor boot warning drained after the handshake.
// entry carries at least "message"; any of scope/repoOwner/repoName it holds are
// preserved verbatim.
func warningEvent(entry map[string]any) event {
	e := event{"type": "warning"}
	maps.Copy(e, entry)
	return e
}

// pushCompleteEvent builds a push_complete. repoFields echoes repoOwner/repoName
// when the push spec carried a repo identity (empty otherwise).
func pushCompleteEvent(branch string, repoFields map[string]any) event {
	e := event{"type": "push_complete", "branchName": branch, "timestamp": nowUnix()}
	maps.Copy(e, repoFields)
	return e
}

// pushErrorEvent builds a push_error. branchName is always included (even when
// empty) so the control plane can resolve its pending push instead of leaking
// it; repoFields echoes repoOwner/repoName when the spec carried them.
func pushErrorEvent(errMsg, branch string, repoFields map[string]any) event {
	e := event{"type": "push_error", "error": errMsg, "branchName": branch, "timestamp": nowUnix()}
	maps.Copy(e, repoFields)
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
	// Attachments is the untyped session-image attachment list from the control
	// plane (validated by parseSessionImageAttachments). Kept as `any` so a
	// non-list value is a validation reject, not a JSON unmarshal failure,
	// matching the Python bridge's untyped WebSocket boundary.
	Attachments any `json:"attachments"`
}

type commandAuthor struct {
	// GitIdentity is the control plane's explicit attribution mode. Older
	// control planes omit it and send scmName/scmEmail instead.
	GitIdentity *gitIdentity `json:"gitIdentity"`
	SCMName     string       `json:"scmName"`
	SCMEmail    string       `json:"scmEmail"`
}

// gitIdentity is the prompt's commit attribution: mode "agent-only" (no
// user to attribute to) or "attributed-user" with the trusted GitHub
// user's name and email.
type gitIdentity struct {
	Mode  string `json:"mode"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type pushSpec struct {
	TargetBranch      string `json:"targetBranch"`
	RepoOwner         string `json:"repoOwner"`
	RepoName          string `json:"repoName"`
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
