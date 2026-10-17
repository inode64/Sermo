package app

import (
	"testing"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/web"
)

func TestStrayMetricsDeletedExecutableAndPIDReuse(t *testing.T) {
	now := time.Unix(1000, 0)
	reader := &liveCPUReader{
		cpu: map[int]uint64{10: 100, 11: 10000}, rss: map[int]uint64{10: 4096, 11: 8192},
		ioRead: map[int]uint64{10: 100}, ioWrite: map[int]uint64{10: 200}, hz: 100, ncpu: 1,
	}
	collector := metrics.New(reader)
	collector.Now = func() time.Time { return now }
	procs := serviceOwnershipReader{
		10: {PID: 10, PPID: 1, StartTicks: 42, StartTicksOK: true, ExePrev: "/usr/bin/bash", TTY: 7, TTYOK: true},
		11: {PID: 11, PPID: 10, StartTicks: 43, StartTicksOK: true, TTY: 7, TTYOK: true},
	}
	reads := 0
	b := &WebBackend{terminalProcessReader: procs, sessionMetricCollector: collector, now: func() time.Time { return now },
		terminalAccessedAt: func(device uint64) (time.Time, bool) {
			reads++
			if device != 7 {
				t.Fatalf("unexpected terminal %d", device)
			}
			return time.Unix(900, 0), true
		},
	}
	sample := func(identity string, ticks uint64) web.StraySession {
		t.Helper()
		inv := web.SessionInventory{Strays: []web.StraySession{{Service: "ssh", PID: 10, StartTicks: ticks, Identity: identity}}}
		b.attachSessionMetrics(&inv)
		return inv.Strays[0]
	}
	first := sample("old", 42)
	if !first.MemoryReady || first.RSS != 4096 || first.CPUReady || first.IOReady || !first.HasIdle || first.IdleSeconds != 100 || reads != 1 {
		t.Fatalf("first sample (must exclude child usage): %+v, terminal reads %d", first, reads)
	}
	now = now.Add(time.Second)
	reader.cpu[10], reader.ioRead[10], reader.ioWrite[10] = 150, 1100, 2200
	second := sample("old", 42)
	if !second.CPUReady || second.CPU != 50 || !second.IOReady || second.IORead != 1000 || second.IOWrite != 2000 {
		t.Fatalf("deleted process rates: %+v", second)
	}
	procs[10] = process.Identity{PID: 10, PPID: 1, StartTicks: 50, StartTicksOK: true}
	stale := sample("old", 42)
	if stale.MemoryReady || stale.CPUReady || stale.IOReady || stale.HasIdle || len(b.sessionMetricKeys) != 0 {
		t.Fatalf("reused PID accepted stale identity: %+v", stale)
	}
	delete(procs, 11)
	fresh := sample("new", 50)
	if !fresh.MemoryReady || fresh.CPUReady || fresh.IOReady || fresh.HasIdle {
		t.Fatalf("new generation inherited metrics or invented idle: %+v", fresh)
	}
	delete(procs, 10)
	missing := sample("new", 50)
	if missing.MemoryReady || len(b.sessionMetricKeys) != 0 {
		t.Fatalf("missing process retained metrics: %+v", missing)
	}
}

func TestTerminalIdleUnavailableAndActivity(t *testing.T) {
	reads := 0
	accessedAt := cachedTerminalAccess(func(uint64) (time.Time, bool) { reads++; return time.Time{}, false })
	snapshot := sessionProcessSnapshot{byPID: map[int]process.Identity{1: {TTY: 7, TTYOK: true}, 2: {TTY: 7, TTYOK: true}}}
	row := web.TerminalSession{}
	attachTerminalSessionIdle(snapshot, &row, []int{1, 2}, time.Unix(100, 0), accessedAt)
	if row.HasIdle || reads != 1 {
		t.Fatalf("unavailable terminal: %+v, reads %d", row, reads)
	}
	row.ActivityUnix = 90
	attachTerminalSessionIdle(snapshot, &row, []int{1, 2}, time.Unix(100, 0), accessedAt)
	if !row.HasIdle || row.IdleSeconds != 10 || reads != 1 {
		t.Fatalf("multiplexer activity: %+v, reads %d", row, reads)
	}
}
