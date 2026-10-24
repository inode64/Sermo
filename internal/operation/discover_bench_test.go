package operation

import (
	"fmt"
	"testing"
	"time"

	"sermo/internal/process"
)

// benchProcessTable is a fixed process table served as a process.Reader.
type benchProcessTable map[int]process.Identity

func (t benchProcessTable) PIDs() ([]int, error) {
	pids := make([]int, 0, len(t))
	for pid := range t {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (t benchProcessTable) Identity(pid int) (process.Identity, bool) {
	id, ok := t[pid]
	return id, ok
}

// BenchmarkDiscoverStoppedService measures the worst case of the global
// selector fallback: a stopped systemd service (no backend PID, no pidfile)
// whose exact exe/user selector matches nothing on a host of 2000 processes
// that all live in other units, with the unit's cgroup ownership wired as the
// monitor wires it. The cycle's shared snapshot is cached, as in production.
func BenchmarkDiscoverStoppedService(b *testing.B) {
	const procs = 2000
	table := make(benchProcessTable, procs)
	for pid := 2; pid < procs+2; pid++ {
		exe := fmt.Sprintf("/usr/bin/worker%d", pid%40)
		table[pid] = process.Identity{
			PID: pid, PPID: 1, StartTicks: uint64(pid), UID: 1000, User: "app",
			Exe: exe, ExeOK: true, Cmdline: []string{exe},
			Cgroup: fmt.Sprintf("0::/system.slice/worker%d.service", pid%40),
		}
	}
	selectors, _ := process.ParseSelectors(map[string]any{
		process.SectionProcesses: map[string]any{
			"main": map[string]any{process.SelectorKeyExe: "/usr/sbin/mysqld", process.SelectorKeyUser: "mysql"},
		},
	})
	d := process.Discoverer{
		Reader:           process.NewCachingReader(table, time.Hour),
		ResolveUser:      func(string) (uint32, bool) { return 27, true },
		ProcessOwnership: UnitProcessOwnership("mysqld"),
	}
	for b.Loop() {
		if found, _ := d.Discover(selectors); len(found) != 0 {
			b.Fatalf("found %d processes, want none", len(found))
		}
	}
}
