package bridge

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Session image attachments: the control plane resolves image metadata and the
// bridge hydrates each image (downloads bytes, base64-encodes) into an OpenCode
// file part so the agent can see it. Port of attachment_processor.py +
// _handle_prompt's attachment handling (upstream #1019).
const (
	maxSessionAttachmentsPerMessage = 6
	maxAttachmentBytes              = 10 * 1024 * 1024
	attachmentDownloadTimeout       = 120 * time.Second
	attachmentDownloadConcurrency   = 2
)

// attachmentImageMimeTypes is the allowlist of image MIME types the control
// plane resolves and OpenCode accepts as file parts.
var attachmentImageMimeTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// attachmentIDRE bounds an attachment id to a safe, path-segment-only token so a
// crafted id cannot escape the session attachments URL.
var attachmentIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

// resolvedAttachment is trusted image metadata after boundary validation.
type resolvedAttachment struct {
	attachmentID string
	name         string
	mimeType     string
}

// hydratedAttachment is a downloaded, base64-encoded image ready to become an
// OpenCode file part.
type hydratedAttachment struct {
	name     string
	mimeType string
	content  string // base64-encoded bytes
}

// parseSessionImageAttachments validates the untyped WebSocket attachment list,
// returning the accepted entries and the count of rejected ones. It enforces the
// per-message cap, the id/name/mime constraints, and treats a non-list value as
// a single rejection. Mirrors parse_session_image_attachments.
func parseSessionImageAttachments(value any) ([]resolvedAttachment, int) {
	if value == nil {
		return nil, 0
	}
	list, ok := value.([]any)
	if !ok {
		return nil, 1
	}

	rejected := 0
	if len(list) > maxSessionAttachmentsPerMessage {
		rejected = len(list) - maxSessionAttachmentsPerMessage
		list = list[:maxSessionAttachmentsPerMessage]
	}

	var parsed []resolvedAttachment
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			rejected++
			continue
		}
		id, _ := m["attachmentId"].(string)
		name, _ := m["name"].(string)
		mime, _ := m["mimeType"].(string)
		if !attachmentIDRE.MatchString(id) ||
			len(name) < 1 || len(name) > 255 ||
			!attachmentImageMimeTypes[mime] {
			rejected++
			continue
		}
		parsed = append(parsed, resolvedAttachment{attachmentID: id, name: name, mimeType: mime})
	}
	return parsed, rejected
}

// processAttachments hydrates resolved attachments with bounded concurrency,
// preserving order and dropping any that fail to download (a per-attachment
// media warning is surfaced to the user). Mirrors AttachmentProcessor.process.
func (b *AgentBridge) processAttachments(ctx context.Context, atts []resolvedAttachment) []hydratedAttachment {
	if len(atts) == 0 {
		return nil
	}

	results := make([]hydratedAttachment, len(atts))
	ok := make([]bool, len(atts))
	sem := make(chan struct{}, attachmentDownloadConcurrency)
	var wg sync.WaitGroup

	for i, att := range atts {
		wg.Add(1)
		go func(i int, att resolvedAttachment) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := b.downloadAttachment(ctx, att.attachmentID)
			if err != nil {
				b.log.Warn("attachments.fetch_failed",
					"attachment_name", att.name, "attachment_id", att.attachmentID, "exc", err)
				b.sendMediaWarning(fmt.Sprintf("Attachment %s could not be fetched and was skipped.", att.name))
				return
			}
			b.log.Info("attachments.fetched",
				"attachment_name", att.name, "attachment_id", att.attachmentID, "size_bytes", len(data))
			results[i] = hydratedAttachment{
				name:     att.name,
				mimeType: att.mimeType,
				content:  base64.StdEncoding.EncodeToString(data),
			}
			ok[i] = true
		}(i, att)
	}
	wg.Wait()

	out := make([]hydratedAttachment, 0, len(atts))
	for i := range results {
		if ok[i] {
			out = append(out, results[i])
		}
	}
	return out
}

// downloadAttachment fetches an attachment's bytes from the control plane,
// enforcing a hard size cap and refusing to follow redirects (a redirect must
// not carry the bearer token to another host). Mirrors _download_attachment_bytes.
func (b *AgentBridge) downloadAttachment(ctx context.Context, attachmentID string) ([]byte, error) {
	if !attachmentIDRE.MatchString(attachmentID) {
		return nil, fmt.Errorf("invalid attachment id")
	}

	cctx, cancel := context.WithTimeout(ctx, attachmentDownloadTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/sessions/%s/attachments/%s",
		strings.TrimRight(b.controlPlaneURL, "/"), b.sessionID, attachmentID)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.authToken)

	// Reuse the bridge transport (connection pooling) but never follow
	// redirects: a 3xx is returned as-is and rejected by the status check below.
	client := &http.Client{
		Transport:     b.httpClient.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	// Read one byte past the cap so an oversized body is detected, not truncated.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("attachment exceeds %d bytes", maxAttachmentBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty attachment")
	}
	return data, nil
}

// buildFileParts converts hydrated attachments into OpenCode file parts (a data:
// URL per image). Mirrors build_file_parts.
func buildFileParts(atts []hydratedAttachment) []map[string]any {
	parts := make([]map[string]any, 0, len(atts))
	for _, a := range atts {
		parts = append(parts, map[string]any{
			"type":     "file",
			"mime":     a.mimeType,
			"filename": a.name,
			"url":      "data:" + a.mimeType + ";base64," + a.content,
		})
	}
	return parts
}

// sendMediaWarning surfaces a non-fatal media-handling failure to the user
// timeline. Mirrors _send_media_warning.
func (b *AgentBridge) sendMediaWarning(message string) {
	b.sendEvent(warningEvent(map[string]any{"scope": "media", "message": message}))
}
