package sandbox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// toolDef is the OpenCode-facing definition of a tool: its name, agent
// description, and zod argument schema (as JS source). The bridge is the single
// source of truth — `bridge install` renders each of these into a .js file under
// ~/.config/opencode/tools/ that shells back into `bridge tool <name>`.
type toolDef struct {
	name        string
	description string
	argsBlock   string // zod source for the args object body (faithful to the upstream JS tools)
}

// toolDefs holds every generated tool. Names must match the toolImpls dispatch
// table in tools.go (enforced by a test).
var toolDefs = []toolDef{
	{
		name: "create-pull-request",
		description: "Create a pull request for the committed changes. DO NOT use 'gh' CLI - use this tool instead. " +
			"It handles git push and PR creation automatically with pre-configured authentication. Before committing, make " +
			"sure you are on a dedicated feature branch (e.g. `git checkout -b feature/short-description`); do NOT commit on " +
			"the base branch (main/master) or a detached HEAD, or PR creation will be rejected. You MUST provide a " +
			"descriptive title and body that explain what changes were made. Call this after committing your changes.",
		argsBlock: `    title: z
      .string()
      .describe("Title of the pull request. Should be concise and descriptive of the changes made."),
    body: z
      .string()
      .describe("Body/description of the pull request. Explain what changes were made and why. Use markdown formatting for clarity."),
    baseBranch: z
      .string()
      .optional()
      .describe("Target branch to merge into. Defaults to the repository's default branch (usually 'main')."),`,
	},
	{
		name: "spawn-task",
		description: "Spawn a child coding task that runs in its own sandbox. The child inherits the current repository " +
			"and works independently. Returns immediately with a task ID — use get-task-status to check progress later. " +
			"Use this to parallelize work: delegate sub-tasks while you continue on the main task.",
		argsBlock: `    title: z.string().describe("Short title describing the child task (shown in the UI)."),
    prompt: z
      .string()
      .describe("Detailed instructions for the child agent. Be specific — the child has no context beyond what you provide here."),
    model: z
      .string()
      .optional()
      .describe("Override the LLM model for the child. Must use 'provider/model' format (e.g. 'anthropic/claude-sonnet-4-6', 'openai/gpt-5.4'). Defaults to the parent's model."),`,
	},
	{
		name: "get-task-status",
		description: "Check child task status. Without a taskId, lists all child tasks with summary counts. With a taskId, " +
			"returns details. Set includeResponse to retrieve the child's final assistant response when available. Set " +
			"includeTrajectory for a paginated persisted event trajectory.",
		argsBlock: `    taskId: z
      .string()
      .optional()
      .describe("Specific task ID to get details for. Omit to list all child tasks."),
    includeResponse: z
      .boolean()
      .optional()
      .describe("Include the child's final assistant response when available."),
    includeTrajectory: z
      .boolean()
      .optional()
      .describe("Include a persisted child event trajectory page. Use includeResponse separately to include the final response."),
    trajectoryLimit: z
      .number()
      .int()
      .min(1)
      .max(1000)
      .optional()
      .describe("Maximum trajectory events to retrieve when includeTrajectory is true."),
    trajectoryCursor: z
      .string()
      .optional()
      .describe("Cursor returned by a previous trajectory page."),
    includeEventData: z
      .boolean()
      .optional()
      .describe("Include raw JSON payloads for each trajectory event."),`,
	},
	{
		name:        "cancel-task",
		description: "Cancel a running child task. The task's sandbox will be stopped and its status set to cancelled. Use get-task-status to find the task ID.",
		argsBlock:   `    taskId: z.string().describe("The task ID to cancel (from spawn-task or get-task-status)."),`,
	},
	{
		name: "slack-notify",
		description: "Post a message to a Slack channel that the user has authorized. Use this only when the user has explicitly " +
			"asked you to notify Slack — this is an externally-visible action that other humans will see. The user must tell you " +
			"which channel; do not guess. The bot must already be invited to the channel; if you get channel_not_found_or_forbidden, " +
			"ask the user to invite the bot. Plain text + Slack mrkdwn formatting only (bold *...*, italic _..._, inline code `...`, " +
			"fenced blocks, lists, blockquotes). The server attaches the attribution footer and View Session button — do not fabricate them.",
		argsBlock: `    channel: z
      .string()
      .describe("Target channel as either a channel ID (e.g. C01ABC) or the channel name as the user said it (e.g. ops or #ops). Passed verbatim to Slack — no resolution or lookup."),
    text: z
      .string()
      .describe("Message body. Plain text + Slack mrkdwn. No interactive elements. Broadcast mentions are stripped server-side."),
    thread_ts: z
      .string()
      .optional()
      .describe("Optional Slack thread timestamp to reply within an existing thread. Same channel-membership rules apply."),
    reason: z
      .string()
      .optional()
      .describe("Optional short note explaining why you are posting. Recorded server-side for audit; not shown in Slack."),`,
	},
	{
		name: "image-upload",
		description: "Upload a screenshot or video artifact (e.g. a browser screenshot or screen recording) to the session. " +
			"Supports .png, .jpg, .jpeg, .webp images and .mp4 video. Provide the absolute path to the file; for video set " +
			"artifactType to 'video' and supply the recording metadata.",
		argsBlock: `    filePath: z.string().describe("Absolute path to the media file to upload (.png, .jpg, .jpeg, .webp, or .mp4)."),
    artifactType: z
      .enum(["screenshot", "video"])
      .optional()
      .describe("Artifact kind. Defaults to 'screenshot'. MP4 files must use 'video'."),
    caption: z.string().optional().describe("Caption for the artifact. Required for video."),
    sourceUrl: z.string().optional().describe("URL the artifact was captured from."),
    endUrl: z.string().optional().describe("For video: the URL at the end of the recording."),
    fullPage: z.boolean().optional().describe("Screenshot only: whether this is a full-page capture."),
    annotated: z.boolean().optional().describe("Screenshot only: whether the image has annotations."),
    viewport: z.string().optional().describe("Screenshot only: JSON viewport, e.g. '{\"width\":1280,\"height\":720}'."),
    durationMs: z.string().optional().describe("Video only: recording duration in milliseconds."),
    recordingStartedAt: z.string().optional().describe("Video only: epoch-ms recording start."),
    recordingEndedAt: z.string().optional().describe("Video only: epoch-ms recording end."),
    dimensions: z.string().optional().describe("Video only: JSON dimensions, e.g. '{\"width\":1280,\"height\":720}'."),
    truncated: z.string().optional().describe("Video only: 'true' if the recording was truncated."),
    hasAudio: z.string().optional().describe("Video only: 'true' if the recording has audio. Defaults to 'false'."),`,
	},
}

