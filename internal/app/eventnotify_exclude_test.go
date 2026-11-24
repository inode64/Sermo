package app

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/notify"
)

func TestEventNotifyExcludesQualifiedSubjects(t *testing.T) {
	router, target, _ := outageRouter(t, nil, time.Minute)
	router.Update(config.EventNotification{Targets: []string{"ops"}, Exclude: []string{"watch:ping-*", "watch:pressure-io", "service:retired-*", "app:preview-*"}}, map[string]notify.Notifier{"ops": target})
	for _, event := range []Event{
		{Watch: "ping-gateway", Kind: eventKindFiring},
		{Watch: "pressure-io", Kind: eventKindFiring},
		{Service: "retired-api", Kind: eventKindFiring},
		{App: "preview-client", Kind: eventKindError},
		{Watch: "ping-gateway", Kind: eventKindAction, Notice: true},
	} {
		router.deliver(t.Context(), eventNotification{Event: event})
		event.Kind = eventKindRecovered
		event.Notice = false
		router.deliver(t.Context(), eventNotification{Event: event})
	}
	if len(target.messages) != 0 || len(router.memory) != 0 {
		t.Fatal("excluded subjects notified or opened delivery incidents")
	}
	for _, event := range []Event{
		{Service: "ping-gateway", Kind: eventKindFiring},
		{Watch: "pressure-cpu", Kind: eventKindFiring},
		{Watch: "web:port", Kind: eventKindFiring},
		{App: "client", Kind: eventKindError},
	} {
		router.deliver(t.Context(), eventNotification{Event: event})
	}
	if len(target.messages) != 4 {
		t.Fatalf("other alerts delivered %d messages, want 4", len(target.messages))
	}
}

func TestEventNotifyExclusionReloadClosesQuietlyAndStopsPending(t *testing.T) {
	store := openEventNotifyStore(t)
	router, target, now := outageRouter(t, store, time.Minute)
	event := Event{Watch: "ping-gateway", Kind: eventKindFiring}
	router.deliver(t.Context(), eventNotification{Event: event})
	router.Emit(event)
	queued := <-router.queue
	router.burst("ops").ordinary = eventNotifyBurstLimit
	router.dispatch(t.Context(), target, queued, notify.Message{Subject: "pending"}, false)
	router.Update(config.EventNotification{Targets: []string{"ops"}, Exclude: []string{"watch:ping-*"}, RepeatInterval: time.Minute}, map[string]notify.Notifier{"ops": target})
	router.deliver(t.Context(), queued)
	*now = now.Add(2 * time.Minute)
	router.remind(t.Context())
	if len(target.messages) != 1 || len(router.bursts["ops"].pending) != 0 {
		t.Fatal("excluded queue, summary or reminder delivered")
	}
	event.Kind = eventKindRecovered
	router.deliver(t.Context(), eventNotification{Event: event})
	key, _, _ := eventNotifyIdentity(event)
	rec, found, err := store.EventNotifyState(key, "ops")
	if err != nil || !found || rec.Active {
		t.Fatalf("receipt after quiet recovery = %+v, %t, %v", rec, found, err)
	}
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": target})
	event.Kind = eventKindFiring
	router.deliver(t.Context(), eventNotification{Event: event})
	if len(target.messages) != 2 {
		t.Fatal("removing exclusion did not allow a new incident")
	}
}

func TestEventNotifyExclusionStopsRetries(t *testing.T) {
	router, _, _ := outageRouter(t, nil, 0)
	target := &flakyNotifier{errs: []error{notify.Temporary(errors.New("temporarily unavailable"))}}
	policy := config.EventNotification{Targets: []string{"ops"}}
	configured := map[string]notify.Notifier{"ops": target}
	router.Update(policy, configured)
	router.sleep = func(time.Duration) {
		policy.Exclude = []string{"watch:ping-*"}
		router.Update(policy, configured)
	}
	router.deliver(t.Context(), eventNotification{Watch: "ping-gateway", Kind: eventKindFiring})
	if target.calls != 1 || len(router.memory) != 0 {
		t.Fatal("excluded notification retried or counted as delivered")
	}
}

func TestEventNotifyExclusionDropsDeferredReceiptRead(t *testing.T) {
	store := &failingEventNotifyStore{Store: openEventNotifyStore(t), readErr: errors.New("database unavailable")}
	router, target, now := outageRouter(t, store, 0)
	router.deliver(t.Context(), eventNotification{Watch: "ping-gateway", Kind: eventKindFiring})
	if len(router.unread) != 1 {
		t.Fatal("receipt lookup was not deferred")
	}
	router.Update(config.EventNotification{Targets: []string{"ops"}, Exclude: []string{"watch:ping-*"}}, map[string]notify.Notifier{"ops": target})
	store.readErr = nil
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(target.messages) != 0 || len(router.unread) != 0 {
		t.Fatal("deferred receipt lookup bypassed exclusion")
	}
}
