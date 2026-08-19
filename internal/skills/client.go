package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fetch tuning, mirroring MANAGED_SKILLS_* upstream.
const (
	fetchTimeout    = 15 * time.Second
	requestAttempts = 3
	retryBase       = 250 * time.Millisecond
)

// Client is the sandbox-only client for the managed-skills endpoint. It is
// deliberately separate from internal/controlplane: the response is untrusted
// bytes read under a hard size cap, never decoded by the transport.
type Client struct {
	baseURL   string // control-plane URL, no trailing slash
	sessionID string
	token     string

	// HTTPClient issues the request; tests replace it. The per-attempt deadline
	// comes from the request context, not from this client's Timeout.
	HTTPClient *http.Client
}

// NewClient builds a Client for one session.
func NewClient(controlPlaneURL, sessionID, sandboxToken string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(controlPlaneURL, "/"),
		sessionID:  sessionID,
		token:      sandboxToken,
		HTTPClient: &http.Client{},
	}
}

// skillsURL is the session-bound endpoint. The session id is escaped as a
// single path segment so it cannot widen the request to another path.
func (c *Client) skillsURL() string {
	return c.baseURL + "/sessions/" + url.PathEscape(c.sessionID) + "/sandbox-skills"
}

// FetchInstallation returns the raw installation DTO bytes, retrying transient
// failures with exponential backoff and refusing a response past the size cap.
func (c *Client) FetchInstallation(ctx context.Context) ([]byte, error) {
	var lastErr error
	for attempt := range requestAttempts {
		raw, err := c.fetchOnce(ctx)
		if err == nil {
			return raw, nil
		}
		// A response that is too large (or any other local verdict) is final.
		if terminal, ok := errors.AsType[*Error](err); ok {
			return nil, terminal
		}
		lastErr = err
		if !retryable(err) || attempt == requestAttempts-1 {
			break
		}
		select {
		case <-time.After(retryBase << attempt):
		case <-ctx.Done():
			return nil, wrapError("fetch_failed", ctx.Err(), "failed to fetch managed skills: %v", ctx.Err())
		}
	}
	return nil, wrapError("fetch_failed", lastErr, "failed to fetch managed skills: %v", lastErr)
}

// fetchOnce performs a single bounded request. A non-2xx status or transport
// failure returns a plain error (retryable is consulted); a body past the cap
// returns a terminal *Error.
func (c *Client) fetchOnce(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.skillsURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &statusError{StatusCode: resp.StatusCode}
	}
	// Read one byte past the cap so an oversized body is detected rather than
	// silently truncated.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxManagedSkillResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxManagedSkillResponseBytes {
		return nil, newError("installation_too_large", "managed skills installation exceeds the size limit")
	}
	return raw, nil
}

// statusError is a non-2xx response.
type statusError struct{ StatusCode int }

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// retryable reports whether err is worth another attempt: transport errors
// always, and the status codes that indicate a transient control-plane state.
func retryable(err error) bool {
	status, ok := errors.AsType[*statusError](err)
	if !ok {
		return true
	}
	return status.StatusCode == http.StatusRequestTimeout ||
		status.StatusCode == http.StatusTooManyRequests ||
		status.StatusCode >= 500
}
