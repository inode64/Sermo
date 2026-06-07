package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

func TestWebBackendEventsNilLog(t *testing.T) {
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
	}
	if got := b.Events(context.Background(), 10); got != nil {
		t.Fatalf("Events with nil log = %v, want nil", got)
	}
	events, ok := b.ServiceEvents(context.Background(), "web", 10)
	if !ok {
		t.Fatal("ServiceEvents should find configured service")
	}
	if events != nil {
		t.Fatalf("ServiceEvents with nil log = %v, want nil", events)
	}
	if _, ok := b.ServiceEvents(context.Background(), "missing", 10); ok {
		t.Fatal("unknown service must not be found")
	}
}

func TestWebBackendDetailRanFlag(t *testing.T) {
	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true, Message: "ok"},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				displayName: "web",
				checkNames:  []string{"fast", "slow"},
				checkTypes:  map[string]string{"fast": "tcp", "slow": "http"},
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots: snaps,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]bool{}
	for _, c := range detail.Checks {
		byName[c.Name] = c.Ran
	}
	if !byName["fast"] {
		t.Fatal("fast check should show ran=true in web detail")
	}
	if byName["slow"] {
		t.Fatal("interval-cached slow check should show ran=false in web detail")
	}
}

func TestWebBackendDetailAtTimestamp(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 12, 30, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return t0 }
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true},
	}, map[string]bool{"fast": true, "slow": true})

	snaps.now = func() time.Time { return t1 }
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames: []string{"fast", "slow", "new"},
				checkTypes: map[string]string{"fast": "tcp", "slow": "http", "new": "command"},
			},
		},
		snapshots: snaps,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]struct {
		ran bool
		at  string
	}{}
	for _, c := range detail.Checks {
		byName[c.Name] = struct {
			ran bool
			at  string
		}{c.Ran, c.At}
	}
	wantT1 := t1.UTC().Format(time.RFC3339)
	wantT0 := t0.UTC().Format(time.RFC3339)
	if !byName["fast"].ran || byName["fast"].at != wantT1 {
		t.Fatalf("fast = %+v, want ran=true at=%s", byName["fast"], wantT1)
	}
	if byName["slow"].ran || byName["slow"].at != wantT0 {
		t.Fatalf("cached slow = %+v, want ran=false at=%s", byName["slow"], wantT0)
	}
	if byName["new"].ran || byName["new"].at != "" {
		t.Fatalf("unobserved new = %+v", byName["new"])
	}
}