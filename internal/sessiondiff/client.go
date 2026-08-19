package sessiondiff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultRefreshTimeout bounds both a collection and one control-plane call,
// mirroring DEFAULT_REFRESH_TIMEOUT_SECONDS.
const DefaultRefreshTimeout = 60 * time.Second

// Outcome is the control plane's disposition of a diff endpoint call.
type Outcome int

const (
	// OutcomeAccepted means the control plane stored the payload.
	OutcomeAccepted Outcome = iota
	// OutcomeUnsupported means this control plane predates the diff viewer;
	// the caller stops uploading for the rest of the sandbox's life.
	OutcomeUnsupported
)

// Uploader is the control-plane surface the worker needs; tests substitute it.
type Uploader interface {
	UploadBundle(ctx context.Context, bundle *Bundle) (Outcome, error)
	ReportFailure(ctx context.Context, message string) (Outcome, error)
}

// Client talks to the control plane's session diff endpoints. It is separate
// from internal/controlplane because the payload is a pre-encoded byte slice
// whose exact size the capture limits are measured against.
type Client struct {
	diffURL string
	token   string
	timeout time.Duration

	// HTTPClient issues the requests; tests replace it.
	HTTPClient *http.Client
}

// NewClient builds a Client for one session.
func NewClient(controlPlaneURL, sessionID, sandboxToken string) *Client {
	base := strings.TrimRight(controlPlaneURL, "/")
	return &Client{
		diffURL:    base + "/sessions/" + url.PathEscape(sessionID) + "/diff",
		token:      sandboxToken,
		timeout:    DefaultRefreshTimeout,
		HTTPClient: &http.Client{},
	}
}

// UploadBundle PUTs the encoded bundle, replacing whatever the control plane
// holds for the session.
func (c *Client) UploadBundle(ctx context.Context, bundle *Bundle) (Outcome, error) {
	return c.send(ctx, http.MethodPut, c.diffURL, encodeBundle(bundle))
}

// ReportFailure records that this refresh could not produce a bundle, so the
// viewer shows a failure rather than a stale diff.
func (c *Client) ReportFailure(ctx context.Context, message string) (Outcome, error) {
	body, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		return OutcomeAccepted, err
	}
	return c.send(ctx, http.MethodPost, c.diffURL+"/failure", body)
}

func (c *Client) send(ctx context.Context, method, url string, body []byte) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return OutcomeAccepted, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return OutcomeAccepted, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return OutcomeUnsupported, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OutcomeAccepted, fmt.Errorf("session diff request failed: HTTP %d: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return OutcomeAccepted, nil
}
