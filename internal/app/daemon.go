package app

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

// MonitorStore is the persistent monitoring-state store the daemon consults to
// decide whether a service is actively monitored. It is implemented by
// internal/state.Store; kept as an interface so workers can be tested without a
// database. A nil store means "always monitor" (no persistence).
type MonitorStore interface {
	Active(service string) (active, found bool, err error)
	SetActive(service string, active bool, source string) error
}

// SLARecorder persists one availability sample per observed monitoring cycle, so
// availability can be reported over rolling windows. Implemented by
// internal/state.Store; nil disables SLA tracking.
type SLARecorder interface {
	RecordSLA(service string, up bool, at time.Time) error
}

// SLAReader reports a service's availability for the web detail view: the rolling
// windows and the per-minute history series. Implemented by internal/state.Store.
type SLAReader interface {
	SLAReport(service string, now time.Time) ([]state.SLAValue, error)
	SLASeries(service string, from, to time.Time) ([]state.SLAPoint, error)
}

// MeasurementRecorder persists one numeric per-check observation (latency, ms) per
// observed cycle, for the latency graph. Implemented by internal/state.Store.
type MeasurementRecorder interface {
	RecordMeasurement(service, check string, valueMs float64, at time.Time) error
}

// MeasurementReader reads a check's measurement summary and history for the web.
// Implemented by internal/state.Store.
type MeasurementReader interface {
	MeasurementSummary(service, check string, span time.Duration, now time.Time) (state.MeasurementStat, error)
	MeasurementSeries(service, check string, from, to time.Time) ([]state.MeasurementPoint, error)
}

// measuredCheckTypes are the check types whose latency is recorded as a time
// series (and graphed in the web), mirroring icmp's latency metric.
var measuredCheckTypes = map[string]bool{"tcp": true, "ports": true, "http": true, "service": true}

// Deps are the host capabilities the daemon wires into each worker.
type Deps struct {
	Backend        servicemgr.Backend
	Manager        servicemgr.Manager
	Runtime        string
	DefaultTimeout    time.Duration
	OperationTimeout  time.Duration
	// Interval is the global resolution (engine.interval). It is the base cycle
	// rate and the unit a per-check `interval` is rounded to (a check runs every
	// round(interval/resolution) cycles). A service's own `interval` overrides it.
	Interval    time.Duration
	MaxParallel int
	Sleep       func(time.Duration)
	Now         func() time.Time
	Emit        func(Event)
	// Monitor persists per-service monitoring state (active/paused) across daemon
	// restarts and reboots. Optional: nil means every service is always monitored.
	Monitor MonitorStore
	// SLA persists per-cycle availability samples for SLA reporting. Optional: nil
	// disables SLA tracking.
	SLA SLARecorder
	// ProcSampler lists matching processes and their counters for `process`
	// watches. Optional: nil uses the host /proc.
	ProcSampler ProcSampler
	// Notifiers are the configured delivery targets (email, …) addressable by name
	// from a watch's `then.notify`. Optional: nil/empty means no notifications.
	Notifiers map[string]notify.Notifier
	// Snapshots collects each service's latest check results for the web detail
	// view. Optional: nil disables publishing.
	Snapshots *Snapshots
	// Events is the recent-event log the web UI reads (global and per-service).
	// Optional: nil disables it. Wire it into Emit via MultiEmit to populate it.
	Events *EventLog
	// SystemFreshness caches system metrics so concurrent workers in one cycle
	// share a computation; it must be below the scheduler interval.
	SystemFreshness time.Duration
}

