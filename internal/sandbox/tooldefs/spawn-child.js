export default tool({
  name: "spawn-child",
  description:
    "Use this tool ONLY when the user's current request explicitly and affirmatively asks to create a " +
    "'child session' or 'child sessions' in a separate sandbox. " +
    "DO NOT use it for 'sub-agent', 'subagent', 'sub agent', 'sub-task', 'subtask', or Task tool requests; " +
    "use the Task tool for those in-process delegations instead. " +
    "Merely mentioning, comparing, or rejecting child sessions does not authorize this tool. " +
    "Never infer permission or suggest creating a child session. The child inherits the repository, not " +
    "conversation context, and continues running after the parent responds. Returns a child ID; check status " +
    "only when its result is needed.",
  args: {
    title: z.string().describe("Short title describing the child session (shown in the UI)."),
    prompt: z
      .string()
      .describe("Detailed instructions for the child agent. Be specific — the child has no context beyond what you provide here."),
    model: z
      .string()
      .optional()
      .describe("Override the LLM model for the child. Must use 'provider/model' format (e.g. 'anthropic/claude-sonnet-4-6', 'openai/gpt-5.4'). Defaults to the parent's model."),
    reasoning: z
      .string()
      .optional()
      .describe(
        "Overrides the reasoning effort for the child. Valid values depend on the model and may include " +
        "'none', 'low', 'medium', 'high', 'xhigh', and 'max'. Use 'xhigh', not 'x-high'. Defaults to the " +
        "parent's reasoning effort when the selected model supports it."
      ),
  },
  async execute(args) {
    return await runBridgeTool("spawn-child", args);
  },
});
