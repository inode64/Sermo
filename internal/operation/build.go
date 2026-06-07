package operation

import (
	"context"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

// Config wires the real components an Engine needs for a resolved service.
type Config struct {
	Service string
	Unit    string
	Backend string
	Tree    map[string]any // resolved service config

	Manager    Manager
	Locker     *locks.OperationLocker
	Scanner    locks.Scanner
	Discoverer process.Discoverer
	CheckDeps  checks.Deps // Runner/HTTPClient/DefaultTimeout; Status is filled in
	LockTTL           time.Duration
	Sleep             func(time.Duration)
	OperationTimeout  time.Duration
	Emit              func(Result)
}

// New builds an Engine from real components, deriving preflight/postflight/guard
// closures, residual discovery, the kill policy and the reaper from the resolved
// config tree (sections 12, 14, 17, 19, 21, 22).
func New(c Config) Engine {
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	deps := c.CheckDeps
	deps.Service = c.Service
	if deps.Status == nil {
		unit := c.Unit
		mgr := c.Manager
		deps.Status = func(ctx context.Context) (servicemgr.Status, error) {
			st, err := mgr.Status(ctx, unit)
			if err != nil {
				return "", err
			}
			return st.Status, nil
		}
	}
	if deps.Processes == nil {
		deps.Processes = c.Discoverer.ObserveState
	}

	tree := c.Tree
	killPolicy, _ := process.ParseStopPolicy(tree)

	discover := func() []process.Process {
		selectors, _ := process.ParseSelectors(tree)
		procs, _ := c.Discoverer.Discover(selectors)
		return procs
	}

	reaper := process.Reaper{
		Signaler:    process.OSSignaler{},
		ResolveUser: process.OSUserResolver,
		Sleep:       sleep,
		Rediscover:  discover,
	}

	ttl := c.LockTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	return Engine{
		Service: c.Service,
		Unit:    c.Unit,
		Backend: c.Backend,
		Manager: c.Manager,
		AcquireLock: func(t time.Duration) (func() error, error) {
			h, err := c.Locker.Acquire(c.Service, t)
			if err != nil {
				return nil, err
			}
			return h.Release, nil
		},
		LockTTL: ttl,
		NamedLocks: func() ([]locks.Lock, error) {
			report, err := c.Scanner.Scan(c.Service)
			return report.Locks, err
		},
		Guard:      guardClosure(tree, deps),
		Preflight:  sectionRunner(tree, "preflight", deps),
		Postflight: sectionRunner(tree, "postflight", deps),
		Discover:   discover,
		Reaper:     reaper,
		KillPolicy:       killPolicy,
		Sleep:            sleep,
		OperationTimeout: c.OperationTimeout,
		Emit:             c.Emit,
	}
}

// sectionRunner builds and runs a checks/preflight/postflight section, returning
// its evaluated outcome. A missing section is a trivial pass.
func sectionRunner(tree map[string]any, section string, deps checks.Deps) func(context.Context) checks.Outcome {
	return func(ctx context.Context) checks.Outcome {
		entries, _ := tree[section].(map[string]any)
		built, _ := checks.Build(entries, deps)
		return checks.Evaluate(checks.Run(ctx, built, 0))
	}
}

// guardClosure runs the service's named checks once, caches them, and evaluates
// the guard rules against that cache plus inline probes (sections 14, 17).
func guardClosure(tree map[string]any, deps checks.Deps) func(context.Context, string) (bool, string, error) {
	return func(ctx context.Context, action string) (bool, string, error) {
		ruleSet, _ := rules.ParseRules(tree)
		section, _ := tree["checks"].(map[string]any)
		built, _ := checks.Build(section, deps)
		cache := map[string]checks.Result{}
		for _, r := range checks.Run(ctx, built, 0) {
			cache[r.Check] = r
		}
		ev := &rules.Evaluator{Cache: cache, Deps: deps}
		return rules.Guard(ctx, ruleSet, action, ev)
	}
}
