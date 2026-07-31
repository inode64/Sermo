package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/state"
)

type fakeMeasureStore struct {
	recorded []string // measured (latency) check names
	metrics  []string // "check.metric"
	sla      int
	checkSLA []string
}

func (f *fakeMeasureStore) RecordSLA(string, bool, time.Time) error {
	f.sla++
	return nil
}
func (f *fakeMeasureStore) RecordCheckSLA(_, check string, _ bool, _ time.Time) error {
	f.checkSLA = append(f.checkSLA, check)
	return nil
}
func (f *fakeMeasureStore) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	f.recorded = append(f.recorded, check)
	return nil
}
func (f *fakeMeasureStore) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	f.metrics = append(f.metrics, check+"."+metric)
	return nil
}

type batchMeasureStore struct {
	fakeMeasureStore
	batches int
}

type failingMeasureStore struct {
	fakeMeasureStore
	err error
}

func (f *failingMeasureStore) RecordMeasurement(_, check string, _ float64, _ time.Time) error {
	f.recorded = append(f.recorded, check)
	if check == "web" {
		return f.err
	}
	return nil
}

func (f *batchMeasureStore) WithBatch(_ context.Context, record func(state.Batch) error) error {
	f.batches++
	return record(f)
}

func (f *batchMeasureStore) RecordDaemonMetric(string, float64, time.Time) error { return nil }
func (f *batchMeasureStore) RecordServiceMetric(string, string, float64, time.Time) error {
	return nil
}

func TestMeasurementRecorderGraphMetrics(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{
		"speed": map[string]any{"type": "hdparm"},
	}}
	deps := Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }}

	writer := newCycleWriter(deps, "svc", tree)
	if writer == nil {
		t.Fatal("expected a cycle writer for a service with graphable checks (hdparm)")
	}
	writer.RecordMeasurement(checks.Result{Check: "speed", Data: map[string]any{"read": 166.7, "cached": 9000.0, "device": "/dev/sda"}})
	writer.RecordCycle(context.Background(), cycleRecord{up: true, recordAvailability: true})

	want := map[string]bool{"speed.read": true, "speed.cached": true}
	for _, m := range store.metrics {
		delete(want, m)
	}
	if len(want) != 0 {
		t.Fatalf("missing metrics %v; recorded %v", want, store.metrics)
	}
	if len(store.metrics) != 2 {
		t.Errorf("non-numeric/undeclared Data keys must be ignored: %v", store.metrics)
	}
	if len(store.recorded) != 0 {
		t.Errorf("hdparm has no latency series: %v", store.recorded)
	}
}

func TestMeasuredCheckNames(t *testing.T) {
	tree := map[string]any{"checks": map[string]any{
		"web":   map[string]any{"type": "http"},
		"ping":  map[string]any{"type": "tcp"},
		"scan":  map[string]any{"type": "ports"},
		"unit":  map[string]any{"type": "service"},
		"space": map[string]any{"type": "storage"}, // not measured
		"flag":  map[string]any{"type": "file_exists"},
	}}
	got := measuredCheckNames(tree)
	for _, want := range []string{"web", "ping", "scan", "unit"} {
		if !got[want] {
			t.Errorf("expected %q to be measured", want)
		}
	}
	if got["space"] || got["flag"] {
		t.Error("storage/file_exists must not be measured")
	}
}

func TestMeasurementRecorderOnlyMeasuredChecks(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{
		"web":   map[string]any{"type": "http"},
		"space": map[string]any{"type": "storage"},
	}}
	deps := Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }}

	writer := newCycleWriter(deps, "svc", tree)
	if writer == nil {
		t.Fatal("expected a cycle writer for a service with measured checks")
	}
	writer.RecordMeasurement(checks.Result{Check: "web", Latency: 12 * time.Millisecond})
	writer.RecordMeasurement(checks.Result{Check: "space", Latency: 5 * time.Millisecond}) // not measured -> ignored
	writer.RecordCycle(context.Background(), cycleRecord{up: true, recordAvailability: true})

	if len(store.recorded) != 1 || store.recorded[0] != "web" {
		t.Fatalf("recorded = %v, want only [web]", store.recorded)
	}
}

