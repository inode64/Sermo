package app

import (
	"context"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/metrics"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// MetricSampleForOperation builds a per-operation metric reader for preflight,
// postflight and guard evaluation when the resolved service references metrics.
func MetricSampleForOperation(name string, tree map[string]any, collector *metrics.Collector, discoverer process.Discoverer, selectors []process.Selector) func(context.Context) checks.MetricReader {
	if collector == nil || noResidentProcess(tree) {
		return nil
	}
	return metricSampler(name, tree, collector, func() []int {
		return discoverPIDs(discoverer, selectors)
	})
}

// serviceRuntime builds the per-service runtime pieces shared by a worker and the
// web backend: a process discoverer, the check deps (with a backend-status
// closure), and the safe operation engine. The engine's per-service operation
// lock serializes start/stop/restart/reload/resume across the worker and the web.
func serviceRuntime(name, unit string, tree map[string]any, deps Deps, libBaseline map[string]string, recordOperation func(operation.Result)) (operation.Engine, checks.Deps, process.Discoverer) {
	lookup := deps.UserLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	discoverer := process.NewDiscovererWithUserLookup(lookup)
	if deps.ProcReader != nil {
		discoverer.Reader = deps.ProcReader
	}
	backendPIDs := serviceBackendPIDs(deps, unit)
	if backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	selectors, _ := serviceProcessSelectors(context.Background(), tree, deps, unit)
	noResident := serviceNoResidentProcess(tree, selectors, backendPIDs)
	metricSample := MetricSampleForOperation(name, tree, deps.Collector, discoverer, selectors)
	if noResident {
		metricSample = nil
	}
	checkDeps := checkDepsFromAppDeps(deps, checks.Deps{
		Service:        name,
		DefaultTimeout: deps.DefaultTimeout,
		Runner:         deps.ExecxRunner,
		Status: func(ctx context.Context) (servicemgr.Status, error) {
			st, err := deps.Manager.Status(ctx, unit)
			if err != nil {
				return "", err
			}
			return st.Status, nil
		},
		Processes:           discoverer.ObserveState,
		ProcessesAny:        discoverer.ObserveAnyState,
		ProcessCount:        discoverer.CountMatching,
		PidfileFallbackPIDs: pidfileFallbackPIDs(context.Background(), deps, unit, backendPIDs),
	})
	locker := configureOperationLocker(deps.Runtime, operationLockReclaimEvent(deps.Emit))
	engine := operation.New(operation.Config{
		Service:          name,
		Unit:             unit,
		Backend:          string(deps.Backend),
		Tree:             tree,
		Manager:          deps.Manager,
		Locker:           &locker,
		Scanner:          locks.NewScanner(locks.RuntimeLocksDir(deps.Runtime)),
		Discoverer:       discoverer,
		ResolveUser:      discoverer.ResolveUser,
		CheckDeps:        checkDeps,
		MetricSample:     metricSample,
		Changed:          LibChangedFunc(libBaseline),
		Sleep:            deps.Sleep,
		OperationTimeout: deps.OperationTimeout,
		Emit:             recordOperation,
	})
	return engine, checkDeps, discoverer
}

func pidfileFallbackPIDs(ctx context.Context, deps Deps, unit string, backendPIDs func() []int) func() []int {
	if deps.Backend != servicemgr.BackendSystemd || backendPIDs == nil {
		return nil
	}
	info := servicemgr.DetectProcInfo(ctx, deps.ExecxRunner, nil, deps.Backend, unit)
	if info.Pidfile != "" {
		return nil
	}
	return backendPIDs
}

func serviceBackendPIDs(deps Deps, unit string) func() []int {
	if deps.BackendPIDs != nil {
		return deps.BackendPIDs
	}
	if deps.Backend == servicemgr.BackendLibvirt || deps.Backend == servicemgr.BackendDocker {
		return nil
	}
	return servicemgr.BackendPIDsFuncWithRunner(deps.Backend, unit, deps.ExecxRunner, nil)
}

// serviceProcessSelectors returns the process selectors a service should use
// for both monitoring workers and web detail. Explicit `processes:` entries win;
// otherwise we derive the safest init-provided identity we can detect.
func serviceProcessSelectors(ctx context.Context, tree map[string]any, deps Deps, unit string) ([]process.Selector, []string) {
	selectors, warnings := process.ParseSelectors(tree)
	if _, configured := tree[config.SectionProcesses]; !configured && len(selectors) == 0 {
		selectors = initDerivedProcessSelectors(servicemgr.DetectProcInfo(ctx, deps.ExecxRunner, nil, deps.Backend, unit))
	}
	return selectors, warnings
}

func noResidentProcess(tree map[string]any) bool {
	processes, ok := tree[config.SectionProcesses].(map[string]any)
	return ok && len(processes) == 0 && len(cfgval.StringList(tree[config.ServiceKeyPidfile])) == 0
}

func serviceNoResidentProcess(tree map[string]any, selectors []process.Selector, backendPIDs func() []int) bool {
	if noResidentProcess(tree) {
		return true
	}
	if processes, configured := tree[config.SectionProcesses].(map[string]any); configured && len(processes) > 0 && len(selectors) == 0 {
		return false
	}
	if len(selectors) > 0 || len(cfgval.StringList(tree[config.ServiceKeyPidfile])) > 0 {
		return false
	}
	return !hasBackendPIDs(backendPIDs)
}

func hasBackendPIDs(backendPIDs func() []int) bool {
	if backendPIDs == nil {
		return false
	}
	for _, pid := range backendPIDs() {
		if pid > 0 {
			return true
		}
	}
	return false
}

func initDerivedProcessSelectors(info servicemgr.ProcInfo) []process.Selector {
	if info.Pidfile != "" {
		return []process.Selector{{
			Name:  "init",
			Type:  process.SelectorPidfile,
			Paths: []string{info.Pidfile},
		}}
	}
	if info.Cmd != "" && info.User != "" {
		return []process.Selector{{
			Name: "init",
			Type: process.SelectorCommandMatch,
			Cmd:  info.Cmd,
			User: info.User,
		}}
	}
	if info.Exe != "" && info.User != "" {
		return []process.Selector{{
			Name: "init",
			Type: process.SelectorCommandMatch,
			Exe:  info.Exe,
			User: info.User,
		}}
	}
	return nil
}