// toolJSTemplate renders a self-contained OpenCode tool that proxies to the
// bridge CLI. %[1]s=BRIDGE_BIN(JSON), %[2]s=name(JSON), %[3]s=description(JSON),
// %[4]s=args block.
const toolJSTemplate = `// Generated by ` + "`bridge install`" + ` — do not edit by hand.
// This thin tool definition proxies to the bridge CLI, which authenticates and
// calls the control plane. The bridge binary is the source of truth.
import { tool } from "@opencode-ai/plugin";
import { z } from "zod";
import { spawn } from "node:child_process";

const BRIDGE_BIN = %[1]s;

function runBridgeTool(name, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(BRIDGE_BIN, ["tool", name], { stdio: ["pipe", "pipe", "inherit"] });
    let stdout = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve(stdout.replace(/\n$/, ""));
      else reject(new Error(` + "`bridge tool ${name} exited with code ${code}`" + `));
    });
    child.stdin.write(JSON.stringify(args ?? {}));
    child.stdin.end();
  });
}

export default tool({
  name: %[2]s,
  description: %[3]s,
  args: {
%[4]s
  },
  async execute(args) {
    return await runBridgeTool(%[2]s, args);
  },
});
`

// generateToolJS renders the .js source for one tool, embedding the absolute
// path to the bridge binary so the shim invokes the same binary that wrote it.
func generateToolJS(def toolDef, exePath string) string {
	return fmt.Sprintf(toolJSTemplate,
		jsString(exePath),
		jsString(def.name),
		jsString(def.description),
		def.argsBlock,
	)
}

// jsString returns a JS/JSON string literal for s.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// toolDefNames returns the generated tool names in stable order.
func toolDefNames() []string {
	names := make([]string, 0, len(toolDefs))
	for _, d := range toolDefs {
		names = append(names, d.name)
	}
	sort.Strings(names)
	return names
}

// fileNameFor returns the .js filename for a tool.
func fileNameFor(name string) string {
	return strings.TrimSuffix(name, ".js") + ".js"
}
