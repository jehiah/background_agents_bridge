package controlplane

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestClient starts a stub server with h and returns a client pointed at it.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok", "sess-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	if _, err := New("", "t", "s"); err == nil {
		t.Error("expected error for empty URL")
	}
	if _, err := New("u", "", "s"); err == nil {
		t.Error("expected error for empty token")
	}
	if _, err := New("u", "t", ""); err == nil {
		t.Error("expected error for empty session")
	}
}

func TestSCMCredentials(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions/sess-1/scm-credentials" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("host") != "github.com" {
			t.Errorf("host = %s", r.URL.Query().Get("host"))
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"username":"u","password":"p"}`))
	})
	creds, err := c.SCMCredentials(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Username != "u" || creds.Password != "p" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestSCMCredentialsTokenFallback(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"ghs"}`))
	})
	creds, err := c.SCMCredentials(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Username != "x-access-token" || creds.Password != "ghs" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestCreatePR(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"title":"T"`) || !strings.Contains(string(body), `"headBranch":"feat"`) {
			t.Errorf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{"prNumber":7,"prUrl":"https://x/pr/7","state":"open"}`))
	})
	res, err := c.CreatePR(context.Background(), CreatePRRequest{Title: "T", Body: "B", HeadBranch: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PRNumber != 7 || res.PRURL != "https://x/pr/7" || res.Manual() {
		t.Fatalf("res = %+v", res)
	}
}

func TestCreatePRConflict(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"branch already has a PR"}`))
	})
	_, err := c.CreatePR(context.Background(), CreatePRRequest{Title: "T", Body: "B"})
	e, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err = %T, want *APIError", err)
	}
	if e.StatusCode != http.StatusConflict || e.Display() != "branch already has a PR" {
		t.Fatalf("apiErr = %+v", e)
	}
}

func TestListChildren(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sessions/sess-1/children" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"children":[{"id":"c1","title":"t","status":"active","createdAt":1700000000000}]}`))
	})
	children, err := c.ListChildren(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != "c1" || children[0].Status != "active" {
		t.Fatalf("children = %+v", children)
	}
}

func TestGetChildQuery(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("include") != "result,trajectory" {
			t.Errorf("include = %s", q.Get("include"))
		}
		if q.Get("trajectoryLimit") != "50" {
			t.Errorf("trajectoryLimit = %s", q.Get("trajectoryLimit"))
		}
		_, _ = w.Write([]byte(`{"session":{"id":"c1"}}`))
	})
	_, err := c.GetChild(context.Background(), "c1", ChildDetailOptions{
		IncludeResponse: true, IncludeTrajectory: true, TrajectoryLimit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSlackNotifyError(t *testing.T) {
	ra := 12.0
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited","message":"slow down","retryAfter":12}`))
	})
	_, err := c.SlackNotify(context.Background(), SlackNotifyRequest{Channel: "ops", Text: "hi"})
	e, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err = %T", err)
	}
	if e.Code != "rate_limited" || e.Message != "slow down" || e.RetryAfter == nil || *e.RetryAfter != ra {
		t.Fatalf("apiErr = %+v", e)
	}
}

func TestUploadMediaMultipart(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "multipart/form-data" {
			t.Fatalf("content-type = %s", r.Header.Get("Content-Type"))
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		form, err := mr.ReadForm(1 << 20)
		if err != nil {
			t.Fatal(err)
		}
		if got := form.Value["artifactType"]; len(got) != 1 || got[0] != "screenshot" {
			t.Errorf("artifactType = %v", got)
		}
		if got := form.Value["caption"]; len(got) != 1 || got[0] != "hello" {
			t.Errorf("caption = %v", got)
		}
		if len(form.File["file"]) != 1 || form.File["file"][0].Filename != "shot.png" {
			t.Errorf("file part missing")
		}
		_, _ = w.Write([]byte(`{"artifactId":"a1","objectKey":"k1"}`))
	})

	res, err := c.UploadMedia(context.Background(), UploadMediaRequest{FilePath: img, Caption: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ArtifactID != "a1" || res.ObjectKey != "k1" {
		t.Fatalf("res = %+v", res)
	}
}

func TestUploadMediaVideoRequiresFields(t *testing.T) {
	dir := t.TempDir()
	vid := filepath.Join(dir, "rec.mp4")
	if err := os.WriteFile(vid, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := New("http://unused", "t", "s")
	_, err := c.UploadMedia(context.Background(), UploadMediaRequest{FilePath: vid, ArtifactType: "video"})
	if err == nil || !strings.Contains(err.Error(), "required for video") {
		t.Fatalf("err = %v, want video field requirement", err)
	}
}

func TestUploadMediaRejectsMP4AsScreenshot(t *testing.T) {
	dir := t.TempDir()
	vid := filepath.Join(dir, "rec.mp4")
	if err := os.WriteFile(vid, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := New("http://unused", "t", "s")
	_, err := c.UploadMedia(context.Background(), UploadMediaRequest{FilePath: vid})
	if err == nil || !strings.Contains(err.Error(), "artifactType=video") {
		t.Fatalf("err = %v", err)
	}
}
