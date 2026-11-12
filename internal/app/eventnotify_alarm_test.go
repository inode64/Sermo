package app

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/severity"
	"sermo/internal/state"
)

func TestWatchSilenceInvalidatesQueuedEventsSummariesAndReminders(t *testing.T) {
	store := openEventNotifyStore(t)
	alarms := NewWatchAlarms(store, nil)
	router, target, now := outageRouter(t, store, time.Minute)
	router.WatchAlarms = alarms
	fault := state.WatchAlarmSlot{Active: true, Severity: severity.Warning.String()}
	event := Event{Watch: "disk", Kind: eventKindFiring, Severity: severity.Warning}
	alarms.Observe("disk", "space", "config", fault)
	router.Emit(event)
	router.deliver(t.Context(), <-router.queue)
	if len(target.messages) != 1 {
		t.Fatal("first alarm not sent")
	}
	router.Emit(event)
	queued := <-router.queue
	router.burst("ops").ordinary = eventNotifyBurstLimit
	router.dispatch(t.Context(), target, eventNotification{Event: event}, notify.Message{Subject: "pending alarm"}, false)
	silenceAlarm(t, alarms, "disk")
	router.Emit(event)
	if len(router.queue) != 0 {
		t.Fatal("silenced event queued")
	}
	router.deliver(t.Context(), queued)
	*now = now.Add(2 * time.Minute)
	router.remind(t.Context())
	if len(target.messages) != 1 || len(router.bursts["ops"].pending) != 0 {
		t.Fatal("silenced queued alarm or reminder delivered")
	}
	// The old receipt stays invalid after a restart and another metric's change.
	router, target, now = outageRouter(t, store, time.Minute)
	router.WatchAlarms = NewWatchAlarms(store, nil)
	*now = now.Add(3 * time.Minute)
	router.remind(t.Context())
	if len(target.messages) != 0 {
		t.Fatal("restart resurrected a muted reminder")
	}
	alarms = router.WatchAlarms
	alarms.Observe("disk", "inodes", "config", fault)
	router.deliver(t.Context(), queued)
	router.remind(t.Context())
	if len(target.messages) != 0 {
		t.Fatal("rearm resurrected an old reminder")
	}
	router.Emit(event)
	router.deliver(t.Context(), <-router.queue)
	if len(target.messages) != 1 {
		t.Fatal("old receipt suppressed the new occurrence")
	}
}

func TestWatchSilenceStopsNotificationRetries(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	alarms.Observe("disk", "space", "", state.WatchAlarmSlot{Active: true})
	router, _, _ := outageRouter(t, nil, 0)
	router.WatchAlarms = alarms
	target := &flakyNotifier{errs: []error{notify.Temporary(errors.New("temporarily unavailable"))}}
	router.Update(config.EventNotification{Targets: []string{"ops"}}, map[string]notify.Notifier{"ops": target})
	router.sleep = func(time.Duration) { silenceAlarm(t, alarms, "disk") }
	router.deliver(t.Context(), eventNotification{Watch: "disk", Kind: eventKindFiring})
	if target.calls != 1 {
		t.Fatalf("retried silenced alarm %d times", target.calls)
	}
	if len(router.memory) != 0 {
		t.Fatal("cancelled retry counted as delivery")
	}
}
