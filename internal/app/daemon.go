package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/emission"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/telegrambot"
	"sermo/internal/web"
)

// MonitorStateStore is the minimal persistent store used to apply an explicit
// monitor or unmonitor transition.
type MonitorStateStore interface {
	SetActive(service string, active bool, source string) error
	MonitorState(service string) (state.MonitorRecord, bool, error)
}

// MonitorStore is the persistent monitoring-state store the daemon consults to
// decide whether a service or watch is actively monitored. It is implemented by
// internal/state.Store; kept as an interface so workers can be tested without a
// database. A nil store means "always monitor" (no persistence).
type MonitorStore interface {
	MonitorStateStore
	Active(service string) (active, found bool, err error)
	// Panic / SetPanic back the daemon-wide "panic mode" flag (a single global
	// row, not keyed by service). Panic mode suppresses hooks, alerts and
	// automatic remediation while keeping monitoring/status running.
	Panic() (state.GlobalRecord, bool, error)
	SetPanic(on bool, source string) error
}

// OperationSettlingStore persists short-lived service operation transitions so
// workers suppress alerts/remediation until one post-operation cycle has data.
type OperationSettlingStore interface {
	SetOperationSettling(service, phase string) error
	OperationSettling(service string) (state.OperationSettlingRecord, bool, error)
	ClearOperationSettling(service string) error
}

// ServiceRestartNoticeStore persists the principal process identity already
// handled by the external-restart notice monitor. It is separate from
// operation-settling state because it must survive ordinary daemon restarts.
type ServiceRestartNoticeStore interface {
	ServiceRestartNotice(service string) (state.ServiceRestartNoticeRecord, bool, error)
	SetServiceRestartNotice(service string, record state.ServiceRestartNoticeRecord) error
}

// SLARecorder persists one availability sample per observed monitoring cycle, so
// availability can be reported over rolling windows. Implemented by
// internal/state.Store; nil disables SLA tracking.
type SLARecorder interface {
	RecordSLA(service string, up bool, at time.Time) error
	RecordCheckSLA(service, check string, up bool, at time.Time) error
}

// SLAReader reports availability for the CLI report and the web: the rolling
// window totals and the history series a timeline is drawn from. Implemented by
// internal/state.Store.
type SLAReader interface {
	SLAReport(service string, now time.Time) ([]state.SLAValue, error)
	SLASeries(service string, from, to time.Time) ([]state.SLAPoint, error)
	CheckSLAReport(service, check string, now time.Time) ([]state.SLAValue, error)
	CheckSLASeries(service, check string, from, to time.Time) ([]state.SLAPoint, error)
}

// MeasurementRecorder persists per-check observations per observed cycle: the
// latency (ms) for measured check types, and any named metrics a check publishes
// in Result.Data (e.g. hdparm read/cached). Implemented by internal/state.Store.
type MeasurementRecorder interface {
	RecordMeasurement(service, check string, valueMs float64, at time.Time) error
	RecordMetric(service, check, metric string, value float64, at time.Time) error
}

// MeasurementReader reads a check's latency and named-metric summaries and history
// for the web. Implemented by internal/state.Store.
type MeasurementReader interface {
	MeasurementSummary(service, check string, span time.Duration, now time.Time) (state.MeasurementStat, error)
	MeasurementSeries(service, check string, from, to time.Time) ([]state.MeasurementPoint, error)
	MetricSummary(service, check, metric string, span time.Duration, now time.Time) (state.MeasurementStat, error)
	MetricSeries(service, check, metric string, from, to time.Time) ([]state.MeasurementPoint, error)
}

// DaemonMetricStore persists sermod's own process metrics so the daemon graphs
// survive process restarts. Implemented by internal/state.Store.
type DaemonMetricStore interface {
	RecordDaemonMetric(metric string, value float64, at time.Time) error
	DaemonMetricSummary(metric string, span time.Duration, now time.Time) (state.MeasurementStat, error)
	DaemonMetricSeries(metric string, from, to time.Time) ([]state.MeasurementPoint, error)
}

// ServiceMetricStore persists per-service process-tree runtime metrics so the
// service detail graphs survive daemon restarts. Implemented by
// internal/state.Store.
type ServiceMetricStore interface {
	RecordServiceMetric(service, metric string, value float64, at time.Time) error
	ServiceMetricSummary(service, metric string, span time.Duration, now time.Time) (state.MeasurementStat, error)
	ServiceMetricSeries(service, metric string, from, to time.Time) ([]state.MeasurementPoint, error)
}

// RuleStateStore persists automatic remediation and rule-window control state so
// daemon restarts do not reset cooldown/backoff or for/within progress.
type RuleStateStore interface {
	RemediationState(service string) (state.RemediationRecord, bool, error)
	SetRemediationState(service string, record state.RemediationRecord) error
	RuleWindowStates(service string) (map[string]state.RuleWindowRecord, error)
	SetRuleWindowStates(service string, records map[string]state.RuleWindowRecord) error
}

// WatchStateStore persists watch firing episodes, rule windows and action
// pacing so an unchanged condition does not fire again after a daemon restart.
type WatchStateStore interface {
	WatchRuntimeState(watch, slot string) (state.WatchRuntimeRecord, bool, error)
	SetWatchRuntimeState(watch, slot string, record state.WatchRuntimeRecord) error
}

