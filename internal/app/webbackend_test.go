package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/volume"
	web "sermo/internal/web"
)

type fakeSLAReader struct {
	service map[string][]state.SLAValue
	check   map[string][]state.SLAValue
}

func (f fakeSLAReader) SLAReport(service string, _ time.Time) ([]state.SLAValue, error) {
	return f.service[service], nil
}

func (f fakeSLAReader) SLASeries(string, time.Time, time.Time) ([]state.SLAPoint, error) {
	return nil, nil
}

func (f fakeSLAReader) CheckSLAReport(service, check string, _ time.Time) ([]state.SLAValue, error) {
	return f.check[service+"\x00"+check], nil
}

func (f fakeSLAReader) CheckSLASeries(string, string, time.Time, time.Time) ([]state.SLAPoint, error) {
	return nil, nil
}

func (f fakeSLAReader) SLATimelines(service string, _ time.Time) ([]state.SLAWindowTimeline, error) {
	return slaValuesToTimelines(f.service[service]), nil
}

func (f fakeSLAReader) CheckSLATimelines(service, check string, _ time.Time) ([]state.SLAWindowTimeline, error) {
	return slaValuesToTimelines(f.check[service+"\x00"+check]), nil
}

// slaValuesToTimelines reuses the fake's window totals as timelines without
// segments, so existing SLA ratio assertions hold against the timeline path.
func slaValuesToTimelines(vals []state.SLAValue) []state.SLAWindowTimeline {
	out := make([]state.SLAWindowTimeline, 0, len(vals))
	for _, v := range vals {
		out = append(out, state.SLAWindowTimeline{Window: v.Window, Up: v.Up, Total: v.Total})
	}
	return out
}

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

func TestWebBackendEventPageFiltersAndContinuesByID(t *testing.T) {
	events := NewEventLog(10)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	events.now = func() time.Time { return now.Add(-2 * time.Hour) }
	events.Add(Event{Service: "web", Kind: eventKindError, Status: eventStatusFailed, Message: "old failed"})
	events.now = func() time.Time { return now.Add(-30 * time.Minute) }
	events.Add(Event{Service: "db", Kind: eventKindError, Status: eventStatusFailed, Message: "db failed"})
	events.Add(Event{Service: "web", Kind: eventKindError, Status: eventStatusFailed, Message: "web failed"})
	events.Add(Event{Service: "web", Kind: eventKindAction, Status: eventStatusOK, Message: "new ok"})

	b := &WebBackend{events: events, now: func() time.Time { return now }}
	first := b.EventPage(context.Background(), web.EventQuery{Limit: 1, Since: time.Hour, Service: "web", OnlyErrors: true})
	if len(first.Events) != 1 || first.Events[0].Message != "web failed" || first.Events[0].ID <= 0 {
		t.Fatalf("first page = %+v, want web failure with stable ID", first)
	}
	if !first.HasMore || first.NextBeforeID != first.Events[0].ID {
		t.Fatalf("first cursor = %+v, want continuation after returned event", first)
	}
	second := b.EventPage(context.Background(), web.EventQuery{
		BeforeID: first.NextBeforeID, Limit: 10, Since: time.Hour, Service: "web", OnlyErrors: true,
	})
	if len(second.Events) != 0 || second.HasMore || second.NextBeforeID != 0 {
		t.Fatalf("second page = %+v, want exhausted filtered feed", second)
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

func TestWebBackendDetailCheckReadings(t *testing.T) {
	snap := NewSnapshots()
	snap.Publish("web", map[string]checks.Result{
		"tls": {
			Check: "tls", OK: true, Message: "valid",
			Data: map[string]any{
				"source": "/etc/ssl/cert.pem", "days_left": 45, "not_after": "2026-08-01T00:00:00Z",
				"issuer": "Test CA",
			},
		},
		"fw": {
			Check: "fw", OK: true, Message: "ok",
			Data: map[string]any{"backend": "nftables", "rules": uint64(10), "min_rules": 1},
		},
	}, map[string]bool{"tls": true, "fw": true})
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				displayName: "web",
				checkNames:  []string{"tls", "fw"},
				checkTypes:  map[string]string{"tls": "cert", "fw": "firewall_rules"},
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots: snap,
	}
	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]web.Check{}
	for _, ch := range detail.Checks {
		byName[ch.Name] = ch
	}
	if got := readingByField(byName["tls"].Readings, "days_left").Value; got != "45" {
		t.Fatalf("tls readings = %+v", byName["tls"].Readings)
	}
	if got := readingByField(byName["fw"].Readings, "rules").Value; got != "10" {
		t.Fatalf("fw readings = %+v", byName["fw"].Readings)
	}
}

func TestWebBackendDetailIncludesCheckSLA(t *testing.T) {
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				displayName: "web",
				checkNames:  []string{"http"},
				checkTypes:  map[string]string{"http": "http"},
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		sla: fakeSLAReader{
			service: map[string][]state.SLAValue{"web": {{Window: "hour", Up: 9, Total: 10}}},
			check:   map[string][]state.SLAValue{"web\x00http": {{Window: "hour", Up: 3, Total: 4}}},
		},
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.SLA) != 1 || detail.SLA[0].Ratio == nil || *detail.SLA[0].Ratio != 0.9 {
		t.Fatalf("service SLA = %+v, want 90%%", detail.SLA)
	}
	if len(detail.Checks) != 1 || len(detail.Checks[0].SLA) != 1 ||
		detail.Checks[0].SLA[0].Ratio == nil || *detail.Checks[0].SLA[0].Ratio != 0.75 {
		t.Fatalf("check SLA = %+v, want 75%%", detail.Checks)
	}
}

func TestWebBackendApplicationsIncludeServiceSLA(t *testing.T) {
	b := &WebBackend{
		entries: map[string]*webEntry{"nginx": {}},
		sla: fakeSLAReader{
			service: map[string][]state.SLAValue{"nginx": {{Window: "day", Up: 99, Total: 100}}},
		},
		applications: catalogInventoryCache{list: func(context.Context) []web.CatalogItem {
			return []web.Application{{Name: "nginx", Status: appinspect.StatusOK}, {Name: "orphan", Status: appinspect.StatusOK}}
		}},
	}

	apps := b.Applications(context.Background())
	if len(apps) != 2 {
		t.Fatalf("apps = %+v", apps)
	}
	if len(apps[0].SLA) != 1 || apps[0].SLA[0].Ratio == nil || *apps[0].SLA[0].Ratio != 0.99 {
		t.Fatalf("nginx SLA = %+v, want 99%%", apps[0].SLA)
	}
	if len(apps[1].SLA) != 0 {
		t.Fatalf("orphan SLA = %+v, want none", apps[1].SLA)
	}
}

func TestWebBackendApplicationsIncludeLastEvent(t *testing.T) {
	events := NewEventLog(10)
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	events.now = func() time.Time { return t0 }
	events.Add(Event{App: "nginx", Kind: eventKindFiring, Message: "version changed"})
	events.now = func() time.Time { return t0.Add(time.Minute) }
	events.Add(Event{App: "nginx", Kind: eventKindRecovered, Message: "ok"})

	b := &WebBackend{
		events: events,
		applications: catalogInventoryCache{list: func(context.Context) []web.CatalogItem {
			return []web.Application{{Name: "nginx", Status: appinspect.StatusOK}, {Name: "orphan", Status: appinspect.StatusOK}}
		}},
	}

	apps := b.Applications(context.Background())
	if len(apps) != 2 {
		t.Fatalf("apps = %+v", apps)
	}
	if apps[0].LastEvent == nil || apps[0].LastEvent.Kind != eventKindRecovered || apps[0].LastEvent.Message != "ok" {
		t.Fatalf("nginx LastEvent = %+v, want recovered ok", apps[0].LastEvent)
	}
	if apps[1].LastEvent != nil {
		t.Fatalf("orphan LastEvent = %+v, want nil", apps[1].LastEvent)
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
			"web": {unit: "nginx", backend: string(servicemgr.BackendSystemd)},
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

func TestWebBackendViewInterval(t *testing.T) {
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {unit: "nginx", backend: string(servicemgr.BackendSystemd), interval: 10 * time.Second},
		},
	}
	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.Interval != "10s" {
		t.Fatalf("Interval = %q, want %q", svc.Interval, "10s")
	}
}

