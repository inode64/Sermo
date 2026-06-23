package app

import "sync"

// Settling tracks which monitored targets have completed their startup
// observation cycle (backend active plus a first check for services, or a
// first check for watches/apps). While unsettled, targets report state
// "starting" and must not drive alerts, hooks or remediation.
type Settling struct {
	mu       sync.RWMutex
	observed map[string]struct{}
	ready    *Readiness
}

// NewSettling returns an empty settling registry. When ready is non-nil,
// MarkObserved also advances the daemon readiness first-cycle gate.
func NewSettling(ready *Readiness) *Settling {
	return &Settling{observed: map[string]struct{}{}, ready: ready}
}

// Reset arms the named targets as unsettled for a new scheduler generation.
func (s *Settling) Reset(names []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.observed = make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			s.observed[name] = struct{}{}
		}
	}
	s.mu.Unlock()
}

// MarkObserved records that name has finished its startup observation cycle.
func (s *Settling) MarkObserved(name string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	if _, pending := s.observed[name]; !pending {
		s.mu.Unlock()
		return
	}
	delete(s.observed, name)
	s.mu.Unlock()
	if s.ready != nil {
		s.ready.markFirstCycle()
	}
}

// Observed reports whether name has completed its startup observation cycle.
func (s *Settling) Observed(name string) bool {
	if s == nil || name == "" {
		return true
	}
	s.mu.RLock()
	_, pending := s.observed[name]
	s.mu.RUnlock()
	return !pending
}

// MarkObservedBulk marks several targets observed without advancing readiness.
// Used on config reload to preserve settled state for targets that already
// cycled in a prior generation.
func (s *Settling) MarkObservedBulk(names []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, name := range names {
		if name != "" {
			delete(s.observed, name)
		}
	}
	s.mu.Unlock()
}