// Deps are the host capabilities the daemon wires into each worker.
type Deps struct {
	Backend servicemgr.Backend
	Manager servicemgr.Manager
	// Targets memoizes service unit resolution for one build generation, so the
	// workers build and the web backend build probe each unit once and log each
	// resolution warning once. Optional: nil resolves directly. Create a fresh
	// cache per config load/reload.
	Targets *control.TargetCache
	// BackendPIDs reports backend-owned process roots for the resolved service
	// (systemd cgroup PIDs, Docker container init PID, etc.). Optional: nil lets
	// the runtime derive init-backend PIDs when that is supported.
	BackendPIDs      func() []int
	Runtime          string
	DefaultTimeout   time.Duration
	OperationTimeout time.Duration
	// WatchCheckDeps, when set, is the checks.Deps used to build a watch's inline
	// check instead of the host-global sampler subset. It carries per-service
	// context (backend status, PID-tree-scoped process counting) so a watch
	// declared inside a service (`watches:` in the service tree) is scoped to that
	// service's processes. nil for host watches. A `metric` check's source is
	// injected per watch by serviceWatches (a dedicated collector), not carried
	// here.
	WatchCheckDeps *checks.Deps
	// Interval is the global resolution (engine.interval). It is the base cycle
	// rate and the unit a per-check `interval` is rounded to (a check runs every
	// round(interval/resolution) cycles). A service's own `interval` overrides it.
	Interval    time.Duration
	MaxParallel int
	// ArtifactSamples shares catalog app/library/file observations with workers so
	// artifact changes are sampled at their artifact cadence, not per service cycle.
	ArtifactSamples *ArtifactSamples
	Sleep           func(time.Duration)
	Now             func() time.Time
	Emit            func(Event)
	// Monitor persists per-entry monitoring state (active/paused) across daemon
	// restarts and reboots. Optional: nil means every service/watch is always
	// monitored.
	Monitor MonitorStore
	// OperationSettling suppresses checks' side effects while a service operation
	// is running and through the first active post-operation cycle.
	OperationSettling OperationSettlingStore
	// ServiceRestartNotice persists principal-process identities so one external
	// restart emits at most one notice across sermod restarts. Optional: a
	// configured notice remains safely silent without durable state.
	ServiceRestartNotice ServiceRestartNoticeStore
	// RestartNotice is the validated global principal-process restart notice.
	// nil disables it for every service in this generation.
	RestartNotice *config.ServiceRestartNotice
	// Panic gates the daemon-wide panic mode (hooks, alerts and automatic
	// remediation suppressed). Optional: nil means panic mode is never on.
	Panic *PanicGate
	// RuleState persists automatic remediation cooldown/backoff and rule-window
	// progress. Optional: nil keeps those states in memory for this process only.
	RuleState RuleStateStore
	// WatchState persists host/application watch episodes, windows and action
	// pacing. Optional: nil keeps those states in memory for this process only.
	WatchState WatchStateStore
	// SLA persists per-cycle availability samples for SLA reporting. Optional: nil
	// disables SLA tracking.
	SLA SLARecorder
	// DaemonMetrics persists sermod's own process metric history for the web UI.
	// Optional: nil keeps only in-memory history for this process lifetime.
	DaemonMetrics DaemonMetricStore
	// DaemonMetricSampler is the daemon-owned sampler read by the web backend.
	// Optional: nil builds an unstarted read-only sampler for tests/non-daemon users.
	DaemonMetricSampler *DaemonMetricSampler
	// ProcSampler lists matching processes and their counters for `process`
	// watches. Optional: nil uses the host /proc.
	ProcSampler ProcSampler
	// ProcReader is the shared /proc identity source for service discovery. A
	// *process.CachingReader lets concurrent workers (and web runtime queries)
	// within one cycle share a single /proc walk instead of each scanning every
	// PID, cutting discovery from O(services × processes) to O(processes).
	// Optional: nil makes each discoverer read /proc directly.
	ProcReader process.Reader
	// Samplers are the shared host probes used by checks, watches and web
	// summaries. Service-specific dependencies stay on Deps itself.
	checks.Samplers
	// SSHSessionSampler presents terminal-aware evidence in the web UI and
	// verifies a requested close. It shares a separate procfs cache with the
	// embedded SSHIdleSampler because ordinary service discovery deliberately
	// avoids reading tty_nr for every process.
	SSHSessionSampler checks.SSHSessionSamplerFunc
	// SSHSessionVerifier must obtain a fresh terminal/process snapshot for a
	// close action. Optional nil uses an uncached native sampler.
	SSHSessionVerifier checks.SSHSessionSamplerFunc
	// TerminalProcessReader is the shared terminal-aware /proc snapshot used to
	// attribute tmux/screen process trees without another host-wide scan.
	TerminalProcessReader process.Reader
	// Notifiers are the configured delivery targets (email, …) addressable by name
	// from a watch's `then.notify`. Optional: nil/empty means no notifications.
	Notifiers map[string]notify.Notifier
	// GlobalNotify is the top-level `notify` default selection (notifier names): the
	// fallback for any notify site (watch or rule alert) that declares none of its
	// own. Empty means no default. See config.NotifyDefault.
	GlobalNotify []string
	// GlobalEmission is the top-level automatic event/notification cadence.
	GlobalEmission emission.Policy
	// GlobalClear is the fallback recovery (clear) window for watches that
	// declare no clear: of their own — defaults.clear_window, or the built-in
	// rules.DefaultClearWindow. Optional: nil leaves watch windows untouched
	// (tests, non-daemon builders). Service rules inherit theirs from the
	// merged service tree in rules.ParseRules instead.
	GlobalClear *rules.ForWindow
	// Snapshots collects each service's latest check results for the web detail
	// view. Optional: nil disables publishing.
	Snapshots *Snapshots
	// WatchSnapshots collects each host watch's latest daemon-cycle check result
	// for the web watch list. The daemon and web backend share this registry so
	// HTTP reads never run watches themselves.
	WatchSnapshots *WatchSnapshots
	// Remediation collects each service's remediation policy view for the web
	// detail. Optional: nil disables publishing.
	Remediation *RemediationRegistry
	// RuleWindows collects each service's rule window progress for the web detail.
	// Optional: nil disables publishing.
	RuleWindows *RuleWindowRegistry
	// Events is the recent-event log the web UI reads (global and per-service).
	// Optional: nil disables it. Wire it into Emit via MultiEmit to populate it.
	Events *EventLog
	// DiagnosticLog exports scheduled diagnostics to engine.diagnostics when set.
	DiagnosticLog *DiagnosticLog
	// TelegramBot is the interactive read-only report bot. Optional: nil when the
	// `telegram_bot` section is absent. It is refreshed on reload via UpdateConfig.
	TelegramBot *telegrambot.Bot
	// SystemFreshness caches system metrics so concurrent workers in one cycle
	// share a computation; it must be below the scheduler interval.
	SystemFreshness time.Duration
	// Collector provides live system and per-service metrics (cpu, memory, load).
	// Made available to the web UI for host overview.
	Collector *metrics.Collector
	// Live collects each service's per-cycle live CPU readings (per-process and
	// aggregate) for the web detail view. Optional: nil disables live CPU.
	Live *LiveMetrics
	// LiveCollector is a collector dedicated to the per-cycle live web CPU
	// sampling, kept separate from Collector so the two never corrupt each
	// other's rate deltas. Optional: nil disables live CPU sampling.
	LiveCollector *metrics.Collector
	// ServiceMetrics stores per-cycle service CPU, memory and IO samples for the
	// web detail graphs. Optional: nil leaves history empty; the web backend still
	// probes current process counters but never records samples from HTTP reads.
	ServiceMetrics *ServiceMetricSampler
	// Observability tracks when a service has completed a normal observed cycle
	// and has fresh indicators available for the web/CLI state view.
	Observability *ObservabilityRegistry
	// ExecxRunner is used for executing hook commands from watches (file, process,
	// and generic watches). If nil, OSHookRunner will use execx.CommandRunner{}.
	ExecxRunner execx.Runner
	// UserLookup resolves users/groups for process discovery, kill policies and
	// owner display. Optional: nil uses process.DefaultUserLookup.
	UserLookup *process.UserLookup
	// MountDiscoverUsers reports processes using a configured mount path for web
	// mount blockers and unmount escalation. Optional: nil scans /proc.
	MountDiscoverUsers func(string) ([]process.Process, error)
	// MountSignaler sends TERM/KILL during policy-gated web umount escalation.
	// Optional: nil uses process.OSSignaler.
	MountSignaler process.Signaler
	// SSHSessionSignaler sends the single SIGTERM used to close a freshly
	// revalidated interactive SSH session. Optional: nil uses process.OSSignaler.
	SSHSessionSignaler process.Signaler
	// ManagedSSHSessionCloser terminates an exact systemd-logind SSH session.
	// Optional: nil uses the native login1 D-Bus client on systemd services.
	ManagedSSHSessionCloser func(context.Context, operation.SessionTarget) error
	// MountUserAlerter sends a console alert to users blocking a web mount
	// operation. Optional: nil uses the native tty notifier.
	MountUserAlerter MountUserAlerter
	// VolumeExpander grows storage-watch filesystems for `then.expand`. Optional:
	// nil uses volume.Expander with ExecxRunner. Tests inject a fake so no real
	// LVM/filesystem commands run.
	VolumeExpander VolumeExpander
	// ClockStepper corrects the system clock for a clock watch's `then.makestep`.
	// Optional: nil uses conn.MakeStep. Tests inject a fake so no real chronyd is
	// ever commanded.
	ClockStepper ClockStepper
	// Settling tracks per-target startup observation for the web UI and suppresses
	// premature alerts and remediation. Optional: nil disables settling gates.
	Settling *Settling
}

