export default tool({
  name: "cancel-child",
  description:
    "Cancel a running child session only when the user requests it or the work is clearly obsolete. Do not " +
    "cancel because a child is slow, the parent is finished, or as cleanup. Nested children are cancelled by " +
    "default. The child's sandbox will be stopped and its status set to cancelled.",
  args: {
    childId: z.string().describe("The child ID to cancel (from spawn-child or get-child-status)."),
    cancelNested: z
      .boolean()
      .default(true)
      .describe("Whether to also cancel all nested child sessions. Defaults to true."),
  },
  async execute(args) {
    return await runBridgeTool("cancel-child", args);
  },
});
