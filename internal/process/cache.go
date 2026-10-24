package process

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// SnapshotReader can return a whole-/proc identity snapshot in one call.
type SnapshotReader interface {
	Snapshot() map[int]Identity
}

// SnapshotErrorReader returns a snapshot and the error that prevented its
// refresh. It lets read-only safety checks distinguish an empty process table
// from an unreadable one.
type SnapshotErrorReader interface {
	SnapshotWithError() (map[int]Identity, error)
}

// CachingReader shares one /proc identity snapshot per freshness window, turning
// per-cycle discovery from O(services x processes) into O(processes). Safe for
// concurrent use.
//
// A freshness of 0 disables caching (every call rebuilds), so behaviour matches
// a bare reader. The cached map is replaced wholesale on rebuild and never
// mutated, so concurrent readers of a given snapshot are safe.
type CachingReader struct {
	inner Reader
	now   func() time.Time

	mu        sync.Mutex
	freshness time.Duration
	idx       *snapshotIndex
	err       error
	at        time.Time
	primed    bool
}

// NewCachingReader returns a CachingReader over inner. nil uses OSReader.
func NewCachingReader(inner Reader, freshness time.Duration) *CachingReader {
	if inner == nil {
		inner = OSReader{}
	}
	return &CachingReader{inner: inner, freshness: freshness, now: time.Now}
}

// Invalidate drops the cached snapshot so the next Snapshot rebuilds from live
// /proc. Safety-critical callers that must never act on a stale process table —
// notably residual discovery and the kill-escalation reaper, which would
// otherwise SIGKILL a PID that has already exited (and may have been reused) —
// call this before each read. Safe to call concurrently.
func (c *CachingReader) Invalidate() {
	c.mu.Lock()
	c.primed = false
	c.mu.Unlock()
}

// Snapshot returns the cached identity map, rebuilding it when stale. The
// returned map is shared and must not be mutated by callers (discovery only
// reads it).
func (c *CachingReader) Snapshot() map[int]Identity {
	return c.snapshotIndex().byPID
}

// SnapshotWithError returns the cached snapshot and any failure from the procfs
// walk that produced it. Callers must treat a non-nil error as incomplete data.
func (c *CachingReader) SnapshotWithError() (map[int]Identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.snapshotIndexLocked()
	return idx.byPID, c.err
}

// CachedSnapshot returns the shared snapshot only while it is fresh and
// complete, without rebuilding it. A caller that can read what it needs more
// cheaply than a full identity walk reuses another consumer's walk this way and
// falls back to its own read.
func (c *CachingReader) CachedSnapshot() (map[int]Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.primed || c.err != nil || c.freshness <= 0 || c.now().Sub(c.at) >= c.freshness {
		return nil, false
	}
	return c.idx.byPID, true
}

// snapshotIndex serves the pre-derived snapshot index (sorted PIDs, children
// map) built once per refresh, so per-service discovery does not rebuild it.
func (c *CachingReader) snapshotIndex() *snapshotIndex {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotIndexLocked()
}

func (c *CachingReader) snapshotIndexLocked() *snapshotIndex {
	now := c.now()
	if c.primed && c.freshness > 0 && now.Sub(c.at) < c.freshness {
		return c.idx
	}
	snapshot, err := readSnapshot(c.inner)
	c.idx = buildSnapshotIndex(snapshot)
	c.err = err
	c.at = now
	c.primed = true
	return c.idx
}

// PIDs satisfies Reader by serving the cached snapshot's process IDs, reusing
// the index's already-sorted order.
func (c *CachingReader) PIDs() ([]int, error) {
	return slices.Clone(c.snapshotIndex().sorted), nil
}

// Identity satisfies Reader by serving one process from the cached snapshot.
func (c *CachingReader) Identity(pid int) (Identity, bool) {
	id, ok := c.Snapshot()[pid]
	return id, ok
}

// Snapshot reads one whole process snapshot, retaining a procfs-read failure so
// callers that use the result as an operation guard can fail closed.
func Snapshot(reader Reader) (map[int]Identity, error) {
	if sr, ok := reader.(SnapshotErrorReader); ok {
		snapshot, err := sr.SnapshotWithError()
		if err != nil {
			return snapshot, fmt.Errorf("read snapshot: %w", err)
		}
		return snapshot, nil
	}
	if sr, ok := reader.(SnapshotReader); ok {
		return sr.Snapshot(), nil
	}
	return readSnapshot(reader)
}

// CachedPIDs shares one backend PID query among a unit's discoverers for a
// freshness window, the way CachingReader shares the /proc walk: otherwise the
// worker, the web runtime and every metric watch of one service each run the
// init system's query on every discovery. Fresh always queries and refreshes
// the cache; Discoverer.FreshBackendPIDs uses it for the observations an
// operation acts on. A freshness of 0 disables caching. Safe for concurrent
// use; the query runs under the lock, so concurrent callers share one run.
type CachedPIDs struct {
	query     func() ([]int, error)
	freshness time.Duration
	now       func() time.Time

	mu     sync.Mutex
	pids   []int
	err    error
	at     time.Time
	primed bool
}

// NewCachedPIDs returns a CachedPIDs over query.
func NewCachedPIDs(query func() ([]int, error), freshness time.Duration) *CachedPIDs {
	return &CachedPIDs{query: query, freshness: freshness, now: time.Now}
}

// PIDs returns the cached answer, querying again once it is freshness old.
func (c *CachedPIDs) PIDs() []int {
	pids, _ := c.Checked()
	return pids
}

// Checked is PIDs with the query's error, for a caller that would read an
// empty answer as "no process".
func (c *CachedPIDs) Checked() ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.primed && c.freshness > 0 && c.now().Sub(c.at) < c.freshness {
		return slices.Clone(c.pids), c.err
	}
	return c.refreshLocked()
}

// Fresh queries the backend now, refreshes the cached answer and returns it
// with the query's error.
func (c *CachedPIDs) Fresh() ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshLocked()
}

func (c *CachedPIDs) refreshLocked() ([]int, error) {
	c.at = c.now()
	c.pids, c.err = c.query()
	c.primed = true
	return slices.Clone(c.pids), c.err
}