// BuildWorkers resolves every enabled service and wires a Worker for it: a check
// cache producer and an operation-engine Operate closure. Services
// that are disabled or fail to resolve are skipped with a warning.
func BuildWorkers(ctx context.Context, cfg *config.Config, deps Deps, collector *metrics.Collector) ([]*Worker, []*Watch, []string) {
	var workers []*Worker
	var serviceWatchList []*Watch
	var warnings []string
	cascadeMap := map[string][]string{}
	if collector == nil {
		collector = metrics.New(metrics.OSReader{})
		if deps.SystemFreshness > 0 {
			collector.SystemFreshness = deps.SystemFreshness
		}
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = deps.Manager
	restartNotice, restartNoticeConfigured := config.EngineServiceRestartNotice(cfg)

	for _, name := range cfg.SortedServiceNames() {
		doc := cfg.Services[name]
		if doc == nil || cfgval.Disabled(doc.Body) {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 {
			warnings = append(warnings, "skip service "+name+": "+errs[0])
			continue
		}

		if w := applyMonitorMode(deps.Monitor, name, config.MonitorMode(resolved.Tree)); w != "" {
			warnings = append(warnings, w)
		}

		target, warn := resolveServiceTarget(ctx, deps, name, resolved.Tree, resolver)
		if warn != "" {
			warnings = append(warnings, serviceResolutionNotice(name, warn, resolved.Tree, deps.Backend))
		}
		if target.Unit == "" {
			continue
		}
		serviceDeps := deps
		serviceDeps.Backend = target.Backend
		serviceDeps.Manager = target.Manager
		serviceDeps.BackendPIDs = target.BackendPIDs
		if restartNoticeConfigured {
			serviceDeps.RestartNotice = &restartNotice
		}
		w, svcWatches, warns := buildWorker(ctx, name, target.Unit, resolved.Tree, serviceDeps, collector)
		for _, x := range warns {
			warnings = append(warnings, serviceSubjectPrefix+name+": "+x)
		}
		if t := config.CascadeTargets(resolved.Tree); len(t) > 0 {
			cascadeMap[name] = t
		}
		workers = append(workers, w)
		serviceWatchList = append(serviceWatchList, svcWatches...)
	}
	wireCascade(workers, cascadeMap, deps)
	return workers, serviceWatchList, warnings
}

// resolveServiceTarget resolves one service's control target, through the
// per-generation cache when the deps carry one.
func resolveServiceTarget(ctx context.Context, deps Deps, name string, tree map[string]any, resolver servicemgr.UnitResolver) (control.Target, string) {
	if deps.Targets != nil {
		return deps.Targets.ResolveWithFallback(ctx, name, tree, deps.Backend, deps.Manager, resolver)
	}
	return control.ResolveWithFallback(ctx, name, tree, deps.Backend, deps.Manager, resolver)
}

// serviceResolutionNotice renders one service's resolution warning, demoted to
// an informational notice when the service's own map declares no unit for the
// active backend (skipping it is the configuration working as written).
func serviceResolutionNotice(name, warn string, tree map[string]any, backend servicemgr.Backend) string {
	msg := serviceSubjectPrefix + name + ": " + warn
	if control.UnsupportedOnBackend(tree, backend, name) {
		return infoNotice(msg)
	}
	return msg
}

// wireCascade gives every worker whose service declares also_apply a Cascade
// closure that operates the service plus its additional services (resolved from
// this generation's worker set) in dependency order. The byName index is built
// once per generation and read-only thereafter, so concurrent cascades are safe.
func wireCascade(workers []*Worker, cascadeMap map[string][]string, deps Deps) {
	if len(cascadeMap) == 0 {
		return
	}
	byName := make(map[string]*Worker, len(workers))
	for _, w := range workers {
		byName[w.Service] = w
	}
	op := func(ctx context.Context, svc, action string) (operation.Result, error) {
		if tw := byName[svc]; tw != nil {
			return tw.operateForRemediation(ctx, action), nil
		}
		return operation.Result{Service: svc, Action: action, Status: operation.ResultFailed, Message: "cascade target not configured"}, nil
	}
	lookup := func(svc string) []string { return cascadeMap[svc] }
	for _, w := range workers {
		if len(cascadeMap[w.Service]) == 0 {
			continue
		}
		cfg := CascadeConfig{Operate: op, Lookup: lookup, Emit: deps.Emit}
		service := w.Service
		w.Cascade = func(ctx context.Context, action string) operation.Result {
			result, _ := RunCascade(ctx, service, action, cfg)
			return result
		}
	}
}

// appVersionCmds collects the resolved version command of every app the service
// declares, so a `changed: {app}` rule can sample the app's version. expandApps
// merges each app's preflight under "<app>-<check>"; the "<app>-version" entries
// carry the app version probe with variables already expanded. Keyed by app name.
func appVersionCmds(tree map[string]any) map[string]appVersionCmd {
	preflight, ok := tree[config.SectionPreflight].(map[string]any)
	if !ok {
		return nil
	}
	cmds := map[string]appVersionCmd{}
	for key, raw := range preflight {
		app, ok := strings.CutSuffix(key, config.ServiceMonitorVersionCheckSuffix)
		if !ok || app == "" {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		argv := cfgval.StringList(entry[checks.CheckKeyCommand])
		if len(argv) == 0 {
			continue
		}
		cmds[app] = appVersionCmd{
			argv:    argv,
			user:    cfgval.String(entry[checks.CheckKeyUser]),
			timeout: cfgval.Duration(entry[checks.CheckKeyTimeout]),
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return cmds
}

func buildWorker(ctx context.Context, name, unit string, tree map[string]any, deps Deps, collector *metrics.Collector) (*Worker, []*Watch, []string) {
	libBaseline := map[string]string{}
	runtime := BuildServiceRuntime(ctx, ServiceRuntimeConfig{
		Service: name, Unit: unit, Tree: tree, Deps: deps, LibraryBaseline: libBaseline,
	})
	engine, checkDeps, discoverer := runtime.Engine, runtime.CheckDeps, runtime.Discoverer

	maxParallel := deps.MaxParallel
	ruleSet, _ := rules.ParseRules(tree)
	selectors := runtime.Selectors
	noResident := runtime.NoResidentProcess
	var worker *Worker
	processesForCycle := cycleProcessSource(func() []process.Process {
		if noResident {
			return nil
		}
		procs, _ := discoverer.Discover(selectors)
		return procs
	}, func() int {
		if worker == nil {
			return 0
		}
		return worker.cycle
	})
	pidsForCycle := func() []int { return processPIDs(processesForCycle()) }
	primaryProcess := primaryProcessForCycle(processesForCycle, primaryStartReader(collector), deps.Now)
	// Reuse the cycle's memoized discovery: the stale-binary check runs once per
	// service per cycle, and rediscovering would repeat the whole selector sweep
	// (and, on systemd, the backend PID lookup's subprocesses) for a set that is
	// normally empty.
	checkDeps.StaleBinaries = func() []process.StaleBinary {
		return discoverer.StaleBinariesIn(processesForCycle(), selectors)
	}
	// Strays come from the same memoized discovery: classification already ran
	// inside Discover, so the check costs one slice filter per cycle.
	checkDeps.Strays = func() []process.Process { return process.Strays(processesForCycle()) }
	sampleMetrics := metricSampler(name, tree, collector, pidsForCycle)
	liveSample := liveSampler(name, deps.LiveCollector, deps.Live, deps.ServiceMetrics, processesForCycle, deps.Now)
	if noResident {
		liveSample = nil
	}

	// A per-check `interval` runs that check every N cycles (N rounded from
	// interval/resolution); skipped cycles reuse its last result so the cache and
	// rule windows stay complete. resolution is the service's own interval, or the
	// global one.
	resolution := cfgval.Duration(tree[config.EntryKeyInterval])
	if resolution <= 0 {
		resolution = deps.Interval
	}
	if resolution <= 0 {
		resolution = config.DefaultEngineInterval
	}
	every, warnings := checkIntervals(tree, resolution)

	cycleWriter := newCycleWriter(deps, name, tree)
	var recordMeasurement func(checks.Result)
	var recordCycle func(context.Context, cycleRecord)
	if cycleWriter != nil {
		recordMeasurement = cycleWriter.RecordMeasurement
		recordCycle = cycleWriter.RecordCycle
	}
	section, _ := tree[config.SectionChecks].(map[string]any)
	_, checkTypes, _ := checkCatalog(tree, resolution)
	built, checkWarnings, setCycleMetrics := buildWorkerCheckSet(section, checkDeps, sampleMetrics != nil)
	warnings = append(warnings, checkWarnings...)
	preflightSection, _ := tree[config.SectionPreflight].(map[string]any)
	preflightBuilt, preflightWarnings := checks.Build(preflightSection, checkDeps)
	warnings = append(warnings, preflightWarnings...)
	remediationState, windowStates, stateWarnings := loadRuleState(deps.RuleState, name, ruleSet)
	warnings = append(warnings, stateWarnings...)

	worker = &Worker{
		Service:              name,
		Unit:                 unit,
		Rules:                ruleSet,
		MetricChecks:         rules.ReferencedChecks(tree),
		Policy:               rules.ParsePolicy(tree),
		State:                remediationState,
		Notifiers:            deps.Notifiers,
		GlobalNotify:         deps.GlobalNotify,
		GlobalEmission:       deps.GlobalEmission,
		Remediation:          deps.Remediation,
		RuleWindows:          deps.RuleWindows,
		CheckDeps:            checkDeps,
		Interval:             cfgval.Duration(tree[config.EntryKeyInterval]),
		Gates:                parseCheckGates(tree),
		Sample:               sampleMetrics,
		LiveSample:           liveSample,
		Operate:              engine.Do,
		IsPaused:             monitorPaused(deps.Monitor, name),
		InPanic:              deps.Panic.Active,
		Settling:             deps.Settling,
		OperationSettling:    deps.OperationSettling,
		ServiceRestartNotice: deps.ServiceRestartNotice,
		RestartNotice:        deps.RestartNotice,
		PrimaryProcess:       primaryProcess,
		Observability:        deps.Observability,
		DryRun:               config.DryRun(tree),
		ResolveRefs:          func() rules.RefResolver { return rules.NewCheckResolver(preflightBuilt, maxParallel) },
		RecordCycle:          recordCycle,
		Publish:              publishSnapshots(deps.Snapshots, name, checkTypes),
		PersistState:         ruleStatePersister(deps.RuleState, deps.Emit, name, ruleSet),
		Now:                  deps.Now,
		Emit:                 deps.Emit,
		windows:              windowStates,
		libBaseline:          libBaseline,
		checkFailing:         checkFailingFromSnapshots(deps.Snapshots, name, checkTypes),
		artifactSamples:      deps.ArtifactSamples,

		appVersionCmd:   appVersionCmds(tree),
		appVersions:     map[string]string{},
		appVersionsLast: map[string]string{},
	}
	worker.Checks = workerCheckRunner(worker, built, every, maxParallel, recordMeasurement, setCycleMetrics)
	watchDeps := serviceWatchCheckDeps(checkDeps, discoverer, selectors)
	newMetricSource := watchMetricSourceFactory(name, discoverer, selectors, deps.SystemFreshness)
	watches, watchWarnings := serviceWatches(name, tree, watchDeps, newMetricSource, deps, resolution)
	warnings = append(warnings, watchWarnings...)
	return worker, watches, warnings
}

// checkFailingFromSnapshots restores the last check-health edge after a daemon
// restart. Snapshot type metadata ensures a same-named check from a changed
// configuration does not inherit an unrelated state.
func checkFailingFromSnapshots(snapshots *Snapshots, service string, checkTypes map[string]string) map[string]bool {
	restored := map[string]bool{}
	for name, snapshot := range snapshots.Get(service) {
		checkType, configured := checkTypes[name]
		if !configured || snapshot.CheckType != checkType || snapshot.Optional || !snapshot.Observation.AffectsHealth() {
			continue
		}
		restored[name] = !snapshot.healthy()
	}
	if len(restored) == 0 {
		return nil
	}
	return restored
}

// workerCheckRunner returns the worker's per-cycle check runner. Each cycle it
// runs the checks due (per-check intervals skip cycles; skipped checks reuse
// the cached result so the cache stays complete), then any gate-triggered
// extras, recording measurements as results land.
func workerCheckRunner(worker *Worker, built []checks.Built, every map[string]int, maxParallel int, recordMeasurement func(checks.Result), setCycleMetrics func(checks.MetricReader)) func(context.Context, checks.Deps) map[string]checks.Result {
	cache := map[string]checks.Result{}
	runAndCache := func(ctx context.Context, due []checks.Built) {
		for _, r := range checks.Run(ctx, due, maxParallel) {
			cache[r.Check] = r
			if recordMeasurement != nil {
				recordMeasurement(r)
			}
		}
	}
	return func(ctx context.Context, d checks.Deps) map[string]checks.Result {
		setCycleMetrics(d.Metrics)
		due := dueChecks(worker.cycle, built, every, cache)
		ran := make(map[string]bool, len(due))
		for _, b := range due {
			ran[b.Check.Name()] = true
		}
		worker.cycleRan = ran
		runAndCache(ctx, due)
		extra := worker.gatedChecksDue(built, cache)
		for _, b := range extra {
			ran[b.Check.Name()] = true
		}
		worker.cycleRan = ran
		runAndCache(ctx, extra)
		return cache
	}
}

// serviceWatchCheckDeps derives the check deps a service's embedded watches use
// from the worker's deps. It scopes the process-counting closure to everything
// discovery attributes to the service, so a process_count watch counts the
// service's own processes rather than unrelated host processes that share a user
// or exe.
//
// That set is wider than the service's PID tree: Discover seeds from the init
// backend first, so on systemd it is the unit's whole control group plus the
// selector matches and their descendants. Strays are therefore counted here and
// cannot be excluded — which is the point of the separate `strays` check, whose
// whole job is to name the members this number silently absorbs.
func serviceWatchCheckDeps(base checks.Deps, discoverer process.Discoverer, selectors []process.Selector) checks.Deps {
	base.ProcessCount = func(user, exe, exeDir string) int {
		return discoverer.CountInTree(selectors, user, exe, exeDir)
	}
	return base
}

// watchMetricSourceFactory returns a builder for the metric source a service
// watch's `metric` check reads. Each call builds a fresh, dedicated collector so
// a watch's rate metrics (cpu/cpu_thread/io) never collide with the engine's
// per-cycle sampling or with another watch — mirroring the dedicated
// LiveCollector the web live view uses. Service-scope samples the service's PID
// tree (parent + children); system-scope reads the host. metricCheck.Run samples
// once per cycle, so successive cycles of one watch yield correct rate deltas.
func watchMetricSourceFactory(service string, discoverer process.Discoverer, selectors []process.Selector, freshness time.Duration) func() checks.MetricReader {
	return func() checks.MetricReader {
		wc := metrics.New(metrics.OSReader{})
		if freshness > 0 {
			wc.SystemFreshness = freshness
		}
		return func(scope, metric string) (metrics.Reading, bool) {
			var snap metrics.Snapshot
			if scope == checks.MetricScopeSystem {
				snap = wc.SampleSystem()
			} else {
				snap = wc.SampleService(service, discoverPIDs(discoverer, selectors))
			}
			if snap == nil {
				return metrics.Reading{}, false
			}
			r, ok := snap[metric]
			return r, ok
		}
	}
}

func buildWorkerCheckSet(section map[string]any, deps checks.Deps, dynamicMetrics bool) ([]checks.Built, []string, func(checks.MetricReader)) {
	if !dynamicMetrics {
		built, warnings := checks.Build(section, deps)
		return built, warnings, func(checks.MetricReader) {}
	}

	var current checks.MetricReader
	buildDeps := deps
	buildDeps.Metrics = func(scope, name string) (metrics.Reading, bool) {
		if current == nil {
			return metrics.Reading{}, false
		}
		return current(scope, name)
	}
	built, warnings := checks.Build(section, buildDeps)
	return built, warnings, func(reader checks.MetricReader) {
		current = reader
	}
}

// publishSnapshots returns the worker's per-cycle check-cache publisher, or nil
// when no snapshot registry is wired.
func publishSnapshots(s *Snapshots, name string, checkTypes map[string]string) func(map[string]checks.Result, map[string]bool) {
	if s == nil {
		return nil
	}
	return func(cache map[string]checks.Result, ran map[string]bool) {
		s.PublishWithCheckTypes(name, cache, ran, checkTypes)
	}
}

// checkIntervals computes, per check in the `checks` section that sets an
// `interval`, how many cycles to skip between runs: round(interval/resolution),
// at least 1. It returns warnings (surfaced at daemon start) when an interval is
// below the resolution or not an exact multiple of it.
func checkIntervals(tree map[string]any, resolution time.Duration) (map[string]int, []string) {
	if resolution <= 0 {
		// Callers normalise resolution to a positive value; guard anyway so a
		// misuse can't divide by zero below (round(d/0) -> +Inf -> undefined int).
		return nil, nil
	}
	section, ok := tree[config.SectionChecks].(map[string]any)
	if !ok {
		return nil, nil
	}
	every := map[string]int{}
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[name].(map[string]any)
		if !ok {
			continue
		}
		d := cfgval.Duration(entry[config.EntryKeyInterval])
		if d <= 0 {
			continue // no per-check interval: runs every cycle
		}
		n := int(math.Round(float64(d) / float64(resolution)))
		switch {
		case n < 1:
			warnings = append(warnings, fmt.Sprintf("check %q interval %s is below the %s resolution; running every cycle", name, d, resolution))
			n = 1
		case time.Duration(n)*resolution != d:
			warnings = append(warnings, fmt.Sprintf("check %q interval %s is not a multiple of the %s resolution; running every %s", name, d, resolution, time.Duration(n)*resolution))
		}
		every[name] = n
	}
	return every, warnings
}

// checkIntervalCycles returns the worker-cycle spacing for a configured check
// interval. Both scheduling and snapshot freshness use it, so the web does not
// expire a result before the worker considers the next run due.
func checkIntervalCycles(interval, resolution time.Duration) int {
	if interval <= 0 || resolution <= 0 {
		return 1
	}
	return max(int(math.Round(float64(interval)/float64(resolution))), 1)
}

func effectiveCheckInterval(interval, resolution time.Duration) time.Duration {
	if resolution <= 0 {
		return interval
	}
	return time.Duration(checkIntervalCycles(interval, resolution)) * resolution
}

// dueChecks selects the checks to run on a given cycle: a check with `every` N
// runs on cycles 1, N+1, 2N+1, … Skipped checks keep their cached result.
// A check with no cached result always runs so a reload or config change cannot
// leave long-interval checks unobserved until their next scheduled modulo.
func dueChecks(cycle int, built []checks.Built, every map[string]int, cache map[string]checks.Result) []checks.Built {
	due := make([]checks.Built, 0, len(built))
	for _, b := range built {
		name := b.Check.Name()
		if cache != nil {
			if _, ok := cache[name]; !ok {
				due = append(due, b)
				continue
			}
		}
		n := max(every[name], 1)
		if (cycle-1)%n == 0 {
			due = append(due, b)
		}
	}
	return due
}

// stateBatchStore is the optional transaction capability a state store exposes.
type stateBatchStore interface {
	WithBatch(ctx context.Context, record func(state.Batch) error) error
}

// cycleRecords is the subset of time-series record methods a worker cycle uses.
// state.Batch and directCycleRecords both implement it, keeping the fallback for
// test stores that intentionally do not provide transaction support.
type cycleRecords interface {
	SLARecorder
	MeasurementRecorder
}

type directCycleRecords struct {
	sla          SLARecorder
	measurements MeasurementRecorder
}

// bestEffortCycleRecords remembers direct write errors while letting the cycle
// continue. It preserves the pre-batch behavior for alternate state stores that
// do not support transactions.
type bestEffortCycleRecords struct {
	cycleRecords
	err error
}

func (r *bestEffortCycleRecords) record(err error) error {
	if err != nil && r.err == nil {
		r.err = err
	}
	return nil
}

func (r *bestEffortCycleRecords) RecordSLA(service string, up bool, at time.Time) error {
	return r.record(r.cycleRecords.RecordSLA(service, up, at))
}

func (r *bestEffortCycleRecords) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	return r.record(r.cycleRecords.RecordCheckSLA(service, check, up, at))
}

func (r *bestEffortCycleRecords) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	return r.record(r.cycleRecords.RecordMeasurement(service, check, valueMs, at))
}

func (r *bestEffortCycleRecords) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	return r.record(r.cycleRecords.RecordMetric(service, check, metric, value, at))
}

// wrapRecord names the failed write in the error the cycle reports. The four
// direct recorders below differ only in the store call and that name.
func wrapRecord(what string, err error) error {
	if err != nil {
		return fmt.Errorf("record %s: %w", what, err)
	}
	return nil
}

func (r directCycleRecords) RecordSLA(service string, up bool, at time.Time) error {
	return wrapRecord("SLA", r.sla.RecordSLA(service, up, at))
}

func (r directCycleRecords) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	return wrapRecord("check SLA", r.sla.RecordCheckSLA(service, check, up, at))
}

