package sandbox

import (
	"context"
	"encoding/json"
	"io"
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
	currentGitBranch = func(context.Context, string) string { return name }
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
	if !strings.Contains(got, "ready for review") {
		t.Errorf("expected ready-for-review status, got %q", got)
	}
}

// TestRunCreatePRDraftState verifies the reported status follows the created
// PR, which repository policy can force to draft regardless of the request.
func TestRunCreatePRDraftState(t *testing.T) {
	stubBranch(t, "feature/x")
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"prNumber":42,"prUrl":"https://x/pr/42","state":"draft"}`))
	})
	got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B"})
	if !strings.Contains(got, "in draft mode") {
		t.Fatalf("got %q", got)
	}
}

// TestRunCreatePRReportsBranches verifies the resolved head/base pair reaches
// the agent, so branch resolution is never silent — an omitted baseBranch falls
// back to the session's, which the agent otherwise could not see.
func TestRunCreatePRReportsBranches(t *testing.T) {
	stubBranch(t, "feature/x")
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"prNumber":42,"prUrl":"https://x/pr/42","headBranch":"feature/x","baseBranch":"main"}`))
	})
	got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B"})
	if !strings.Contains(got, "PR #42 (feature/x -> main): https://x/pr/42") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "created successfully") {
		t.Errorf("expected a creation message, got %q", got)
	}
}

// TestRunCreatePRUpdatesExisting verifies a second call from the same branch is
// reported as an update of that branch's open PR rather than as a new one.
func TestRunCreatePRUpdatesExisting(t *testing.T) {
	stubBranch(t, "feature/x")
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"prNumber":42,"prUrl":"https://x/pr/42","headBranch":"feature/x","baseBranch":"main","updated":true}`))
	})
	got := runCreatePR(context.Background(), c, map[string]any{"title": "T", "body": "B"})
	if !strings.Contains(got, "Pull request updated with your latest commits.") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "PR #42 (feature/x -> main): https://x/pr/42") {
		t.Errorf("branches missing: %q", got)
	}
	if strings.Contains(got, "created successfully") {
		t.Errorf("reused PR reported as created: %q", got)
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
	// The hint has to be actionable: a conflict now means this branch already
	// has an open PR, and another one needs another branch.
	if !strings.Contains(got, "create a new branch (`git checkout -b`)") {
		t.Errorf("conflict hint missing the new-branch instruction: %q", got)
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

func TestRunSpawnChild(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sessionId":"child-9","status":"created"}`))
	})
	got := runSpawnChild(context.Background(), c, map[string]any{"title": "t", "prompt": "p"})
	if !strings.Contains(got, "child-9") || !strings.Contains(got, "Child spawned successfully") {
		t.Fatalf("got %q", got)
	}
}

// TestRunSpawnChildReasoning verifies the optional reasoning override reaches
// the control plane as reasoningEffort, and that an unset arg is omitted rather
// than sent as "" — the child inherits the parent's effort only when the field
// is absent, so an explicit empty string must stay distinguishable.
func TestRunSpawnChildReasoning(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		wantBody string
	}{
		{"set", map[string]any{"title": "t", "prompt": "p", "reasoning": "high"}, `{"prompt":"p","reasoningEffort":"high","title":"t"}`},
		{"empty", map[string]any{"title": "t", "prompt": "p", "reasoning": ""}, `{"prompt":"p","reasoningEffort":"","title":"t"}`},
		{"absent", map[string]any{"title": "t", "prompt": "p"}, `{"prompt":"p","title":"t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = strings.TrimSpace(string(body))
				_, _ = w.Write([]byte(`{"sessionId":"child-9","status":"created"}`))
			})
			if got := runSpawnChild(context.Background(), c, tc.args); !strings.Contains(got, "child-9") {
				t.Fatalf("got %q", got)
			}
			if gotBody != tc.wantBody {
				t.Errorf("body = %s, want %s", gotBody, tc.wantBody)
			}
		})
	}
}

func TestRunSpawnChildForbidden(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"depth limit"}`))
	})
	got := runSpawnChild(context.Background(), c, map[string]any{"title": "t", "prompt": "p"})
	if !strings.Contains(got, "Cannot spawn child: depth limit") {
		t.Fatalf("got %q", got)
	}
}

// TestRunCancelChildCascades verifies nested cancellation is the default and the
// cancelled descendants are reported back to the agent.
func TestRunCancelChildCascades(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		wantBody string
		want     string
	}{
		{
			"default_cascades",
			map[string]any{"childId": "c1"},
			`{"cancelNested":true}`,
			`Child "c1" cancelled successfully. Also cancelled 2 nested child session(s). Status: CANCELLED`,
		},
		// The agent can still cancel one child without touching its own children.
		{
			"opt_out",
			map[string]any{"childId": "c1", "cancelNested": false},
			`{"cancelNested":false}`,
			`Child "c1" cancelled successfully. Status: CANCELLED`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = strings.TrimSpace(string(body))
				if tc.args["cancelNested"] == false {
					_, _ = w.Write([]byte(`{"status":"cancelled"}`))
					return
				}
				_, _ = w.Write([]byte(`{"status":"cancelled","cancelledDescendantIds":["c2","c3"]}`))
			})
			if got := runCancelChild(context.Background(), c, tc.args); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if gotBody != tc.wantBody {
				t.Errorf("request body = %q, want %q", gotBody, tc.wantBody)
			}
		})
	}
}

