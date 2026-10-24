package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
)

// cascadeMaxDepth backstops pathological (but acyclic) also_apply chains; the
// visited set already cuts cycles. It counts recursion depth, not total nodes.
const (
	cascadeMaxDepth          = 16
	cascadeBlockedRetryDelay = time.Second
	// cascadeOperateAttempts is the first attempt plus the single blocked retry.
	cascadeOperateAttempts = 2
)

// CascadeConfig supplies the service graph and the one guarded operation used by
// every cascade caller. Target observes each additional target after its final
// attempt; Emit records the canonical cascade relationship event.
//
// Downgraded receives the root's result when it succeeded on its own but an
// additional target failed, so the cascade reports it failed. A caller whose
// root operation already recorded its own success records that final result
// through it, as the daemon worker does for the result RunCascade returns; the
// root's buttons then show the cascade failed from every source.
type CascadeConfig struct {
	Operate    func(ctx context.Context, service, action string) (operation.Result, error)
	Lookup     func(service string) []string
	Target     func(service string, result operation.Result, err error)
	Emit       func(Event)
	Downgraded func(primary operation.Result)
}

// cascader orchestrates an action across a service and the services it lists in
// also_apply. It owns the ordering so it can place the primary correctly for the
// dependency-aware semantics, and runs strictly sequentially (each Operate takes
// that service's own operation lock and releases it before the next step, so a
// serial walk never self-deadlocks even when a target repeats).
type cascader struct {
	config CascadeConfig
	// sleep is the backoff's process.Wait sleeper: tests inject a no-op;
	// production leaves it nil for the cancellable timer.
	sleep func(time.Duration)
}

// RunCascade operates root plus its also_apply graph through the canonical
// ordering, retry, failure and event semantics shared by daemon, web and CLI.
func RunCascade(ctx context.Context, root, action string, cfg CascadeConfig) (operation.Result, error) {
	return (cascader{config: cfg}).run(ctx, root, action)
}

// run operates root plus its also_apply graph for action, in dependency order
// (start/restart: primary first, pre-order; stop: primary last, post-order),
// sequentially and best-effort. It returns root's own Result (the primary), which
// drives the caller's bookkeeping; additionals are reported as `cascade` events.
// When an additional target fails, a successful primary is downgraded to failed
// so callers do not treat the cascade as fully successful.
func (c cascader) run(ctx context.Context, root, action string) (operation.Result, error) {
	visited := map[string]bool{}
	seq := orderedGroup(root, action, c.config.Lookup, visited, 0)
	var primary operation.Result
	var primaryErr error
	var cascadeFailed bool
	for _, svc := range seq {
		res, err := c.operate(ctx, svc, action)
		res = cascadeResult(svc, action, res, err)
		if svc == root {
			primary = res
			primaryErr = err
			continue
		}
		if err != nil || cascadeTargetFailed(res) {
			cascadeFailed = true
		}
		if c.config.Target != nil {
			c.config.Target(svc, res, err)
		}
		emitSafe(c.config.Emit, cascadeEvent(root, res))
	}
	final := downgradePrimaryOnCascadeFailure(primary, cascadeFailed)
	if final.Status != primary.Status && c.config.Downgraded != nil {
		c.config.Downgraded(final)
	}
	return final, primaryErr
}

func cascadeResult(service, action string, result operation.Result, err error) operation.Result {
	if result.Service == "" {
		result.Service = service
	}
	if result.Action == "" {
		result.Action = action
	}
	if err == nil {
		return result
	}
	result.Status = operation.ResultFailed
	if result.Message == "" {
		result.Message = err.Error()
	} else {
		result.Message += "; " + err.Error()
	}
	return result
}

func cascadeTargetFailed(res operation.Result) bool {
	return res.Status != operation.ResultOK && res.Status != operation.ResultBlocked
}

func downgradePrimaryOnCascadeFailure(primary operation.Result, cascadeFailed bool) operation.Result {
	if !cascadeFailed || !primary.OK() {
		return primary
	}
	primary.Status = operation.ResultFailed
	if primary.Message == "" {
		primary.Message = "cascade target failed"
	} else {
		primary.Message += "; cascade target failed"
	}
	return primary
}

// operate runs one service, retrying once after a short backoff when it is
// blocked (a target concurrently mid-operation holds its per-service lock).
func (c cascader) operate(ctx context.Context, svc, action string) (operation.Result, error) {
	res, err := c.config.Operate(ctx, svc, action)
	if err == nil && res.Status == operation.ResultBlocked {
		if err := c.backoff(ctx); err != nil {
			return res, err
		}
		res, err = c.config.Operate(ctx, svc, action)
	}
	return res, err
}

// backoff waits cascadeBlockedRetryDelay before a single blocked-lock retry,
// returning early when ctx ends.
func (c cascader) backoff(ctx context.Context) error {
	if err := process.Wait(ctx, c.sleep, cascadeBlockedRetryDelay); err != nil {
		return fmt.Errorf("cascade retry wait: %w", err)
	}
	return nil
}

// cascadeBudget is the longest a synchronous RunCascade over root's also_apply
// group may take: members run one after another under their own timeout, and
// each may be retried once after cascadeBlockedRetryDelay when blocked.
func cascadeBudget(root string, lookup func(string) []string, timeout func(string) time.Duration) time.Duration {
	var total time.Duration
	for _, svc := range orderedGroup(root, "", lookup, map[string]bool{}, 0) {
		total += cascadeOperateAttempts*timeout(svc) + cascadeBlockedRetryDelay
	}
	return total
}

// orderedGroup returns the services to operate, in dependency order. For stop the
// root is placed AFTER its targets (post-order: dependents down first); otherwise
// BEFORE (pre-order: the thing depended on comes up first). A visited set cuts
// cycles and de-duplicates; depth caps pathological chains.
func orderedGroup(root, action string, lookup func(string) []string, visited map[string]bool, depth int) []string {
	if visited[root] || depth > cascadeMaxDepth {
		return nil
	}
	visited[root] = true
	stop := action == string(rules.ActionStop)
	var out []string
	if !stop {
		out = append(out, root)
	}
	for _, t := range lookup(root) {
		out = append(out, orderedGroup(t, action, lookup, visited, depth+1)...)
	}
	if stop {
		out = append(out, root)
	}
	return out
}