func (r directCycleRecords) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	return wrapRecord("measurement", r.measurements.RecordMeasurement(service, check, valueMs, at))
}

func (r directCycleRecords) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	return wrapRecord("metric", r.measurements.RecordMetric(service, check, metric, value, at))
}

type cycleMeasurement struct {
	check  string
	metric string
	value  float64
	at     time.Time
}

// cycleWriter buffers time-series samples while checks run, then records the
// complete observed cycle in one short transaction. It deliberately starts the
// transaction only after the checks finish, so a slow probe never holds the
// store's single writer connection.
type cycleWriter struct {
	name         string
	now          func() time.Time
	emit         func(Event)
	sla          SLARecorder
	measurements MeasurementRecorder
	batch        stateBatchStore
	measured     map[string]bool
	graphable    map[string][]checks.GraphMetric
	bands        map[string][]checks.BandMetric
	records      []cycleMeasurement
	bandSamples  []cycleBandSample
}

// cycleBandSample is one staged state sample: which check's band, and whether
// its OK predicate held this cycle.
type cycleBandSample struct {
	check  string
	metric string
	ok     bool
	at     time.Time
}

func newCycleWriter(deps Deps, name string, tree map[string]any) *cycleWriter {
	if deps.SLA == nil {
		return nil
	}
	now := clockOrNow(deps.Now)
	w := &cycleWriter{name: name, now: now, emit: deps.Emit, sla: deps.SLA}
	if measurements, ok := deps.SLA.(MeasurementRecorder); ok && measurements != nil {
		w.measurements = measurements
		w.measured = measuredCheckNames(tree)
		w.graphable = graphableCheckMetrics(tree)
	}
	w.bands = bandCheckMetrics(tree)
	if batch, ok := deps.SLA.(stateBatchStore); ok {
		w.batch = batch
	}
	return w
}

