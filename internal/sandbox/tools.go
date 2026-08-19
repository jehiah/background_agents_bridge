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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/controlplane"
	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
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
	"spawn-child":         {run: runSpawnChild},
	"get-child-status":    {run: runGetChildStatus},
	"cancel-child":        {run: runCancelChild},
	"send-child-prompt":   {run: runSendChildPrompt},
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

// prRepoTarget is a create-pull-request target repository resolved from the
// session manifest (or parsed from the argument when no manifest is present).
type prRepoTarget struct {
	owner string
	name  string
	path  string // manifest checkout path; "" when parsed without a manifest
}

// resolveRepositoryTarget resolves an "owner/name" argument, preserving nested
// owners and the manifest's canonical casing/path. It returns nil when the
// argument names a repo outside the manifest, or (manifest absent) is not a
// well-formed "owner/name". Mirrors resolveRepositoryTarget in the upstream
// inspect-plugin.js: nested owners are supported by splitting on the LAST "/".
func resolveRepositoryTarget(repo string, repositories []repomanifest.Entry) *prRepoTarget {
	requested := strings.TrimSpace(repo)

	if len(repositories) > 0 {
		normalized := strings.ToLower(requested)
		for _, r := range repositories {
			if strings.ToLower(r.Owner+"/"+r.Name) == normalized {
				return &prRepoTarget{owner: r.Owner, name: r.Name, path: r.Path}
			}
		}
		return nil
	}

	sep := strings.LastIndex(requested, "/")
	if sep <= 0 || sep == len(requested)-1 {
		return nil
	}
	owner := requested[:sep]
	name := requested[sep+1:]
	if slices.Contains(strings.Split(owner, "/"), "") {
		return nil
	}
	return &prRepoTarget{owner: owner, name: name}
}

// validRepoValues formats the manifest's "owner/name" identities for error
// messages that list the session's valid repositories.
func validRepoValues(repositories []repomanifest.Entry) string {
	vals := make([]string, len(repositories))
	for i, r := range repositories {
		vals[i] = r.Owner + "/" + r.Name
	}
	return strings.Join(vals, ", ")
}

func runCreatePR(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	baseBranch := argStr(args, "baseBranch")

	// Resolve the target repository for multi-repo sessions. repoPath, when set,
	// pins branch resolution to the member checkout named by the spec.
	repositories := repomanifest.Load(repomanifest.ManifestPath)
	var repoOwner, repoName, repoPath string
	if repoArg := argStr(args, "repo"); repoArg != "" {
		target := resolveRepositoryTarget(repoArg, repositories)
		if target == nil && len(repositories) > 0 {
			return fmt.Sprintf("Failed to create pull request: %s is not part of this session. Valid values: %s.",
				repoArg, validRepoValues(repositories))
		}
		if target == nil {
			return `Failed to create pull request: repo must be "owner/name".`
		}
		repoOwner, repoName, repoPath = target.owner, target.name, target.path
	} else if len(repositories) > 1 {
		return fmt.Sprintf("Failed to create pull request: this session spans multiple repositories — pass repo with one of: %s.",
			validRepoValues(repositories))
	}

	branchDir := repoPath
	if branchDir == "" {
		branchDir = resolveRepoDir()
	}
	headBranch := currentGitBranch(ctx, branchDir)
	if msg := requireFeatureBranch(headBranch, baseBranch); msg != "" {
		return msg
	}

	req := controlplane.CreatePRRequest{
		Title:      orDefault(argStr(args, "title"), "Changes from OpenCode session"),
		Body:       orDefault(argStr(args, "body"), "Automated PR created via create-pull-request tool"),
		BaseBranch: baseBranch,
		HeadBranch: headBranch,
		RepoOwner:  repoOwner,
		RepoName:   repoName,
		Draft:      argBoolPtr(args, "draft"),
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
				return fmt.Sprintf("Conflict: %s. To open an additional pull request, create a new branch "+
					"(`git checkout -b`), commit, and call this tool again.", e.Display())
			}
			return "Failed to create pull request: " + e.Display()
		}
		return "Failed to create pull request: " + err.Error()
	}

	if result.Manual() {
		return fmt.Sprintf("Branch pushed successfully.\n\nCreate the pull request in GitHub:\n%s\n\nUse your logged-in GitHub account to finish creating the PR.", result.CreatePRURL)
	}
	return prSuccessMessage(result)
}

