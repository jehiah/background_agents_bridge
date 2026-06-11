package bridge

import (
	"io"
	"log/slog"
)

// testBridge returns an AgentBridge wired with a discarding logger and the maps
// that handlers expect, suitable for unit tests that don't open connections.
func testBridge() *AgentBridge {
	return &AgentBridge{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ids:         &identifier{},
		pendingAcks: map[string]event{},
		sandboxID:   "sb-test",
	}
}
