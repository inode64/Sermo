package app

import (
	"context"
	"fmt"
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
	// firstTotal/firstRemaining gate the transition to ready on the first boot:
	// the daemon stays "starting" until every monitored target has completed its
	// first cycle (and so has data), not merely been launched. firstRemaining
	// counts targets that have not yet reported.
	firstTotal     int
	firstRemaining int
	// panic reports the daemon-wide panic mode (optional). When set and active,
	// it overrides the "ok" status with "panic mode".
	panic func() bool
}

// NewReadiness returns a checker in the starting state (not ready until MarkReady).
func NewReadiness(backend string, services, watches int) *Readiness {
	return &Readiness{
		backend: backend, services: services, watches: watches,
		state: readinessStarting,
	}
}

// WatchPanic wires the daemon-wide panic-mode source so the readiness report
// surfaces it as the daemon status.
func (r *Readiness) WatchPanic(active func() bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.panic = active
	r.mu.Unlock()
}

// MarkReady records that monitoring is up. It only advances from starting to
// ready, so a late first-cycle signal can never undo a shutting_down state.
func (r *Readiness) MarkReady() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.state == readinessStarting {
		r.state = readinessReady
	}
	r.mu.Unlock()
}

// ExpectFirstCycles arms the first-cycle gate for n monitored targets: the
// daemon stays "starting" until markFirstCycle has fired n times. n<=0 means
// there is nothing to wait for, so it becomes ready immediately.
func (r *Readiness) ExpectFirstCycles(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.firstTotal = n
	r.firstRemaining = n
	if n <= 0 && r.state == readinessStarting {
		r.state = readinessReady
	}
	r.mu.Unlock()
}

// markFirstCycle records that one target has completed its first cycle. When the
// last one reports, the daemon transitions from starting to ready.
func (r *Readiness) markFirstCycle() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.firstRemaining > 0 {
		r.firstRemaining--
		if r.firstRemaining == 0 && r.state == readinessStarting {
			r.state = readinessReady
		}
	}
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
		// Panic mode overrides the healthy status (but not starting/shutting_down,
		// which describe the lifecycle): the daemon is up but holding back hooks,
		// alerts and remediation.
		if r.panic != nil && r.panic() {
			rep.Panic = true
			rep.Status = "panic mode"
			rep.Message = "panic mode: hooks, alerts and automatic remediation are suspended"
		}
	case readinessShuttingDown:
		rep.Status = readinessShuttingDown
		rep.Message = "daemon is shutting down"
	default:
		rep.Status = readinessStarting
		if r.firstTotal > 0 {
			rep.Message = fmt.Sprintf("starting: %d/%d monitored targets have reported", r.firstTotal-r.firstRemaining, r.firstTotal)
		} else {
			rep.Message = "monitoring has not started yet"
		}
	}
	return rep
}
