package process

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/ctxutil"
)

const (
	// processExitPollInterval bounds how often stop waits rediscover processes.
	processExitPollInterval = 250 * time.Millisecond
	waitCancelledFormat     = "wait cancelled: %w"
)

// Wait blocks for d, returning early if ctx is cancelled. A non-positive d is an
// immediate ctx-check. sleep is injectable for tests; nil uses a cancellable
// timer. It is shared by the reaper and the operation engine.
func Wait(ctx context.Context, sleep func(time.Duration), d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf(waitCancelledFormat, err)
	}
	if d <= 0 {
		return nil
	}
	if sleep == nil {
		// Default: the shared stoppable-timer wait, so a cancelled Wait leaks no
		// goroutine. An injected sleep (tests) takes the goroutine path below,
		// where the fake returns promptly and so cannot leak — unlike a real
		// time.Sleep, which is not cancellable and would block until d elapsed.
		if !ctxutil.Sleep(ctx, d) {
			return fmt.Errorf(waitCancelledFormat, ctx.Err())
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf(waitCancelledFormat, err)
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf(waitCancelledFormat, ctx.Err())
	case <-done:
		if err := ctx.Err(); err != nil {
			return fmt.Errorf(waitCancelledFormat, err)
		}
		return nil
	}
}

// WaitUntil checks done immediately and between bounded sleeps until it succeeds
// or timeout expires. A false result with no error means the full grace period
// elapsed. The caller retains responsibility for verifying or handling survivors.
func WaitUntil(ctx context.Context, sleep func(time.Duration), timeout time.Duration, done func() (bool, error)) (bool, error) {
	deadline := time.Now().Add(timeout)
	remaining := timeout
	for {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf(waitCancelledFormat, err)
		}
		ready, err := done()
		if err != nil {
			return false, fmt.Errorf("check process exit: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf(waitCancelledFormat, err)
		}
		if ready {
			return true, nil
		}
		// Track both wall time (including discovery) and requested sleep time so
		// an injected test sleeper can advance the wait without a real delay.
		delay := min(processExitPollInterval, remaining, time.Until(deadline))
		if delay <= 0 {
			return false, nil
		}
		if err := Wait(ctx, sleep, delay); err != nil {
			return false, err
		}
		remaining -= delay
	}
}
