package bridge

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
	"github.com/jehiah/background_agents_bridge/internal/sessiondiff"
)

// gitInDir runs git in dir, failing the test on error.
func gitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// recordingUploader counts the bundles the diff worker sends.
type recordingUploader struct {
	mu      sync.Mutex
	uploads int
}

func (u *recordingUploader) UploadBundle(context.Context, *sessiondiff.Bundle) (sessiondiff.Outcome, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.uploads++
	return sessiondiff.OutcomeAccepted, nil
}

func (u *recordingUploader) ReportFailure(context.Context, string) (sessiondiff.Outcome, error) {
	return sessiondiff.OutcomeAccepted, nil
}

func (u *recordingUploader) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.uploads
}

// withDiffWorker wires b to a diff worker over a real one-repository checkout
// so a refresh actually collects and uploads.
func withDiffWorker(t *testing.T, b *AgentBridge) *recordingUploader {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string { return gitInDir(t, dir, args...) }
	run("init", "--initial-branch=main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "base")
	head := run("rev-parse", "HEAD")

	manifest := filepath.Join(t.TempDir(), "manifest.json")
	body := `{"repositories":[{"repo_owner":"acme","repo_name":"web","path":"` + dir +
		`","base_sha":"` + head + `"}]}`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	uploader := &recordingUploader{}
	b.repoManifestPath = manifest
	b.diffRefresh = sessiondiff.NewWorker(uploader, manifest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.diffRefresh.Start(t.Context())
	t.Cleanup(func() { b.diffRefresh.Close(2 * time.Second) })
	return uploader
}

func waitForUploads(t *testing.T, uploader *recordingUploader, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if uploader.count() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d uploads (got %d)", want, uploader.count())
}

func TestRefreshDiffCommandTriggersUpload(t *testing.T) {
	b := testBridge()
	uploader := withDiffWorker(t, b)

	b.handleCommand(t.Context(), &command{Type: "refresh_diff"})

	waitForUploads(t, uploader, 1)
}

func TestReadyEventCarriesBaselines(t *testing.T) {
	b := testBridge()
	withDiffWorker(t, b)

	// The control plane learns what the sandbox will diff against from the
	// handshake, so the manifest's baselines have to reach the ready event.
	ready := readyEvent("ses_1", repomanifest.Load(b.repoManifestPath))
	repositories, ok := ready["repositories"].([]any)
	if !ok || len(repositories) != 1 {
		t.Fatalf("ready repositories = %#v", ready["repositories"])
	}
	entry := repositories[0].(map[string]any)
	if entry["repoOwner"] != "acme" || entry["repoName"] != "web" || entry["baseSha"] == "" {
		t.Errorf("ready repository = %+v", entry)
	}
}
