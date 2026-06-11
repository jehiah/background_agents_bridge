// Package gcpmeta is a tiny, dependency-free client for the Google Compute
// Engine metadata server. It exists so the bridge can resolve empty CLI flags
// from instance attributes without pulling in cloud.google.com/go.
package gcpmeta

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// hostEnv overrides the metadata host (host or host:port, no scheme). This
	// is the same variable the official Google libraries honor.
	hostEnv = "GCE_METADATA_HOST"

	defaultHost  = "metadata.google.internal"
	flavorHeader = "Metadata-Flavor"
	flavorValue  = "Google"

	requestTimeout = 2 * time.Second
)

// Client talks to the GCE metadata server over plain HTTP.
type Client struct {
	host       string
	httpClient *http.Client
}

// NewClient returns a Client targeting GCE_METADATA_HOST if set, else the
// well-known metadata host.
func NewClient() *Client {
	host := os.Getenv(hostEnv)
	if host == "" {
		host = defaultHost
	}
	return &Client{
		host:       host,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// InstanceAttribute fetches instance/attributes/<key>. A missing attribute
// returns ("", nil); transport failures and unexpected statuses return an error.
func (c *Client) InstanceAttribute(ctx context.Context, key string) (string, error) {
	return c.get(ctx, "instance/attributes/"+key)
}

func (c *Client) get(ctx context.Context, suffix string) (string, error) {
	url := "http://" + c.host + "/computeMetadata/v1/" + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(flavorHeader, flavorValue)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	// Guard against captive portals / proxies impersonating the metadata server.
	if resp.Header.Get(flavorHeader) != flavorValue {
		return "", fmt.Errorf("metadata %s: missing %s response header", suffix, flavorHeader)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", nil // attribute not set
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata %s: HTTP %d", suffix, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}
