package controlplane

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mimeByExt maps supported media extensions to content types.
var mimeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".mp4":  "video/mp4",
}

// UploadMediaRequest describes a screenshot or video artifact to upload. FilePath
// is read from disk and posted as multipart form data; the remaining fields are
// optional metadata. ArtifactType defaults to "screenshot"; video uploads
// require the recording metadata fields.
type UploadMediaRequest struct {
	FilePath     string
	ArtifactType string // "screenshot" (default) or "video"

	Caption   string
	SourceURL string
	EndURL    string

	// Screenshot-only.
	FullPage  bool
	Annotated bool
	Viewport  string

	// Video-only.
	DurationMs         string
	RecordingStartedAt string
	RecordingEndedAt   string
	Dimensions         string
	Truncated          string
	HasAudio           string
}

// MediaResult identifies the stored artifact.
type MediaResult struct {
	ArtifactID string `json:"artifactId"`
	ObjectKey  string `json:"objectKey"`
}

// UploadMedia uploads a media artifact (POST /media). It validates the file type
// and required metadata before building the multipart request.
func (c *Client) UploadMedia(ctx context.Context, req UploadMediaRequest) (MediaResult, error) {
	if req.FilePath == "" {
		return MediaResult{}, fmt.Errorf("filePath is required")
	}
	path, err := filepath.Abs(req.FilePath)
	if err != nil {
		return MediaResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return MediaResult{}, err
	}
	if info.IsDir() {
		return MediaResult{}, fmt.Errorf("%s is not a file", req.FilePath)
	}

	mimeType, ok := mimeByExt[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return MediaResult{}, fmt.Errorf("unsupported file type: only .png, .jpg, .jpeg, .webp, and .mp4 are supported")
	}

	artifactType := req.ArtifactType
	if artifactType == "" {
		artifactType = "screenshot"
	}
	if mimeType == "video/mp4" && artifactType != "video" {
		return MediaResult{}, fmt.Errorf("MP4 files must be uploaded with artifactType=video")
	}
	if artifactType == "video" {
		if missing := req.missingVideoField(); missing != "" {
			return MediaResult{}, fmt.Errorf("%s is required for video uploads", missing)
		}
	}

	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return MediaResult{}, err
	}

	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	if err := writeMediaForm(w, filepath.Base(path), mimeType, fileBytes, artifactType, req); err != nil {
		return MediaResult{}, err
	}
	if err := w.Close(); err != nil {
		return MediaResult{}, err
	}

	resp, err := c.do(ctx, "POST", "/media", buf, w.FormDataContentType())
	if err != nil {
		return MediaResult{}, err
	}
	var out MediaResult
	if err := decode(resp, &out); err != nil {
		return MediaResult{}, err
	}
	return out, nil
}

func (req UploadMediaRequest) missingVideoField() string {
	for name, v := range map[string]string{
		"caption":            req.Caption,
		"durationMs":         req.DurationMs,
		"recordingStartedAt": req.RecordingStartedAt,
		"recordingEndedAt":   req.RecordingEndedAt,
		"dimensions":         req.Dimensions,
		"truncated":          req.Truncated,
	} {
		if v == "" {
			return name
		}
	}
	return ""
}

func writeMediaForm(w *multipart.Writer, filename, mimeType string, fileBytes []byte, artifactType string, req UploadMediaRequest) error {
	part, err := newFilePart(w, "file", filename, mimeType)
	if err != nil {
		return err
	}
	if _, err := part.Write(fileBytes); err != nil {
		return err
	}
	if err := w.WriteField("artifactType", artifactType); err != nil {
		return err
	}

	if artifactType == "video" {
		required := map[string]string{
			"caption":            req.Caption,
			"durationMs":         req.DurationMs,
			"recordingStartedAt": req.RecordingStartedAt,
			"recordingEndedAt":   req.RecordingEndedAt,
			"dimensions":         req.Dimensions,
			"truncated":          req.Truncated,
		}
		for _, name := range []string{"caption", "durationMs", "recordingStartedAt", "recordingEndedAt", "dimensions", "truncated"} {
			if err := w.WriteField(name, required[name]); err != nil {
				return err
			}
		}
		hasAudio := req.HasAudio
		if hasAudio == "" {
			hasAudio = "false"
		}
		if err := w.WriteField("hasAudio", hasAudio); err != nil {
			return err
		}
		return writeOptional(w, map[string]string{"sourceUrl": req.SourceURL, "endUrl": req.EndURL})
	}

	// screenshot
	if err := writeOptional(w, map[string]string{
		"caption":   req.Caption,
		"sourceUrl": req.SourceURL,
		"viewport":  req.Viewport,
	}); err != nil {
		return err
	}
	if req.FullPage {
		if err := w.WriteField("fullPage", "true"); err != nil {
			return err
		}
	}
	if req.Annotated {
		if err := w.WriteField("annotated", "true"); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeOptional(w *multipart.Writer, fields map[string]string) error {
	for _, name := range sortedKeys(fields) {
		if v := fields[name]; v != "" {
			if err := w.WriteField(name, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// newFilePart creates a form-data file part with an explicit Content-Type
// (multipart.CreateFormFile hardcodes application/octet-stream).
func newFilePart(w *multipart.Writer, field, filename, contentType string) (io.Writer, error) {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	h.Set("Content-Type", contentType)
	return w.CreatePart(h)
}
