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
	if muted, until := gate.target(); !muted || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("gate = %v until %v, want the latest end among the watch and its service", muted, until)
	}
	if muted, _ := notifyMuteGate(store, clock, notifyMuteKeys("web:other")...).target(); !muted {
		t.Fatal("a service watch must follow its service's mute")
	}
	now = now.Add(time.Hour)
	if muted, _ := notifyMuteGate(store, clock, "web").target(); muted {
		t.Fatal("a mute must end at its until")
	}
	failing := fakeMuteStore{err: errors.New("database is locked")}
	if muted, _ := notifyMuteGate(failing, clock, "web").target(); muted {
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
	mutes := fakeMuteStore{rows: map[string]state.NotificationMuteRecord{"web": {Until: now.Add(time.Hour)}}}
	w.notifyMuted = notifyMuteGate(mutes, w.Now, "web")

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

	delete(mutes.rows, "web")
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
	mutes := fakeMuteStore{rows: map[string]state.NotificationMuteRecord{"watch:storage-root": {Until: time.Now().Add(time.Hour)}}}
	w := &Watch{
		Name:        "storage-root",
		CheckType:   "storage",
		Check:       stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Notifiers:   []notify.Notifier{n},
		Emit:        func(e Event) { events = append(events, e) },
		notifyMuted: notifyMuteGate(mutes, nil, notifyMuteKeys("storage-root")...),
	}
	w.RunCycle(context.Background())
	if len(n.msgs) != 0 {
		t.Fatalf("a muted watch sent %d messages", len(n.msgs))
	}
	if !hasEvent(events, eventKindFiring) || !hasEvent(events, eventKindNotifySuppressed) {
		t.Fatalf("want the firing event and a notify-suppressed one, got %v", events)
	}

	delete(mutes.rows, "watch:storage-root")
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

// A muted notifier hears nothing while the others keep hearing: the mute is
// the channel's, not the target's.
func TestNotifierMuteWithholdsOnlyThatNotifier(t *testing.T) {
	email, chat := &fakeNotifier{name: "ops-email"}, &fakeNotifier{name: "ops-chat"}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mutes := fakeMuteStore{rows: map[string]state.NotificationMuteRecord{
		NotifierMuteKey("ops-email"): {Until: now.Add(time.Hour)},
	}}
	var events []Event
	w := &Watch{
		Name:        "storage-root",
		CheckType:   "storage",
		Check:       stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Notifiers:   []notify.Notifier{email, chat},
		Emit:        func(e Event) { events = append(events, e) },
		Now:         func() time.Time { return now },
		notifyMuted: notifyMuteGate(mutes, func() time.Time { return now }, notifyMuteKeys("storage-root")...),
	}
	w.RunCycle(context.Background())
	if len(email.msgs) != 0 || len(chat.msgs) != 1 {
		t.Fatalf("muted notifier got %d messages and the other %d, want 0 and 1", len(email.msgs), len(chat.msgs))
	}
	if !hasEventMessage(events, eventKindNotifySuppressed, "ops-email: notifier muted until ") {
		t.Fatalf("want a notify-suppressed event naming the muted notifier, got %v", events)
	}
	if hasEventMessage(events, eventKindNotifySuppressed, "ops-chat") {
		t.Fatalf("the unmuted notifier was reported as withheld: %v", events)
	}

	now = now.Add(time.Hour)
	w.Check = stubCheck{name: "storage", ok: false, data: map[string]any{"path": "/", "used_pct": 40.0}}
	w.RunCycle(context.Background())
	if len(email.msgs) != 1 || len(chat.msgs) != 2 {
		t.Fatalf("after the mute ended the recovery reached %d and %d messages, want 1 and 2", len(email.msgs), len(chat.msgs))
	}
}

// event_notify keeps one record per notifier, so a muted one never records
// the opening it missed: the recovery after the mute stays quiet for it, and
// an incident it did hear closes quietly on a recovery it misses.
func TestEventNotifierWithholdsAMutedNotifier(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	email, chat := &fakeNotifier{name: "ops-email"}, &fakeNotifier{name: "ops-chat"}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, store)
	router.now = func() time.Time { return now }
	router.NotifyMutes = store
	router.Update(config.EventNotification{Targets: []string{"ops-email", "ops-chat"}, RepeatInterval: time.Hour},
		map[string]notify.Notifier{"ops-email": email, "ops-chat": chat})

	router.deliver(t.Context(), eventNotification{Service: "apache", Kind: eventKindFiring, Message: "down"})
	if len(email.msgs) != 1 || len(chat.msgs) != 1 {
		t.Fatalf("opening reached %d and %d messages, want 1 and 1", len(email.msgs), len(chat.msgs))
	}
	if err := store.SetNotificationMute(NotifierMuteKey("ops-email"), now.Add(time.Hour), state.SourceWeb); err != nil {
		t.Fatal(err)
	}
	router.deliver(t.Context(), eventNotification{Watch: "disk-root", Kind: eventKindFiring, Message: "space low"})
	router.deliver(t.Context(), eventNotification{Service: "apache", Kind: eventKindRecovered, Message: "up"})
	if len(email.msgs) != 1 || len(chat.msgs) != 3 {
		t.Fatalf("during the mute the notifiers hold %d and %d messages, want 1 and 3", len(email.msgs), len(chat.msgs))
	}

	now = now.Add(2 * time.Hour)
	router.remind(t.Context())
	router.deliver(t.Context(), eventNotification{Watch: "disk-root", Kind: eventKindRecovered, Message: "space available"})
	if len(email.msgs) != 1 {
		t.Fatalf("the muted notifier was told later of what it missed: %+v", email.msgs[1:])
	}
	if len(chat.msgs) != 5 {
		t.Fatalf("the unmuted notifier holds %d messages, want its reminder and recovery too", len(chat.msgs))
	}
}

