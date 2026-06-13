package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

func cpClient(t *testing.T, h http.HandlerFunc) *controlplane.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := controlplane.New(srv.URL, "tok", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// stubBranch overrides currentGitBranch for the duration of a test so PR tests
// don't depend on the real checkout's branch.
func stubBranch(t *testing.T, name string) {
	t.Helper()
	orig := currentGitBranch
	currentGitBranch = func(context.Context) string { return name }
	t.Cleanup(func() { currentGitBranch = orig })
}

func TestRunCreatePRSuccess(t *testing.T) {
	stubBranch(t, "feature/x")
	c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/sess-1/pr" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"prNumber":42,"prUrl":"https://x/pr/42"}`))
	})
	got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B"})
	if !strings.Contains(got, "PR #42") || !strings.Contains(got, "https://x/pr/42") {
		t.Fatalf("got %q", got)
	}
}

func TestRunCreatePRManual(t *testing.T) {
	stubBranch(t, "feature/x")
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"manual","createPrUrl":"https://x/compare"}`))
	})
	got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B"})
	if !strings.Contains(got, "https://x/compare") || !strings.Contains(got, "Branch pushed") {
		t.Fatalf("got %q", got)
	}
}

func TestRunCreatePRConflict(t *testing.T) {
	stubBranch(t, "feature/x")
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"exists"}`))
	})
	got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B"})
	if !strings.Contains(got, "Conflict: exists") {
		t.Fatalf("got %q", got)
	}
}

// TestRunCreatePRRejectsNonFeatureBranch ensures the tool stops before calling
// the control plane when the checkout isn't on a usable feature branch.
func TestRunCreatePRRejectsNonFeatureBranch(t *testing.T) {
	cases := []struct {
		name string
		head string
		base string
	}{
		{"detached", "", ""},
		{"main", "main", ""},
		{"master", "master", ""},
		{"equals base", "develop", "develop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubBranch(t, tc.head)
			c := cpClient(t, func(http.ResponseWriter, *http.Request) {
				t.Fatal("control plane should not be called")
			})
			got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B", "baseBranch": tc.base})
			if !strings.Contains(got, "Cannot create a pull request") {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestRunSpawnTask(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sessionId":"child-9","status":"created"}`))
	})
	got := runSpawnTask(context.Background(), c, map[string]any{"title": "t", "prompt": "p"})
	if !strings.Contains(got, "child-9") || !strings.Contains(got, "Task spawned successfully") {
		t.Fatalf("got %q", got)
	}
}

func TestRunSpawnTaskForbidden(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"depth limit"}`))
	})
	got := runSpawnTask(context.Background(), c, map[string]any{"title": "t", "prompt": "p"})
	if !strings.Contains(got, "Cannot spawn task: depth limit") {
		t.Fatalf("got %q", got)
	}
}

func TestRunCancelTaskNotFound(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	got := runCancelTask(context.Background(), c, map[string]any{"taskId": "x"})
	if !strings.Contains(got, `Task "x" not found`) {
		t.Fatalf("got %q", got)
	}
}

func TestRunSlackNotifySuccess(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"channelId":"C1","messageTs":"123.45","permalink":"https://s/p"}`))
	})
	got := runSlackNotify(context.Background(), c, map[string]any{"channel": "ops", "text": "hi"})
	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("not JSON: %q", got)
	}
	if env["ok"] != true || env["channelId"] != "C1" {
		t.Fatalf("env = %v", env)
	}
}

func TestRunSlackNotifyRateLimited(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited","message":"slow","retryAfter":5}`))
	})
	got := runSlackNotify(context.Background(), c, map[string]any{"channel": "ops", "text": "hi"})
	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("not JSON: %q", got)
	}
	if env["ok"] != false || env["reason"] != "rate_limited" || env["retryAfter"].(float64) != 5 {
		t.Fatalf("env = %v", env)
	}
	if !strings.Contains(env["agentMessage"].(string), "rate-limited") {
		t.Fatalf("agentMessage = %v", env["agentMessage"])
	}
}

func TestRunSlackNotifyUnknownReasonFallsBack(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"some_new_code"}`))
	})
	got := runSlackNotify(context.Background(), c, map[string]any{"channel": "ops", "text": "hi"})
	var env map[string]any
	_ = json.Unmarshal([]byte(got), &env)
	if env["reason"] != "slack_api_error" {
		t.Fatalf("reason = %v, want slack_api_error", env["reason"])
	}
}

func TestRunGetTaskStatusList(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/sess-1/children" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"children":[
			{"id":"c1","title":"first","status":"active","createdAt":1700000000000},
			{"id":"c2","title":"second","status":"completed","createdAt":1700000001000}
		]}`))
	})
	got := runGetTaskStatus(context.Background(), c, map[string]any{})
	if !strings.Contains(got, "2 child task(s): 1 running, 0 pending, 1 done, 0 failed") {
		t.Fatalf("header missing: %q", got)
	}
	if !strings.Contains(got, "[RUNNING] c1") || !strings.Contains(got, "[DONE] c2") {
		t.Fatalf("rows missing: %q", got)
	}
}

func TestRunGetTaskStatusDetail(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"session":{"id":"c1","title":"task","status":"completed","model":"m","repoOwner":"o","repoName":"r","branchName":"b","createdAt":1700000000000,"updatedAt":1700000005000},
			"sandbox":{"status":"stopped"},
			"artifacts":[{"type":"pr","url":"https://x/pr/1"}]
		}`))
	})
	got := runGetTaskStatus(context.Background(), c, map[string]any{"taskId": "c1"})
	for _, want := range []string{"Task: c1", "Status:  DONE", "Repo:    o/r", "Sandbox: stopped", "- PR: https://x/pr/1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRunImageUpload(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "s.png")
	if err := os.WriteFile(img, []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/sess-1/media" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"artifactId":"a1","objectKey":"k"}`))
	})
	got := runImageUpload(context.Background(), c, map[string]any{"filePath": img})
	if !strings.Contains(got, `"artifactId": "a1"`) {
		t.Fatalf("got %q", got)
	}
}

func TestRunImageUploadBadType(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	c, _ := controlplane.New("http://unused", "t", "s")
	got := runImageUpload(context.Background(), c, map[string]any{"filePath": f})
	if !strings.Contains(got, "Failed to upload media") || !strings.Contains(got, "unsupported file type") {
		t.Fatalf("got %q", got)
	}
}

func TestRunToolUnknown(t *testing.T) {
	err := RunTool("nope", strings.NewReader("{}"), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err = %v", err)
	}
}
