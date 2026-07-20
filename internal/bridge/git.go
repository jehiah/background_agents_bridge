package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// credentialURLRE matches a userinfo component in an http(s) URL so credentials
// can be stripped from git output.
var credentialURLRE = regexp.MustCompile(`(https?://)([^/\s@]+)@`)

// findRepoDir locates the checked-out repository under workspacePath. It prefers
// workspacePath/$REPO_NAME when REPO_NAME is set and that checkout exists, then falls
// back to the first "*/.git" entry (mirroring the Python glob). Preferring
// REPO_NAME keeps handlePush pinned to the same tree opencode edits when more
// than one checkout is present, matching resolveRepoDir in internal/sandbox.
func (b *AgentBridge) findRepoDir() (string, bool) {
	if repo := os.Getenv("REPO_NAME"); repo != "" {
		cand := filepath.Join(b.workspacePath, repo)
		if _, err := os.Stat(filepath.Join(cand, ".git")); err == nil {
			return cand, true
		}
	}
	entries, err := os.ReadDir(b.workspacePath)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.workspacePath, e.Name(), ".git")); err == nil {
			return filepath.Join(b.workspacePath, e.Name()), true
		}
	}
	return "", false
}

// configureGitIdentity sets git user.name/user.email for commit attribution in
// every member checkout under the workspace (git config --local per checkout,
// matching the multi-repo upstream). Failures are logged but not fatal.
func (b *AgentBridge) configureGitIdentity(ctx context.Context, user GitUser) {
	b.log.Debug("git.identity_configure", "git_name", user.Name, "git_email", user.Email)

	repoDirs := b.memberCheckouts()
	if len(repoDirs) == 0 {
		b.log.Debug("git.identity_skip", "reason", "no_repo_configured")
		return
	}

	run := func(dir string, args ...string) error {
		cctx, cancel := context.WithTimeout(ctx, gitConfigTimeout)
		defer cancel()
		c := exec.CommandContext(cctx, "git", append([]string{"config", "--local"}, args...)...)
		c.Dir = dir
		var stderr bytes.Buffer
		c.Stderr = &stderr
		return c.Run()
	}

	for _, dir := range repoDirs {
		if err := run(dir, "user.name", user.Name); err != nil {
			b.log.Error("git.identity_error", "exc", err)
			return
		}
		if err := run(dir, "user.email", user.Email); err != nil {
			b.log.Error("git.identity_error", "exc", err)
			return
		}
	}
}

// memberCheckouts lists every checkout directory (parent of a "*/.git" entry)
// under the workspace, sorted for determinism. Mirrors the Python
// repo_path.glob("*/.git") enumeration used for per-repo identity config.
func (b *AgentBridge) memberCheckouts() []string {
	entries, err := os.ReadDir(b.workspacePath)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.workspacePath, e.Name(), ".git")); err == nil {
			dirs = append(dirs, filepath.Join(b.workspacePath, e.Name()))
		}
	}
	return dirs
}

// pushRequest is a provider-generated push spec normalized for execution.
// Absent string fields normalize to ""; validatePushRequest decides which are
// fatal. Mirrors PushRequest in the upstream bridge.
type pushRequest struct {
	branchName      string
	repoOwner       string
	repoName        string
	refspec         string
	pushURL         string
	redactedPushURL string
	force           bool
}

func newPushRequest(spec *pushSpec) pushRequest {
	if spec == nil {
		return pushRequest{}
	}
	return pushRequest{
		branchName:      strings.TrimSpace(spec.TargetBranch),
		repoOwner:       strings.TrimSpace(spec.RepoOwner),
		repoName:        strings.TrimSpace(spec.RepoName),
		refspec:         strings.TrimSpace(spec.Refspec),
		pushURL:         strings.TrimSpace(spec.RemoteURL),
		redactedPushURL: strings.TrimSpace(spec.RedactedRemoteURL),
		force:           spec.Force,
	}
}

// hasRepoIdentity reports whether the spec names its target repository. Owner
// and name always travel together — validatePushRequest rejects partial
// identity before anything consults this.
func (r pushRequest) hasRepoIdentity() bool { return r.repoOwner != "" && r.repoName != "" }

func (r pushRequest) repoFullName() string { return r.repoOwner + "/" + r.repoName }

// repoFields is the repo identity echoed on push events when the spec carried it.
func (r pushRequest) repoFields() map[string]any {
	f := map[string]any{}
	if r.repoOwner != "" {
		f["repoOwner"] = r.repoOwner
	}
	if r.repoName != "" {
		f["repoName"] = r.repoName
	}
	return f
}

// pushRejected is a push that cannot proceed; its message is user-facing. Raise
// sites log their own specific event first — this only carries the message to
// the single push_error emitter in handlePush. Mirrors PushRejected.
type pushRejected struct{ msg string }

func (e *pushRejected) Error() string { return e.msg }

// rejectPush logs a push rejection and returns it toward handlePush's emitter.
func (b *AgentBridge) rejectPush(reason, message string, logFields ...any) *pushRejected {
	b.log.Warn("git.push_error", append([]any{"reason", reason}, logFields...)...)
	return &pushRejected{msg: message}
}

