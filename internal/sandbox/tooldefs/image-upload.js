export default tool({
  name: "image-upload",
  description:
    "Upload a screenshot or video artifact (e.g. a browser screenshot or screen recording) to the session. " +
    "Supports .png, .jpg, .jpeg, .webp images and .mp4 video. Provide the absolute path to the file; for video set " +
    "artifactType to 'video' and supply the recording metadata.",
  args: {
    filePath: z.string().describe("Absolute path to the media file to upload (.png, .jpg, .jpeg, .webp, or .mp4)."),
    artifactType: z
      .enum(["screenshot", "video"])
      .optional()
      .describe("Artifact kind. Defaults to 'screenshot'. MP4 files must use 'video'."),
    caption: z.string().optional().describe("Caption for the artifact. Required for video."),
    sourceUrl: z.string().optional().describe("URL the artifact was captured from."),
    endUrl: z.string().optional().describe("For video: the URL at the end of the recording."),
    fullPage: z.boolean().optional().describe("Screenshot only: whether this is a full-page capture."),
    annotated: z.boolean().optional().describe("Screenshot only: whether the image has annotations."),
    viewport: z.string().optional().describe("Screenshot only: JSON viewport, e.g. '{\"width\":1280,\"height\":720}'."),
    durationMs: z.string().optional().describe("Video only: recording duration in milliseconds."),
    recordingStartedAt: z.string().optional().describe("Video only: epoch-ms recording start."),
    recordingEndedAt: z.string().optional().describe("Video only: epoch-ms recording end."),
    dimensions: z.string().optional().describe("Video only: JSON dimensions, e.g. '{\"width\":1280,\"height\":720}'."),
    truncated: z.string().optional().describe("Video only: 'true' if the recording was truncated."),
    hasAudio: z.string().optional().describe("Video only: 'true' if the recording has audio. Defaults to 'false'."),
  },
  async execute(args) {
    return await runBridgeTool("image-upload", args);
  },
});
