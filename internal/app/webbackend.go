package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

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
	checkNames  []string          // sorted
	checkTypes  map[string]string // check name -> type
}

// WebBackend implements web.Backend over the daemon's services: status from the
// backend, monitoring state and SLA from the store, the latest check results from
// the shared snapshots, and start/stop/restart through the same safe operation
// engine the workers use.
type WebBackend struct {
	order     []string
	entries   map[string]*webEntry
	store     MonitorStore
	snapshots *Snapshots
	sla       SLAReader
}

// NewWebBackend resolves every enabled service once and wires its status, engine
// and metadata for the web UI. Services that fail to resolve are skipped with a
// warning (like BuildWorkers).
func NewWebBackend(cfg *config.Config, deps Deps) (*WebBackend, []string) {
	wb := &WebBackend{entries: map[string]*webEntry{}, store: deps.Monitor, snapshots: deps.Snapshots}
	wb.sla, _ = deps.SLA.(SLAReader)
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
		names, types := checkCatalog(resolved.Tree)
		wb.entries[name] = &webEntry{
			displayName: config.DisplayName(resolved.Tree, name),
			unit:        unit,
			backend:     string(deps.Backend),
			engine:      engine,
			status:      checkDeps.Status,
			checkNames:  names,
			checkTypes:  types,
		}
		wb.order = append(wb.order, name)
	}
	return wb, warnings
}

// checkCatalog returns a service's check names (sorted) and their types, from the
// resolved `checks` section.
func checkCatalog(tree map[string]any) ([]string, map[string]string) {
	section, ok := tree["checks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	types := make(map[string]string, len(section))
	names := make([]string, 0, len(section))
	for name, raw := range section {
		typ := ""
		if m, ok := raw.(map[string]any); ok {
			typ, _ = m["type"].(string)
		}
		types[name] = typ
		names = append(names, name)
	}
	sort.Strings(names)
	return names, types
}

func (b *WebBackend) view(ctx context.Context, name string, e *webEntry) web.Service {
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
	return web.Service{
		Name:        name,
		DisplayName: e.displayName,
		Backend:     e.backend,
		Unit:        e.unit,
		Status:      status,
		Monitored:   monitored,
	}
}

func (b *WebBackend) Services(ctx context.Context) []web.Service {
	out := make([]web.Service, 0, len(b.order))
	for _, name := range b.order {
		out = append(out, b.view(ctx, name, b.entries[name]))
	}
	return out
}

func (b *WebBackend) Detail(ctx context.Context, name string) (web.Detail, bool) {
	e := b.entries[name]
	if e == nil {
		return web.Detail{}, false
	}
	d := web.Detail{Service: b.view(ctx, name, e)}

	snap := b.snapshots.Get(name)
	for _, cn := range e.checkNames {
		cs, ran := snap[cn]
		d.Checks = append(d.Checks, web.Check{
			Name:     cn,
			Type:     e.checkTypes[cn],
			OK:       cs.OK,
			Optional: cs.Optional,
			Message:  cs.Message,
			Ran:      ran,
		})
	}

	if b.sla != nil {
		if vals, err := b.sla.SLAReport(name, time.Now()); err == nil {
			for _, v := range vals {
				win := web.SLAWindow{Window: v.Window, Up: v.Up, Total: v.Total}
				if ratio, ok := v.Ratio(); ok {
					win.Ratio = &ratio
				}
				d.SLA = append(d.SLA, win)
			}
		}
	}
	return d, true
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
