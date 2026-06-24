package virt

import (
	"context"
	"testing"
	"time"
)

// timeoutFromContext returns the context's remaining time, a 10s default with no
// deadline, or 1ns when already past. Pin all three branches.
func TestTimeoutFromContext(t *testing.T) {
	if got := timeoutFromContext(context.Background()); got != 10*time.Second {
		t.Errorf("no deadline = %v, want 10s", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := timeoutFromContext(ctx); got <= 0 || got > time.Hour {
		t.Errorf("future deadline = %v, want within (0, 1h]", got)
	}

	ctx2, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel2()
	if got := timeoutFromContext(ctx2); got != time.Nanosecond {
		t.Errorf("past deadline = %v, want 1ns", got)
	}
}

// ParseUUID accepts hyphenated or compact 32-hex strings and rejects anything
// else (wrong length, or right length but non-hex).
func TestParseUUID(t *testing.T) {
	const compact = "1234567890abcdef1234567890abcdef"
	for _, ok := range []string{
		"12345678-90ab-cdef-1234-567890abcdef",
		compact,
		"  " + compact + "  ",
	} {
		if _, err := ParseUUID(ok); err != nil {
			t.Errorf("ParseUUID(%q) unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"too-short",
		compact[:30],
		"zz34567890abcdef1234567890abcdef", // 32 chars but non-hex prefix
	} {
		if _, err := ParseUUID(bad); err == nil {
			t.Errorf("ParseUUID(%q) = nil error, want failure", bad)
		}
	}
}
