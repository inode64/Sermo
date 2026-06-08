package app

import (
	"context"
	"sync"

	"sermo/internal/web"
)

const (
	readinessStarting     = "starting"
	readinessReady        = "ready"
	readinessShuttingDown = "shutting_down"
)

// Readiness tracks whether sermod has finished its startup delay and begun
// monitoring. The web /readyz probe reads it.
type Readiness struct {
	mu       sync.RWMutex
	backend  string
	services int
	watches  int
	state    string
}

// NewReadiness returns a checker in the starting state (not ready until MarkReady).
func NewReadiness(backend string, services, watches int) *Readiness {
	return &Readiness{
		backend: backend, services: services, watches: watches,
		state: readinessStarting,
	}
}

// MarkReady records that workers and watches have been started.
func (r *Readiness) MarkReady() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.state = readinessReady
	r.mu.Unlock()
}

// UpdateCounts refreshes the service and watch totals after a config reload.
func (r *Readiness) UpdateCounts(services, watches int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.services = services
	r.watches = watches
	r.mu.Unlock()
}

// MarkShuttingDown records that the daemon is stopping.
func (r *Readiness) MarkShuttingDown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.state = readinessShuttingDown
	r.mu.Unlock()
}

// Report implements web.ReadinessChecker.
func (r *Readiness) Report(context.Context) web.ReadyReport {
	if r == nil {
		return web.ReadyReport{Ready: true, Status: "ok"}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rep := web.ReadyReport{
		Backend:  r.backend,
		Services: r.services,
		Watches:  r.watches,
	}
	switch r.state {
	case readinessReady:
		rep.Ready = true
		rep.Status = "ok"
	case readinessShuttingDown:
		rep.Status = readinessShuttingDown
		rep.Message = "daemon is shutting down"
	default:
		rep.Status = readinessStarting
		rep.Message = "monitoring has not started yet"
	}
	return rep
}
