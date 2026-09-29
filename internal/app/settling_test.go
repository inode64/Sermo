package app

import (
	"context"
	"slices"
	"testing"

	"sermo/internal/servicemgr"
)

func TestSettlingMarkObservedAdvancesReadiness(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendSystemd), 2, 0)
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
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != TargetStateOK {
		t.Fatalf("all observed targets should be ready: %+v", rep)
	}
}

// TestSettlingPendingDedupesMetricWatchKeys guards the readiness first-cycle
// gate against metric watches: net/icmp/swap watches expand to one Watch object
// per metric, all sharing one settling key (SettlingWatchKey(name)). The gate
// must be armed with the number of distinct keys, not objects — otherwise it
// waits for more first cycles than can ever fire and the daemon stays "starting"
// (readyz 503) forever.
func TestSettlingPendingDedupesMetricWatchKeys(t *testing.T) {
	watches := []*Watch{
		{Name: "uplink-ppp0"},      // net metric 1 (address)
		{Name: "uplink-ppp0"},      // net metric 2 (state) — same settling key
		{Name: "uplink-ppp0-ping"}, // icmp, single metric
	}
	ready := NewReadiness(string(servicemgr.BackendOpenRC), 0, len(watches))
	s := NewSettling(ready)
	s.Reset(monitorTargetNames(nil, watches))
	if got := s.Pending(); got != 2 {
		t.Fatalf("expected 2 distinct settling keys, got %d (counting objects wedges readiness)", got)
	}

	// End-to-end: arm readiness with the deduped count and let every watch
	// object complete its observe-only cycle. The daemon must reach ready.
	ready.ExpectFirstCycles(s.Pending())
	for _, w := range watches {
		s.MarkObserved(settlingKeyForWatch(w))
	}
	if rep := ready.Report(context.Background()); !rep.Ready {
		t.Fatalf("daemon must be ready once every distinct key settles: %+v", rep)
	}
}

// TestSettlingSharedKeyObservesEveryMetricWatch: every Watch object of a
// metric watch runs its own observe-only first cycle, so a metric that cycles
// second cannot fire a hook or notification, or skip reconciling its restored
// episode, just because a sibling metric settled the shared key first.
func TestSettlingSharedKeyObservesEveryMetricWatch(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendOpenRC), 0, 2)
	settling := NewSettling(ready)
	var observeOnly []bool
	metric := func() *Watch {
		return &Watch{Name: "uplink-ppp0", Settling: settling, Cycle: func(ctx context.Context) {
			observeOnly = append(observeOnly, observeOnlyCycle(ctx))
		}}
	}
	address, state := metric(), metric()
	settling.Reset(monitorTargetNames(nil, []*Watch{address, state}))
	ready.ExpectFirstCycles(settling.Pending())

	address.RunCycle(t.Context())
	if rep := ready.Report(context.Background()); rep.Ready {
		t.Fatalf("ready before every metric of the watch completed its first cycle: %+v", rep)
	}
	state.RunCycle(t.Context())
	address.RunCycle(t.Context())

	if !slices.Equal(observeOnly, []bool{true, true, false}) {
		t.Fatalf("observe-only cycles = %v, want [true true false]", observeOnly)
	}
	if rep := ready.Report(context.Background()); !rep.Ready {
		t.Fatalf("ready after every metric observed = %+v", rep)
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
