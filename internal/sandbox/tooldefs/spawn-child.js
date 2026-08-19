export default tool({
  name: "spawn-child",
  description:
    "Spawn a child coding session in a separate sandbox. Invoke only when the user's current request explicitly " +
    "asks for a 'child session' or 'child sessions'; otherwise work directly. Never infer permission or suggest " +
    "using one. The child inherits the repository, not conversation context, and continues running after the " +
    "parent responds. Returns a child ID; check status only when its result is needed.",
  args: {
    title: z.string().describe("Short title describing the child session (shown in the UI)."),
    prompt: z
      .string()
      .describe("Detailed instructions for the child agent. Be specific — the child has no context beyond what you provide here."),
    model: z
      .string()
      .optional()
      .describe("Override the LLM model for the child. Must use 'provider/model' format (e.g. 'anthropic/claude-sonnet-4-6', 'openai/gpt-5.4'). Defaults to the parent's model."),
  },
  async execute(args) {
    return await runBridgeTool("spawn-child", args);
  },
});
