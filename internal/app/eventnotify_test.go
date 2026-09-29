package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

type eventNotifyRecorder struct {
	messages chan notify.Message
}

func (n *eventNotifyRecorder) Name() string { return "ops" }
func (n *eventNotifyRecorder) Type() string { return notify.TypeSlack }
func (n *eventNotifyRecorder) Send(_ context.Context, msg notify.Message) error {
	n.messages <- msg
	return nil
}

func TestEventNotifierRoutesAllTargetsDespiteDryRun(t *testing.T) {
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 5)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewEventNotifier("host-a", logger, nil, nil)
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": recorder})
	ctx := t.Context()
	go router.Run(ctx)
	service := &Worker{Service: "apache", DryRun: true, Emit: router.Emit}
	watch := &Watch{Name: "storage-root", DryRun: true, Emit: router.Emit}
	service.emit(Event{Kind: eventKindFiring, Message: "connection refused"})
	watch.emit(Event{Watch: watch.Name, Kind: eventKindWarning, Message: "space low"})
	watch.emit(Event{Watch: watch.Name, Kind: eventKindRecovered, Message: "space available"})
	service.emit(Event{Kind: eventKindAlert, Rule: "latency", Message: "slow"})
	service.emit(Event{Kind: eventKindDryRun, Rule: "repair", Action: "restart", Message: "would restart"})
	for _, want := range []string{"apache: firing", "storage-root: warning", "storage-root: recovered", "apache: alert", "apache: dry-run"} {
		select {
		case msg := <-recorder.messages:
			if !strings.Contains(msg.Subject, want) {
				t.Fatalf("subject = %q, want %q", msg.Subject, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("notification %q not delivered", want)
		}
	}
}

func TestEventNotifierIncludesAutomaticRemediationOutcomesOnly(t *testing.T) {
	for _, e := range []Event{
		{Service: "apache", Kind: eventKindDryRun, Rule: "repair", Action: "restart"},
		{Service: "apache", Kind: eventKindSuppressed, Rule: "repair", Action: "restart"},
		{Service: "apache", Kind: eventKindAction, Rule: "repair", Action: "restart"},
	} {
		if !eventNeedsNotification(e) {
			t.Errorf("automatic remediation event omitted: %+v", e)
		}
	}
	for _, e := range []Event{
		{Service: "apache", Kind: eventKindAction, Action: "restart"},
		{Watch: "disk", Kind: eventKindDryRun, Message: "would expand"},
		{Service: "apache", Kind: eventKindSuppressed, Action: "restart"},
	} {
		if eventNeedsNotification(e) {
			t.Errorf("non-rule event was routed: %+v", e)
		}
	}
}

func TestEventNotifierSkipsRoutineAndPanicEvents(t *testing.T) {
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 1)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), func() bool { return true }, nil)
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": recorder})
	router.Emit(Event{Watch: "storage-root", Kind: eventKindAction, Message: "routine"})
	router.Emit(Event{Watch: "storage-root", Kind: eventKindFiring, Message: "space low"})
	ctx := t.Context()
	go router.Run(ctx)
	select {
	case msg := <-recorder.messages:
		t.Fatalf("unexpected message: %+v", msg)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestEventNotifierQueueDoesNotBlockMonitoring(t *testing.T) {
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": &eventNotifyRecorder{messages: make(chan notify.Message)}})
	for range eventNotifyQueueSize + 1 {
		router.Emit(Event{Service: "apache", Kind: eventKindFiring})
	}
}

func TestEventNotifierPersistsIncidentEdgesAndReminds(t *testing.T) {
	store, err := state.OpenContextWith(t.Context(), filepath.Join(t.TempDir(), state.Filename), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	configured := map[string]notify.Notifier{"ops": recorder}
	policy := config.EventNotification{Targets: []string{"ops"}, RepeatInterval: 24 * time.Hour}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	newRouter := func() *EventNotifier {
		router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, store)
		router.now = func() time.Time { return now }
		router.Update(policy, configured)
		return router
	}
	router := newRouter()
	event := Event{Watch: "security-user-postgres", Kind: eventKindFiring, Message: "pid 1: unresolved"}
	router.deliver(t.Context(), event)
	event.Message = "pid 2: unresolved"
	router.deliver(t.Context(), event)
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("PID-level events delivered %d times, want once", got)
	}
	router = newRouter() // same store, new daemon instance
	router.deliver(t.Context(), event)
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("restart repeated open incident: %d messages", got)
	}
	now = now.Add(24 * time.Hour)
	router.remind(t.Context())
	if got := len(recorder.messages); got != 2 {
		t.Fatalf("daily reminder count = %d, want 2", got)
	}
	<-recorder.messages
	if msg := <-recorder.messages; !strings.Contains(msg.Subject, "continues") {
		t.Fatalf("reminder subject = %q", msg.Subject)
	}
	router.deliver(t.Context(), Event{Watch: event.Watch, Kind: eventKindRecovered, Message: "no violations"})
	router.deliver(t.Context(), Event{Watch: event.Watch, Kind: eventKindRecovered, Message: "no violations"})
	if got := len(recorder.messages); got != 1 {
		t.Fatalf("recovery messages = %d, want once", got)
	}
	router.deliver(t.Context(), event)
	if got := len(recorder.messages); got != 2 {
		t.Fatalf("new episode did not alert: %d messages", got)
	}
}

