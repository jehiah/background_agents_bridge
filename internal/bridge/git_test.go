package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRedactGitStderr(t *testing.T) {
	cases := []struct {
		name              string
		stderr            string
		pushURL, redacted string
		want              string
	}{
		{
			"replaces_push_url",
			"fatal: unable to access https://user:tok@github.com/o/r.git/",
			"https://user:tok@github.com/o/r.git", "https://github.com/o/r.git",
			"fatal: unable to access https://github.com/o/r.git/",
		},
		{
			"regex_strips_credentials",
			"error pushing to https://abc:def@host.example/y",
			"", "",
			"error pushing to https://***@host.example/y",
		},
		{
			"nothing_to_redact",
			"fatal: repository not found",
			"", "",
			"fatal: repository not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactGitStderr(tc.stderr, tc.pushURL, tc.redacted); got != tc.want {
				t.Errorf("redactGitStderr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindRepoDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "my-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := testBridge()
	b.repoPath = root

	got, ok := b.findRepoDir()
	if !ok {
		t.Fatal("expected to find repo")
	}
	if got != repo {
		t.Errorf("findRepoDir = %q, want %q", got, repo)
	}
}

func TestFindRepoDirNone(t *testing.T) {
	b := testBridge()
	b.repoPath = t.TempDir()
	if _, ok := b.findRepoDir(); ok {
		t.Error("expected no repo in empty dir")
	}
}

// TestFindRepoDirPrefersRepoName verifies that when REPO_NAME is set and that
// checkout exists, findRepoDir returns it rather than the first "*/.git" entry,
// keeping handlePush pinned to the same tree the sandbox tools resolve.
func TestFindRepoDirPrefersRepoName(t *testing.T) {
	root := t.TempDir()
	// "a-other" sorts before "wanted", so plain autodiscovery would pick it.
	for _, name := range []string{"a-other", "wanted"} {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := testBridge()
	b.repoPath = root

	t.Setenv("REPO_NAME", "wanted")
	got, ok := b.findRepoDir()
	if !ok {
		t.Fatal("expected to find repo")
	}
	if want := filepath.Join(root, "wanted"); got != want {
		t.Errorf("findRepoDir = %q, want %q", got, want)
	}
}

// TestFindRepoDirRepoNameMissing verifies that a set-but-absent REPO_NAME falls
// back to autodiscovery rather than failing.
func TestFindRepoDirRepoNameMissing(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "present")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := testBridge()
	b.repoPath = root

	t.Setenv("REPO_NAME", "does-not-exist")
	got, ok := b.findRepoDir()
	if !ok {
		t.Fatal("expected to find repo via fallback")
	}
	if got != repo {
		t.Errorf("findRepoDir = %q, want %q", got, repo)
	}
}

// TestHandlePushNoRepository verifies the no-repository push_error omits
// branchName, matching the Python contract.
func TestHandlePushNoRepository(t *testing.T) {
	b := testBridge()
	b.repoPath = t.TempDir() // no repo inside
	b.rootCtx = t.Context()

	b.handlePush(t.Context(), &command{
		Type:     "push",
		PushSpec: &pushSpec{TargetBranch: "feature", Refspec: "HEAD:feature", RemoteURL: "https://x/y"},
	})

	if len(b.eventBuffer) != 1 {
		t.Fatalf("expected 1 buffered push_error, got %d", len(b.eventBuffer))
	}
	e := b.eventBuffer[0]
	if e["type"] != "push_error" || e["error"] != "No repository found" {
		t.Errorf("unexpected event: %+v", e)
	}
	if _, hasBranch := e["branchName"]; hasBranch {
		t.Errorf("no-repository push_error should omit branchName: %+v", e)
	}
}
