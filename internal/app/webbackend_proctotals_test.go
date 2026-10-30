package app

import (
	"slices"
	"testing"

	"sermo/internal/process"
	"sermo/internal/web"
)

// fakeProcMetrics returns scripted per-pid resource values.
type fakeProcMetrics struct {
	rss     map[int]uint64
	ioRead  map[int]uint64
	ioWrite map[int]uint64
	fds     map[int]uint64
	threads map[int]uint64
}

func (f fakeProcMetrics) ProcessRSS(pid int) (uint64, bool) { v, ok := f.rss[pid]; return v, ok }
func (f fakeProcMetrics) ProcessIO(pid int) (uint64, uint64, bool) {
	return f.ioRead[pid], f.ioWrite[pid], true
}
func (f fakeProcMetrics) ProcessFDs(pid int) (uint64, bool) { v, ok := f.fds[pid]; return v, ok }
func (f fakeProcMetrics) ProcessThreads(pid int) (uint64, bool) {
	v, ok := f.threads[pid]
	return v, ok
}

func TestAggregateProcessesSumsTree(t *testing.T) {
	// A main process (100) and its child (200, source=child).
	procs := []process.Process{
		{PID: 100, Source: "command_match"},
		{PID: 200, PPID: 100, Source: "child"},
	}
	r := fakeProcMetrics{
		rss:     map[int]uint64{100: 1000, 200: 500},
		ioRead:  map[int]uint64{100: 10, 200: 5},
		ioWrite: map[int]uint64{100: 20, 200: 7},
		fds:     map[int]uint64{100: 8, 200: 3},
		threads: map[int]uint64{100: 4, 200: 2},
	}

	rows, totals := aggregateProcesses(procs, r)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if totals == nil {
		t.Fatal("totals must be set when there are processes")
	}
	if totals.Count != 2 || totals.RSS != 1500 {
		t.Fatalf("count/rss = %d/%d, want 2/1500", totals.Count, totals.RSS)
	}
	if totals.IORead != 15 || totals.IOWrite != 27 {
		t.Fatalf("io = %d/%d, want 15/27 (parent+child)", totals.IORead, totals.IOWrite)
	}
	if totals.FDs != 11 || totals.Threads != 6 {
		t.Fatalf("fds/threads = %d/%d, want 11/6", totals.FDs, totals.Threads)
	}
	// Per-row values still present (the child carries its own).
	if rows[1].RSS != 500 || rows[1].FDs != 3 {
		t.Fatalf("child row = %+v", rows[1])
	}
}

// A kept child keeps its row but stays out of the total, which has to match the
// metrics the service's rules read.
func TestAggregateProcessesLeavesKeptChildrenOutOfTheTotal(t *testing.T) {
	procs := []process.Process{
		{PID: 100, Source: "command_match"},
		{PID: 200, PPID: 100, Source: "child", Delegated: true, Kept: true},
	}
	r := fakeProcMetrics{
		rss:     map[int]uint64{100: 1000, 200: 500},
		ioRead:  map[int]uint64{100: 10, 200: 5},
		ioWrite: map[int]uint64{100: 20, 200: 7},
		fds:     map[int]uint64{100: 8, 200: 3},
		threads: map[int]uint64{100: 4, 200: 2},
	}

	rows, totals := aggregateProcesses(procs, r)
	if len(rows) != 2 || rows[1].RSS != 500 {
		t.Fatalf("rows = %+v, want both with their own readings", rows)
	}
	want := web.ProcessTotals{Count: 1, RSS: 1000, IORead: 10, IOWrite: 20, FDs: 8, Threads: 4}
	if *totals != want {
		t.Fatalf("totals = %+v, want %+v", *totals, want)
	}
}

func TestProcessPIDsLeavesKeptChildrenOut(t *testing.T) {
	got := processPIDs([]process.Process{
		{PID: 1},
		{PID: 2, Delegated: true},
		{PID: 3, Delegated: true, Kept: true},
	})
	if !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("pids = %v, want the principal and the selector-delegated process", got)
	}
}

func TestAggregateProcessesEmpty(t *testing.T) {
	rows, totals := aggregateProcesses(nil, fakeProcMetrics{})
	if rows != nil || totals != nil {
		t.Fatalf("empty set should give nil rows and totals, got %v/%v", rows, totals)
	}
}
