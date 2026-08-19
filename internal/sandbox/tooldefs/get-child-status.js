export default tool({
  name: "get-child-status",
  description:
    "Check child session status only when its result is needed; do not poll repeatedly. Without a childId, " +
    "lists all child sessions with summary counts. With a childId, " +
    "returns details. Set includeResponse to retrieve the child's final assistant response when available. Set " +
    "includeTrajectory for a paginated persisted event trajectory.",
  args: {
    childId: z
      .string()
      .optional()
      .describe("Specific child ID to get details for. Omit to list all child sessions."),
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
    return await runBridgeTool("get-child-status", args);
  },
});
