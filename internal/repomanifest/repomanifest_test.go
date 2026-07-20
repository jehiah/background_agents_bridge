package repomanifest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	// The manifest is consumed verbatim from upstream: entries are returned in
	// position order with no normalization (no skipping of blank fields, no
	// branch defaulting). Fields use the upstream repo_owner/repo_name keys.
	p := writeManifest(t, `{"repositories":[
		{"repo_owner":"octocat","repo_name":"hello","branch":"dev","path":"/workspace/hello"},
		{"repo_owner":"group/sub","repo_name":"world","path":"/workspace/world"}
	]}`)

	got := Load(p)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(got), got)
	}
	if got[0] != (Entry{Owner: "octocat", Name: "hello", Branch: "dev", Path: "/workspace/hello"}) {
		t.Errorf("entry 0 = %+v", got[0])
	}
	// Nested owner preserved; a blank branch stays blank (no normalization).
	if got[1] != (Entry{Owner: "group/sub", Name: "world", Branch: "", Path: "/workspace/world"}) {
		t.Errorf("entry 1 = %+v", got[1])
	}
}

func TestLoadMissingOrMalformed(t *testing.T) {
	if got := Load("/no/such/file.json"); got != nil {
		t.Errorf("missing file: want nil, got %+v", got)
	}
	if got := Load(writeManifest(t, "not json")); got != nil {
		t.Errorf("malformed: want nil, got %+v", got)
	}
	if got := Load(writeManifest(t, `{"other":1}`)); len(got) != 0 {
		t.Errorf("no repositories key: want empty, got %+v", got)
	}
}

func TestFindCaseInsensitiveCanonical(t *testing.T) {
	entries := []Entry{{Owner: "OctoCat", Name: "Hello", Branch: "main", Path: "/workspace/Hello"}}

	got, ok := Find(entries, "octocat", "hello")
	if !ok {
		t.Fatal("expected case-insensitive match")
	}
	// Returned entry carries canonical casing, not the lookup args.
	if got.Owner != "OctoCat" || got.Name != "Hello" {
		t.Errorf("want canonical casing, got %+v", got)
	}
	if _, ok := Find(entries, "octocat", "other"); ok {
		t.Error("unexpected match for wrong name")
	}
}
