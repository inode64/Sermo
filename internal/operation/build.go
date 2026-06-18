package operation

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
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
	Tree    map[string]any // resolved service config; New derives also_service units
	//                        and the stop_policy invariants from it

	Manager          Manager
	Locker           *locks.OperationLocker
	Scanner          locks.Scanner
	Discoverer       process.Discoverer
	ResolveUser      process.UserResolver
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
	// Derive the also_service units and stop_policy invariants from the resolved
	// tree here, consistent with how the kill policy/selectors below are parsed —
	// callers pass only the Tree, not pre-parsed forms.
	alsoUnits := config.AdditionalUnits(tree, c.Backend)
	stopArtifacts := stopArtifactsFromTree(tree)
	killPolicy, stopPolicyWarnings := process.ParseStopPolicy(tree)
	selectors, selectorWarnings := process.ParseSelectors(tree)
	hasCommandMatch := hasCommandMatchSelector(selectors)
	configErr := firstWarningError(
		warningError("stop_policy", stopPolicyWarnings),
		warningError("selector config", selectorWarnings),
	)

	// This closure is the Engine's residual discovery and the reaper's
	// Rediscover: it runs after a stop and between every SIGTERM/SIGKILL round.
	// It must read live /proc, never a shared monitoring snapshot — acting on a
	// stale process table would escalate SIGKILL against PIDs that already exited
	// (and may have been reused), defeating the reaper's per-round identity
	// re-check (safety invariants 1, 4, 12). So invalidate the cache first when
	// the reader is a CachingReader.
	discover := func() ([]process.Process, error) {
		if inv, ok := c.Discoverer.Reader.(interface{ Invalidate() }); ok {
			inv.Invalidate()
		}
		procs, warnings := c.Discoverer.Discover(selectors)
		if len(warnings) > 0 && !hasCommandMatch {
			return procs, warningError("runtime discovery", warnings)
		}
		return procs, nil
	}

	resolveUser := c.ResolveUser
	if resolveUser == nil {
		resolveUser = c.Discoverer.ResolveUser
	}
	reaper := process.Reaper{
		Signaler:    process.OSSignaler{},
		ResolveUser: resolveUser,
		Sleep:       sleep,
	}

	ttl := c.LockTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	return Engine{
		Service:       c.Service,
		Unit:          c.Unit,
		Backend:       c.Backend,
		AlsoUnits:     alsoUnits,
		StopArtifacts: stopArtifacts,
		ConfigError:   configErr,
		Manager:       c.Manager,
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
		ReloadFunc:       reloadClosure(tree, deps, c.Manager, c.Backend, c.Unit),
		ResumeFunc:       resumeClosure(c.Manager, c.Unit),
		Discover:         discover,
		Reaper:           reaper,
		KillPolicy:       killPolicy,
		Sleep:            sleep,
		OperationTimeout: ResolveTimeout(c.OperationTimeout, tree),
		Emit:             c.Emit,
	}
}

type resumeManager interface {
	Resume(ctx context.Context, service string) error
}

func resumeClosure(mgr Manager, unit string) func(context.Context) error {
	rm, ok := mgr.(resumeManager)
	if !ok {
		return nil
	}
	return func(ctx context.Context) error {
		return rm.Resume(ctx, unit)
	}
}

// stopArtifactsFromTree maps a resolved service's stop_policy invariants into the
// engine's StopArtifacts form. It is the single config→engine translation of the
// stopped-state invariants, shared by every engine build (daemon, web, CLI).
func stopArtifactsFromTree(tree map[string]any) StopArtifacts {
	pp, ff, cleanEnabled, clean := config.StopInvariants(tree)
	return StopArtifacts{PidfilePaths: pp, Files: ff, CleanEnabled: cleanEnabled, Clean: clean}
}

// reloadClosure builds the engine's reload step. With no native reload declared
// it asks the backend to reload in place (systemctl reload <unit> /
// rc-service <svc> reload). A `reload:` block (or legacy commands.reload) adds a
// native reload — a signal to the main process or a command — that either
// overrides the backend reload (`when: always`) or stands in for it only when the
// init backend cannot reload the unit itself (`when: auto`, the default).
func reloadClosure(tree map[string]any, deps checks.Deps, mgr Manager, backend, unit string) func(context.Context) error {
	initReload := func(ctx context.Context) error { return mgr.Reload(ctx, unit) }

	spec := parseReloadSpec(tree)
	if spec == nil {
		return initReload
	}
	native := nativeReloadFunc(spec, deps, backend, unit, tree)
	if spec.always {
		return native
	}
	// `when: auto` — prefer the backend reload, fall back to the native reload only
	// when the unit/script exposes no reload of its own.
	return func(ctx context.Context) error {
		if ok, _ := mgr.SupportsReload(ctx, unit); ok {
			return initReload(ctx)
		}
		return native(ctx)
	}
}

