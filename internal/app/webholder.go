package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"sermo/internal/config"
	"sermo/internal/web"
)

const (
	webBackendUnavailableMessage = "web backend unavailable"
	initialWebBackendGeneration  = 1
)

// webGeneration is one immutable (backend, generation) pair. A reload publishes
// a new pair instead of mutating the current one, so a request that took the
// pointer keeps a consistent view for as long as it needs it.
type webGeneration struct {
	backend    *WebBackend
	generation uint64
}

// WebBackendHolder exposes a web.Backend that can be swapped on config reload.
// The active generation is published through an atomic pointer: readers never
// block a reload and a reload never blocks readers, however long an in-flight
// web action runs.
type WebBackendHolder struct {
	reload  sync.Mutex // serializes Reload only; never taken on a read path
	current atomic.Pointer[webGeneration]
}

// NewWebBackendHolder builds the initial backend.
func NewWebBackendHolder(ctx context.Context, cfg *config.Config, deps Deps) (*WebBackendHolder, []string) {
	b, warnings := NewWebBackend(ctx, cfg, deps)
	h := &WebBackendHolder{}
	h.current.Store(&webGeneration{backend: b, generation: initialWebBackendGeneration})
	return h, warnings
}

// Reload rebuilds the backend from the new config and publishes it atomically.
func (h *WebBackendHolder) Reload(ctx context.Context, cfg *config.Config, deps Deps) []string {
	if h == nil {
		return nil
	}
	b, warnings := NewWebBackend(ctx, cfg, deps)
	h.reload.Lock()
	defer h.reload.Unlock()
	generation := uint64(initialWebBackendGeneration)
	if cur := h.current.Load(); cur != nil {
		if cur.backend != nil && cur.backend.daemonMetrics != nil {
			b.daemonMetrics = cur.backend.daemonMetrics
		}
		if cur.generation > 0 {
			generation = cur.generation + 1
		}
	}
	h.current.Store(&webGeneration{backend: b, generation: generation})
	return warnings
}

func (h *WebBackendHolder) backend() *WebBackend {
	b, _ := h.backendAndGeneration()
	return b
}

func (h *WebBackendHolder) backendAndGeneration() (*WebBackend, uint64) {
	if h == nil {
		return nil, 0
	}
	cur := h.current.Load()
	if cur == nil {
		return nil, 0
	}
	return cur.backend, cur.generation
}

// BackendGeneration reports the active configuration generation for web API
// responses. It is sampled after a read so callers can detect a concurrent
// reload and avoid combining old and new views.
func (h *WebBackendHolder) BackendGeneration() uint64 {
	_, generation := h.backendAndGeneration()
	return generation
}

// BeginBackendRead pins one backend generation for the caller. The pin is the
// returned instance itself, not a lock: the web server keeps using it for the
// whole request, so a reload can never swap the backend between collecting data
// and attaching that response's generation marker, nor change service/watch
// identity underneath an action that already passed its generation
// precondition. Because nothing is held, a long action cannot stall a reload
// (and through it the monitoring cycle) or starve concurrent dashboard reads.
func (h *WebBackendHolder) BeginBackendRead() (web.Backend, uint64) {
	b, generation := h.backendAndGeneration()
	if b == nil {
		return nil, generation
	}
	return b, generation
}

// MaxOperationTimeout reports the longest current operation timeout, including
// per-service stop policies, from the active reloadable backend.
func (h *WebBackendHolder) MaxOperationTimeout() time.Duration {
	return webCall(h, time.Duration(0), func(b *WebBackend) time.Duration {
		return b.maxOperationTimeout()
	})
}

// DashboardSnapshot collects the aggregate dashboard from exactly one active
// backend generation, even if Reload swaps the holder while the request runs.
func (h *WebBackendHolder) DashboardSnapshot(ctx context.Context, since time.Duration) web.DashboardSnapshot {
	b, generation := h.backendAndGeneration()
	if b == nil {
		return web.DashboardSnapshot{}
	}
	snapshot := b.DashboardSnapshot(ctx, since)
	snapshot.Generation = generation
	return snapshot
}

