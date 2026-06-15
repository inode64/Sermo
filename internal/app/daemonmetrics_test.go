package app

import (
	"testing"
	"time"
)

type fakeDaemonMetricReader struct {
	cpu       uint64
	rss       uint64
	ioRead    uint64
	ioWrite   uint64
	fds       uint64
	threads   uint64
	memTotal  uint64
	memUsed   uint64
	numCPU    int
	clockTick float64
}

func (r *fakeDaemonMetricReader) ProcessCPU(int) (uint64, bool) { return r.cpu, true }
func (r *fakeDaemonMetricReader) ProcessRSS(int) (uint64, bool) { return r.rss, true }
func (r *fakeDaemonMetricReader) ProcessIO(int) (uint64, uint64, bool) {
	return r.ioRead, r.ioWrite, true
}
func (r *fakeDaemonMetricReader) ProcessFDs(int) (uint64, bool)     { return r.fds, true }
func (r *fakeDaemonMetricReader) ProcessThreads(int) (uint64, bool) { return r.threads, true }
func (r *fakeDaemonMetricReader) TotalMemory() (uint64, uint64, bool) {
	return r.memTotal, r.memUsed, r.memTotal > 0
}
func (r *fakeDaemonMetricReader) SystemCPU() (uint64, uint64, bool) { return 0, 0, false }
func (r *fakeDaemonMetricReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (r *fakeDaemonMetricReader) NumCPU() int         { return r.numCPU }
func (r *fakeDaemonMetricReader) ClockTicks() float64 { return r.clockTick }

func TestDaemonMetricSamplerSeries(t *testing.T) {
	reader := &fakeDaemonMetricReader{
		cpu:       100,
		rss:       10 * 1024 * 1024,
		ioRead:    1000,
		ioWrite:   2000,
		fds:       8,
		threads:   3,
		memTotal:  100 * 1024 * 1024,
		numCPU:    2,
		clockTick: 100,
	}
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	sampler := &daemonMetricSampler{
		reader: reader,
		now:    func() time.Time { return now },
		pid:    42,
	}

	first := sampler.Series(time.Hour)
	if first.Current.PID != 42 || first.Current.CPUReady || first.Current.IOReady {
		t.Fatalf("first current = %+v, want pid and not-ready rates", first.Current)
	}
	if first.Memory.Summary.Count != 1 || first.CPU.Summary.Count != 0 || first.IO.Summary.Count != 0 {
		t.Fatalf("first summaries = mem:%+v cpu:%+v io:%+v", first.Memory.Summary, first.CPU.Summary, first.IO.Summary)
	}

	now = now.Add(time.Second)
	reader.cpu = 150
	reader.ioRead += 4096
	reader.ioWrite += 1024
	reader.fds = 9
	reader.threads = 4

	second := sampler.Series(time.Hour)
	if !second.Current.CPUReady || second.Current.CPU != 25 {
		t.Fatalf("CPU = ready:%v %.2f, want 25%%", second.Current.CPUReady, second.Current.CPU)
	}
	if !second.Current.IOReady || second.Current.IO != 5120 || second.Current.IORead != 4096 || second.Current.IOWrite != 1024 {
		t.Fatalf("IO current = %+v, want read/write/total rates", second.Current)
	}
	if second.Current.RSS != int64(10*1024*1024) || second.Current.FDs != 9 || second.Current.Threads != 4 {
		t.Fatalf("current counters = %+v", second.Current)
	}
	if second.Current.MemoryPercent != 10 {
		t.Fatalf("memory percent = %.2f, want 10", second.Current.MemoryPercent)
	}
	if second.CPU.Summary.Count != 1 || second.CPU.Summary.Avg != 25 {
		t.Fatalf("CPU summary = %+v", second.CPU.Summary)
	}
	if second.IO.Summary.Count != 1 || second.IO.Summary.Avg != 5120 {
		t.Fatalf("IO summary = %+v", second.IO.Summary)
	}
	if second.Memory.Summary.Count != 2 || len(second.Memory.Points) != 1 || second.Memory.Points[0].N != 2 {
		t.Fatalf("memory series = summary:%+v points:%+v", second.Memory.Summary, second.Memory.Points)
	}
}
