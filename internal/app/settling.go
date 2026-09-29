package app

import "sync"

// SettlingServiceKey namespaces a service worker in the settling registry.
func SettlingServiceKey(name string) string {
	if name == "" {
		return ""
	}
	return "service:" + name
}

// SettlingWatchKey namespaces a host or synthesized service watch.
func SettlingWatchKey(name string) string {
	if name == "" {
		return ""
	}
	return "watch:" + name
}

// SettlingAppKey namespaces an installed-app monitor.
func SettlingAppKey(name string) string {
	if name == "" {
		return ""
	}
	return "app:" + name
}

// settlingKeyForWatch returns the registry key for a runnable watch.
func settlingKeyForWatch(w *Watch) string {
	if w == nil {
		return ""
	}
	if w.App != "" {
		return SettlingAppKey(w.Name)
	}
	return SettlingWatchKey(w.Name)
}

// Settling tracks which monitored targets have completed their startup
// observation cycle (backend active plus a first check for services, or a
// first check for watches/apps). While unsettled, targets report state
// "starting" and must not drive alerts, hooks or remediation.
type Settling struct {
	mu sync.RWMutex
	// pending counts, per target key, the runnable objects that have not yet
	// completed their startup observation cycle. Metric watches (net/icmp/swap)
	// expand to one Watch per metric sharing one key: each metric runs its own
	// observe-only cycle, and the key settles (advancing readiness once) when
	// the last of them has.
	pending map[string]int
	ready   *Readiness
}

// NewSettling returns an empty settling registry. When ready is non-nil,
// MarkObserved also advances the daemon readiness first-cycle gate.
func NewSettling(ready *Readiness) *Settling {
	return &Settling{pending: map[string]int{}, ready: ready}
}

// Reset arms the named targets as unsettled for a new scheduler generation.
// A name listed once per runnable object is pending until each has reported.
func (s *Settling) Reset(names []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.pending = make(map[string]int, len(names))
	for _, name := range names {
		if name != "" {
			s.pending[name]++
		}
	}
	s.mu.Unlock()
}

// Pending returns how many armed targets (distinct keys) have not completed
// their startup observation cycle yet.
func (s *Settling) Pending() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pending)
}

// MarkObserved records that one object of name has finished its startup
// observation cycle; name settles when the last of its objects has.
func (s *Settling) MarkObserved(name string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	remaining, pending := s.pending[name]
	if !pending {
		s.mu.Unlock()
		return
	}
	if remaining > 1 {
		s.pending[name] = remaining - 1
		s.mu.Unlock()
		return
	}
	delete(s.pending, name)
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
	_, pending := s.pending[name]
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
			delete(s.pending, name)
		}
	}
	s.mu.Unlock()
}
