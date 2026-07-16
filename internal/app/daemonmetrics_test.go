package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sermo/internal/state"
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

type metricSeriesTestSample struct {
	at    time.Time
	value float64
	ok    bool
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

func TestMetricSeriesAggregatesValidSamplesByMinute(t *testing.T) {
	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	series := metricSeries(
		"test", "custom", "widgets", 2*time.Hour,
		[]metricSeriesTestSample{
			{at: base.Add(2 * time.Second), value: 2, ok: true},
			{at: base.Add(45 * time.Second), value: 4, ok: true},
			{at: base.Add(time.Minute), value: 8, ok: true},
			{at: base.Add(2 * time.Minute), value: 16, ok: false},
		},
		func(sample metricSeriesTestSample) time.Time { return sample.at },
		func(sample metricSeriesTestSample) (float64, bool) { return sample.value, sample.ok },
	)

	if series.Check != "test" || series.Metric != "custom" || series.Unit != "widgets" || series.Since != "2h0m0s" {
		t.Fatalf("series metadata = %+v", series)
	}
	if series.Summary.Count != 3 || series.Summary.Avg != 14.0/3.0 || series.Summary.Min != 2 || series.Summary.Max != 8 {
		t.Fatalf("series summary = %+v", series.Summary)
	}
	if len(series.Points) != 2 {
		t.Fatalf("series points = %+v, want 2 points", series.Points)
	}
	if first := series.Points[0]; first.Start != base.Format(time.RFC3339) || first.N != 2 || first.Avg != 3 || first.Min != 2 || first.Max != 4 {
		t.Fatalf("first point = %+v", first)
	}
	if second := series.Points[1]; second.Start != base.Add(time.Minute).Format(time.RFC3339) || second.N != 1 || second.Avg != 8 || second.Min != 8 || second.Max != 8 {
		t.Fatalf("second point = %+v", second)
	}
}

func TestLoadPersistentMetricTriplet(t *testing.T) {
	at := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		summaryError bool
		seriesError  bool
		wantOK       bool
	}{
		{name: "success", wantOK: true},
		{name: "summary error", summaryError: true},
		{name: "series error", seriesError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			triplet, ok := loadPersistentMetricTriplet("runtime", at, time.Hour, persistentMetricReader{
				summary: func(string, time.Duration, time.Time) (state.MeasurementStat, error) {
					if test.summaryError {
						return state.MeasurementStat{}, errors.New("summary unavailable")
					}
					return state.MeasurementStat{Count: 1, Avg: 2, Min: 2, Max: 2}, nil
				},
				series: func(string, time.Time, time.Time) ([]state.MeasurementPoint, error) {
					if test.seriesError {
						return nil, errors.New("series unavailable")
					}
					return []state.MeasurementPoint{{Start: at, N: 1, Avg: 2, Min: 2, Max: 2}}, nil
				},
			})
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v", ok, test.wantOK)
			}
			if test.wantOK && (triplet.cpu.Check != "runtime" || triplet.memory.Metric != "memory" || triplet.io.Unit != "B/s") {
				t.Fatalf("triplet metadata = %+v", triplet)
			}
		})
	}
}

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
	sampler := &DaemonMetricSampler{
		reader: reader,
		now:    func() time.Time { return now },
		pid:    42,
	}

	sampler.sample()
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

	sampler.sample()
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

