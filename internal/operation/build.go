package operation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
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
	// AlsoUnits are auxiliary init units (also_service) for the active backend,
	// resolved by the caller; the engine acts on them in wrap order.
	AlsoUnits []string
	Tree      map[string]any // resolved service config

	Manager          Manager
	Locker           *locks.OperationLocker
	Scanner          locks.Scanner
	Discoverer       process.Discoverer
	CheckDeps        checks.Deps // Runner/HTTPClient/DefaultTimeout; Status is filled in
	LockTTL          time.Duration
	Sleep            func(time.Duration)
	OperationTimeout time.Duration
	Emit             func(Result)
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
	killPolicy, stopPolicyWarnings := process.ParseStopPolicy(tree)
	selectors, selectorWarnings := process.ParseSelectors(tree)
	hasCommandMatch := hasCommandMatchSelector(selectors)
	configErr := firstWarningError(
		warningError("stop_policy", stopPolicyWarnings),
		warningError("selector config", selectorWarnings),
	)

	discover := func() ([]process.Process, error) {
		procs, warnings := c.Discoverer.Discover(selectors)
		if len(warnings) > 0 && !hasCommandMatch {
			return procs, warningError("runtime discovery", warnings)
		}
		return procs, nil
	}

	reaper := process.Reaper{
		Signaler:    process.OSSignaler{},
		ResolveUser: process.OSUserResolver,
		Sleep:       sleep,
	}

	ttl := c.LockTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	return Engine{
		Service:     c.Service,
		Unit:        c.Unit,
		Backend:     c.Backend,
		AlsoUnits:   c.AlsoUnits,
		ConfigError: configErr,
		Manager:     c.Manager,
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
		Guard:            guardClosure(tree, deps),
		Preflight:        sectionRunner(tree, "preflight", deps),
		Postflight:       sectionRunner(tree, "postflight", deps),
		ReloadFunc:       reloadClosure(tree, deps, c.Manager, c.Unit),
		Discover:         discover,
		Reaper:           reaper,
		KillPolicy:       killPolicy,
		Sleep:            sleep,
		OperationTimeout: ResolveTimeout(c.OperationTimeout, tree),
		Emit:             c.Emit,
	}
}

// reloadClosure builds the engine's reload step: a daemon-declared
// `commands.reload` (e.g. `systemctl daemon-reload`, `nginx -s reload`) runs
// through the command runner; otherwise it falls back to the backend's per-unit
// reload (systemctl reload <unit> / rc-service <svc> reload).
func reloadClosure(tree map[string]any, deps checks.Deps, mgr Manager, unit string) func(context.Context) error {
	if argv := reloadCommand(tree); len(argv) > 0 {
		runner := deps.Runner
		return func(ctx context.Context) error {
			res, err := runner.Run(ctx, argv[0], argv[1:]...)
			if err != nil {
				return err
			}
			if res.ExitCode != 0 {
				msg := strings.TrimSpace(res.Stderr)
				if msg == "" {
					msg = strings.TrimSpace(res.Stdout)
				}
				return fmt.Errorf("reload command exited %d: %s", res.ExitCode, msg)
			}
			return nil
		}
	}
	return func(ctx context.Context) error { return mgr.Reload(ctx, unit) }
}

// reloadCommand extracts the optional `commands.reload` command array.
func reloadCommand(tree map[string]any) []string {
	cmds, _ := tree["commands"].(map[string]any)
	if cmds == nil {
		return nil
	}
	r, _ := cmds["reload"].(map[string]any)
	if r == nil {
		return nil
	}
	return cfgval.StringArray(r["command"])
}

func hasCommandMatchSelector(selectors []process.Selector) bool {
	for _, sel := range selectors {
		if sel.Type == process.SelectorCommandMatch {
			return true
		}
	}
	return false
}

func warningError(prefix string, warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", prefix, strings.Join(warnings, "; "))
}

func firstWarningError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
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