func TestWebBackendMonitoringStatusAvoidsServiceViewWork(t *testing.T) {
	store := newFakeStore()
	if err := store.SetActive("paused", false, state.SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	statusCalls := 0
	b := &WebBackend{
		order: []string{"active", "paused", "disabled"},
		entries: map[string]*webEntry{
			"active": {
				status: func(context.Context) (servicemgr.Status, error) {
					statusCalls++
					return servicemgr.StatusActive, nil
				},
			},
			"paused": {
				status: func(context.Context) (servicemgr.Status, error) {
					statusCalls++
					return servicemgr.StatusActive, nil
				},
			},
			"disabled": {disabled: true},
		},
		store: store,
	}

	got := b.MonitoringStatus(context.Background())
	if got.Total != 2 || got.Monitored != 1 || got.Paused != 1 {
		t.Fatalf("MonitoringStatus = %+v, want total=2 monitored=1 paused=1", got)
	}
	if statusCalls != 0 {
		t.Fatalf("MonitoringStatus called service status %d times, want 0", statusCalls)
	}
}

func TestWebBackendStatusCacheIgnoresCancelledRequests(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	e := &webEntry{status: func(ctx context.Context) (servicemgr.Status, error) {
		calls++
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return servicemgr.StatusActive, nil
	}}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if got := e.backendStatus(cancelled, now); got != string(servicemgr.StatusUnknown) {
		t.Fatalf("cold cancelled status = %q, want unknown", got)
	}
	if !e.statusAt.IsZero() {
		t.Fatalf("cancelled request populated statusAt = %v, want zero", e.statusAt)
	}

	if got := e.backendStatus(context.Background(), now); got != string(servicemgr.StatusActive) {
		t.Fatalf("status = %q, want active", got)
	}

	// A cancelled probe after TTL expiry must serve the previous entry instead
	// of caching "error" for every other viewer.
	later := now.Add(serviceStatusCacheTTL + time.Second)
	if got := e.backendStatus(cancelled, later); got != string(servicemgr.StatusActive) {
		t.Fatalf("cancelled status after expiry = %q, want cached active", got)
	}
	if got := e.backendStatus(context.Background(), later); got != string(servicemgr.StatusActive) {
		t.Fatalf("status after cancelled probe = %q, want active from a fresh probe", got)
	}
	if calls != 4 {
		t.Fatalf("status probe calls = %d, want 4", calls)
	}
}

func TestWebBackendLastEventIndexes(t *testing.T) {
	events := NewEventLog(10)
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	add := func(at time.Time, e Event) {
		events.now = func() time.Time { return at }
		events.Add(e)
	}
	add(t0, Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionStart), Status: eventStatusOK})
	add(t0.Add(time.Minute), Event{Service: "db", Kind: eventKindAction, Action: string(rules.ActionRestart), Status: eventStatusOK})
	add(t0.Add(2*time.Minute), Event{Watch: "storage-root", Kind: eventKindNotify, Message: "sent"})
	add(t0.Add(3*time.Minute), Event{Watch: "storage-root", Kind: eventKindError, Message: "ignored"})
	add(t0.Add(4*time.Minute), Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionRestart), Status: eventStatusBlocked})
	add(t0.Add(5*time.Minute), Event{Watch: "storage-root", Kind: eventKindHookFail, Message: "failed"})

	b := &WebBackend{
		events:     events,
		order:      []string{"web", "db"},
		watchOrder: []string{"storage-root"},
	}

	services := b.lastServiceEvents()
	if got := services["web"]; got == nil || got.Action != string(rules.ActionRestart) || got.Status != eventStatusBlocked {
		t.Fatalf("web last event = %+v, want restart/blocked", got)
	}
	if got := services["db"]; got == nil || got.Action != string(rules.ActionRestart) || got.Status != eventStatusOK {
		t.Fatalf("db last event = %+v, want restart/ok", got)
	}

	activities := b.lastWatchActivities()
	wantAt := t0.Add(5 * time.Minute).Format(time.RFC3339)
	if got := activities["storage-root"]; got.Kind != eventKindHookFail || got.At != wantAt {
		t.Fatalf("storage-root activity = %+v, want hook-failed at %s", got, wantAt)
	}
}

func TestWebBackendActivitySummaryCountsAllServiceOperations(t *testing.T) {
	events := NewEventLog(10)
	for _, action := range serviceOperationActionList() {
		events.Add(Event{Service: "web", Kind: eventKindAction, Action: action, Status: eventStatusOK})
	}
	events.Add(Event{Watch: "storage-root", Kind: eventKindHook, Status: eventStatusOK})
	events.Add(Event{Watch: "storage-root", Kind: eventKindNotify, Status: eventStatusOK})
	events.Add(Event{Kind: eventKindError, Message: "boom"})

	b := &WebBackend{events: events}
	got := b.ActivitySummary(context.Background())
	if got.ServiceActions != 5 {
		t.Fatalf("ServiceActions = %d, want 5 for start/stop/restart/reload/resume", got.ServiceActions)
	}
	if got.WatchHooks != 1 || got.WatchNotifies != 1 || got.Errors != 1 {
		t.Fatalf("ActivitySummary = %+v, want hook/notify/error counted", got)
	}
}

func TestWebBackendLastWatchActivityIncludesRecovered(t *testing.T) {
	events := NewEventLog(10)
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	add := func(at time.Time, e Event) {
		events.now = func() time.Time { return at }
		events.Add(e)
	}
	add(t0, Event{Watch: "uplink-dns", Kind: eventKindFiring, Message: "dns timeout"})
	add(t0.Add(time.Minute), Event{Watch: "uplink-dns", Kind: eventKindRecovered, Message: "dns ok"})

	b := &WebBackend{
		events:     events,
		watchOrder: []string{"uplink-dns"},
	}
	activities := b.lastWatchActivities()
	wantAt := t0.Add(time.Minute).Format(time.RFC3339)
	if got := activities["uplink-dns"]; got.Kind != eventKindRecovered || got.At != wantAt {
		t.Fatalf("uplink-dns activity = %+v, want recovered at %s", got, wantAt)
	}
}

func TestWatchViewFailedIgnoresActivityBeforeMonitorChange(t *testing.T) {
	tests := []struct {
		name     string
		watch    web.Watch
		wantFail bool
	}{
		{
			name: "failed activity before monitor change is stale",
			watch: web.Watch{
				LastActivityKind: eventKindFiring,
				LastActivity:     "2026-06-17T14:10:43Z",
				MonitorChangedAt: "2026-06-17T14:14:53Z",
			},
		},
		{
			name: "failed activity after monitor change is current",
			watch: web.Watch{
				LastActivityKind: eventKindFiring,
				LastActivity:     "2026-06-17T14:20:43Z",
				MonitorChangedAt: "2026-06-17T14:14:53Z",
			},
			wantFail: true,
		},
		{
			name: "bad timestamp keeps conservative failure",
			watch: web.Watch{
				LastActivityKind: eventKindFiring,
				LastActivity:     "bad-time",
				MonitorChangedAt: "2026-06-17T14:14:53Z",
			},
			wantFail: true,
		},
		{
			name: "recovered activity is not failed",
			watch: web.Watch{
				LastActivityKind: eventKindRecovered,
				LastActivity:     "2026-06-17T14:20:43Z",
				MonitorChangedAt: "2026-06-17T14:14:53Z",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := watchViewFailed(tt.watch); got != tt.wantFail {
				t.Fatalf("watchViewFailed() = %v, want %v", got, tt.wantFail)
			}
		})
	}
}

func TestWebBackendApplicationsCache(t *testing.T) {
	if catalogInventoryCacheTTL < 5*time.Minute {
		t.Fatalf("catalogInventoryCacheTTL = %s, want at least 5m to avoid frequent version probes", catalogInventoryCacheTTL)
	}

	calls := 0
	b := &WebBackend{
		applications: catalogInventoryCache{list: func(context.Context) []web.CatalogItem {
			calls++
			name := "first"
			if calls > 1 {
				name = "second"
			}
			return []web.Application{{Name: name}}
		}},
	}

	first := b.Applications(context.Background())
	if calls != 1 || len(first) != 1 || first[0].Name != "first" {
		t.Fatalf("first Applications = %v, calls=%d", first, calls)
	}
	if first[0].ObservedAt == "" {
		t.Fatal("first Applications response must expose observed_at")
	}
	first[0].Name = "mutated"

	second := b.Applications(context.Background())
	if calls != 1 || len(second) != 1 || second[0].Name != "first" {
		t.Fatalf("cached Applications = %v, calls=%d; want cached first", second, calls)
	}
	if second[0].ObservedAt != first[0].ObservedAt {
		t.Fatalf("cached observed_at = %q, want original %q", second[0].ObservedAt, first[0].ObservedAt)
	}

	b.applications.at = time.Now().Add(-catalogInventoryCacheTTL - time.Nanosecond)
	third := b.Applications(context.Background())
	if calls != 2 || len(third) != 1 || third[0].Name != "second" {
		t.Fatalf("expired Applications = %v, calls=%d; want refreshed second", third, calls)
	}
}

func TestWebBackendApplicationsCacheIgnoresCancelledRequests(t *testing.T) {
	b := &WebBackend{
		applications: catalogInventoryCache{list: func(ctx context.Context) []web.CatalogItem {
			if ctx.Err() != nil {
				// A cancelled request aborts inspection early and yields a
				// partial inventory; model that as an empty list.
				return nil
			}
			return []web.Application{{Name: "complete"}}
		}},
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if got := b.Applications(cancelled); len(got) != 0 {
		t.Fatalf("cold cancelled Applications = %v, want empty partial result", got)
	}
	if !b.applications.at.IsZero() {
		t.Fatalf("cancelled request populated inventory time = %v, want zero", b.applications.at)
	}

	if got := b.Applications(context.Background()); len(got) != 1 || got[0].Name != "complete" {
		t.Fatalf("Applications = %v, want complete inventory", got)
	}

	b.applications.at = time.Now().Add(-catalogInventoryCacheTTL - time.Nanosecond)
	if got := b.Applications(cancelled); len(got) != 1 || got[0].Name != "complete" {
		t.Fatalf("cancelled Applications after expiry = %v, want previous complete cache", got)
	}
}

func TestWebBackendApplicationsServeStaleWhileRefreshing(t *testing.T) {
	scanning := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	b := &WebBackend{
		applications: catalogInventoryCache{list: func(context.Context) []web.CatalogItem {
			if calls.Add(1) == 1 {
				close(scanning)
				<-release
			}
			return []web.Application{{Name: "fresh"}}
		}},
	}
	b.applications.items = []web.CatalogItem{{Name: "stale"}}
	b.applications.at = time.Now().Add(-catalogInventoryCacheTTL - time.Nanosecond)

	leader := make(chan []web.Application)
	go func() { leader <- b.Applications(context.Background()) }()
	<-scanning

	// While the scan holds the refresh slot, other viewers must be served the
	// expired-but-complete inventory instead of queueing behind the scan.
	if got := b.Applications(context.Background()); len(got) != 1 || got[0].Name != "stale" {
		t.Fatalf("Applications during refresh = %v, want stale cache", got)
	}
	close(release)
	if got := <-leader; len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("refreshing Applications = %v, want fresh inventory", got)
	}
	if got := b.Applications(context.Background()); len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("Applications after refresh = %v, want fresh cache", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("applicationsList calls = %d, want 1", n)
	}
}

