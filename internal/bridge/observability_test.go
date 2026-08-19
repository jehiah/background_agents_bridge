package bridge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestConnectionAggregatesTrackLifetimeAndReconnects covers the counters that
// back bridge.disconnect / bridge.run_complete: each connection contributes its
// own duration, the total accumulates, and reconnect_count trails
// connection_count. Port of
// test_connection_aggregate_fields_track_lifetime_and_reconnects (upstream #1017).
func TestConnectionAggregatesTrackLifetimeAndReconnects(t *testing.T) {
	b := testBridge()
	b.reconnectAttemptCount = 2

	b.markConnected()
	b.connectedAt = time.Now().Add(-3250 * time.Millisecond)
	first, ok := b.finalizeConnection()
	if !ok {
		t.Fatal("first finalize reported no active connection")
	}
	b.markConnected()
	b.connectedAt = time.Now().Add(-1500 * time.Millisecond)
	second, ok := b.finalizeConnection()
	if !ok {
		t.Fatal("second finalize reported no active connection")
	}

	assertNear(t, "first connection_duration_seconds", fieldFloat(t, first, "connection_duration_seconds"), 3.25)
	assertNear(t, "first total_connected_duration_seconds", fieldFloat(t, first, "total_connected_duration_seconds"), 3.25)
	assertNear(t, "second connection_duration_seconds", fieldFloat(t, second, "connection_duration_seconds"), 1.5)
	assertNear(t, "second total_connected_duration_seconds", fieldFloat(t, second, "total_connected_duration_seconds"), 4.75)

	for field, want := range map[string]int{"connection_count": 1, "reconnect_count": 0, "reconnect_attempt_count": 2} {
		if got := fieldInt(t, first, field); got != want {
			t.Errorf("first %s = %d, want %d", field, got, want)
		}
	}
	for field, want := range map[string]int{"connection_count": 2, "reconnect_count": 1, "reconnect_attempt_count": 2} {
		if got := fieldInt(t, second, field); got != want {
			t.Errorf("second %s = %d, want %d", field, got, want)
		}
	}
}

// TestFinalizeConnectionWithoutActiveConnection verifies the guard that keeps a
// single connection from being logged as disconnected twice: the first
// logDisconnect wins and later ones are silent.
func TestFinalizeConnectionWithoutActiveConnection(t *testing.T) {
	b := testBridge()
	if _, ok := b.finalizeConnection(); ok {
		t.Fatal("finalize reported a connection that was never made")
	}

	logs := &recordingHandler{}
	b.log = slog.New(logs)
	b.markConnected()
	b.logDisconnect("connection_closed", slog.LevelInfo)
	b.logDisconnect("shutdown_requested", slog.LevelInfo)

	if got := len(logs.records()); got != 1 {
		t.Fatalf("got %d bridge.disconnect records, want 1", got)
	}
}

// TestRunCompleteOnSessionTerminated verifies the terminal summary for a run
// that never connected: the control plane rejects the handshake, so the outcome
// is session_terminated and the aggregates are all zero.
func TestRunCompleteOnSessionTerminated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	logs := &recordingHandler{}
	b := newTestRunBridge(t, server.URL, logs)
	if err := b.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	complete := requireRecord(t, logs, "bridge.run_complete")
	if got := complete["outcome"]; got != "session_terminated" {
		t.Errorf("outcome = %v, want session_terminated", got)
	}
	if got := complete["connection_count"]; got != int64(0) {
		t.Errorf("connection_count = %v, want 0", got)
	}
	if complete["detail"] == nil {
		t.Error("run_complete carries no detail for the terminal error")
	}
	// The per-reason disconnect logs were folded into run_complete.
	if rec := findRecord(logs, "bridge.disconnect"); rec != nil {
		t.Errorf("unexpected bridge.disconnect without a connection: %v", rec)
	}
}

