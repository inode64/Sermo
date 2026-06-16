package app

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

// MonitorStore is the persistent monitoring-state store the daemon consults to
// decide whether a service or watch is actively monitored. It is implemented by
// internal/state.Store; kept as an interface so workers can be tested without a
// database. A nil store means "always monitor" (no persistence).
type MonitorStore interface {
	Active(service string) (active, found bool, err error)
	SetActive(service string, active bool, source string) error
	MonitorState(service string) (state.MonitorRecord, bool, error)
}

// SLARecorder persists one availability sample per observed monitoring cycle, so
// availability can be reported over rolling windows. Implemented by
// internal/state.Store; nil disables SLA tracking.
type SLARecorder interface {
	RecordSLA(service string, up bool, at time.Time) error
	RecordCheckSLA(service, check string, up bool, at time.Time) error
}

// SLAReader reports a service's availability for the web detail view: the rolling
// windows and the per-minute history series. Implemented by internal/state.Store.
type SLAReader interface {
	SLAReport(service string, now time.Time) ([]state.SLAValue, error)
	SLASeries(service string, from, to time.Time) ([]state.SLAPoint, error)
	CheckSLAReport(service, check string, now time.Time) ([]state.SLAValue, error)
	CheckSLASeries(service, check string, from, to time.Time) ([]state.SLAPoint, error)
	SLATimelines(service string, now time.Time) ([]state.SLAWindowTimeline, error)
	CheckSLATimelines(service, check string, now time.Time) ([]state.SLAWindowTimeline, error)
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

// measuredCheckTypes are the check types whose latency is recorded as a time
// series (and graphed in the web), mirroring icmp's latency metric.
var measuredCheckTypes = map[string]bool{"tcp": true, "ports": true, "http": true, "service": true}

// Deps are the host capabilities the daemon wires into each worker.
type Deps struct {
	Backend          servicemgr.Backend
	Manager          servicemgr.Manager
	Runtime          string
	DefaultTimeout   time.Duration
	OperationTimeout time.Duration
	// Interval is the global resolution (engine.interval). It is the base cycle
	// rate and the unit a per-check `interval` is rounded to (a check runs every
	// round(interval/resolution) cycles). A service's own `interval` overrides it.
	Interval    time.Duration
	MaxParallel int
	Sleep       func(time.Duration)
	Now         func() time.Time
	Emit        func(Event)
	// Monitor persists per-entry monitoring state (active/paused) across daemon
	// restarts and reboots. Optional: nil means every service/watch is always
	// monitored.
	Monitor MonitorStore
	// SLA persists per-cycle availability samples for SLA reporting. Optional: nil
	// disables SLA tracking.
	SLA SLARecorder
	// DaemonMetrics persists sermod's own process metric history for the web UI.
	// Optional: nil keeps only in-memory history for this process lifetime.
	DaemonMetrics DaemonMetricStore
	// ProcSampler lists matching processes and their counters for `process`
	// watches. Optional: nil uses the host /proc.
	ProcSampler ProcSampler
	// DiskUsage reports filesystem usage for storage checks and web watch summaries.
	// Optional: nil uses statfs.
	DiskUsage checks.DiskUsageFunc
	// MountSampler reads the mount table for storage/autofs checks and web watch
	// summaries. Optional: nil reads /proc/mounts.
	MountSampler checks.MountSamplerFunc
	// NetSampler reads one interface for net checks and web watch summaries.
	// Optional: nil reads net.Interfaces and /sys/class/net.
	NetSampler checks.NetSamplerFunc
	// PingSampler probes ICMP hosts for icmp checks and web watch summaries.
	// Optional: nil uses native ICMP.
	PingSampler checks.PingSamplerFunc
	// OomSampler reads the cumulative OOM-kill counter for checks and web watch
	// summaries. Optional: nil reads /proc/vmstat.
	OomSampler checks.OomSamplerFunc
	// PidsSampler reads the kernel PID table for checks and web watch summaries.
	// Optional: nil reads /proc/loadavg and kernel.pid_max.
	PidsSampler checks.PidsSamplerFunc
	// DiskIOSampler reads block-device counters for diskio checks and web watch
	// summaries. Optional: nil reads /proc/diskstats.
	DiskIOSampler checks.DiskIOSamplerFunc
	// SensorSampler reads hardware sensors for sensors checks and web watch
	// summaries. Optional: nil reads hwmon.
	SensorSampler checks.SensorSamplerFunc
	// RaidSampler reads Linux md RAID state for raid checks and web watch
	// summaries. Optional: nil reads /proc/mdstat.
	RaidSampler checks.RaidSamplerFunc
	// EdacSampler reads EDAC memory-error counters for edac checks and web watch
	// summaries. Optional: nil reads sysfs.
	EdacSampler checks.EdacSamplerFunc
	// RouteSampler reads default routes for route checks and web watch summaries.
	// Optional: nil reads /proc/net/route and /proc/net/ipv6_route.
	RouteSampler checks.RouteSamplerFunc
	// PressureSampler reads kernel PSI for pressure checks and web watch summaries.
	// Optional: nil reads /proc/pressure/<resource>.
	PressureSampler checks.PressureSamplerFunc
	// ConntrackSampler reads the netfilter conntrack table for checks and web
	// watch summaries. Optional: nil reads /proc/sys/net/netfilter.
	ConntrackSampler checks.ConntrackSamplerFunc
	// FirewallRulesSampler reads loaded packet-filter rules for checks.
	// Optional: nil runs nft/iptables-save through ExecxRunner.
	FirewallRulesSampler checks.FirewallRulesSamplerFunc
	// EntropySampler reads kernel entropy for entropy checks and web watch
	// summaries. Optional: nil reads /proc/sys/kernel/random/entropy_avail.
	EntropySampler checks.EntropySamplerFunc
	// ZombieSampler counts zombie processes for checks and web watch summaries.
	// Optional: nil scans /proc.
	ZombieSampler checks.ZombieSamplerFunc
	// Notifiers are the configured delivery targets (email, …) addressable by name
	// from a watch's `then.notify`. Optional: nil/empty means no notifications.
	Notifiers map[string]notify.Notifier
	// GlobalNotify is the top-level `notify` default selection (notifier names): the
	// fallback for any notify site (watch or rule alert) that declares none of its
	// own. Empty means no default. See config.NotifyDefault.
	GlobalNotify []string
	// Snapshots collects each service's latest check results for the web detail
	// view. Optional: nil disables publishing.
	Snapshots *Snapshots
	// Remediation collects each service's remediation policy view for the web
	// detail. Optional: nil disables publishing.
	Remediation *RemediationRegistry
	// RuleWindows collects each service's rule window progress for the web detail.
	// Optional: nil disables publishing.
	RuleWindows *RuleWindowRegistry
	// Events is the recent-event log the web UI reads (global and per-service).
	// Optional: nil disables it. Wire it into Emit via MultiEmit to populate it.
	Events *EventLog
	// SystemFreshness caches system metrics so concurrent workers in one cycle
	// share a computation; it must be below the scheduler interval.
	SystemFreshness time.Duration
	// OpGate bounds concurrent operations across workers and the web UI. sermoctl
	// uses the same slot pool under <paths.runtime>/op-slots.
	OpGate *OpGate
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
	// web detail graphs. Optional: nil means the web backend samples on demand.
	ServiceMetrics *ServiceMetricSampler
	// ExecxRunner is used for executing hook commands from watches (file, process,
	// and generic watches). If nil, OSHookRunner will use execx.CommandRunner{}.
	ExecxRunner execx.Runner
	// VolumeExpander grows storage-watch filesystems for `then.expand`. Optional:
	// nil uses volume.Expander with ExecxRunner. Tests inject a fake so no real
	// LVM/filesystem commands run.
	VolumeExpander VolumeExpander
}

// BuildWorkers resolves every enabled service and wires a Worker for it: a check
// cache producer and an operation-engine Operate closure (section 24). Services
// that are disabled or fail to resolve are skipped with a warning.
func BuildWorkers(cfg *config.Config, deps Deps, collector *metrics.Collector) ([]*Worker, []string) {
	var workers []*Worker
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

		base := config.ServiceUnit(resolved.Tree, name)
		candidates, trust := config.ServiceCandidates(resolved.Tree, string(deps.Backend), name)
		unit, err := resolver.Resolve(context.Background(), deps.Backend, candidates, trust)
		if err != nil {
			warnings = append(warnings, "service "+name+": "+err.Error()+" (using "+base+")")
			unit = base
		}
		w, warns := buildWorker(name, unit, resolved.Tree, deps, collector)
		for _, x := range warns {
			warnings = append(warnings, "service "+name+": "+x)
		}
		if t := config.CascadeTargets(resolved.Tree); len(t) > 0 {
			cascadeMap[name] = t
		}
		workers = append(workers, w)
	}
	wireCascade(workers, cascadeMap, deps)
	return workers, warnings
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
	op := func(ctx context.Context, svc, action string) operation.Result {
		if tw := byName[svc]; tw != nil {
			return tw.Operate(ctx, action)
		}
		return operation.Result{Service: svc, Action: action, Status: operation.ResultFailed, Message: "cascade target not configured"}
	}
	lookup := func(svc string) []string { return cascadeMap[svc] }
	for _, w := range workers {
		if len(cascadeMap[w.Service]) == 0 {
			continue
		}
		c := cascader{op: op, lookup: lookup, emit: deps.Emit, sleep: time.Sleep}
		service := w.Service
		w.Cascade = func(ctx context.Context, action string) operation.Result {
			return c.run(ctx, service, action)
		}
	}
}

