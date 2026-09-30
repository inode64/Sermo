package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/logfile"
	"sermo/internal/rules"
	"sermo/internal/severity"
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

	if got, err := l.Prune(context.Background(), now.Add(5*time.Minute)); err != nil || got != 2 {
		t.Fatalf("prune before 5m pruned %d err=%v, want 2", got, err)
	}
	rem := l.Recent("", 0)
	if len(rem) != 1 || rem[0].Message != "recent" {
		t.Fatalf("after prune: %+v", rem)
	}

	// prune all
	if got, err := l.Prune(context.Background(), time.Time{}); err != nil || got != 1 {
		t.Fatalf("prune zero-time cleared %d err=%v", got, err)
	}
	if len(l.Recent("", 0)) != 0 {
		t.Fatal("not empty after clear all")
	}
}

// TestEventLogPruneReportsStoreFailure pins that a failed persistent DELETE
// reaches the caller and leaves the ring intact, so `events clear` cannot report
// success for events a daemon restart would bring back.
func TestEventLogPruneReportsStoreFailure(t *testing.T) {
	store := &stubEventStore{pruneErr: errors.New("database is locked")}
	l, err := NewPersistentEventLog(10, store, nil)
	if err != nil {
		t.Fatalf("NewPersistentEventLog: %v", err)
	}
	l.Add(Event{Message: "kept"})

	if _, err := l.Prune(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("Prune err = %v, want the store failure", err)
	}
	if rem := l.Recent("", 0); len(rem) != 1 {
		t.Fatalf("ring after a failed prune = %+v, want the event kept", rem)
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
	first, err := state.OpenContextWith(context.Background(), path, state.Options{})
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

	second, err := state.OpenContextWith(context.Background(), path, state.Options{})
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

func TestWebBackendLastServiceEventReadsExternalPersistentWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), state.Filename)
	store, err := state.OpenContextWith(context.Background(), path, state.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	log, err := NewPersistentEventLog(10, store, nil)
	if err != nil {
		t.Fatalf("NewPersistentEventLog: %v", err)
	}
	if _, err := store.RecordEvent(state.EventRecord{
		Service: "web", Kind: eventKindAction, Action: string(rules.ActionStop), Status: eventStatusOK, Message: "stop ok",
	}); err != nil {
		t.Fatalf("external RecordEvent: %v", err)
	}

	b := &WebBackend{order: []string{"web"}, entries: map[string]*webEntry{"web": {}}, events: log}
	last := b.lastServiceEvent("web")
	if last == nil || last.Action != string(rules.ActionStop) || last.Status != eventStatusOK {
		t.Fatalf("last service event = %+v", last)
	}
	if listed := b.lastServiceEvents()["web"]; listed == nil || listed.ID != last.ID {
		t.Fatalf("listed service event = %+v, want ID %d", listed, last.ID)
	}
}

func TestWebBackendLastServiceEventIsNotBoundedByGlobalScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), state.Filename)
	store, err := state.OpenContextWith(context.Background(), path, state.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	log, err := NewPersistentEventLog(10, store, nil)
	if err != nil {
		t.Fatalf("NewPersistentEventLog: %v", err)
	}
	wanted, err := store.RecordEvent(state.EventRecord{
		Service: "web", Kind: eventKindAction, Action: string(rules.ActionStop), Status: eventStatusOK,
	})
	if err != nil {
		t.Fatalf("record service event: %v", err)
	}
	for range activitySummaryEventScanLimit + 1 {
		if _, err := store.RecordEvent(state.EventRecord{Service: "other", Kind: eventKindAction}); err != nil {
			t.Fatalf("record unrelated event: %v", err)
		}
	}

	b := &WebBackend{entries: map[string]*webEntry{"web": {}}, events: log}
	last := b.lastServiceEvent("web")
	if last == nil || last.ID != wanted {
		t.Fatalf("last service event = %+v, want persisted ID %d beyond the global scan", last, wanted)
	}
}

