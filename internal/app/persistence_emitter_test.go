package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"sermo/internal/severity"
	"sermo/internal/state"
)

type failingWatchStateStore struct {
	*state.Store
	writeErr error
	writes   int
}

func (s *failingWatchStateStore) SetWatchRuntimeState(name, slot string, rec state.WatchRuntimeRecord) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.Store.SetWatchRuntimeState(name, slot, rec)
}

func TestPersistenceEventsWatchRetriesAfterReturningToPersistedState(t *testing.T) {
	store := &failingWatchStateStore{Store: openRuleStateStore(t)}
	var events []Event
	p := NewPersistenceEvents(func(e Event) { events = append(events, e) }, nil)
	w := &Watch{Name: "root", StateStore: store, Emit: p.Emit, stateLoaded: true}
	w.persistedState = w.runtimeRecord()
	store.writeErr = errors.New("disk full")
	w.firing = true
	w.persistRuntimeState()
	store.writeErr = nil
	w.firing = false // back to the last successful snapshot
	w.persistRuntimeState()
	w.persistRuntimeState() // successful retry restores the change gate
	if store.writes != 2 || len(events) != 2 || events[1].Kind != eventKindRecovered {
		t.Fatalf("writes=%d events=%+v; want one failed write, one retry and recovery", store.writes, events)
	}
}

func TestPersistenceEventsPIDStateRetriesWithoutAnotherTransition(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
			store := &failingWatchStateStore{Store: openRuleStateStore(t), writeErr: errors.New("disk full")}
			var events []Event
			p := NewPersistenceEvents(func(e Event) { events = append(events, e) }, nil)
			tracker := newPIDIncidents("processes", processPolicyStateSlot, 0, severity.Warning, store, p.Emit)
			tracker.activeLoaded, tracker.active = true, !active
			offenders := 0
			if active {
				offenders = 1
			}
			tracker.settle(offenders)
			store.writeErr = nil
			tracker.settle(offenders)
			tracker.settle(offenders)
			rec, found, err := store.WatchRuntimeState(tracker.watch, tracker.slot)
			if err != nil || found != active || rec.Firing != active || store.writes != 2 || len(events) != 2 || events[1].Kind != eventKindRecovered {
				t.Fatalf("record=%+v found=%t err=%v writes=%d events=%+v", rec, found, err, store.writes, events)
			}
		})
	}
}

func TestPersistenceEventsQueryPolicyRetriesWithoutAnotherAction(t *testing.T) {
	f := newDBQueryFixture(t)
	store := &failingWatchStateStore{Store: openRuleStateStore(t), writeErr: errors.New("disk full")}
	f.w.stateStore = store
	f.w.emit = NewPersistenceEvents(f.w.emit, nil).Emit
	f.w.policyState.LastActionAt = f.now
	f.w.persistPolicyState()
	store.writeErr = nil
	f.cycle() // no statements and no killer: retry must not repeat an action
	f.cycle()
	rec, found, err := store.WatchRuntimeState(f.w.name, dbQueryPolicyStateSlot)
	if err != nil || !found || !rec.Policy.LastActionAt.Equal(f.now) || store.writes != 2 || len(f.events) != 2 || f.events[1].Kind != eventKindRecovered {
		t.Fatalf("record=%+v found=%t err=%v writes=%d events=%+v", rec, found, err, store.writes, f.events)
	}
}

func TestPersistenceEventsGroupsWritersAndPreservesOperationAudit(t *testing.T) {
	var events []Event
	p := NewPersistenceEvents(func(e Event) { events = append(events, e) }, nil)
	now := t0
	p.now = func() time.Time { return now }
	for range 1000 {
		for _, service := range []string{"mysql", "nginx"} {
			p.Emit(Event{Service: service, Kind: eventKindError, persistenceSource: "rules", Message: "disk full"})
		}
		p.Emit(Event{Watch: "root", Kind: eventKindError, persistenceSource: "result", Message: "disk full"})
	}
	if len(events) != 1 || events[0].Check != persistenceCheck || events[0].Service != "" {
		t.Fatalf("events=%+v", events)
	}
	now = now.Add(time.Minute)
	p.Emit(Event{Watch: "root", Kind: eventKindError, persistenceSource: "result", Message: "still full"})
	if len(events) != 2 {
		t.Fatal("no bounded log reminder")
	}
	for _, service := range []string{"mysql", "nginx"} {
		p.Emit(Event{Service: service, Kind: eventKindPersistence, persistenceSource: "rules"})
	}
	if len(events) != 2 {
		t.Fatal("recovered while watch writer still failing")
	}
	p.Emit(Event{Watch: "root", Kind: eventKindPersistence, persistenceSource: "result"})
	p.Emit(Event{Watch: "root", Kind: eventKindPersistence, persistenceSource: "result"})
	if len(events) != 3 || events[2].Kind != eventKindRecovered {
		t.Fatal("missing or duplicate recovery")
	}
	for _, kind := range []string{eventKindAction, eventKindError, eventKindSuppressed} {
		e := Event{Service: "mysql", Kind: kind, Action: "restart", Message: "audit"}
		p.Emit(e)
		if events[len(events)-1] != e {
			t.Fatal("operation audit altered")
		}
	}
}

