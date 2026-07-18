package netutil

import (
	"context"
	"testing"
	"time"
)

// TimeoutFromContext returns the context's remaining time, the fallback with no
// deadline, or 1ns when already past. Pin all three branches.
func TestTimeoutFromContext(t *testing.T) {
	if got := TimeoutFromContext(context.Background(), 10*time.Second); got != 10*time.Second {
		t.Errorf("no deadline = %v, want the 10s fallback", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := TimeoutFromContext(ctx, 10*time.Second); got <= 0 || got > time.Hour {
		t.Errorf("future deadline = %v, want within (0, 1h]", got)
	}

	past, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel2()
	if got := TimeoutFromContext(past, 10*time.Second); got != time.Nanosecond {
		t.Errorf("past deadline = %v, want 1ns fail-fast", got)
	}
}
