package process

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestWaitUntil(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timeout   time.Duration
		exitAfter time.Duration
		wantWait  time.Duration
		wantReady bool
	}{
		{name: "already exited", timeout: time.Minute, wantReady: true},
		{name: "exits during grace", timeout: time.Minute, exitAfter: processExitPollInterval, wantWait: processExitPollInterval, wantReady: true},
		{name: "zero grace still observes", exitAfter: time.Minute},
		{name: "negative grace still observes", timeout: -time.Second, exitAfter: time.Minute},
		{name: "short grace", timeout: processExitPollInterval / 2, exitAfter: time.Minute, wantWait: processExitPollInterval / 2},
		{name: "partial final interval", timeout: 2*processExitPollInterval + processExitPollInterval/2, exitAfter: time.Minute, wantWait: 2*processExitPollInterval + processExitPollInterval/2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var slept time.Duration
			observations := 0
			ready, err := WaitUntil(t.Context(), func(d time.Duration) { slept += d }, tc.timeout, func() (bool, error) {
				observations++
				return slept >= tc.exitAfter, nil
			})
			if err != nil || ready != tc.wantReady || slept != tc.wantWait || observations == 0 {
				t.Fatalf("ready=%v err=%v waited=%v observations=%d, want ready=%v waited=%v", ready, err, slept, observations, tc.wantReady, tc.wantWait)
			}
		})
	}
}

func TestWaitUntilErrors(t *testing.T) {
	discoveryErr := errors.New("process discovery failed")
	for _, tc := range []struct {
		name           string
		cancelBefore   bool
		cancelObserve  bool
		observationErr error
		wantErr        error
	}{
		{name: "cancelled before discovery", cancelBefore: true, wantErr: context.Canceled},
		{name: "cancelled observation cannot succeed", cancelObserve: true, wantErr: context.Canceled},
		{name: "discovery error", observationErr: discoveryErr, wantErr: discoveryErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			ready, err := WaitUntil(ctx, func(time.Duration) { t.Error("unexpected sleep") }, time.Minute, func() (bool, error) {
				if tc.cancelBefore {
					t.Error("cancelled wait must not observe")
				}
				if tc.cancelObserve {
					cancel()
				}
				return true, tc.observationErr
			})
			if ready || !errors.Is(err, tc.wantErr) {
				t.Fatalf("ready=%v err=%v, want false and %v", ready, err, tc.wantErr)
			}
		})
	}
}

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
