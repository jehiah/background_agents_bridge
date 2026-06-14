export default tool({
  name: "slack-notify",
  description:
    "Post a message to a Slack channel that the user has authorized. Use this only when the user has explicitly " +
    "asked you to notify Slack — this is an externally-visible action that other humans will see. The user must tell you " +
    "which channel; do not guess. The bot must already be invited to the channel; if you get channel_not_found_or_forbidden, " +
    "ask the user to invite the bot. Plain text + Slack mrkdwn formatting only (bold *...*, italic _..._, inline code `...`, " +
    "fenced blocks, lists, blockquotes). The server attaches the attribution footer and View Session button — do not fabricate them.",
  args: {
    channel: z
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
      .describe("Optional short note explaining why you are posting. Recorded server-side for audit; not shown in Slack."),
  },
  async execute(args) {
    return await runBridgeTool("slack-notify", args);
  },
});
