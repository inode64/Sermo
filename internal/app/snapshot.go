package app

import (
	"maps"
	"slices"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// CheckSnapshot is the last observed result of one check, for the web detail view.
type CheckSnapshot struct {
	OK        bool
	Condition bool
	Optional  bool
	Skipped   bool
	Message   string
	Data      map[string]any
	Ran       bool // true when the check actually executed this cycle (not interval cache)
	At        time.Time
}

func (c CheckSnapshot) healthy() bool {
	if c.Condition {
		return !c.OK
	}
	return c.OK
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

// Publish replaces a service's snapshot with the given cycle's check cache. ran
// lists the checks that actually executed this cycle (from the worker's cycleRan
// map); interval-deferred checks keep their cached result with Ran false.
func (s *Snapshots) Publish(service string, cache map[string]checks.Result, ran map[string]bool) {
	if s == nil {
		return
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	at := now()
	s.mu.Lock()
	prior := s.byService[service]
	m := make(map[string]CheckSnapshot, len(cache))
	for name, r := range cache {
		cs := CheckSnapshot{
			OK: r.OK, Condition: r.Condition, Optional: r.Optional, Skipped: r.Skipped, Message: r.Message,
			Data: r.Data, Ran: ran[name],
		}
		if ran[name] {
			cs.At = at
		} else if prev, ok := prior[name]; ok && !prev.At.IsZero() {
			cs.At = prev.At
		}
		m[name] = cs
	}
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

type watchResultSnapshot struct {
	checkType string
	result    CheckSnapshot
}

// WatchSnapshots holds each host watch's latest daemon-cycle check result. The
// web UI reads this registry so /api/watches does not start probes of its own.
type WatchSnapshots struct {
	mu      sync.RWMutex
	now     func() time.Time
	byWatch map[string]map[string]watchResultSnapshot
}

// NewWatchSnapshots returns an empty host-watch result registry.
func NewWatchSnapshots() *WatchSnapshots {
	return &WatchSnapshots{now: time.Now, byWatch: map[string]map[string]watchResultSnapshot{}}
}

// Publish records one daemon-cycle result for a watch. Multi-metric watches
// share a visible watch name, so each metric gets its own slot under that name.
func (s *WatchSnapshots) Publish(watch, checkType string, r checks.Result) {
	if s == nil {
		return
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	slot := watchResultSlot(r)
	snap := CheckSnapshot{
		OK: r.OK, Condition: r.Condition, Optional: r.Optional, Skipped: r.Skipped, Message: r.Message,
		Data: maps.Clone(r.Data), Ran: true, At: now(),
	}
	s.mu.Lock()
	if s.byWatch[watch] == nil {
		s.byWatch[watch] = map[string]watchResultSnapshot{}
	}
	s.byWatch[watch][slot] = watchResultSnapshot{checkType: checkType, result: snap}
	s.mu.Unlock()
}

// Get returns the latest result snapshots for a watch and check type, sorted by
// stable slot. Results from a previous config generation with the same watch
// name but a different check type are ignored.
func (s *WatchSnapshots) Get(watch, checkType string) []CheckSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	slots := s.byWatch[watch]
	if len(slots) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(slots))
	out := make([]CheckSnapshot, 0, len(keys))
	for _, key := range keys {
		snap := slots[key]
		if snap.checkType != checkType {
			continue
		}
		out = append(out, snap.result)
	}
	return out
}

func watchResultSlot(r checks.Result) string {
	if metric := cfgval.String(r.Data[checks.DataKeyMetric]); metric != "" {
		return checks.DataKeyMetric + ":" + metric
	}
	if r.Check != "" {
		return r.Check
	}
	return checks.DataKeyResult
}
