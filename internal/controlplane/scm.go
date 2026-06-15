package controlplane

import (
	"context"
	"fmt"
	"io"
	"net/url"
)

// Credentials is a git credential pair brokered for a single host.
type Credentials struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	ExpiresAtEpochMs int64  `json:"expires_at_epoch_ms,omitempty"`
}

// SCMCredentials brokers a fresh git credential from the control plane for the
// given host (POST /scm-credentials?host=<host>). The control plane responds
// with {username, password, expires_at_epoch_ms} or {token}; the username
// defaults to "x-access-token" and the password falls back to the token.
func (c *Client) SCMCredentials(ctx context.Context, host string) (Credentials, error) {
	var resp struct {
		Username         string `json:"username"`
		Password         string `json:"password"`
		Token            string `json:"token"`
		ExpiresAtEpochMs int64  `json:"expires_at_epoch_ms"`
	}
	if err := c.doJSON(ctx, "POST", "/scm-credentials?host="+url.QueryEscape(host), nil, &resp); err != nil {
		return Credentials{}, err
	}
	creds := Credentials{Username: resp.Username, Password: resp.Password, ExpiresAtEpochMs: resp.ExpiresAtEpochMs}
	if creds.Username == "" {
		creds.Username = "x-access-token"
	}
	if creds.Password == "" {
		creds.Password = resp.Token
	}
	return creds, nil
}

// WriteTo emits the credential as git credential-helper reply lines
// (username=...\npassword=...\n). The password line is omitted when empty.
// The signature matches io.WriterTo.
func (c Credentials) WriteTo(w io.Writer) (int64, error) {
	n, err := fmt.Fprintf(w, "username=%s\n", c.Username)
	total := int64(n)
	if err != nil {
		return total, err
	}
	if c.Password != "" {
		n, err = fmt.Fprintf(w, "password=%s\n", c.Password)
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
