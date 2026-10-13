package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/severity"
)

func TestEventNotifierBurstSummariesAndPriority(t *testing.T) {
	router, target, now := outageRouter(t, nil, 0)
	for i := range eventNotifyBurstLimit + 10 {
		router.deliver(t.Context(), Event{Watch: fmt.Sprintf("disk-%02d", i), Kind: eventKindFiring, Severity: severity.Warning})
	}
	if len(target.messages) != eventNotifyBurstLimit {
		t.Fatalf("burst=%d", len(target.messages))
	}
	router.deliver(t.Context(), Event{Service: "mysql", Check: "health", Kind: eventKindFiring, Severity: severity.Critical})
	// Escalation to error also uses the reserved budget.
	router.deliver(t.Context(), Event{Watch: "disk-00", Kind: eventKindFiring, Severity: severity.Error})
	if len(target.messages) != eventNotifyBurstLimit+2 {
		t.Fatal("critical/escalation not prioritized")
	}
	key, _, _ := eventNotifyIdentity(Event{Watch: "disk-25", Kind: eventKindFiring})
	if _, found, _ := router.load(key, "ops"); found {
		t.Fatal("queued summary counted as delivered")
	}
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(target.messages) != eventNotifyBurstLimit+3 {
		t.Fatalf("summary=%d", len(target.messages))
	}
	if rec, found, _ := router.load(key, "ops"); !found || !rec.Active {
		t.Fatal("confirmed summary not recorded")
	}
	var last notify.Message
	for len(target.messages) > 0 {
		last = <-target.messages
	}
	if !strings.Contains(last.Subject, "summary") || !strings.Contains(last.Body, "10 grouped notifications") || !strings.Contains(last.Body, "disk-25") {
		t.Fatalf("summary=%+v", last)
	}
}

func TestEventNotifierDeferredRecoveryReconcilesLatestState(t *testing.T) {
	router, target, now := outageRouter(t, nil, 0)
	for i := range eventNotifyBurstLimit {
		router.deliver(t.Context(), Event{Watch: fmt.Sprint(i), Kind: eventKindFiring})
	}
	// Never announce a recovery for a deferred opening that was never delivered.
	router.deliver(t.Context(), Event{Watch: "brief", Kind: eventKindFiring})
	router.deliver(t.Context(), Event{Watch: "brief", Kind: eventKindRecovered})
	// A deferred recovery becomes obsolete when the fault returns.
	router.deliver(t.Context(), Event{Watch: "0", Kind: eventKindRecovered})
	router.deliver(t.Context(), Event{Watch: "0", Kind: eventKindFiring})
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(target.messages) != eventNotifyBurstLimit || len(router.bursts["ops"].pending) != 0 {
		t.Fatal("obsolete recovery delivered")
	}
}

func TestEventNotifierFailedSummaryKeepsPendingWithoutReceipts(t *testing.T) {
	router, _, now := outageRouter(t, nil, 0)
	target := &flakyNotifier{errs: []error{errors.New("rejected")}}
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": target})
	burst := router.burst("ops")
	burst.ordinary = eventNotifyBurstLimit
	event := Event{Watch: "root", Kind: eventKindFiring}
	router.deliver(t.Context(), event)
	router.remind(t.Context())
	router.remind(t.Context())
	if target.calls != 1 || len(router.memory) != 0 || len(burst.pending) != 1 {
		t.Fatal("failed summary acknowledged or retried too soon")
	}
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if target.calls != 2 || len(router.memory) != 1 || len(burst.pending) != 0 {
		t.Fatal("summary retry failed")
	}
}

func TestEventNotifierBurstCapacityAndCriticalBudget(t *testing.T) {
	router, target, now := outageRouter(t, nil, 0)
	for i := range 1000 {
		router.deliver(t.Context(), Event{Watch: fmt.Sprint(i), Kind: eventKindFiring, Severity: severity.Critical})
	}
	burst := router.bursts["ops"]
	if len(target.messages) != eventNotifyBurstLimit+eventNotifyPriorityLimit || len(burst.pending) != eventNotifyPendingLimit || burst.overflow == 0 {
		t.Fatal("unbounded burst")
	}
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(target.messages) != eventNotifyBurstLimit+eventNotifyPriorityLimit+1 {
		t.Fatal("summary budget not bounded")
	}
}

