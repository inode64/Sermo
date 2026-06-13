package app

import (
	"sync"
	"time"
)

// ServiceLive is a service's most recent live CPU readings, published per cycle
// by its worker and read by the web detail view. CPU is the whole-machine rate
// (% of all cores); CPUThread is the busiest single process against one core
// (100% = one saturated core); PerProcCPU maps each PID to its single-core rate.
// The *Ready flags are false until two samples exist (the first cycle has no
// delta to rate against).
type ServiceLive struct {
	CPU            float64
	CPUReady       bool
	CPUThread      float64
	CPUThreadReady bool
	NumCPU         int
	PerProcCPU     map[int]float64
	At             time.Time
}

// LiveMetrics holds each service's latest live CPU sample so the web UI can show
// per-process and aggregate CPU without re-sampling /proc (which would corrupt
// the engine's rate deltas). Workers publish after every cycle; the web reads.
// Safe for concurrent use, mirroring Snapshots.
type LiveMetrics struct {
	mu        sync.RWMutex
	now       func() time.Time
	byService map[string]ServiceLive
}

// NewLiveMetrics returns an empty registry.
func NewLiveMetrics() *LiveMetrics {
	return &LiveMetrics{now: time.Now, byService: map[string]ServiceLive{}}
}

// Publish replaces a service's live CPU sample. A nil registry is a no-op so
// callers need not nil-check.
func (l *LiveMetrics) Publish(service string, sl ServiceLive) {
	if l == nil {
		return
	}
	now := l.now
	if now == nil {
		now = time.Now
	}
	sl.At = now()
	l.mu.Lock()
	l.byService[service] = sl
	l.mu.Unlock()
}

// Get returns a service's last live CPU sample, and false if none exists yet.
func (l *LiveMetrics) Get(service string) (ServiceLive, bool) {
	if l == nil {
		return ServiceLive{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	sl, ok := l.byService[service]
	return sl, ok
}
