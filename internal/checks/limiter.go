package checks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Limiter bounds executing probes across independently built services, watches
// and operation checks. One daemon shares one instance, including across reloads.
type Limiter struct {
	mu        sync.Mutex
	limit     int
	running   int
	changed   chan struct{}
	onStarved func()
}

// NewLimiter creates a shared check budget. A non-positive limit is unbounded.
func NewLimiter(limit int) *Limiter {
	return &Limiter{limit: limit, changed: make(chan struct{})}
}

// SetLimit updates the budget without losing permits held by in-flight checks.
// Lowering it lets those checks finish before admitting more work.
func (l *Limiter) SetLimit(limit int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limit = limit
	l.wake()
}

// OnStarved sets the callback run when a check's deadline passes while it
// waits for a slot: the shared budget, not the probed target, kept it from
// running. A cancelled wait (reload, shutdown) is not starvation.
func (l *Limiter) OnStarved(callback func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onStarved = callback
}

func (l *Limiter) starved(err error) {
	if !errors.Is(err, context.DeadlineExceeded) {
		return
	}
	l.mu.Lock()
	callback := l.onStarved
	l.mu.Unlock()
	if callback != nil {
		callback()
	}
}

func (l *Limiter) wake() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *Limiter) acquire(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for check slot: %w", err)
		}
		l.mu.Lock()
		if l.limit <= 0 || l.running < l.limit {
			l.running++
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			l.starved(ctx.Err())
			return fmt.Errorf("wait for check slot: %w", ctx.Err())
		case <-changed:
		}
	}
}

func (l *Limiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running--
	l.wake()
}

type limitedCheck struct {
	Check
	limiter *Limiter
	timeout time.Duration
}

func withLimiter(check Check, limiter *Limiter, timeout time.Duration) Check {
	if limiter == nil {
		return check
	}
	return limitedCheck{Check: check, limiter: limiter, timeout: timeout}
}

func (c limitedCheck) resultMetadata() Result { return checkResultMetadata(c.Check) }

// Run waits for a slot within the caller's context and only then starts the
// check's timeout: a probe queued behind a burst runs late rather than failing
// unrun, which would read as the target being unavailable.
func (c limitedCheck) Run(ctx context.Context) Result {
	if err := c.limiter.acquire(ctx); err != nil {
		return checkNotStartedResult(Built{Check: c.Check}, err)
	}
	defer c.limiter.release()
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return checkNotStartedResult(Built{Check: c.Check}, err)
	}
	return c.Check.Run(ctx)
}
