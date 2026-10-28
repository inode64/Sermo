package operation

import (
	"context"
	"time"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// DefaultOperationTimeout is the outer deadline for start/stop/restart/reload/resume
// when no shorter parent context applies. Matches sermoctl's default for service actions.
const DefaultOperationTimeout = 90 * time.Second

// backendMargin budgets servicemgr stop/start and check phases beyond the
// stop_policy signal waits.
const backendMargin = 30 * time.Second

// defaultDockerStopTimeout bounds Docker's no-kill stop request when the
// operator has not configured a positive stop_policy.graceful_timeout.
// It fits within backendMargin, preserving time for verification and start.
const defaultDockerStopTimeout = 10 * time.Second

// minimumTimeout is the shortest safe operation deadline implied by a resolved
// stop policy: the graceful wait, the SIGTERM wait (a residual in the unit's
// own cgroup gets it even without force_kill), the SIGKILL wait when force_kill
// is true or auto, plus backendMargin.
func minimumTimeout(policy process.KillPolicy) time.Duration {
	d := policy.GracefulTimeout + policy.TermTimeout
	if policy.ForceKill || policy.Automatic {
		d += policy.KillTimeout
	}
	return d + backendMargin
}

// ResolveTimeout parses a service tree and returns its effective operation
// deadline. Engine construction uses resolveTimeout with its already-resolved
// policy; tree-level callers such as web deadline planning use this adapter.
func ResolveTimeout(configured time.Duration, tree map[string]any) time.Duration {
	policy, _ := process.ParseStopPolicy(tree)
	selectors, _ := process.ParseSelectors(tree)
	return resolveTimeout(configured, process.EnableAutomaticReaping(policy, selectors))
}

// resolveTimeout returns the effective operation deadline: the configured value
// (or DefaultOperationTimeout when <= 0), raised to minimumTimeout when the
// resolved stop policy needs longer.
func resolveTimeout(configured time.Duration, policy process.KillPolicy) time.Duration {
	if configured <= 0 {
		configured = DefaultOperationTimeout
	}
	if m := minimumTimeout(policy); m > configured {
		return m
	}
	return configured
}

// operationTimeout is the deadline of one action. An action that stops a
// native init unit also covers the init's own stop timeout: the stop request
// can block that long before the init's final SIGKILL, and a restart that
// spent its whole deadline there left the service stopped without starting it.
func (e Engine) operationTimeout(ctx context.Context, p plan) time.Duration {
	reporter, ok := e.Manager.(servicemgr.StopTimeoutReporter)
	if !p.stop || !ok {
		return e.OperationTimeout
	}
	initStop, bounded := reporter.StopTimeout(ctx, e.Unit)
	if !bounded {
		return e.OperationTimeout
	}
	return max(e.OperationTimeout, initStop+minimumTimeout(e.KillPolicy))
}

func timedOut(ctx context.Context) bool {
	return ctx.Err() == context.DeadlineExceeded
}