func TestWebBackendApplicationsColdStartSingleScan(t *testing.T) {
	scanning := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	b := &WebBackend{
		applications: catalogInventoryCache{list: func(context.Context) []web.CatalogItem {
			if calls.Add(1) == 1 {
				close(scanning)
				<-release
			}
			return []web.Application{{Name: "fresh"}}
		}},
	}

	leader := make(chan []web.Application)
	go func() { leader <- b.Applications(context.Background()) }()
	<-scanning

	// A cold-start viewer has no previous inventory to serve, so it waits for
	// the running scan and shares its result rather than starting a second one.
	follower := make(chan []web.Application)
	go func() { follower <- b.Applications(context.Background()) }()

	// A cold-start viewer that goes away stops waiting instead of scanning.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := b.Applications(cancelled); got != nil {
		t.Fatalf("cancelled cold-start Applications = %v, want nil", got)
	}

	close(release)
	if got := <-leader; len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("cold-start Applications = %v, want fresh inventory", got)
	}
	if got := <-follower; len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("cold-start follower Applications = %v, want shared fresh inventory", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("applicationsList calls = %d, want 1", n)
	}
}

type concurrentAppRunner struct {
	delay    time.Duration
	mu       sync.Mutex
	inFlight int
	max      int
}

func (r *concurrentAppRunner) Run(ctx context.Context, _ string, _ ...string) (execx.Result, error) {
	r.mu.Lock()
	r.inFlight++
	if r.inFlight > r.max {
		r.max = r.inFlight
	}
	r.mu.Unlock()

	select {
	case <-ctx.Done():
	case <-time.After(r.delay):
	}

	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
	return execx.Result{Stdout: "app 1.2.3\n", ExitCode: 0}, nil
}

func (r *concurrentAppRunner) Max() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.max
}

func TestWebBackendApplicationsInspectInParallel(t *testing.T) {
	root := t.TempDir()
	names := []string{"app-a", "app-b", "app-c", "app-d"}
	cfg := &config.Config{AppNames: names, Apps: map[string]*config.Document{}}
	for _, name := range names {
		bin := filepath.Join(root, name)
		if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.Apps[name] = &config.Document{Name: name, Body: map[string]any{
			"name": name,
			"preflight": map[string]any{
				"binary": map[string]any{"type": "binary", "path": bin},
				"version": map[string]any{
					"type":    "command",
					"command": []any{bin, "--version"},
				},
			},
		}}
	}

	runner := &concurrentAppRunner{delay: 25 * time.Millisecond}
	b := &WebBackend{cfg: cfg, execRunner: runner}
	apps := b.loadApplications(context.Background())
	if len(apps) != len(names) {
		t.Fatalf("apps = %+v, want %d apps", apps, len(names))
	}
	if runner.Max() < 2 {
		t.Fatalf("max concurrent app probes = %d, want at least 2", runner.Max())
	}
}

func TestWebBackendLibrariesInspectInstalledCatalogFiles(t *testing.T) {
	root := t.TempDir()
	libraryPath := filepath.Join(root, "libdemo.so")
	if err := os.WriteFile(libraryPath, []byte("library"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		LibraryNames: []string{"libdemo"},
		Libraries: map[string]*config.Document{
			"libdemo": {Name: "libdemo", Body: map[string]any{
				"name":         "libdemo",
				"display_name": "Demo library",
				"category":     "runtime",
				"preflight": map[string]any{
					"file": map[string]any{"type": "file", "path": libraryPath},
				},
			}},
		},
	}
	b := &WebBackend{cfg: cfg}
	libraries := b.Libraries(context.Background())
	if len(libraries) != 1 {
		t.Fatalf("Libraries = %+v, want one installed library", libraries)
	}
	got := libraries[0]
	if got.Name != "libdemo" || got.DisplayName != "Demo library" || got.Category != "runtime" || got.Binary != libraryPath {
		t.Fatalf("library = %+v, want resolved installed catalog library", got)
	}
	if got.ObservedAt == "" {
		t.Fatal("library inventory must expose its probe observation time")
	}
}

func TestWebBackendWatchPolarityUsesSharedHealthTypes(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"autofs": map[string]any{"check": map[string]any{"type": "autofs"}},
			"count":  map[string]any{"check": map[string]any{"type": "count"}},
			"mysql":  map[string]any{"check": map[string]any{"type": "mysql"}},
			"ports":  map[string]any{"check": map[string]any{"type": "ports"}},
			"ws":     map[string]any{"check": map[string]any{"type": "websocket"}},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	got := map[string]bool{}
	for _, w := range b.Watches(context.Background()) {
		got[w.Name] = w.FireOnFail
	}
	for _, name := range []string{"autofs", "mysql", "ports", "ws"} {
		if !got[name] {
			t.Fatalf("%s watch should be health-style: %v", name, got)
		}
	}
	if got["count"] {
		t.Fatalf("count watch should be condition-style: %v", got)
	}
}

func TestWebBackendWatchesExposeMonitorMode(t *testing.T) {
	store := newFakeStore()
	if err := store.SetActive(watchMonitorKey("storage-root"), false, state.SourceConfig); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-root": map[string]any{
				"display_name": "Root disk",
				"category":     "storage",
				"monitor":      config.MonitorDisabled,
				"check":        map[string]any{"type": "storage", "path": "/"},
			},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{Monitor: store})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches", len(watches))
	}
	if watches[0].DisplayName != "Root disk" || watches[0].Category != "storage" || watches[0].Monitor != config.MonitorDisabled || watches[0].Monitored {
		t.Fatalf("watch monitor view = %+v", watches[0])
	}
}

func TestWebBackendKernelWatchReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"mem-pressure": map[string]any{"check": map[string]any{
				"type":       "pressure",
				"resource":   "memory",
				"some_avg60": map[string]any{"op": ">", "value": 10},
			}},
			"entropy": map[string]any{"check": map[string]any{
				"type":  "entropy",
				"avail": map[string]any{"op": "<", "value": 200},
			}},
			"zombies": map[string]any{"check": map[string]any{
				"type":  "zombies",
				"count": map[string]any{"op": ">", "value": 20},
			}},
			"conntrack": map[string]any{"check": map[string]any{
				"type":  "conntrack",
				"count": map[string]any{"op": ">", "value": 100},
			}},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{
		PressureSampler: func(resource string) (checks.PressureSample, error) {
			if resource != "memory" {
				t.Fatalf("pressure resource = %q, want memory", resource)
			}
			return checks.PressureSample{
				Some: checks.PressureAverages{Avg10: 1.25, Avg60: 2.5, Avg300: 3.75},
				Full: checks.PressureAverages{Avg10: 0.5, Avg60: 0.75, Avg300: 1},
			}, nil
		},
		ConntrackSampler: func() (checks.ConntrackSample, error) {
			return checks.ConntrackSample{Count: 25, Max: 100}, nil
		},
		EntropySampler: func() (uint64, bool) { return 123, true },
		ZombieSampler:  func() (uint64, bool) { return 4, true },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}

	pressure := byName["mem-pressure"]
	if !strings.Contains(pressure.Summary, "pressure memory") || pressure.State != TargetStateOK {
		t.Fatalf("pressure watch = %+v", pressure)
	}
	if got := readingByField(pressure.Readings, "some_avg60").Value; got != "2.50%" {
		t.Fatalf("pressure some_avg60 reading = %q, want 2.50%%", got)
	}
	if got := conditionByField(pressure.Conditions, "some_avg60").Value; got != "10" {
		t.Fatalf("pressure condition = %q, want 10", got)
	}

	entropy := byName["entropy"]
	if got := readingByField(entropy.Readings, "avail").Value; got != "123 bits" {
		t.Fatalf("entropy reading = %q, want 123 bits", got)
	}
	if conditionByField(entropy.Conditions, "avail").Op != "<" {
		t.Fatalf("entropy conditions = %+v", entropy.Conditions)
	}

	zombies := byName["zombies"]
	if got := readingByField(zombies.Readings, "count").Value; got != "4" {
		t.Fatalf("zombies reading = %q, want 4", got)
	}
	if conditionByField(zombies.Conditions, "count").Op != ">" {
		t.Fatalf("zombies conditions = %+v", zombies.Conditions)
	}

	conntrack := byName["conntrack"]
	if conntrack.Meter == nil || conntrack.Meter.Kind != "conntrack" || conntrack.Meter.Count != 25 || conntrack.Meter.Max != 100 {
		t.Fatalf("conntrack meter = %+v", conntrack.Meter)
	}
	if conditionByField(conntrack.Conditions, "count").Value != "100" {
		t.Fatalf("conntrack conditions = %+v", conntrack.Conditions)
	}
}

