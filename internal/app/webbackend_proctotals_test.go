package app

import (
	"testing"

	"sermo/internal/process"
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
	rd, rok := f.ioRead[pid]
	wr, wok := f.ioWrite[pid]
	return rd, wr, rok || wok
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
	if !totals.HasIO || !rows[0].HasIO || !rows[1].HasIO {
		t.Fatalf("has_io = totals:%v rows:%v/%v, want all true", totals.HasIO, rows[0].HasIO, rows[1].HasIO)
	}
	if totals.FDs != 11 || totals.Threads != 6 {
		t.Fatalf("fds/threads = %d/%d, want 11/6", totals.FDs, totals.Threads)
	}
	// Per-row values still present (the child carries its own).
	if rows[1].RSS != 500 || rows[1].FDs != 3 {
		t.Fatalf("child row = %+v", rows[1])
	}
}

func TestAggregateProcessesPreservesZeroIOAvailability(t *testing.T) {
	procs := []process.Process{{PID: 100, Source: "pidfile"}}
	rows, totals := aggregateProcesses(procs, fakeProcMetrics{
		ioRead:  map[int]uint64{100: 0},
		ioWrite: map[int]uint64{100: 0},
	})
	if len(rows) != 1 || totals == nil {
		t.Fatalf("aggregateProcesses() rows=%d totals=%+v, want one row and totals", len(rows), totals)
	}
	if !rows[0].HasIO || !totals.HasIO {
		t.Fatalf("has_io = row:%v totals:%v, want true even for 0/0 counters", rows[0].HasIO, totals.HasIO)
	}
	if rows[0].IORead != 0 || rows[0].IOWrite != 0 || totals.IORead != 0 || totals.IOWrite != 0 {
		t.Fatalf("io counters = row %d/%d totals %d/%d, want all zero", rows[0].IORead, rows[0].IOWrite, totals.IORead, totals.IOWrite)
	}
}

func TestAggregateProcessesLeavesIOUnavailableWhenUnreadable(t *testing.T) {
	procs := []process.Process{{PID: 100, Source: "pidfile"}}
	rows, totals := aggregateProcesses(procs, fakeProcMetrics{})
	if len(rows) != 1 || totals == nil {
		t.Fatalf("aggregateProcesses() rows=%d totals=%+v, want one row and totals", len(rows), totals)
	}
	if rows[0].HasIO || totals.HasIO {
		t.Fatalf("has_io = row:%v totals:%v, want false for unreadable counters", rows[0].HasIO, totals.HasIO)
	}
}

func TestAggregateProcessesEmpty(t *testing.T) {
	rows, totals := aggregateProcesses(nil, fakeProcMetrics{})
	if rows != nil || totals != nil {
		t.Fatalf("empty set should give nil rows and totals, got %v/%v", rows, totals)
	}
}
