// Package bridge implements the sandbox-side agent bridge: bidirectional
// communication between a local OpenCode instance and the background-agents
// control plane.
//
// It is a Go port of the upstream Python bridge
// (packages/sandbox-runtime/src/sandbox_runtime/bridge.py). The on-the-wire
// protocol — event shapes, ack IDs, ascending message IDs, OpenCode request
// bodies — is kept byte-compatible with the original; the internals are
// idiomatic Go (contexts and goroutines in place of asyncio tasks).
package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// errSessionTerminated is returned when the control plane has terminated the
// session (HTTP 401/403/404/410). It is non-recoverable: the bridge exits
// gracefully rather than retrying.
var errSessionTerminated = errors.New("session terminated by control plane")

// AgentBridge bridges a sandbox OpenCode instance and the control plane.
type AgentBridge struct {
	sandboxID       string
	sessionID       string
	controlPlaneURL string
	authToken       string
	opencodePort    int
	opencodeBaseURL string

	log                  *slog.Logger
	sseInactivityTimeout time.Duration
	httpClient           *http.Client
	ids                  *identifier

	sessionIDFile    string
	workspacePath    string
	repoManifestPath string
	bootWarningsPath string

	rootCtx context.Context
	cancel  context.CancelFunc

	// mu guards the connection and all reconnection-spanning state.
	mu                        sync.Mutex
	conn                      *websocket.Conn
	eventBuffer               []event
	pendingAcks               map[string]event
	opencodeSessionID         string
	lastForwardedSessionTitle string

	// Aggregate connection observability, also guarded by mu. connectedAt is
	// zero whenever no connection is active.
	connectedAt            time.Time
	connectionCount        int
	reconnectAttemptCount  int
	totalConnectedDuration time.Duration

	// promptMu guards the in-flight prompt's cancel func and generation.
	promptMu      sync.Mutex
	cancelPromptF context.CancelFunc
	promptGen     int

	gitSyncOnce  sync.Once
	gitSyncDoneC chan struct{}
}

// New constructs an AgentBridge. log should already carry base attributes
// (service, sandbox_id, session_id).
func New(sandboxID, sessionID, controlPlaneURL, authToken string, opencodePort int, log *slog.Logger) *AgentBridge {
	b := &AgentBridge{
		sandboxID:        sandboxID,
		sessionID:        sessionID,
		controlPlaneURL:  controlPlaneURL,
		authToken:        authToken,
		opencodePort:     opencodePort,
		opencodeBaseURL:  fmt.Sprintf("http://127.0.0.1:%d", opencodePort),
		log:              log,
		ids:              &identifier{},
		pendingAcks:      make(map[string]event),
		sessionIDFile:    filepath.Join(os.TempDir(), "opencode-session-id"),
		workspacePath:    "/workspace",
		repoManifestPath: repomanifest.ManifestPath,
		bootWarningsPath: defaultBootWarningsPath,
		gitSyncDoneC:     make(chan struct{}),
	}
	b.sseInactivityTimeout = resolveTimeout(
		log, "BRIDGE_SSE_INACTIVITY_TIMEOUT",
		sseInactivityDefault, sseInactivityMin, sseInactivityMax,
	)
	// No global client timeout: SSE streaming needs an unbounded read. Per-call
	// timeouts are applied via context; the dialer keeps a connect timeout.
	b.httpClient = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: httpConnectTimeout}).DialContext,
		},
	}
	return b
}

// wsURL is the control-plane WebSocket URL for this session.
func (b *AgentBridge) wsURL() string {
	u := strings.Replace(b.controlPlaneURL, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return fmt.Sprintf("%s/sessions/%s/ws?type=sandbox", u, b.sessionID)
}

// Run is the main bridge loop with reconnection handling. It returns when the
// context is cancelled or a terminal error occurs.
func (b *AgentBridge) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	b.rootCtx = ctx
	b.cancel = cancel

	b.log.Info("bridge.run_start")
	b.loadSessionID(ctx)

	// The terminal summary: how the run ended plus the lifetime aggregates.
	// runOutcome is reset per iteration so a failure that a later reconnect
	// recovers from does not survive into the summary.
	runOutcome, runDetail := "shutdown", ""
	defer func() {
		args := append([]any{"outcome", runOutcome}, b.aggregateFields()...)
		args = append(args, "total_connected_duration_seconds", roundSeconds(b.connectedDuration()))
		if runDetail != "" {
			args = append(args, "detail", runDetail)
		}
		b.log.Info("bridge.run_complete", args...)
	}()

	attempts := 0
	for ctx.Err() == nil {
		runOutcome, runDetail = "shutdown", ""
		err := b.connectAndRun(ctx)
		switch {
		case err == nil:
			if ctx.Err() == nil {
				runOutcome = "connection_closed"
			}
			attempts = 0
		case errors.Is(err, errSessionTerminated):
			// Non-recoverable: the control plane has terminated the session.
			runOutcome, runDetail = "session_terminated", err.Error()
			cancel()
		case isFatalConnectionError(err):
			runOutcome, runDetail = "fatal_error", err.Error()
			cancel()
		case ctx.Err() != nil:
			// Shutting down; suppress the noisy close error.
		case websocket.CloseStatus(err) != -1:
			// The peer closed the socket; connectAndRun already logged the
			// bridge.disconnect with the close code and the aggregates.
			runOutcome = "connection_closed"
		default:
			runOutcome, runDetail = "connection_error", err.Error()
			b.log.Warn("bridge.connect_error", "detail", err.Error())
		}

		if ctx.Err() != nil {
			break
		}

		attempts++
		b.countReconnectAttempt()
		delay := reconnectDelay(attempts)
		b.log.Info("bridge.reconnect",
			"attempt", attempts,
			"reconnect_attempt_count", b.reconnectAttempts(),
			"delay_s", delay.Seconds(),
		)
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}

	b.cancelPrompt()
	return nil
}

