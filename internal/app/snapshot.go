package app

import (
	"sync"
	"time"

	"sermo/internal/checks"
)

// CheckSnapshot is the last observed result of one check, for the web detail view.
type CheckSnapshot struct {
	OK       bool
	Optional bool
	Skipped  bool
	Message  string
	At       time.Time
}

// Snapshots holds each service's most recent check results so the web UI can show
// them without re-running the checks. Workers publish after every cycle; the web
// reads. Safe for concurrent use.
type Snapshots struct {
	mu        sync.RWMutex
	now       func() time.Time
	byService map[string]map[string]CheckSnapshot
}

// NewSnapshots returns an empty registry.
func NewSnapshots() *Snapshots {
	return &Snapshots{now: time.Now, byService: map[string]map[string]CheckSnapshot{}}
}

// Publish replaces a service's snapshot with the given cycle's check cache.
func (s *Snapshots) Publish(service string, cache map[string]checks.Result) {
	if s == nil {
		return
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	at := now()
	m := make(map[string]CheckSnapshot, len(cache))
	for name, r := range cache {
		m[name] = CheckSnapshot{OK: r.OK, Optional: r.Optional, Skipped: r.Skipped, Message: r.Message, At: at}
	}
	s.mu.Lock()
	s.byService[service] = m
	s.mu.Unlock()
}

// Get returns a service's last check snapshot (check name -> result), or nil if it
// has not been observed yet. The returned map is immutable (Publish swaps whole
// maps), so the caller may read it without copying.
func (s *Snapshots) Get(service string) map[string]CheckSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byService[service]
}
