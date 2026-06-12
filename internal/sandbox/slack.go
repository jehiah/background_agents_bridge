package sandbox

import (
	"context"
	"encoding/json"

	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// slackReasonGuidance maps a denial reason to agent-facing guidance. Keys must
// stay symmetric with SLACK_DENIAL_REASONS upstream (ported from slack-notify.js).
var slackReasonGuidance = map[string]string{
	"feature_unavailable":              "The deployment is not configured to send agent notifications. Tell the user this is unavailable.",
	"feature_disabled":                 "Agent notifications are disabled for this repository. Ask the user to enable them in integration settings.",
	"channel_not_found_or_forbidden":   "The channel was not found, is archived, or the bot is not in it. If the channel name is correct and not archived, ask the user to invite the bot.",
	"empty_message_after_sanitization": "The message body was empty after sanitization. Try again with non-empty content.",
	"rate_limited":                     "Slack rate-limited the request. Wait before retrying.",
	"slack_api_error":                  "Slack returned an unexpected error. The post did not go through.",
	"invalid_input":                    "The notification arguments were invalid; correct them and retry.",
	"bridge_error":                     "Could not reach the control plane to post the notification.",
}

// slackClientErr formats a control-plane connection failure as a bridge_error
// envelope, matching the JS tool's contract (the agent expects JSON).
func slackClientErr(err error) string {
	return slackFailureEnvelope("bridge_error", err.Error(), nil)
}

func runSlackNotify(ctx context.Context, c *controlplane.Client, args map[string]any) string {
	result, err := c.SlackNotify(ctx, controlplane.SlackNotifyRequest{
		Channel:  argStr(args, "channel"),
		Text:     argStr(args, "text"),
		ThreadTS: argStr(args, "thread_ts"),
		Reason:   argStr(args, "reason"),
	})
	if err != nil {
		if e, ok := apiErr(err); ok {
			reason := e.Code
			if _, known := slackReasonGuidance[reason]; !known {
				reason = "slack_api_error"
			}
			return slackFailureEnvelope(reason, e.Message, e.RetryAfter)
		}
		return slackFailureEnvelope("bridge_error", err.Error(), nil)
	}

	out, _ := json.Marshal(result)
	return string(out)
}

// slackFailureEnvelope builds the {ok:false, reason, agentMessage, retryAfter?}
// JSON the agent expects on failure.
func slackFailureEnvelope(reason, message string, retryAfter *float64) string {
	guidance, ok := slackReasonGuidance[reason]
	if !ok {
		guidance = slackReasonGuidance["slack_api_error"]
	}
	detail := guidance
	if message != "" {
		detail = guidance + " (" + message + ")"
	}
	env := map[string]any{"ok": false, "reason": reason, "agentMessage": detail}
	if retryAfter != nil {
		env["retryAfter"] = *retryAfter
	}
	out, _ := json.Marshal(env)
	return string(out)
}
