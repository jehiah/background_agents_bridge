package sessiondiff

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// Collector collects every session repository into one bounded bundle; tests
// substitute it. Mirrors the BundleCollector protocol.
type Collector func(ctx context.Context, repositories []repomanifest.Entry, triggerMessageID *string, capturedAt int64, limits Limits) (*Bundle, error)

// Worker coalesces terminal prompt executions into one eventual refresh of the
// idle checkout. Requests are cheap and never block the caller: the actual git
// work happens on the worker goroutine, only while no prompt is in flight, and
// a refresh whose inputs went stale is discarded rather than uploaded.
//
// Port of SessionDiffRefreshWorker. Where upstream respawns an asyncio task per
// request, this runs one long-lived goroutine woken by a channel — the
// generation bookkeeping (and the "requested during exit" race it guards
// against) is the same, minus the respawn.
type Worker struct {
	client         Uploader
	manifestPath   string
	log            *slog.Logger
	collect        Collector
	limits         Limits
	refreshTimeout time.Duration
	// now supplies the capture timestamp; tests replace it.
	now func() time.Time

	mu                  sync.Mutex
	requestedGeneration int
	settledGeneration   int
	activityGeneration  int
	triggerMessageID    *string
	activePromptCount   int
	// idle is closed while no prompt is in flight and replaced with a fresh
	// open channel when one starts, so waiters can select on it with a context.
	idle        chan struct{}
	unsupported bool
	closed      bool

	// workCancel aborts in-flight git and control-plane work. It is deliberately
	// not tied to the bridge's context: a refresh collected just before shutdown
	// must still be able to finish uploading, bounded by Close's timeout.
	workCancel context.CancelFunc

	wake chan struct{}
	done chan struct{}
}

// NewWorker builds a Worker uploading through client and reading the session
// repository list (with its baselines) from manifestPath.
func NewWorker(client Uploader, manifestPath string, log *slog.Logger) *Worker {
	idle := make(chan struct{})
	close(idle)
	return &Worker{
		client:         client,
		manifestPath:   manifestPath,
		log:            log,
		collect:        CollectBundle,
		limits:         DefaultLimits(),
		refreshTimeout: DefaultRefreshTimeout,
		now:            time.Now,
		idle:           idle,
		wake:           make(chan struct{}, 1),
		done:           make(chan struct{}),
	}
}

// PromptStarted invalidates overlapping collections and holds refreshes until
// the checkout is idle again.
func (w *Worker) PromptStarted() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.activityGeneration++
	w.activePromptCount++
	if w.activePromptCount == 1 {
		w.idle = make(chan struct{})
	}
}

// PromptFinished releases the idle gate once every started prompt has
// terminated.
func (w *Worker) PromptFinished() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.activePromptCount == 0 {
		return
	}
	w.activePromptCount--
	if w.activePromptCount == 0 {
		close(w.idle)
	}
}

// Request asks for a refresh without waiting for git or the control plane. A
// nil triggerMessageID marks a control-plane-initiated refresh.
func (w *Worker) Request(triggerMessageID *string) {
	w.mu.Lock()
	if w.unsupported || w.closed {
		w.mu.Unlock()
		return
	}
	w.requestedGeneration++
	w.triggerMessageID = triggerMessageID
	w.mu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default:
		// A wake-up is already pending; the loop re-reads the generation.
	}
}

// Start runs the worker until ctx is cancelled. It returns immediately.
func (w *Worker) Start(ctx context.Context) {
	workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.mu.Lock()
	w.workCancel = cancel
	w.mu.Unlock()
	go w.run(ctx, workCtx)
}

// Close stops accepting refreshes and gives in-flight work up to timeout to
// finish, so a bundle collected just before shutdown still reaches the viewer.
func (w *Worker) Close(timeout time.Duration) {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()

	// Wake the loop so it observes the closed flag and exits once the current
	// settle finishes.
	select {
	case w.wake <- struct{}{}:
	default:
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
		// Out of time: abandon whatever git or upload is still running.
	}
	w.mu.Lock()
	cancel := w.workCancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run consumes refresh requests until ctx (the bridge's lifetime) ends. Work
// already under way runs on workCtx, so it survives that cancellation until
// Close gives up on it.
func (w *Worker) run(ctx, workCtx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-workCtx.Done():
			return
		case <-w.wake:
		}
		for w.pending() {
			if !w.waitIdle(ctx) {
				return
			}
			if !w.settle(workCtx, w.snapshot()) {
				return
			}
		}
	}
}

