package app

import (
	"context"
	"time"

	"sermo/internal/config"
	"sermo/internal/operation"
)

// cascadeMaxDepth backstops pathological (but acyclic) also_apply chains; the
// visited set already cuts cycles. It counts recursion depth, not total nodes.
const cascadeMaxDepth = 16

// operateFn runs one service's guarded operation for an action, returning its
// Result. The daemon supplies the monitor's per-service Operate; the CLI builds a
// target engine on demand.
type operateFn func(ctx context.Context, service, action string) operation.Result

// cascader orchestrates an action across a service and the services it lists in
// also_apply. It owns the ordering so it can place the primary correctly for the
// dependency-aware semantics, and runs strictly sequentially (each Operate
// acquires its own global slot, so a serial walk never self-deadlocks).
type cascader struct {
	op     operateFn
	lookup func(service string) []string // a service's also_apply targets
	emit   func(Event)
	sleep  func(time.Duration) // backoff before the single retry; injectable for tests
}

// run operates root plus its also_apply graph for action, in dependency order
// (start/restart: primary first, pre-order; stop: primary last, post-order),
// sequentially and best-effort. It returns root's own Result (the primary), which
// drives the caller's bookkeeping; additionals are reported as `cascade` events.
func (c cascader) run(ctx context.Context, root, action string) operation.Result {
	visited := map[string]bool{}
	seq := OrderedGroup(root, action, c.lookup, visited, 0)
	var primary operation.Result
	for _, svc := range seq {
		res := c.operate(ctx, svc, action)
		if svc == root {
			primary = res
			continue
		}
		if c.emit != nil {
			c.emit(Event{Service: svc, Kind: "cascade", Action: action,
				Status: string(res.Status), Message: "cascade from " + root})
		}
	}
	return primary
}

// operate runs one service, retrying once after a short backoff when it is
// blocked (a target concurrently mid-operation holds its per-service lock).
func (c cascader) operate(ctx context.Context, svc, action string) operation.Result {
	res := c.op(ctx, svc, action)
	if res.Status == operation.ResultBlocked {
		if c.sleep != nil {
			c.sleep(time.Second)
		}
		res = c.op(ctx, svc, action)
	}
	return res
}

// OrderedGroup returns the services to operate, in dependency order. For stop the
// root is placed AFTER its targets (post-order: dependents down first); otherwise
// BEFORE (pre-order: the thing depended on comes up first). A visited set cuts
// cycles and de-duplicates; depth caps pathological chains.
func OrderedGroup(root, action string, lookup func(string) []string, visited map[string]bool, depth int) []string {
	if visited[root] || depth > cascadeMaxDepth {
		return nil
	}
	visited[root] = true
	stop := action == "stop"
	var out []string
	if !stop {
		out = append(out, root)
	}
	for _, t := range lookup(root) {
		out = append(out, OrderedGroup(t, action, lookup, visited, depth+1)...)
	}
	if stop {
		out = append(out, root)
	}
	return out
}

// stopArtifacts maps a service's resolved stop_policy invariants into the engine
// form, shared by the daemon worker and web backend engine builds.
func stopArtifacts(tree map[string]any) operation.StopArtifacts {
	pp, ff, rm := config.StopInvariants(tree)
	return operation.StopArtifacts{PidfilePaths: pp, Files: ff, Remove: rm}
}