// isFatalConnectionError reports whether err indicates an invalid or terminated
// session that should not be retried.
func isFatalConnectionError(err error) bool {
	s := err.Error()
	for _, p := range []string{"HTTP 401", "HTTP 403", "HTTP 404", "HTTP 410"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// reconnectDelay returns the backoff for the nth reconnect attempt:
// min(base^attempt, max).
func reconnectDelay(attempt int) time.Duration {
	d := time.Duration(math.Pow(reconnectBackoffBase, float64(attempt)) * float64(time.Second))
	if d > reconnectMaxDelay {
		d = reconnectMaxDelay
	}
	return d
}

// --- connection observability ------------------------------------------------
//
// Aggregates for the whole run: how many times the bridge connected, how many
// reconnect attempts it made (including ones that failed), and how long it was
// actually connected. Port of the _mark_connected / _finalize_connection /
// _log_disconnect trio in bridge.py (upstream #1017).

// markConnected records the start of a connection.
func (b *AgentBridge) markConnected() {
	b.mu.Lock()
	b.connectionCount++
	b.connectedAt = time.Now()
	b.mu.Unlock()
}

// countReconnectAttempt records that a reconnect is about to be attempted.
func (b *AgentBridge) countReconnectAttempt() {
	b.mu.Lock()
	b.reconnectAttemptCount++
	b.mu.Unlock()
}

func (b *AgentBridge) reconnectAttempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reconnectAttemptCount
}

// aggregateFields returns the run-level connection counters as log attributes.
func (b *AgentBridge) aggregateFields() []any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return []any{
		"connection_count", b.connectionCount,
		"reconnect_count", max(0, b.connectionCount-1),
		"reconnect_attempt_count", b.reconnectAttemptCount,
	}
}

// connectedDuration is the total time spent connected so far.
func (b *AgentBridge) connectedDuration() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalConnectedDuration
}

// finalizeConnection closes out the active connection and returns its log
// attributes. ok is false when no connection is active — that is what keeps a
// connection from being logged as disconnected twice.
func (b *AgentBridge) finalizeConnection() ([]any, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.connectedAt.IsZero() {
		return nil, false
	}
	duration := max(time.Since(b.connectedAt), 0)
	b.connectedAt = time.Time{}
	b.totalConnectedDuration += duration
	return []any{
		"connection_duration_seconds", roundSeconds(duration),
		"total_connected_duration_seconds", roundSeconds(b.totalConnectedDuration),
		"connection_count", b.connectionCount,
		"reconnect_count", max(0, b.connectionCount-1),
		"reconnect_attempt_count", b.reconnectAttemptCount,
	}, true
}

// logDisconnect emits the single bridge.disconnect for the active connection,
// or nothing if it was already logged.
func (b *AgentBridge) logDisconnect(reason string, level slog.Level, extra ...any) {
	fields, ok := b.finalizeConnection()
	if !ok {
		return
	}
	args := append([]any{"reason", reason}, fields...)
	b.log.Log(context.Background(), level, "bridge.disconnect", append(args, extra...)...)
}

// roundSeconds renders a duration as seconds with millisecond precision, as the
// Python bridge does with round(..., 3).
func roundSeconds(d time.Duration) float64 {
	return math.Round(d.Seconds()*1000) / 1000
}

// --- shared state accessors --------------------------------------------------

func (b *AgentBridge) setConn(c *websocket.Conn) {
	b.mu.Lock()
	b.conn = c
	b.mu.Unlock()
}

func (b *AgentBridge) getConn() *websocket.Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn
}

func (b *AgentBridge) clearConn() {
	b.mu.Lock()
	b.conn = nil
	b.mu.Unlock()
}

func (b *AgentBridge) getOpencodeSessionID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opencodeSessionID
}

func (b *AgentBridge) setOpencodeSessionID(id string) {
	b.mu.Lock()
	b.opencodeSessionID = id
	b.mu.Unlock()
}

// setPromptCancel stores the cancel func for the in-flight prompt and returns a
// generation token used to clear it without clobbering a newer prompt.
func (b *AgentBridge) setPromptCancel(cancel context.CancelFunc) int {
	b.promptMu.Lock()
	defer b.promptMu.Unlock()
	b.promptGen++
	b.cancelPromptF = cancel
	return b.promptGen
}

// clearPromptCancel clears the stored cancel func only if gen is still current.
func (b *AgentBridge) clearPromptCancel(gen int) {
	b.promptMu.Lock()
	defer b.promptMu.Unlock()
	if b.promptGen == gen {
		b.cancelPromptF = nil
	}
}

// cancelPrompt cancels the in-flight prompt, if any.
func (b *AgentBridge) cancelPrompt() {
	b.promptMu.Lock()
	cancel := b.cancelPromptF
	b.promptMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