// webCall runs fn against the active backend, returning zero when no backend
// is installed; the nil-guard shared by every delegate below.
func webCall[T any](h *WebBackendHolder, zero T, fn func(b *WebBackend) T) T {
	if b := h.backend(); b != nil {
		return fn(b)
	}
	return zero
}

// webCallOK is webCall for the (value, ok) lookups; a missing backend reports
// zero, false.
func webCallOK[T any](h *WebBackendHolder, zero T, fn func(b *WebBackend) (T, bool)) (T, bool) {
	if b := h.backend(); b != nil {
		return fn(b)
	}
	return zero, false
}

// unavailableAction is the shared failure result when no backend is installed.
func unavailableAction() web.ActionResult {
	return web.ActionResult{OK: false, Message: webBackendUnavailableMessage}
}

// Services returns the service list from the active backend (nil if unset).
func (h *WebBackendHolder) Services(ctx context.Context) []web.Service {
	return webCall(h, nil, func(b *WebBackend) []web.Service { return b.Services(ctx) })
}

// Sessions returns the interactive-session inventory from the active backend.
func (h *WebBackendHolder) Sessions(ctx context.Context) web.SessionInventory {
	return webCall(h, web.SessionInventory{}, func(b *WebBackend) web.SessionInventory { return b.Sessions(ctx) })
}

// Watches returns host-level and service-scoped watches from the active backend.
func (h *WebBackendHolder) Watches(ctx context.Context) []web.Watch {
	return webCall(h, nil, func(b *WebBackend) []web.Watch { return b.Watches(ctx) })
}

// Notifiers returns the configured notifiers from the active backend.
func (h *WebBackendHolder) Notifiers(ctx context.Context) []web.Notifier {
	return webCall(h, nil, func(b *WebBackend) []web.Notifier { return b.Notifiers(ctx) })
}

// TestNotifier sends an explicit test message through a configured notifier.
func (h *WebBackendHolder) TestNotifier(ctx context.Context, name string) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.TestNotifier(ctx, name) })
}

// Applications returns the installed applications from the active backend.
func (h *WebBackendHolder) Applications(ctx context.Context) []web.Application {
	return webCall(h, nil, func(b *WebBackend) []web.Application { return b.Applications(ctx) })
}

// Libraries returns the installed catalog libraries from the active backend.
func (h *WebBackendHolder) Libraries(ctx context.Context) []web.Library {
	return webCall(h, nil, func(b *WebBackend) []web.Library { return b.Libraries(ctx) })
}

// Mounts returns configured mount units from the active backend.
func (h *WebBackendHolder) Mounts(ctx context.Context) []web.Mount {
	return webCall(h, nil, func(b *WebBackend) []web.Mount { return b.Mounts(ctx) })
}

// MountAction runs mount or umount through the active backend.
func (h *WebBackendHolder) MountAction(ctx context.Context, name, action string, opts web.MountActionOptions) web.MountActionResult {
	return webCall(h, web.MountActionResult{OK: false, Name: name, Action: action, Message: webBackendUnavailableMessage},
		func(b *WebBackend) web.MountActionResult { return b.MountAction(ctx, name, action, opts) })
}

// MountBlockers reports current mount blockers through the active backend.
//
//nolint:dupl // interface forwarding: already one webCall line plus the unavailable-backend zero value this method must name; there is nothing left to fold.
func (h *WebBackendHolder) MountBlockers(ctx context.Context, name string) web.MountBlockersResult {
	return webCall(h, web.MountBlockersResult{OK: false, Name: name, Message: webBackendUnavailableMessage},
		func(b *WebBackend) web.MountBlockersResult { return b.MountBlockers(ctx, name) })
}

// AlertMountUsers sends a console alert through the active backend.
//
//nolint:dupl // interface forwarding; see MountBlockers.
func (h *WebBackendHolder) AlertMountUsers(ctx context.Context, name string) web.MountAlertResult {
	return webCall(h, web.MountAlertResult{OK: false, Name: name, Message: webBackendUnavailableMessage},
		func(b *WebBackend) web.MountAlertResult { return b.AlertMountUsers(ctx, name) })
}