// RecordMeasurement stages a just-completed check result. It retains the
// observation timestamp from completion so batching changes commits, not the
// archive bucket assigned to a result.
func (w *cycleWriter) RecordMeasurement(r checks.Result) {
	if w == nil {
		return
	}
	at := w.now()
	// Band samples need only the SLA store this writer's existence guarantees;
	// the measurement store below gates the value series alone.
	eachBandSample(w.bands[r.Check], r.Data, func(metric string, ok bool) {
		w.bandSamples = append(w.bandSamples, cycleBandSample{check: r.Check, metric: metric, ok: ok, at: at})
	})
	if w.measurements == nil {
		return
	}
	if w.measured[r.Check] {
		w.records = append(w.records, cycleMeasurement{
			check: r.Check,
			value: float64(r.Latency) / float64(time.Millisecond),
			at:    at,
		})
	}
	eachGraphMetricSample(w.graphable[r.Check], r.Data, func(metric string, value float64) {
		w.records = append(w.records, cycleMeasurement{check: r.Check, metric: metric, value: value, at: at})
	})
}

// eachGraphMetricSample calls emit for every declared metric the result actually
// carries as a number. A service persists through the cycle batch and a host
// watch straight through the store, but they must select the same fields from the
// same declaration, so the selection lives here rather than in each sink.
func eachGraphMetricSample(graphs []checks.GraphMetric, data map[string]any, emit func(metric string, value float64)) {
	for _, metric := range graphs {
		if value, ok := checks.NumericData(data[metric.Key]); ok {
			emit(metric.Key, value)
		}
	}
}

