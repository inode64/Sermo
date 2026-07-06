package operation

import (
	"context"
	"errors"
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

	Manager     Manager
	Locker      *locks.OperationLocker
	Scanner     locks.Scanner
	Discoverer  process.Discoverer
	ResolveUser process.UserResolver
	CheckDeps   checks.Deps // Runner/HTTPClient/DefaultTimeout; Status is filled in
	// MetricSample supplies fresh metric readings for preflight/verification/guard
	// evaluation when CheckDeps.Metrics is unset. Optional.
	MetricSample func(context.Context) checks.MetricReader
	// Changed reports whether a watched library/config path differs from its
	// acknowledged baseline for `changed:` guard conditions. Optional.
	Changed          func(string) (bool, error)
	LockTTL          time.Duration
	Sleep            func(time.Duration)
	OperationTimeout time.Duration
	Emit             func(Result)
}

// New builds an Engine from real components, deriving preflight/verification/guard
// closures, residual discovery, the kill policy and the reaper from the resolved
// config tree.
func New(c Config) Engine {
	// Leave sleep nil when unset so process.Wait uses its cancellable timer in
	// production (no goroutine leak on a cancelled stop); tests inject a fake.
	sleep := c.Sleep

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
		reloadConfigError(tree),
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
		Guard:            guardClosure(tree, deps, c.MetricSample, c.Changed),
		Preflight:        sectionRunner(tree, "preflight", deps, c.MetricSample),
		Postflight:       verifyRunner(tree, deps, c.MetricSample),
		ReloadFunc:       reloadClosure(tree, deps, c.Manager, c.Backend, c.Unit, c.Discoverer, selectors),
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
// rc-service <svc> reload). A `reload:` block adds a native reload — a signal to
// the main process or a command — that either overrides the backend reload
// (`when: always`) or stands in for it only when the init backend cannot reload
// the unit itself (`when: auto`, the default).
func reloadClosure(tree map[string]any, deps checks.Deps, mgr Manager, backend, unit string, discoverer process.Discoverer, selectors []process.Selector) func(context.Context) error {
	initReload := func(ctx context.Context) error { return mgr.Reload(ctx, unit) }

	spec := parseReloadSpec(tree)
	if spec == nil {
		return initReload
	}
	native := nativeReloadFunc(spec, deps, backend, unit, tree, discoverer, selectors)
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

// ReloadSupported reports whether the resolved service can perform a reload
// action without waiting for the operation to fail at execution time.
func ReloadSupported(ctx context.Context, tree map[string]any, mgr Manager, unit string) (bool, error) {
	if err := reloadConfigError(tree); err != nil {
		return false, err
	}
	if parseReloadSpec(tree) != nil {
		return true, nil
	}
	if mgr == nil {
		return false, errors.New("reload support unavailable: no service manager")
	}
	if unit == "" {
		return false, errors.New("reload support unavailable: empty unit")
	}
	return mgr.SupportsReload(ctx, unit)
}

// reloadSpec is the parsed native-reload declaration: exactly one of command or
// signal, plus whether it always replaces the backend reload (`when: always`) or
// only fills in when the init cannot.
type reloadSpec struct {
	command []string
	signal  syscall.Signal
	hasSig  bool
	always  bool
}

// reloadConfigError reports an invalid native reload declaration that validation
// should have rejected but must not be silently ignored at runtime.
func reloadConfigError(tree map[string]any) error {
	r, ok := tree["reload"].(map[string]any)
	if !ok {
		return nil
	}
	if name := cfgval.AsString(r["signal"]); name != "" {
		if _, err := process.ParseSignal(name); err != nil {
			return fmt.Errorf("reload.signal: %w", err)
		}
		return nil
	}
	if argv := cfgval.StringArray(r["command"]); len(argv) > 0 {
		return nil
	}
	return fmt.Errorf("reload: block declares no command or signal")
}

// parseReloadSpec reads the native reload from the `reload:` block. It returns
// nil when the block is absent or empty/invalid; the engine then uses the plain
// backend reload.
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
	return nil
}

// nativeReloadFunc turns a reloadSpec into the closure that performs the reload:
// running its command, or sending its signal to the service's main process.
func nativeReloadFunc(spec *reloadSpec, deps checks.Deps, backend, unit string, tree map[string]any, discoverer process.Discoverer, selectors []process.Selector) func(context.Context) error {
	if spec.hasSig {
		pidfile := reloadPidfile(tree)
		return func(ctx context.Context) error {
			pid, source, err := reloadPID(ctx, deps.Runner, backend, unit, pidfile)
			if err != nil {
				return err
			}
			if source == reloadPIDPidfile {
				if _, ok := discoverer.StrictMatchPID(pid, selectors); !ok {
					return fmt.Errorf("reload: pidfile %q resolved pid %d, but it does not match any process selector with exact exe and user", pidfile, pid)
				}
			}
			if source == reloadPIDMain && hasCommandMatchSelector(selectors) {
				if _, ok := discoverer.StrictMatchPID(pid, selectors); !ok {
					return fmt.Errorf("reload: MainPID %d does not match any process selector with exact exe and user", pid)
				}
			}
			if err := ctx.Err(); err != nil {
				return reloadContextError(err)
			}
			return process.OSSignaler{}.Signal(pid, spec.signal)
		}
	}
	argv := spec.command
	runner := deps.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	return func(ctx context.Context) error {
		res, err := runner.Run(ctx, argv[0], argv[1:]...)
		if err != nil {
			if res.ExitCode == -1 {
				msg := execx.OperatorFailure(err, res, 0)
				if msg == "" {
					msg = execx.CommandDidNotStart
				}
				return errors.New(msg)
			}
			return err
		}
		if res.ExitCode == -1 {
			return errors.New(execx.CommandDidNotStart)
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
type reloadPIDSource string

const (
	reloadPIDMain    reloadPIDSource = "mainpid"
	reloadPIDPidfile reloadPIDSource = "pidfile"
)

func reloadPID(ctx context.Context, runner execx.Runner, backend, unit, pidfile string) (int, reloadPIDSource, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", reloadContextError(err)
	}
	if pid, ok := servicemgr.MainPIDContext(ctx, runner, servicemgr.Backend(backend), unit); ok {
		return pid, reloadPIDMain, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, "", reloadContextError(err)
	}
	if pidfile != "" {
		pid, err := process.ReadPidfile(pidfile)
		return pid, reloadPIDPidfile, err
	}
	return 0, "", fmt.Errorf("reload: cannot resolve a pid to signal — the backend exposes no MainPID (OpenRC) and the service declares no pidfile:; add a pidfile: so the signal target can be found")
}

// reloadPidfile returns the service's top-level pidfile path, used as the signal
// target when the backend has no MainPID (OpenRC).
func reloadContextError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(execx.ContextFailure(err, 0))
}

func reloadPidfile(tree map[string]any) string {
	if paths := cfgval.StringList(tree["pidfile"]); len(paths) > 0 {
		return paths[0]
	}
	return ""
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

func checkDepsForEval(ctx context.Context, deps checks.Deps, sample func(context.Context) checks.MetricReader) checks.Deps {
	if deps.Metrics == nil && sample != nil {
		deps.Metrics = sample(ctx)
	}
	return deps
}

// sectionRunner builds and runs a checks/preflight section, returning its
// evaluated outcome. A missing section is a trivial pass.
func sectionRunner(tree map[string]any, section string, deps checks.Deps, sample func(context.Context) checks.MetricReader) func(context.Context) checks.Outcome {
	return func(ctx context.Context) checks.Outcome {
		entries, _ := tree[section].(map[string]any)
		built, warnings := checks.BuildWithWarnings(entries, checkDepsForEval(ctx, deps, sample))
		results := checks.BuildWarningResults(warnings)
		results = append(results, checks.Run(ctx, built, 0)...)
		return checks.Evaluate(results)
	}
}

// verifyRunner builds the post-operation verification outcome from every check
// flagged `verify: true`, run once. It replaces the dedicated postflight: section:
// the same health probe used for periodic monitoring doubles as the
// start-verification, run immediately with the standard required/optional model
// (a check's for/within window and then action are irrelevant here — only
// Result.OK counts). A service with no verify checks is a trivial pass, exactly
// as a missing postflight section was.
func verifyRunner(tree map[string]any, deps checks.Deps, sample func(context.Context) checks.MetricReader) func(context.Context) checks.Outcome {
	return func(ctx context.Context) checks.Outcome {
		section := collectVerifyChecks(tree)
		built, warnings := checks.BuildWithWarnings(section, checkDepsForEval(ctx, deps, sample))
		results := checks.BuildWarningResults(warnings)
		results = append(results, checks.Run(ctx, built, 0)...)
		return checks.Evaluate(results)
	}
}

// collectVerifyChecks returns the resolved service checks flagged `verify: true`
// — the post-operation health verifiers — as a fresh section map. Check-only
// service watches have already been desugared into `checks:` by config
// resolution.
func collectVerifyChecks(tree map[string]any) map[string]any {
	out := map[string]any{}
	section, _ := tree["checks"].(map[string]any)
	for name, raw := range section {
		if entry, ok := raw.(map[string]any); ok && cfgval.Bool(entry["verify"]) {
			out[name] = entry
		}
	}
	return out
}

// guardClosure runs the service's named checks once, caches them, and evaluates
// the guard rules against that cache plus inline probes.
func guardClosure(tree map[string]any, deps checks.Deps, sample func(context.Context) checks.MetricReader, changed func(string) (bool, error)) func(context.Context, string) (bool, string, error) {
	return func(ctx context.Context, action string) (bool, string, error) {
		runDeps := checkDepsForEval(ctx, deps, sample)
		ruleSet, _ := rules.ParseRules(tree)
		section, _ := tree["checks"].(map[string]any)
		built, _ := checks.Build(section, runDeps)
		preflightSection, _ := tree["preflight"].(map[string]any)
		preflightBuilt, _ := checks.Build(preflightSection, runDeps)
		cache := map[string]checks.Result{}
		for _, r := range checks.Run(ctx, built, 0) {
			cache[r.Check] = r
		}
		ev := &rules.Evaluator{Cache: cache, ResolveRef: rules.NewCheckResolver(preflightBuilt, 0), Deps: runDeps, Changed: changed}
		return rules.Guard(ctx, ruleSet, action, ev)
	}
}
