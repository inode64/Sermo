package app

import (
	"context"
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
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

// ServiceRuntimeConfig describes one resolved service runtime. Callers supply
// presentation-specific callbacks, while BuildServiceRuntime owns every safety
// dependency used to judge and execute the operation.
type ServiceRuntimeConfig struct {
	Service         string
	Unit            string
	Tree            map[string]any
	Deps            Deps
	LibraryBaseline map[string]string
	LockReclaimed   func(service, reason string)
	RecordOperation func(operation.Result)
}

// ServiceRuntime is the canonical per-service runtime shared by sermod,
// sermoctl and the web backend.
type ServiceRuntime struct {
	Engine            operation.Engine
	CheckDeps         checks.Deps
	Discoverer        process.Discoverer
	Selectors         []process.Selector
	ProcessWarnings   []string
	NoResidentProcess bool
}

// BuildServiceRuntime builds the process discoverer, check dependencies and safe
// operation engine for one resolved service. The engine's per-service operation
// lock serializes start/stop/restart/reload/resume across every caller.
func BuildServiceRuntime(ctx context.Context, cfg ServiceRuntimeConfig) ServiceRuntime {
	deps := cfg.Deps
	lookup := deps.UserLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	discoverer := process.NewDiscovererWithUserLookup(lookup)
	if deps.ProcReader != nil {
		discoverer.Reader = deps.ProcReader
	}
	backendPIDs := ServiceBackendPIDs(ctx, deps.Backend, cfg.Unit, deps.BackendPIDs, deps.ExecxRunner)
	if backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	selectors, processWarnings := serviceProcessSelectors(ctx, cfg.Tree, deps, cfg.Unit)
	noResident := serviceNoResidentProcess(cfg.Tree, selectors, backendPIDs)
	metricSample := MetricSampleForOperation(cfg.Service, cfg.Tree, deps.Collector, discoverer, selectors)
	if noResident {
		metricSample = nil
	}
	checkDeps := checkDepsFromAppDeps(deps, checks.Deps{
		Service:        cfg.Service,
		DefaultTimeout: deps.DefaultTimeout,
		Runner:         deps.ExecxRunner,
		Status: func(ctx context.Context) (servicemgr.Status, error) {
			st, err := deps.Manager.Status(ctx, cfg.Unit)
			if err != nil {
				return "", fmt.Errorf("status %s: %w", cfg.Unit, err)
			}
			return st.Status, nil
		},
		Processes:    discoverer.ObserveState,
		ProcessesAny: discoverer.ObserveAnyState,
		// Service scope, not host scope: a process_count check inside a service
		// counts what discovery attributes to that service (docs promise exactly
		// this). The host-wide CountMatching here made a filterless catalog check
		// count every process on the host once the service died — 362 "active
		// jobs" for a crashed fcron — which latched its own block action and made
		// the service unrepairable through Sermo.
		ProcessCount:        func(user, exe, exeDir string) int { return discoverer.CountInTree(selectors, user, exe, exeDir) },
		PidfileFallbackPIDs: pidfileFallbackPIDs(ctx, deps, cfg.Unit, backendPIDs),
		StaleBinaries:       func() []process.StaleBinary { return discoverer.StaleBinaries(selectors) },
		Strays: func() []process.Process {
			procs, _ := discoverer.Discover(selectors)
			return process.Strays(procs)
		},
	})
	lockReclaimed := cfg.LockReclaimed
	if lockReclaimed == nil {
		lockReclaimed = operationLockReclaimEvent(deps.Emit)
	}
	locker := configureOperationLocker(deps.Runtime, lockReclaimed)
	engine := operation.New(operation.Config{
		Service:          cfg.Service,
		Unit:             cfg.Unit,
		Backend:          string(deps.Backend),
		Tree:             cfg.Tree,
		Manager:          deps.Manager,
		Locker:           &locker,
		Scanner:          locks.NewScanner(locks.RuntimeLocksDir(deps.Runtime)),
		Discoverer:       discoverer,
		ResolveUser:      discoverer.ResolveUser,
		CheckDeps:        checkDeps,
		MetricSample:     metricSample,
		Changed:          ArtifactChangedFunc(cfg.LibraryBaseline, deps.ArtifactSamples),
		Sleep:            deps.Sleep,
		OperationTimeout: deps.OperationTimeout,
		Emit:             cfg.RecordOperation,
	})
	return ServiceRuntime{
		Engine:            engine,
		CheckDeps:         checkDeps,
		Discoverer:        discoverer,
		Selectors:         selectors,
		ProcessWarnings:   processWarnings,
		NoResidentProcess: noResident,
	}
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

// ServiceBackendPIDs returns the backend-owned process roots for one resolved
// service. A control target's explicit provider wins; only init backends derive
// their process set from the unit.
func ServiceBackendPIDs(ctx context.Context, backend servicemgr.Backend, unit string, configured func() []int, runner execx.Runner) func() []int {
	if configured != nil {
		return configured
	}
	if backend != servicemgr.BackendSystemd && backend != servicemgr.BackendOpenRC {
		return nil
	}
	return servicemgr.BackendPIDsFuncWithRunner(ctx, backend, unit, runner, nil)
}

// ServiceScopedProcessCount returns the process_count dependency for one
// service's own checks, shared by the daemon and the CLI so both paths judge
// the same set: what discovery attributes to the service (selector matches plus
// descendants), never the whole host. The distinction is load-bearing for the
// catalog's filterless block checks — host scope turned "fcron has active
// jobs" into a count of every process on the host once the service died.
func ServiceScopedProcessCount(ctx context.Context, tree map[string]any, runner execx.Runner, backend servicemgr.Backend, unit string, discoverer process.Discoverer) func(user, exe, exeDir string) int {
	selectors, _ := serviceProcessSelectors(ctx, tree, Deps{ExecxRunner: runner, Backend: backend}, unit)
	return func(user, exe, exeDir string) int {
		return discoverer.CountInTree(selectors, user, exe, exeDir)
	}
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
	lifecycle, err := config.ResolveServiceLifecycle(tree, "")
	return err == nil && lifecycle.ProcessMode == config.ServiceProcessNone
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
