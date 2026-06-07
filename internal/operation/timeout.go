package operation

import (
	"context"
	"time"
)

// DefaultOperationTimeout is the outer deadline for start/stop/restart when no
// shorter parent context applies. Matches sermoctl's default for service actions.
const DefaultOperationTimeout = 90 * time.Second

func boundContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = DefaultOperationTimeout
	}
	return context.WithTimeout(parent, timeout)
}

// wait blocks for d or until ctx is cancelled. An injectable sleep supports tests.
func wait(ctx context.Context, sleep func(time.Duration), d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	done := make(chan struct{})
	go func() {
		sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ctx.Err()
	}
}

func timedOut(ctx context.Context) bool {
	return ctx.Err() == context.DeadlineExceeded
}