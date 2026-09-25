package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

func TestWebBackendDetailSharesFreshnessAndRetainsStaleTimestamp(t *testing.T) {
	now := time.Unix(1000, 0)
	snapshots := NewSnapshots()
	snapshots.byService["web"] = map[string]CheckSnapshot{
		"fresh": {CheckType: checks.CheckTypeHTTP, At: now.Add(-time.Minute), Observation: checks.ObservationHealthy, OK: true, Ran: true},
		"old":   {CheckType: checks.CheckTypeHTTP, At: now.Add(-3 * time.Minute), Observation: checks.ObservationHealthy, OK: true, Ran: true},
	}
	clockReads := 0
	b := &WebBackend{
		now:       func() time.Time { clockReads++; return now },
		snapshots: snapshots,
		entries: map[string]*webEntry{"web": {
			interval: time.Minute, noResidentProcess: true,
			checkNames: []string{"fresh", "old"},
			checkTypes: map[string]string{"fresh": checks.CheckTypeHTTP, "old": checks.CheckTypeHTTP},
			status:     func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
		}},
	}
	detail, ok := b.Detail(t.Context(), "web")
	if !ok || clockReads != 1 || len(detail.Checks) != 2 {
		t.Fatalf("detail=%+v ok=%v clock reads=%d", detail, ok, clockReads)
	}
	if fresh := detail.Checks[0]; fresh.Stale || !fresh.Ran || !fresh.OK {
		t.Fatalf("fresh check=%+v", fresh)
	}
	if old := detail.Checks[1]; !old.Stale || old.Ran || old.At != now.Add(-3*time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("old check=%+v", old)
	}
}
