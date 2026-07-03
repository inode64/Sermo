package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

func TestCheckHealthSummary(t *testing.T) {
	snap := map[string]CheckSnapshot{
		"http": {OK: true},
		"tcp":  {OK: false},
		"warn": {OK: false, Optional: true},
		"gate": {OK: true, Skipped: true},
	}
	failing, health := checkHealthSummary(snap, []string{"http", "tcp", "warn", "gate"}, true)
	if failing != 1 || health != "failing" {
		t.Fatalf("got failing=%d health=%q, want 1 failing", failing, health)
	}

	failing, health = checkHealthSummary(snap, []string{"http", "warn", "gate"}, true)
	if failing != 0 || health != "ok" {
		t.Fatalf("without tcp: failing=%d health=%q, want ok", failing, health)
	}

	snap = map[string]CheckSnapshot{
		"cert": {OK: false, Condition: true},
	}
	failing, health = checkHealthSummary(snap, []string{"cert"}, true)
	if failing != 0 || health != "ok" {
		t.Fatalf("healthy condition: failing=%d health=%q, want ok", failing, health)
	}
	snap["cert"] = CheckSnapshot{OK: true, Condition: true}
	failing, health = checkHealthSummary(snap, []string{"cert"}, true)
	if failing != 1 || health != "failing" {
		t.Fatalf("firing condition: failing=%d health=%q, want failing", failing, health)
	}

	failing, health = checkHealthSummary(nil, []string{"http"}, true)
	if failing != 0 || health != "unknown" {
		t.Fatalf("no snapshot: failing=%d health=%q, want unknown", failing, health)
	}

	failing, health = checkHealthSummary(nil, []string{"http"}, false)
	if failing != 0 || health != "paused" {
		t.Fatalf("paused: failing=%d health=%q, want paused", failing, health)
	}

	failing, health = checkHealthSummary(map[string]CheckSnapshot{}, []string{"http"}, true)
	if failing != 0 || health != "unknown" {
		t.Fatalf("no observed checks: failing=%d health=%q, want unknown", failing, health)
	}
}

func TestWebBackendViewCheckHealth(t *testing.T) {
	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: true},
		"tcp":  {Check: "tcp", OK: false},
	}, map[string]bool{"http": true, "tcp": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames: []string{"http", "tcp"},
				status:     func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots: snaps,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.CheckHealth != "failing" || svc.ChecksFailing != 1 || svc.State != TargetStateFailed {
		t.Fatalf("service = %+v, want failing with 1", svc)
	}
}

func TestWebBackendViewCheckHealthPaused(t *testing.T) {
	at := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.now = func() time.Time { return at }
	if err := store.SetActive("web", false, "cli"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: false},
	}, map[string]bool{"http": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {checkNames: []string{"http"}},
		},
		store:     store,
		snapshots: snaps,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.CheckHealth != "paused" || svc.ChecksFailing != 0 || svc.State != TargetStateStopped {
		t.Fatalf("paused service = %+v, want check_health=paused", svc)
	}
}

func TestWebBackendServiceStateStartupCollectingMonitored(t *testing.T) {
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("web")})
	observability := NewObservabilityRegistry()
	snaps := NewSnapshots()
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames:        []string{"http"},
				noResidentProcess: true,
				status:            func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots:     snaps,
		settling:      settling,
		observability: observability,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateStarting || svc.ObservabilityReady || len(svc.ObservabilityMissing) == 0 {
		t.Fatalf("starting service = %+v, want starting with missing observability", svc)
	}

	settling.MarkObserved(SettlingServiceKey("web"))
	svc = b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateCollecting || svc.ObservabilityReady {
		t.Fatalf("collecting service without snapshots = %+v, want collecting", svc)
	}

	snaps.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: true},
	}, map[string]bool{"http": true})
	svc = b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateCollecting || svc.ObservabilityReady {
		t.Fatalf("collecting service without availability history = %+v, want collecting", svc)
	}

	observability.MarkReady("web", time.Now())
	svc = b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateMonitored || !svc.ObservabilityReady || len(svc.ObservabilityMissing) != 0 {
		t.Fatalf("monitored service = %+v, want monitored with observability ready", svc)
	}
}
