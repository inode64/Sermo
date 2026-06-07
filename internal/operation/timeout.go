package operation

import (
	"context"
	"time"

	"sermo/internal/process"
)

// DefaultOperationTimeout is the outer deadline for start/stop/restart when no
// shorter parent context applies. Matches sermoctl's default for service actions.
const DefaultOperationTimeout = 90 * time.Second

// backendMargin budgets servicemgr stop/start and check phases beyond the
// stop_policy signal waits.
const backendMargin = 30 * time.Second

// MinimumTimeout is the shortest safe operation deadline implied by a service's
// resolved stop_policy: graceful wait plus signal escalation sleeps when
// force_kill is enabled, plus backendMargin.
func MinimumTimeout(tree map[string]any) time.Duration {
	policy, _ := process.ParseStopPolicy(tree)
	d := policy.GracefulTimeout
	if policy.ForceKill {
		d += policy.TermTimeout + policy.KillTimeout
	}
	return d + backendMargin
}

// ResolveTimeout returns the effective operation deadline: the configured value
// (or DefaultOperationTimeout when <= 0), raised to MinimumTimeout when the
// service's stop_policy needs longer.
func ResolveTimeout(configured time.Duration, tree map[string]any) time.Duration {
	if configured <= 0 {
		configured = DefaultOperationTimeout
	}
	if min := MinimumTimeout(tree); min > configured {
		return min
	}
	return configured
}

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