package app

import (
	"context"
	"testing"
	"time"
)

func TestWebBackendSetMonitoredEmitsEvent(t *testing.T) {
	store := newFakeStore()
	store.active["web"] = true

	var events []Event
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
		store:   store,
		emit:    func(e Event) { events = append(events, e) },
	}

	if err := b.SetMonitored(context.Background(), "web", false); err != nil {
		t.Fatalf("unmonitor: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want one event, got %+v", events)
	}
	if events[0].Kind != "action" || events[0].Action != "unmonitor" || events[0].Status != "ok" ||
		events[0].Message != "monitoring paused" || events[0].Service != "web" {
		t.Fatalf("unmonitor event = %+v", events[0])
	}
	if store.active["web"] {
		t.Fatal("store should record paused")
	}

	if err := b.SetMonitored(context.Background(), "web", false); err != nil {
		t.Fatalf("repeat unmonitor: %v", err)
	}
	if len(events) != 2 || events[1].Kind != "suppressed" || events[1].Action != "unmonitor" ||
		events[1].Message != "already paused" {
		t.Fatalf("repeat unmonitor event = %+v", events[1])
	}

	if err := b.SetMonitored(context.Background(), "web", true); err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if len(events) != 3 || events[2].Kind != "action" || events[2].Action != "monitor" ||
		events[2].Message != "monitoring resumed" {
		t.Fatalf("monitor event = %+v", events[2])
	}

	if err := b.SetMonitored(context.Background(), "web", true); err != nil {
		t.Fatalf("repeat monitor: %v", err)
	}
	if len(events) != 4 || events[3].Kind != "suppressed" || events[3].Action != "monitor" ||
		events[3].Message != "already monitored" {
		t.Fatalf("repeat monitor event = %+v", events[3])
	}
}

func TestWebBackendSetMonitoredEmitsError(t *testing.T) {
	store := newFakeStore()
	var events []Event
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
		store:   store,
		emit:    func(e Event) { events = append(events, e) },
	}

	if err := b.SetMonitored(context.Background(), "ghost", false); err == nil {
		t.Fatal("unknown service should fail")
	}
	if len(events) != 1 || events[0].Kind != "error" || events[0].Action != "unmonitor" {
		t.Fatalf("unknown service event = %+v", events[0])
	}

	store.failSet = true
	if err := b.SetMonitored(context.Background(), "web", false); err == nil {
		t.Fatal("store failure should fail")
	}
	if len(events) != 2 || events[1].Kind != "error" || events[1].Action != "unmonitor" {
		t.Fatalf("store failure event = %+v", events[1])
	}
}

func TestWebBackendSetMonitoredAppearsInEventLog(t *testing.T) {
	store := newFakeStore()
	log := NewEventLog(10)
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
		store:   store,
		events:  log,
		emit:    MultiEmit(log.Add),
	}

	if err := b.SetMonitored(context.Background(), "web", false); err != nil {
		t.Fatalf("unmonitor: %v", err)
	}
	recent := log.Recent("web", 0)
	if len(recent) != 1 || recent[0].Action != "unmonitor" || recent[0].Kind != "action" {
		t.Fatalf("event log = %+v", recent)
	}
	webEvents, ok := b.ServiceEvents(context.Background(), "web", 10)
	if !ok || len(webEvents) != 1 || webEvents[0].Action != "unmonitor" {
		t.Fatalf("service events = %+v ok=%v", webEvents, ok)
	}
}

func TestWebBackendSetWatchMonitoredEmitsEvent(t *testing.T) {
	store := newFakeStore()
	store.active[watchMonitorKey("storage-root")] = true

	var events []Event
	b := &WebBackend{
		watches: map[string]*webWatch{"storage-root": {name: "storage-root"}},
		store:   store,
		emit:    func(e Event) { events = append(events, e) },
	}

	if err := b.SetWatchMonitored(context.Background(), "storage-root", false); err != nil {
		t.Fatalf("unmonitor watch: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want one event, got %+v", events)
	}
	if events[0].Kind != "action" || events[0].Action != "unmonitor" || events[0].Status != "ok" ||
		events[0].Message != "monitoring paused" || events[0].Watch != "storage-root" || events[0].Service != "" {
		t.Fatalf("unmonitor watch event = %+v", events[0])
	}
	if store.active[watchMonitorKey("storage-root")] {
		t.Fatal("store should record watch paused")
	}

	if err := b.SetWatchMonitored(context.Background(), "storage-root", true); err != nil {
		t.Fatalf("monitor watch: %v", err)
	}
	if len(events) != 2 || events[1].Kind != "action" || events[1].Action != "monitor" ||
		events[1].Message != "monitoring resumed" {
		t.Fatalf("monitor watch event = %+v", events[1])
	}
}

func TestWebBackendSetWatchMonitoredEmitsError(t *testing.T) {
	store := newFakeStore()
	var events []Event
	b := &WebBackend{
		watches: map[string]*webWatch{"storage-root": {name: "storage-root"}},
		store:   store,
		emit:    func(e Event) { events = append(events, e) },
	}

	if err := b.SetWatchMonitored(context.Background(), "ghost", false); err == nil {
		t.Fatal("unknown watch should fail")
	}
	if len(events) != 1 || events[0].Kind != "error" || events[0].Action != "unmonitor" || events[0].Watch != "ghost" {
		t.Fatalf("unknown watch event = %+v", events[0])
	}

	store.failSet = true
	if err := b.SetWatchMonitored(context.Background(), "storage-root", false); err == nil {
		t.Fatal("store failure should fail")
	}
	if len(events) != 2 || events[1].Kind != "error" || events[1].Action != "unmonitor" || events[1].Watch != "storage-root" {
		t.Fatalf("store failure event = %+v", events[1])
	}
}

func TestWebBackendWatchesIncludeMonitoringState(t *testing.T) {
	store := newFakeStore()
	at := time.Date(2026, 6, 10, 11, 12, 13, 0, time.UTC)
	store.now = func() time.Time { return at }
	if err := store.SetActive(watchMonitorKey("storage-root"), false, "web"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	b := &WebBackend{
		watchOrder: []string{"storage-root"},
		watches: map[string]*webWatch{
			"storage-root": {name: "storage-root", checkType: "storage", monitorMode: "previous"},
		},
		store: store,
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches", len(watches))
	}
	if watches[0].Monitored || watches[0].MonitorSource != "web" || watches[0].MonitorChangedAt != at.Format(time.RFC3339) {
		t.Fatalf("watch monitoring state = %+v", watches[0])
	}
	if watches[0].State != TargetStateDisabled {
		t.Fatalf("watch state = %q, want %q", watches[0].State, TargetStateDisabled)
	}
	if watches[0].Monitor != "previous" {
		t.Fatalf("watch monitor mode = %q, want previous", watches[0].Monitor)
	}
}
