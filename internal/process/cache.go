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