func TestWebBackendKernelWatchReadingErrorMarksWatchFailed(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"mem-pressure": map[string]any{"check": map[string]any{
				"type":       "pressure",
				"resource":   "memory",
				"some_avg60": map[string]any{"op": ">", "value": 10},
			}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		PressureSampler: func(string) (checks.PressureSample, error) {
			return checks.PressureSample{}, errors.New("PSI disabled")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if w.State != TargetStateFailed || len(w.Readings) != 1 || w.Readings[0].Error != "PSI disabled" {
		t.Fatalf("watch = %+v, want failed with PSI error reading", w)
	}
}

func TestWebBackendOomNetICMPAndPidsReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"oom": map[string]any{"check": map[string]any{"type": "oom"}},
			"pid-table": map[string]any{"check": map[string]any{
				"type":     "pids",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			}},
			"fd-table": map[string]any{"check": map[string]any{
				"type":     "fds",
				"used_pct": map[string]any{"op": ">=", "value": 80},
			}},
			"net-eth0": map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change"},
					"errors":  map[string]any{"delta": map[string]any{"op": ">", "value": 10}},
					"address": map[string]any{"expect": "present"},
					"speed":   map[string]any{"on": "change"},
				},
			},
			"ping-gw": map[string]any{
				"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change"},
					"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}},
				},
			},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{
		OomSampler:  func() (uint64, bool) { return 7, true },
		FdsSampler:  func() (checks.FdsSample, error) { return checks.FdsSample{Allocated: 8500, Max: 10000}, nil },
		PidsSampler: func() (checks.PidsSample, error) { return checks.PidsSample{Threads: 123, Max: 1000}, nil },
		NetSampler: func(iface string) (checks.NetSample, error) {
			if iface != "eth0" {
				t.Fatalf("net iface = %q, want eth0", iface)
			}
			return checks.NetSample{
				State:      "up",
				SpeedMbps:  1000,
				SpeedKnown: true,
				Counters:   map[string]uint64{"rx_errors": 2, "tx_errors": 3},
				Addrs:      []string{"192.0.2.10"},
			}, nil
		},
		PingSampler: func(host, iface string, count int, timeout time.Duration) (checks.PingSample, error) {
			if host != "8.8.8.8" || iface != "" || count != 3 {
				t.Fatalf("ping args host=%q iface=%q count=%d", host, iface, count)
			}
			if timeout <= 0 {
				t.Fatal("ping timeout must be bounded")
			}
			return checks.PingSample{Reachable: true, RTTms: 42.5, RTTKnown: true}, nil
		},
		DefaultTimeout: time.Second,
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}

	oom := byName["oom"]
	if got := readingByField(oom.Readings, "total").Value; got != "7" {
		t.Fatalf("oom reading = %q, want 7", got)
	}
	if conditionByField(oom.Conditions, "delta").Op != ">" {
		t.Fatalf("oom default condition = %+v", oom.Conditions)
	}

	pids := byName["pid-table"]
	if pids.Meter == nil || pids.Meter.Kind != "pids" || pids.Meter.Count != 123 || pids.Meter.Max != 1000 {
		t.Fatalf("pids meter = %+v", pids.Meter)
	}
	if !strings.Contains(pids.Summary, "123/1000") {
		t.Fatalf("pids summary = %q", pids.Summary)
	}

	fds := byName["fd-table"]
	if fds.Meter == nil || fds.Meter.Kind != "fds" || fds.Meter.Count != 8500 || fds.Meter.Max != 10000 {
		t.Fatalf("fds meter = %+v", fds.Meter)
	}
	if !strings.Contains(fds.Summary, "8500/10000") {
		t.Fatalf("fds summary = %q", fds.Summary)
	}

	netWatch := byName["net-eth0"]
	if got := readingByField(netWatch.Readings, "state").Value; got != "up" {
		t.Fatalf("net state reading = %q, want up", got)
	}
	if got := readingByField(netWatch.Readings, "speed").Value; got != "1000 Mbps" {
		t.Fatalf("net speed reading = %q, want 1000 Mbps", got)
	}
	if got := readingByField(netWatch.Readings, "errors").Value; got != "5" {
		t.Fatalf("net errors reading = %q, want 5", got)
	}
	if got := conditionByField(netWatch.Conditions, "errors.delta").Value; got != "10" {
		t.Fatalf("net conditions = %+v", netWatch.Conditions)
	}

	icmp := byName["ping-gw"]
	if got := readingByField(icmp.Readings, "state").Value; got != "up" {
		t.Fatalf("icmp state reading = %q, want up", got)
	}
	if got := readingByField(icmp.Readings, "latency").Value; got != "42.5 ms" {
		t.Fatalf("icmp latency reading = %q, want 42.5 ms", got)
	}
	if got := conditionByField(icmp.Conditions, "latency.threshold").Value; got != "100" {
		t.Fatalf("icmp conditions = %+v", icmp.Conditions)
	}
}

func TestWebBackendProcessWatchReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"hot-workers": map[string]any{
				"check": map[string]any{
					"type":   "process",
					"name":   "apache2",
					"user":   "apache",
					"for":    "5m",
					"cpu":    map[string]any{"op": ">", "value": 80},
					"memory": map[string]any{"op": ">", "value": 524288000},
					"io":     map[string]any{"op": ">", "value": 10485760},
					"gone":   true,
				},
			},
		},
	}}}
	sampler := &webProcSampler{samples: []ProcInfo{
		{PID: 42, CPUTicks: 20, RSS: 100, IOBytes: 500, HasIO: true},
		{PID: 7, CPUTicks: 30, RSS: 200},
	}}
	b, warns := NewWebBackend(cfg, Deps{ProcSampler: sampler})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if sampler.match != (ProcMatch{Name: "apache2", User: "apache"}) {
		t.Fatalf("process sample match = %+v", sampler.match)
	}
	if !strings.Contains(w.Summary, "2 matching processes") || !strings.Contains(w.Summary, "rss 300 bytes") {
		t.Fatalf("process summary = %q", w.Summary)
	}
	if got := readingByField(w.Readings, "pids").Value; got != "7, 42" {
		t.Fatalf("process pids reading = %q, want sorted pids", got)
	}
	if got := readingByField(w.Readings, "rss").Value; got != "300 bytes" {
		t.Fatalf("process rss reading = %q, want 300 bytes", got)
	}
	if got := readingByField(w.Readings, "cpu_ticks").Value; got != "50" {
		t.Fatalf("process cpu ticks reading = %q, want 50", got)
	}
	if got := readingByField(w.Readings, "io").Value; got != "500 bytes" {
		t.Fatalf("process io reading = %q, want 500 bytes", got)
	}
	if conditionByField(w.Conditions, "for").Value != "5m" ||
		conditionByField(w.Conditions, "gone").Value != "true" ||
		conditionByField(w.Conditions, "cpu").Op != ">" ||
		conditionByField(w.Conditions, "memory").Value != "524288000" ||
		conditionByField(w.Conditions, "io").Value != "10485760" {
		t.Fatalf("process conditions = %+v", w.Conditions)
	}
}

func TestWebBackendAdditionalHostWatchReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"autofs-net": map[string]any{"check": map[string]any{
				"type": "autofs",
				"path": "/net",
			}},
			"diskio-root": map[string]any{"check": map[string]any{
				"type":     "diskio",
				"device":   "sda",
				"util_pct": map[string]any{"op": ">", "value": 80},
			}},
			"edac": map[string]any{"check": map[string]any{
				"type": "edac",
				"ue":   map[string]any{"op": ">", "value": 0},
			}},
			"raid": map[string]any{"check": map[string]any{
				"type":     "raid",
				"degraded": map[string]any{"op": ">", "value": 0},
			}},
			"route-wan": map[string]any{"check": map[string]any{
				"type":      "route",
				"family":    "ipv4",
				"interface": "ppp0",
			}},
			"sensors": map[string]any{"check": map[string]any{
				"type": "sensors",
				"temp": map[string]any{"op": ">", "value": 70},
			}},
		},
	}}}
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	storageSamples := []checks.DiskIOSample{
		{ReadsCompleted: 10, SectorsRead: 100, ReadTicksMs: 100, WritesCompleted: 10, SectorsWritten: 200, WriteTicksMs: 100, IOTicksMs: 1000},
		{ReadsCompleted: 20, SectorsRead: 102, ReadTicksMs: 130, WritesCompleted: 20, SectorsWritten: 204, WriteTicksMs: 120, IOTicksMs: 1500},
	}
	storageCalls := 0
	b, warns := NewWebBackend(cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/net", FSType: "autofs"}}, nil
		},
		DiskIOSampler: func(device string) (checks.DiskIOSample, error) {
			if device != "sda" {
				t.Fatalf("diskio device = %q, want sda", device)
			}
			sample := storageSamples[min(storageCalls, len(storageSamples)-1)]
			storageCalls++
			return sample, nil
		},
		EdacSampler: func() (checks.EdacCounts, error) {
			return checks.EdacCounts{CE: 2, UE: 1, Present: true}, nil
		},
		RaidSampler: func() (checks.RaidStatus, error) {
			return checks.RaidStatus{Arrays: 2, Degraded: 1, Recovering: 1, DegradedNames: []string{"md0"}}, nil
		},
		RouteSampler: func(family string) ([]checks.DefaultRoute, error) {
			if family != "ipv4" {
				t.Fatalf("route family = %q, want ipv4", family)
			}
			return []checks.DefaultRoute{{Iface: "ppp0", Gateway: "192.0.2.1"}}, nil
		},
		SensorSampler: func() ([]checks.SensorReading, error) {
			return []checks.SensorReading{
				{Chip: "coretemp", Kind: "temp", Label: "Package", Value: 82.5},
				{Chip: "nct", Kind: "fan", Label: "fan1", Value: 900},
				{Chip: "nct", Kind: "in", Label: "12V", Value: 11.9},
			}, nil
		},
		Now: func() time.Time { return now },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	_ = b.Watches(context.Background()) // primes diskio's rate baseline.
	now = now.Add(time.Second)
	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}

	autofs := byName["autofs-net"]
	if got := readingByField(autofs.Readings, "mountpoints").Value; got != "/net" {
		t.Fatalf("autofs mountpoints = %q, want /net", got)
	}
	if !strings.Contains(autofs.Summary, "/net active") {
		t.Fatalf("autofs summary = %q", autofs.Summary)
	}

	diskio := byName["diskio-root"]
	if got := readingByField(diskio.Readings, "util_pct").Value; got != "50.00%" {
		t.Fatalf("diskio util = %q, want 50.00%%", got)
	}
	if got := readingByField(diskio.Readings, "read_bytes").Value; got != "1024 B/s" {
		t.Fatalf("diskio read = %q, want 1024 B/s", got)
	}

	// A second viewer polling inside diskIORateMinWindow must not re-base the
	// shared delta baseline over a near-zero window; it re-serves the rates
	// computed for the previous poll.
	now = now.Add(200 * time.Millisecond)
	quick := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		quick[w.Name] = w
	}
	if got := readingByField(quick["diskio-root"].Readings, "util_pct").Value; got != "50.00%" {
		t.Fatalf("diskio util on quick re-poll = %q, want cached 50.00%%", got)
	}
	if got := readingByField(quick["diskio-root"].Readings, "read_bytes").Value; got != "1024 B/s" {
		t.Fatalf("diskio read on quick re-poll = %q, want cached 1024 B/s", got)
	}

	edac := byName["edac"]
	if got := readingByField(edac.Readings, "ue").Value; got != "1" {
		t.Fatalf("edac ue = %q, want 1", got)
	}
	if conditionByField(edac.Conditions, "ue").Op != ">" {
		t.Fatalf("edac conditions = %+v", edac.Conditions)
	}

	raid := byName["raid"]
	if got := readingByField(raid.Readings, "degraded_arrays").Value; got != "md0" {
		t.Fatalf("raid degraded arrays = %q, want md0", got)
	}

	route := byName["route-wan"]
	if got := readingByField(route.Readings, "gateway").Value; got != "192.0.2.1" {
		t.Fatalf("route gateway = %q, want 192.0.2.1", got)
	}
	if conditionByField(route.Conditions, "interface").Value != "ppp0" {
		t.Fatalf("route conditions = %+v", route.Conditions)
	}

	sensors := byName["sensors"]
	if got := readingByField(sensors.Readings, "temp").Value; got != "82.5 C" {
		t.Fatalf("sensors temp = %q, want 82.5 C", got)
	}
	if got := readingByField(sensors.Readings, "fan").Value; got != "900 RPM" {
		t.Fatalf("sensors fan = %q, want 900 RPM", got)
	}
}

