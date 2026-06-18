package app

import (
	"context"
	"path/filepath"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// serviceRuntime builds the per-service runtime pieces shared by a worker and the
// web backend: a process discoverer, the check deps (with a backend-status
// closure), and the safe operation engine. The engine's per-service operation
// lock serializes start/stop/restart/reload/resume across the worker and the web.
func serviceRuntime(name, unit string, tree map[string]any, deps Deps, recordOperation func(operation.Result)) (operation.Engine, checks.Deps, process.Discoverer) {
	lookup := deps.UserLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	discoverer := process.NewDiscovererWithUserLookup(lookup)
	if deps.ProcReader != nil {
		discoverer.Reader = deps.ProcReader
	}
	backendPIDs := deps.BackendPIDs
	if backendPIDs == nil && deps.Backend != servicemgr.BackendLibvirt && deps.Backend != servicemgr.BackendDocker {
		backendPIDs = servicemgr.BackendPIDsFuncWithRunner(deps.Backend, unit, deps.ExecxRunner, nil)
	}
	if backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
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
		Scanner:          locks.NewScanner(filepath.Join(deps.Runtime, "locks")),
		Discoverer:       discoverer,
		ResolveUser:      discoverer.ResolveUser,
		CheckDeps:        checkDeps,
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

// serviceProcessSelectors returns the process selectors a service should use
// for both monitoring workers and web detail. Explicit `processes:` entries win;
// otherwise we derive the safest init-provided identity we can detect.
func serviceProcessSelectors(ctx context.Context, tree map[string]any, deps Deps, unit string) ([]process.Selector, []string) {
	selectors, warnings := process.ParseSelectors(tree)
	if _, configured := tree["processes"]; !configured && len(selectors) == 0 {
		selectors = initDerivedProcessSelectors(servicemgr.DetectProcInfo(ctx, deps.ExecxRunner, nil, deps.Backend, unit))
	}
	return selectors, warnings
}

func noResidentProcess(tree map[string]any) bool {
	processes, ok := tree["processes"].(map[string]any)
	return ok && len(processes) == 0
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
