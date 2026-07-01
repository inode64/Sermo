package app

import (
	"context"
	"testing"
)

func TestSettlingMarkObservedAdvancesReadiness(t *testing.T) {
	ready := NewReadiness("systemd", 2, 0)
	ready.ExpectFirstCycles(2)
	s := NewSettling(ready)
	s.Reset([]string{"a", "b"})

	if s.Observed("a") || s.Observed("b") {
		t.Fatal("targets should start unsettled")
	}
	s.MarkObserved("a")
	if rep := ready.Report(context.Background()); rep.Ready || rep.Status != readinessStarting {
		t.Fatalf("one observed target should stay starting: %+v", rep)
	}
	s.MarkObserved("b")
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != "ok" {
		t.Fatalf("all observed targets should be ready: %+v", rep)
	}
}

// TestActiveMonitorTargetsDedupesMetricWatchKeys guards the readiness first-cycle
// gate against metric watches: net/icmp/swap watches expand to one Watch object
// per metric, all sharing one settling key (SettlingWatchKey(name)). The gate
// must be armed with the number of distinct keys, not objects — otherwise it
// waits for more first cycles than can ever fire and the daemon stays "starting"
// (readyz 503) forever.
func TestActiveMonitorTargetsDedupesMetricWatchKeys(t *testing.T) {
	watches := []*Watch{
		{Name: "uplink-ppp0"},      // net metric 1 (address)
		{Name: "uplink-ppp0"},      // net metric 2 (state) — same settling key
		{Name: "uplink-ppp0-ping"}, // icmp, single metric
	}
	if got := activeMonitorTargets(nil, watches); got != 2 {
		t.Fatalf("expected 2 distinct settling keys, got %d (counting objects wedges readiness)", got)
	}

	// End-to-end: arm readiness with the deduped count and settle each distinct
	// key once, as the scheduler does (only the first object per key runs the
	// observe-only cycle). The daemon must reach ready.
	ready := NewReadiness("openrc", 0, len(watches))
	ready.ExpectFirstCycles(activeMonitorTargets(nil, watches))
	s := NewSettling(ready)
	s.Reset(monitorTargetNames(nil, watches))
	seen := map[string]bool{}
	for _, w := range watches {
		if k := settlingKeyForWatch(w); !seen[k] {
			seen[k] = true
			s.MarkObserved(k)
		}
	}
	if rep := ready.Report(context.Background()); !rep.Ready {
		t.Fatalf("daemon must be ready once every distinct key settles: %+v", rep)
	}
}

func TestSettlingMarkObservedBulk(t *testing.T) {
	s := NewSettling(nil)
	s.Reset([]string{"a", "b"})
	s.MarkObservedBulk([]string{"a"})
	if !s.Observed("a") || s.Observed("b") {
		t.Fatalf("bulk mark should clear only named targets")
	}
}
