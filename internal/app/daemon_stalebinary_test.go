package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// mapProcReader serves a fixed process table.
type mapProcReader map[int]process.Identity

func (r mapProcReader) PIDs() ([]int, error) {
	pids := make([]int, 0, len(r))
	for pid := range r {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r mapProcReader) Identity(pid int) (process.Identity, bool) {
	id, ok := r[pid]
	return id, ok
}

// The daemon's cycle builds its own stale-binary source over the cycle's
// discovery; it must mark an uninstalled binary as the service wiring does, or
// the running daemon keeps advising a restart that cannot start anything.
func TestWorkerStaleBinariesMarkAnUninstalledBinary(t *testing.T) {
	dir := t.TempDir()
	upgraded := filepath.Join(dir, "upgraded")
	if err := os.WriteFile(upgraded, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(dir, "removed")
	deleted := func(pid int, prev string) process.Identity {
		return process.Identity{PID: pid, PPID: 1, UID: 0, User: "root", ExePrev: prev, StartTicks: uint64(pid), StartTicksOK: true,
			Cgroup: "0::/system.slice/svc.service"}
	}
	// 101 is a worker of the principal 100, so it is the service's own process
	// rather than a stray of the unit.
	worker := deleted(101, upgraded)
	worker.PPID = 100
	reader := mapProcReader{100: deleted(100, removed), 101: worker}
	w, _, warnings := buildWorker(t.Context(), "svc", "svc.service", map[string]any{}, Deps{
		Backend: servicemgr.BackendSystemd, Manager: fakeManager{},
		Runtime: t.TempDir(), DefaultTimeout: time.Second,
		ProcReader: reader, ExecxRunner: &execxtest.Runner{},
		BackendPIDs: func() []int { return []int{100, 101} },
	}, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	got := map[int]bool{}
	for _, s := range w.CheckDeps.StaleBinaries() {
		got[s.PID] = s.Removed
	}
	if removed, seen := got[100]; !seen || !removed {
		t.Fatalf("stale = %v, want pid 100 marked removed", got)
	}
	if removed, seen := got[101]; !seen || removed {
		t.Fatalf("stale = %v, want pid 101 replaced, not removed", got)
	}
}