func TestDaemonMetricSamplerReadsPersistedHistory(t *testing.T) {
	store, err := state.OpenContext(context.Background(), filepath.Join(t.TempDir(), state.Filename))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	reader := &fakeDaemonMetricReader{
		cpu:       100,
		rss:       1024,
		ioRead:    1000,
		ioWrite:   2000,
		memTotal:  4096,
		numCPU:    1,
		clockTick: 100,
	}
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	first := &DaemonMetricSampler{
		reader: reader,
		store:  store,
		now:    func() time.Time { return now },
		pid:    42,
	}
	first.sample()

	now = now.Add(time.Minute)
	reader.cpu = 200
	reader.ioRead += 6000
	reader.ioWrite += 3000
	first.sample()
	recorded := first.Series(time.Hour)
	if recorded.CPU.Summary.Count != 1 || recorded.IO.Summary.Count != 1 || recorded.Memory.Summary.Count == 0 {
		t.Fatalf("recorded summaries = cpu:%+v io:%+v memory:%+v", recorded.CPU.Summary, recorded.IO.Summary, recorded.Memory.Summary)
	}

	now = now.Add(time.Minute)
	reader.cpu = 300
	second := &DaemonMetricSampler{
		reader: reader,
		store:  store,
		now:    func() time.Time { return now },
		pid:    42,
	}
	second.sample()
	afterRestart := second.Series(time.Hour)
	if afterRestart.Current.CPUReady {
		t.Fatalf("fresh sampler current CPU should be measuring, got %+v", afterRestart.Current)
	}
	if afterRestart.CPU.Summary.Count != 1 || len(afterRestart.CPU.Points) != 1 {
		t.Fatalf("persisted CPU series not restored: summary=%+v points=%+v", afterRestart.CPU.Summary, afterRestart.CPU.Points)
	}
	if afterRestart.IO.Summary.Count != 1 || len(afterRestart.IO.Points) != 1 {
		t.Fatalf("persisted IO series not restored: summary=%+v points=%+v", afterRestart.IO.Summary, afterRestart.IO.Points)
	}
	if afterRestart.Memory.Summary.Count < 3 {
		t.Fatalf("memory history = %+v, want prior samples plus current", afterRestart.Memory.Summary)
	}
}

func TestDaemonMetricSamplerSeriesDoesNotSampleDashboardReads(t *testing.T) {
	reader := &fakeDaemonMetricReader{rss: 1024, memTotal: 4096, numCPU: 1, clockTick: 100}
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	sampler := &DaemonMetricSampler{reader: reader, now: func() time.Time { return now }, pid: 42}
	sampler.sample()

	reader.rss = 4096
	first := sampler.Series(time.Hour)
	second := sampler.Series(time.Hour)
	if first.Memory.Summary.Count != 1 || second.Memory.Summary.Count != 1 {
		t.Fatalf("dashboard reads changed daemon samples: first=%d second=%d", first.Memory.Summary.Count, second.Memory.Summary.Count)
	}
	if first.Current.RSS != 1024 || second.Current.RSS != 1024 {
		t.Fatalf("dashboard read sampled current RSS: first=%d second=%d", first.Current.RSS, second.Current.RSS)
	}
}

type signalingDaemonMetricReader struct {
	*fakeDaemonMetricReader
	once    sync.Once
	sampled chan struct{}
}

func (r *signalingDaemonMetricReader) ProcessRSS(pid int) (uint64, bool) {
	r.once.Do(func() { close(r.sampled) })
	return r.fakeDaemonMetricReader.ProcessRSS(pid)
}

func TestDaemonMetricSamplerRunSamplesWithoutDashboard(t *testing.T) {
	reader := &signalingDaemonMetricReader{
		fakeDaemonMetricReader: &fakeDaemonMetricReader{rss: 1024, memTotal: 4096, numCPU: 1, clockTick: 100},
		sampled:                make(chan struct{}),
	}
	sampler := &DaemonMetricSampler{reader: reader, now: time.Now, pid: 42}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sampler.Run(ctx, time.Hour)
		close(done)
	}()

	select {
	case <-reader.sampled:
	case <-time.After(time.Second):
		t.Fatal("daemon sampler did not take its startup sample")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("daemon sampler did not stop after cancellation")
	}
	if got := sampler.Series(time.Hour).Memory.Summary.Count; got != 1 {
		t.Fatalf("background sample count = %d, want 1", got)
	}
}
