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

func TestLoadBaseSHA(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	// Either spelling is accepted; a baseline that is not a full object name is
	// dropped so the bridge resolves a trustworthy one instead.
	p := writeManifest(t, `{"repositories":[
		{"repo_owner":"o","repo_name":"snake","base_sha":"`+sha+`"},
		{"repo_owner":"o","repo_name":"camel","baseSha":"`+sha+`"},
		{"repo_owner":"o","repo_name":"abbrev","base_sha":"0123456"},
		{"repo_owner":"o","repo_name":"garbage","base_sha":"HEAD"},
		{"repo_owner":"o","repo_name":"none"}
	]}`)

	got := Load(p)
	want := []string{sha, sha, "", "", ""}
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(got))
	}
	for i, wantSHA := range want {
		if got[i].BaseSHA != wantSHA {
			t.Errorf("%s: base_sha = %q, want %q", got[i].Name, got[i].BaseSHA, wantSHA)
		}
	}
}

func TestIsObjectName(t *testing.T) {
	cases := map[string]bool{
		"0123456789abcdef0123456789abcdef01234567":                         true,
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": true,
		// Uppercase is not what git rev-parse emits, and accepting it would let
		// two spellings of one baseline through.
		"0123456789ABCDEF0123456789abcdef01234567": false,
		"0123456789abcdef0123456789abcdef0123456":  false,
		"": false,
	}
	for value, want := range cases {
		if got := IsObjectName(value); got != want {
			t.Errorf("IsObjectName(%q) = %v, want %v", value, got, want)
		}
	}
}
