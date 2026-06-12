package controlplane

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// SpawnChildRequest describes a child coding task to spawn.
type SpawnChildRequest struct {
	Title  string
	Prompt string
	Model  string // optional; defaults to the parent's model
}

// SpawnChildResult identifies the spawned child session.
type SpawnChildResult struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

// SpawnChild creates a child session (POST /children).
func (c *Client) SpawnChild(ctx context.Context, req SpawnChildRequest) (SpawnChildResult, error) {
	payload := map[string]any{"title": req.Title, "prompt": req.Prompt}
	if req.Model != "" {
		payload["model"] = req.Model
	}
	var out SpawnChildResult
	if err := c.doJSON(ctx, "POST", "/children", payload, &out); err != nil {
		return SpawnChildResult{}, err
	}
	return out, nil
}

// ChildSummary is one entry from the children list.
type ChildSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
}

// ListChildren lists the session's children (GET /children).
func (c *Client) ListChildren(ctx context.Context) ([]ChildSummary, error) {
	var out struct {
		Children []ChildSummary `json:"children"`
	}
	if err := c.doJSON(ctx, "GET", "/children", nil, &out); err != nil {
		return nil, err
	}
	return out.Children, nil
}

// ChildDetailOptions selects optional sections of a child detail response.
type ChildDetailOptions struct {
	IncludeResponse   bool
	IncludeTrajectory bool
	TrajectoryLimit   int    // 0 = server default
	TrajectoryCursor  string // page cursor from a prior trajectory response
}

// ChildDetail is the detailed view of a single child session.
type ChildDetail struct {
	Session       ChildSession   `json:"session"`
	Sandbox       *ChildSandbox  `json:"sandbox"`
	Artifacts     []Artifact     `json:"artifacts"`
	FinalResponse *FinalResponse `json:"finalResponse"`
	Trajectory    *Trajectory    `json:"trajectory"`
	RecentEvents  []Event        `json:"recentEvents"`
}

// ChildSession is the core session metadata in a child detail.
type ChildSession struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	RepoOwner  string `json:"repoOwner"`
	RepoName   string `json:"repoName"`
	BranchName string `json:"branchName"`
	Model      string `json:"model"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// ChildSandbox is the child's sandbox status, when present.
type ChildSandbox struct {
	Status string `json:"status"`
}

// Artifact is a produced artifact (e.g. a PR or media upload).
type Artifact struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// FinalResponse is the child's final assistant response summary.
type FinalResponse struct {
	Success           bool       `json:"success"`
	Error             string     `json:"error"`
	TextContent       string     `json:"textContent"`
	ToolCalls         []ToolCall `json:"toolCalls"`
	EventCount        int        `json:"eventCount"`
	EventLimitReached bool       `json:"eventLimitReached"`
}

// ToolCall is one tool invocation in a final response summary.
type ToolCall struct {
	Summary string `json:"summary"`
	Tool    string `json:"tool"`
}

// Trajectory is a paginated page of persisted child events.
type Trajectory struct {
	Events  []Event `json:"events"`
	HasMore bool    `json:"hasMore"`
	Cursor  string  `json:"cursor"`
}

// Event is a single persisted child event. Data is an opaque payload whose shape
// varies by event type.
type Event struct {
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
	CreatedAt int64          `json:"createdAt"`
	MessageID string         `json:"messageId"`
}

// GetChild fetches the detail for one child (GET /children/{id}).
func (c *Client) GetChild(ctx context.Context, childID string, opts ChildDetailOptions) (ChildDetail, error) {
	var out ChildDetail
	if err := c.doJSON(ctx, "GET", "/children/"+url.PathEscape(childID)+childQuery(opts), nil, &out); err != nil {
		return ChildDetail{}, err
	}
	return out, nil
}

// childQuery builds the include/trajectory query string.
func childQuery(opts ChildDetailOptions) string {
	var include []string
	if opts.IncludeResponse {
		include = append(include, "result")
	}
	if opts.IncludeTrajectory {
		include = append(include, "trajectory")
	}
	params := url.Values{}
	if len(include) > 0 {
		params.Set("include", strings.Join(include, ","))
	}
	if opts.IncludeTrajectory && opts.TrajectoryLimit > 0 {
		params.Set("trajectoryLimit", strconv.Itoa(opts.TrajectoryLimit))
	}
	if opts.IncludeTrajectory && opts.TrajectoryCursor != "" {
		params.Set("trajectoryCursor", opts.TrajectoryCursor)
	}
	if q := params.Encode(); q != "" {
		return "?" + q
	}
	return ""
}

// CancelResult is the outcome of cancelling a child.
type CancelResult struct {
	Status string `json:"status"`
}

// CancelChild cancels a running child (POST /children/{id}/cancel).
func (c *Client) CancelChild(ctx context.Context, childID string) (CancelResult, error) {
	var out CancelResult
	if err := c.doJSON(ctx, "POST", "/children/"+url.PathEscape(childID)+"/cancel", nil, &out); err != nil {
		return CancelResult{}, err
	}
	return out, nil
}