// handlePush pushes using a provider-generated push spec and reports the result.
// Pipeline: parse → validate → resolve checkout → run git push. Every failure
// lands in the single push_error emitter below.
func (b *AgentBridge) handlePush(ctx context.Context, cmd *command) {
	req := newPushRequest(cmd.PushSpec)

	b.log.Info("git.push_start",
		"branch_name", req.branchName, "repo_owner", req.repoOwner, "repo_name", req.repoName,
		"mode", "push_spec")

	repoDir, err := b.resolvePushCheckout(req, cmd.PushSpec != nil)
	if err == nil {
		err = b.runGitPush(ctx, req, repoDir)
	}
	if err != nil {
		if _, ok := errors.AsType[*pushRejected](err); !ok {
			// Unexpected error: the reject helpers already logged their own event.
			b.log.Error("git.push_error", "exc", err, "branch_name", req.branchName)
		}
		b.sendEvent(pushErrorEvent(err.Error(), req.branchName, req.repoFields()))
		return
	}

	b.log.Info("git.push_complete",
		"branch_name", req.branchName, "repo_owner", req.repoOwner, "repo_name", req.repoName)
	b.sendEvent(pushCompleteEvent(req.branchName, req.repoFields()))
}

// resolvePushCheckout validates the spec then picks the checkout the push runs
// in: the manifest member when the spec names a repo, else the sole workspace
// clone.
func (b *AgentBridge) resolvePushCheckout(req pushRequest, specPresent bool) (string, error) {
	if rej := b.validatePushRequest(req, specPresent); rej != nil {
		return "", rej
	}
	if req.hasRepoIdentity() {
		return b.memberCheckout(req)
	}
	return b.soleWorkspaceCheckout()
}

// validatePushRequest rejects structurally unusable specs before touching the
// workspace.
func (b *AgentBridge) validatePushRequest(req pushRequest, specPresent bool) *pushRejected {
	if !specPresent {
		return b.rejectPush("missing_push_spec", "Push failed - missing push specification")
	}
	if (req.repoOwner != "") != (req.repoName != "") {
		return b.rejectPush("partial_repo_identity",
			"Push failed - pushSpec must carry both repoOwner and repoName",
			"repo_owner", req.repoOwner, "repo_name", req.repoName)
	}
	if req.branchName == "" {
		return b.rejectPush("missing_target_branch", "Push failed - missing target branch")
	}
	if req.refspec == "" || req.pushURL == "" {
		return b.rejectPush("invalid_push_spec", "Push failed - invalid push specification")
	}
	return nil
}

// memberCheckout returns the checkout of the session member the spec names. The
// identity is matched against the supervisor-written manifest and the matched
// entry's path is used verbatim — spec-supplied strings never become filesystem
// paths, so a crafted name cannot select a checkout outside the session.
func (b *AgentBridge) memberCheckout(req pushRequest) (string, error) {
	member, ok := repomanifest.Find(
		repomanifest.Load(b.repoManifestPath), req.repoOwner, req.repoName)
	if !ok {
		return "", b.rejectPush("repo_not_session_member",
			fmt.Sprintf("Repository %s is not part of this session", req.repoFullName()),
			"repo_owner", req.repoOwner, "repo_name", req.repoName)
	}
	if _, err := os.Stat(filepath.Join(member.Path, ".git")); err != nil {
		return "", b.rejectPush("repo_not_in_workspace",
			fmt.Sprintf("Repository %s not found in workspace", req.repoFullName()),
			"repo_owner", req.repoOwner, "repo_name", req.repoName)
	}
	return member.Path, nil
}

// soleWorkspaceCheckout returns the checkout for a spec that names no repository
// (legacy control planes, single-repo sessions). findRepoDir prefers REPO_NAME
// then the sole "*/.git" clone under the workspace.
func (b *AgentBridge) soleWorkspaceCheckout() (string, error) {
	dir, ok := b.findRepoDir()
	if !ok {
		return "", b.rejectPush("no_repo_configured", "No repository found")
	}
	return dir, nil
}

// runGitPush runs git push in repoDir; returns a *pushRejected on failure or
// timeout.
func (b *AgentBridge) runGitPush(ctx context.Context, req pushRequest, repoDir string) error {
	b.log.Info("git.push_command",
		"branch_name", req.branchName, "refspec", req.refspec,
		"force", req.force, "remote_url", req.redactedPushURL)

	args := []string{"push", req.pushURL, req.refspec}
	if req.force {
		args = append(args, "-f")
	}

	cctx, cancel := context.WithTimeout(ctx, gitPushTimeout)
	defer cancel()
	c := exec.CommandContext(cctx, "git", args...)
	c.Dir = repoDir
	var stderr bytes.Buffer
	c.Stderr = &stderr
	// On timeout: SIGTERM, then SIGKILL after the grace period.
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = gitPushTerminateGrace

	err := c.Run()

	if cctx.Err() == context.DeadlineExceeded {
		b.log.Warn("git.push_timeout", "branch_name", req.branchName, "timeout_ms", gitPushTimeout.Milliseconds())
		return &pushRejected{msg: fmt.Sprintf(
			"Push failed - git push timed out after %ds", int(gitPushTimeout.Seconds()))}
	}
	if err != nil {
		stderrText := strings.TrimSpace(stderr.String())
		redacted := redactGitStderr(stderrText, req.pushURL, req.redactedPushURL)
		b.log.Warn("git.push_failed", "branch_name", req.branchName, "stderr", redacted)
		if redacted != "" {
			return &pushRejected{msg: "Push failed: " + redacted}
		}
		return &pushRejected{msg: "Push failed - unknown error"}
	}
	return nil
}

// redactGitStderr removes credential-bearing URLs from git stderr.
func redactGitStderr(stderr, pushURL, redactedURL string) string {
	out := stderr
	if pushURL != "" && redactedURL != "" {
		out = strings.ReplaceAll(out, pushURL, redactedURL)
	}
	return credentialURLRE.ReplaceAllString(out, "${1}***@")
}
