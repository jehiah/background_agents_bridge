package sessiondiff

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// fakeUploader records what the worker sent and can be told how to answer.
type fakeUploader struct {
	mu        sync.Mutex
	bundles   []*Bundle
	failures  []string
	outcome   Outcome
	uploadErr error
}

func (f *fakeUploader) UploadBundle(_ context.Context, bundle *Bundle) (Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bundles = append(f.bundles, bundle)
	return f.outcome, f.uploadErr
}

func (f *fakeUploader) ReportFailure(_ context.Context, message string) (Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, message)
	return f.outcome, nil
}

func (f *fakeUploader) uploaded() []*Bundle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Bundle(nil), f.bundles...)
}

func (f *fakeUploader) reported() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.failures...)
}

// newTestWorker builds a started worker over a one-entry manifest with a
// substituted collector.
func newTestWorker(t *testing.T, client Uploader, collect Collector) *Worker {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	body := `{"repositories":[{"repo_owner":"acme","repo_name":"web","path":"/workspace/web",` +
		`"base_sha":"0123456789abcdef0123456789abcdef01234567"}]}`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.collect = collect
	w.Start(t.Context())
	t.Cleanup(func() { w.Close(2 * time.Second) })
	return w
}

// readyBundle is a collector that always succeeds.
func readyBundle(_ context.Context, repositories []repomanifest.Entry, triggerMessageID *string, capturedAt int64, _ Limits) (*Bundle, error) {
	bundle := &Bundle{Version: Version, TriggerMessageID: triggerMessageID, CapturedAt: capturedAt}
	for position, repository := range repositories {
		bundle.Repositories = append(bundle.Repositories, Repository{
			Status: "ready", Position: position,
			RepoOwner: repository.Owner, RepoName: repository.Name, BaseSHA: repository.BaseSHA,
			Files: []File{},
		})
	}
	return bundle, nil
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWorkerUploadsRequestedRefresh(t *testing.T) {
	client := &fakeUploader{}
	w := newTestWorker(t, client, readyBundle)

	messageID := "msg-1"
	w.Request(&messageID)

	waitFor(t, "the upload", func() bool { return len(client.uploaded()) == 1 })
	bundle := client.uploaded()[0]
	if bundle.TriggerMessageID == nil || *bundle.TriggerMessageID != "msg-1" {
		t.Errorf("triggerMessageId = %v", bundle.TriggerMessageID)
	}
	if len(bundle.Repositories) != 1 || bundle.Repositories[0].RepoName != "web" {
		t.Errorf("bundle repositories = %+v", bundle.Repositories)
	}
}

func TestWorkerWaitsForPromptToFinish(t *testing.T) {
	client := &fakeUploader{}
	w := newTestWorker(t, client, readyBundle)

	// A refresh requested while a prompt is running must not capture the
	// half-written checkout.
	w.PromptStarted()
	w.Request(nil)
	time.Sleep(20 * time.Millisecond)
	if got := len(client.uploaded()); got != 0 {
		t.Fatalf("uploaded %d bundles while a prompt was in flight", got)
	}

	w.PromptFinished()
	waitFor(t, "the upload after the prompt finished", func() bool { return len(client.uploaded()) == 1 })
}

func TestWorkerCoalescesBurst(t *testing.T) {
	client := &fakeUploader{}
	var mu sync.Mutex
	collections := 0
	first := make(chan struct{})
	release := make(chan struct{})
	w := newTestWorker(t, client, func(ctx context.Context, r []repomanifest.Entry, id *string, at int64, l Limits) (*Bundle, error) {
		mu.Lock()
		nth := collections
		collections++
		mu.Unlock()
		if nth == 0 {
			close(first)
			<-release
		}
		return readyBundle(ctx, r, id, at, l)
	})

	w.Request(nil)
	<-first
	// Three more requests arrive while the first collection is still running;
	// they collapse into a single follow-up rather than three.
	for range 3 {
		w.Request(nil)
	}
	close(release)

	// The in-flight capture is already stale by the time it finishes, so it is
	// discarded and re-collected once — the burst costs one extra collection
	// and produces exactly one upload, of the freshest state.
	waitFor(t, "the coalesced upload", func() bool { return len(client.uploaded()) == 1 })
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if collections != 2 {
		t.Errorf("collected %d times, want 2", collections)
	}
	if got := len(client.uploaded()); got != 1 {
		t.Errorf("uploaded %d bundles, want 1", got)
	}
}

func TestWorkerDiscardsStaleCapture(t *testing.T) {
	client := &fakeUploader{}
	var w *Worker
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	w = newTestWorker(t, client, func(ctx context.Context, r []repomanifest.Entry, id *string, at int64, l Limits) (*Bundle, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return readyBundle(ctx, r, id, at, l)
	})

	w.Request(nil)
	<-started
	// A prompt begins mid-collection: whatever was captured describes a
	// checkout that is already being rewritten, so it must not be uploaded.
	w.PromptStarted()
	close(release)

	time.Sleep(50 * time.Millisecond)
	if got := len(client.uploaded()); got != 0 {
		t.Fatalf("uploaded a stale capture (%d bundles)", got)
	}
	// The request is still outstanding, so it retries once the prompt ends.
	w.PromptFinished()
	waitFor(t, "the retry", func() bool { return len(client.uploaded()) == 1 })
}

func TestWorkerStopsAfterUnsupported(t *testing.T) {
	client := &fakeUploader{outcome: OutcomeUnsupported}
	w := newTestWorker(t, client, readyBundle)

	w.Request(nil)
	waitFor(t, "the first upload", func() bool { return len(client.uploaded()) == 1 })

	// A control plane that predates the diff viewer answers 404; the worker
	// goes quiet for the rest of the sandbox's life.
	for range 3 {
		w.Request(nil)
	}
	time.Sleep(30 * time.Millisecond)
	if got := len(client.uploaded()); got != 1 {
		t.Errorf("kept uploading after 404: %d bundles", got)
	}
}

func TestWorkerReportsCollectionFailure(t *testing.T) {
	client := &fakeUploader{}
	w := newTestWorker(t, client, func(context.Context, []repomanifest.Entry, *string, int64, Limits) (*Bundle, error) {
		return nil, errorf("Repository checkout is missing: /workspace/web")
	})

	w.Request(nil)

	waitFor(t, "the failure report", func() bool { return len(client.reported()) == 1 })
	if got := client.reported()[0]; got != "Repository checkout is missing: /workspace/web" {
		t.Errorf("reported %q", got)
	}
	if len(client.uploaded()) != 0 {
		t.Error("uploaded a bundle despite the failure")
	}
}

func TestWorkerReportsAllRepositoriesUnavailable(t *testing.T) {
	client := &fakeUploader{}
	w := newTestWorker(t, client, func(_ context.Context, r []repomanifest.Entry, id *string, at int64, _ Limits) (*Bundle, error) {
		return &Bundle{
			Version: Version, TriggerMessageID: id, CapturedAt: at,
			Repositories: []Repository{{Status: "unavailable", Error: "boom", Files: []File{}}},
		}, nil
	})

	// A bundle in which nothing could be captured is a failure, not an empty
	// diff — uploading it would erase the viewer's last good state.
	w.Request(nil)

	waitFor(t, "the failure report", func() bool { return len(client.reported()) == 1 })
	if len(client.uploaded()) != 0 {
		t.Error("uploaded an all-unavailable bundle")
	}
}

func TestWorkerIgnoresRequestsAfterClose(t *testing.T) {
	client := &fakeUploader{}
	w := newTestWorker(t, client, readyBundle)

	w.Close(time.Second)
	w.Request(nil)
	time.Sleep(20 * time.Millisecond)
	if got := len(client.uploaded()); got != 0 {
		t.Errorf("uploaded %d bundles after Close", got)
	}
}

// Close waits only for work that is actually in flight: an idle worker stops as
// soon as it observes the closed flag, rather than sitting out the whole
// shutdown budget.
func TestWorkerCloseReturnsWhenIdle(t *testing.T) {
	w := newTestWorker(t, &fakeUploader{}, readyBundle)

	start := time.Now()
	w.Close(10 * time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Close took %s while idle, want a prompt return", elapsed)
	}
}

// A collection already under way when Close arrives still gets to upload,
// which is the reason Close has a timeout at all.
func TestWorkerCloseWaitsForInFlightRefresh(t *testing.T) {
	client := &fakeUploader{}
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	w := newTestWorker(t, client, func(ctx context.Context, repositories []repomanifest.Entry,
		triggerMessageID *string, capturedAt int64, limits Limits) (*Bundle, error) {
		once.Do(func() { close(started) })
		<-release
		return readyBundle(ctx, repositories, triggerMessageID, capturedAt, limits)
	})

	w.Request(nil)
	<-started
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		w.Close(10 * time.Second)
	}()
	close(release)
	<-closed

	if got := len(client.uploaded()); got != 1 {
		t.Errorf("uploaded %d bundles, want the in-flight one to land", got)
	}
}

