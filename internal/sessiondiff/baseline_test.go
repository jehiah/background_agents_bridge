package sessiondiff

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeFile writes body into dir/name and returns the path.
func writeFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// readRecords decodes a JSON file into generic records, accepting either the
// manifest document or the bare repository list.
func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if document, ok := value.(map[string]any); ok {
		return records(document["repositories"])
	}
	return records(value)
}

func TestResolveBaselinesFillsGapsAndPersists(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "x\n")
	head := repo.commit("base")

	dir := t.TempDir()
	supplied := "0123456789abcdef0123456789abcdef01234567"
	manifest := writeFile(t, dir, "manifest.json", `{"repositories":[
		{"repo_owner":"acme","repo_name":"web","branch":"main","path":"`+repo.dir+`"},
		{"repo_owner":"acme","repo_name":"api","path":"/nope","base_sha":"`+supplied+`"}
	]}`, 0o644)
	list := writeFile(t, dir, "repositories.json", `[
		{"repo_owner":"acme","repo_name":"web","branch":"main","extra":"keep me"},
		{"repo_owner":"acme","repo_name":"api","base_sha":"`+supplied+`"}
	]`, 0o600)

	ResolveBaselines(t.Context(), manifest, list, quietLogger())

	entries := repomanifest.Load(manifest)
	if len(entries) != 2 {
		t.Fatalf("manifest lost entries: %+v", entries)
	}
	if entries[0].BaseSHA != head {
		t.Errorf("web baseline = %q, want the checkout HEAD %q", entries[0].BaseSHA, head)
	}
	// A baseline the control plane already supplied is never second-guessed.
	if entries[1].BaseSHA != supplied {
		t.Errorf("api baseline = %q, want the supplied %q", entries[1].BaseSHA, supplied)
	}
	if entries[0].Branch != "main" || entries[0].Path != repo.dir {
		t.Errorf("manifest entry lost fields: %+v", entries[0])
	}

	// The persisted list is what survives a resume, so the fallback has to land
	// there too — with the rest of the entry untouched.
	persisted := readRecords(t, list)
	if persisted[0]["base_sha"] != head {
		t.Errorf("persisted baseline = %v, want %q", persisted[0]["base_sha"], head)
	}
	if persisted[0]["extra"] != "keep me" {
		t.Errorf("unknown key dropped from the persisted list: %+v", persisted[0])
	}
	if info, err := os.Stat(list); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("persisted list mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}
}

func TestResolveBaselinesLeavesUnresolvableRepository(t *testing.T) {
	dir := t.TempDir()
	manifest := writeFile(t, dir, "manifest.json",
		`{"repositories":[{"repo_owner":"acme","repo_name":"web","path":"`+filepath.Join(dir, "absent")+`"}]}`, 0o644)
	list := writeFile(t, dir, "repositories.json", `[{"repo_owner":"acme","repo_name":"web"}]`, 0o600)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}

	// A checkout git cannot read leaves no baseline; the capture reports the
	// repository as unavailable rather than the boot failing.
	ResolveBaselines(t.Context(), manifest, list, quietLogger())

	after, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("manifest was rewritten: %s", after)
	}
	if entries := repomanifest.Load(manifest); entries[0].BaseSHA != "" {
		t.Errorf("baseline = %q, want empty", entries[0].BaseSHA)
	}
}

func TestResolveBaselinesNoManifest(t *testing.T) {
	dir := t.TempDir()
	// Nothing to do and nothing to crash on when the boot never wrote a
	// manifest (a repo-less session).
	ResolveBaselines(t.Context(), filepath.Join(dir, "absent.json"), filepath.Join(dir, "absent-list.json"), quietLogger())
}

func TestClientUploadAndFailure(t *testing.T) {
	type request struct {
		method string
		path   string
		auth   string
		body   string
	}
	var got []request
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, request{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(body)})
		w.WriteHeader(status)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/", "sess/1", "token-x")
	bundle := &Bundle{Version: Version, CapturedAt: 7, Repositories: []Repository{}}

	if outcome, err := client.UploadBundle(t.Context(), bundle); err != nil || outcome != OutcomeAccepted {
		t.Fatalf("UploadBundle = %v, %v", outcome, err)
	}
	if outcome, err := client.ReportFailure(t.Context(), "boom"); err != nil || outcome != OutcomeAccepted {
		t.Fatalf("ReportFailure = %v, %v", outcome, err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 requests, got %+v", got)
	}
	// The session id is one path segment, so a slash in it cannot widen the
	// request to another path.
	if got[0].method != http.MethodPut || got[0].path != "/sessions/sess%2F1/diff" {
		t.Errorf("upload request = %+v", got[0])
	}
	if got[0].auth != "Bearer token-x" || got[0].body != string(encodeBundle(bundle)) {
		t.Errorf("upload request = %+v", got[0])
	}
	if got[1].method != http.MethodPost || got[1].path != "/sessions/sess%2F1/diff/failure" ||
		got[1].body != `{"error":"boom"}` {
		t.Errorf("failure request = %+v", got[1])
	}

	// 404 means this control plane predates the diff viewer.
	status = http.StatusNotFound
	if outcome, err := client.UploadBundle(t.Context(), bundle); err != nil || outcome != OutcomeUnsupported {
		t.Errorf("404 upload = %v, %v", outcome, err)
	}
	status = http.StatusInternalServerError
	if _, err := client.UploadBundle(t.Context(), bundle); err == nil {
		t.Error("want an error for HTTP 500")
	}
}