// DaemonInfo returns daemon and engine info from the active backend.
func (h *WebBackendHolder) DaemonInfo(ctx context.Context) web.DaemonInfo {
	return webCall(h, web.DaemonInfo{}, func(b *WebBackend) web.DaemonInfo { return b.DaemonInfo(ctx) })
}

// DaemonMetrics returns sermod process metrics from the active backend.
func (h *WebBackendHolder) DaemonMetrics(ctx context.Context, since time.Duration) web.DaemonMetrics {
	return webCall(h, web.DaemonMetrics{Since: since.String()},
		func(b *WebBackend) web.DaemonMetrics { return b.DaemonMetrics(ctx, since) })
}

// HostMetrics returns current host metrics from the active backend.
func (h *WebBackendHolder) HostMetrics(ctx context.Context) []web.HostMetric {
	return webCall(h, nil, func(b *WebBackend) []web.HostMetric { return b.HostMetrics(ctx) })
}

// Locks returns runtime locks from the active backend.
func (h *WebBackendHolder) Locks(ctx context.Context) []web.Lock {
	return webCall(h, nil, func(b *WebBackend) []web.Lock { return b.Locks(ctx) })
}

// ReleaseLock removes an inactive named runtime lock from the active backend.
func (h *WebBackendHolder) ReleaseLock(ctx context.Context, service, name string) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.ReleaseLock(ctx, service, name) })
}

// ActivitySummary returns the recent-activity rollup from the active backend.
func (h *WebBackendHolder) ActivitySummary(ctx context.Context) web.ActivitySummary {
	return webCall(h, web.ActivitySummary{}, func(b *WebBackend) web.ActivitySummary { return b.ActivitySummary(ctx) })
}

// MonitoringStatus returns the monitored/paused summary from the active backend.
func (h *WebBackendHolder) MonitoringStatus(ctx context.Context) web.MonitoringStatus {
	return webCall(h, web.MonitoringStatus{}, func(b *WebBackend) web.MonitoringStatus { return b.MonitoringStatus(ctx) })
}

// Detail returns a service's detail from the active backend.
func (h *WebBackendHolder) Detail(ctx context.Context, name string) (web.Detail, bool) {
	return webCallOK(h, web.Detail{}, func(b *WebBackend) (web.Detail, bool) { return b.Detail(ctx, name) })
}

// Series returns a service's (or one check's) SLA series from the active backend.
func (h *WebBackendHolder) Series(ctx context.Context, name, check string, since time.Duration) ([]web.SeriesPoint, bool) {
	return webCallOK(h, nil, func(b *WebBackend) ([]web.SeriesPoint, bool) { return b.Series(ctx, name, check, since) })
}

// Metrics returns a check's metric series from the active backend.
func (h *WebBackendHolder) Metrics(ctx context.Context, name, check, metric string, since time.Duration) (web.MetricSeries, bool) {
	return webCallOK(h, web.MetricSeries{},
		func(b *WebBackend) (web.MetricSeries, bool) { return b.Metrics(ctx, name, check, metric, since) })
}

// ServiceRuntime returns process-tree runtime series from the active backend.
func (h *WebBackendHolder) ServiceRuntime(ctx context.Context, name string, since time.Duration) (web.ServiceRuntimeMetrics, bool) {
	return webCallOK(h, web.ServiceRuntimeMetrics{Since: since.String()},
		func(b *WebBackend) (web.ServiceRuntimeMetrics, bool) { return b.ServiceRuntime(ctx, name, since) })
}

// Events returns recent events from the active backend.
func (h *WebBackendHolder) Events(ctx context.Context, limit int) []web.Event {
	return webCall(h, nil, func(b *WebBackend) []web.Event { return b.Events(ctx, limit) })
}

// EventPage returns a filtered cursor page from the active backend.
func (h *WebBackendHolder) EventPage(ctx context.Context, query web.EventQuery) web.EventPage {
	return webCall(h, web.EventPage{}, func(b *WebBackend) web.EventPage { return b.EventPage(ctx, query) })
}

