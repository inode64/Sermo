package app

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/state"
)

// CheckSnapshot is the last observed result of one check, for the web detail view.
type CheckSnapshot struct {
	CheckType string
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
	mu          sync.RWMutex
	now         func() time.Time
	byService   map[string]map[string]CheckSnapshot
	store       serviceSnapshotStore
	reportError func(error)
}

// NewSnapshots returns an empty registry.
func NewSnapshots() *Snapshots {
	return &Snapshots{now: time.Now, byService: map[string]map[string]CheckSnapshot{}}
}

type serviceSnapshotStore interface {
	ServiceCheckSnapshots() (map[string]map[string]state.CheckSnapshotRecord, error)
	SetServiceCheckSnapshots(service string, records map[string]state.CheckSnapshotRecord) error
}

// NewPersistentSnapshots returns a registry hydrated from store and persists
// future publishes back to it.
func NewPersistentSnapshots(store serviceSnapshotStore, reportError func(error)) (*Snapshots, error) {
	s := NewSnapshots()
	s.store = store
	s.reportError = reportError
	if store == nil {
		return s, nil
	}
	records, err := store.ServiceCheckSnapshots()
	if err != nil {
		return s, fmt.Errorf("load service check snapshots: %w", err)
	}
	for service, checkRecords := range records {
		s.byService[service] = serviceSnapshotsFromRecords(checkRecords)
	}
	return s, nil
}

// Publish replaces a service's snapshot with the given cycle's check cache. ran
// lists the checks that actually executed this cycle (from the worker's cycleRan
// map); interval-deferred checks keep their cached result with Ran false.
func (s *Snapshots) Publish(service string, cache map[string]checks.Result, ran map[string]bool) {
	s.PublishWithCheckTypes(service, cache, ran, nil)
}

// PublishWithCheckTypes replaces a service snapshot with the given cycle's
// cache and check types. Type metadata prevents a same-named check from an old
// configuration from being decoded under a newly configured check type.
func (s *Snapshots) PublishWithCheckTypes(service string, cache map[string]checks.Result, ran map[string]bool, checkTypes map[string]string) {
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
			CheckType: checkTypes[name],
			OK:        r.OK, Condition: r.Condition, Optional: r.Optional, Skipped: r.Skipped, Message: r.Message,
			Data: maps.Clone(r.Data), Ran: ran[name],
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
	if s.store != nil {
		if err := s.store.SetServiceCheckSnapshots(service, serviceSnapshotRecords(m)); err != nil {
			s.reportStoreError(err)
		}
	}
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
	mu          sync.RWMutex
	now         func() time.Time
	byWatch     map[string]map[string]watchResultSnapshot
	store       watchSnapshotStore
	reportError func(error)
}

// NewWatchSnapshots returns an empty host-watch result registry.
func NewWatchSnapshots() *WatchSnapshots {
	return &WatchSnapshots{now: time.Now, byWatch: map[string]map[string]watchResultSnapshot{}}
}

type watchSnapshotStore interface {
	WatchCheckSnapshots() (map[string]map[string]state.CheckSnapshotRecord, error)
	SetWatchCheckSnapshot(watch, slot string, rec state.CheckSnapshotRecord) error
}

// NewPersistentWatchSnapshots returns a host-watch registry hydrated from store
// and persists future publishes back to it.
func NewPersistentWatchSnapshots(store watchSnapshotStore, reportError func(error)) (*WatchSnapshots, error) {
	s := NewWatchSnapshots()
	s.store = store
	s.reportError = reportError
	if store == nil {
		return s, nil
	}
	records, err := store.WatchCheckSnapshots()
	if err != nil {
		return s, fmt.Errorf("load watch check snapshots: %w", err)
	}
	for watch, slots := range records {
		if s.byWatch[watch] == nil {
			s.byWatch[watch] = map[string]watchResultSnapshot{}
		}
		for slot, rec := range slots {
			s.byWatch[watch][slot] = watchResultSnapshot{
				checkType: rec.CheckType,
				result:    snapshotFromRecord(rec),
			}
		}
	}
	return s, nil
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
	if s.store != nil {
		rec := snapshotRecord(snap)
		rec.CheckType = checkType
		if err := s.store.SetWatchCheckSnapshot(watch, slot, rec); err != nil {
			s.reportStoreError(err)
		}
	}
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

func serviceSnapshotsFromRecords(records map[string]state.CheckSnapshotRecord) map[string]CheckSnapshot {
	out := make(map[string]CheckSnapshot, len(records))
	for name, rec := range records {
		out[name] = snapshotFromRecord(rec)
	}
	return out
}

func serviceSnapshotRecords(snaps map[string]CheckSnapshot) map[string]state.CheckSnapshotRecord {
	out := make(map[string]state.CheckSnapshotRecord, len(snaps))
	for name, snap := range snaps {
		rec := snapshotRecord(snap)
		rec.Name = name
		out[name] = rec
	}
	return out
}

func snapshotFromRecord(rec state.CheckSnapshotRecord) CheckSnapshot {
	return CheckSnapshot{
		CheckType: rec.CheckType, OK: rec.OK, Condition: rec.Condition, Optional: rec.Optional, Skipped: rec.Skipped,
		Message: rec.Message, Data: maps.Clone(rec.Data), Ran: rec.Ran, At: rec.At,
	}
}

func snapshotRecord(snap CheckSnapshot) state.CheckSnapshotRecord {
	return state.CheckSnapshotRecord{
		CheckType: snap.CheckType, OK: snap.OK, Condition: snap.Condition, Optional: snap.Optional, Skipped: snap.Skipped,
		Message: snap.Message, Data: maps.Clone(snap.Data), Ran: snap.Ran, At: snap.At,
	}
}

func (s *Snapshots) reportStoreError(err error) {
	if s.reportError != nil {
		s.reportError(err)
	}
}

func (s *WatchSnapshots) reportStoreError(err error) {
	if s.reportError != nil {
		s.reportError(err)
	}
}
