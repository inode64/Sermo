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

const (
	defaultLockTTL = 5 * time.Minute
	// lockTTLMargin keeps the operation lock alive past the operation it guards,
	// covering the time between the engine's operation timeout firing and the
	// deferred lock release running.
	lockTTLMargin = time.Minute

	reloadCommandPath             = config.SectionReload + "." + config.ReloadKeyCommand
	reloadCommandLabel            = config.SectionReload + " " + config.ReloadKeyCommand
	reloadSignalPath              = config.SectionReload + "." + config.ReloadKeySignal
	reloadSupportLabel            = config.SectionReload + " support"
	runtimeDiscoveryWarningPrefix = "runtime discovery"
	selectorWarningPrefix         = "selector config"
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
				return "", fmt.Errorf("status %s: %w", unit, err)
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
		warningError(process.SectionStopPolicy, stopPolicyWarnings),
		warningError(selectorWarningPrefix, selectorWarnings),
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
			return procs, warningError(runtimeDiscoveryWarningPrefix, warnings)
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
		// The operation lock must outlive the operation it guards. A long
		// graceful stop can run past a fixed default and expire the lock
		// mid-flight, letting a second operation run concurrently on the same
		// service (whose SIGKILL could then hit the freshly restarted PID).
		// Derive the TTL from the effective operation timeout — which already
		// accounts for stop_policy escalation — plus a margin.
		ttl = max(ResolveTimeout(c.OperationTimeout, c.Tree)+lockTTLMargin, defaultLockTTL)
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
				return nil, fmt.Errorf("acquire operation lock for %s: %w", c.Service, err)
			}
			return h.Release, nil
		},
		LockTTL: ttl,
		NamedLocks: func() ([]locks.Lock, error) {
			report, err := c.Scanner.Scan(c.Service)
			if err != nil {
				return nil, fmt.Errorf("scan locks for %s: %w", c.Service, err)
			}
			return report.Locks, nil
		},
		Guard:            guardClosure(tree, deps, c.MetricSample, c.Changed),
		Preflight:        sectionRunner(tree, deps, c.MetricSample),
		Postflight:       verifyRunner(tree, deps, c.MetricSample),
		RestartIdentity:  restartIdentityClosure(c.Manager, c.Unit, discover, c.Discoverer, selectors),
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
	backendReload := func(ctx context.Context) error { return mgr.Reload(ctx, unit) }
	initReload := func(ctx context.Context) error {
		ok, err := mgr.SupportsReload(ctx, unit)
		if err != nil {
			return fmt.Errorf("%s: %w", reloadSupportLabel, err)
		}
		if !ok {
			return UnsupportedReloadError(unit)
		}
		return backendReload(ctx)
	}

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
		ok, err := mgr.SupportsReload(ctx, unit)
		if err != nil {
			return fmt.Errorf("%s: %w", reloadSupportLabel, err)
		}
		if ok {
			return backendReload(ctx)
		}
		return native(ctx)
	}
}

// UnsupportedReloadError reports a reload action rejected before execution
// because neither the init backend nor a native fallback can reload the unit.
func UnsupportedReloadError(unit string) error {
	return fmt.Errorf("service %s does not support reload: init backend reports no reload and no %s or %s is configured", unit, reloadCommandPath, reloadSignalPath)
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
	supported, err := mgr.SupportsReload(ctx, unit)
	if err != nil {
		return false, fmt.Errorf("reload support for %s: %w", unit, err)
	}
	return supported, nil
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
	r, ok := tree[config.SectionReload].(map[string]any)
	if !ok {
		return nil
	}
	if name := cfgval.AsString(r[config.ReloadKeySignal]); name != "" {
		if _, err := process.ParseSignal(name); err != nil {
			return fmt.Errorf("%s: %w", reloadSignalPath, err)
		}
		return nil
	}
	if argv := cfgval.StringArray(r[config.ReloadKeyCommand]); len(argv) > 0 {
		return nil
	}
	return fmt.Errorf("%s: block declares no %s or %s", config.SectionReload, config.ReloadKeyCommand, config.ReloadKeySignal)
}

