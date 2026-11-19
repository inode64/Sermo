package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/state"
	"sermo/internal/web"
)

// fakeMuteStore serves mutes from a map, or fails every read.
type fakeMuteStore struct {
	rows map[string]state.NotificationMuteRecord
	err  error
}

func (f fakeMuteStore) NotificationMute(key string) (state.NotificationMuteRecord, bool, error) {
	if f.err != nil {
		return state.NotificationMuteRecord{}, false, f.err
	}
	rec, ok := f.rows[key]
	return rec, ok, nil
}

func openMuteTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.OpenContextWith(t.Context(), filepath.Join(t.TempDir(), state.Filename), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestNotifyMuteGateHoldsUntilItsEndAndFailsOpen(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := fakeMuteStore{rows: map[string]state.NotificationMuteRecord{
		"web":            {Until: now.Add(time.Hour)},
		"watch:web:feed": {Until: now.Add(2 * time.Hour)},
	}}

	gate := notifyMuteGate(store, clock, notifyMuteKeys("web:feed")...)
	if muted, until := gate(); !muted || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("gate = %v until %v, want the latest end among the watch and its service", muted, until)
	}
	if muted, _ := notifyMuteGate(store, clock, notifyMuteKeys("web:other")...)(); !muted {
		t.Fatal("a service watch must follow its service's mute")
	}
	now = now.Add(time.Hour)
	if muted, _ := notifyMuteGate(store, clock, "web")(); muted {
		t.Fatal("a mute must end at its until")
	}
	failing := fakeMuteStore{err: errors.New("database is locked")}
	if muted, _ := notifyMuteGate(failing, clock, "web")(); muted {
		t.Fatal("an unreadable mute must let notifications go")
	}
	if notifyMuteGate(nil, clock, "web") != nil {
		t.Fatal("without a store nothing is muted")
	}
}

func TestApplyNotificationMuteBoundsAndUnmute(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{30 * time.Second, 25 * time.Hour} {
		if _, _, err := ApplyNotificationMute(store, "web", d, state.SourceCLI, now); err == nil {
			t.Fatalf("a %s mute must be refused", d)
		}
	}
	until, changed, err := ApplyNotificationMute(store, "web", 4*time.Hour, state.SourceCLI, now)
	if err != nil || !changed || !until.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("mute = %v %v %v", until, changed, err)
	}
	if got := NotificationMuteMessage(until, changed); !strings.HasPrefix(got, "notifications muted until ") {
		t.Fatalf("message = %q", got)
	}
	until, changed, err = ApplyNotificationMute(store, "web", 0, state.SourceCLI, now)
	if err != nil || !changed || !until.IsZero() || NotificationMuteMessage(until, changed) != "notifications unmuted" {
		t.Fatalf("unmute = %v %v %v", until, changed, err)
	}
	_, changed, _ = ApplyNotificationMute(store, "web", 0, state.SourceCLI, now)
	if changed || NotificationMuteMessage(time.Time{}, changed) != "notifications were not muted" {
		t.Fatalf("a second unmute must report nothing changed")
	}
}

// A muted alert reached no one, so its recovery must not either: a recovery
// of an alert nobody received reads as an incident nobody opened.
func TestWorkerNotifyMuteWithholdsAlertAndItsRecovery(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	w.Now = func() time.Time { return now }
	muted := true
	w.notifyMuted = func() (bool, time.Time) { return muted, now.Add(time.Hour) }

	w.RunCycle(context.Background())
	if len(n.msgs) != 0 {
		t.Fatalf("a muted service sent %d messages", len(n.msgs))
	}
	if _, ok := h.eventOf(eventKindAlert); !ok {
		t.Fatalf("the alert event must still be emitted: %+v", h.events)
	}
	if e, ok := h.eventOf(eventKindNotifySuppressed); !ok || !strings.HasPrefix(e.Message, "ops: notifications muted until ") {
		t.Fatalf("expected a notify-suppressed event naming the notifier: %+v", h.events)
	}

	muted = false
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	now = now.Add(time.Minute)
	w.RunCycle(context.Background())
	now = now.Add(rules.DefaultClearWindow + time.Minute)
	w.RunCycle(context.Background())
	if got := h.countEvents(eventKindRecovered); got != 1 {
		t.Fatalf("the rule must still recover, got %d: %+v", got, h.events)
	}
	if len(n.msgs) != 0 {
		t.Fatalf("the recovery of a muted alert was sent: %+v", n.msgs)
	}
}

func TestWatchNotifyMuteWithholdsFiringAndItsRecovery(t *testing.T) {
	n := &fakeNotifier{name: "ops-email"}
	var events []Event
	muted := true
	w := &Watch{
		Name:        "storage-root",
		CheckType:   "storage",
		Check:       stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Notifiers:   []notify.Notifier{n},
		Emit:        func(e Event) { events = append(events, e) },
		notifyMuted: func() (bool, time.Time) { return muted, time.Now().Add(time.Hour) },
	}
	w.RunCycle(context.Background())
	if len(n.msgs) != 0 {
		t.Fatalf("a muted watch sent %d messages", len(n.msgs))
	}
	if !hasEvent(events, eventKindFiring) || !hasEvent(events, eventKindNotifySuppressed) {
		t.Fatalf("want the firing event and a notify-suppressed one, got %v", events)
	}

	muted = false
	w.Check = stubCheck{name: "storage", ok: false, data: map[string]any{"path": "/", "used_pct": 40.0}}
	w.RunCycle(context.Background())
	if !hasEvent(events, eventKindRecovered) {
		t.Fatalf("the watch must still recover, got %v", events)
	}
	if len(n.msgs) != 0 {
		t.Fatalf("the recovery of a muted firing was sent: %+v", n.msgs)
	}
}

