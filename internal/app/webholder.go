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
	initialWebBackendGeneration = 1
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

// MaxOperationTimeout reports the longest current action timeout, including
// per-service stop policies and watch probe budgets, from the active reloadable
// backend.
func (h *WebBackendHolder) MaxOperationTimeout() time.Duration {
	b, _ := h.backendAndGeneration()
	if b == nil {
		return 0
	}
	return b.maxOperationTimeout()
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
