package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
)

// bandSLACapture records what a band recorder persists, keeping the series key
// so the tests can pin the "watch:<name>" / "<check>:<metric>" spellings.
type bandSLACapture struct {
	records []bandSLARecord
}

type bandSLARecord struct {
	service string
	check   string
	up      bool
}

func (c *bandSLACapture) RecordSLA(string, bool, time.Time) error { return nil }
func (c *bandSLACapture) RecordCheckSLA(service, check string, up bool, _ time.Time) error {
	c.records = append(c.records, bandSLARecord{service: service, check: check, up: up})
	return nil
}

// TestWatchMetricRecorderRecordsBandsNotValues pins the split for a raid watch:
// degraded and recovering persist as per-cycle state samples keyed like a
// service check's SLA, and never as value rows — a state draws as a band or as
// a line, never both.
func TestWatchMetricRecorderRecordsBandsNotValues(t *testing.T) {
	store := &bandSLACapture{}
	entry := map[string]any{checks.CheckKeyType: checks.CheckTypeRAID, checks.CheckKeyArray: "md0"}
	bands := checks.DeclaredBandMetrics(checks.CheckTypeRAID, entry)
	record := watchMetricRecorder(Deps{SLA: store}, "raid-md0", checks.CheckTypeRAID, "", bands)
	if record == nil {
		t.Fatal("a banded watch must get a recorder even though it has no line metrics")
	}
	record(map[string]any{
		checks.DataKeyDegraded:   float64(1),
		checks.DataKeyRecovering: float64(0),
	}, time.Now())

	if len(store.records) != 2 {
		t.Fatalf("records = %+v, want one sample per band", store.records)
	}
	byMetric := map[string]bandSLARecord{}
	for _, r := range store.records {
		if r.service != "watch:raid-md0" {
			t.Fatalf("series keyed %q, want the watch monitor key", r.service)
		}
		byMetric[r.check] = r
	}
	if byMetric[checks.DataKeyDegraded].up {
		t.Fatal("degraded=1 must record a down sample")
	}
	if !byMetric[checks.DataKeyRecovering].up {
		t.Fatal("recovering=0 must record an up sample")
	}
}

// TestWatchMetricRecorderNilStore pins that a banded watch with no SLA store
// gets no recorder rather than a panic.
func TestWatchMetricRecorderNilStore(t *testing.T) {
	bands := checks.DeclaredBandMetrics(checks.CheckTypeRAID, map[string]any{})
	if record := watchMetricRecorder(Deps{}, "raid-md0", checks.CheckTypeRAID, "", bands); record != nil {
		t.Fatal("no store, no recorder")
	}
}

// TestFileWatcherRecordsBandSamples pins the dead-letter shape: the band is the
// level the scan saw — breached is down, clean is up, and absence records what
// absent_ok declares it to mean, as an explicit sample rather than a gap.
func TestFileWatcherRecordsBandSamples(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  map[string]fileState
		absentOK bool
		want     bool
	}{
		{"breached file is down", map[string]fileState{"/root/dead.letter": {size: 42, breached: true}}, true, false},
		{"clean file is up", map[string]fileState{"/root/dead.letter": {size: 0}}, true, true},
		{"absent with absent_ok is up", map[string]fileState{}, true, true},
		{"absent without absent_ok is down", map[string]fileState{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []bool
			w := &fileWatcher{
				absentOK:   tc.absentOK,
				cond:       fileCond{sizeOp: ">", sizeValue: 0},
				recordBand: func(ok bool, _ time.Time) { got = append(got, ok) },
			}
			w.recordBandSample(tc.current, time.Now())
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("samples = %v, want one %v", got, tc.want)
			}
		})
	}

	// No size predicate: nothing to band; nil recorder: nothing to do.
	silent := &fileWatcher{recordBand: func(bool, time.Time) { t.Fatal("no predicate, no sample") }}
	silent.recordBandSample(map[string]fileState{}, time.Now())
	none := &fileWatcher{cond: fileCond{sizeOp: ">"}}
	none.recordBandSample(map[string]fileState{}, time.Now())
}

