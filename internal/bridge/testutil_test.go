package bridge

import (
	"io"
	"log/slog"

	"github.com/jehiah/background_agents_bridge/internal/sessiondiff"
)

// testBridge returns an AgentBridge wired with a discarding logger and the maps
// that handlers expect, suitable for unit tests that don't open connections.
func testBridge() *AgentBridge {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &AgentBridge{
		log:         log,
		ids:         &identifier{},
		pendingAcks: map[string]event{},
		sandboxID:   "sb-test",
		// Never started, so requests are recorded and nothing is collected; a
		// test that wants uploads replaces it (see withDiffWorker).
		diffRefresh: sessiondiff.NewWorker(nil, "", log),
	}
}