func TestRunCancelChildNotFound(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	got := runCancelChild(context.Background(), c, map[string]any{"childId": "x"})
	if !strings.Contains(got, `Child "x" not found`) {
		t.Fatalf("got %q", got)
	}
}

func TestRunSendChildPrompt(t *testing.T) {
	var gotPath, gotBody string
	c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		b, _ := io.ReadAll(r.Body)
		gotBody = strings.TrimSpace(string(b))
		_, _ = w.Write([]byte(`{"messageId":"m-7"}`))
	})
	got := runSendChildPrompt(context.Background(), c, map[string]any{"childId": "c 1", "prompt": "again"})

	if gotPath != "/sessions/sess-1/children/c%201/prompt" {
		t.Errorf("path = %s", gotPath)
	}
	if gotBody != `{"content":"again"}` {
		t.Errorf("body = %s", gotBody)
	}
	if !strings.Contains(got, `durably queued for child "c 1"`) || !strings.Contains(got, "Message ID: m-7") {
		t.Errorf("got %q", got)
	}
}

// TestRunSendChildPromptErrors verifies each rejection the control plane can
// return is explained in terms the agent can act on.
func TestRunSendChildPromptErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"not_found", http.StatusNotFound, `Child "c1" not found`},
		{"unresumable", http.StatusConflict, `Cannot prompt child "c1"`},
		{"queue_full", http.StatusTooManyRequests, `Cannot queue another prompt for child "c1"`},
		{"other", http.StatusInternalServerError, "Failed to prompt child"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})
			got := runSendChildPrompt(context.Background(), c, map[string]any{"childId": "c1", "prompt": "p"})
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want it to contain %q", got, tc.want)
			}
		})
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

// TestRunSlackNotifyDeliveryUnknown verifies a timed-out confirmation keeps its
// own reason. Flattened into slack_api_error it would read as "the post did not
// go through", and the agent would retry a notification Slack may have posted.
func TestRunSlackNotifyDeliveryUnknown(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":"delivery_unknown"}`))
	})
	got := runSlackNotify(context.Background(), c, map[string]any{"channel": "ops", "text": "hi"})
	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("not JSON: %q", got)
	}
	if env["reason"] != "delivery_unknown" {
		t.Fatalf("reason = %v, want delivery_unknown", env["reason"])
	}
	message, _ := env["agentMessage"].(string)
	if !strings.Contains(message, "Do not retry automatically") {
		t.Errorf("agentMessage = %q, want the do-not-retry guidance", message)
	}
	if strings.Contains(message, "did not go through") {
		t.Errorf("agentMessage = %q, want no claim that the post failed", message)
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

func TestRunGetChildStatusList(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/sess-1/children" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"children":[
			{"id":"c1","title":"first","status":"active","createdAt":1700000000000},
			{"id":"c2","title":"second","status":"completed","createdAt":1700000001000}
		]}`))
	})
	got := runGetChildStatus(context.Background(), c, map[string]any{})
	if !strings.Contains(got, "2 child session(s): 1 running, 0 pending, 1 done, 0 failed") {
		t.Fatalf("header missing: %q", got)
	}
	if !strings.Contains(got, "[RUNNING] c1") || !strings.Contains(got, "[DONE] c2") {
		t.Fatalf("rows missing: %q", got)
	}
}

func TestRunGetChildStatusDetail(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"session":{"id":"c1","title":"task","status":"completed","model":"m","repoOwner":"o","repoName":"r","branchName":"b","createdAt":1700000000000,"updatedAt":1700000005000},
			"sandbox":{"status":"stopped"},
			"artifacts":[{"type":"pr","url":"https://x/pr/1"}]
		}`))
	})
	got := runGetChildStatus(context.Background(), c, map[string]any{"childId": "c1"})
	for _, want := range []string{"Child: c1", "Status:  DONE", "Repo:    o/r", "Sandbox: stopped", "- PR: https://x/pr/1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRunGetChildStatusUnfinishedPrompt(t *testing.T) {
	c := cpClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"session":{"id":"c1","status":"active"},
			"hasUnfinishedPrompt":true,
			"finalResponse":{"success":true,"textContent":"older answer"}
		}`))
	})
	got := runGetChildStatus(context.Background(), c, map[string]any{"childId": "c1", "includeResponse": true})
	if !strings.Contains(got, "Latest completed response (newer prompt queued or running):") {
		t.Fatalf("stale-response label missing:\n%s", got)
	}
	if strings.Contains(got, "Final response:") {
		t.Fatalf("plain label should not appear:\n%s", got)
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