func TestEventNotifierPanicHoldsSummaryButFlushesReceipts(t *testing.T) {
	store := &failingEventNotifyStore{Store: openRuleStateStore(t), writeErr: errors.New("full")}
	router, target, now := outageRouter(t, store, 0)
	for i := range eventNotifyBurstLimit + 1 {
		router.deliver(t.Context(), Event{Watch: fmt.Sprint(i), Kind: eventKindFiring})
	}
	router.inPanic = func() bool { return true }
	store.writeErr = nil
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(router.memory) != 0 || len(target.messages) != eventNotifyBurstLimit || len(router.bursts["ops"].pending) != 1 {
		t.Fatal("panic persistence/delivery boundaries wrong")
	}
}

func TestEventNotifierSummaryHonorsChannelSelectionAndReload(t *testing.T) {
	router, _, now := outageRouter(t, nil, 0)
	ops := &floorNotifier{name: "ops", floor: severity.Warning}
	critical := &floorNotifier{name: "critical", floor: severity.Critical}
	router.Update(config.EventNotification{Targets: []string{"ops", "critical"}}, map[string]notify.Notifier{"ops": ops, "critical": critical})
	for i := range eventNotifyBurstLimit + 1 {
		router.deliver(t.Context(), Event{Watch: fmt.Sprint(i), Kind: eventKindFiring, Severity: severity.Warning})
	}
	if len(critical.msgs) != 0 {
		t.Fatal("warning reached critical channel")
	}
	ops.floor = severity.Critical
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(ops.msgs) != eventNotifyBurstLimit || len(router.bursts["ops"].pending) != 0 {
		t.Fatal("reload severity floor ignored by summary")
	}
	router.deliver(t.Context(), Event{Watch: "0", Kind: eventKindRecovered})
	if len(ops.msgs) != eventNotifyBurstLimit+1 || len(critical.msgs) != 0 {
		t.Fatal("recovery did not follow actual recipients")
	}
}

func TestEventNotifierRetainDiscardsDeferredRemovedWatch(t *testing.T) {
	router, target, now := outageRouter(t, nil, 0)
	for i := range eventNotifyBurstLimit + 1 {
		router.deliver(t.Context(), Event{Watch: fmt.Sprint(i), Kind: eventKindFiring})
	}
	router.Retain(eventNotifyScope{watches: map[string]bool{"0": true}})
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	if len(target.messages) != eventNotifyBurstLimit || len(router.bursts["ops"].pending) != 0 {
		t.Fatal("removed watch delivered after reload")
	}
}

func TestEventNotifierFailedRecoverySummaryCannotBecomeReminder(t *testing.T) {
	router, _, now := outageRouter(t, nil, time.Hour)
	target := &fakeNotifier{name: "ops"}
	router.Update(config.EventNotification{Targets: []string{"ops"}, RepeatInterval: time.Hour}, map[string]notify.Notifier{"ops": target})
	router.deliver(t.Context(), Event{Watch: "root", Kind: eventKindFiring})
	*now = now.Add(time.Hour)
	router.burst("ops").ordinary = eventNotifyBurstLimit
	router.deliver(t.Context(), Event{Watch: "root", Kind: eventKindRecovered})
	target.fail = true
	router.remind(t.Context())
	key, _, _ := eventNotifyIdentity(Event{Watch: "root", Kind: eventKindRecovered})
	if pending := router.bursts["ops"].pending[key]; pending.record.Phase != eventNotifyPhaseRecovered {
		t.Fatal("stale reminder replaced pending recovery")
	}
	target.fail = false
	*now = now.Add(time.Minute)
	router.remind(t.Context())
	rec, _, _ := router.load(key, "ops")
	if rec.Active {
		t.Fatal("recovery summary failed to close incident")
	}
}

func TestEventNotifierQueuedEscalationSurvivesBudgetReset(t *testing.T) {
	router, target, now := outageRouter(t, nil, 0)
	b := router.burst("ops")
	b.ordinary, b.priority = eventNotifyBurstLimit, eventNotifyPriorityLimit
	event := Event{Watch: "root", Kind: eventKindFiring, Severity: severity.Critical}
	router.deliver(t.Context(), event)
	*now = now.Add(time.Minute)
	event.Severity = severity.Warning
	router.deliver(t.Context(), event)
	router.remind(t.Context())
	key, _, _ := eventNotifyIdentity(event)
	rec, found, err := router.load(key, "ops")
	if err != nil || !found || rec.Severity != severity.Critical.String() || len(target.messages) != 1 {
		t.Fatalf("queued critical alert lost: record=%+v found=%t err=%v messages=%d", rec, found, err, len(target.messages))
	}
	if msg := <-target.messages; !strings.Contains(msg.Subject, "summary") || msg.Severity != severity.Critical {
		t.Fatalf("message=%+v; want queued critical summary", msg)
	}
}
