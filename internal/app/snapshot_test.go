package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/state"
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

func TestPersistentSnapshotsHydrateAndStore(t *testing.T) {
	t0 := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	store := &snapshotStoreFake{
		service: map[string]map[string]state.CheckSnapshotRecord{
			"web": {
				"http": {OK: true, Message: "status 200", Data: map[string]any{"status": float64(200)}, Ran: true, At: t0},
			},
		},
	}
	s, err := NewPersistentSnapshots(store, nil)
	if err != nil {
		t.Fatalf("NewPersistentSnapshots: %v", err)
	}
	if got := s.Get("web")["http"]; !got.OK || got.Message != "status 200" || got.Data["status"] != float64(200) || !got.At.Equal(t0) {
		t.Fatalf("hydrated snapshot = %+v", got)
	}

	t1 := t0.Add(time.Minute)
	s.now = func() time.Time { return t1 }
	s.Publish("web", map[string]checks.Result{
		"tcp": {Check: "tcp", OK: false, Message: "connection refused", Data: map[string]any{"port": float64(443)}},
	}, map[string]bool{"tcp": true})

	service := store.service["web"]
	if len(service) != 1 {
		t.Fatalf("stored service snapshots = %+v, want replaced current rows", service)
	}
	if got := service["tcp"]; got.OK || got.Message != "connection refused" || got.Data["port"] != float64(443) || !got.At.Equal(t1) {
		t.Fatalf("stored snapshot = %+v", got)
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

func TestPersistentWatchSnapshotsHydrateAndStore(t *testing.T) {
	t0 := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	store := &snapshotStoreFake{
		watch: map[string]map[string]state.CheckSnapshotRecord{
			"clock": {
				checks.DataKeyResult: {
					CheckType: "clock", OK: false, Message: "offset 1200ms",
					Data: map[string]any{"offset_ms": float64(1200)}, Ran: true, At: t0,
				},
			},
		},
	}
	s, err := NewPersistentWatchSnapshots(store, nil)
	if err != nil {
		t.Fatalf("NewPersistentWatchSnapshots: %v", err)
	}
	if got := s.Get("clock", "clock"); len(got) != 1 || got[0].Message != "offset 1200ms" || got[0].Data["offset_ms"] != float64(1200) {
		t.Fatalf("hydrated watch snapshots = %+v", got)
	}

	t1 := t0.Add(time.Minute)
	s.now = func() time.Time { return t1 }
	s.Publish("clock", "clock", checks.Result{
		Check:   "clock",
		OK:      true,
		Message: "offset 4ms",
		Data:    map[string]any{"offset_ms": float64(4)},
	})

	got := store.watch["clock"]["clock"]
	if got.CheckType != "clock" || !got.OK || got.Message != "offset 4ms" || got.Data["offset_ms"] != float64(4) || !got.At.Equal(t1) {
		t.Fatalf("stored watch snapshot = %+v", got)
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

type snapshotStoreFake struct {
	service map[string]map[string]state.CheckSnapshotRecord
	watch   map[string]map[string]state.CheckSnapshotRecord
}

func (s *snapshotStoreFake) ServiceCheckSnapshots() (map[string]map[string]state.CheckSnapshotRecord, error) {
	return s.service, nil
}

func (s *snapshotStoreFake) SetServiceCheckSnapshots(service string, records map[string]state.CheckSnapshotRecord) error {
	if s.service == nil {
		s.service = map[string]map[string]state.CheckSnapshotRecord{}
	}
	s.service[service] = records
	return nil
}

func (s *snapshotStoreFake) WatchCheckSnapshots() (map[string]map[string]state.CheckSnapshotRecord, error) {
	return s.watch, nil
}

func (s *snapshotStoreFake) SetWatchCheckSnapshot(watch, slot string, rec state.CheckSnapshotRecord) error {
	if s.watch == nil {
		s.watch = map[string]map[string]state.CheckSnapshotRecord{}
	}
	if s.watch[watch] == nil {
		s.watch[watch] = map[string]state.CheckSnapshotRecord{}
	}
	s.watch[watch][slot] = rec
	return nil
}
