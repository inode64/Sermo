package process

import (
	"errors"
	"testing"
	"time"
)

func TestCachingReaderReusesSnapshotWithinFreshness(t *testing.T) {
	inner := &countingReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1},
		200: {PID: 200, PPID: 1},
	}}
	now := time.Unix(0, 0)
	cr := NewCachingReader(inner, 5*time.Second)
	cr.now = func() time.Time { return now }

	// Two discoveries within the window share a single /proc walk.
	snapshotIdentities(cr)
	snapshotIdentities(cr)
	if inner.pidCalls != 1 {
		t.Fatalf("pidCalls = %d; want 1 (snapshot reused within freshness)", inner.pidCalls)
	}

	// Past the window, the snapshot is rebuilt once and reused again.
	now = now.Add(6 * time.Second)
	snapshotIdentities(cr)
	snapshotIdentities(cr)
	if inner.pidCalls != 2 {
		t.Fatalf("pidCalls = %d; want 2 (rebuilt once after freshness)", inner.pidCalls)
	}
}

func TestCachingReaderInvalidateForcesRebuild(t *testing.T) {
	inner := &countingReader{ids: map[int]Identity{100: {PID: 100, PPID: 1}}}
	now := time.Unix(0, 0)
	cr := NewCachingReader(inner, 5*time.Second)
	cr.now = func() time.Time { return now }

	// Prime the cache, then a second read within the window reuses it.
	snapshotIdentities(cr)
	snapshotIdentities(cr)
	if inner.pidCalls != 1 {
		t.Fatalf("pidCalls = %d; want 1 (snapshot reused within freshness)", inner.pidCalls)
	}

	// Invalidate forces the next read to rebuild from live /proc even though the
	// freshness window has not elapsed — the guarantee the reaper depends on.
	cr.Invalidate()
	snapshotIdentities(cr)
	if inner.pidCalls != 2 {
		t.Fatalf("pidCalls = %d; want 2 (rebuilt after Invalidate)", inner.pidCalls)
	}
}

func TestCachingReaderZeroFreshnessAlwaysRebuilds(t *testing.T) {
	inner := &countingReader{ids: map[int]Identity{100: {PID: 100}}}
	cr := NewCachingReader(inner, 0)
	snapshotIdentities(cr)
	snapshotIdentities(cr)
	if inner.pidCalls != 2 {
		t.Fatalf("pidCalls = %d; want 2 (no caching when freshness is 0)", inner.pidCalls)
	}
}

func TestCachingReaderServesReaderInterfaceFromSnapshot(t *testing.T) {
	inner := fakeReader{ids: map[int]Identity{100: {PID: 100, PPID: 1}}}
	cr := NewCachingReader(inner, time.Second)

	// snapshotIdentities takes the SnapshotReader fast path.
	if _, ok := Reader(cr).(SnapshotReader); !ok {
		t.Fatal("CachingReader must implement SnapshotReader")
	}
	pids, err := cr.PIDs()
	if err != nil || len(pids) != 1 || pids[0] != 100 {
		t.Fatalf("PIDs() = %v, %v; want [100], nil", pids, err)
	}
	if id, ok := cr.Identity(100); !ok || id.PID != 100 {
		t.Fatalf("Identity(100) = %+v, %v; want pid 100, true", id, ok)
	}
	if _, ok := cr.Identity(999); ok {
		t.Fatal("Identity(999) = true; want false for unknown pid")
	}
}

func TestCachingReaderSnapshotReturnsProcError(t *testing.T) {
	want := errors.New("cannot read proc")
	reader := failingSnapshotReader{err: want}
	cached := NewCachingReader(reader, time.Second)
	_, err := Snapshot(cached)
	if !errors.Is(err, want) {
		t.Fatalf("Snapshot() error = %v, want %v", err, want)
	}
	if _, ok := cached.CachedSnapshot(); ok {
		t.Fatal("an incomplete snapshot was served as cached")
	}
}

func TestSnapshotIdentitiesUsesErrorAwareSnapshot(t *testing.T) {
	reader := &errorAwareSnapshotReader{
		snapshot: map[int]Identity{2: {PID: 2}},
		err:      errors.New("cannot list proc"),
	}
	got := snapshotIdentities(reader)
	if len(got) != 1 || got[2].PID != 2 {
		t.Fatalf("snapshotIdentities() = %v, want error-aware snapshot", got)
	}
	if reader.errorCalls != 1 || reader.snapshotCalls != 0 {
		t.Fatalf("snapshot calls = error-aware:%d plain:%d, want 1:0", reader.errorCalls, reader.snapshotCalls)
	}
}

