package app

import (
	"context"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/web"
)

const webBackendUnavailableMessage = "web backend unavailable"

// WebBackendHolder exposes a web.Backend that can be swapped on config reload.
type WebBackendHolder struct {
	mu sync.RWMutex
	b  *WebBackend
}

// NewWebBackendHolder builds the initial backend.
func NewWebBackendHolder(ctx context.Context, cfg *config.Config, deps Deps) (*WebBackendHolder, []string) {
	b, warnings := NewWebBackend(ctx, cfg, deps)
	return &WebBackendHolder{b: b}, warnings
}

// Reload rebuilds the backend from the new config and swaps it in atomically.
func (h *WebBackendHolder) Reload(ctx context.Context, cfg *config.Config, deps Deps) []string {
	if h == nil {
		return nil
	}
	b, warnings := NewWebBackend(ctx, cfg, deps)
	h.mu.Lock()
	if h.b != nil && h.b.daemonMetrics != nil {
		b.daemonMetrics = h.b.daemonMetrics
	}
	h.b = b
	h.mu.Unlock()
	return warnings
}

func (h *WebBackendHolder) backend() *WebBackend {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.b
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
	return webCall(h, web.DashboardSnapshot{}, func(b *WebBackend) web.DashboardSnapshot {
		return b.DashboardSnapshot(ctx, since)
	})
}

// webCall runs fn against the active backend, returning zero when no backend
// is installed; the nil-guard shared by every delegate below.
//
//nolint:ireturn // T is the caller's concrete result type, not an interface.
func webCall[T any](h *WebBackendHolder, zero T, fn func(b *WebBackend) T) T {
	if b := h.backend(); b != nil {
		return fn(b)
	}
	return zero
}

// webCallOK is webCall for the (value, ok) lookups; a missing backend reports
// zero, false.
//
//nolint:ireturn // T is the caller's concrete result type, not an interface.
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

// Watches returns the host watches from the active backend.
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
func (h *WebBackendHolder) MountBlockers(ctx context.Context, name string) web.MountBlockersResult {
	return webCall(h, web.MountBlockersResult{OK: false, Name: name, Message: webBackendUnavailableMessage},
		func(b *WebBackend) web.MountBlockersResult { return b.MountBlockers(ctx, name) })
}

// AlertMountUsers sends a console alert through the active backend.
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

// Series returns a service's SLA series from the active backend.
func (h *WebBackendHolder) Series(ctx context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	return webCallOK(h, nil, func(b *WebBackend) ([]web.SeriesPoint, bool) { return b.Series(ctx, name, since) })
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

// Operations returns operation-slot usage from the active backend.
func (h *WebBackendHolder) Operations(ctx context.Context) web.OperationSlots {
	return webCall(h, web.OperationSlots{}, func(b *WebBackend) web.OperationSlots { return b.Operations(ctx) })
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