// parseReloadSpec reads the native reload from the `reload:` block. It returns
// nil when the block is absent or empty/invalid; the engine then uses the plain
// backend reload.
func parseReloadSpec(tree map[string]any) *reloadSpec {
	if r, ok := tree[config.SectionReload].(map[string]any); ok {
		spec := &reloadSpec{always: cfgval.AsString(r[config.ReloadKeyWhen]) == config.ReloadWhenAlways}
		if name := cfgval.AsString(r[config.ReloadKeySignal]); name != "" {
			sig, err := process.ParseSignal(name)
			if err != nil {
				return nil
			}
			spec.signal, spec.hasSig = sig, true
			return spec
		}
		if argv := cfgval.StringArray(r[config.ReloadKeyCommand]); len(argv) > 0 {
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
		return nativeSignalReloadFunc(spec.signal, deps.Runner, backend, unit, reloadPidfile(tree), discoverer, selectors)
	}
	return nativeCommandReloadFunc(spec.command, deps.Runner)
}

func nativeSignalReloadFunc(signal syscall.Signal, runner execx.Runner, backend, unit, pidfile string, discoverer process.Discoverer, selectors []process.Selector) func(context.Context) error {
	return func(ctx context.Context) error {
		pid, source, err := reloadPID(ctx, runner, backend, unit, pidfile)
		if err != nil {
			return err
		}
		if err := verifyReloadPID(pid, source, pidfile, discoverer, selectors); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return reloadContextError(err)
		}
		return process.OSSignaler{}.Signal(pid, signal)
	}
}

func verifyReloadPID(pid int, source reloadPIDSource, pidfile string, discoverer process.Discoverer, selectors []process.Selector) error {
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
	return nil
}

func nativeCommandReloadFunc(argv []string, runner execx.Runner) func(context.Context) error {
	runner = execx.RunnerOrDefault(runner)
	return func(ctx context.Context) error {
		res, err := runner.Run(ctx, argv[0], argv[1:]...)
		return reloadCommandError(res, err)
	}
}

func reloadCommandError(res execx.Result, err error) error {
	if err != nil {
		if res.ExitCode == execx.ExitCodeRunFailure {
			msg := execx.OperatorFailureOr(err, res, execx.NoTimeout, execx.CommandDidNotStart)
			return errors.New(msg)
		}
		return fmt.Errorf("%s: %w", reloadCommandLabel, err)
	}
	if res.ExitCode == execx.ExitCodeRunFailure {
		return errors.New(execx.CommandDidNotStart)
	}
	if res.ExitCode != execx.ExitCodeSuccess {
		message := strings.TrimSpace(res.Stderr)
		if message == "" {
			message = strings.TrimSpace(res.Stdout)
		}
		return fmt.Errorf("%s exited %d: %s", reloadCommandLabel, res.ExitCode, message)
	}
	return nil
}

// reloadPID resolves the process to signal for a native reload: systemd's MainPID
// when available, otherwise the service's pidfile (the only source on OpenRC).
type reloadPIDSource string

const (
	reloadPIDMain    reloadPIDSource = "mainpid"
	reloadPIDPidfile reloadPIDSource = config.ServiceKeyPidfile
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
		if err != nil {
			return 0, "", fmt.Errorf("read pidfile %s: %w", pidfile, err)
		}
		return pid, reloadPIDPidfile, nil
	}
	return 0, "", fmt.Errorf("reload: cannot resolve a pid to signal: backend exposes no MainPID (OpenRC) and the service declares no %s; add %s: so the signal target can be found", config.ServiceKeyPidfile, config.ServiceKeyPidfile)
}

// reloadContextError builds the operator-facing context failure as a
// package-local error (wrapcheck requires returned errors to originate here);
// callers outside such a constraint can use execx.ContextError directly.
func reloadContextError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(execx.ContextFailure(err, execx.NoTimeout))
}

