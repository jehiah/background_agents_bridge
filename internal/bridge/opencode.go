package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// opencodeGet issues a GET to the OpenCode API with a per-request timeout.
func (b *AgentBridge) opencodeGet(ctx context.Context, path string) (*http.Response, error) {
	// The caller closes the body; cancel fires on close (see cancelOnClose).
	reqCtx, cancel := context.WithTimeout(ctx, opencodeRequestTimeout)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, b.opencodeBaseURL+path, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// Wrap the body so cancel fires when the caller closes it.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// createOpencodeSession creates a new OpenCode session and persists its ID.
func (b *AgentBridge) createOpencodeSession(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, opencodeRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, b.opencodeBaseURL+"/session", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("create session: HTTP %d: %s", resp.StatusCode, body)
	}

	var data struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	b.setOpencodeSessionID(data.ID)
	b.log.Info("opencode.session.ensure", "opencode_session_id", data.ID, "action", "created")
	b.saveSessionID()
	return nil
}

// opencodeSessionExists reports whether the given session still exists.
func (b *AgentBridge) opencodeSessionExists(ctx context.Context, id string) bool {
	resp, err := b.opencodeGet(ctx, "/session/"+id)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// requestOpencodeStop best-effort aborts the active OpenCode session, saving LLM
// compute when a prompt is cancelled or times out.
func (b *AgentBridge) requestOpencodeStop(ctx context.Context, reason string) bool {
	id := b.getOpencodeSessionID()
	if id == "" {
		return false
	}
	reqCtx, cancel := context.WithTimeout(ctx, opencodeRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, b.opencodeBaseURL+"/session/"+id+"/abort", nil)
	if err != nil {
		b.log.Warn("bridge.stop_request_error", "exc", err, "reason", reason)
		return false
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		b.log.Warn("bridge.stop_request_error", "exc", err, "reason", reason)
		return false
	}
	_ = resp.Body.Close()
	b.log.Info("bridge.stop_requested", "reason", reason)
	return true
}

// listMessages fetches the current message list for the active session. Each
// element is a generic map mirroring OpenCode's {info, parts} envelope.
func (b *AgentBridge) listMessages(ctx context.Context) ([]map[string]any, error) {
	id := b.getOpencodeSessionID()
	if id == "" {
		return nil, fmt.Errorf("no opencode session")
	}
	resp, err := b.opencodeGet(ctx, "/session/"+id+"/message")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list messages: HTTP %d", resp.StatusCode)
	}
	var msgs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
