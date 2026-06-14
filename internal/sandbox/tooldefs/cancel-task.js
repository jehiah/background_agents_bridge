export default tool({
  name: "cancel-task",
  description:
    "Cancel a running child task. The task's sandbox will be stopped and its status set to cancelled. Use get-task-status to find the task ID.",
  args: {
    taskId: z.string().describe("The task ID to cancel (from spawn-task or get-task-status)."),
  },
  async execute(args) {
    return await runBridgeTool("cancel-task", args);
  },
});
