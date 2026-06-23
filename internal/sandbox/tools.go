package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// toolImpl is the execution side of a tool: the work it does plus an optional
// formatter for the case where the control-plane client can't be constructed
// (so a tool with a structured contract, like slack-notify, can keep it).
type toolImpl struct {
	run       func(ctx context.Context, c *controlplane.Client, args map[string]any) string
	clientErr func(err error) string // optional; defaults to a generic message
}

// toolImpls is the dispatch table for `bridge tool <name>`. Keys must match the
// generated tool definitions (toolDefs in toolgen.go; enforced by a test).
var toolImpls = map[string]toolImpl{
	"create-pull-request": {run: runCreatePR},
	"spawn-task":          {run: runSpawnTask},
	"get-task-status":     {run: runGetTaskStatus},
	"cancel-task":         {run: runCancelTask},
	"slack-notify":        {run: runSlackNotify, clientErr: slackClientErr},
	"image-upload":        {run: runImageUpload},
}

// ToolNames returns the registered tool names in stable order.
func ToolNames() []string {
	names := make([]string, 0, len(toolImpls))
	for name := range toolImpls {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RunTool executes a single tool call. Arguments are read as a JSON object from
// stdin; the agent-facing result is written to stdout. It returns an error only
// for invocation problems (unknown tool, unreadable stdin); tool-level failures
// are surfaced as the result string with a nil error, matching the JS tools
// which return strings rather than throwing.
func RunTool(name string, stdin io.Reader, stdout io.Writer) error {
	impl, ok := toolImpls[name]
	if !ok {
		return fmt.Errorf("unknown tool %q (known: %s)", name, strings.Join(ToolNames(), ", "))
	}

	args, err := readArgs(stdin)
	if err != nil {
		return fmt.Errorf("read tool args: %w", err)
	}

	cfg := config.Resolve(config.Flags{})
	c, err := controlplane.New(cfg.ControlPlaneURL, cfg.AuthToken, cfg.SessionID)
	if err != nil {
		msg := "Tool unavailable: " + err.Error()
		if impl.clientErr != nil {
			msg = impl.clientErr(err)
		}
		_, werr := fmt.Fprintln(stdout, msg)
		return werr
	}

	_, werr := fmt.Fprintln(stdout, impl.run(context.Background(), c, args))
	return werr
}

// apiErr extracts a *controlplane.APIError from err, if present.
func apiErr(err error) (*controlplane.APIError, bool) {
	return errors.AsType[*controlplane.APIError](err)
}

// --- create-pull-request -----------------------------------------------------

func runCreatePR(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	baseBranch := argStr(args, "baseBranch")
	headBranch := currentGitBranch(ctx, resolveRepoDir(argStr(args, "directory")))
	if msg := requireFeatureBranch(headBranch, baseBranch); msg != "" {
		return msg
	}

	req := controlplane.CreatePRRequest{
		Title:      orDefault(argStr(args, "title"), "Changes from OpenCode session"),
		Body:       orDefault(argStr(args, "body"), "Automated PR created via create-pull-request tool"),
		BaseBranch: baseBranch,
		HeadBranch: headBranch,
	}

	result, err := c.CreatePR(ctx, req)
	if err != nil {
		if e, ok := apiErr(err); ok {
			switch e.StatusCode {
			case http.StatusUnauthorized:
				return fmt.Sprintf("Authentication failed: %s. The GitHub token may have expired - please re-authenticate.", e.Display())
			case http.StatusNotFound:
				return fmt.Sprintf("Session not found: %s. The session may have been deleted or the ID is incorrect.", e.Display())
			case http.StatusConflict:
				return fmt.Sprintf("Conflict: %s. A PR may already exist for this branch.", e.Display())
			}
			return "Failed to create pull request: " + e.Display()
		}
		return "Failed to create pull request: " + err.Error()
	}

	if result.Manual() {
		return fmt.Sprintf("Branch pushed successfully.\n\nCreate the pull request in GitHub:\n%s\n\nUse your logged-in GitHub account to finish creating the PR.", result.CreatePRURL)
	}
	return fmt.Sprintf("Pull request created successfully!\n\nPR #%d: %s\n\nThe PR is now ready for review.", result.PRNumber, result.PRURL)
}

// requireFeatureBranch returns a non-empty, agent-facing error when head is not
// a usable PR source branch. Creating a PR from a detached HEAD or from the base
// branch makes the control plane discard the value and fall back to a generated
// branch name ("<prefix>/<sessionId>") instead of the agent's branch — so we
// stop early and tell the agent to create a dedicated feature branch.
func requireFeatureBranch(head, baseBranch string) string {
	const hint = "Create a dedicated feature branch first, e.g. `git checkout -b feature/short-description`, " +
		"move your commits onto it, then call this tool again."
	if head == "" {
		return "Cannot create a pull request: the repository is in a detached HEAD state (no current branch). " + hint
	}
	h := strings.ToLower(head)
	base := strings.ToLower(strings.TrimSpace(baseBranch))
	if h == "main" || h == "master" || (base != "" && h == base) {
		return fmt.Sprintf("Cannot create a pull request from %q: pull requests must come from a feature branch, not the base branch. ", head) + hint
	}
	return ""
}

// currentGitBranch resolves the current branch name of the checked-out
// repository, returning "" for a detached HEAD or any error (the server then
// falls back to a generated branch name like "<prefix>/<sessionId>").
//
// The branch reported here is the only reliable source the control plane has for
// the PR head branch, so it must be resolved deterministically. `bridge tool` is
// a short-lived child process spawned by the OpenCode tool shim; trusting its
// inherited working directory is fragile and was the cause of branches
// intermittently being lost (and replaced by the generated default). We instead
// pin git to the same checkout the daemon pushes from (see findRepoDir in
// internal/bridge/git.go), falling back to the inherited cwd only when no
// checkout is found under the workspace root.
//
// It is a package var so tests can stub branch resolution.
var currentGitBranch = func(ctx context.Context, dir string) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c := exec.CommandContext(cctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if dir != "" {
		c.Dir = dir
	}
	out, err := c.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return ""
	}
	return branch
}

// workspaceRoot is where the sandbox checks out the repository. The repo lives
// in a subdirectory (workspaceRoot/<name>/.git), matching the bridge daemon.
const workspaceRoot = "/workspace"

// resolveRepoDir picks the directory to run git in for a tool call:
//  1. an explicit `directory` arg (trusted as-is),
//  2. /workspace/$REPO_NAME when REPO_NAME is set AND that checkout exists,
//  3. first single-"*/.git" autodiscovery under /workspace,
//  4. "" → caller falls back to git's inherited cwd.
//
// Step 2 mirrors defaultWorkdir() in cmd/bridge so the PR tool reads HEAD from
// the same tree opencode is editing.
func resolveRepoDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if repo := os.Getenv("REPO_NAME"); repo != "" {
		cand := filepath.Join(workspaceRoot, repo)
		if _, err := os.Stat(filepath.Join(cand, ".git")); err == nil {
			return cand
		}
	}
	return firstRepoDir()
}

