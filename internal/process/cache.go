package process

import (
	"sync"
	"time"
)

// SnapshotReader can return a whole-/proc identity snapshot in one call. The
// shared CachingReader implements it so discovery reuses a single /proc walk
// instead of issuing PIDs() + Identity() round-trips itself.
type SnapshotReader interface {
	Snapshot() map[int]Identity
}

// CachingReader wraps a Reader and serves a whole-/proc identity snapshot that
// is rebuilt at most once per freshness window. Many service discoveries (and
// web runtime queries) running within the same window then share one /proc
// walk instead of each scanning every PID — turning the per-cycle cost from
// O(services × processes) into O(processes). Safe for concurrent use.
//
// A freshness of 0 disables caching (every call rebuilds), so behaviour matches
// a bare reader. The cached map is replaced wholesale on rebuild and never
// mutated, so concurrent readers of a given snapshot are safe.
type CachingReader struct {
	inner Reader
	now   func() time.Time

	mu        sync.Mutex
	freshness time.Duration
	snap      map[int]Identity
	at        time.Time
	primed    bool
}

// NewCachingReader returns a CachingReader over inner (defaulting to the host
// OSReader) that reuses a snapshot for up to freshness.
func NewCachingReader(inner Reader, freshness time.Duration) *CachingReader {
	if inner == nil {
		inner = OSReader{}
	}
	return &CachingReader{inner: inner, freshness: freshness, now: time.Now}
}

// SetFreshness updates the reuse window (e.g. after a config reload changes the
// scheduler interval). Safe to call concurrently.
func (c *CachingReader) SetFreshness(d time.Duration) {
	c.mu.Lock()
	c.freshness = d
	c.mu.Unlock()
}

// Snapshot returns the cached identity map, rebuilding it when stale. The
// returned map is shared and must not be mutated by callers (discovery only
// reads it).
func (c *CachingReader) Snapshot() map[int]Identity {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.primed && c.freshness > 0 && now.Sub(c.at) < c.freshness {
		return c.snap
	}
	c.snap = buildSnapshot(c.inner)
	c.at = now
	c.primed = true
	return c.snap
}

// PIDs satisfies Reader by serving the cached snapshot's process IDs.
func (c *CachingReader) PIDs() ([]int, error) {
	snap := c.Snapshot()
	pids := make([]int, 0, len(snap))
	for pid := range snap {
		pids = append(pids, pid)
	}
	return pids, nil
}

// Identity satisfies Reader by serving one process from the cached snapshot.
func (c *CachingReader) Identity(pid int) (Identity, bool) {
	id, ok := c.Snapshot()[pid]
	return id, ok
}