// TestRunCompleteDoesNotRetainTransientOutcome covers a run whose first
// connection is closed by the control plane and whose reconnect succeeds: the
// close is logged once with its code and the aggregates, and the terminal
// summary reports the shutdown that actually ended the run — not the transient
// failure along the way. Port of
// test_run_complete_does_not_retain_transient_outcome (upstream #1017).
func TestRunCompleteDoesNotRetainTransientOutcome(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if connections.Add(1) == 1 {
			conn.Close(websocket.StatusInternalError, "server going away")
			return
		}
		<-r.Context().Done() // hold the reconnect open until the bridge leaves
	}))
	defer server.Close()

	logs := &recordingHandler{}
	b := newTestRunBridge(t, server.URL, logs)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Shut down while the second connection is live, so the run ends in
	// shutdown rather than in the earlier connection loss.
	waitFor(t, func() bool { return countRecords(logs, "bridge.connect") == 2 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if got := requireRecord(t, logs, "bridge.connect")["connection_count"]; got != int64(1) {
		t.Errorf("first bridge.connect connection_count = %v, want 1", got)
	}
	if got := requireRecord(t, logs, "bridge.reconnect")["reconnect_attempt_count"]; got != int64(1) {
		t.Errorf("bridge.reconnect reconnect_attempt_count = %v, want 1", got)
	}

	closed := requireRecord(t, logs, "bridge.disconnect")
	if got := closed["reason"]; got != "connection_closed" {
		t.Errorf("disconnect reason = %v, want connection_closed", got)
	}
	if got := closed["ws_close_code"]; got != int64(websocket.StatusInternalError) {
		t.Errorf("ws_close_code = %v, want %d", got, websocket.StatusInternalError)
	}
	if _, ok := closed["connection_duration_seconds"]; !ok {
		t.Error("disconnect carries no connection_duration_seconds")
	}
	// One disconnect per connection: the read-loop log and the backstop in the
	// deferred cleanup must not both fire.
	if got := countRecords(logs, "bridge.disconnect"); got != 2 {
		t.Fatalf("got %d bridge.disconnect records, want 2 (one per connection)", got)
	}
	if got := lastRecord(logs, "bridge.disconnect")["reason"]; got != "shutdown_requested" {
		t.Errorf("second disconnect reason = %v, want shutdown_requested", got)
	}

	complete := requireRecord(t, logs, "bridge.run_complete")
	if got := complete["outcome"]; got != "shutdown" {
		t.Errorf("outcome = %v, want shutdown", got)
	}
	if got := complete["connection_count"]; got != int64(2) {
		t.Errorf("run_complete connection_count = %v, want 2", got)
	}
	if got := complete["reconnect_count"]; got != int64(1) {
		t.Errorf("run_complete reconnect_count = %v, want 1", got)
	}
	if _, ok := complete["total_connected_duration_seconds"]; !ok {
		t.Error("run_complete carries no total_connected_duration_seconds")
	}
}

// newTestRunBridge builds a bridge pointed at a fake control plane, logging
// into logs.
func newTestRunBridge(t *testing.T, controlPlaneURL string, logs *recordingHandler) *AgentBridge {
	t.Helper()
	b := New("sb-test", "ses-test", controlPlaneURL, "token", 4096, slog.New(logs))
	b.sessionIDFile = t.TempDir() + "/opencode-session-id"
	b.bootWarningsPath = t.TempDir() + "/oi-boot-warnings.jsonl"
	b.repoManifestPath = t.TempDir() + "/oi-repo-manifest.json"
	return b
}

// waitFor blocks until cond holds, or fails the test.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recordingHandler is a slog.Handler that keeps every record's message and
// attributes for assertions.
type recordingHandler struct {
	mu   sync.Mutex
	recs []map[string]any
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := map[string]any{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		rec[a.Key] = a.Value.Resolve().Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, rec)
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) records() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.recs...)
}

// findRecord returns the first record with the given message, or nil.
func findRecord(h *recordingHandler, msg string) map[string]any {
	for _, rec := range h.records() {
		if rec["msg"] == msg {
			return rec
		}
	}
	return nil
}

// lastRecord returns the final record with the given message, or nil.
func lastRecord(h *recordingHandler, msg string) map[string]any {
	var found map[string]any
	for _, rec := range h.records() {
		if rec["msg"] == msg {
			found = rec
		}
	}
	return found
}

func countRecords(h *recordingHandler, msg string) int {
	count := 0
	for _, rec := range h.records() {
		if rec["msg"] == msg {
			count++
		}
	}
	return count
}

func requireRecord(t *testing.T, h *recordingHandler, msg string) map[string]any {
	t.Helper()
	rec := findRecord(h, msg)
	if rec == nil {
		t.Fatalf("no %s record; got %v", msg, h.records())
	}
	return rec
}

// fieldFloat / fieldInt read a value out of a []any attribute list.
func fieldFloat(t *testing.T, fields []any, key string) float64 {
	t.Helper()
	value, ok := fieldValue(fields, key).(float64)
	if !ok {
		t.Fatalf("field %q is not a float: %v", key, fieldValue(fields, key))
	}
	return value
}

func fieldInt(t *testing.T, fields []any, key string) int {
	t.Helper()
	value, ok := fieldValue(fields, key).(int)
	if !ok {
		t.Fatalf("field %q is not an int: %v", key, fieldValue(fields, key))
	}
	return value
}

func fieldValue(fields []any, key string) any {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == key {
			return fields[i+1]
		}
	}
	return nil
}

func assertNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 0.1 || diff < -0.1 {
		t.Errorf("%s = %v, want ~%v", name, got, want)
	}
}
