package checks

import (
	"testing"

	"sermo/internal/process"
)

type cachedSnapshotFake struct {
	snapshot map[int]process.Identity
	fresh    bool
}

func (f cachedSnapshotFake) CachedSnapshot() (map[int]process.Identity, bool) {
	return f.snapshot, f.fresh
}

func TestZombieSamplerCountsTheSharedSnapshotWhileFresh(t *testing.T) {
	scans := 0
	scan := func() (uint64, bool) { scans++; return 7, true }
	snapshot := map[int]process.Identity{
		10: {PID: 10, State: process.ProcStateZombie},
		11: {PID: 11, State: "S"},
		12: {PID: 12, State: process.ProcStateZombie},
	}
	if got, ok := newZombieSampler(cachedSnapshotFake{snapshot: snapshot, fresh: true}, scan)(); !ok || got != 2 || scans != 0 {
		t.Fatalf("fresh snapshot: count=%d ok=%v scans=%d, want 2 from the snapshot", got, ok, scans)
	}
	if got, ok := newZombieSampler(cachedSnapshotFake{snapshot: snapshot}, scan)(); !ok || got != 7 || scans != 1 {
		t.Fatalf("stale snapshot: count=%d ok=%v scans=%d, want its own scan", got, ok, scans)
	}
}
