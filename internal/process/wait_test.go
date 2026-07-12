package process

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestWaitNilSleepCancelLeavesNoGoroutine pins that the default (nil sleep) Wait
// uses a stoppable timer: a cancelled long Wait returns promptly and leaves no
// goroutine blocked. The previous implementation defaulted to time.Sleep in a
// goroutine that lingered for the full duration after ctx was cancelled.
func TestWaitNilSleepCancelLeavesNoGoroutine(t *testing.T) {
	base := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if err := Wait(ctx, nil, time.Hour); err == nil {
		t.Fatal("Wait should return ctx.Err() after cancel")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Wait blocked %v, want prompt return on cancel", elapsed)
	}

	// A leaked goroutine would still be blocked in time.Sleep(1h). Poll briefly
	// to let the canceller goroutine exit, then confirm we are back near baseline.
	for range 50 {
		if runtime.NumGoroutine() <= base+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine leaked: base=%d now=%d", base, runtime.NumGoroutine())
}
