package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/web"
)

type processUptimeMetricReader struct {
	starts map[int]time.Time
}

func (processUptimeMetricReader) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (processUptimeMetricReader) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (processUptimeMetricReader) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (processUptimeMetricReader) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (processUptimeMetricReader) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (processUptimeMetricReader) TotalMemory() (uint64, uint64, bool)  { return 0, 0, false }
func (processUptimeMetricReader) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (processUptimeMetricReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (processUptimeMetricReader) NumCPU() int         { return 0 }
func (processUptimeMetricReader) ClockTicks() float64 { return 0 }
func (r processUptimeMetricReader) ProcessStartTime(pid int) (time.Time, bool) {
	startedAt, found := r.starts[pid]
	return startedAt, found
}

var _ metrics.Reader = processUptimeMetricReader{}

type processUptimeRecord struct {
	service     string
	startedAt   time.Time
	confirmedAt time.Time
	source      string
}

type processUptimeCapture struct {
	records []processUptimeRecord
}

func (c *processUptimeCapture) RecordProcessUptime(service string, startedAt, confirmedAt time.Time, source string) error {
	c.records = append(c.records, processUptimeRecord{service: service, startedAt: startedAt, confirmedAt: confirmedAt, source: source})
	return nil
}

func TestProcessUptimeRecorderUsesTrustedOldestProcess(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	backendStart := now.Add(-2 * time.Hour)
	childStart := now.Add(-3 * time.Hour)
	capture := &processUptimeCapture{}
	record := processUptimeRecorder(Deps{
		ProcessUptime: capture,
		Now:           func() time.Time { return now },
	}, "web", nil, func() []process.Process {
		return []process.Process{
			{PID: 10, Source: process.SourceBackend},
			{PID: 11, Source: process.SourceChild},
		}
	}, processUptimeMetricReader{starts: map[int]time.Time{10: backendStart, 11: childStart}})

	record(context.Background())
	if len(capture.records) != 1 {
		t.Fatalf("records = %+v, want one trusted process record", capture.records)
	}
	got := capture.records[0]
	if got.service != "web" || !got.startedAt.Equal(backendStart) || !got.confirmedAt.Equal(now) || got.source != process.SourceBackend {
		t.Fatalf("record = %+v, want web backend process from %s", got, backendStart)
	}
}

func TestProcessUptimeRecorderRequiresTrustedIdentityAndPlausibleStart(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	strictStart := now.Add(-time.Hour)

	for _, tc := range []struct {
		name      string
		selectors []process.Selector
		procs     []process.Process
		starts    map[int]time.Time
		want      bool
	}{
		{
			name:   "child only is not evidence",
			procs:  []process.Process{{PID: 1, Source: process.SourceChild}},
			starts: map[int]time.Time{1: strictStart},
		},
		{
			name:   "pidfile alone is not evidence",
			procs:  []process.Process{{PID: 1, Source: process.SelectorPidfile}},
			starts: map[int]time.Time{1: strictStart},
		},
		{
			name:      "strict command selector is evidence",
			selectors: []process.Selector{{Name: "main", Type: process.SelectorCommandMatch, Exe: "/usr/sbin/demo", User: "demo"}},
			procs:     []process.Process{{PID: 1, Role: "main", Source: process.SelectorCommandMatch}},
			starts:    map[int]time.Time{1: strictStart},
			want:      true,
		},
		{
			name:   "future start is rejected",
			procs:  []process.Process{{PID: 1, Source: process.SourceBackend}},
			starts: map[int]time.Time{1: now.Add(time.Second)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &processUptimeCapture{}
			record := processUptimeRecorder(Deps{
				ProcessUptime: capture,
				Now:           func() time.Time { return now },
			}, "web", tc.selectors, func() []process.Process { return tc.procs }, processUptimeMetricReader{starts: tc.starts})
			record(context.Background())
			if got := len(capture.records) == 1; got != tc.want {
				t.Fatalf("recorded=%v records=%+v, want %v", got, capture.records, tc.want)
			}
		})
	}
}

func TestWorkerRecordsProcessUptimeOnlyDuringMonitoredCycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		paused bool
		want   int
	}{
		{name: "active", want: 1},
		{name: "paused", paused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			w := &Worker{
				Service:  "web",
				IsPaused: func() bool { return tc.paused },
				RecordProcessUptime: func(context.Context) {
					calls++
				},
				Checks: func(context.Context, checks.Deps) map[string]checks.Result {
					return map[string]checks.Result{}
				},
			}
			w.RunCycle(context.Background())
			if calls != tc.want {
				t.Fatalf("process uptime calls = %d, want %d", calls, tc.want)
			}
		})
	}
}

func TestServiceProcessActiveUsesFreshDaemonSampleWithoutMetrics(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	samples := NewServiceMetricSampler()
	samples.Record("web", web.ServiceRuntime{
		At:        now.UTC().Format(time.RFC3339),
		StartedAt: now.Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	b := &WebBackend{serviceMetrics: samples, now: func() time.Time { return now }}
	if !b.serviceProcessActive("web", &webEntry{interval: time.Minute}) {
		t.Fatal("fresh process start sample should mark the service process active")
	}
	if got := ServiceState(true, true, "active", checkHealthUnknown, true, false, true); got != TargetStateActive {
		t.Fatalf("ServiceState with confirmed process = %q, want %q", got, TargetStateActive)
	}
}
