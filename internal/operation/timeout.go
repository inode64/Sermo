package operation

import (
	"context"
	"time"

	"sermo/internal/process"
)

// DefaultOperationTimeout is the outer deadline for start/stop/restart/reload/resume
// when no shorter parent context applies. Matches sermoctl's default for service actions.
const DefaultOperationTimeout = 90 * time.Second

// backendMargin budgets servicemgr stop/start and check phases beyond the
// stop_policy signal waits.
const backendMargin = 30 * time.Second

// minimumTimeout is the shortest safe operation deadline implied by a resolved
// stop policy: graceful wait plus signal escalation sleeps when force_kill is
// enabled, plus backendMargin.
func minimumTimeout(policy process.KillPolicy) time.Duration {
	d := policy.GracefulTimeout
	if policy.ForceKill {
		d += policy.TermTimeout + policy.KillTimeout
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

func boundContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func timedOut(ctx context.Context) bool {
	return ctx.Err() == context.DeadlineExceeded
}