// eachBandSample calls emit for every declared band whose state the result
// actually carries as a number, with the value already judged against the
// band's OK predicate. Watches and service checks both select through it, the
// same single-rule arrangement eachGraphMetricSample gives the value series.
func eachBandSample(bands []checks.BandMetric, data map[string]any, emit func(metric string, ok bool)) {
	for _, band := range bands {
		if value, ok := checks.NumericData(data[band.Key]); ok {
			emit(band.Key, band.OKFor(value))
		}
	}
}

// bandSeriesName keys one service check's band series: the check name plus the
// metric, joined by a separator config validation keeps out of check names so
// the composite can never collide with a real check.
func bandSeriesName(check, metric string) string {
	return check + bandSeriesSeparator + metric
}

// bandSeriesSeparator joins a check name and a band metric into one series key.
const bandSeriesSeparator = ":"

// RecordCycle writes the staged measurements plus, for observed cycles, check
// and service SLA after checks complete. A failed batch rolls back the entire
// cycle and emits one best-effort error event; it never blocks rule evaluation
// or remediation.
//
// Cancellation is the exception: a stop or a reload cancels the cycle context
// mid-batch, which is the daemon shutting down cleanly, not a storage fault.
// Recording it wrote one error event per in-flight service on every restart,
// and because it is the newest event it became the service's last_event and sat
// on the dashboard for days describing an outage that never happened.
func (w *cycleWriter) RecordCycle(ctx context.Context, cycle cycleRecord) {
	if w == nil {
		return
	}
	defer func() { w.records, w.bandSamples = w.records[:0], w.bandSamples[:0] }()

	var err error
	if w.batch != nil {
		err = w.batch.WithBatch(ctx, func(records state.Batch) error {
			return w.writeCycle(records, cycle)
		})
	} else {
		direct := &bestEffortCycleRecords{cycleRecords: directCycleRecords{sla: w.sla, measurements: w.measurements}}
		_ = w.writeCycle(direct, cycle)
		err = direct.err
	}
	if err != nil && !errors.Is(err, context.Canceled) && w.emit != nil {
		w.emit(Event{Service: w.name, Kind: eventKindError, Message: "record cycle: " + err.Error()})
	}
}

