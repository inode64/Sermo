package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestWatchRowUsesOnePublishedObservation(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	w := &webWatch{
		name: "memory", checkType: checks.CheckTypeMemory, interval: time.Minute,
		check:  map[string]any{checks.CheckKeyType: checks.CheckTypeMemory},
		graphs: []checks.GraphMetric{{Key: checks.DataKeyUsedPct}},
	}
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return at.Add(-time.Second) }
	result := checks.Result{Check: "memory", OK: true, Message: "original", Data: map[string]any{
		checks.DataKeyTotalBytes: uint64(100), checks.DataKeyAvailableBytes: uint64(80), checks.DataKeyUsedPct: float64(20),
	}}
	publishWatchFor(snapshots, w, result)
	clocks := 0
	backend := &WebBackend{watchSnapshots: snapshots, now: func() time.Time {
		clocks++
		snapshots.now = func() time.Time { return at }
		result.Message = "next publication"
		result.Data = map[string]any{
			checks.DataKeyTotalBytes: uint64(100), checks.DataKeyAvailableBytes: uint64(10), checks.DataKeyUsedPct: float64(90),
		}
		publishWatchFor(snapshots, w, result)
		return at
	}}
	view := backend.watchView(w, nil, watchActivity{})
	if clocks != 1 || view.Summary != "original" || view.LastCheckedAt != at.Add(-time.Second).Format(time.RFC3339) {
		t.Fatalf("row mixed publications or clocks: clocks=%d view=%+v", clocks, view)
	}
	if view.Meter == nil || view.Meter.UsedPct != 20 || len(view.Metrics) != 1 || view.Metrics[0].Value == nil || *view.Metrics[0].Value != 20 {
		t.Fatalf("row metrics mixed publications: %+v", view)
	}
}