// pending reports whether a requested refresh has not settled yet.
func (w *Worker) pending() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.unsupported && !w.closed && w.settledGeneration < w.requestedGeneration
}

// waitIdle blocks until no prompt is in flight, reporting false if ctx ended.
func (w *Worker) waitIdle(ctx context.Context) bool {
	for {
		w.mu.Lock()
		idle := w.idle
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-idle:
		}
		// Re-read: a prompt may have started between the unlock and the receive.
		w.mu.Lock()
		settled := w.activePromptCount == 0
		w.mu.Unlock()
		if settled {
			return true
		}
	}
}

// attempt is one coalesced refresh request, snapshotted before collection
// starts so staleness can be judged against what has happened since.
type attempt struct {
	generation         int
	activityGeneration int
	triggerMessageID   *string
}

func (w *Worker) snapshot() attempt {
	w.mu.Lock()
	defer w.mu.Unlock()
	return attempt{
		generation:         w.requestedGeneration,
		activityGeneration: w.activityGeneration,
		triggerMessageID:   w.triggerMessageID,
	}
}

// settle collects and uploads one bundle, marking a settled generation unless
// the attempt went stale. Leaving it unsettled keeps the loop condition true,
// so the next iteration retries against the checkout's current state. It
// returns false when ctx ended and the worker should stop.
func (w *Worker) settle(ctx context.Context, a attempt) bool {
	repositories := repomanifest.Load(w.manifestPath)
	if len(repositories) == 0 {
		w.markSettled(a)
		return true
	}

	bundle, err := w.collectBundle(ctx, a, repositories)
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		if !w.isStale(a) {
			w.reportFailure(ctx, err)
			w.markSettled(a)
		}
		return ctx.Err() == nil
	}

	if w.isStale(a) {
		w.log.Info("session_diff.refresh_discarded", "generation", a.generation)
		return true
	}

	outcome, err := w.client.UploadBundle(ctx, bundle)
	switch {
	case ctx.Err() != nil:
		return false
	case err != nil:
		w.reportFailure(ctx, err)
	case outcome == OutcomeUnsupported:
		w.markUnsupported()
	default:
		w.log.Info("session_diff.collection_completed",
			"repository_count", len(bundle.Repositories),
			"encoded_bytes", len(encodeBundle(bundle)))
	}
	w.markSettled(a)
	return ctx.Err() == nil
}

// collectBundle runs one bounded collection. A bundle in which no repository
// could be captured is a failure, not an empty diff.
func (w *Worker) collectBundle(ctx context.Context, a attempt, repositories []repomanifest.Entry) (*Bundle, error) {
	ctx, cancel := context.WithTimeout(ctx, w.refreshTimeout)
	defer cancel()

	bundle, err := w.collect(ctx, repositories, a.triggerMessageID, w.now().UnixMilli(), w.limits)
	if err != nil {
		return nil, err
	}
	for _, repository := range bundle.Repositories {
		if repository.Status == "ready" {
			return bundle, nil
		}
	}
	return nil, errorf("All repositories failed to collect changes")
}

// isStale reports whether anything has happened since the attempt was
// snapshotted that makes its capture untrustworthy.
func (w *Worker) isStale(a attempt) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.activePromptCount > 0 ||
		a.generation != w.requestedGeneration ||
		a.activityGeneration != w.activityGeneration
}

func (w *Worker) markSettled(a attempt) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settledGeneration = max(w.settledGeneration, a.generation)
}

// reportFailure tells the control plane the refresh failed, so the viewer
// shows a failure instead of a silently stale diff.
func (w *Worker) reportFailure(ctx context.Context, err error) {
	message := orFallback(truncate(captureMessage(err), maxErrorLength), "Session diff refresh failed")
	w.log.Warn("session_diff.refresh_failed", "error", message)

	w.mu.Lock()
	unsupported := w.unsupported
	w.mu.Unlock()
	if unsupported {
		return
	}
	outcome, reportErr := w.client.ReportFailure(ctx, message)
	if reportErr != nil {
		w.log.Warn("session_diff.failure_report_failed", "error", truncate(reportErr.Error(), maxErrorLength))
		return
	}
	if outcome == OutcomeUnsupported {
		w.markUnsupported()
	}
}

func (w *Worker) markUnsupported() {
	w.mu.Lock()
	w.unsupported = true
	w.mu.Unlock()
	w.log.Info("session_diff.unsupported")
}