func TestCycleWriterSkipsMeasurementsWithoutMeasuredChecks(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{"space": map[string]any{"type": "storage"}}}
	writer := newCycleWriter(Deps{SLA: store}, "svc", tree)
	writer.RecordMeasurement(checks.Result{Check: "space", Latency: 5 * time.Millisecond})
	writer.RecordCycle(context.Background(), cycleRecord{up: true, recordAvailability: true})
	if len(store.recorded) != 0 || len(store.metrics) != 0 {
		t.Fatalf("measurements = %v, metrics = %v, want neither", store.recorded, store.metrics)
	}
}

func TestCycleWriterBatchesOneObservedCycle(t *testing.T) {
	store := &batchMeasureStore{}
	tree := map[string]any{"checks": map[string]any{
		"web":   map[string]any{"type": "http"},
		"speed": map[string]any{"type": "hdparm"},
	}}
	writer := newCycleWriter(Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }}, "svc", tree)
	writer.RecordMeasurement(checks.Result{Check: "web", Latency: 12 * time.Millisecond})
	writer.RecordMeasurement(checks.Result{Check: "speed", Data: map[string]any{"read": 166.7, "cached": 9000.0}})
	writer.RecordCycle(context.Background(), cycleRecord{
		cache: map[string]checks.Result{
			"web":   {Check: "web", OK: true},
			"speed": {Check: "speed", OK: true},
		},
		ran:                map[string]bool{"web": true, "speed": true},
		up:                 true,
		recordAvailability: true,
	})

	if store.batches != 1 {
		t.Fatalf("batches = %d, want 1", store.batches)
	}
	if store.sla != 1 || len(store.checkSLA) != 2 || len(store.recorded) != 1 || len(store.metrics) != 2 {
		t.Fatalf("records: SLA=%d checkSLA=%v measurements=%v metrics=%v", store.sla, store.checkSLA, store.recorded, store.metrics)
	}
}

func TestCycleWriterRecordsMeasurementsDuringObserveOnlyCycle(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{"web": map[string]any{"type": "http"}}}
	writer := newCycleWriter(Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }}, "svc", tree)
	writer.RecordMeasurement(checks.Result{Check: "web", Latency: 12 * time.Millisecond})
	writer.RecordCycle(context.Background(), cycleRecord{
		cache: map[string]checks.Result{"web": {Check: "web", OK: true}},
		ran:   map[string]bool{"web": true},
		up:    true,
	})

	if len(store.recorded) != 1 || store.sla != 0 || len(store.checkSLA) != 0 {
		t.Fatalf("records: SLA=%d checkSLA=%v measurements=%v", store.sla, store.checkSLA, store.recorded)
	}
}

func TestCycleWriterDirectFallbackContinuesAfterWriteError(t *testing.T) {
	store := &failingMeasureStore{err: errors.New("measurement write failed")}
	tree := map[string]any{"checks": map[string]any{
		"web": map[string]any{"type": "http"},
		"api": map[string]any{"type": "http"},
	}}
	writer := newCycleWriter(Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }}, "svc", tree)
	writer.RecordMeasurement(checks.Result{Check: "web", Latency: 12 * time.Millisecond})
	writer.RecordMeasurement(checks.Result{Check: "api", Latency: 8 * time.Millisecond})
	writer.RecordCycle(context.Background(), cycleRecord{
		cache: map[string]checks.Result{
			"web": {Check: "web", OK: true},
			"api": {Check: "api", OK: true},
		},
		ran:                map[string]bool{"web": true, "api": true},
		up:                 true,
		recordAvailability: true,
	})

	if got, want := store.recorded, []string{"web", "api"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("measurements = %v, want %v", got, want)
	}
	recordedChecks := map[string]bool{}
	for _, check := range store.checkSLA {
		recordedChecks[check] = true
	}
	if len(recordedChecks) != 2 || !recordedChecks["web"] || !recordedChecks["api"] {
		t.Fatalf("check SLA = %v, want web and api", store.checkSLA)
	}
	if store.sla != 1 {
		t.Fatalf("service SLA = %d, want 1", store.sla)
	}
}