// A reminder due while its notifier is muted waits for the mute to end.
func TestEventNotifierHoldsRemindersOfAMutedNotifier(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, store)
	router.now = func() time.Time { return now }
	router.NotifyMutes = store
	router.Update(config.EventNotification{Targets: []string{"ops"}, RepeatInterval: time.Hour}, map[string]notify.Notifier{"ops": recorder})

	router.deliver(t.Context(), eventNotification{Service: "apache", Kind: eventKindFiring, Message: "down"})
	<-recorder.messages
	if err := store.SetNotificationMute(NotifierMuteKey("ops"), now.Add(3*time.Hour), state.SourceCLI); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	router.remind(t.Context())
	if got := len(recorder.messages); got != 0 {
		t.Fatalf("a muted notifier was reminded %d times", got)
	}
	now = now.Add(time.Hour)
	router.remind(t.Context())
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("after the mute ended the reminder sent %d messages, want 1", got)
	}
}

func TestWebBackendNotifierMuteAuditsAndShowsInTheList(t *testing.T) {
	store := openMuteTestStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var events []Event
	b := &WebBackend{
		notifiers:     map[string]*webNotifier{"ops": {name: "ops", typ: notify.TypeSlack, enabled: true}, "pager": {name: "pager", enabled: true}},
		notifierOrder: []string{"ops", "pager"},
		store:         store,
		now:           func() time.Time { return now },
		emit:          func(e Event) { events = append(events, e) },
	}
	if err := b.SetNotifierNotifyMute(t.Context(), "ops", 4*time.Hour); err != nil {
		t.Fatalf("mute: %v", err)
	}
	list := b.Notifiers(t.Context())
	if len(list) != 2 || list[0].NotifyMutedUntil != "2026-07-10T16:00:00Z" || list[0].NotifyMuteSource != state.SourceWeb || list[1].NotifyMutedUntil != "" {
		t.Fatalf("notifier list = %+v", list)
	}
	if err := b.SetNotifierNotifyMute(t.Context(), "nope", time.Hour); err == nil {
		t.Fatal("an unknown notifier must be refused")
	}
	if err := b.SetNotifierNotifyMute(t.Context(), "ops", 0); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if got := b.Notifiers(t.Context())[0].NotifyMutedUntil; got != "" {
		t.Fatalf("an unmuted notifier still shows muted until %q", got)
	}
	if len(events) != 3 ||
		events[0].Kind != eventKindAction || events[0].Action != web.APIActionMute || !strings.HasPrefix(events[0].Message, "notifier ops: notifications muted until ") ||
		events[1].Status != eventStatusBlocked || !strings.HasSuffix(events[1].Message, "unknown notifier nope") ||
		events[2].Action != web.APIActionUnmute || events[2].Message != "notifier ops: notifications unmuted" {
		t.Fatalf("audit events = %+v", events)
	}
	for _, e := range events {
		if eventNeedsNotification(e) {
			t.Fatalf("a mute request event would reach event_notify: %+v", e)
		}
	}
}
