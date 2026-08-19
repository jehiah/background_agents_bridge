export default tool({
  name: "cancel-task",
  description:
    "Cancel a running child task. Nested tasks are cancelled by default. The task's sandbox will be stopped and its status set to cancelled. Use get-task-status to find the task ID.",
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
