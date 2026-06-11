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

	sessionIDFile string
	repoPath      string

	rootCtx context.Context
	cancel  context.CancelFunc

	// mu guards the connection and all reconnection-spanning state.
	mu                        sync.Mutex
	conn                      *websocket.Conn
	eventBuffer               []event
	pendingAcks               map[string]event
	opencodeSessionID         string
	lastForwardedSessionTitle string

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
		sandboxID:       sandboxID,
		sessionID:       sessionID,
		controlPlaneURL: controlPlaneURL,
		authToken:       authToken,
		opencodePort:    opencodePort,
		opencodeBaseURL: fmt.Sprintf("http://localhost:%d", opencodePort),
		log:             log,
		ids:             &identifier{},
		pendingAcks:     make(map[string]event),
		sessionIDFile:   filepath.Join(os.TempDir(), "opencode-session-id"),
		repoPath:        "/workspace",
		gitSyncDoneC:    make(chan struct{}),
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

	attempts := 0
	for ctx.Err() == nil {
		err := b.connectAndRun(ctx)
		switch {
		case err == nil:
			attempts = 0
		case errors.Is(err, errSessionTerminated):
			b.log.Info("bridge.disconnect", "reason", "session_terminated", "detail", err.Error())
			cancel()
		case isFatalConnectionError(err):
			b.log.Error("bridge.disconnect", "reason", "fatal_error", "exc", err)
			cancel()
		case ctx.Err() != nil:
			// Shutting down; suppress the noisy close error.
		default:
			b.log.Warn("bridge.disconnect", "reason", "connection_error", "detail", err.Error())
		}

		if ctx.Err() != nil {
			break
		}

		attempts++
		delay := reconnectDelay(attempts)
		b.log.Info("bridge.reconnect", "attempt", attempts, "delay_s", delay.Seconds())
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
