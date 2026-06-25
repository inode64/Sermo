package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/state"
)

func TestStateCompactPrunesOldHistory(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
engine: { backend: auto, interval: 30s }
paths:
  services: [ `+root+`/services ]
  state: `+root+`/state
defaults: { policy: { cooldown: 5m } }
`)
	global := filepath.Join(root, "sermo.yml")

	store, err := state.Open(filepath.Join(root, "state", state.Filename))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{old, recent} {
		if err := store.RecordSLA("web", true, at); err != nil {
			t.Fatalf("RecordSLA(%s): %v", at, err)
		}
		if err := store.RecordCheckSLA("web", "http", true, at); err != nil {
			t.Fatalf("RecordCheckSLA(%s): %v", at, err)
		}
		if err := store.RecordMeasurement("web", "http", 10, at); err != nil {
			t.Fatalf("RecordMeasurement(%s): %v", at, err)
		}
		if err := store.RecordMetric("web", "http", "latency", 10, at); err != nil {
			t.Fatalf("RecordMetric(%s): %v", at, err)
		}
		if err := store.RecordDaemonMetric("cpu", 10, at); err != nil {
			t.Fatalf("RecordDaemonMetric(%s): %v", at, err)
		}
		if err := store.RecordServiceMetric("web", "cpu", 10, at); err != nil {
			t.Fatalf("RecordServiceMetric(%s): %v", at, err)
		}
		if err := store.RecordEvent(state.EventRecord{At: at, Service: "web", Kind: "action", Message: "restart"}); err != nil {
			t.Fatalf("RecordEvent(%s): %v", at, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	var stdout, stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"--config", global, "state", "compact", "--before", recent.Format(time.RFC3339)})
	if code != exitSuccess {
		t.Fatalf("state compact exit=%d stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "pruned 7 row(s)") || !strings.Contains(got, "service_metrics=1") || !strings.Contains(got, "events=1") {
		t.Fatalf("state compact output = %q, want pruned history summary", got)
	}

	store, err = state.Open(filepath.Join(root, "state", state.Filename))
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer store.Close()
	points, err := store.ServiceMetricSeries("web", "cpu", old.Add(-time.Minute), recent.Add(time.Minute))
	if err != nil {
		t.Fatalf("ServiceMetricSeries: %v", err)
	}
	if len(points) != 1 || !points[0].Start.Equal(recent) {
		t.Fatalf("service metric points = %+v, want only recent bucket", points)
	}
}