func buildWorker(name, unit string, tree map[string]any, deps Deps, collector *metrics.Collector) (*Worker, []string) {
	engine, checkDeps, discoverer := serviceRuntime(name, unit, tree, deps, nil)

	maxParallel := deps.MaxParallel
	ruleSet, _ := rules.ParseRules(tree)
	selectors, _ := serviceProcessSelectors(context.Background(), tree, deps, unit)
	noResident := noResidentProcess(tree)
	var worker *Worker
	pidsForCycle := cyclePIDSource(func() []int {
		if noResident {
			return nil
		}
		return discoverPIDs(discoverer, selectors)
	}, func() int {
		if worker == nil {
			return 0
		}
		return worker.cycle
	})
	sampleMetrics := metricSampler(name, tree, collector, pidsForCycle)
	liveSample := liveSampler(name, deps.LiveCollector, deps.Live, deps.ServiceMetrics, pidsForCycle, deps.Now)
	if noResident {
		liveSample = nil
	}

	// remediation.shadow (or mode: shadow) allows full rule+window+guard+policy
	// evaluation and event emission without ever executing operations. It merges
	// from defaults via perServiceDefaults.
	shadow := false
	if r, ok := tree["remediation"].(map[string]any); ok {
		if cfgval.Bool(r["shadow"]) {
			shadow = true
		}
		if cfgval.AsString(r["mode"]) == "shadow" {
			shadow = true
		}
	}

	// A per-check `interval` runs that check every N cycles (N rounded from
	// interval/resolution); skipped cycles reuse its last result so the cache and
	// rule windows stay complete. resolution is the service's own interval, or the
	// global one.
	resolution := cfgval.Duration(tree["interval"])
	if resolution <= 0 {
		resolution = deps.Interval
	}
	if resolution <= 0 {
		resolution = 30 * time.Second
	}
	every, warnings := checkIntervals(tree, resolution)

	cache := map[string]checks.Result{}
	recordMeasurement := measurementRecorder(deps, name, tree)
	section, _ := tree["checks"].(map[string]any)
	built, checkWarnings, setCycleMetrics := buildWorkerCheckSet(section, checkDeps, sampleMetrics != nil)
	warnings = append(warnings, checkWarnings...)
	preflightSection, _ := tree["preflight"].(map[string]any)
	preflightBuilt, preflightWarnings := checks.Build(preflightSection, checkDeps)
	warnings = append(warnings, preflightWarnings...)

	worker = &Worker{
		Service:      name,
		Rules:        ruleSet,
		Policy:       rules.ParsePolicy(tree),
		State:        &rules.RemediationState{},
		Notifiers:    deps.Notifiers,
		GlobalNotify: deps.GlobalNotify,
		Remediation:  deps.Remediation,
		RuleWindows:  deps.RuleWindows,
		CheckDeps:    checkDeps,
		Interval:     cfgval.Duration(tree["interval"]),
		Gates:        parseCheckGates(tree),
		Sample:       sampleMetrics,
		LiveSample:   liveSample,
		Operate: func(ctx context.Context, action string) operation.Result {
			return engine.Do(ctx, action)
		},
		IsPaused:     monitorPaused(deps.Monitor, name),
		Shadow:       shadow,
		ResolveRefs:  func() rules.RefResolver { return rules.NewCheckResolver(preflightBuilt, maxParallel) },
		RecordHealth: healthRecorder(deps, name),
		RecordChecks: checkSLARecorder(deps, name),
		Publish:      publishSnapshots(deps.Snapshots, name),
		Now:          deps.Now,
		Emit:         deps.Emit,
	}
	worker.Checks = func(ctx context.Context, d checks.Deps) map[string]checks.Result {
		setCycleMetrics(d.Metrics)
		due := dueChecks(worker.cycle, built, every)
		ran := make(map[string]bool, len(due))
		for _, b := range due {
			ran[b.Check.Name()] = true
		}
		worker.cycleRan = ran
		for _, r := range checks.Run(ctx, due, maxParallel) {
			cache[r.Check] = r
			if recordMeasurement != nil {
				recordMeasurement(r)
			}
		}
		extra := worker.gatedChecksDue(built, cache)
		for _, b := range extra {
			ran[b.Check.Name()] = true
		}
		worker.cycleRan = ran
		for _, r := range checks.Run(ctx, extra, maxParallel) {
			cache[r.Check] = r
			if recordMeasurement != nil {
				recordMeasurement(r)
			}
		}
		return cache
	}
	return worker, warnings
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
func publishSnapshots(s *Snapshots, name string) func(map[string]checks.Result, map[string]bool) {
	if s == nil {
		return nil
	}
	return func(cache map[string]checks.Result, ran map[string]bool) {
		s.Publish(name, cache, ran)
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
	section, ok := tree["checks"].(map[string]any)
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
		d := cfgval.Duration(entry["interval"])
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

// dueChecks selects the checks to run on a given cycle: a check with `every` N
// runs on cycles 1, N+1, 2N+1, … Skipped checks keep their cached result.
func dueChecks(cycle int, built []checks.Built, every map[string]int) []checks.Built {
	due := make([]checks.Built, 0, len(built))
	for _, b := range built {
		n := every[b.Check.Name()]
		if n < 1 {
			n = 1
		}
		if (cycle-1)%n == 0 {
			due = append(due, b)
		}
	}
	return due
}

// measurementRecorder returns a hook that records a freshly-run check's latency
// (ms) for tcp/ports/http/service checks, or nil when no measurement store is
// wired or the service has no measured checks. Called only for checks that
// actually ran this cycle (respecting per-check intervals), on observed cycles.
func measurementRecorder(deps Deps, name string, tree map[string]any) func(checks.Result) {
	store, ok := deps.SLA.(MeasurementRecorder)
	if !ok || store == nil {
		return nil
	}
	measured := measuredCheckNames(tree)     // latency-graphed check names
	graphable := graphableCheckMetrics(tree) // check name -> named metrics
	if len(measured) == 0 && len(graphable) == 0 {
		return nil
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	fail := func(err error) {
		if err != nil && deps.Emit != nil {
			deps.Emit(Event{Service: name, Kind: "error", Message: "record measurement: " + err.Error()})
		}
	}
	return func(r checks.Result) {
		if measured[r.Check] {
			ms := float64(r.Latency) / float64(time.Millisecond)
			fail(store.RecordMeasurement(name, r.Check, ms, now()))
		}
		for _, m := range graphable[r.Check] {
			if v, ok := numericData(r.Data[m.Key]); ok {
				fail(store.RecordMetric(name, r.Check, m.Key, v, now()))
			}
		}
	}
}

// checkSLARecorder returns the worker's per-check SLA recording hook. Called
// only for checks that actually ran this cycle, so per-check interval caching
// does not create duplicate availability samples.
func checkSLARecorder(deps Deps, name string) func(map[string]checks.Result, map[string]bool) {
	if deps.SLA == nil {
		return nil
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return func(cache map[string]checks.Result, ran map[string]bool) {
		for check, r := range cache {
			if !ran[check] || r.Skipped {
				continue
			}
			if err := deps.SLA.RecordCheckSLA(name, check, r.OK, now()); err != nil && deps.Emit != nil {
				deps.Emit(Event{Service: name, Kind: "error", Message: "record check sla: " + err.Error()})
			}
		}
	}
}

// graphableCheckMetrics maps each configured check name to the named metrics its
// type publishes (checks.GraphMetrics), for the recorder to persist from
// Result.Data. Empty when no configured check declares graphable metrics.
func graphableCheckMetrics(tree map[string]any) map[string][]checks.GraphMetric {
	section, _ := tree["checks"].(map[string]any)
	out := map[string][]checks.GraphMetric{}
	for cn, raw := range section {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if g := checks.GraphMetrics(typ); len(g) > 0 {
			out[cn] = g
		}
	}
	return out
}

// numericData coerces a Result.Data value to a float64 (the recorder only graphs
// numeric fields).
func numericData(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

// parseCheckGates reads each check's `requires` and `skip_when_changed` fields
// into the worker's interdependency map. Returns nil when no check is gated.
func parseCheckGates(tree map[string]any) map[string]CheckGate {
	section, _ := tree["checks"].(map[string]any)
	gates := map[string]CheckGate{}
	for name, raw := range section {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		gate := CheckGate{
			Requires:        cfgval.StringList(m["requires"]),
			SkipWhenChanged: cfgval.StringList(m["skip_when_changed"]),
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

// measuredCheckNames returns the names of a service's checks whose type is graphed
// (measuredCheckTypes).
func measuredCheckNames(tree map[string]any) map[string]bool {
	section, _ := tree["checks"].(map[string]any)
	out := map[string]bool{}
	for cn, raw := range section {
		if m, ok := raw.(map[string]any); ok {
			if t, _ := m["type"].(string); measuredCheckTypes[t] {
				out[cn] = true
			}
		}
	}
	return out
}

// healthRecorder returns the worker's per-cycle SLA recording hook, or nil when
// no store is wired. A write error is logged through Emit but never blocks the
// cycle — SLA accounting is best-effort and must not affect remediation.
func healthRecorder(deps Deps, name string) func(up bool) {
	if deps.SLA == nil {
		return nil
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return func(up bool) {
		if err := deps.SLA.RecordSLA(name, up, now()); err != nil && deps.Emit != nil {
			deps.Emit(Event{Service: name, Kind: "error", Message: "record sla: " + err.Error()})
		}
	}
}

// applyMonitorMode reconciles a service's persisted monitoring state with its
// `monitor` flag at daemon startup, returning a non-empty warning on store error.
//   - enabled : force monitoring on
//   - disabled: force monitoring off
//   - previous: keep the persisted state; first run defaults to on
func applyMonitorMode(store MonitorStore, name, mode string) string {
	return applyMonitorModeFor(store, "service "+name, name, mode)
}

func applyWatchMonitorMode(store MonitorStore, name, mode string) string {
	return applyMonitorModeFor(store, "watch "+name, watchMonitorKey(name), mode)
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

func watchMonitorKey(name string) string {
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

	return func(ctx context.Context) checks.MetricReader {
		var svc, sys metrics.Snapshot
		if needService {
			svc = collector.SampleService(service, pids())
		}
		if needSystem {
			sys = collector.SampleSystem()
		}
		return func(scope, name string) (metrics.Reading, bool) {
			snap := svc
			if scope == "system" {
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

// cyclePIDSource reuses one discovered PID set for every sampler in a worker
// cycle. The cycle key changes once per RunCycle, so service metrics and live CPU
// can share process discovery without reusing stale PIDs in the next cycle.
func cyclePIDSource(discover func() []int, cycle func() int) func() []int {
	if discover == nil {
		discover = func() []int { return nil }
	}
	var cached []int
	var cachedCycle int
	var ok bool
	return func() []int {
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
func liveSampler(service string, lc *metrics.Collector, live *LiveMetrics, serviceMetrics *ServiceMetricSampler, pids func() []int, now func() time.Time) func(context.Context) {
	if lc == nil || (live == nil && serviceMetrics == nil) {
		return nil
	}
	if pids == nil {
		pids = func() []int { return nil }
	}
	if now == nil {
		now = time.Now
	}
	return func(_ context.Context) {
		at := now()
		pidList := pids()
		sc := lc.SampleServiceCPU(service, pidList)
		sl := ServiceLive{
			CPU:            sc.CPU.Percent,
			CPUReady:       sc.CPU.Ready,
			CPUThread:      sc.CPUThread.Percent,
			CPUThreadReady: sc.CPUThread.Ready,
			NumCPU:         sc.NumCPU,
			PerProcCPU:     sc.PerProc,
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
		serviceMetrics.Record(service, cur)
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
		if scope == "system" {
			system = true
		} else {
			service = true
		}
	}
	for _, section := range []string{"checks", "preflight", "postflight"} {
		entries, ok := tree[section].(map[string]any)
		if !ok {
			continue
		}
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "metric" {
					mark(scopeOf(m))
				}
			}
		}
	}
	if ruleMap, ok := tree["rules"].(map[string]any); ok {
		for _, e := range ruleMap {
			if m, ok := e.(map[string]any); ok {
				if ifNode, ok := m["if"].(map[string]any); ok {
					scanMetricScopes(ifNode, mark)
				}
			}
		}
	}
	return service, system
}

func scanMetricScopes(node map[string]any, mark func(string)) {
	for k, v := range node {
		switch k {
		case "metric":
			if m, ok := v.(map[string]any); ok {
				mark(scopeOf(m))
			}
		case "and", "or":
			if list, ok := v.([]any); ok {
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						scanMetricScopes(m, mark)
					}
				}
			}
		case "not":
			if m, ok := v.(map[string]any); ok {
				scanMetricScopes(m, mark)
			}
		}
	}
}

func scopeOf(m map[string]any) string {
	if s, _ := m["scope"].(string); s != "" {
		return s
	}
	return "service"
}

// HasConfiguredTargets reports whether the config declares any services or
// watches at all, regardless of whether they are enabled. The daemon uses this
// to distinguish "everything is disabled" (still worth starting, so the fleet
// can be enabled later via reload or the web UI) from "nothing is configured".
func HasConfiguredTargets(cfg *config.Config) bool {
	if len(cfg.Services) > 0 {
		return true
	}
	raw, ok := cfg.Global.Raw["watches"].(map[string]any)
	return ok && len(raw) > 0
}
