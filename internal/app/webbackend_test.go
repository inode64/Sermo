package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	web "sermo/internal/web"
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

func TestWebBackendViewMonitorSource(t *testing.T) {
	at := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.now = func() time.Time { return at }
	if err := store.SetActive("web", false, state.SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {unit: "nginx", backend: "systemd"},
		},
		store: store,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.Monitored || svc.MonitorSource != state.SourceCLI {
		t.Fatalf("service = %+v", svc)
	}
	wantAt := at.UTC().Format(time.RFC3339)
	if svc.MonitorChangedAt != wantAt {
		t.Fatalf("MonitorChangedAt = %q, want %q", svc.MonitorChangedAt, wantAt)
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

func TestWebBackendIncludesDisabledServices(t *testing.T) {
	// NewWebBackend (and thus the web list) must include services that have
	// `enabled: false` so they are visible in the dashboard for the operator
	// to know they exist and can be activated (by editing config + reload).
	b := &WebBackend{
		order: []string{"mysql", "web"},
		entries: map[string]*webEntry{
			"mysql": {displayName: "MySQL", unit: "mysqld", backend: "systemd", disabled: true},
			"web":   {displayName: "Web", unit: "nginx", backend: "systemd"},
		},
	}

	svcs := b.Services(context.Background())
	if len(svcs) != 2 {
		t.Fatalf("got %d services, want 2 (including the disabled one)", len(svcs))
	}
	byName := map[string]web.Service{}
	for _, s := range svcs {
		byName[s.Name] = s
	}

	dis := byName["mysql"]
	if dis.Enabled || dis.Status != "disabled" || dis.Monitored {
		t.Fatalf("disabled service = %+v, want Enabled=false, Status=disabled, Monitored=false", dis)
	}

	en := byName["web"]
	if !en.Enabled || en.Status == "disabled" {
		t.Fatalf("normal service = %+v, want Enabled=true", en)
	}

	// Detail for disabled should succeed (returns basic info) but have no checks etc.
	d, ok := b.Detail(context.Background(), "mysql")
	if !ok || d.Name != "mysql" || d.Enabled {
		t.Fatalf("detail for disabled: ok=%v d=%+v", ok, d)
	}
	if len(d.Checks) != 0 {
		t.Fatalf("disabled detail should not expose checks")
	}

	// Operate must be rejected for disabled
	res := b.Operate(context.Background(), "mysql", "restart")
	if res.OK {
		t.Fatal("operate on disabled must fail")
	}
	if res.Message == "" || !strings.Contains(res.Message, "disabled") {
		t.Fatalf("operate error message should mention disabled: %q", res.Message)
	}

	// Preflight on disabled should not be found (no engine)
	if _, ok := b.Preflight(context.Background(), "mysql"); ok {
		t.Fatal("preflight on disabled should not succeed")
	}
}