func TestWebBackendStatefulWatchReadings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		config.SectionWatches: map[string]any{
			"cfg-file": map[string]any{config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeFile,
				checks.CheckKeyPath: filepath.Join(dir, "a.txt"),
			}},
			"entry-count": map[string]any{config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeCount,
				checks.CheckKeyPath: dir,
				checks.CheckKeyOf:   checks.CountKindFile,
			}},
			"fw": map[string]any{config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeFirewallRules,
			}},
			"grow": map[string]any{config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType:   checks.CheckTypeSize,
				checks.CheckKeyPath:   filepath.Join(dir, "a.txt"),
				checks.CheckKeyGrowBy: "1M",
				checks.CheckKeyWithin: "1h",
			}},
			"disk-speed": map[string]any{config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType:   checks.CheckTypeHdparm,
				checks.CheckKeyDevice: "/dev/sda",
				checks.HdparmFieldRead: map[string]any{
					checks.CheckKeyOp:    ">",
					checks.CheckKeyValue: 50,
				},
			}},
			"disk-health": map[string]any{config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType:   checks.CheckTypeSmart,
				checks.CheckKeyDevice: "/dev/sda",
			}},
		},
	}}}
	hdparmOut := " Timing buffered disk reads: 1 GB in 2.00 seconds = 500.00 MB/sec\n"
	smartOut := `{"smart_status":{"passed":true},"temperature":{"current":41},"power_on_time":{"hours":1000}}`
	b, warns := NewWebBackend(cfg, Deps{
		DefaultTimeout: 5 * time.Second,
		FirewallRulesSampler: func(context.Context, string, execx.Runner) (checks.FirewallRulesSample, error) {
			return checks.FirewallRulesSample{Backend: checks.FirewallBackendNftables, Rules: 42}, nil
		},
		ExecxRunner: webBackendTestRunner{byCommand: map[string]execx.Result{
			"hdparm":   {Stdout: hdparmOut},
			"smartctl": {Stdout: smartOut},
		}},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}
	if got := readingByField(byName["cfg-file"].Readings, "kind").Value; got != "file" {
		t.Fatalf("file kind = %q, want file", got)
	}
	if got := readingByField(byName["entry-count"].Readings, "count").Value; got != "1" {
		t.Fatalf("count = %q, want 1", got)
	}
	if got := readingByField(byName["fw"].Readings, "rules").Value; got != "42" {
		t.Fatalf("firewall rules = %q, want 42", got)
	}
	if got := readingByField(byName["grow"].Readings, "current_bytes").Value; got != "5 B" {
		t.Fatalf("size = %q, want 5 B", got)
	}
	if got := readingByField(byName["disk-speed"].Readings, "read").Value; got != "" {
		t.Fatalf("hdparm read = %q, want no cold web probe", got)
	}
	if got := readingByField(byName["disk-health"].Readings, "health").Value; got != "" {
		t.Fatalf("smart health = %q, want no cold web probe", got)
	}
}

func TestWebBackendProbeWatchReadings(t *testing.T) {
	certPath := filepath.Join(t.TempDir(), "leaf.pem")
	certDER := mustProbeCertPEM(t)
	if err := os.WriteFile(certPath, certDER, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"tls-file": map[string]any{"check": map[string]any{
				"type": "cert",
				"path": certPath,
			}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{DefaultTimeout: 5 * time.Second})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	w := b.Watches(context.Background())[0]
	if got := readingByField(w.Readings, "source").Value; got != certPath {
		t.Fatalf("cert source = %q, want %q", got, certPath)
	}
	if got := readingByField(w.Readings, "days_left").Value; got == "" {
		t.Fatalf("cert days_left missing: %+v", w.Readings)
	}
}

func mustProbeCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "probe.test"},
		DNSNames:     []string{"probe.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(30 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestWebBackendPidsReadingErrorMarksWatchFailed(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"pid-table": map[string]any{"check": map[string]any{
				"type":     "pids",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		PidsSampler: func() (checks.PidsSample, error) {
			return checks.PidsSample{}, errors.New("loadavg failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if w.State != TargetStateFailed || len(w.Readings) != 1 || w.Readings[0].Error != "loadavg failed" {
		t.Fatalf("watch = %+v, want failed with pids error reading", w)
	}
}

func TestWebBackendFdsReadingErrorMarksWatchFailed(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"fd-table": map[string]any{"check": map[string]any{
				"type":     "fds",
				"used_pct": map[string]any{"op": ">=", "value": 80},
			}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		FdsSampler: func() (checks.FdsSample, error) {
			return checks.FdsSample{}, errors.New("file-nr failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if w.State != TargetStateFailed || len(w.Readings) != 1 || w.Readings[0].Error != "file-nr failed" {
		t.Fatalf("watch = %+v, want failed with fds error reading", w)
	}
}

type webBackendTestRunner struct {
	byCommand map[string]execx.Result
}

func (r webBackendTestRunner) Run(_ context.Context, name string, _ ...string) (execx.Result, error) {
	if res, ok := r.byCommand[name]; ok {
		return res, nil
	}
	return execx.Result{ExitCode: 127}, nil
}

type countingWebRunner struct {
	calls int
}

func (r *countingWebRunner) Run(_ context.Context, _ string, _ ...string) (execx.Result, error) {
	r.calls++
	return execx.Result{ExitCode: 0}, nil
}

func readingByField(readings []web.WatchReading, field string) web.WatchReading {
	for _, reading := range readings {
		if reading.Field == field {
			return reading
		}
	}
	return web.WatchReading{}
}

func conditionByField(conditions []web.WatchCondition, field string) web.WatchCondition {
	for _, condition := range conditions {
		if condition.Field == field {
			return condition
		}
	}
	return web.WatchCondition{}
}

type webProcSampler struct {
	match   ProcMatch
	samples []ProcInfo
	calls   int
}

func (s *webProcSampler) Sample(match ProcMatch) ([]ProcInfo, bool) {
	s.match = match
	s.calls++
	return s.samples, true
}

// fakeSwapReader is the minimal metrics.Reader (plus optional TotalSwap) the
// swap watch view needs.
type fakeSwapReader struct {
	total, used uint64
}

func (fakeSwapReader) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (fakeSwapReader) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (fakeSwapReader) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (fakeSwapReader) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (fakeSwapReader) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (fakeSwapReader) TotalMemory() (uint64, uint64, bool)  { return 1 << 30, 1 << 29, true }
func (fakeSwapReader) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (fakeSwapReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (fakeSwapReader) NumCPU() int         { return 1 }
func (fakeSwapReader) ClockTicks() float64 { return 100 }
func (r fakeSwapReader) TotalSwap() (uint64, uint64, bool) {
	return r.total, r.used, true
}

type countingSystemReader struct {
	memoryCalls int
	loadCalls   int
	swapCalls   int
}

func (*countingSystemReader) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (*countingSystemReader) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (*countingSystemReader) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (*countingSystemReader) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (*countingSystemReader) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (*countingSystemReader) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (*countingSystemReader) NumCPU() int                          { return 4 }
func (*countingSystemReader) ClockTicks() float64                  { return 100 }

func (r *countingSystemReader) TotalMemory() (uint64, uint64, bool) {
	r.memoryCalls++
	return 1000, 250, true
}

func (r *countingSystemReader) LoadAverages() (float64, float64, float64, bool) {
	r.loadCalls++
	return 1, 2, 3, true
}

func (r *countingSystemReader) TotalSwap() (uint64, uint64, bool) {
	r.swapCalls++
	return 2000, 500, true
}

func TestWebBackendSwapWatchIncludesUsage(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{
						"used_pct": map[string]any{"op": ">=", "value": 80},
						"then":     map[string]any{"notify": []any{"none"}},
					},
				},
			},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{Collector: metrics.New(fakeSwapReader{total: 2048, used: 512})})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].Swap == nil {
		t.Fatalf("watches = %+v, want one with swap usage", watches)
	}
	if watches[0].State == "failed" || len(watches[0].Readings) != 0 {
		t.Fatalf("swap watch state/readings = %q/%+v, want no probe failure", watches[0].State, watches[0].Readings)
	}
	sw := watches[0].Swap
	if sw.TotalBytes != 2048 || sw.UsedBytes != 512 || sw.FreeBytes != 1536 || sw.UsedPct != 25 {
		t.Fatalf("swap view = %+v, want 512/2048 used (25%%, 1536 free)", sw)
	}
}