type failingSnapshotReader struct {
	err error
}

func (r failingSnapshotReader) PIDs() ([]int, error) {
	return nil, r.err
}

func (failingSnapshotReader) Identity(int) (Identity, bool) {
	return Identity{}, false
}

type errorAwareSnapshotReader struct {
	snapshot      map[int]Identity
	err           error
	errorCalls    int
	snapshotCalls int
}

func (r *errorAwareSnapshotReader) PIDs() ([]int, error) {
	return nil, errors.New("PIDs must not be called")
}

func (r *errorAwareSnapshotReader) Identity(int) (Identity, bool) {
	return Identity{}, false
}

func (r *errorAwareSnapshotReader) Snapshot() map[int]Identity {
	r.snapshotCalls++
	return nil
}

func (r *errorAwareSnapshotReader) SnapshotWithError() (map[int]Identity, error) {
	r.errorCalls++
	return r.snapshot, r.err
}

func TestCachedPIDsSharesOneQueryWithinFreshness(t *testing.T) {
	queries := 0
	now := time.Unix(0, 0)
	c := NewCachedPIDs(func() ([]int, error) { queries++; return []int{10, 11}, nil }, 5*time.Second)
	c.now = func() time.Time { return now }

	first := c.PIDs()
	first[0] = 99 // callers own their copy
	if got := c.PIDs(); queries != 1 || got[0] != 10 {
		t.Fatalf("queries=%d pids=%v; want one query and an unaltered cached answer", queries, got)
	}
	now = now.Add(6 * time.Second)
	c.PIDs()
	if queries != 2 {
		t.Fatalf("queries=%d; want a new query after freshness", queries)
	}
	c.Fresh()
	if queries != 3 {
		t.Fatalf("queries=%d; Fresh must always query", queries)
	}
	c.PIDs()
	if queries != 3 {
		t.Fatalf("queries=%d; Fresh must refresh the cached answer", queries)
	}
}

// Checked keeps the cached query's error for the callers that read an empty
// answer as "no process"; PIDs serves discovery, which does not need it.
func TestCachedPIDsCheckedKeepsTheQueryError(t *testing.T) {
	queryErr := errors.New("systemctl show MainPID: timeout")
	queries := 0
	c := NewCachedPIDs(func() ([]int, error) { queries++; return []int{10}, queryErr }, time.Minute)
	if pids := c.PIDs(); len(pids) != 1 || pids[0] != 10 {
		t.Fatalf("PIDs = %v, want the partial answer", pids)
	}
	if pids, err := c.Checked(); !errors.Is(err, queryErr) || len(pids) != 1 || queries != 1 {
		t.Fatalf("Checked = %v, %v after %d queries, want the cached answer and error", pids, err, queries)
	}
}

func TestCachedPIDsZeroFreshnessAlwaysQueries(t *testing.T) {
	queries := 0
	c := NewCachedPIDs(func() ([]int, error) { queries++; return nil, nil }, 0)
	c.PIDs()
	c.PIDs()
	if queries != 2 {
		t.Fatalf("queries=%d; want every call to query with caching disabled", queries)
	}
}

func TestCachingReaderCachedSnapshotNeverRebuilds(t *testing.T) {
	inner := &countingReader{ids: map[int]Identity{100: {PID: 100, PPID: 1, State: ProcStateZombie}}}
	now := time.Unix(0, 0)
	cr := NewCachingReader(inner, 5*time.Second)
	cr.now = func() time.Time { return now }

	if _, ok := cr.CachedSnapshot(); ok || inner.pidCalls != 0 {
		t.Fatalf("unprimed cache served a snapshot or walked /proc (walks=%d)", inner.pidCalls)
	}
	cr.Snapshot()
	if snapshot, ok := cr.CachedSnapshot(); !ok || snapshot[100].State != ProcStateZombie || inner.pidCalls != 1 {
		t.Fatalf("fresh snapshot = %v, %v (walks=%d), want the primed one", snapshot, ok, inner.pidCalls)
	}
	now = now.Add(5 * time.Second)
	if _, ok := cr.CachedSnapshot(); ok || inner.pidCalls != 1 {
		t.Fatalf("expired snapshot served or rebuilt (walks=%d)", inner.pidCalls)
	}
}
