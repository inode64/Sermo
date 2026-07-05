// Package checks runs a service's monitoring/preflight/postflight checks.
// Each Run invocation is single-shot; checks that track
// change over time keep their state in the built check instance. The runner
// executes a set concurrently and returns one Result per check.
//
// Service checks/preflight/postflight support tcp, ports, http, command, service,
// file_exists, file, lockfile, binary, process, metric (via the daemon's stateful collector),
// libraries, count, and host-resource probes (storage, autofs, load, fds, conntrack,
// firewall_rules, entropy, zombies, oom, cert). The multi-target watch types
// (net, icmp, swap and file) are also usable in single-metric/single-shot form.
// See buildCheck and TypeInfo for the shared dispatch/validation vocabulary.
package checks

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sermo/internal/conn"
)

// DataKeyOutput is the Result.Data key under which a command check or app probe
// stores its bounded stdout/stderr on failure. The daemon reads it to thread
// command output into an event, so writer (checks) and reader (app) share it.
const DataKeyOutput = "output"

// Result is the observable outcome of one check.
type Result struct {
	Service   string         `json:"service,omitempty"`
	Check     string         `json:"check"`
	OK        bool           `json:"ok"`
	Condition bool           `json:"-"`
	Optional  bool           `json:"optional,omitempty"`
	Skipped   bool           `json:"skipped,omitempty"` // gated off this cycle (requires/skip_when_changed)
	Message   string         `json:"message,omitempty"`
	Latency   time.Duration  `json:"latency_ns,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Healthy reports whether this result means the target is available. Most
// checks are health-style (OK means healthy); condition checks are alert-style
// (OK means the condition fired), so their availability is inverted.
func (r Result) Healthy() bool {
	if r.Condition {
		return !r.OK
	}
	return r.OK
}

// Check is a single-shot probe.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// IsHealthType reports whether OK==true means the check is healthy. Host watches
// invert these checks and fire on failure; condition-style checks fire on OK.
func IsHealthType(typ string) bool {
	if info, ok := TypeInfoFor(typ); ok {
		return info.Health
	}
	_, ok := conn.Lookup(typ)
	return ok
}

// Built pairs a check with whether its failure is optional (a warning) or
// required (blocks the action), per policy.
type Built struct {
	Check    Check
	Optional bool
}

// Run executes checks concurrently and returns their results in input order.
// maxParallel bounds concurrency; 0 means unbounded (the sermoctl one-shot
// path; the daemon's global semaphore is a separate concern).
func Run(ctx context.Context, built []Built, maxParallel int) []Result {
	results := make([]Result, len(built))
	var sem chan struct{}
	if maxParallel > 0 {
		sem = make(chan struct{}, maxParallel)
	}

	var wg sync.WaitGroup
	for i, b := range built {
		wg.Add(1)
		go func(i int, b Built) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}
			res := b.Check.Run(ctx)
			// A check may mark its own result optional (a warning, e.g. an output
			// pattern match graded `warning`); keep that, and the static flag also
			// makes a check optional.
			res.Optional = res.Optional || b.Optional
			results[i] = res
		}(i, b)
	}
	wg.Wait()
	return results
}

// base carries the fields every check shares and applies the per-check timeout.
type base struct {
	name      string
	service   string
	timeout   time.Duration
	condition bool
}

func (b base) Name() string { return b.name }

// withTimeout derives the check's deadline from the caller's context.
func (b base) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, b.timeout)
}

func (b base) result(ok bool, message string, start time.Time) Result {
	return Result{
		Service:   b.service,
		Check:     b.name,
		OK:        ok,
		Condition: b.condition,
		Message:   message,
		Latency:   time.Since(start),
	}
}

// levelCountResult builds the Result shared by the count-vs-max level checks
// (fds, pids, conntrack): the used_pct/free predicate fields — with free clamped
// so a count momentarily above the max can't underflow the unsigned subtraction
// — the "label cur/max unit (pct)" message, and the Data map. countField names
// the primary metric in values/Data ("allocated", "count"). The kernel maximum
// (each sample's Max, the Data `max` field) is the `limit` parameter: the
// lowercase local is `limit`, not `max`, only to avoid shadowing the Go `max`
// builtin — keep it that way. When it is 0 the maximum is unknown, so used_pct/
// free are omitted and a predicate on them cannot hold (the level check is an AND).
func levelCountResult(b base, preds []levelPred, label, unit, countField string, count, limit uint64, start time.Time) Result {
	values := map[string]float64{countField: float64(count)}
	usedPct := 0.0
	if limit > 0 {
		usedPct = float64(count) / float64(limit) * 100
		values[fieldUsedPct] = usedPct
		values[fieldFree] = float64(limit - min(count, limit))
	}
	res := b.result(levelPredsHold(preds, values), fmt.Sprintf("%s %d/%d %s (%.1f%%)", label, count, limit, unit, usedPct), start)
	res.Data = map[string]any{countField: count, "max": limit, fieldUsedPct: usedPct}
	if limit > 0 {
		res.Data[fieldFree] = limit - min(count, limit)
	}
	res.Data["value"] = firstPredValue(preds, values, usedPct)
	return res
}