func TestPersistenceEventsRetainsOnlyConfiguredWriters(t *testing.T) {
	var events []Event
	p := NewPersistenceEvents(func(e Event) { events = append(events, e) }, nil)
	for i := range 10 {
		p.Emit(Event{Watch: fmt.Sprint(i), Kind: eventKindError, persistenceSource: "result"})
	}
	p.retain(eventNotifyScope{watches: map[string]bool{"0": true}})
	if len(p.failed) != 1 || len(events) != 1 {
		t.Fatal("retention lost active failure")
	}
	// First successful write by a replacement worker after reload closes it.
	p.Emit(Event{Watch: "0", Kind: eventKindPersistence, persistenceSource: "result"})
	if len(events) != 2 || events[1].Kind != eventKindRecovered {
		t.Fatal("replacement worker did not recover incident")
	}
}

func TestPersistenceEventsSlowLogDoesNotBlockOtherWriters(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	events := make(chan Event, 2)
	p := NewPersistenceEvents(func(e Event) {
		if e.Kind == eventKindFiring {
			close(entered)
			<-release
		}
		events <- e
	}, nil)
	go func() { p.Emit(Event{Watch: "root", Kind: eventKindError, persistenceSource: "result"}); close(done) }()
	<-entered
	recovered := make(chan struct{})
	go func() {
		p.Emit(Event{Watch: "root", Kind: eventKindPersistence, persistenceSource: "result"})
		close(recovered)
	}()
	select {
	case <-recovered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("another worker blocked on log I/O")
	}
	close(release)
	<-done
	if first, second := <-events, <-events; first.Kind != eventKindFiring || second.Kind != eventKindRecovered {
		t.Fatal("transitions reordered")
	}
}

func TestPersistenceEventsClosesStoredIncidentAfterRestart(t *testing.T) {
	store := openRuleStateStore(t)
	router, target, _ := outageRouter(t, store, 0)
	router.deliver(t.Context(), Event{Kind: eventKindFiring, Check: persistenceCheck})
	restarted := NewPersistenceEvents(func(e Event) { router.deliver(t.Context(), e) }, router)
	restarted.Emit(Event{Watch: "root", Kind: eventKindPersistence, persistenceSource: "result"})
	restarted.Emit(Event{Watch: "root", Kind: eventKindPersistence, persistenceSource: "result"})
	if len(target.messages) != 2 {
		t.Fatalf("opening and single recovery: %d messages", len(target.messages))
	}
	key, _, _ := eventNotifyIdentity(Event{Kind: eventKindFiring, Check: persistenceCheck})
	rec, found, err := router.load(key, "ops")
	if err != nil || !found || rec.Active {
		t.Fatalf("reconciled receipt=%+v found=%v err=%v", rec, found, err)
	}
}

func TestPersistenceEventsGroupsRepeatedCycleWriteFailures(t *testing.T) {
	var events []Event
	p := NewPersistenceEvents(func(e Event) { events = append(events, e) }, nil)
	p.now = func() time.Time { return t0 }
	store := &failingBatchStore{err: errors.New("disk full")}
	writer := newCycleWriter(Deps{SLA: store, Now: func() time.Time { return t0 }, Emit: p.Emit}, "mysql", nil)
	for range 1000 {
		writer.RecordCycle(t.Context(), cycleRecord{up: true, recordAvailability: true})
	}
	if len(events) != 1 {
		t.Fatalf("cycle failures emitted %d events", len(events))
	}
	store.err = nil
	writer.RecordCycle(t.Context(), cycleRecord{up: true, recordAvailability: true})
	if len(events) != 2 || events[1].Kind != eventKindRecovered {
		t.Fatal("cycle writer recovery not reported")
	}
}
