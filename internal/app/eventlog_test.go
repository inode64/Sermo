package app

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/logfile"
	"sermo/internal/rules"
	"sermo/internal/state"
)

func TestEventLogExportsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.log")
	w, err := logfile.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l := NewEventLog(10)
	l.SetEventFile(w)
	l.now = func() time.Time { return time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC) }
	l.Add(Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionRestart), Status: eventStatusOK, Message: "done"})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("expected one line")
	}
	var row map[string]any
	if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
		t.Fatalf("json: %v", err)
	}
	if row["service"] != "web" || row["kind"] != eventKindAction {
		t.Fatalf("row = %+v", row)
	}
}

func TestEventLogRecentNewestFirst(t *testing.T) {
	l := NewEventLog(10)
	l.Add(Event{Service: "a", Kind: eventKindAction, Message: "1"})
	l.Add(Event{Service: "b", Kind: eventKindAlert, Message: "2"})
	l.Add(Event{Service: "a", Kind: eventKindError, Message: "3"})

	all := l.Recent("", 0)
	if len(all) != 3 || all[0].Message != "3" || all[2].Message != "1" {
		t.Fatalf("recent newest-first wrong: %+v", all)
	}
	if got := l.Recent("", 2); len(got) != 2 || got[0].Message != "3" {
		t.Fatalf("limit not applied: %+v", got)
	}
	if all[0].ID <= all[1].ID || all[1].ID <= all[2].ID || all[2].ID <= 0 {
		t.Fatalf("event IDs are not positive and newest-first: %+v", all)
	}
	page := l.Page(all[1].ID, 2)
	if len(page) != 1 || page[0].ID != all[2].ID {
		t.Fatalf("cursor page = %+v, want oldest event", page)
	}
}

func TestEventLogPerService(t *testing.T) {
	l := NewEventLog(10)
	l.Add(Event{Service: "a", Message: "a1"})
	l.Add(Event{Watch: "storage-root", Message: "w1"}) // host watch, no service
	l.Add(Event{Service: "b", Message: "b1"})
	l.Add(Event{Service: "a", Message: "a2"})

	a := l.Recent("a", 0)
	if len(a) != 2 || a[0].Message != "a2" || a[1].Message != "a1" {
		t.Fatalf("per-service filter wrong: %+v", a)
	}
	// the global feed includes the watch event
	if len(l.Recent("", 0)) != 4 {
		t.Fatal("global feed should include the watch event")
	}
}

func TestEventLogPerApp(t *testing.T) {
	l := NewEventLog(10)
	l.Add(Event{App: "salt-minion", Kind: eventKindFiring, Message: "error: exit 1"})
	l.Add(Event{Service: "web", Message: "svc"})
	l.Add(Event{App: "salt-minion", Kind: eventKindRecovered, Message: "ok"})
	l.Add(Event{App: "redis", Kind: eventKindFiring, Message: "boom"})

	salt := l.RecentApp("salt-minion", 0)
	if len(salt) != 2 || salt[0].Message != "ok" || salt[1].Message != "error: exit 1" {
		t.Fatalf("per-app filter wrong: %+v", salt)
	}
	// app events are not mixed into the per-service feed, but appear in the global feed.
	if len(l.Recent("web", 0)) != 1 {
		t.Fatalf("service feed must not include app events")
	}
	if len(l.Recent("", 0)) != 4 {
		t.Fatal("global feed should include app events")
	}
}

func TestEventLogRingEviction(t *testing.T) {
	l := NewEventLog(3)
	for _, m := range []string{"1", "2", "3", "4", "5"} {
		l.Add(Event{Service: "s", Message: m})
	}
	got := l.Recent("", 0)
	if len(got) != 3 || got[0].Message != "5" || got[2].Message != "3" {
		t.Fatalf("ring eviction wrong: %+v", got)
	}
}

func TestMultiEmit(t *testing.T) {
	var a, b []Event
	emit := MultiEmit(
		func(e Event) { a = append(a, e) },
		nil, // skipped
		func(e Event) { b = append(b, e) },
	)
	emit(Event{Kind: eventKindAction})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("MultiEmit did not fan out: a=%d b=%d", len(a), len(b))
	}
}

func TestEventLogPrune(t *testing.T) {
	l := NewEventLog(10)
	now := time.Now()
	l.now = func() time.Time { return now }

	l.Add(Event{Message: "old1"})
	l.Add(Event{Message: "old2"})
	l.now = func() time.Time { return now.Add(10 * time.Minute) }
	l.Add(Event{Message: "recent"})

	if got := l.Prune(now.Add(5 * time.Minute)); got != 2 {
		t.Fatalf("prune before 5m pruned %d, want 2", got)
	}
	rem := l.Recent("", 0)
	if len(rem) != 1 || rem[0].Message != "recent" {
		t.Fatalf("after prune: %+v", rem)
	}

	// prune all
	if got := l.Prune(time.Time{}); got != 1 {
		t.Fatalf("prune zero-time cleared %d", got)
	}
	if len(l.Recent("", 0)) != 0 {
		t.Fatal("not empty after clear all")
	}
}

func TestEventLogConcurrentAddRecent(t *testing.T) {
	l := NewEventLog(64)
	done := make(chan struct{})
	go func() {
		for range 5000 {
			l.Add(Event{Service: "a", Kind: eventKindAction, Message: "x"})
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			_ = l.Recent("", 10)
		}
	}
}

func TestPersistentEventLogHydratesServiceEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), state.Filename)
	first, err := state.Open(path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t0 := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	log, err := NewPersistentEventLog(10, first, nil)
	if err != nil {
		t.Fatalf("NewPersistentEventLog(first): %v", err)
	}
	log.now = func() time.Time { return t0 }
	log.Add(Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionRestart), Status: eventStatusOK, Message: "restart completed"})
	log.now = func() time.Time { return t0.Add(time.Minute) }
	log.Add(Event{Watch: "storage-root", Kind: eventKindHook, Message: "hook completed"})
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := state.Open(path)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer second.Close()
	hydrated, err := NewPersistentEventLog(10, second, nil)
	if err != nil {
		t.Fatalf("NewPersistentEventLog(second): %v", err)
	}

	global := hydrated.Recent("", 10)
	if len(global) != 2 || global[0].Watch != "storage-root" || global[1].Service != "web" {
		t.Fatalf("hydrated global events = %+v", global)
	}
	if global[0].ID <= global[1].ID || global[1].ID <= 0 {
		t.Fatalf("hydrated event IDs = [%d %d], want stable positive IDs", global[0].ID, global[1].ID)
	}
	page := hydrated.Page(global[0].ID, 10)
	if len(page) != 1 || page[0].ID != global[1].ID {
		t.Fatalf("hydrated cursor page = %+v, want older service event", page)
	}
	service := hydrated.Recent("web", 10)
	if len(service) != 1 || service[0].Service != "web" || service[0].Action != string(rules.ActionRestart) {
		t.Fatalf("hydrated service events = %+v", service)
	}

	b := &WebBackend{entries: map[string]*webEntry{"web": {}}, events: hydrated}
	webEvents, ok := b.ServiceEvents(context.Background(), "web", 10)
	if !ok || len(webEvents) != 1 || webEvents[0].Service != "web" || webEvents[0].Action != string(rules.ActionRestart) {
		t.Fatalf("web service events = %+v ok=%v", webEvents, ok)
	}
}
