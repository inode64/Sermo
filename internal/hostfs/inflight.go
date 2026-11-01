package hostfs

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrHung marks a read of an operator-named path that did not answer: a stat,
// open or walk on a hard network mount whose server is gone blocks in the kernel
// and cannot be interrupted.
var ErrHung = errors.New("hung path")

// InFlight bounds an operation that can block in the kernel. The operation runs
// on its own goroutine with at most one in flight per key, so a hung path costs
// one goroutine and not one per cycle: callers stop waiting at their timeout, and
// until that operation returns later callers fail at once instead of stacking
// another blocked call. A result that arrives after every caller gave up is
// dropped.
//
// A caller shares the running operation of its key, so a key must identify
// everything run depends on. run must not touch state its caller goes on using
// once it gave up.
type InFlight[T any] struct {
	op    string
	hung  error
	grace time.Duration

	mu    sync.Mutex
	calls map[string]*inFlightCall[T]
}

type inFlightCall[T any] struct {
	done     chan struct{}
	value    T
	err      error
	timedOut bool // protected by InFlight.mu
}

// NewInFlight returns a guard whose give-up errors wrap hung and name op. grace
// extends the wait past the timeout those errors report, for an operation that
// honors a context of the same timeout: it then reports its own deadline, which
// says "slow", rather than being called hung.
func NewInFlight[T any](op string, hung error, grace time.Duration) *InFlight[T] {
	return &InFlight[T]{op: op, hung: hung, grace: grace, calls: make(map[string]*inFlightCall[T])}
}

// Do runs run for key within timeout, unbounded when timeout is not positive.
func (g *InFlight[T]) Do(key string, timeout time.Duration, run func() (T, error)) (T, error) { //nolint:ireturn // T is the concrete result each guard instantiates
	if timeout <= 0 {
		return run()
	}
	g.mu.Lock()
	call := g.calls[key]
	if call == nil {
		call = &inFlightCall[T]{done: make(chan struct{})}
		g.calls[key] = call
		go func() {
			call.value, call.err = run()
			g.mu.Lock()
			delete(g.calls, key)
			close(call.done)
			g.mu.Unlock()
		}()
	}
	timedOut := call.timedOut
	g.mu.Unlock()
	var zero T
	if timedOut {
		return zero, fmt.Errorf("%w: an earlier %s is still blocked", g.hung, g.op)
	}
	timer := time.NewTimer(timeout + g.grace)
	defer timer.Stop()
	select {
	case <-call.done:
		return call.value, call.err
	case <-timer.C:
		g.mu.Lock()
		call.timedOut = true
		g.mu.Unlock()
		return zero, fmt.Errorf("%w: no answer within %s", g.hung, timeout)
	}
}