// ServiceEvents returns a service's recent events from the active backend.
func (h *WebBackendHolder) ServiceEvents(ctx context.Context, name string, limit int) ([]web.Event, bool) {
	return webCallOK(h, nil, func(b *WebBackend) ([]web.Event, bool) { return b.ServiceEvents(ctx, name, limit) })
}

// ApplicationEvents returns one application's recent events through the active backend.
func (h *WebBackendHolder) ApplicationEvents(ctx context.Context, name string, limit int) ([]web.Event, bool) {
	return webCallOK(h, nil, func(b *WebBackend) ([]web.Event, bool) { return b.ApplicationEvents(ctx, name, limit) })
}

// PruneEvents removes old events from the active event feed (if the backend is available).
func (h *WebBackendHolder) PruneEvents(ctx context.Context, before time.Time) int {
	return webCall(h, 0, func(b *WebBackend) int { return b.PruneEvents(ctx, before) })
}

// Operate runs a start/stop/restart/reload/resume action through the active backend.
func (h *WebBackendHolder) Operate(ctx context.Context, name, action string, opts web.OperateOpts) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.Operate(ctx, name, action, opts) })
}

// CloseSSHSession gracefully closes one revalidated SSH terminal through the
// active backend.
func (h *WebBackendHolder) CloseSSHSession(ctx context.Context, name string, session web.SSHSession) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult {
		return b.CloseSSHSession(ctx, name, session)
	})
}

// CloseTerminalSession closes one revalidated tmux or screen session through
// the active backend.
func (h *WebBackendHolder) CloseTerminalSession(ctx context.Context, name string, session web.TerminalSession) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult {
		return b.CloseTerminalSession(ctx, name, session)
	})
}

// CloseEmptyTerminalSession closes one revalidated empty tmux server through
// the active backend.
func (h *WebBackendHolder) CloseEmptyTerminalSession(ctx context.Context, name, check string) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult {
		return b.CloseEmptyTerminalSession(ctx, name, check)
	})
}

// CompactState prunes old persisted history through the active backend.
func (h *WebBackendHolder) CompactState(ctx context.Context, before time.Time) web.StateCompactResult {
	return webCall(h, web.StateCompactResult{OK: false, Message: webBackendUnavailableMessage},
		func(b *WebBackend) web.StateCompactResult { return b.CompactState(ctx, before) })
}

// Preflight runs a service's preflight checks through the active backend.
func (h *WebBackendHolder) Preflight(ctx context.Context, name string) (web.PreflightResult, bool) {
	return webCallOK(h, web.PreflightResult{}, func(b *WebBackend) (web.PreflightResult, bool) { return b.Preflight(ctx, name) })
}

// SetMonitored toggles a service's monitoring through the active backend.
func (h *WebBackendHolder) SetMonitored(ctx context.Context, name string, monitored bool) error {
	return webCall(h, nil, func(b *WebBackend) error { return b.SetMonitored(ctx, name, monitored) })
}

// SetPanic toggles the daemon-wide panic mode through the active backend.
func (h *WebBackendHolder) SetPanic(ctx context.Context, on bool) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.SetPanic(ctx, on) })
}

// SetWatchMonitored toggles a host watch's monitoring through the active backend.
func (h *WebBackendHolder) SetWatchMonitored(ctx context.Context, name string, monitored bool) error {
	return webCall(h, nil, func(b *WebBackend) error { return b.SetWatchMonitored(ctx, name, monitored) })
}

// ExpandWatch runs a configured storage-watch expansion through the active backend.
func (h *WebBackendHolder) ExpandWatch(ctx context.Context, name string) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.ExpandWatch(ctx, name) })
}

// ProbeWatch runs an isolated manual watch sample through the active backend.
func (h *WebBackendHolder) ProbeWatch(ctx context.Context, name string) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.ProbeWatch(ctx, name) })
}

// ControlRAID pauses or resumes a configured RAID reconstruction through the active backend.
func (h *WebBackendHolder) ControlRAID(ctx context.Context, name, action, confirmation string) web.ActionResult {
	return webCall(h, unavailableAction(), func(b *WebBackend) web.ActionResult { return b.ControlRAID(ctx, name, action, confirmation) })
}
