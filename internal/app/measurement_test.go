package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
)

type fakeMeasureStore struct {
	recorded []string // measured (latency) check names
	metrics  []string // "check.metric"
}

func (f *fakeMeasureStore) RecordSLA(string, bool, time.Time) error              { return nil }
func (f *fakeMeasureStore) RecordCheckSLA(string, string, bool, time.Time) error { return nil }
func (f *fakeMeasureStore) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	f.recorded = append(f.recorded, check)
	return nil
}
func (f *fakeMeasureStore) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	f.metrics = append(f.metrics, check+"."+metric)
	return nil
}

func TestMeasurementRecorderGraphMetrics(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{
		"speed": map[string]any{"type": "hdparm"},
	}}
	deps := Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }, ExecxRunner: execx.CommandRunner{}}

	record := measurementRecorder(deps, "svc", tree)
	if record == nil {
		t.Fatal("expected a recorder for a service with graphable checks (hdparm)")
	}
	record(checks.Result{Check: "speed", Data: map[string]any{"read": 166.7, "cached": 9000.0, "device": "/dev/sda"}})

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
	deps := Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }, ExecxRunner: execx.CommandRunner{}}

	record := measurementRecorder(deps, "svc", tree)
	if record == nil {
		t.Fatal("expected a recorder for a service with measured checks")
	}
	record(checks.Result{Check: "web", Latency: 12 * time.Millisecond})
	record(checks.Result{Check: "space", Latency: 5 * time.Millisecond}) // not measured -> ignored

	if len(store.recorded) != 1 || store.recorded[0] != "web" {
		t.Fatalf("recorded = %v, want only [web]", store.recorded)
	}
}

func TestMeasurementRecorderNilWithoutMeasuredChecks(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{"space": map[string]any{"type": "storage"}}}
	if measurementRecorder(Deps{SLA: store, ExecxRunner: execx.CommandRunner{}}, "svc", tree) != nil {
		t.Fatal("expected nil recorder when no checks are measured")
	}
}