// reloadPidfile returns the service's top-level pidfile path, used as the signal
// target when the backend has no MainPID (OpenRC).
func reloadPidfile(tree map[string]any) string {
	if paths := cfgval.StringList(tree[config.ServiceKeyPidfile]); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

func hasCommandMatchSelector(selectors []process.Selector) bool {
	for i := range selectors {
		if selectors[i].Type == process.SelectorCommandMatch {
			return true
		}
	}
	return false
}

func hasExactProcessIdentitySelector(selectors []process.Selector) bool {
	for i := range selectors {
		if selectors[i].Type == process.SelectorCommandMatch && selectors[i].Exe != "" && selectors[i].User != "" {
			return true
		}
	}
	return false
}

func restartIdentityClosure(mgr Manager, unit string, discover func() ([]process.Process, error), discoverer process.Discoverer, selectors []process.Selector) func(context.Context) (bool, string, error) {
	if mgr == nil || discover == nil || !hasExactProcessIdentitySelector(selectors) {
		return nil
	}
	return func(ctx context.Context) (bool, string, error) {
		st, err := mgr.Status(ctx, unit)
		if err != nil {
			return false, "", fmt.Errorf("status %s: %w", unit, err)
		}
		if st.Status != servicemgr.StatusActive {
			return true, "", nil
		}
		procs, err := discover()
		if err != nil {
			return false, "", err
		}
		for i := range procs {
			if _, ok := discoverer.StrictMatchPID(procs[i].PID, selectors); ok {
				return true, "", nil
			}
		}
		return false, "blocked: active service has no process matching configured exact exe/user selectors", nil
	}
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

// sectionRunner builds and runs the checks/preflight section, returning its
// evaluated outcome. A missing section is a trivial pass.
func sectionRunner(tree map[string]any, deps checks.Deps, sample func(context.Context) checks.MetricReader) func(context.Context) checks.Outcome {
	return func(ctx context.Context) checks.Outcome {
		entries, _ := tree[config.SectionPreflight].(map[string]any)
		return runCheckSection(ctx, entries, deps, sample)
	}
}

// runCheckSection builds and evaluates one resolved check section; the
// execution shared by the preflight and verify runners.
func runCheckSection(ctx context.Context, entries map[string]any, deps checks.Deps, sample func(context.Context) checks.MetricReader) checks.Outcome {
	built, warnings := checks.BuildWithWarnings(entries, checkDepsForEval(ctx, deps, sample))
	results := checks.BuildWarningResults(warnings)
	results = append(results, checks.Run(ctx, built, 0)...)
	return checks.Evaluate(results)
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
		return runCheckSection(ctx, collectVerifyChecks(tree), deps, sample)
	}
}

// collectVerifyChecks returns the resolved service checks flagged `verify: true`
// — the post-operation health verifiers — as a fresh section map. Check-only
// service watches have already been desugared into `checks:` by config
// resolution.
func collectVerifyChecks(tree map[string]any) map[string]any {
	out := map[string]any{}
	section, _ := tree[config.SectionChecks].(map[string]any)
	for name, raw := range section {
		if entry, ok := raw.(map[string]any); ok && cfgval.Bool(entry[checks.CheckKeyVerify]) {
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
		section, _ := tree[config.SectionChecks].(map[string]any)
		built, _ := checks.Build(section, runDeps)
		preflightSection, _ := tree[config.SectionPreflight].(map[string]any)
		preflightBuilt, _ := checks.Build(preflightSection, runDeps)
		cache := map[string]checks.Result{}
		for _, r := range checks.Run(ctx, built, 0) {
			cache[r.Check] = r
		}
		ev := &rules.Evaluator{Cache: cache, ResolveRef: rules.NewCheckResolver(preflightBuilt, 0), Deps: runDeps, Changed: changed}
		return rules.Guard(ctx, ruleSet, action, ev)
	}
}
