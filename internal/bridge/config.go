package bridge

import (
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Tunables ported from the Python bridge. Durations that were float seconds
// upstream are expressed as time.Duration here.
const (
	heartbeatInterval     = 30 * time.Second
	reconnectBackoffBase  = 2.0
	reconnectMaxDelay     = 60 * time.Second
	sseInactivityDefault  = 120 * time.Second
	sseInactivityMin      = 5 * time.Second
	sseInactivityMax      = 3600 * time.Second
	httpConnectTimeout    = 30 * time.Second
	httpDefaultTimeout    = 30 * time.Second
	opencodeRequestTimeout = 30 * time.Second
	gitPushTimeout        = 300 * time.Second
	gitPushTerminateGrace = 5 * time.Second
	promptMaxDuration     = 5400 * time.Second
	gitConfigTimeout      = 10 * time.Second

	maxPendingPartEvents = 2000
	maxEventBufferSize   = 1000

	wsReadLimit = 32 * 1024 * 1024 // 32 MiB; token/tool-output frames exceed the 32 KiB default.
)

// criticalEventTypes are re-sent until acknowledged by the control plane.
var criticalEventTypes = map[string]bool{
	"execution_complete": true,
	"error":              true,
	"snapshot_ready":     true,
	"push_complete":      true,
	"push_error":         true,
}

// opencodeDefaultTitleRE matches OpenCode's auto-generated session titles, which
// must not be forwarded as user-visible session titles.
var opencodeDefaultTitleRE = regexp.MustCompile(
	`(?i)^(new session|child session) - \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`,
)

// resolveTimeout reads a duration (in seconds) from an environment variable,
// falling back to def and clamping to [min, max]. It mirrors the logging and
// clamping behavior of the Python implementation.
func resolveTimeout(log *slog.Logger, name string, def, min, max time.Duration) time.Duration {
	value := def
	if raw := os.Getenv(name); raw != "" {
		secs, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			log.Warn("bridge.timeout_invalid",
				"timeout_name", name,
				"timeout_ms", def.Milliseconds(),
				"detail", "invalid value '"+raw+"', using default",
			)
		} else {
			value = time.Duration(secs * float64(time.Second))
		}
	}

	switch {
	case value < min:
		log.Warn("bridge.timeout_clamped",
			"timeout_name", name,
			"timeout_ms", min.Milliseconds(),
			"detail", "below min, clamped",
		)
		value = min
	case value > max:
		log.Warn("bridge.timeout_clamped",
			"timeout_name", name,
			"timeout_ms", max.Milliseconds(),
			"detail", "above max, clamped",
		)
		value = max
	}

	log.Info("bridge.timeout_config",
		"timeout_name", name,
		"timeout_ms", value.Milliseconds(),
		"min_ms", min.Milliseconds(),
		"max_ms", max.Milliseconds(),
	)
	return value
}