func TestWebBackendWatchesShareSystemSnapshot(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"load":   map[string]any{"check": map[string]any{"type": "load"}},
			"memory": map[string]any{"check": map[string]any{"type": "memory"}},
			"swap":   map[string]any{"check": map[string]any{"type": "swap"}},
		},
	}}}
	reader := &countingSystemReader{}
	collector := metrics.New(reader)
	collector.SystemFreshness = 0
	b, warns := NewWebBackend(cfg, Deps{Collector: collector})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 3 {
		t.Fatalf("watches = %+v, want 3", watches)
	}
	if reader.memoryCalls != 1 || reader.loadCalls != 1 || reader.swapCalls != 1 {
		t.Fatalf("system samples memory/load/swap = %d/%d/%d, want 1/1/1", reader.memoryCalls, reader.loadCalls, reader.swapCalls)
	}
}

func TestWebBackendStorageOpenFiles(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"interval": "45s",
				"check":    map[string]any{"type": "storage", "path": "/data/app"},
			},
		},
	}}}
	scans := 0
	now := time.Unix(1000, 0)
	b, warns := NewWebBackend(cfg, Deps{
		Now: func() time.Time { return now },
		StorageUsage: func(string) (checks.StorageStats, error) {
			return checks.StorageStats{TotalBytes: 1000, FreeBytes: 500, UsedPct: 50}, nil
		},
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{
				{Device: "/dev/root", MountPoint: "/", FSType: "ext4"},
				{Device: "/dev/data", MountPoint: "/data", FSType: "xfs"},
			}, nil
		},
		OpenFilesByMount: func([]checks.Mount) map[string]int64 {
			scans++
			return map[string]int64{"/data": 4242, "/": 7}
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	get := func() *web.StorageWatchInfo {
		ws := b.Watches(context.Background())
		if len(ws) != 1 || ws[0].Storage == nil {
			t.Fatalf("want 1 storage watch with storage info, got %+v", ws)
		}
		return ws[0].Storage
	}
	// /data/app resolves to the /data mount (longest prefix), so it reports /data's tally.
	if si := get(); si.OpenFiles != 4242 {
		t.Fatalf("OpenFiles = %d, want 4242 (from the /data mount tally)", si.OpenFiles)
	}
	// A second call within the TTL reuses the cached tally (no re-scan).
	_ = get()
	if scans != 1 {
		t.Fatalf("open-files scanned %d times within TTL, want 1", scans)
	}
	// Once the TTL elapses, the next call re-scans.
	now = now.Add(openFilesTallyTTL + time.Second)
	_ = get()
	if scans != 2 {
		t.Fatalf("open-files scans after TTL = %d, want 2", scans)
	}
}

func TestWebBackendStorageWatchIncludesFilesystemDetails(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"interval": "45s",
				"dry_run":  true,
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data",
					"mounted":  true,
					"free_pct": map[string]any{"op": "<", "value": 15},
					"free_bytes": map[string]any{
						"op":    "<",
						"value": "10G",
					},
				},
				"then": map[string]any{
					"notify": []any{"ops", "pager"},
					"expand": map[string]any{"by": "5G"},
					"hook": map[string]any{
						"command": []any{"/usr/local/bin/sermo-disk-alert", "--path", "/data"},
					},
				},
			},
		},
	}}}
	usagePath := ""
	mountSampled := false
	b, warns := NewWebBackend(cfg, Deps{
		StorageUsage: func(path string) (checks.StorageStats, error) {
			usagePath = path
			return checks.StorageStats{
				UsedPct:       87.5,
				FreePct:       12.5,
				TotalBytes:    1000,
				FreeBytes:     125,
				InodesTotal:   100,
				InodesFree:    20,
				InodesUsedPct: 80,
				InodesFreePct: 20,
			}, nil
		},
		MountSampler: func() ([]checks.Mount, error) {
			mountSampled = true
			return []checks.Mount{
				{Device: "/dev/root", MountPoint: "/", FSType: "ext4", Options: []string{"rw"}},
				{Device: "/dev/mapper/data", MountPoint: "/data", FSType: "xfs", Options: []string{"rw", "noatime"}},
			}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches, want 1: %+v", len(watches), watches)
	}
	w := watches[0]
	if usagePath != "/data" || !mountSampled {
		t.Fatalf("samplers not called as expected: usagePath=%q mountSampled=%v", usagePath, mountSampled)
	}
	if w.Name != "storage-data" || w.Interval != "45s" || w.CheckType != "storage" {
		t.Fatalf("watch identity = %+v", w)
	}
	if !slices.Equal(w.Notifiers, []string{"ops", "pager"}) {
		t.Fatalf("notifiers = %v, want ops,pager", w.Notifiers)
	}
	if !w.DryRun {
		t.Fatal("dry_run flag not exposed")
	}
	if !slices.Equal(w.HookCommand, []string{"/usr/local/bin/sermo-disk-alert", "--path", "/data"}) {
		t.Fatalf("hook command = %v", w.HookCommand)
	}
	if w.Summary == "" || !strings.Contains(w.Summary, "/data") || !strings.Contains(w.Summary, "xfs") {
		t.Fatalf("summary = %q, want path and filesystem", w.Summary)
	}
	if len(w.Conditions) != 3 {
		t.Fatalf("conditions = %+v, want free_pct/free_bytes/mounted", w.Conditions)
	}
	cond := map[string]web.WatchCondition{}
	for _, c := range w.Conditions {
		cond[c.Field] = c
	}
	if cond["free_pct"].Op != "<" || cond["free_pct"].Value != "15" {
		t.Fatalf("free_pct condition = %+v", cond["free_pct"])
	}
	if cond["free_bytes"].Op != "<" || cond["free_bytes"].Value != "10G" {
		t.Fatalf("free_bytes condition = %+v", cond["free_bytes"])
	}
	if cond["mounted"].Op != "==" || cond["mounted"].Value != "true" {
		t.Fatalf("mounted condition = %+v", cond["mounted"])
	}
	if w.Storage == nil {
		t.Fatal("storage watch should include live filesystem info")
	}
	if w.Storage.MountPoint != "/data" || w.Storage.Device != "/dev/mapper/data" || w.Storage.FileSystem != "xfs" {
		t.Fatalf("storage mount info = %+v", w.Storage)
	}
	if w.Storage.FreeBytes != 125 || w.Storage.UsedBytes != 875 || w.Storage.FreePct != 12.5 || w.Storage.InodesFree != 20 {
		t.Fatalf("storage usage info = %+v", w.Storage)
	}
	if w.Expand == nil || w.Expand.ByBytes != 5<<30 {
		t.Fatalf("expand info = %+v, want 5G", w.Expand)
	}
}

func TestWebBackendStorageMountedExpectationDoesNotUseParentMount(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-boot-desktop": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/var/spool/boot_desktop",
					"mounted":  true,
					"free_pct": map[string]any{"op": "<", "value": 10},
				},
			},
		},
	}}}
	usageCalled := false
	b, warns := NewWebBackend(cfg, Deps{
		StorageUsage: func(string) (checks.StorageStats, error) {
			usageCalled = true
			return checks.StorageStats{TotalBytes: 1000, FreeBytes: 900, FreePct: 90}, nil
		},
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{Device: "/dev/root", MountPoint: "/", FSType: "ext4"}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches, want 1: %+v", len(watches), watches)
	}
	w := watches[0]
	if usageCalled {
		t.Fatal("storage usage must not read parent filesystem when expected mountpoint is absent")
	}
	if w.State != TargetStateFailed {
		t.Fatalf("state = %q, want failed", w.State)
	}
	if w.Summary != "/var/spool/boot_desktop: not mounted" {
		t.Fatalf("summary = %q", w.Summary)
	}
	if w.Storage == nil {
		t.Fatal("storage info missing")
	}
	if w.Storage.Mounted || w.Storage.MountPoint != "" || w.Storage.Device != "" {
		t.Fatalf("storage info = %+v, want not mounted with no parent mount", w.Storage)
	}
}