// stubEventStore records what Append writes and can also hold rows that were
// never appended through this log — the shape sermoctl produces when it runs an
// operation in its own process and writes the audit row straight to the state
// database.
type stubEventStore struct {
	rows     []state.EventRecord
	nextID   int64
	queries  []string
	failFor  string
	pruneErr error
}

func (s *stubEventStore) RecordEvent(rec state.EventRecord) (int64, error) {
	s.nextID++
	rec.ID = s.nextID
	s.rows = append(s.rows, rec)
	return rec.ID, nil
}

func (s *stubEventStore) RecentEventsBefore(_ int64, limit int) ([]state.EventRecord, error) {
	return s.newest(func(state.EventRecord) bool { return true }, limit), nil
}

func (s *stubEventStore) RecentEventsForColumn(column, name string, limit int) ([]state.EventRecord, error) {
	s.queries = append(s.queries, column+"="+name)
	if s.failFor == column {
		return nil, errors.New("store unavailable")
	}
	return s.newest(func(r state.EventRecord) bool {
		switch column {
		case state.EventColumnService:
			return r.Service == name
		case state.EventColumnApp:
			return r.App == name
		default:
			return r.Watch == name
		}
	}, limit), nil
}

func (s *stubEventStore) newest(match func(state.EventRecord) bool, limit int) []state.EventRecord {
	var out []state.EventRecord
	for _, v := range slices.Backward(s.rows) {
		if !match(v) {
			continue
		}
		out = append(out, v)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *stubEventStore) PruneEvents(context.Context, time.Time) (int64, error) {
	return 0, s.pruneErr
}

// sermoctl performs a service operation in its own process and records the
// audit row directly in the state database, so it never reaches the running
// daemon's ring. A per-service read that consulted only the ring omitted every
// operator action until a restart rehydrated it, while the global feed showed
// them — the exact CLI/WebUI disagreement observed on 192.0.2.35.
func TestRecentReadsOneServiceFromTheStoreNotTheRing(t *testing.T) {
	store := &stubEventStore{}
	l, err := NewPersistentEventLog(10, store, nil)
	if err != nil {
		t.Fatalf("NewPersistentEventLog: %v", err)
	}
	l.Add(Event{Service: "freshclam", Kind: eventKindError, Message: "older, through the daemon"})

	// Written by another process: in the store, absent from this ring.
	if _, err := store.RecordEvent(state.EventRecord{
		At: time.Unix(100, 0), Service: "freshclam", Kind: eventKindAction,
		Action: "restart", Status: eventStatusOK, Message: "restart ok",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	got := l.Recent("freshclam", 10)
	if len(got) != 2 {
		t.Fatalf("Recent(freshclam) = %d events, want 2 (ring miss means the store was not consulted)", len(got))
	}
	if got[0].Message != "restart ok" {
		t.Fatalf("newest event = %q, want the operator action recorded outside this process", got[0].Message)
	}
}

// The global feed (dashboard activity summary, Telegram /events) must include
// what sermoctl wrote straight to the store, exactly like the paged feed.
func TestRecentGlobalReadsTheStoreNotTheRing(t *testing.T) {
	store := &stubEventStore{}
	l, _ := NewPersistentEventLog(10, store, nil)
	l.Add(Event{Service: "a", Kind: eventKindError, Message: "one"})
	if _, err := store.RecordEvent(state.EventRecord{
		At: time.Unix(100, 0), Service: "freshclam", Kind: eventKindError, Message: "restart failed",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	got := l.Recent("", 10)
	if len(got) != 2 || got[0].Message != "restart failed" {
		t.Fatalf("Recent(\"\") = %+v, want the operator action recorded outside this process first", got)
	}
	for _, q := range store.queries {
		t.Fatalf("global read issued a per-target store query %q", q)
	}
}

// An unbounded global read stays bounded by the ring size when it goes to the
// store, instead of loading the whole persisted table.
func TestRecentGlobalUnboundedReadIsCappedAtRingSize(t *testing.T) {
	store := &stubEventStore{}
	l, _ := NewPersistentEventLog(2, store, nil)
	for _, msg := range []string{"1", "2", "3"} {
		l.Add(Event{Service: "a", Kind: eventKindError, Message: msg})
	}
	if got := l.Recent("", 0); len(got) != 2 || got[0].Message != "3" {
		t.Fatalf("Recent(\"\", 0) = %+v, want the newest two", got)
	}
}

// An unreadable store must not blank the view: fall back to the ring.
func TestRecentFallsBackToRingWhenStoreFails(t *testing.T) {
	store := &stubEventStore{failFor: state.EventColumnService}
	var storeErr error
	l, _ := NewPersistentEventLog(10, store, func(err error) { storeErr = err })
	l.Add(Event{Service: "freshclam", Kind: eventKindError, Message: "from the ring"})

	got := l.Recent("freshclam", 10)
	if len(got) != 1 || got[0].Message != "from the ring" {
		t.Fatalf("Recent(freshclam) = %+v, want the ring fallback", got)
	}
	if storeErr == nil {
		t.Fatal("store failure was not reported")
	}
}

// The delivery report of a recovery notification follows the recovery: a
// notifier that failed to hear it must not paint the recovered watch red, and
// the next firing is still the watch's last activity.
func TestEventLogRecoveryStaysTheWatchActivity(t *testing.T) {
	l := NewEventLog(10)
	l.Add(Event{Watch: "disk-root", Kind: eventKindFiring, Severity: severity.Critical})
	l.Add(Event{Watch: "disk-root", Kind: eventKindNotify, Message: "notified ops"})
	l.Add(Event{Watch: "disk-root", Kind: eventKindRecovered, Severity: severity.Critical})
	l.Add(Event{Watch: "disk-root", Kind: eventKindNotifyFail, Message: "ops: timeout"})
	l.Add(Event{Watch: "disk-root", Kind: eventKindNotify, Message: "notified pager"})
	if last, _ := l.LastWatchActivity("disk-root"); last.Kind != eventKindRecovered {
		t.Fatalf("last activity = %s, want recovered", last.Kind)
	}
	l.Add(Event{Watch: "disk-root", Kind: eventKindFiring, Severity: severity.Warning})
	l.Add(Event{Watch: "disk-root", Kind: eventKindNotifyFail, Message: "ops: timeout"})
	if last, _ := l.LastWatchActivity("disk-root"); last.Kind != eventKindNotifyFail {
		t.Fatalf("last activity = %s, want the firing's failed delivery", last.Kind)
	}
}

// A row an older binary wrote under the retired warning kind reads the way the
// migration converts one.
func TestLoggedEventNormalizesLegacyWarningRows(t *testing.T) {
	tests := []struct {
		rec  state.EventRecord
		kind string
	}{
		{state.EventRecord{Kind: legacyEventKindWarning, Watch: "disk-root", Message: "used 85%"}, eventKindFiring},
		{state.EventRecord{Kind: legacyEventKindWarning, Watch: "disk-root", Message: checkUnavailablePrefix + "statfs failed"}, eventKindError},
		{state.EventRecord{Kind: legacyEventKindWarning, Watch: "disk-root", Action: eventActionProbe}, eventKindError},
	}
	for _, tt := range tests {
		got := loggedEventFromRecord(tt.rec)
		if got.Kind != tt.kind || got.Severity != severity.Warning {
			t.Errorf("loggedEventFromRecord(%+v) = %s/%s, want %s/warning", tt.rec, got.Kind, got.Severity, tt.kind)
		}
	}
	// A row written with a severity is never rewritten.
	if got := loggedEventFromRecord(state.EventRecord{Kind: eventKindFiring, Severity: "critical"}); got.Kind != eventKindFiring || got.Severity != severity.Critical {
		t.Fatalf("graded row = %+v", got)
	}
}
