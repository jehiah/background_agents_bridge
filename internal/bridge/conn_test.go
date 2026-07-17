package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDrainBootWarnings verifies boot warnings are forwarded as `warning`
// events, that blank/malformed/message-less lines are skipped, and that the
// file is consumed exactly once (a second drain is a no-op).
func TestDrainBootWarnings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oi-boot-warnings.jsonl")
	content := "" +
		`{"scope":"repo","message":"deleted branch","repoOwner":"o","repoName":"n"}` + "\n" +
		"\n" + // blank line skipped
		"not json\n" + // malformed skipped
		`{"scope":"repo"}` + "\n" + // no message skipped
		`{"message":"second"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	b := testBridge()
	b.bootWarningsPath = path
	b.drainBootWarnings()

	if len(b.eventBuffer) != 2 {
		t.Fatalf("expected 2 warning events, got %d: %+v", len(b.eventBuffer), b.eventBuffer)
	}
	first := b.eventBuffer[0]
	if first["type"] != "warning" || first["message"] != "deleted branch" ||
		first["repoOwner"] != "o" || first["repoName"] != "n" || first["scope"] != "repo" {
		t.Errorf("unexpected first warning: %+v", first)
	}
	if b.eventBuffer[1]["message"] != "second" {
		t.Errorf("unexpected second warning: %+v", b.eventBuffer[1])
	}

	// Exactly-once: the file is consumed, so a reconnect drain adds nothing.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("boot warnings file should be removed, stat err = %v", err)
	}
	b.drainBootWarnings()
	if len(b.eventBuffer) != 2 {
		t.Errorf("second drain replayed warnings: %d events", len(b.eventBuffer))
	}
}

// TestDrainBootWarningsAbsent verifies a missing file is a silent no-op.
func TestDrainBootWarningsAbsent(t *testing.T) {
	b := testBridge()
	b.bootWarningsPath = filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	b.drainBootWarnings()
	if len(b.eventBuffer) != 0 {
		t.Errorf("expected no events, got %+v", b.eventBuffer)
	}
}