// event_notify withholds a muted service's events and its service watches',
// exactly as panic mode does, and sends again once the mute ends.
func TestEventNotifierWithholdsMutedTargets(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, store)
	router.now = func() time.Time { return now }
	router.NotifyMutes = store
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": recorder})
	if err := store.SetNotificationMute("apache", now.Add(time.Hour), state.SourceCLI); err != nil {
		t.Fatal(err)
	}

	router.deliver(t.Context(), eventNotification{Service: "apache", Kind: eventKindFiring, Message: "down"})
	router.deliver(t.Context(), eventNotification{Watch: "apache:queue", Kind: eventKindFiring, Message: "queue full"})
	if got := len(recorder.messages); got != 0 {
		t.Fatalf("a muted service and its watch sent %d messages", got)
	}
	router.deliver(t.Context(), eventNotification{Watch: "disk-root", Kind: eventKindFiring, Message: "space low"})
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("an unmuted watch delivered %d messages, want 1", got)
	}
	<-recorder.messages

	now = now.Add(time.Hour)
	router.deliver(t.Context(), eventNotification{Service: "apache", Kind: eventKindFiring, Message: "down"})
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("after the mute ended the service delivered %d messages, want 1", got)
	}
}

// A reminder of an incident announced before the mute waits for its end
// instead of being dropped.
func TestEventNotifierHoldsRemindersWhileMuted(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, store)
	router.now = func() time.Time { return now }
	router.NotifyMutes = store
	router.Update(config.EventNotification{Targets: []string{"ops"}, RepeatInterval: time.Hour}, map[string]notify.Notifier{"ops": recorder})

	router.deliver(t.Context(), eventNotification{Service: "apache", Kind: eventKindFiring, Message: "down"})
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("opening delivered %d messages, want 1", got)
	}
	<-recorder.messages
	if err := store.SetNotificationMute("apache", now.Add(3*time.Hour), state.SourceWeb); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	router.remind(t.Context())
	if got := len(recorder.messages); got != 0 {
		t.Fatalf("a muted incident was reminded %d times", got)
	}
	if _, err := store.ClearNotificationMute("apache"); err != nil {
		t.Fatal(err)
	}
	router.remind(t.Context())
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("after unmute the reminder sent %d messages, want 1", got)
	}
}

// A refused mute is audited as a blocked action, never as an error event:
// event_notify routes error events, and an operator's own bad request must not
// page anyone.
func TestWebBackendSetNotifyMuteAuditsAndPersists(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var events []Event
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
		watches: map[string]*webWatch{"web:queue": {name: "web:queue"}},
		store:   store,
		now:     func() time.Time { return now },
		emit:    func(e Event) { events = append(events, e) },
	}
	if err := b.SetServiceNotifyMute(t.Context(), "web", time.Hour); err != nil {
		t.Fatalf("mute: %v", err)
	}
	rec, found, err := store.NotificationMute("web")
	if err != nil || !found || !rec.Until.Equal(now.Add(time.Hour)) || rec.Source != state.SourceWeb {
		t.Fatalf("stored mute = %+v found=%v err=%v", rec, found, err)
	}
	if err := b.SetServiceNotifyMute(t.Context(), "web", 30*time.Second); err == nil {
		t.Fatal("a mute under the minimum must be refused")
	}
	if err := b.SetWatchNotifyMute(t.Context(), "nope", time.Hour); err == nil {
		t.Fatal("an unknown watch must be refused")
	}
	if err := b.SetServiceNotifyMute(t.Context(), "web", 0); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if err := b.SetServiceNotifyMute(t.Context(), "web", 0); err != nil {
		t.Fatalf("repeat unmute: %v", err)
	}
	if len(events) != 5 ||
		events[0].Kind != eventKindAction || events[0].Action != web.APIActionMute || events[0].Service != "web" ||
		events[1].Status != eventStatusBlocked || events[2].Status != eventStatusBlocked || events[2].Watch != "nope" ||
		events[3].Kind != eventKindAction || events[3].Action != web.APIActionUnmute ||
		events[4].Kind != eventKindSuppressed || events[4].Message != "notifications were not muted" {
		t.Fatalf("audit events = %+v", events)
	}
	for _, e := range events {
		if eventNeedsNotification(e) {
			t.Fatalf("a mute request event would reach event_notify: %+v", e)
		}
	}
}

func TestNotifyMuteViewMarksAMuteInheritedFromTheService(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	records := map[string]state.NotificationMuteRecord{
		"web":            {Until: now.Add(time.Hour), Source: state.SourceCLI},
		"watch:db:queue": {Until: now.Add(-time.Minute), Source: state.SourceWeb},
	}
	keys := notifyMuteKeys("web:queue")
	until, source, fromService := notifyMuteView(records, now, keys[0], keys[1:]...)
	if until != "2026-07-10T13:00:00Z" || source != state.SourceCLI || !fromService {
		t.Fatalf("inherited view = %q %q %v", until, source, fromService)
	}
	records["watch:web:queue"] = state.NotificationMuteRecord{Until: now.Add(30 * time.Minute), Source: state.SourceWeb}
	if _, _, fromService := notifyMuteView(records, now, keys[0], keys[1:]...); fromService {
		t.Fatal("a watch muted on its own can lift its own mute")
	}
	keys = notifyMuteKeys("db:queue")
	if until, _, _ := notifyMuteView(records, now, keys[0], keys[1:]...); until != "" {
		t.Fatalf("an expired mute must not show: %q", until)
	}
}
