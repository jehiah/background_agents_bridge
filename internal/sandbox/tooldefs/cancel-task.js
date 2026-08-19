export default tool({
  name: "cancel-task",
  description:
    "Cancel a running child task only when the user requests it or the work is clearly obsolete. Do not cancel " +
    "because a task is slow, the parent is finished, or as cleanup. Nested tasks are cancelled by default. The " +
    "task's sandbox will be stopped and its status set to cancelled.",
  args: {
    taskId: z.string().describe("The task ID to cancel (from spawn-task or get-task-status)."),
    cancelNested: z
      .boolean()
      .default(true)
      .describe("Whether to also cancel all nested child tasks. Defaults to true."),
  },
  async execute(args) {
    return await runBridgeTool("cancel-task", args);
  },
});