func (w *cycleWriter) writeCycle(records cycleRecords, cycle cycleRecord) error {
	for _, sample := range w.records {
		var err error
		if sample.metric == "" {
			err = records.RecordMeasurement(w.name, sample.check, sample.value, sample.at)
		} else {
			err = records.RecordMetric(w.name, sample.check, sample.metric, sample.value, sample.at)
		}
		if err != nil {
			return fmt.Errorf("record cycle measurement for %s: %w", sample.check, err)
		}
	}
	// Band samples sit beside the measurements, outside the availability gate:
	// a state sample is a measurement of the host, not a verdict about the
	// service, so a settling cycle records it just as truthfully as a live one.
	for _, sample := range w.bandSamples {
		if err := records.RecordCheckSLA(w.name, bandSeriesName(sample.check, sample.metric), sample.ok, sample.at); err != nil {
			return fmt.Errorf("record cycle band for %s: %w", sample.check, err)
		}
	}
	if cycle.recordAvailability {
		for check, result := range cycle.cache {
			// A verdictless check has no availability to record: neither side of
			// "a backup is running" is uptime, and a bare measurement asserts
			// nothing at all, so neither gets an SLA series. Nor does an advisory:
			// a warning is a thing to look at, not downtime to hold against the
			// service.
			observation := result.Observation()
			if !cycle.ran[check] || !result.CountsTowardHealth() {
				continue
			}
			if err := records.RecordCheckSLA(w.name, check, observation.Healthy(), w.now()); err != nil {
				return fmt.Errorf("record cycle check SLA for %s: %w", check, err)
			}
		}
		if err := records.RecordSLA(w.name, cycle.up, w.now()); err != nil {
			return fmt.Errorf("record cycle SLA: %w", err)
		}
	}
	return nil
}

// checkSectionMetrics walks a service's checks: section once and maps each
// check name to whatever its resolver declares, keeping only non-empty
// declarations. The one walk shared by the line-metric and band-metric maps,
// so the two recorders can never disagree on which checks they saw.
func checkSectionMetrics[T any](tree map[string]any, resolve func(typ string, entry map[string]any) []T) map[string][]T {
	section, _ := tree[config.SectionChecks].(map[string]any)
	out := map[string][]T{}
	for cn, raw := range section {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m[checks.CheckKeyType].(string)
		if declared := resolve(typ, m); len(declared) > 0 {
			out[cn] = declared
		}
	}
	return out
}

// graphableCheckMetrics maps each configured check name to the named metrics its
// type publishes (checks.GraphMetrics), for the recorder to persist from
// Result.Data — minus the keys the check has banded, because a state draws as a
// band or as a line and never both. Empty when no configured check declares
// graphable metrics.
func graphableCheckMetrics(tree map[string]any) map[string][]checks.GraphMetric {
	return checkSectionMetrics(tree, func(typ string, entry map[string]any) []checks.GraphMetric {
		return checks.ResolvedGraphMetrics(typ, cfgval.AsString(entry[checks.CheckKeyUnit]), entry)
	})
}

// bandCheckMetrics maps each configured check name to its resolved band
// metrics, the state series the recorder persists per cycle.
func bandCheckMetrics(tree map[string]any) map[string][]checks.BandMetric {
	return checkSectionMetrics(tree, checks.DeclaredBandMetrics)
}

// parseCheckGates reads each check's `requires` and `skip_when_changed` fields
// into the worker's interdependency map. Returns nil when no check is gated.
func parseCheckGates(tree map[string]any) map[string]CheckGate {
	section, _ := tree[config.SectionChecks].(map[string]any)
	gates := map[string]CheckGate{}
	for name, raw := range section {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		gate := CheckGate{
			Requires:        cfgval.StringList(m[checks.CheckKeyRequires]),
			SkipWhenChanged: cfgval.StringList(m[checks.CheckKeySkipWhenChanged]),
		}
		if len(gate.Requires) > 0 || len(gate.SkipWhenChanged) > 0 {
			gates[name] = gate
		}
	}
	if len(gates) == 0 {
		return nil
	}
	return gates
}

// measuredCheckNames returns the names of a service's checks whose type records
// elapsed latency according to the central check registry.
func measuredCheckNames(tree map[string]any) map[string]bool {
	section, _ := tree[config.SectionChecks].(map[string]any)
	out := map[string]bool{}
	for cn, raw := range section {
		if m, ok := raw.(map[string]any); ok {
			if t, _ := m[checks.CheckKeyType].(string); checks.RecordsLatency(t) {
				out[cn] = true
			}
		}
	}
	return out
}

// applyMonitorMode reconciles a service's persisted monitoring state with its
// `monitor` flag at daemon startup, returning a non-empty warning on store error.
//   - enabled : force monitoring on
//   - disabled: force monitoring off
//   - previous: keep the persisted state; first run defaults to on
func applyMonitorMode(store MonitorStore, name, mode string) string {
	return applyMonitorModeFor(store, serviceSubjectPrefix+name, name, mode)
}

func applyWatchMonitorMode(store MonitorStore, name, mode string) string {
	return applyMonitorModeFor(store, watchSubjectPrefix+name, WatchMonitorKey(name), mode)
}

func applyMonitorModeFor(store MonitorStore, label, key, mode string) string {
	if store == nil {
		return ""
	}
	var err error
	switch mode {
	case config.MonitorDisabled:
		err = store.SetActive(key, false, state.SourceConfig)
	case config.MonitorPrevious:
		if _, found, e := store.Active(key); e != nil {
			err = e
		} else if !found {
			err = store.SetActive(key, true, state.SourceConfig)
		}
	default: // MonitorEnabled
		err = store.SetActive(key, true, state.SourceConfig)
	}
	if err != nil {
		return label + ": persist monitor state: " + err.Error()
	}
	return ""
}

// monitorPaused returns the worker's live pause check. It reads the persisted
// state every cycle so an operator's monitor/unmonitor takes effect without a
// daemon restart. It fails open: on a missing row or a store error the service
// is monitored, never silently dropped.
func monitorPaused(store MonitorStore, name string) func() bool {
	if store == nil {
		return func() bool { return false }
	}
	return func() bool {
		active, found, err := store.Active(name)
		if err != nil || !found {
			return false
		}
		return !active
	}
}

// WatchMonitorKey is the persistent monitor-state key for a watch (host or
// service-embedded, "<service>:<watch>"), distinct from a service's bare-name
// key. The CLI and web use it to pause/resume a watch independently of any service.
func WatchMonitorKey(name string) string {
	return "watch:" + name
}

// metricSampler returns a per-cycle metric reader for a service, or nil when the
// service references no metrics (so the daemon does not read /proc every cycle
// for nothing). Service metrics are sampled over the discovered process set;
// system metrics come from the shared collector's cached system sample.
func metricSampler(service string, tree map[string]any, collector *metrics.Collector, pids func() []int) func(context.Context) checks.MetricReader {
	needService, needSystem := usesMetrics(tree)
	if !needService && !needSystem {
		return nil
	}
	if pids == nil {
		pids = func() []int { return nil }
	}

	return func(_ context.Context) checks.MetricReader {
		var svc, sys metrics.Snapshot
		if needService {
			svc = collector.SampleService(service, pids())
		}
		if needSystem {
			sys = collector.SampleSystem()
		}
		return func(scope, name string) (metrics.Reading, bool) {
			snap := svc
			if scope == checks.MetricScopeSystem {
				snap = sys
			}
			if snap == nil {
				return metrics.Reading{}, false
			}
			r, ok := snap[name]
			return r, ok
		}
	}
}