func TestWebBackendNotifiersExposeEnabledState(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"notifiers": map[string]any{
			"muted": map[string]any{"enabled": false, "type": "slack", "webhook": "https://hooks.example/x"},
			"ops":   map[string]any{"type": "email", "dsn": "smtp://x", "from": "x@y", "to": []any{"a@b"}},
		},
		"watches": map[string]any{
			"disk": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/"},
				"then":  map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	got := b.Notifiers(context.Background())
	if len(got) != 4 {
		t.Fatalf("notifiers = %+v, want 4 entries", got)
	}
	byName := map[string]web.Notifier{}
	for _, n := range got {
		byName[n.Name] = n
	}
	if !byName["ops"].Enabled {
		t.Fatalf("ops should default enabled: %+v", byName["ops"])
	}
	if byName["muted"].Enabled {
		t.Fatalf("muted should be disabled: %+v", byName["muted"])
	}
	if byName["ops"].Summary != "a@b" || byName["ops"].UsedBy != 1 {
		t.Fatalf("ops summary/usage = %+v, want a@b and used_by 1", byName["ops"])
	}
	if byName["muted"].Summary != "hooks.example" {
		t.Fatalf("muted summary = %q, want hooks.example", byName["muted"].Summary)
	}
	if !byName["tty"].Enabled || byName["tty"].Summary != "all active terminals" || byName["tty"].UsedBy != 0 {
		t.Fatalf("tty notifier metadata = %+v", byName["tty"])
	}
	if !byName["wall"].Enabled || byName["wall"].Summary != "all active terminals" || byName["wall"].UsedBy != 0 {
		t.Fatalf("wall notifier metadata = %+v", byName["wall"])
	}
}

func TestWebBackendExpandWatchUsesConfiguredPathAndSize(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	var events []Event
	b, warns := NewWebBackend(cfg, Deps{
		VolumeExpander:   exp,
		OperationTimeout: time.Second,
		Emit:             func(e Event) { events = append(events, e) },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if !res.OK {
		t.Fatalf("ExpandWatch failed: %+v", res)
	}
	if !slices.Equal(exp.calls, []string{"/data/app:5368709120"}) {
		t.Fatalf("expand calls = %v, want configured path and 5G", exp.calls)
	}
	if len(events) != 1 || events[0].Watch != "storage-data" || events[0].Kind != eventKindExpand || events[0].Action != eventActionExpand || events[0].Status != eventStatusOK {
		t.Fatalf("events = %+v, want successful expand event", events)
	}
}

func TestWebBackendExpandWatchDryRunDoesNotExpand(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"dry_run": true,
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	var events []Event
	b, warns := NewWebBackend(cfg, Deps{
		VolumeExpander: exp,
		Emit:           func(e Event) { events = append(events, e) },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if !res.OK || res.Message != "dry-run: would run expand" {
		t.Fatalf("ExpandWatch = %+v, want dry-run success", res)
	}
	if len(exp.calls) != 0 {
		t.Fatalf("dry-run expand must not call expander, calls = %v", exp.calls)
	}
	if len(events) != 1 || events[0].Watch != "storage-data" || events[0].Kind != eventKindDryRun || events[0].Action != eventActionExpand || events[0].Status != eventStatusOK {
		t.Fatalf("events = %+v, want dry-run expand event", events)
	}
}

func TestWebBackendExpandWatchRejectsUnconfiguredAction(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	b, warns := NewWebBackend(cfg, Deps{VolumeExpander: exp, OperationTimeout: time.Second})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if res.OK || !strings.Contains(res.Message, "no then.expand") {
		t.Fatalf("ExpandWatch = %+v, want missing expand rejection", res)
	}
	if len(exp.calls) != 0 {
		t.Fatalf("expand must not run without then.expand, calls=%v", exp.calls)
	}
}

func TestWebBackendStorageWatchReportsSamplerErrors(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data",
					"free_pct": map[string]any{"op": "<", "value": 15},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		StorageUsage: func(string) (checks.StorageStats, error) {
			return checks.StorageStats{}, errors.New("statfs failed")
		},
		MountSampler: func() ([]checks.Mount, error) {
			return nil, errors.New("mount table failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].Storage == nil {
		t.Fatalf("watch storage info = %+v", watches)
	}
	if watches[0].Storage.SampleError != "statfs failed" || watches[0].Storage.MountSampleError != "mount table failed" {
		t.Fatalf("storage errors = %+v", watches[0].Storage)
	}
	if !strings.Contains(watches[0].Summary, "statfs failed") {
		t.Fatalf("summary = %q, want statfs error", watches[0].Summary)
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
			"mysql": {displayName: "MySQL", category: "database", unit: "mysqld", backend: string(servicemgr.BackendSystemd), disabled: true},
			"web": {
				displayName: "Web",
				category:    "frontend",
				unit:        "nginx",
				backend:     string(servicemgr.BackendSystemd),
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
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
	if dis.Enabled || dis.Status != "disabled" || dis.State != TargetStateDisabled || dis.Monitored {
		t.Fatalf("disabled service = %+v, want Enabled=false, Status=disabled, State=disabled, Monitored=false", dis)
	}
	if dis.Category != "database" {
		t.Fatalf("disabled service category = %q, want database", dis.Category)
	}

	en := byName["web"]
	if !en.Enabled || en.Status == "disabled" || en.State != TargetStateMonitored || !en.Monitored {
		t.Fatalf("normal service = %+v, want Enabled=true, State=monitored, Monitored=true", en)
	}
	if en.Category != "frontend" {
		t.Fatalf("normal service category = %q, want frontend", en.Category)
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
	res := b.Operate(context.Background(), "mysql", string(rules.ActionRestart), web.OperateOpts{})
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

func TestWebBackendReloadUnsupportedIsExposedAndBlocked(t *testing.T) {
	var events []Event
	b := &WebBackend{
		order: []string{"acpid"},
		entries: map[string]*webEntry{
			"acpid": {
				displayName: "ACPI Daemon",
				category:    "hardware",
				unit:        "acpid",
				backend:     string(servicemgr.BackendOpenRC),
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
				canReload:   false,
			},
		},
		emit: func(e Event) { events = append(events, e) },
	}

	svcs := b.Services(context.Background())
	if len(svcs) != 1 {
		t.Fatalf("services = %+v, want one", svcs)
	}
	if svcs[0].Name != "acpid" || svcs[0].CanReload {
		t.Fatalf("service reload support = %+v, want acpid CanReload=false", svcs[0])
	}

	res := b.Operate(context.Background(), "acpid", "reload", web.OperateOpts{})
	if res.OK || !strings.Contains(res.Message, "does not support reload") {
		t.Fatalf("reload result = %+v, want unsupported reload error", res)
	}
	if len(events) != 1 || events[0].Kind != eventKindError || events[0].Action != eventActionReload {
		t.Fatalf("events = %+v, want one reload error event", events)
	}
}

func TestWebBackendReloadSupportedByInitBackend(t *testing.T) {
	cfg := &config.Config{
		ServiceNames: []string{"nginx"},
		Services: map[string]*config.Document{
			"nginx": {Name: "nginx", Body: map[string]any{
				"name":    "nginx",
				"service": "nginx",
			}},
		},
	}

	b, warns := NewWebBackend(cfg, Deps{
		Backend: servicemgr.BackendOpenRC,
		Manager: fakeManager{},
	})
	if len(warns) != 0 {
		t.Fatalf("NewWebBackend warnings = %v, want none", warns)
	}
	svcs := b.Services(context.Background())
	if len(svcs) != 1 {
		t.Fatalf("services = %+v, want one", svcs)
	}
	if svcs[0].Name != "nginx" || !svcs[0].CanReload {
		t.Fatalf("service reload support = %+v, want nginx CanReload=true from init backend", svcs[0])
	}
}

func TestWebBackendApplicationsStartingUnsettled(t *testing.T) {
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingAppKey("git")})

	b := &WebBackend{
		cfg: &config.Config{
			AppNames: []string{"git"},
			Apps: map[string]*config.Document{
				"git": {Body: map[string]any{"name": "git", "display_name": "Git"}},
			},
		},
		settling: settling,
	}

	apps := b.loadApplications(context.Background())
	if len(apps) != 1 {
		t.Fatalf("unsettled apps = %+v, want one placeholder", apps)
	}
	if apps[0].State != TargetStateStarting || apps[0].Name != "git" {
		t.Fatalf("unsettled app = %+v, want git starting", apps[0])
	}

	settling.MarkObserved(SettlingAppKey("git"))
	apps = b.loadApplications(context.Background())
	for _, a := range apps {
		if a.State == TargetStateStarting {
			t.Fatalf("settled app still starting: %+v", a)
		}
	}
}

func TestWebBackendStartingStateUnsettled(t *testing.T) {
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("web"), SettlingWatchKey("disk")})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				displayName: "Web",
				status: func(context.Context) (servicemgr.Status, error) {
					return servicemgr.StatusInactive, nil
				},
			},
		},
		watchOrder: []string{"disk"},
		watches: map[string]*webWatch{
			"disk": {name: "disk", checkType: "storage"},
		},
		settling: settling,
	}

	svcs := b.Services(context.Background())
	if len(svcs) != 1 || svcs[0].State != TargetStateStarting {
		t.Fatalf("unsettled service = %+v, want state starting", svcs[0])
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].State != TargetStateStarting {
		t.Fatalf("unsettled watch = %+v, want state starting", watches[0])
	}

	settling.MarkObserved(SettlingServiceKey("web"))
	settling.MarkObserved(SettlingWatchKey("disk"))

	svcs = b.Services(context.Background())
	if svcs[0].State != TargetStateFailed {
		t.Fatalf("settled inactive service = %+v, want state failed", svcs[0])
	}
	watches = b.Watches(context.Background())
	if watches[0].State != TargetStateOK {
		t.Fatalf("settled healthy watch = %+v, want state ok", watches[0])
	}
}

// TestWatchSnapshotsFeedHeavyProbeView verifies that /api/watches renders
// expensive disk checks from daemon-cycle snapshots and never starts hdparm/smart
// from the web handler.
func TestWatchSnapshotsFeedHeavyProbeView(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	runner := &countingWebRunner{}
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return now }
	b := &WebBackend{
		watchOrder: []string{"disk"},
		watches: map[string]*webWatch{
			"disk": {
				name:      "disk",
				checkType: checks.CheckTypeHdparm,
				interval:  time.Hour,
				check: map[string]any{
					checks.CheckKeyType:   checks.CheckTypeHdparm,
					checks.CheckKeyDevice: "/dev/sda",
					checks.HdparmFieldRead: map[string]any{
						checks.CheckKeyOp:    "<",
						checks.CheckKeyValue: 100,
					},
				},
			},
		},
		watchSnapshots: snapshots,
		execRunner:     runner,
		now:            func() time.Time { return now },
	}

	// Cold heavy probes are not run by the web handler.
	ws := b.Watches(context.Background())
	if len(ws) != 1 || strings.Contains(ws[0].Summary, "hdparm") {
		t.Fatalf("cold Watches() = %+v, want no hdparm summary", ws)
	}
	if runner.calls != 0 {
		t.Fatalf("cold heavy probe ran %d commands, want 0", runner.calls)
	}

	snapshots.Publish("disk", checks.CheckTypeHdparm, checks.Result{
		Check:     "disk",
		Condition: true,
		Message:   "hdparm /dev/sda read=500.0 MB/s",
		Data: map[string]any{
			checks.DataKeyDevice:   "/dev/sda",
			checks.HdparmFieldRead: 500.0,
		},
	})
	ws = b.Watches(context.Background())
	if len(ws) != 1 || !strings.Contains(ws[0].Summary, "hdparm") || readingByField(ws[0].Readings, checks.HdparmFieldRead).Value == "" {
		t.Fatalf("snapshot Watches() = %+v, want hdparm summary/readings", ws)
	}
	if runner.calls != 0 {
		t.Fatalf("snapshot heavy probe ran %d commands, want 0", runner.calls)
	}
}

func TestWatchSnapshotsSuppressColdWebProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var samplerCalls int
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return now }
	b := &WebBackend{
		watchOrder: []string{"fw"},
		watches: map[string]*webWatch{
			"fw": {
				name:      "fw",
				checkType: checks.CheckTypeFirewallRules,
				interval:  time.Minute,
				check: map[string]any{
					checks.CheckKeyType:     checks.CheckTypeFirewallRules,
					checks.CheckKeyMinRules: 2,
				},
			},
		},
		watchSnapshots: snapshots,
		firewallSampler: func(context.Context, string, execx.Runner) (checks.FirewallRulesSample, error) {
			samplerCalls++
			return checks.FirewallRulesSample{Backend: checks.FirewallBackendNftables, Rules: 3}, nil
		},
		now: func() time.Time { return now },
	}

	ws := b.Watches(context.Background())
	if len(ws) != 1 || strings.Contains(ws[0].Summary, "firewall") {
		t.Fatalf("cold Watches() = %+v, want no live firewall summary", ws)
	}
	if samplerCalls != 0 {
		t.Fatalf("cold web probe sampled firewall %d times, want 0", samplerCalls)
	}

	snapshots.Publish("fw", checks.CheckTypeFirewallRules, checks.Result{
		Check:     "fw",
		OK:        true,
		Condition: false,
		Message:   "firewall nft has 3 rules",
		Data: map[string]any{
			checks.DataKeyBackend:  checks.FirewallBackendNftables,
			checks.DataKeyRules:    3,
			checks.DataKeyMinRules: 2,
		},
	})
	ws = b.Watches(context.Background())
	if len(ws) != 1 || !strings.Contains(ws[0].Summary, "firewall") || len(ws[0].Readings) != 3 {
		t.Fatalf("snapshot Watches() = %+v, want firewall summary/readings", ws)
	}
	if samplerCalls != 0 {
		t.Fatalf("snapshot web probe sampled firewall %d times, want 0", samplerCalls)
	}
}

