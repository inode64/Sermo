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

// DaemonInfo returns daemon and engine info from the active backend.
func (h *WebBackendHolder) DaemonInfo(ctx context.Context) web.DaemonInfo {
	if b := h.backend(); b != nil {
		return b.DaemonInfo(ctx)
	}
	return web.DaemonInfo{}
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

// ConfigRender returns a resolved service config from the active backend.
func (h *WebBackendHolder) ConfigRender(ctx context.Context, name, format string) (web.ConfigRender, bool, error) {
	if b := h.backend(); b != nil {
		return b.ConfigRender(ctx, name, format)
	}
	return web.ConfigRender{}, false, nil
}

// ConfigDiff compares resolved service configs from the active backend.
func (h *WebBackendHolder) ConfigDiff(ctx context.Context, base, service string) (web.ConfigDiff, bool, error) {
	if b := h.backend(); b != nil {
		return b.ConfigDiff(ctx, base, service)
	}
	return web.ConfigDiff{}, false, nil
}

// Series returns a service's SLA series from the active backend.
func (h *WebBackendHolder) Series(ctx context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	if b := h.backend(); b != nil {
		return b.Series(ctx, name, since)
	}
	return nil, false
}

// Metrics returns a check's metric series from the active backend.
func (h *WebBackendHolder) Metrics(ctx context.Context, name, check string, since time.Duration) (web.MetricSeries, bool) {
	if b := h.backend(); b != nil {
		return b.Metrics(ctx, name, check, since)
	}
	return web.MetricSeries{}, false
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

// Operate runs a start/stop/restart action through the active backend.
func (h *WebBackendHolder) Operate(ctx context.Context, name, action string) web.ActionResult {
	if b := h.backend(); b != nil {
		return b.Operate(ctx, name, action)
	}
	return web.ActionResult{OK: false, Message: "web backend unavailable"}
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
