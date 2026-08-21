package app

import (
	"testing"
	"time"

	"sermo/internal/process"
)

// fakeStartReader is the minimal metrics.Reader that also reports process start
// times, which is what serviceStartTime needs.
type fakeStartReader struct {
	starts map[int]time.Time
}

func (fakeStartReader) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (fakeStartReader) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (fakeStartReader) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (fakeStartReader) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (fakeStartReader) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (fakeStartReader) TotalMemory() (uint64, uint64, bool)  { return 0, 0, false }
func (fakeStartReader) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (fakeStartReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (fakeStartReader) NumCPU() int         { return 1 }
func (fakeStartReader) ClockTicks() float64 { return 100 }
func (r fakeStartReader) ProcessStartTime(pid int) (time.Time, bool) {
	started, ok := r.starts[pid]
	return started, ok
}

// TestServiceStartTimeUsesPrincipalNotOldestMember is the libvirtd case: a
// service's control group holds per-domain virtiofsd helpers that outlive a
// restart, and reading the oldest member reported their age as the daemon's
// uptime — 74 days for a libvirtd restarted minutes earlier.
func TestServiceStartTimeUsesPrincipalNotOldestMember(t *testing.T) {
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	principal := now.Add(-90 * time.Second)
	helper := now.Add(-74 * 24 * time.Hour)
	reader := fakeStartReader{starts: map[int]time.Time{1: principal, 2: helper}}

	procs := []process.Process{
		{PID: 1, Source: process.SourceBackend, Role: process.RoleMain},
		{PID: 2, Source: process.SourceBackend, Role: process.RoleMain, Stray: true},
	}
	started, ok := serviceStartTime(procs, reader, now)
	if !ok {
		t.Fatal("serviceStartTime() reported no start time")
	}
	if !started.Equal(principal) {
		t.Fatalf("start = %v, want the principal's %v, not the 74-day-old helper's", started, helper)
	}
	if _, _, secs := serviceRuntimeUptime(started, now); secs != 90 {
		t.Fatalf("uptime = %ds, want 90s", secs)
	}
}

// TestServiceStartTimeFallsBackToOldestWithoutPrincipal keeps the previous
// behaviour where no process is an unambiguous principal.
func TestServiceStartTimeFallsBackToOldestWithoutPrincipal(t *testing.T) {
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	oldest := now.Add(-10 * time.Minute)
	reader := fakeStartReader{starts: map[int]time.Time{3: now.Add(-time.Minute), 4: oldest}}

	procs := []process.Process{
		{PID: 3, Source: process.SourceChild, Role: process.RoleChild},
		{PID: 4, Source: process.SourceChild, Role: process.RoleChild},
	}
	started, ok := serviceStartTime(procs, reader, now)
	if !ok {
		t.Fatal("serviceStartTime() reported no start time")
	}
	if !started.Equal(oldest) {
		t.Fatalf("start = %v, want the oldest member's %v", started, oldest)
	}
}

// TestServiceStartTimeFallsBackWhenPrincipalHasNoStart guards the case where the
// principal exited between discovery and the start-time read.
func TestServiceStartTimeFallsBackWhenPrincipalHasNoStart(t *testing.T) {
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	helper := now.Add(-time.Hour)
	reader := fakeStartReader{starts: map[int]time.Time{2: helper}}

	procs := []process.Process{
		{PID: 1, Source: process.SourceBackend, Role: process.RoleMain},
		{PID: 2, Source: process.SourceBackend, Role: process.RoleMain},
	}
	started, ok := serviceStartTime(procs, reader, now)
	if !ok {
		t.Fatal("serviceStartTime() reported no start time")
	}
	if !started.Equal(helper) {
		t.Fatalf("start = %v, want the fallback %v", started, helper)
	}
}
