package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/diag"
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
func serviceRuntime(name, unit string, tree map[string]any, deps Deps, recordOperation func(operation.Result)) (operation.Engine, checks.Deps, process.Discoverer) {
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
	locker := configureOperationLocker(deps.Runtime, operationLockReclaimEvent(deps.Emit))
	engine := operation.New(operation.Config{
		Service:    name,
		Unit:       unit,
		Backend:    string(deps.Backend),
		Tree:       tree,
		Manager:    deps.Manager,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(deps.Runtime, "locks")),
		Discoverer: discoverer,
		CheckDeps:        checkDeps,
		Sleep:            deps.Sleep,
		OperationTimeout: deps.OperationTimeout,
		Emit:             recordOperation,
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
	discoverer  process.Discoverer
	selectors   []process.Selector
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
	events    *EventLog
	cfg       *config.Config
	diagStore diag.Store
	host      diag.Host
	measure   MeasurementReader
	emit      func(Event)
	opGate    *OpGate
}

// NewWebBackend resolves every enabled service once and wires its status, engine
// and metadata for the web UI. Services that fail to resolve are skipped with a
// warning (like BuildWorkers).
func NewWebBackend(cfg *config.Config, deps Deps) (*WebBackend, []string) {
	wb := &WebBackend{
		entries: map[string]*webEntry{}, store: deps.Monitor, snapshots: deps.Snapshots,
		events: deps.Events, cfg: cfg, host: diag.OSHost{}, emit: deps.Emit, opGate: deps.OpGate,
	}
	wb.sla, _ = deps.SLA.(SLAReader)
	wb.measure, _ = deps.SLA.(MeasurementReader)
	wb.diagStore, _ = deps.Monitor.(diag.Store)
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
		engine, checkDeps, discoverer := serviceRuntime(name, unit, resolved.Tree, deps, operationEventEmitter(deps.Emit))
		selectors, _ := process.ParseSelectors(resolved.Tree)
		names, types := checkCatalog(resolved.Tree)
		wb.entries[name] = &webEntry{
			displayName: config.DisplayName(resolved.Tree, name),
			unit:        unit,
			backend:     string(deps.Backend),
			engine:      engine,
			status:      checkDeps.Status,
			checkNames:  names,
			checkTypes:  types,
			discoverer:  discoverer,
			selectors:   selectors,
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
	svc := web.Service{
		Name:        name,
		DisplayName: e.displayName,
		Backend:     e.backend,
		Unit:        e.unit,
		Status:      status,
		Monitored:   true, // no recorded state defaults to monitored
	}
	if b.store != nil {
		if rec, found, err := b.store.MonitorState(name); err == nil && found {
			svc.Monitored = rec.Active
			svc.MonitorSource = rec.Source
			if !rec.UpdatedAt.IsZero() {
				svc.MonitorChangedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
			}
		}
	}
	return svc
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
		cs, seen := snap[cn]
		ch := web.Check{
			Name:     cn,
			Type:     e.checkTypes[cn],
			OK:       cs.OK,
			Optional: cs.Optional,
			Skipped:  cs.Skipped,
			Message:  cs.Message,
			Ran:      seen && cs.Ran,
		}
		if seen && !cs.At.IsZero() {
			ch.At = cs.At.UTC().Format(time.RFC3339)
		}
		d.Checks = append(d.Checks, ch)
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

	if b.cfg != nil {
		dir := filepath.Join(b.cfg.Global.RuntimeDir(), "locks")
		if report, err := locks.NewScanner(dir).Scan(name); err == nil {
			for _, lk := range report.Locks {
				d.Locks = append(d.Locks, lockToWeb(lk))
			}
		}
	}

	procs, _ := e.discoverer.Discover(e.selectors)
	for _, p := range procs {
		d.Processes = append(d.Processes, processToWeb(p))
	}
	return d, true
}

func processToWeb(p process.Process) web.Process {
	return web.Process{
		PID:         p.PID,
		PPID:        p.PPID,
		User:        p.User,
		Exe:         p.Exe,
		ExeResolved: p.ExeOK,
		Role:        p.Role,
		Source:      p.Source,
		Cmdline:     p.Cmdline,
	}
}

func lockToWeb(lk locks.Lock) web.Lock {
	w := web.Lock{
		Name:        lk.Name,
		Reason:      lk.Reason,
		State:       string(lk.State),
		OwnerPID:    lk.OwnerPID,
		StaleReason: lk.StaleReason,
	}
	if !lk.CreatedAt.IsZero() {
		w.CreatedAt = lk.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !lk.ExpiresAt.IsZero() {
		w.ExpiresAt = lk.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return w
}

func (b *WebBackend) Series(_ context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	e := b.entries[name]
	if e == nil {
		return nil, false
	}
	if b.sla == nil {
		return []web.SeriesPoint{}, true
	}
	now := time.Now()
	pts, err := b.sla.SLASeries(name, now.Add(-since), now)
	if err != nil {
		return []web.SeriesPoint{}, true
	}
	out := make([]web.SeriesPoint, 0, len(pts))
	for _, p := range pts {
		sp := web.SeriesPoint{Start: p.Start.Format(time.RFC3339), Up: p.Up, Total: p.Total}
		if p.Total > 0 {
			ratio := float64(p.Up) / float64(p.Total)
			sp.Ratio = &ratio
		}
		out = append(out, sp)
	}
	return out, true
}

func (b *WebBackend) Diagnostics(_ context.Context) []web.Finding {
	r := diag.Diagnose(b.cfg, b.diagStore, b.host)
	out := make([]web.Finding, 0, len(r.Findings)+1)
	for _, f := range r.Findings {
		out = append(out, web.Finding{Level: string(f.Level), Scope: f.Scope, Message: f.Message})
	}
	if b.opGate != nil {
		inUse, total := b.opGate.Usage()
		out = append(out, operationSlotFindings(inUse, total)...)
	}
	return out
}

func operationSlotFindings(inUse, total int) []web.Finding {
	if total <= 0 || inUse <= 0 {
		return nil
	}
	if inUse >= total {
		return []web.Finding{{
			Level:   "warning",
			Scope:   "operations",
			Message: fmt.Sprintf("operation slots saturated (%d/%d in use)", inUse, total),
		}}
	}
	return []web.Finding{{
		Level:   "info",
		Scope:   "operations",
		Message: fmt.Sprintf("operation slots %d/%d in use", inUse, total),
	}}
}

func (b *WebBackend) Operations(_ context.Context) web.OperationSlots {
	if b.opGate == nil {
		return web.OperationSlots{}
	}
	inUse, total := b.opGate.Usage()
	return web.OperationSlots{InUse: inUse, Total: total}
}

func (b *WebBackend) Metrics(_ context.Context, name, check string, since time.Duration) (web.MetricSeries, bool) {
	e := b.entries[name]
	if e == nil {
		return web.MetricSeries{}, false
	}
	typ, ok := e.checkTypes[check]
	if !ok || !measuredCheckTypes[typ] {
		return web.MetricSeries{}, false
	}
	out := web.MetricSeries{Check: check, Since: since.String(), Unit: "ms"}
	if b.measure == nil {
		return out, true
	}
	now := time.Now()
	if stat, err := b.measure.MeasurementSummary(name, check, since, now); err == nil {
		out.Summary = web.MetricSummary{Count: stat.Count, Avg: stat.Avg, Min: stat.Min, Max: stat.Max}
	}
	points, err := b.measure.MeasurementSeries(name, check, now.Add(-since), now)
	if err == nil {
		out.Points = make([]web.MetricPoint, 0, len(points))
		for _, p := range points {
			out.Points = append(out.Points, web.MetricPoint{
				Start: p.Start.Format(time.RFC3339), N: p.N, Avg: p.Avg, Min: p.Min, Max: p.Max,
			})
		}
	}
	return out, true
}

func (b *WebBackend) Events(_ context.Context, limit int) []web.Event {
	if b.events == nil {
		return nil
	}
	return toWebEvents(b.events.Recent("", limit))
}

func (b *WebBackend) ServiceEvents(_ context.Context, name string, limit int) ([]web.Event, bool) {
	if _, ok := b.entries[name]; !ok {
		return nil, false
	}
	if b.events == nil {
		return nil, true
	}
	return toWebEvents(b.events.Recent(name, limit)), true
}

func toWebEvents(events []LoggedEvent) []web.Event {
	out := make([]web.Event, 0, len(events))
	for _, e := range events {
		out = append(out, web.Event{
			Time:    e.Time.Format(time.RFC3339),
			Service: e.Service,
			Watch:   e.Watch,
			Kind:    e.Kind,
			Rule:    e.Rule,
			Action:  e.Action,
			Status:  e.Status,
			Message: e.Message,
		})
	}
	return out
}

func (b *WebBackend) Operate(ctx context.Context, name, action string) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		msg := "unknown service " + name
		if b.emit != nil {
			b.emit(Event{Service: name, Kind: "error", Action: action, Message: msg})
		}
		return web.ActionResult{OK: false, Message: msg}
	}
	run := func(ctx context.Context) operation.Result {
		switch action {
		case "start":
			return e.engine.Start(ctx)
		case "stop":
			return e.engine.Stop(ctx)
		case "restart":
			return e.engine.Restart(ctx)
		default:
			return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: "unknown action " + action}
		}
	}
	var r operation.Result
	if b.opGate != nil {
		r = b.opGate.Run(ctx, name, action, run)
	} else {
		r = run(ctx)
	}
	if r.Action == "" && action != "" {
		r.Action = action
	}
	if r.Service == "" {
		r.Service = name
	}
	msg := r.Message
	if msg == "" {
		msg = string(r.Status)
	}
	return web.ActionResult{OK: r.OK(), Message: msg}
}

func (b *WebBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	action := "monitor"
	if !monitored {
		action = "unmonitor"
	}
	if _, ok := b.entries[name]; !ok {
		msg := fmt.Sprintf("unknown service %q", name)
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := "monitoring state is unavailable"
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	priorActive, found, err := b.store.Active(name)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(name, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := "already monitored"
		if !monitored {
			msg = "already paused"
		}
		b.emitMonitorEvent(name, action, "suppressed", "", msg)
		return nil
	}
	msg := "monitoring resumed"
	if !monitored {
		msg = "monitoring paused"
	}
	b.emitMonitorEvent(name, action, "action", "ok", msg)
	return nil
}

func (b *WebBackend) emitMonitorEvent(service, action, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Service: service,
		Kind:    kind,
		Action:  action,
		Status:  status,
		Message: message,
	})
}
