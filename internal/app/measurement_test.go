package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
)

type fakeMeasureStore struct {
	recorded []string // "check=valueMs"
}

func (f *fakeMeasureStore) RecordSLA(string, bool, time.Time) error { return nil }
func (f *fakeMeasureStore) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	f.recorded = append(f.recorded, check)
	return nil
}

func TestMeasuredCheckNames(t *testing.T) {
	tree := map[string]any{"checks": map[string]any{
		"web":   map[string]any{"type": "http"},
		"ping":  map[string]any{"type": "tcp"},
		"scan":  map[string]any{"type": "ports"},
		"unit":  map[string]any{"type": "service"},
		"space": map[string]any{"type": "disk"}, // not measured
		"flag":  map[string]any{"type": "file_exists"},
	}}
	got := measuredCheckNames(tree)
	for _, want := range []string{"web", "ping", "scan", "unit"} {
		if !got[want] {
			t.Errorf("expected %q to be measured", want)
		}
	}
	if got["space"] || got["flag"] {
		t.Error("disk/file_exists must not be measured")
	}
}

func TestMeasurementRecorderOnlyMeasuredChecks(t *testing.T) {
	store := &fakeMeasureStore{}
	tree := map[string]any{"checks": map[string]any{
		"web":   map[string]any{"type": "http"},
		"space": map[string]any{"type": "disk"},
	}}
	deps := Deps{SLA: store, Now: func() time.Time { return time.Unix(0, 0) }}

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
	tree := map[string]any{"checks": map[string]any{"space": map[string]any{"type": "disk"}}}
	if measurementRecorder(Deps{SLA: store}, "svc", tree) != nil {
		t.Fatal("expected nil recorder when no checks are measured")
	}
}
