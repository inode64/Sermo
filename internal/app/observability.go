package app

import (
	"sync"
	"time"
)

// ObservabilityRegistry tracks services whose monitoring data has completed at
// least one normal observed cycle. It is process-local by design: persisted SLA,
// check and metric history remain in the state store, while readiness is about
// the current daemon generation having fresh indicators to show.
type ObservabilityRegistry struct {
	mu      sync.RWMutex
	readyAt map[string]time.Time
}

// NewObservabilityRegistry returns an empty service observability registry.
func NewObservabilityRegistry() *ObservabilityRegistry {
	return &ObservabilityRegistry{readyAt: map[string]time.Time{}}
}

// MarkReady records that service observability is ready at the given time.
func (r *ObservabilityRegistry) MarkReady(service string, at time.Time) {
	if r == nil || service == "" {
		return
	}
	r.mu.Lock()
	if r.readyAt == nil {
		r.readyAt = map[string]time.Time{}
	}
	r.readyAt[service] = at
	r.mu.Unlock()
}

// Clear removes service observability readiness.
func (r *ObservabilityRegistry) Clear(service string) {
	if r == nil || service == "" {
		return
	}
	r.mu.Lock()
	delete(r.readyAt, service)
	r.mu.Unlock()
}

// Ready reports when service observability became ready.
func (r *ObservabilityRegistry) Ready(service string) (time.Time, bool) {
	if r == nil || service == "" {
		return time.Time{}, false
	}
	r.mu.RLock()
	at, ok := r.readyAt[service]
	r.mu.RUnlock()
	return at, ok
}
