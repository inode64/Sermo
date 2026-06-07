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

func (h *WebBackendHolder) Services(ctx context.Context) []web.Service {
	if b := h.backend(); b != nil {
		return b.Services(ctx)
	}
	return nil
}

func (h *WebBackendHolder) Watches(ctx context.Context) []web.Watch {
	if b := h.backend(); b != nil {
		return b.Watches(ctx)
	}
	return nil
}

func (h *WebBackendHolder) Notifiers(ctx context.Context) []web.Notifier {
	if b := h.backend(); b != nil {
		return b.Notifiers(ctx)
	}
	return nil
}

func (h *WebBackendHolder) DaemonInfo(ctx context.Context) web.DaemonInfo {
	if b := h.backend(); b != nil {
		return b.DaemonInfo(ctx)
	}
	return web.DaemonInfo{}
}

func (h *WebBackendHolder) HostMetrics(ctx context.Context) []web.HostMetric {
	if b := h.backend(); b != nil {
		return b.HostMetrics(ctx)
	}
	return nil
}

func (h *WebBackendHolder) Locks(ctx context.Context) []web.Lock {
	if b := h.backend(); b != nil {
		return b.Locks(ctx)
	}
	return nil
}

func (h *WebBackendHolder) Detail(ctx context.Context, name string) (web.Detail, bool) {
	if b := h.backend(); b != nil {
		return b.Detail(ctx, name)
	}
	return web.Detail{}, false
}

func (h *WebBackendHolder) Series(ctx context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	if b := h.backend(); b != nil {
		return b.Series(ctx, name, since)
	}
	return nil, false
}

func (h *WebBackendHolder) Metrics(ctx context.Context, name, check string, since time.Duration) (web.MetricSeries, bool) {
	if b := h.backend(); b != nil {
		return b.Metrics(ctx, name, check, since)
	}
	return web.MetricSeries{}, false
}

func (h *WebBackendHolder) Events(ctx context.Context, limit int) []web.Event {
	if b := h.backend(); b != nil {
		return b.Events(ctx, limit)
	}
	return nil
}

func (h *WebBackendHolder) Diagnostics(ctx context.Context) []web.Finding {
	if b := h.backend(); b != nil {
		return b.Diagnostics(ctx)
	}
	return nil
}

func (h *WebBackendHolder) Operations(ctx context.Context) web.OperationSlots {
	if b := h.backend(); b != nil {
		return b.Operations(ctx)
	}
	return web.OperationSlots{}
}

func (h *WebBackendHolder) ServiceEvents(ctx context.Context, name string, limit int) ([]web.Event, bool) {
	if b := h.backend(); b != nil {
		return b.ServiceEvents(ctx, name, limit)
	}
	return nil, false
}

func (h *WebBackendHolder) Operate(ctx context.Context, name, action string) web.ActionResult {
	if b := h.backend(); b != nil {
		return b.Operate(ctx, name, action)
	}
	return web.ActionResult{OK: false, Message: "web backend unavailable"}
}

func (h *WebBackendHolder) Preflight(ctx context.Context, name string) (web.PreflightResult, bool) {
	if b := h.backend(); b != nil {
		return b.Preflight(ctx, name)
	}
	return web.PreflightResult{}, false
}

func (h *WebBackendHolder) SetMonitored(ctx context.Context, name string, monitored bool) error {
	if b := h.backend(); b != nil {
		return b.SetMonitored(ctx, name, monitored)
	}
	return nil
}