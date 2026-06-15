package app

import (
	"testing"
	"time"
)

func TestParseProcUptime(t *testing.T) {
	// First field is seconds since boot; the second (idle) is ignored.
	if d, ok := parseProcUptime([]byte("3600.50 1234.00\n")); !ok || d != 3600500*time.Millisecond {
		t.Fatalf("got %v ok=%v, want 1h0.5s", d, ok)
	}
	// A lone value (no idle field) is still valid.
	if d, ok := parseProcUptime([]byte("90\n")); !ok || d != 90*time.Second {
		t.Fatalf("got %v ok=%v, want 90s", d, ok)
	}
	// Empty / unparseable / negative -> not ok.
	for _, bad := range []string{"", "   \n", "abc 1.0", "-5 1.0"} {
		if d, ok := parseProcUptime([]byte(bad)); ok {
			t.Fatalf("input %q should be invalid, got %v", bad, d)
		}
	}
}
