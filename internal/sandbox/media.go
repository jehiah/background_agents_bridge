package sandbox

import (
	"context"
	"encoding/json"

	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// runImageUpload uploads a screenshot or video artifact via the control plane.
// The agent passes a file path plus metadata; controlplane.UploadMedia handles
// validation and the multipart request.
func runImageUpload(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	req := controlplane.UploadMediaRequest{
		FilePath:           argStr(args, "filePath"),
		ArtifactType:       argStr(args, "artifactType"),
		Caption:            argStr(args, "caption"),
		SourceURL:          argStr(args, "sourceUrl"),
		EndURL:             argStr(args, "endUrl"),
		FullPage:           argBool(args, "fullPage"),
		Annotated:          argBool(args, "annotated"),
		Viewport:           argStr(args, "viewport"),
		DurationMs:         argStr(args, "durationMs"),
		RecordingStartedAt: argStr(args, "recordingStartedAt"),
		RecordingEndedAt:   argStr(args, "recordingEndedAt"),
		Dimensions:         argStr(args, "dimensions"),
		Truncated:          argStr(args, "truncated"),
		HasAudio:           argStr(args, "hasAudio"),
	}

	result, err := c.UploadMedia(ctx, req)
	if err != nil {
		if e, ok := apiErr(err); ok {
			return "Failed to upload media: " + e.Display()
		}
		return "Failed to upload media: " + err.Error()
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out)
}
