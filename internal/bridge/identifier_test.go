package bridge

import (
	"strings"
	"testing"
)

func TestAscendingFormat(t *testing.T) {
	id := &identifier{}
	got, err := id.ascending("message")
	if err != nil {
		t.Fatalf("ascending: %v", err)
	}
	if !strings.HasPrefix(got, "msg_") {
		t.Errorf("prefix: got %q", got)
	}
	// msg_ (4) + 12 hex timestamp + 14 base62 random = 30
	if len(got) != 30 {
		t.Errorf("length: got %d (%q), want 30", len(got), got)
	}
}

func TestAscendingUnknownPrefix(t *testing.T) {
	id := &identifier{}
	if _, err := id.ascending("bogus"); err == nil {
		t.Fatal("expected error for unknown prefix")
	}
}

func TestAscendingMonotonic(t *testing.T) {
	id := &identifier{}
	const n = 5000
	prev := ""
	for i := 0; i < n; i++ {
		cur, err := id.ascending("message")
		if err != nil {
			t.Fatalf("ascending: %v", err)
		}
		// Compare only the ordered portion (prefix + timestamp/counter hex);
		// the random suffix is not ordered.
		curOrdered := cur[:len("msg_")+12]
		prevOrdered := ""
		if prev != "" {
			prevOrdered = prev[:len("msg_")+12]
		}
		if prev != "" && curOrdered < prevOrdered {
			t.Fatalf("not monotonic at %d: %q < %q", i, curOrdered, prevOrdered)
		}
		prev = cur
	}
}

func TestAscendingUnique(t *testing.T) {
	id := &identifier{}
	seen := make(map[string]bool)
	for i := 0; i < 10000; i++ {
		v, err := id.ascending("message")
		if err != nil {
			t.Fatalf("ascending: %v", err)
		}
		if seen[v] {
			t.Fatalf("duplicate id: %q", v)
		}
		seen[v] = true
	}
}