func TestWorkerSkipsEmptyManifest(t *testing.T) {
	client := &fakeUploader{}
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	w := NewWorker(client, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.collect = readyBundle
	w.Start(t.Context())
	defer w.Close(time.Second)

	// No manifest means no session repositories to describe; the request
	// settles silently instead of reporting a failure.
	w.Request(nil)
	time.Sleep(30 * time.Millisecond)
	if len(client.uploaded()) != 0 || len(client.reported()) != 0 {
		t.Errorf("uploads=%d failures=%d, want none", len(client.uploaded()), len(client.reported()))
	}
}

func TestEncodeBundleWireShape(t *testing.T) {
	messageID := "msg-1"
	truncated, omitted, additions, deletions := true, 2, 3, 1
	bundle := &Bundle{
		Version: Version, TriggerMessageID: &messageID, CapturedAt: 1700000000000,
		Repositories: []Repository{{
			Status: "ready", Position: 0, RepoOwner: "acme", RepoName: "web",
			BaseSHA: "abc", HeadSHA: "def", Truncated: &truncated, OmittedFileCount: &omitted,
			Files: []File{{
				ID: "id-1", Path: "a.txt", Status: statusModified,
				Additions: &additions, Deletions: &deletions,
				RenderState: renderRenderable, Patch: "@@\n+x\n",
			}},
		}},
	}
	var decoded map[string]any
	if err := json.Unmarshal(encodeBundle(bundle), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	repository := decoded["repositories"].([]any)[0].(map[string]any)
	file := repository["files"].([]any)[0].(map[string]any)
	for key, want := range map[string]any{"baseSha": "abc", "headSha": "def", "omittedFileCount": float64(2)} {
		if repository[key] != want {
			t.Errorf("repository[%q] = %v, want %v", key, repository[key], want)
		}
	}
	for key, want := range map[string]any{"renderState": "renderable", "additions": float64(3), "oldPath": nil} {
		if file[key] != want {
			t.Errorf("file[%q] = %v, want %v", key, file[key], want)
		}
	}
	// A binary file's counts must serialize as null, not as 0.
	bundle.Repositories[0].Files[0].Additions = nil
	bundle.Repositories[0].Files[0].Deletions = nil
	if err := json.Unmarshal(encodeBundle(bundle), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	file = decoded["repositories"].([]any)[0].(map[string]any)["files"].([]any)[0].(map[string]any)
	if value, ok := file["additions"]; !ok || value != nil {
		t.Errorf("additions = %v (present=%v), want null", value, ok)
	}
}
