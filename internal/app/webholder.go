package app

import (
	"context"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/web"
)

// WebBackendHolder exposes a web.Backend that can be swapped on config reload.
type WebBackendHolder struct {
	mu sync.RWMutex
	b  *WebBackend
}

// NewWebBackendHolder builds the initial backend.
func NewWebBackendHolder(cfg *config.Config, deps Deps) (*WebBackendHolder, []string) {
	b, warnings := NewWebBackend(cfg, deps)
	return &WebBackendHolder{b: b}, warnings
}

// Reload rebuilds the backend from the new config and swaps it in atomically.
func (h *WebBackendHolder) Reload(cfg *config.Config, deps Deps) []string {
	if h == nil {
		return nil
	}
	b, warnings := NewWebBackend(cfg, deps)
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

// Services returns the service list from the active backend (nil if unset).
func (h *WebBackendHolder) Services(ctx context.Context) []web.Service {
	if b := h.backend(); b != nil {
		return b.Services(ctx)
	}
	return nil
}

// Watches returns the host watches from the active backend.
func (h *WebBackendHolder) Watches(ctx context.Context) []web.Watch {
	if b := h.backend(); b != nil {
		return b.Watches(ctx)
	}
	return nil
}

// Notifiers returns the configured notifiers from the active backend.
func (h *WebBackendHolder) Notifiers(ctx context.Context) []web.Notifier {
	if b := h.backend(); b != nil {
		return b.Notifiers(ctx)
	}
	return nil
}

// Applications returns the installed applications from the active backend.
func (h *WebBackendHolder) Applications(ctx context.Context) []web.Application {
	if b := h.backend(); b != nil {
		return b.Applications(ctx)
	}
	return nil
}

// DaemonInfo returns daemon and engine info from the active backend.
func (h *WebBackendHolder) DaemonInfo(ctx context.Context) web.DaemonInfo {
	if b := h.backend(); b != nil {
		return b.DaemonInfo(ctx)
	}
	return web.DaemonInfo{}
}

// DaemonMetrics returns sermod process metrics from the active backend.
func (h *WebBackendHolder) DaemonMetrics(ctx context.Context, since time.Duration) web.DaemonMetrics {
	if b := h.backend(); b != nil {
		return b.DaemonMetrics(ctx, since)
	}
	return web.DaemonMetrics{Since: since.String()}
}

// HostMetrics returns current host metrics from the active backend.
func (h *WebBackendHolder) HostMetrics(ctx context.Context) []web.HostMetric {
	if b := h.backend(); b != nil {
		return b.HostMetrics(ctx)
	}
	return nil
}

// Locks returns runtime locks from the active backend.
func (h *WebBackendHolder) Locks(ctx context.Context) []web.Lock {
	if b := h.backend(); b != nil {
		return b.Locks(ctx)
	}
	return nil
}

// ReleaseLock removes an inactive named runtime lock from the active backend.
func (h *WebBackendHolder) ReleaseLock(ctx context.Context, service, name string) web.ActionResult {
	if b := h.backend(); b != nil {
		return b.ReleaseLock(ctx, service, name)
	}
	return web.ActionResult{OK: false, Message: "web backend unavailable"}
}

// ActivitySummary returns the recent-activity rollup from the active backend.
func (h *WebBackendHolder) ActivitySummary(ctx context.Context) web.ActivitySummary {
	if b := h.backend(); b != nil {
		return b.ActivitySummary(ctx)
	}
	return web.ActivitySummary{}
}

// MonitoringStatus returns the monitored/paused summary from the active backend.
func (h *WebBackendHolder) MonitoringStatus(ctx context.Context) web.MonitoringStatus {
	if b := h.backend(); b != nil {
		return b.MonitoringStatus(ctx)
	}
	return web.MonitoringStatus{}
}

// Detail returns a service's detail from the active backend.
func (h *WebBackendHolder) Detail(ctx context.Context, name string) (web.Detail, bool) {
	if b := h.backend(); b != nil {
		return b.Detail(ctx, name)
	}
	return web.Detail{}, false
}

// Series returns a service's SLA series from the active backend.
func (h *WebBackendHolder) Series(ctx context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	if b := h.backend(); b != nil {
		return b.Series(ctx, name, since)
	}
	return nil, false
}

// Metrics returns a check's metric series from the active backend.
func (h *WebBackendHolder) Metrics(ctx context.Context, name, check, metric string, since time.Duration) (web.MetricSeries, bool) {
	if b := h.backend(); b != nil {
		return b.Metrics(ctx, name, check, metric, since)
	}
	return web.MetricSeries{}, false
}

// ServiceRuntime returns process-tree runtime series from the active backend.
func (h *WebBackendHolder) ServiceRuntime(ctx context.Context, name string, since time.Duration) (web.ServiceRuntimeMetrics, bool) {
	if b := h.backend(); b != nil {
		return b.ServiceRuntime(ctx, name, since)
	}
	return web.ServiceRuntimeMetrics{Since: since.String()}, false
}

// Events returns recent events from the active backend.
func (h *WebBackendHolder) Events(ctx context.Context, limit int) []web.Event {
	if b := h.backend(); b != nil {
		return b.Events(ctx, limit)
	}
	return nil
}

// Diagnostics returns diagnostic findings from the active backend.
func (h *WebBackendHolder) Diagnostics(ctx context.Context) []web.Finding {
	if b := h.backend(); b != nil {
		return b.Diagnostics(ctx)
	}
	return nil
}

// CleanDiagnostics removes stale control state through the active backend.
func (h *WebBackendHolder) CleanDiagnostics(ctx context.Context) web.DiagnosticCleanResult {
	if b := h.backend(); b != nil {
		return b.CleanDiagnostics(ctx)
	}
	return web.DiagnosticCleanResult{OK: false, Message: "web backend unavailable"}
}

// Operations returns operation-slot usage from the active backend.
func (h *WebBackendHolder) Operations(ctx context.Context) web.OperationSlots {
	if b := h.backend(); b != nil {
		return b.Operations(ctx)
	}
	return web.OperationSlots{}
}

// ServiceEvents returns a service's recent events from the active backend.
func (h *WebBackendHolder) ServiceEvents(ctx context.Context, name string, limit int) ([]web.Event, bool) {
	if b := h.backend(); b != nil {
		return b.ServiceEvents(ctx, name, limit)
	}
	return nil, false
}

// PruneEvents removes old events from the active event feed (if the backend is available).
func (h *WebBackendHolder) PruneEvents(ctx context.Context, before time.Time) int {
	if b := h.backend(); b != nil {
		return b.PruneEvents(ctx, before)
	}
	return 0
}

// Operate runs a start/stop/restart/reload/resume action through the active backend.
func (h *WebBackendHolder) Operate(ctx context.Context, name, action string, opts web.OperateOpts) web.ActionResult {
	if b := h.backend(); b != nil {
		return b.Operate(ctx, name, action, opts)
	}
	return web.ActionResult{OK: false, Message: "web backend unavailable"}
}

// CompactState prunes old persisted history through the active backend.
func (h *WebBackendHolder) CompactState(ctx context.Context, before time.Time) web.StateCompactResult {
	if b := h.backend(); b != nil {
		return b.CompactState(ctx, before)
	}
	return web.StateCompactResult{OK: false, Message: "web backend unavailable"}
}

// Preflight runs a service's preflight checks through the active backend.
func (h *WebBackendHolder) Preflight(ctx context.Context, name string) (web.PreflightResult, bool) {
	if b := h.backend(); b != nil {
		return b.Preflight(ctx, name)
	}
	return web.PreflightResult{}, false
}

// SetMonitored toggles a service's monitoring through the active backend.
func (h *WebBackendHolder) SetMonitored(ctx context.Context, name string, monitored bool) error {
	if b := h.backend(); b != nil {
		return b.SetMonitored(ctx, name, monitored)
	}
	return nil
}

// SetWatchMonitored toggles a host watch's monitoring through the active backend.
func (h *WebBackendHolder) SetWatchMonitored(ctx context.Context, name string, monitored bool) error {
	if b := h.backend(); b != nil {
		return b.SetWatchMonitored(ctx, name, monitored)
	}
	return nil
}

// ExpandWatch runs a configured storage-watch expansion through the active backend.
func (h *WebBackendHolder) ExpandWatch(ctx context.Context, name string) web.ActionResult {
	if b := h.backend(); b != nil {
		return b.ExpandWatch(ctx, name)
	}
	return web.ActionResult{OK: false, Message: "web backend unavailable"}
}
