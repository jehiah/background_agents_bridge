export default tool({
  name: "spawn-task",
  description:
    "Spawn a child coding task that runs in its own sandbox. The child inherits the current repository " +
    "and works independently. Returns immediately with a task ID — use get-task-status to check progress later. " +
    "Use this to parallelize work: delegate sub-tasks while you continue on the main task.",
  args: {
    title: z.string().describe("Short title describing the child task (shown in the UI)."),
    prompt: z
      .string()
      .describe("Detailed instructions for the child agent. Be specific — the child has no context beyond what you provide here."),
    model: z
      .string()
      .optional()
      .describe("Override the LLM model for the child. Must use 'provider/model' format (e.g. 'anthropic/claude-sonnet-4-6', 'openai/gpt-5.4'). Defaults to the parent's model."),
  },
  async execute(args) {
    return await runBridgeTool("spawn-task", args);
  },
});
