package app

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/metrics"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

// Deps are the host capabilities the daemon wires into each worker.
type Deps struct {
	Backend        servicemgr.Backend
	Manager        servicemgr.Manager
	Runtime        string
	DefaultTimeout time.Duration
	MaxParallel    int
	Sleep          func(time.Duration)
	Now            func() time.Time
	Emit           func(Event)
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

		base := config.ServiceUnit(resolved.Tree, name)
		aliases := config.UnitAliases(resolved.Tree, string(deps.Backend))
		unit, err := resolver.Resolve(context.Background(), deps.Backend, base, aliases)
		if err != nil {
			warnings = append(warnings, "service "+name+": "+err.Error()+" (using "+base+")")
			unit = base
		}
		workers = append(workers, buildWorker(name, unit, resolved.Tree, deps, collector))
	}
	return workers, warnings
}

func buildWorker(name, unit string, tree map[string]any, deps Deps, collector *metrics.Collector) *Worker {
	manager := deps.Manager

	discoverer := process.NewDiscoverer()
	discoverer.MainPIDs = servicemgr.MainPIDFunc(deps.Backend, unit)
	checkDeps := checks.Deps{
		Service:        name,
		DefaultTimeout: deps.DefaultTimeout,
		Status: func(ctx context.Context) (servicemgr.Status, error) {
			st, err := manager.Status(ctx, unit)
			if err != nil {
				return "", err
			}
			return st.Status, nil
		},
		Processes: discoverer.ObserveState,
	}

	locker := locks.NewOperationLocker(filepath.Join(deps.Runtime, "ops"))
	engine := operation.New(operation.Config{
		Service:    name,
		Unit:       unit,
		Backend:    string(deps.Backend),
		Tree:       tree,
		Manager:    manager,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(deps.Runtime, "locks")),
		Discoverer: discoverer,
		CheckDeps:  checkDeps,
		Sleep:      deps.Sleep,
	})

	maxParallel := deps.MaxParallel
	ruleSet, _ := rules.ParseRules(tree)
	sampleMetrics := metricSampler(name, tree, collector, discoverer)

	return &Worker{
		Service:   name,
		Rules:     ruleSet,
		Policy:    rules.ParsePolicy(tree),
		State:     &rules.RemediationState{},
		CheckDeps: checkDeps,
		Sample:    sampleMetrics,
		Checks: func(ctx context.Context, d checks.Deps) map[string]checks.Result {
			section, _ := tree["checks"].(map[string]any)
			built, _ := checks.Build(section, d)
			cache := map[string]checks.Result{}
			for _, r := range checks.Run(ctx, built, maxParallel) {
				cache[r.Check] = r
			}
			return cache
		},
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
		Now:  deps.Now,
		Emit: deps.Emit,
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
