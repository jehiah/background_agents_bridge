package bridge

import (
	"os"
	"os/exec"
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
	b.workspacePath = root

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
	b.workspacePath = t.TempDir()
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
	b.workspacePath = root

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
	b.workspacePath = root

	t.Setenv("REPO_NAME", "does-not-exist")
	got, ok := b.findRepoDir()
	if !ok {
		t.Fatal("expected to find repo via fallback")
	}
	if got != repo {
		t.Errorf("findRepoDir = %q, want %q", got, repo)
	}
}

// TestHandlePushNoRepository verifies the no-repository push_error now includes
// branchName (even for a valid spec), so the control plane can resolve its
// pending push instead of leaking it. Matches the upstream _send_push_error
// contract.
func TestHandlePushNoRepository(t *testing.T) {
	b := testBridge()
	b.workspacePath = t.TempDir() // no repo inside
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
	if e["branchName"] != "feature" {
		t.Errorf("push_error should carry branchName %q, got %+v", "feature", e)
	}
}

// writeManifest writes a repo manifest file and returns its path.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestResolvePushCheckoutMember verifies a spec carrying a repo identity is
// routed to the manifest member's checkout path (not a spec-derived filesystem
// path).
func TestResolvePushCheckoutMember(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "world")
	if err := os.MkdirAll(filepath.Join(member, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := testBridge()
	b.repoManifestPath = writeManifest(t, `{"repositories":[
		{"repo_owner":"group/sub","repo_name":"world","path":"`+member+`"}]}`)

	req := newPushRequest(&pushSpec{
		TargetBranch: "feat", Refspec: "HEAD:feat", RemoteURL: "https://x/y",
		RepoOwner: "GROUP/SUB", RepoName: "WORLD", // case-insensitive match
	})
	dir, err := b.resolvePushCheckout(req, true)
	if err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	if dir != member {
		t.Errorf("checkout = %q, want %q", dir, member)
	}
}

// TestResolvePushCheckoutRejections covers the validation and member-resolution
// failure modes and the reasons they surface.
func TestResolvePushCheckoutRejections(t *testing.T) {
	root := t.TempDir()
	present := filepath.Join(root, "present")
	if err := os.MkdirAll(filepath.Join(present, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := writeManifest(t, `{"repositories":[
		{"repo_owner":"o","repo_name":"present","path":"`+present+`"},
		{"repo_owner":"o","repo_name":"gone","path":"`+filepath.Join(root, "gone")+`"}]}`)

	valid := func() *pushSpec {
		return &pushSpec{TargetBranch: "feat", Refspec: "HEAD:feat", RemoteURL: "https://x/y"}
	}
	cases := []struct {
		name        string
		specPresent bool
		mutate      func(*pushSpec) *pushSpec
		wantErr     string
	}{
		{"missing_push_spec", false, func(*pushSpec) *pushSpec { return nil },
			"Push failed - missing push specification"},
		{"partial_identity", true, func(s *pushSpec) *pushSpec { s.RepoOwner = "o"; return s },
			"Push failed - pushSpec must carry both repoOwner and repoName"},
		{"missing_branch", true, func(s *pushSpec) *pushSpec { s.TargetBranch = ""; return s },
			"Push failed - missing target branch"},
		{"invalid_spec", true, func(s *pushSpec) *pushSpec { s.Refspec = ""; return s },
			"Push failed - invalid push specification"},
		{"unknown_member", true, func(s *pushSpec) *pushSpec { s.RepoOwner = "o"; s.RepoName = "nope"; return s },
			"Repository o/nope is not part of this session"},
		{"member_missing_checkout", true, func(s *pushSpec) *pushSpec { s.RepoOwner = "o"; s.RepoName = "gone"; return s },
			"Repository o/gone not found in workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := testBridge()
			b.repoManifestPath = manifest
			b.workspacePath = root
			req := newPushRequest(tc.mutate(valid()))
			_, err := b.resolvePushCheckout(req, tc.specPresent)
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPromptGitAuthor(t *testing.T) {
	user := func(name, email string) *GitUser { return &GitUser{Name: name, Email: email} }
	cases := []struct {
		name    string
		author  commandAuthor
		want    *GitUser
		wantErr bool
	}{
		{
			"attributed_user",
			commandAuthor{GitIdentity: &gitIdentity{Mode: "attributed-user", Name: " Jane ", Email: " jane@example.com "}},
			user("Jane", "jane@example.com"), false,
		},
		// Agent-only is a nil author, not the fallback identity: the caller
		// decides what an unattributed commit is committed as.
		{"agent_only", commandAuthor{GitIdentity: &gitIdentity{Mode: "agent-only"}}, nil, false},
		{"unknown_mode", commandAuthor{GitIdentity: &gitIdentity{Mode: "whatever"}}, nil, true},
		{"empty_mode", commandAuthor{GitIdentity: &gitIdentity{}}, nil, true},
		{
			"attributed_user_missing_email",
			commandAuthor{GitIdentity: &gitIdentity{Mode: "attributed-user", Name: "Jane"}},
			nil, true,
		},
		{
			"attributed_user_blank_name",
			commandAuthor{GitIdentity: &gitIdentity{Mode: "attributed-user", Name: "  ", Email: "jane@example.com"}},
			nil, true,
		},
		// A control plane that predates #1030 sends the legacy pair.
		{
			"legacy_scm_fields",
			commandAuthor{SCMName: "Jane", SCMEmail: "jane@example.com"},
			user("Jane", "jane@example.com"), false,
		},
		// The missing half comes from the agent identity, which a deployment
		// can rename with openinspect.name / openinspect.email.
		{
			"legacy_partial",
			commandAuthor{SCMName: "Jane"},
			user("Jane", "bot@acme.test"), false,
		},
		{"no_author", commandAuthor{}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := promptGitAuthor(tc.author, GitUser{Name: "Acme Bot", Email: "bot@acme.test"})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("promptGitAuthor: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("author = %+v, want nil", got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Errorf("author = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// setGlobalGitConfig sets one key in the git config under the current HOME,
// which the caller has pointed at a scratch directory.
func setGlobalGitConfig(t *testing.T, key, value string) {
	t.Helper()
	if out, err := exec.Command("git", "config", "--global", key, value).CombinedOutput(); err != nil {
		t.Fatalf("git config --global %s: %v: %s", key, err, out)
	}
}

func TestAgentGitUser(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]string
		want   GitUser
	}{
		{"unset", nil, fallbackGitUser},
		{
			"both",
			map[string]string{"openinspect.name": "Acme Bot", "openinspect.email": "bot@acme.test"},
			GitUser{Name: "Acme Bot", Email: "bot@acme.test"},
		},
		// Each half falls back on its own, so naming the bot without giving it
		// an address is not an error.
		{
			"name_only",
			map[string]string{"openinspect.name": "Acme Bot"},
			GitUser{Name: "Acme Bot", Email: fallbackGitUser.Email},
		},
		{
			"email_only",
			map[string]string{"openinspect.email": "bot@acme.test"},
			GitUser{Name: fallbackGitUser.Name, Email: "bot@acme.test"},
		},
		// git stores a blank value verbatim; treat it as unset rather than
		// committing with an empty name.
		{"blank", map[string]string{"openinspect.name": "   "}, fallbackGitUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for key, value := range tc.config {
				setGlobalGitConfig(t, key, value)
			}
			if got := testBridge().agentGitUser(t.Context()); got != tc.want {
				t.Errorf("agentGitUser() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
