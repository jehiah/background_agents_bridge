export default tool({
  name: "get-task-status",
  description:
    "Check child task status. Without a taskId, lists all child tasks with summary counts. With a taskId, " +
    "returns details. Set includeResponse to retrieve the child's final assistant response when available. Set " +
    "includeTrajectory for a paginated persisted event trajectory.",
  args: {
    taskId: z
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
      .describe("Include raw JSON payloads for each trajectory event."),
  },
  async execute(args) {
    return await runBridgeTool("get-task-status", args);
  },
});