func TestWatchSnapshotsFeedProcessView(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return now }
	sampler := &webProcSampler{samples: []ProcInfo{{PID: 1}}}
	b := &WebBackend{
		watchOrder: []string{"hot-workers"},
		watches: map[string]*webWatch{
			"hot-workers": {
				name:      "hot-workers",
				checkType: checks.CheckTypeProcess,
				interval:  time.Minute,
				check: map[string]any{
					checks.CheckKeyType: checks.CheckTypeProcess,
					checks.CheckKeyName: "apache2",
					checks.CheckKeyUser: "apache",
				},
			},
		},
		watchSnapshots: snapshots,
		procSampler:    sampler,
		now:            func() time.Time { return now },
	}

	ws := b.Watches(context.Background())
	if len(ws) != 1 || ws[0].Summary != "" || len(ws[0].Readings) != 0 {
		t.Fatalf("cold process Watches() = %+v, want no live process summary", ws)
	}
	if sampler.calls != 0 {
		t.Fatalf("cold web process sampled %d times, want 0", sampler.calls)
	}

	snapshots.Publish("hot-workers", checks.CheckTypeProcess, checks.Result{
		Check:   "hot-workers",
		OK:      true,
		Message: "process apache2 user apache: 2 matching processes, rss 300 bytes",
		Data: map[string]any{
			watchReadingFieldProcess:  "apache2",
			watchReadingFieldUser:     "apache",
			watchReadingFieldMatches:  2,
			checks.DataKeyPIDs:        "7, 42",
			watchReadingFieldRSS:      uint64(300),
			watchReadingFieldCPUTicks: uint64(50),
			metrics.MetricIO:          uint64(500),
		},
	})
	ws = b.Watches(context.Background())
	if len(ws) != 1 || !strings.Contains(ws[0].Summary, "2 matching processes") {
		t.Fatalf("snapshot process Watches() = %+v, want process summary", ws)
	}
	if got := readingByField(ws[0].Readings, checks.DataKeyPIDs).Value; got != "7, 42" {
		t.Fatalf("process pids reading = %q, want snapshot pids", got)
	}
	if sampler.calls != 0 {
		t.Fatalf("snapshot web process sampled %d times, want 0", sampler.calls)
	}
}

// fakeEnvRunnerForWeb is used to inject a custom execx runner via Deps.ExecxRunner
// and verify that hooks in watches built for the web backend receive the expected env.
type fakeEnvRunnerForWeb struct {
	calls []struct {
		env  []string
		name string
		args []string
	}
}

func (f *fakeEnvRunnerForWeb) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Result{}, nil
}
func (f *fakeEnvRunnerForWeb) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	f.calls = append(f.calls, struct {
		env  []string
		name string
		args []string
	}{env, name, args})
	return execx.Result{ExitCode: 0}, nil
}

func TestWebBackendPropagatesCustomExecxRunnerToWatchHooks(t *testing.T) {
	// Minimal config with a watch that has a hook. The check is a simple command that always succeeds.
	cfgContent := `
paths:
  services: []
  runtime: /tmp
defaults:
  policy:
    cooldown: 1m
watches:
  test-hook-watch:
    check:
      type: command
      command: ["/bin/true"]
    then:
      hook:
        command: ["/bin/custom-web-hook", "web-alert"]
`
	globalPath := filepath.Join(t.TempDir(), "sermo.yml")
	if err := os.WriteFile(globalPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	fake := &fakeEnvRunnerForWeb{}
	deps := Deps{
		Backend:        servicemgr.BackendSystemd,
		Manager:        fakeManager{},
		ExecxRunner:    fake,
		DefaultTimeout: time.Second,
		Now:            time.Now,
		Emit:           func(Event) {},
	}

	// NewWebBackend will internally call BuildWatches with the deps, propagating ExecxRunner
	// to the OSHookRunner for the watch.
	_, warnings := NewWebBackend(cfg, deps)
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	// To exercise the hook, we simulate a watch cycle. Since watches are built internally,
	// we use BuildWatches directly with same deps to get the watch and run it (this mirrors
	// what NewWebBackend does for watch part).
	watches, wwarns := BuildWatches(cfg, deps, 30*time.Second)
	if len(wwarns) != 0 || len(watches) != 1 {
		t.Fatalf("expected 1 watch, warnings=%v", wwarns)
	}
	w := watches[0]
	// Command check is health-style (FireOnFail=true by default), so success (OK=true) means !fired.
	// Override to force fire on success for this test (we only care about runner being called with env).
	w.FireOnFail = false
	w.RunCycle(context.Background())

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call to custom execx runner from web backend watch hook, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.name != "/bin/custom-web-hook" || len(call.args) != 1 || call.args[0] != "web-alert" {
		t.Fatalf("wrong argv to custom runner: %s %v", call.name, call.args)
	}
	// Verify specific env from the watch (SERMO_WATCH and data from the command check result if any, plus type)
	hasWatch := false
	hasType := false
	for _, e := range call.env {
		if e == "SERMO_WATCH=test-hook-watch" {
			hasWatch = true
		}
		if e == "SERMO_CHECK_TYPE=command" {
			hasType = true
		}
	}
	if !hasWatch || !hasType {
		t.Fatalf("custom runner via webbackend Deps did not receive expected SERMO_ env: %v", call.env)
	}
}