// reloadSpec is the parsed native-reload declaration: exactly one of command or
// signal, plus whether it always replaces the backend reload (legacy
// commands.reload and `when: always`) or only fills in when the init cannot.
type reloadSpec struct {
	command []string
	signal  syscall.Signal
	hasSig  bool
	always  bool
}

// parseReloadSpec reads the native reload from the `reload:` block, falling back
// to a legacy `commands.reload` command (treated as `when: always`). A present
// `reload:` block fully shadows any legacy `commands.reload`: even an
// empty/invalid block (which validation rejects, so it cannot reach runtime)
// returns nil here rather than consulting the legacy command. It returns nil when
// neither is present or the block is empty/invalid; the engine then uses the
// plain backend reload. Note a bare `reload: { command: [...] }` defaults to
// `when: auto` (prefer the init reload), unlike legacy `commands.reload` which is
// always `when: always`.
func parseReloadSpec(tree map[string]any) *reloadSpec {
	if r, ok := tree["reload"].(map[string]any); ok {
		spec := &reloadSpec{always: cfgval.AsString(r["when"]) == "always"}
		if name := cfgval.AsString(r["signal"]); name != "" {
			sig, err := process.ParseSignal(name)
			if err != nil {
				return nil
			}
			spec.signal, spec.hasSig = sig, true
			return spec
		}
		if argv := cfgval.StringArray(r["command"]); len(argv) > 0 {
			spec.command = argv
			return spec
		}
		return nil
	}
	if argv := reloadCommand(tree); len(argv) > 0 {
		return &reloadSpec{command: argv, always: true}
	}
	return nil
}

// nativeReloadFunc turns a reloadSpec into the closure that performs the reload:
// running its command, or sending its signal to the service's main process.
func nativeReloadFunc(spec *reloadSpec, deps checks.Deps, backend, unit string, tree map[string]any) func(context.Context) error {
	if spec.hasSig {
		pidfile := reloadPidfile(tree)
		return func(ctx context.Context) error {
			pid, err := reloadPID(deps.Runner, backend, unit, pidfile)
			if err != nil {
				return err
			}
			return process.OSSignaler{}.Signal(pid, spec.signal)
		}
	}
	argv := spec.command
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

// reloadPID resolves the process to signal for a native reload: systemd's MainPID
// when available, otherwise the service's pidfile (the only source on OpenRC).
func reloadPID(runner execx.Runner, backend, unit, pidfile string) (int, error) {
	if pid, ok := servicemgr.MainPID(runner, servicemgr.Backend(backend), unit); ok {
		return pid, nil
	}
	if pidfile != "" {
		return process.ReadPidfile(pidfile)
	}
	return 0, fmt.Errorf("reload: cannot resolve a pid to signal — the backend exposes no MainPID (OpenRC) and the service declares no pidfile:; add a pidfile: so the signal target can be found")
}

// reloadPidfile returns the service's pidfile path from its processes section (a
// pidfile selector, as produced by the `pidfile:` shorthand), used as the signal
// target when the backend has no MainPID (OpenRC).
func reloadPidfile(tree map[string]any) string {
	procs, ok := tree["processes"].(map[string]any)
	if !ok {
		return ""
	}
	for _, v := range procs {
		if m, ok := v.(map[string]any); ok && cfgval.AsString(m["type"]) == "pidfile" {
			if paths := cfgval.StringList(m["path"]); len(paths) > 0 {
				return paths[0]
			}
		}
	}
	return ""
}

// reloadCommand extracts the optional `commands.reload` command array — one of
// the reserved commands: entries features consume (see docs/daemons.md
// "Auxiliary commands"); the `reload:` block is the other reload mechanism.
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
		built, warnings := checks.BuildWithWarnings(entries, deps)
		results := checks.BuildWarningResults(warnings)
		results = append(results, checks.Run(ctx, built, 0)...)
		return checks.Evaluate(results)
	}
}

// guardClosure runs the service's named checks once, caches them, and evaluates
// the guard rules against that cache plus inline probes (sections 14, 17).
func guardClosure(tree map[string]any, deps checks.Deps) func(context.Context, string) (bool, string, error) {
	return func(ctx context.Context, action string) (bool, string, error) {
		ruleSet, _ := rules.ParseRules(tree)
		section, _ := tree["checks"].(map[string]any)
		built, _ := checks.Build(section, deps)
		preflightSection, _ := tree["preflight"].(map[string]any)
		preflightBuilt, _ := checks.Build(preflightSection, deps)
		cache := map[string]checks.Result{}
		for _, r := range checks.Run(ctx, built, 0) {
			cache[r.Check] = r
		}
		ev := &rules.Evaluator{Cache: cache, ResolveRef: rules.NewCheckResolver(preflightBuilt, 0), Deps: deps}
		return rules.Guard(ctx, ruleSet, action, ev)
	}
}
