package controlplane

import "context"

// CreatePRRequest describes a pull request to open from the session's branch.
// Empty optional fields are omitted so the control plane applies its defaults
// (e.g. the repository's default base branch).
type CreatePRRequest struct {
	Title      string
	Body       string
	BaseBranch string // optional
	HeadBranch string // optional
	RepoOwner  string // optional; identifies the target member in multi-repo sessions
	RepoName   string // optional; identifies the target member in multi-repo sessions
	Draft      *bool  // optional; nil omits the field so the server applies the repo's draft policy
}

// PRResult is the control-plane response to a PR creation. On the normal path it
// carries PRNumber/PRURL/State; some configurations instead return a "manual"
// status with a CreatePRURL the user must open to finish the PR.
type PRResult struct {
	PRNumber int    `json:"prNumber"`
	PRURL    string `json:"prUrl"`
	State    string `json:"state"`

	// HeadBranch and BaseBranch are the branches the control plane actually
	// resolved. They can differ from what was requested (an omitted base falls
	// back to the session's), so the agent is told which pair it got.
	HeadBranch string `json:"headBranch"`
	BaseBranch string `json:"baseBranch"`
	// Updated reports that an existing open pull request on this head was
	// force-pushed and reused rather than a new one being opened.
	Updated bool `json:"updated"`

	Status      string `json:"status"`
	CreatePRURL string `json:"createPrUrl"`
}

// Manual reports whether the PR must be completed by hand via CreatePRURL.
func (r PRResult) Manual() bool {
	return r.Status == "manual" && r.CreatePRURL != ""
}

// CreatePR opens a pull request (POST /pr).
func (c *Client) CreatePR(ctx context.Context, req CreatePRRequest) (PRResult, error) {
	payload := map[string]any{
		"title": req.Title,
		"body":  req.Body,
	}
	if req.BaseBranch != "" {
		payload["baseBranch"] = req.BaseBranch
	}
	if req.HeadBranch != "" {
		payload["headBranch"] = req.HeadBranch
	}
	if req.RepoOwner != "" {
		payload["repoOwner"] = req.RepoOwner
	}
	if req.RepoName != "" {
		payload["repoName"] = req.RepoName
	}
	if req.Draft != nil {
		payload["draft"] = *req.Draft
	}
	var out PRResult
	if err := c.doJSON(ctx, "POST", "/pr", payload, &out); err != nil {
		return PRResult{}, err
	}
	return out, nil
}
