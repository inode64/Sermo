package process

import (
	"testing"
	"time"
)

func TestCachingReaderReusesSnapshotWithinFreshness(t *testing.T) {
	inner := &countingReader{fakeReader: fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1},
		200: {PID: 200, PPID: 1},
	}}}
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

func TestCachingReaderZeroFreshnessAlwaysRebuilds(t *testing.T) {
	inner := &countingReader{fakeReader: fakeReader{ids: map[int]Identity{100: {PID: 100}}}}
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