// cycleProcessSource reuses one full process discovery for every sampler in a
// worker cycle. Consumers that need only PIDs derive them from this shared
// result, while continuity inference retains the source/role evidence needed to
// decide whether a process can safely explain an unobserved interval.
func cycleProcessSource(discover func() []process.Process, cycle func() int) func() []process.Process {
	if discover == nil {
		discover = func() []process.Process { return nil }
	}
	var cached []process.Process
	var cachedCycle int
	var ok bool
	return func() []process.Process {
		current := 0
		if cycle != nil {
			current = cycle()
		}
		if ok && cachedCycle == current {
			return cached
		}
		cached = discover()
		cachedCycle = current
		ok = true
		return cached
	}
}

// discoverPIDs returns the PIDs of the processes matching selectors — the input
// the collector samples. Discovery warnings are dropped: the metric and live
// samplers only need the PID set, and surfacing those warnings is the process
// checks' job, not the sampler's.
func discoverPIDs(discoverer process.Discoverer, selectors []process.Selector) []int {
	procs, _ := discoverer.Discover(selectors)
	return processPIDs(procs)
}

func processPIDs(procs []process.Process) []int {
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, p.PID)
	}
	return pids
}

// liveSampler returns a per-cycle closure that discovers the service's process
// tree, samples live CPU for the service tables, and records CPU/memory/IO into
// the service runtime history for the detail graphs. It uses a dedicated
// collector (deps.LiveCollector) so CPU rate deltas never collide with the
// engine's metric sampling. Returns nil when no live/runtime destination is
// wired.
func liveSampler(service string, lc *metrics.Collector, live *LiveMetrics, serviceMetrics *ServiceMetricSampler, procs func() []process.Process, now func() time.Time) func(context.Context) {
	if lc == nil || (live == nil && serviceMetrics == nil) {
		return nil
	}
	if procs == nil {
		procs = func() []process.Process { return nil }
	}
	now = clockOrNow(now)
	return func(ctx context.Context) {
		at := now()
		procList := procs()
		pidList := processPIDs(procList)
		sc := lc.SampleServiceCPU(service, pidList)
		sl := ServiceLive{
			CPU:                 sc.CPU.Percent,
			CPUReady:            sc.CPU.Ready,
			CPUThread:           sc.CPUThread.Percent,
			NumCPU:              sc.NumCPU,
			PerProcCPU:          sc.PerProc,
			PerProcMaxCore:      sc.PerProcMaxCore,
			PerProcMaxCoreExact: sc.PerProcMaxCoreExact,
		}
		live.Publish(service, sl)
		if serviceMetrics == nil {
			return
		}
		cur := web.ServiceRuntime{At: at.UTC().Format(time.RFC3339)}
		if totals := processTotalsFromPIDs(pidList, lc.Reader); totals != nil {
			cur.ProcessTotals = *totals
		}
		cur.NumCPU = sc.NumCPU
		if sc.CPU.Ready {
			cur.CPU = sc.CPU.Percent
			cur.CPUThread = sc.CPUThread.Percent
			cur.HasCPU = true
		}
		if started, ok := serviceStartTime(procList, lc.Reader, at); ok {
			cur.StartedAt, cur.Uptime, cur.UptimeSeconds = serviceRuntimeUptime(started, at)
		}
		serviceMetrics.record(ctx, service, cur)
	}
}

func processTotalsFromPIDs(pids []int, r procMetricReader) *web.ProcessTotals {
	if len(pids) == 0 || r == nil {
		return nil
	}
	totals := web.ProcessTotals{Count: len(pids)}
	for _, pid := range pids {
		if rss, ok := r.ProcessRSS(pid); ok {
			totals.RSS += uintToInt64(rss)
		}
		if rd, wr, ok := r.ProcessIO(pid); ok {
			totals.IORead += uintToInt64(rd)
			totals.IOWrite += uintToInt64(wr)
		}
		if n, ok := r.ProcessFDs(pid); ok {
			totals.FDs += uintToInt64(n)
		}
		if n, ok := r.ProcessThreads(pid); ok {
			totals.Threads += uintToInt64(n)
		}
	}
	return &totals
}

// usesMetrics scans a resolved service for metric checks and metric conditions,
// reporting whether service-scope and/or system-scope metrics are referenced.
func usesMetrics(tree map[string]any) (service, system bool) {
	mark := func(scope string) {
		if scope == checks.MetricScopeSystem {
			system = true
		} else {
			service = true
		}
	}
	for _, section := range []string{config.SectionChecks, config.SectionPreflight} {
		entries, ok := tree[section].(map[string]any)
		if !ok {
			continue
		}
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if t, _ := m[checks.CheckKeyType].(string); t == checks.CheckTypeMetric {
					mark(checkMetricScopeOf(m))
				}
			}
		}
	}
	if ruleMap, ok := tree[rules.SectionRules].(map[string]any); ok {
		for _, e := range ruleMap {
			if m, ok := e.(map[string]any); ok {
				if ifNode, ok := m[rules.RuleFieldIf].(map[string]any); ok {
					scanMetricScopes(ifNode, mark)
				}
			}
		}
	}
	return service, system
}

func scanMetricScopes(node map[string]any, mark func(string)) {
	rules.WalkConditionLeaves(node, func(operator string, operand any) bool {
		switch operator {
		case rules.ConditionMetric:
			if m, ok := operand.(map[string]any); ok {
				mark(ruleMetricScopeOf(m))
			}
		case rules.ConditionFailed, rules.ConditionActive:
			m, ok := operand.(map[string]any)
			if !ok || cfgval.AsString(m[rules.FieldCheck]) != "" {
				return false
			}
			if metric, ok := m[rules.FieldMetric].(map[string]any); ok {
				mark(ruleMetricScopeOf(metric))
			}
		}
		return false
	})
}

func checkMetricScopeOf(m map[string]any) string {
	if s, _ := m[checks.CheckKeyScope].(string); s != "" {
		return s
	}
	return checks.MetricScopeService
}

func ruleMetricScopeOf(m map[string]any) string {
	if s, _ := m[rules.FieldScope].(string); s != "" {
		return s
	}
	return checks.MetricScopeService
}

// HasConfiguredTargets reports whether the config declares any services or
// watches at all, regardless of whether they are enabled. The daemon uses this
// to distinguish "everything is disabled" (still worth starting, so the fleet
// can be enabled later via reload or the web UI) from "nothing is configured".
func HasConfiguredTargets(cfg *config.Config) bool {
	if len(cfg.Services) > 0 {
		return true
	}
	raw, ok := cfg.Global.Raw[config.SectionWatches].(map[string]any)
	return ok && len(raw) > 0
}
