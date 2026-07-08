package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestSnapshotsRoundTrip(t *testing.T) {
	s := NewSnapshots()
	if s.Get("web") != nil {
		t.Fatal("unobserved service should have no snapshot")
	}
	s.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: true, Message: "status 200"},
		"warn": {Check: "warn", OK: false, Optional: true},
	}, map[string]bool{"http": true, "warn": true})
	got := s.Get("web")
	if len(got) != 2 || !got["http"].OK || got["http"].Message != "status 200" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got["warn"].OK || !got["warn"].Optional {
		t.Fatalf("optional/failed not preserved: %+v", got["warn"])
	}
}

func TestSnapshotsPublishRanFlag(t *testing.T) {
	s := NewSnapshots()
	s.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	got := s.Get("web")
	if !got["fast"].Ran {
		t.Fatal("fast check should show ran=true")
	}
	if got["slow"].Ran {
		t.Fatal("interval-deferred slow check should show ran=false")
	}
}

func TestSnapshotsPublishAtOnlyWhenRan(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Second)
	s := NewSnapshots()
	s.now = func() time.Time { return t0 }

	s.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true},
	}, map[string]bool{"fast": true, "slow": true})

	s.now = func() time.Time { return t1 }
	s.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	got := s.Get("web")
	if got["fast"].At != t1 {
		t.Fatalf("fast At = %v, want %v", got["fast"].At, t1)
	}
	if got["slow"].At != t0 {
		t.Fatalf("cached slow At = %v, want prior %v", got["slow"].At, t0)
	}
	if got["slow"].Ran {
		t.Fatal("slow should not show ran on second cycle")
	}
}

func TestWatchSnapshotsKeepMetricSlots(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	s := NewWatchSnapshots()
	s.now = func() time.Time { return t0 }

	s.Publish("uplink", checks.CheckTypeICMP, checks.Result{
		Check: "uplink",
		Data:  map[string]any{checks.DataKeyMetric: checks.NetMetricState, checks.DataKeyValue: checks.NetStateUp},
	})
	s.Publish("uplink", checks.CheckTypeICMP, checks.Result{
		Check: "uplink",
		Data:  map[string]any{checks.DataKeyMetric: checks.IcmpMetricLatency, checks.DataKeyValue: 12.5},
	})

	got := s.Get("uplink", checks.CheckTypeICMP)
	if len(got) != 2 {
		t.Fatalf("watch snapshots = %+v, want two metric slots", got)
	}
	if got[0].Data[checks.DataKeyMetric] != checks.IcmpMetricLatency || got[1].Data[checks.DataKeyMetric] != checks.NetMetricState {
		t.Fatalf("watch snapshot order/data = %+v", got)
	}
	if wrongType := s.Get("uplink", checks.CheckTypeNet); len(wrongType) != 0 {
		t.Fatalf("wrong check type returned snapshots: %+v", wrongType)
	}
}

func TestWorkerPublishesCache(t *testing.T) {
	var published map[string]checks.Result
	w := &Worker{
		Service: "web",
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			return map[string]checks.Result{"http": {Check: "http", OK: true}}
		},
		Publish: func(c map[string]checks.Result, _ map[string]bool) { published = c },
	}
	w.RunCycle(context.Background())
	if published == nil || !published["http"].OK {
		t.Fatalf("worker did not publish its cache: %+v", published)
	}
}

func TestWorkerPausedDoesNotPublish(t *testing.T) {
	called := false
	w := &Worker{
		Service:  "web",
		IsPaused: func() bool { return true },
		Checks:   func(context.Context, checks.Deps) map[string]checks.Result { return nil },
		Publish:  func(map[string]checks.Result, map[string]bool) { called = true },
	}
	w.RunCycle(context.Background())
	if called {
		t.Fatal("a paused cycle must not publish a snapshot")
	}
}
