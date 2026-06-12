package controlplane

import "context"

// SlackNotifyRequest is an agent-initiated Slack message.
type SlackNotifyRequest struct {
	Channel  string
	Text     string
	ThreadTS string // optional
	Reason   string // optional; recorded for audit, not shown in Slack
}

// SlackNotifyResult is the success envelope returned by /slack-notify
// (SlackNotifySuccessOutput in the shared wire contract).
type SlackNotifyResult struct {
	OK                 bool   `json:"ok"`
	ChannelInput       string `json:"channelInput"`
	ChannelID          string `json:"channelId"`
	MessageTS          string `json:"messageTs"`
	Permalink          string `json:"permalink"`
	Truncated          bool   `json:"truncated"`
	StrippedBroadcasts bool   `json:"strippedBroadcasts"`
	MentionsModified   bool   `json:"mentionsModified"`
}

// SlackNotify posts an agent message to Slack (POST /slack-notify). On denial it
// returns an *APIError whose Code is the wire denial reason (e.g.
// "channel_not_found_or_forbidden") and whose RetryAfter is set when rate
// limited.
func (c *Client) SlackNotify(ctx context.Context, req SlackNotifyRequest) (SlackNotifyResult, error) {
	payload := map[string]any{
		"channel":   req.Channel,
		"text":      req.Text,
		"thread_ts": req.ThreadTS,
		"reason":    req.Reason,
	}
	var out SlackNotifyResult
	if err := c.doJSON(ctx, "POST", "/slack-notify", payload, &out); err != nil {
		return SlackNotifyResult{}, err
	}
	return out, nil
}
