package app

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/severity"
	"sermo/internal/state"
)

type failingEventNotifyStore struct {
	*state.Store
	writeErr error
	readErr  error
	writes   int
}

func (s *failingEventNotifyStore) SetEventNotifyState(rec state.EventNotifyRecord) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.Store.SetEventNotifyState(rec)
}

func (s *failingEventNotifyStore) EventNotifyState(key, target string) (state.EventNotifyRecord, bool, error) {
	if s.readErr != nil {
		return state.EventNotifyRecord{}, false, s.readErr
	}
	return s.Store.EventNotifyState(key, target)
}

func outageRouter(t *testing.T, store EventNotifyStore, interval time.Duration) (*EventNotifier, *eventNotifyRecorder, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	recorder := &eventNotifyRecorder{messages: make(chan notify.Message, 10000)}
	router := NewEventNotifier("host", slog.New(slog.DiscardHandler), nil, store)
	router.now = func() time.Time { return now }
	router.Update(config.EventNotification{Targets: []string{"ops"}, RepeatInterval: interval}, map[string]notify.Notifier{"ops": recorder})
	return router, recorder, &now
}

func TestEventNotifierFullDiskKeepsConfirmedEdges(t *testing.T) {
	store := &failingEventNotifyStore{Store: openRuleStateStore(t), writeErr: errors.New("database or disk is full")}
	router, target, now := outageRouter(t, store, 0)
	var logs bytes.Buffer
	router.logger = slog.New(slog.NewTextHandler(&logs, nil))
	event := Event{Watch: "root", Kind: eventKindFiring, Severity: severity.Warning}
	for i := range 10000 {
		event.Message = fmt.Sprintf("free bytes %d", i)
		router.deliver(t.Context(), event)
	}
	if len(target.messages) != 1 || store.writes != 1 {
		t.Fatalf("messages=%d writes=%d", len(target.messages), store.writes)
	}
	// Receipt overlay works even when later reads fail as well.
	store.readErr = errors.New("read unavailable")
	event.Severity = severity.Critical
	router.deliver(t.Context(), event)
	router.deliver(t.Context(), event)
	event.Kind = eventKindRecovered
	router.deliver(t.Context(), event)
	router.deliver(t.Context(), event)
	if len(target.messages) != 3 {
		t.Fatalf("opening/escalation/recovery=%d", len(target.messages))
	}
	store.readErr, store.writeErr = nil, nil
	*now = now.Add(time.Minute)
	router.remind(t.Context()) // no repeat_interval: persistence must still heal
	if len(router.memory) != 0 || len(target.messages) != 3 {
		t.Fatal("flush lost receipts or resent messages")
	}
	if strings.Count(logs.String(), "unavailable") != 1 || !strings.Contains(logs.String(), "persistence recovered") {
		t.Fatalf("logs=%s", logs.String())
	}
	restarted, _, _ := outageRouter(t, store, 0)
	restarted.now = router.now
	restarted.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": target})
	restarted.deliver(t.Context(), event)
	if len(target.messages) != 3 {
		t.Fatal("restart repeated recovery")
	}
	event.Kind = eventKindFiring
	restarted.deliver(t.Context(), event)
	if len(target.messages) != 4 {
		t.Fatal("next episode not delivered")
	}
}

func TestEventNotifierFullDiskOverlaysStaleReminders(t *testing.T) {
	store := &failingEventNotifyStore{Store: openRuleStateStore(t)}
	router, target, now := outageRouter(t, store, time.Hour)
	event := Event{Watch: "root", Kind: eventKindFiring}
	router.deliver(t.Context(), event)
	store.writeErr = errors.New("full")
	*now = now.Add(time.Hour)
	router.remind(t.Context())
	for range 100 {
		router.remind(t.Context())
	}
	if len(target.messages) != 2 {
		t.Fatalf("reminders=%d", len(target.messages))
	}
	event.Kind = eventKindRecovered
	router.deliver(t.Context(), event)
	*now = now.Add(time.Hour)
	router.remind(t.Context())
	if len(target.messages) != 3 {
		t.Fatal("stale DB row reminded closed episode")
	}
	store.writeErr = nil
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(router.memory) != 0 || len(target.messages) != 3 {
		t.Fatal("recovery flush resent messages")
	}
}

func TestEventNotifierOutageRetainAndCapacity(t *testing.T) {
	store := &failingEventNotifyStore{Store: openRuleStateStore(t), writeErr: errors.New("full")}
	router, target, now := outageRouter(t, store, time.Hour)
	router.deliver(t.Context(), Event{Watch: "removed", Kind: eventKindFiring})
	router.Retain(eventNotifyScope{watches: map[string]bool{"kept": true}})
	store.writeErr = nil
	*now = now.Add(time.Hour)
	router.remind(t.Context())
	if len(router.memory) != 0 || len(target.messages) != 1 {
		t.Fatal("removed receipt resurrected")
	}
	for i := range eventNotifyMemoryLimit {
		rec := state.EventNotifyRecord{IncidentKey: fmt.Sprint(i), Notifier: "ops"}
		router.memory[eventNotifyRecordKey(rec.IncidentKey, rec.Notifier)] = rec
	}
	router.deliver(t.Context(), Event{Watch: "new", Kind: eventKindFiring})
	if len(target.messages) != 1 || len(router.memory) != eventNotifyMemoryLimit {
		t.Fatal("capacity evicted a receipt or sent without room")
	}
}

func TestEventNotifierStoreReadFailureDoesNotSendUntrackedAlert(t *testing.T) {
	store := &failingEventNotifyStore{Store: openRuleStateStore(t), readErr: errors.New("unreadable")}
	router, target, _ := outageRouter(t, store, 0)
	router.deliver(t.Context(), Event{Watch: "root", Kind: eventKindFiring})
	if len(target.messages) != 0 {
		t.Fatal("sent without knowing previous state")
	}
	store.readErr = nil
	router.deliver(t.Context(), Event{Watch: "root", Kind: eventKindFiring})
	if len(target.messages) != 1 {
		t.Fatal("read recovery suppressed opening")
	}
}

func TestEventNotifierReloadKeepsUndurableOperationalErrorReceipt(t *testing.T) {
	store := &failingEventNotifyStore{Store: openRuleStateStore(t), writeErr: errors.New("full")}
	router, target, _ := outageRouter(t, store, 0)
	event := Event{Service: "mysql", Kind: eventKindError, Message: "write state failed"}
	router.deliver(t.Context(), event)
	router.Retain(eventNotifyScope{services: map[string]eventNotifyServiceScope{"mysql": {}}})
	router.deliver(t.Context(), event)
	if len(target.messages) != 1 {
		t.Fatal("reload forgot operational-error receipt during disk outage")
	}
}
