package app

import (
	"context"
	"fmt"
	"path/filepath"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

// serviceRuntime builds the per-service runtime pieces shared by a worker and the
// web backend: a process discoverer, the check deps (with a backend-status
// closure), and the safe operation engine. The engine's per-service operation
// lock serializes start/stop/restart across the worker and the web.
func serviceRuntime(name, unit string, tree map[string]any, deps Deps) (operation.Engine, checks.Deps, process.Discoverer) {
	discoverer := process.NewDiscoverer()
	discoverer.BackendPIDs = servicemgr.BackendPIDsFunc(deps.Backend, unit)
	checkDeps := checks.Deps{
		Service:        name,
		DefaultTimeout: deps.DefaultTimeout,
		Status: func(ctx context.Context) (servicemgr.Status, error) {
			st, err := deps.Manager.Status(ctx, unit)
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
		Manager:    deps.Manager,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(deps.Runtime, "locks")),
		Discoverer: discoverer,
		CheckDeps:  checkDeps,
		Sleep:      deps.Sleep,
	})
	return engine, checkDeps, discoverer
}

// webEntry is one service's web-backend record.
type webEntry struct {
	displayName string
	unit        string
	backend     string
	engine      operation.Engine
	status      func(context.Context) (servicemgr.Status, error)
}

// WebBackend implements web.Backend over the daemon's services: status from the
// backend, monitoring state from the store, and start/stop/restart through the
// same safe operation engine the workers use.
type WebBackend struct {
	order   []string
	entries map[string]*webEntry
	store   MonitorStore
}

// NewWebBackend resolves every enabled service once and wires its status, engine
// and metadata for the web UI. Services that fail to resolve are skipped with a
// warning (like BuildWorkers).
func NewWebBackend(cfg *config.Config, deps Deps) (*WebBackend, []string) {
	wb := &WebBackend{entries: map[string]*webEntry{}, store: deps.Monitor}
	var warnings []string
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
			unit = base
		}
		engine, checkDeps, _ := serviceRuntime(name, unit, resolved.Tree, deps)
		wb.entries[name] = &webEntry{
			displayName: config.DisplayName(resolved.Tree, name),
			unit:        unit,
			backend:     string(deps.Backend),
			engine:      engine,
			status:      checkDeps.Status,
		}
		wb.order = append(wb.order, name)
	}
	return wb, warnings
}

func (b *WebBackend) Services(ctx context.Context) []web.Service {
	out := make([]web.Service, 0, len(b.order))
	for _, name := range b.order {
		e := b.entries[name]
		status := "unknown"
		if e.status != nil {
			if st, err := e.status(ctx); err != nil {
				status = "error"
			} else {
				status = string(st)
			}
		}
		monitored := true // no recorded state defaults to monitored
		if b.store != nil {
			if active, found, err := b.store.Active(name); err == nil && found {
				monitored = active
			}
		}
		out = append(out, web.Service{
			Name:        name,
			DisplayName: e.displayName,
			Backend:     e.backend,
			Unit:        e.unit,
			Status:      status,
			Monitored:   monitored,
		})
	}
	return out
}

func (b *WebBackend) Operate(ctx context.Context, name, action string) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		return web.ActionResult{OK: false, Message: "unknown service " + name}
	}
	var r operation.Result
	switch action {
	case "start":
		r = e.engine.Start(ctx)
	case "stop":
		r = e.engine.Stop(ctx)
	case "restart":
		r = e.engine.Restart(ctx)
	default:
		return web.ActionResult{OK: false, Message: "unknown action " + action}
	}
	msg := r.Message
	if msg == "" {
		msg = string(r.Status)
	}
	return web.ActionResult{OK: r.OK(), Message: msg}
}

func (b *WebBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	if _, ok := b.entries[name]; !ok {
		return fmt.Errorf("unknown service %q", name)
	}
	if b.store == nil {
		return fmt.Errorf("monitoring state is unavailable")
	}
	return b.store.SetActive(name, monitored, state.SourceWeb)
}