func TestEventNotifierSeparatesChecksAndPacesErrors(t *testing.T) {
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": recorder})
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }
	for _, check := range []string{"http", "disk"} {
		router.deliver(t.Context(), Event{Service: "apache", Check: check, Kind: eventKindFiring})
	}
	if got := len(recorder.messages); got != 2 {
		t.Fatalf("independent checks delivered %d times, want 2", got)
	}
	errEvent := Event{Service: "apache", Rule: "repair", Kind: eventKindError, Message: "exit 1"}
	router.deliver(t.Context(), errEvent)
	errEvent.Message = "exit 2"
	router.deliver(t.Context(), errEvent)
	if got := len(recorder.messages); got != 3 {
		t.Fatalf("repeated error delivered %d times, want 3 total", got)
	}
	now = now.Add(24 * time.Hour)
	router.deliver(t.Context(), errEvent)
	if got := len(recorder.messages); got != 4 {
		t.Fatalf("daily error delivered %d times, want 4 total", got)
	}
}

func TestEventNotifierSendsEveryOneShotNoticeWithoutReminders(t *testing.T) {
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	router.Update(config.EventNotification{Targets: []string{"ops"}, RepeatInterval: time.Hour}, map[string]notify.Notifier{"ops": recorder})
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }
	var reclaims []Event
	reclaim := operationLockReclaimEvent(func(e Event) { reclaims = append(reclaims, e) })
	reclaim("nginx", "expired")
	reclaim("nginx", "dead owner")
	notices := []Event{
		{Service: "nginx", Kind: eventKindAlert, Rule: restartNoticeRule, Message: "nginx restarted (pid 10)", Notice: true},
		{Service: "nginx", Kind: eventKindAlert, Rule: restartNoticeRule, Message: "nginx restarted (pid 20)", Notice: true},
	}
	notices = append(notices, reclaims...)
	for _, e := range notices {
		router.deliver(t.Context(), e)
		now = now.Add(10 * time.Minute)
	}
	if got := len(recorder.messages); got != len(notices) {
		t.Fatalf("one-shot notices delivered %d times, want %d", got, len(notices))
	}
	for range notices {
		<-recorder.messages
	}
	now = now.Add(3 * time.Hour)
	router.remind(t.Context())
	if got := len(recorder.messages); got != 0 {
		t.Fatalf("one-shot notices were reminded %d times, want none", got)
	}
}

func TestEventNotifierRemediationEpisodesRealertAfterRecovery(t *testing.T) {
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10)}
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": recorder})
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	now := t0
	w.Now = func() time.Time { return now }
	for _, failing := range []bool{true, false, true} {
		h.cache = map[string]checks.Result{"http": {Check: "http", OK: !failing}}
		w.RunCycle(context.Background())
		now = now.Add(2 * time.Minute)
	}
	for _, e := range h.events {
		router.deliver(t.Context(), e)
	}
	want := []string{"web: action", "web: recovered", "web: action"}
	if got := len(recorder.messages); got != len(want) {
		t.Fatalf("remediation episodes delivered %d messages, want %d (events %+v)", got, len(want), h.events)
	}
	for _, subject := range want {
		if msg := <-recorder.messages; !strings.Contains(msg.Subject, subject) {
			t.Fatalf("subject = %q, want %q", msg.Subject, subject)
		}
	}
}