// BuildWorkers resolves every enabled service and wires a Worker for it: a check
// cache producer and an operation-engine Operate closure (section 24). Services
// that are disabled or fail to resolve are skipped with a warning.
func BuildWorkers(cfg *config.Config, deps Deps) ([]*Worker, []string) {
	var workers []*Worker
	var warnings []string
	collector := metrics.New(metrics.OSReader{})
	if deps.SystemFreshness > 0 {
		collector.SystemFreshness = deps.SystemFreshness
	}
	resolver := servicemgr.NewUnitResolver()

	for _, name := range serviceNames(cfg) {
		doc := cfg.Services[name]
		if doc == nil || isDisabled(doc.Body) {
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
		aliases := config.UnitAliases(resolved.Tree, string(deps.Backend))
		unit, err := resolver.Resolve(context.Background(), deps.Backend, base, aliases)
		if err != nil {
			warnings = append(warnings, "service "+name+": "+err.Error()+" (using "+base+")")
			unit = base
		}
		w, warns := buildWorker(name, unit, resolved.Tree, deps, collector)
		for _, x := range warns {
			warnings = append(warnings, "service "+name+": "+x)
		}
		workers = append(workers, w)
	}
	return workers, warnings
}

func buildWorker(name, unit string, tree map[string]any, deps Deps, collector *metrics.Collector) (*Worker, []string) {
	engine, checkDeps, discoverer := serviceRuntime(name, unit, tree, deps, nil)

	maxParallel := deps.MaxParallel
	ruleSet, _ := rules.ParseRules(tree)
	sampleMetrics := metricSampler(name, tree, collector, discoverer)

	// A per-check `interval` runs that check every N cycles (N rounded from
	// interval/resolution); skipped cycles reuse its last result so the cache and
	// rule windows stay complete. resolution is the service's own interval, or the
	// global one.
	resolution := durationField(tree["interval"])
	if resolution <= 0 {
		resolution = deps.Interval
	}
	if resolution <= 0 {
		resolution = 30 * time.Second
	}
	every, warnings := checkIntervals(tree, resolution)

	cycle := 0
	cache := map[string]checks.Result{}
	recordMeasurement := measurementRecorder(deps, name, tree)

	worker := &Worker{
		Service:   name,
		Rules:     ruleSet,
		Policy:    rules.ParsePolicy(tree),
		State:     &rules.RemediationState{},
		CheckDeps: checkDeps,
		Interval:  durationField(tree["interval"]),
		Gates:     parseCheckGates(tree),
		Sample:    sampleMetrics,
		Operate: func(ctx context.Context, action string) operation.Result {
			switch action {
			case "start":
				return engine.Start(ctx)
			case "stop":
				return engine.Stop(ctx)
			case "restart":
				return engine.Restart(ctx)
			default:
				return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: "unknown action"}
			}
		},
		IsPaused:     monitorPaused(deps.Monitor, name),
		RecordHealth: healthRecorder(deps, name),
		Publish:      publishSnapshots(deps.Snapshots, name),
		Now:          deps.Now,
		Emit:         deps.Emit,
	}
	worker.Checks = func(ctx context.Context, d checks.Deps) map[string]checks.Result {
		cycle++
		section, _ := tree["checks"].(map[string]any)
		built, _ := checks.Build(section, d)
		due := dueChecks(cycle, built, every)
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

// publishSnapshots returns the worker's per-cycle check-cache publisher, or nil
// when no snapshot registry is wired.
func publishSnapshots(s *Snapshots, name string) func(map[string]checks.Result) {
	if s == nil {
		return nil
	}
	return func(cache map[string]checks.Result) { s.Publish(name, cache) }
}

// checkIntervals computes, per check in the `checks` section that sets an
// `interval`, how many cycles to skip between runs: round(interval/resolution),
// at least 1. It returns warnings (surfaced at daemon start) when an interval is
// below the resolution or not an exact multiple of it.
func checkIntervals(tree map[string]any, resolution time.Duration) (map[string]int, []string) {
	section, ok := tree["checks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	every := map[string]int{}
	var warnings []string
	for _, name := range sortedKeys(section) {
		entry, ok := section[name].(map[string]any)
		if !ok {
			continue
		}
		d := durationField(entry["interval"])
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

// sortedKeys returns the map keys sorted, for deterministic iteration.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	measured := measuredCheckNames(tree)
	if len(measured) == 0 {
		return nil
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return func(r checks.Result) {
		if !measured[r.Check] {
			return
		}
		ms := float64(r.Latency) / float64(time.Millisecond)
		if err := store.RecordMeasurement(name, r.Check, ms, now()); err != nil && deps.Emit != nil {
			deps.Emit(Event{Service: name, Kind: "error", Message: "record measurement: " + err.Error()})
		}
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
			Requires:        stringSlice(m["requires"]),
			SkipWhenChanged: stringSlice(m["skip_when_changed"]),
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

// stringSlice converts a YAML list (or single scalar) to a []string.
func stringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
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
	if store == nil {
		return ""
	}
	var err error
	switch mode {
	case config.MonitorDisabled:
		err = store.SetActive(name, false, state.SourceConfig)
	case config.MonitorPrevious:
		if _, found, e := store.Active(name); e != nil {
			err = e
		} else if !found {
			err = store.SetActive(name, true, state.SourceConfig)
		}
	default: // MonitorEnabled
		err = store.SetActive(name, true, state.SourceConfig)
	}
	if err != nil {
		return "service " + name + ": persist monitor state: " + err.Error()
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

// metricSampler returns a per-cycle metric reader for a service, or nil when the
// service references no metrics (so the daemon does not read /proc every cycle
// for nothing). Service metrics are sampled over the discovered process set;
// system metrics come from the shared collector's cached system sample.
func metricSampler(service string, tree map[string]any, collector *metrics.Collector, discoverer process.Discoverer) func(context.Context) checks.MetricReader {
	needService, needSystem := usesMetrics(tree)
	if !needService && !needSystem {
		return nil
	}
	selectors, _ := process.ParseSelectors(tree)

	return func(ctx context.Context) checks.MetricReader {
		var svc, sys metrics.Snapshot
		if needService {
			procs, _ := discoverer.Discover(selectors)
			pids := make([]int, 0, len(procs))
			for _, p := range procs {
				pids = append(pids, p.PID)
			}
			svc = collector.SampleService(service, pids)
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

func serviceNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isDisabled(body map[string]any) bool {
	v, ok := body["enabled"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && !b
}
