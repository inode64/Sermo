package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/state"
)

func TestWatchSilenceDiscardsNotificationWaitingForHook(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	target := &fakeNotifier{name: "ops"}
	w := &Watch{Name: "disk", FireOnFail: true, Alarms: alarms,
		Notifiers: []notify.Notifier{target}, Hook: HookSpec{Command: []string{"/hook"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
			silenceAlarm(t, alarms, "disk")
			alarms.Observe("disk", "other-metric", "", state.WatchAlarmSlot{Active: true})
			return nil
		}),
	}
	w.observeResult(t.Context(), checks.Result{OK: false, Message: "old failure"}, false)
	if len(target.msgs) != 0 {
		t.Fatalf("notification from before silence escaped after hook: %+v", target.msgs)
	}
}

func TestStatefulWatchSilenceDiscardsNotificationWaitingForHook(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	alarms.Observe("queries", "query", "", state.WatchAlarmSlot{Active: true})
	target := &fakeNotifier{name: "ops"}
	w := &Watch{Name: "queries", Alarms: alarms}
	hooks := 0
	spec := watchFireSpec{name: w.Name, notifiers: []notify.Notifier{target},
		hook: HookSpec{Command: []string{"/hook"}}, emit: func(Event) {},
		runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
			hooks++
			silenceAlarm(t, alarms, w.Name)
			alarms.Observe(w.Name, "another-query", "", state.WatchAlarmSlot{Active: true})
			return nil
		}),
	}
	dispatchWatchFire(w.alarmContext(t.Context()), spec, "old query", map[string]string{})
	if hooks != 1 || len(target.msgs) != 0 {
		t.Fatalf("hooks=%d notifications=%+v", hooks, target.msgs)
	}
}

func TestWatchSilenceAllowsStateObservedAfterSilencingDuringProbe(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	alarms.Observe("disk", "metric", "", state.WatchAlarmSlot{Active: true})
	w := &Watch{Name: "disk", Alarms: alarms, FireOnFail: true}
	ctx := w.alarmContext(t.Context())
	silenceAlarm(t, alarms, w.Name)
	target := &fakeNotifier{name: "ops"}
	w.Notifiers = []notify.Notifier{target}
	// Another metric's new observation rearms the watch after the acknowledgement.
	w.observeResult(ctx, checks.Result{OK: false, Message: "new observation"}, false)
	if len(target.msgs) != 1 {
		t.Fatalf("new observation suppressed: %+v", target.msgs)
	}
}

func TestWatchSilenceDiscardsRecoveryWaitingForHook(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	target := &fakeNotifier{name: "ops"}
	w := &Watch{Name: "disk", Alarms: alarms, FireOnFail: true, Notifiers: []notify.Notifier{target}}
	w.observeResult(t.Context(), checks.Result{OK: false}, false)
	alarms.Observe(w.Name, "other-metric", "", state.WatchAlarmSlot{Active: true})
	w.RecoverHook = HookSpec{Command: []string{"/recover"}}
	w.Runner = HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
		silenceAlarm(t, alarms, w.Name)
		alarms.Observe(w.Name, "another-metric", "", state.WatchAlarmSlot{Active: true})
		return nil
	})
	w.observeResult(t.Context(), checks.Result{OK: true}, false)
	if len(target.msgs) != 1 {
		t.Fatalf("old recovery escaped after hook: %+v", target.msgs)
	}
}
