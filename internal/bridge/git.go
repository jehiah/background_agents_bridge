package bridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// credentialURLRE matches a userinfo component in an http(s) URL so credentials
// can be stripped from git output.
var credentialURLRE = regexp.MustCompile(`(https?://)([^/\s@]+)@`)

// findRepoDir locates the checked-out repository under repoPath. It prefers
// repoPath/$REPO_NAME when REPO_NAME is set and that checkout exists, then falls
// back to the first "*/.git" entry (mirroring the Python glob). Preferring
// REPO_NAME keeps handlePush pinned to the same tree opencode edits when more
// than one checkout is present, matching resolveRepoDir in internal/sandbox.
func (b *AgentBridge) findRepoDir() (string, bool) {
	if repo := os.Getenv("REPO_NAME"); repo != "" {
		cand := filepath.Join(b.repoPath, repo)
		if _, err := os.Stat(filepath.Join(cand, ".git")); err == nil {
			return cand, true
		}
	}
	entries, err := os.ReadDir(b.repoPath)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.repoPath, e.Name(), ".git")); err == nil {
			return filepath.Join(b.repoPath, e.Name()), true
		}
	}
	return "", false
}

// configureGitIdentity sets the global git user.name/user.email for commit
// attribution. Failures are logged but not fatal.
func (b *AgentBridge) configureGitIdentity(ctx context.Context, user GitUser) {
	b.log.Debug("git.identity_configure", "git_name", user.Name, "git_email", user.Email)

	run := func(args ...string) error {
		cctx, cancel := context.WithTimeout(ctx, gitConfigTimeout)
		defer cancel()
		c := exec.CommandContext(cctx, "git", append([]string{"config", "--global"}, args...)...)
		var stderr bytes.Buffer
		c.Stderr = &stderr
		return c.Run()
	}

	if err := run("user.name", user.Name); err != nil {
		b.log.Error("git.identity_error", "exc", err)
		return
	}
	if err := run("user.email", user.Email); err != nil {
		b.log.Error("git.identity_error", "exc", err)
	}
}

// handlePush pushes using a provider-generated push spec and reports the result.
func (b *AgentBridge) handlePush(ctx context.Context, cmd *command) {
	spec := cmd.PushSpec
	branch := ""
	if spec != nil {
		branch = strings.TrimSpace(spec.TargetBranch)
	}
	b.log.Info("git.push_start", "branch_name", branch, "mode", "push_spec")

	repoDir, ok := b.findRepoDir()
	if !ok {
		b.log.Warn("git.push_error", "reason", "no_repository")
		b.sendEvent(pushErrorEvent("No repository found", "", false))
		return
	}

	if spec == nil {
		b.log.Warn("git.push_error", "reason", "missing_push_spec")
		b.sendEvent(pushErrorEvent("Push failed - missing push specification", branch, true))
		return
	}
	if branch == "" {
		b.log.Warn("git.push_error", "reason", "missing_target_branch")
		b.sendEvent(pushErrorEvent("Push failed - missing target branch", "", true))
		return
	}

	refspec := strings.TrimSpace(spec.Refspec)
	pushURL := strings.TrimSpace(spec.RemoteURL)
	redactedURL := strings.TrimSpace(spec.RedactedRemoteURL)
	force := spec.Force
	if refspec == "" || pushURL == "" {
		b.log.Warn("git.push_error", "reason", "invalid_push_spec")
		b.sendEvent(pushErrorEvent("Push failed - invalid push specification", branch, true))
		return
	}

	b.log.Info("git.push_command",
		"branch_name", branch, "refspec", refspec, "force", force, "remote_url", redactedURL)

	args := []string{"push", pushURL, refspec}
	if force {
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
		b.log.Warn("git.push_timeout", "branch_name", branch, "timeout_ms", gitPushTimeout.Milliseconds())
		b.sendEvent(pushErrorEvent(
			fmt.Sprintf("Push failed - git push timed out after %ds", int(gitPushTimeout.Seconds())),
			branch, true,
		))
		return
	}
	if err != nil {
		stderrText := strings.TrimSpace(stderr.String())
		redacted := redactGitStderr(stderrText, pushURL, redactedURL)
		b.log.Warn("git.push_failed", "branch_name", branch, "stderr", redacted)
		msg := "Push failed - unknown error"
		if redacted != "" {
			msg = "Push failed: " + redacted
		}
		b.sendEvent(pushErrorEvent(msg, branch, true))
		return
	}

	b.log.Info("git.push_complete", "branch_name", branch)
	b.sendEvent(pushCompleteEvent(branch))
}

// redactGitStderr removes credential-bearing URLs from git stderr.
func redactGitStderr(stderr, pushURL, redactedURL string) string {
	out := stderr
	if pushURL != "" && redactedURL != "" {
		out = strings.ReplaceAll(out, pushURL, redactedURL)
	}
	return credentialURLRE.ReplaceAllString(out, "${1}***@")
}
