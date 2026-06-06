package app

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
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
}

// BuildWorkers resolves every enabled service and wires a Worker for it: a check
// cache producer and an operation-engine Operate closure (section 24). Services
// that are disabled or fail to resolve are skipped with a warning.
func BuildWorkers(cfg *config.Config, deps Deps) ([]*Worker, []string) {
	var workers []*Worker
	var warnings []string

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
		workers = append(workers, buildWorker(name, resolved.Tree, deps))
	}
	return workers, warnings
}

func buildWorker(name string, tree map[string]any, deps Deps) *Worker {
	unit := serviceUnit(tree, name)
	manager := deps.Manager

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
		Discoverer: process.NewDiscoverer(),
		CheckDeps:  checkDeps,
		Sleep:      deps.Sleep,
	})

	maxParallel := deps.MaxParallel
	ruleSet, _ := rules.ParseRules(tree)

	return &Worker{
		Service:   name,
		Rules:     ruleSet,
		Policy:    rules.ParsePolicy(tree),
		State:     &rules.RemediationState{},
		CheckDeps: checkDeps,
		Checks: func(ctx context.Context) map[string]checks.Result {
			section, _ := tree["checks"].(map[string]any)
			built, _ := checks.Build(section, checkDeps)
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

func serviceUnit(tree map[string]any, fallback string) string {
	if svc, ok := tree["service"].(map[string]any); ok {
		if name, _ := svc["name"].(string); name != "" {
			return name
		}
	}
	return fallback
}