// firstRepoDir resolves the checked-out repository directory under the workspace
// root, mirroring (*AgentBridge).findRepoDir in internal/bridge/git.go: the
// single "*/.git" entry under /workspace. It returns "" when nothing is found,
// letting the caller fall back to git's own working directory.
func firstRepoDir() string {
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(workspaceRoot, e.Name(), ".git")); err == nil {
			return filepath.Join(workspaceRoot, e.Name())
		}
	}
	return ""
}

// --- spawn-task --------------------------------------------------------------

func runSpawnTask(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	result, err := c.SpawnChild(ctx, controlplane.SpawnChildRequest{
		Title:  argStr(args, "title"),
		Prompt: argStr(args, "prompt"),
		Model:  argStr(args, "model"),
	})
	if err != nil {
		if e, ok := apiErr(err); ok {
			switch e.StatusCode {
			case http.StatusForbidden:
				return fmt.Sprintf("Cannot spawn task: %s. This may be a depth limit or repository restriction.", e.Display())
			case http.StatusTooManyRequests:
				return fmt.Sprintf("Rate limited: %s. Wait a moment before spawning another task.", e.Display())
			}
			return fmt.Sprintf("Failed to spawn task: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to spawn task: " + err.Error()
	}

	return strings.Join([]string{
		"Task spawned successfully.",
		"",
		"  Task ID: " + result.SessionID,
		"  Status:  PENDING",
		"",
		"Use get-task-status with this task ID to check progress.",
	}, "\n")
}

// --- cancel-task -------------------------------------------------------------

func runCancelTask(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	taskID := argStr(args, "taskId")
	result, err := c.CancelChild(ctx, taskID)
	if err != nil {
		if e, ok := apiErr(err); ok {
			switch e.StatusCode {
			case http.StatusNotFound:
				return fmt.Sprintf("Task %q not found. Use get-task-status to list available tasks.", taskID)
			case http.StatusConflict:
				return "Cannot cancel: " + e.Display()
			}
			return fmt.Sprintf("Failed to cancel task: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to cancel task: " + err.Error()
	}
	status := orDefault(result.Status, "cancelled")
	return fmt.Sprintf("Task %q cancelled successfully. Status: %s", taskID, strings.ToUpper(status))
}
