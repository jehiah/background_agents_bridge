package controlplane

import (
	"context"
	"net/url"
)

// Credentials is a git credential pair brokered for a single host.
type Credentials struct {
	Username string
	Password string
}

// SCMCredentials brokers a fresh git credential from the control plane for the
// given host (POST /scm-credentials?host=<host>). The control plane responds
// with {username, password} or {token}; the username defaults to "x-access-token"
// and the password falls back to the token.
func (c *Client) SCMCredentials(ctx context.Context, host string) (Credentials, error) {
	var resp struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := c.doJSON(ctx, "POST", "/scm-credentials?host="+url.QueryEscape(host), nil, &resp); err != nil {
		return Credentials{}, err
	}
	creds := Credentials{Username: resp.Username, Password: resp.Password}
	if creds.Username == "" {
		creds.Username = "x-access-token"
	}
	if creds.Password == "" {
		creds.Password = resp.Token
	}
	return creds, nil
}