// prSuccessMessage reports the created (or reused) PR and its state. Repository
// policy can force a draft even when the agent did not ask for one, so the state
// has to come from the response rather than from the request. Calling the tool
// again from the same branch updates that branch's open PR instead of opening a
// new one, so the message has to say which of the two happened.
func prSuccessMessage(result controlplane.PRResult) string {
	branches := ""
	if result.HeadBranch != "" && result.BaseBranch != "" {
		branches = fmt.Sprintf(" (%s -> %s)", result.HeadBranch, result.BaseBranch)
	}
	if result.Updated {
		return fmt.Sprintf("Pull request updated with your latest commits.\n\nPR #%d%s: %s",
			result.PRNumber, branches, result.PRURL)
	}
	status := "The pull request is now ready for review."
	if result.State == "draft" {
		status = "The pull request is in draft mode."
	}
	return fmt.Sprintf("Pull request created successfully!\n\nPR #%d%s: %s\n\n%s",
		result.PRNumber, branches, result.PRURL, status)
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

// resolveRepoDir picks the directory to run git in for a tool call. The tool is
// always bound to the session's repository, so there is no caller-supplied
// override: it resolves to
//  1. /workspace/$REPO_NAME when REPO_NAME is set AND that checkout exists,
//  2. first single-"*/.git" autodiscovery under /workspace,
//  3. "" → caller falls back to git's inherited cwd.
//
// Step 1 mirrors defaultWorkdir() in cmd/bridge and (*AgentBridge).findRepoDir
// so the PR tool reads HEAD from the same tree opencode is editing and the
// daemon pushes from.
func resolveRepoDir() string {
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

// --- spawn-child -------------------------------------------------------------

func runSpawnChild(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	result, err := c.SpawnChild(ctx, controlplane.SpawnChildRequest{
		Title:     argStr(args, "title"),
		Prompt:    argStr(args, "prompt"),
		Model:     argStr(args, "model"),
		Reasoning: argStrPtr(args, "reasoning"),
	})
	if err != nil {
		if e, ok := apiErr(err); ok {
			switch e.StatusCode {
			case http.StatusForbidden:
				return fmt.Sprintf("Cannot spawn child: %s. This may be a depth limit or repository restriction.", e.Display())
			case http.StatusTooManyRequests:
				return fmt.Sprintf("Rate limited: %s. Wait a moment before spawning another child.", e.Display())
			}
			return fmt.Sprintf("Failed to spawn child: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to spawn child: " + err.Error()
	}

	return strings.Join([]string{
		"Child spawned successfully.",
		"",
		"  Child ID: " + result.SessionID,
		"  Status:  PENDING",
		"",
		"The child will continue independently. Check status only when you need its result; do not poll repeatedly.",
	}, "\n")
}

// --- send-child-prompt -------------------------------------------------------

// runSendChildPrompt queues a follow-up prompt in a direct child session. The
// control plane admits it behind the child's current work, so the tool reports
// that the prompt is queued rather than answered.
func runSendChildPrompt(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	childID := argStr(args, "childId")
	result, err := c.SendChildPrompt(ctx, childID, argStr(args, "prompt"))
	if err != nil {
		if e, ok := apiErr(err); ok {
			switch e.StatusCode {
			case http.StatusNotFound:
				return fmt.Sprintf("Child %q not found. Use get-child-status to list direct children.", childID)
			case http.StatusConflict:
				// The child is in a state it cannot resume from (cancelled or
				// archived), not merely busy.
				return fmt.Sprintf("Cannot prompt child %q: %s", childID, e.Display())
			case http.StatusTooManyRequests:
				return fmt.Sprintf("Cannot queue another prompt for child %q: %s", childID, e.Display())
			}
			return fmt.Sprintf("Failed to prompt child: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to prompt child: " + err.Error()
	}
	return strings.Join([]string{
		fmt.Sprintf("Follow-up durably queued for child %q.", childID),
		"Message ID: " + result.MessageID,
		"The prompt will run after any current child work. Use get-child-status when you need the result.",
	}, "\n")
}

// --- cancel-child ------------------------------------------------------------

func runCancelChild(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	childID := argStr(args, "childId")
	// Cascading is the default: a cancelled child whose own children keep running
	// leaves orphaned sandboxes behind. The tool schema defaults it too, so the
	// arg is normally present; this covers a direct `bridge tool` invocation.
	cancelNested := true
	if v := argBoolPtr(args, "cancelNested"); v != nil {
		cancelNested = *v
	}
	result, err := c.CancelChild(ctx, childID, cancelNested)
	if err != nil {
		if e, ok := apiErr(err); ok {
			switch e.StatusCode {
			case http.StatusNotFound:
				return fmt.Sprintf("Child %q not found. Use get-child-status to list available children.", childID)
			case http.StatusConflict:
				return "Cannot cancel: " + e.Display()
			}
			return fmt.Sprintf("Failed to cancel child: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to cancel child: " + err.Error()
	}
	status := orDefault(result.Status, "cancelled")
	nested := ""
	if n := len(result.CancelledDescendantIDs); n > 0 {
		nested = fmt.Sprintf(" Also cancelled %d nested child session(s).", n)
	}
	return fmt.Sprintf("Child %q cancelled successfully.%s Status: %s", childID, nested, strings.ToUpper(status))
}
