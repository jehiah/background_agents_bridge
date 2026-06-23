export default tool({
  name: "create-pull-request",
  description:
    "Create a pull request for the committed changes in the session's repository at __BRIDGE_DEFAULT_REPO_DIR__. " +
    "DO NOT use 'gh' CLI - use this tool instead. " +
    "It handles git push and PR creation automatically with pre-configured authentication. Before committing, make " +
    "sure you are on a dedicated feature branch (e.g. `git checkout -b feature/short-description`); do NOT commit on " +
    "the base branch (main/master) or a detached HEAD, or PR creation will be rejected. You MUST provide a " +
    "descriptive title and body that explain what changes were made. Call this after committing your changes.",
  args: {
    title: z
      .string()
      .describe("Title of the pull request. Should be concise and descriptive of the changes made."),
    body: z
      .string()
      .describe("Body/description of the pull request. Explain what changes were made and why. Use markdown formatting for clarity."),
    baseBranch: z
      .string()
      .optional()
      .describe("Target branch to merge into. Defaults to the repository's default branch (usually 'main')."),
  },
  async execute(args) {
    return await runBridgeTool("create-pull-request", args);
  },
});
