// Package controlplane is the sandbox-side client for the background-agents
// control plane. It exposes one Go method per control-plane endpoint, each
// taking/returning named request and response structs — the wire contract, not
// HTTP plumbing. Callers never see *http.Response, status codes (except via the
// typed *APIError), or request building.
//
// Every request is session-scoped and bearer-authenticated: paths are rooted at
// {ControlPlaneURL}/sessions/{SessionID}, matching the control-plane endpoint
// allowlist. These are short request/response exchanges, distinct from the
// streaming connect WebSocket in package bridge.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds a single control-plane call.
const requestTimeout = 60 * time.Second

// Client talks to the control plane for one session.
type Client struct {
	baseURL   string // control-plane URL, no trailing slash
	token     string
	sessionID string
	http      *http.Client
}

// New builds a Client, erroring if any piece needed to address the control plane
// is missing.
func New(controlPlaneURL, token, sessionID string) (*Client, error) {
	if controlPlaneURL == "" {
		return nil, fmt.Errorf("control plane URL not configured (set CONTROL_PLANE_URL)")
	}
	if token == "" {
		return nil, fmt.Errorf("sandbox auth token not configured (set SANDBOX_AUTH_TOKEN)")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session id not configured (set SESSION_ID or SESSION_CONFIG)")
	}
	return &Client{
		baseURL:   strings.TrimRight(controlPlaneURL, "/"),
		token:     token,
		sessionID: sessionID,
		http:      &http.Client{Timeout: requestTimeout},
	}, nil
}

// APIError is returned for any non-2xx control-plane response. Code and Message
// come from the JSON body's "error"/"message" fields when present (some
// endpoints put a reason code in "error", others a human message); Body is the
// raw payload as a fallback.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RetryAfter *float64
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Display())
}

// Display returns the most human-readable detail available.
func (e *APIError) Display() string {
	switch {
	case e.Message != "":
		return e.Message
	case e.Code != "":
		return e.Code
	default:
		return e.Body
	}
}

// --- internal transport ------------------------------------------------------

func (c *Client) sessionURL(path string) string {
	return c.baseURL + "/sessions/" + c.sessionID + path
}

// doJSON sends an optional JSON body and decodes a JSON response into out (which
// may be nil to discard). Non-2xx responses become an *APIError.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	contentType := ""
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
		contentType = "application/json"
	}
	resp, err := c.do(ctx, method, path, rdr, contentType)
	if err != nil {
		return err
	}
	return decode(resp, out)
}

// do issues a single authenticated request.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.sessionURL(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

// decode reads resp, returning an *APIError for non-2xx, otherwise unmarshalling
// the body into out (skipped when out is nil or the body is empty).
func decode(resp *http.Response, out any) error {
	body := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp.StatusCode, body)
	}
	if out == nil || body == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("decode %T: %w", out, err)
	}
	return nil
}

// apiError parses a non-2xx body into an *APIError.
func apiError(status int, body string) *APIError {
	e := &APIError{StatusCode: status, Body: body}
	var parsed struct {
		Error      string   `json:"error"`
		Message    string   `json:"message"`
		RetryAfter *float64 `json:"retryAfter"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil {
		e.Code = parsed.Error
		e.Message = parsed.Message
		e.RetryAfter = parsed.RetryAfter
	}
	return e
}

// readBody reads and closes resp.Body, bounding the read.
func readBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return strings.TrimSpace(string(b))
}