// TestCycleWriterRecordsServiceCheckBands pins the service-side key: a banded
// check's state persists under "<check>:<metric>", beside the measurements and
// outside the availability gate.
func TestCycleWriterRecordsServiceCheckBands(t *testing.T) {
	store := &bandSLACapture{}
	tree := map[string]any{
		"checks": map[string]any{
			"raid0": map[string]any{checks.CheckKeyType: checks.CheckTypeRAID, checks.CheckKeyArray: "md0"},
		},
	}
	w := newCycleWriter(Deps{SLA: store}, "storage", tree)
	if w == nil {
		t.Fatal("an SLA store must yield a cycle writer")
	}
	w.RecordMeasurement(checks.Result{Check: "raid0", Data: map[string]any{
		checks.DataKeyDegraded:   float64(0),
		checks.DataKeyRecovering: float64(1),
	}})
	if err := w.writeCycle(directCycleRecords{sla: store}, cycleRecord{}); err != nil {
		t.Fatalf("writeCycle: %v", err)
	}
	if len(store.records) != 2 {
		t.Fatalf("records = %+v, want the two band samples", store.records)
	}
	byKey := map[string]bandSLARecord{}
	for _, r := range store.records {
		if r.service != "storage" {
			t.Fatalf("series keyed %q, want the service name", r.service)
		}
		byKey[r.check] = r
	}
	if !byKey["raid0:degraded"].up || byKey["raid0:recovering"].up {
		t.Fatalf("samples = %+v, want degraded up and recovering down", byKey)
	}
}

// TestWatchSeriesServesBands pins the read gates: a declared band serves even on
// a watch that keeps no availability at all (the file watch), an undeclared
// metric is refused, and a disabled watch serves nothing.
func TestWatchSeriesServesBands(t *testing.T) {
	fileBands := checks.DeclaredBandMetrics(checks.CheckTypeFile, map[string]any{
		checks.CheckKeySize: map[string]any{checks.CheckKeyOp: ">", checks.CheckKeyValue: 0},
	})
	b := &WebBackend{
		sla: fakeSLAReader{},
		watches: map[string]*webWatch{
			"watch-dead-letter": {name: "watch-dead-letter", checkType: checks.CheckTypeFile, bands: fileBands},
			"watch-off":         {name: "watch-off", checkType: checks.CheckTypeFile, bands: fileBands, disabled: true},
		},
	}
	if _, ok := b.WatchSeries(context.Background(), "watch-dead-letter", "size", time.Hour); !ok {
		t.Fatal("a declared band must serve even though the file watch keeps no availability")
	}
	if _, ok := b.WatchSeries(context.Background(), "watch-dead-letter", "ghost", time.Hour); ok {
		t.Fatal("an undeclared band must be refused")
	}
	if _, ok := b.WatchSeries(context.Background(), "watch-dead-letter", "", time.Hour); ok {
		t.Fatal("a file watch keeps no availability series; metric-less must still refuse")
	}
	if _, ok := b.WatchSeries(context.Background(), "watch-off", "size", time.Hour); ok {
		t.Fatal("a disabled watch serves nothing")
	}
}

// TestSeriesServesCheckBands pins the service-side gate: metric requires its
// check, and only a declared band answers.
func TestSeriesServesCheckBands(t *testing.T) {
	raidBands := checks.DeclaredBandMetrics(checks.CheckTypeRAID, map[string]any{})
	b := &WebBackend{
		sla: fakeSLAReader{},
		entries: map[string]*webEntry{"storage": {
			checkTypes: map[string]string{"raid0": checks.CheckTypeRAID},
			checkBands: map[string][]checks.BandMetric{"raid0": raidBands},
		}},
	}
	if _, ok := b.Series(context.Background(), "storage", "raid0", "degraded", time.Hour); !ok {
		t.Fatal("a declared check band must serve")
	}
	if _, ok := b.Series(context.Background(), "storage", "raid0", "ghost", time.Hour); ok {
		t.Fatal("an undeclared band must be refused")
	}
	if _, ok := b.Series(context.Background(), "storage", "", "degraded", time.Hour); ok {
		t.Fatal("a band request without its check must be refused")
	}
}

// TestWebCheckMetricsAdvertisesBands pins the payload split: banded keys leave
// the line list and arrive flagged with severity and label, so a panel is never
// offered for a series nothing writes.
func TestWebCheckMetricsAdvertisesBands(t *testing.T) {
	raidBands := checks.DeclaredBandMetrics(checks.CheckTypeRAID, map[string]any{})
	got := webCheckMetrics(checks.CheckTypeRAID, "", raidBands)
	if len(got) != 2 {
		t.Fatalf("raid metrics = %+v, want its two bands and no lines", got)
	}
	for _, m := range got {
		if !m.Band {
			t.Fatalf("%q advertised as a line metric; raid states are bands", m.Name)
		}
	}
	if got[0].Name != checks.DataKeyDegraded || got[0].Severity != checks.SeverityError || got[0].Label == "" {
		t.Fatalf("degraded = %+v, want error severity and a label", got[0])
	}
	if got[1].Severity != checks.SeverityWarning {
		t.Fatalf("recovering = %+v, want warning severity", got[1])
	}
}
