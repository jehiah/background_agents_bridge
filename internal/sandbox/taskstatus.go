package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// runGetTaskStatus is dual-mode: with no taskId it lists children; with a taskId
// it returns details. Formatting ports tools/get-task-status-format.js.
func runGetTaskStatus(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	includeEventData := argBool(args, "includeEventData")
	opts := controlplane.ChildDetailOptions{
		IncludeResponse:   argBool(args, "includeResponse"),
		IncludeTrajectory: argBool(args, "includeTrajectory") || includeEventData,
		TrajectoryLimit:   argInt(args, "trajectoryLimit"),
		TrajectoryCursor:  argStr(args, "trajectoryCursor"),
	}

	if taskID := argStr(args, "taskId"); taskID != "" {
		return getChildDetail(ctx, c, taskID, opts, includeEventData)
	}
	return listChildren(ctx, c)
}

func listChildren(ctx context.Context, c *controlplane.Client) string {
	children, err := c.ListChildren(ctx)
	if err != nil {
		if e, ok := apiErr(err); ok {
			return fmt.Sprintf("Failed to list tasks: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to list tasks: " + err.Error()
	}
	if len(children) == 0 {
		return "No child tasks found."
	}

	var pending, running, done, failed int
	var lines []string
	for _, child := range children {
		label := formatStatus(child.Status)
		switch label {
		case "PENDING":
			pending++
		case "RUNNING":
			running++
		case "FAILED":
			failed++
		default:
			done++
		}
		lines = append(lines,
			fmt.Sprintf("  [%s] %s", label, child.ID),
			"    Title: "+orDefault(child.Title, "(untitled)"),
			"    Created: "+formatTimestamp(child.CreatedAt),
			"",
		)
	}

	header := fmt.Sprintf("%d child task(s): %d running, %d pending, %d done, %d failed",
		len(children), running, pending, done, failed)
	return strings.Join(append([]string{header, ""}, lines...), "\n")
}

func getChildDetail(ctx context.Context, c *controlplane.Client, taskID string, opts controlplane.ChildDetailOptions, includeEventData bool) string {
	detail, err := c.GetChild(ctx, taskID, opts)
	if err != nil {
		if e, ok := apiErr(err); ok {
			if e.StatusCode == http.StatusNotFound {
				return fmt.Sprintf("Task %q not found. Use get-task-status without a taskId to list all tasks.", taskID)
			}
			return fmt.Sprintf("Failed to get task: %s (HTTP %d)", e.Display(), e.StatusCode)
		}
		return "Failed to get task: " + err.Error()
	}
	return formatChildDetail(detail, taskID, opts.IncludeResponse, includeEventData)
}

// --- formatting (ported from get-task-status-format.js) ----------------------

var statusLabels = map[string]string{
	"created":   "PENDING",
	"active":    "RUNNING",
	"completed": "DONE",
	"failed":    "FAILED",
	"cancelled": "CANCELLED",
	"archived":  "DONE",
}

func formatStatus(status string) string {
	if label, ok := statusLabels[status]; ok {
		return label
	}
	return strings.ToUpper(status)
}

// formatTimestamp renders an epoch-ms timestamp as an ISO-8601 string with
// milliseconds, matching new Date(ts).toISOString(). Zero renders as "n/a".
func formatTimestamp(ms int64) string {
	if ms == 0 {
		return "n/a"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

func indentBlock(text, indent string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

func formatChildDetail(d controlplane.ChildDetail, taskID string, includeResponse, includeEventData bool) string {
	s := d.Session
	id := orDefault(s.ID, taskID)
	lines := []string{
		"Task: " + id,
		"  Title:   " + orDefault(s.Title, "(untitled)"),
		"  Status:  " + formatStatus(orDefault(s.Status, "unknown")),
		"  Model:   " + orDefault(s.Model, "default"),
		"  Repo:    " + s.RepoOwner + "/" + s.RepoName,
		"  Branch:  " + orDefault(s.BranchName, "(none)"),
		"  Created: " + formatTimestamp(s.CreatedAt),
		"  Updated: " + formatTimestamp(s.UpdatedAt),
	}
	if d.Sandbox != nil {
		lines = append(lines, "  Sandbox: "+d.Sandbox.Status)
	}

	lines = append(lines, formatArtifacts(d.Artifacts)...)
	lines = append(lines, formatFinalResponse(d.FinalResponse, includeResponse)...)
	lines = append(lines, formatTrajectory(d.Trajectory, includeEventData)...)
	lines = append(lines, formatRecentEvents(d.RecentEvents)...)
	return strings.Join(lines, "\n")
}

func formatArtifacts(artifacts []controlplane.Artifact) []string {
	if len(artifacts) == 0 {
		return nil
	}
	lines := []string{"", "  Artifacts:"}
	for _, a := range artifacts {
		if a.Type == "pr" {
			lines = append(lines, "    - PR: "+a.URL)
		} else {
			lines = append(lines, fmt.Sprintf("    - %s: %s", a.Type, a.URL))
		}
	}
	return lines
}

func formatFinalResponse(fr *controlplane.FinalResponse, includeResponse bool) []string {
	if fr == nil {
		if includeResponse {
			return []string{"", "  Final response: not available yet"}
		}
		return nil
	}
	lines := []string{"", "  Final response:"}
	if fr.Success {
		lines = append(lines, "    Success: yes")
	} else {
		lines = append(lines, "    Success: no")
	}
	if fr.Error != "" {
		lines = append(lines, "    Error: "+fr.Error)
	}
	if fr.EventLimitReached {
		lines = append(lines, fmt.Sprintf("    Events: %d (limit reached)", fr.EventCount))
	}
	lines = append(lines, "    Text:")
	lines = append(lines, indentBlock(orDefault(fr.TextContent, "(empty)"), "      "))

	if len(fr.ToolCalls) > 0 {
		lines = append(lines, "", "    Tool summary:")
		for _, call := range fr.ToolCalls {
			lines = append(lines, "      - "+orDefault(call.Summary, call.Tool))
		}
	}
	return lines
}

func formatTrajectory(traj *controlplane.Trajectory, includeEventData bool) []string {
	if traj == nil {
		return nil
	}
	suffix := ""
	if traj.HasMore {
		suffix = " (more available)"
	}
	lines := []string{"", "  Trajectory" + suffix + ":"}
	if len(traj.Events) == 0 {
		lines = append(lines, "    (no events)")
	} else {
		for _, e := range traj.Events {
			msg := ""
			if e.MessageID != "" {
				msg = " message=" + e.MessageID
			}
			summary := summarizeEvent(e)
			line := fmt.Sprintf("    [%s] %s%s", formatTimestamp(e.CreatedAt), e.Type, msg)
			if summary != "" {
				line += ": " + summary
			}
			lines = append(lines, line)
			if includeEventData {
				lines = append(lines, "      "+formatEventData(e.Data))
			}
		}
	}
	if traj.HasMore && traj.Cursor != "" {
		lines = append(lines, fmt.Sprintf("    More events available. Re-run with trajectoryCursor=%q.", traj.Cursor))
	}
	return lines
}

func formatRecentEvents(events []controlplane.Event) []string {
	if len(events) == 0 {
		return nil
	}
	lines := []string{"", "  Recent events:"}
	for _, e := range events {
		var raw any
		if e.Data != nil {
			if m := e.Data["message"]; m != nil {
				raw = m
			} else if cnt := e.Data["content"]; cnt != nil {
				raw = cnt
			}
		}
		if raw == nil {
			raw = e.Type
		}
		summary, ok := raw.(string)
		if !ok {
			b, _ := json.Marshal(raw)
			summary = string(b)
		}
		lines = append(lines, fmt.Sprintf("    [%s] %s: %s", formatTimestamp(e.CreatedAt), e.Type, sliceTo(summary, 120)))
	}
	return lines
}

func summarizeEvent(e controlplane.Event) string {
	data := e.Data
	if e.Type == "tool_call" {
		toolName := dataStr(data, "tool")
		if toolName == "" {
			toolName = dataStr(data, "name")
		}
		if toolName == "" {
			toolName = "tool"
		}
		callArgs, _ := data["args"].(map[string]any)
		for _, k := range []string{"command", "file_path", "pattern"} {
			if target := dataStr(callArgs, k); target != "" {
				return toolName + ": " + target
			}
		}
		return toolName
	}
	for _, k := range []string{"message", "content", "result", "error", "status", "state"} {
		if v, ok := data[k].(string); ok && v != "" {
			return truncate(v, 160)
		}
	}
	return ""
}

func formatEventData(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%v", data)
	}
	return string(b)
}

func dataStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// truncate mirrors the JS `len > max ? slice(0, max-3)+"..." : s` ellipsis form.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// sliceTo mirrors a plain JS String.slice(0, max) (no ellipsis).
func sliceTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
