package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/severity"
	"sermo/internal/state"
)

func silenceAlarm(t *testing.T, alarms *WatchAlarms, name string) string {
	t.Helper()
	revision := alarms.status(name).Revision
	if _, err := alarms.Silence(name, revision, state.SourceWeb); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestWatchAlarmDurableRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), state.Filename)
	store, err := state.OpenContextWith(t.Context(), path, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	alarms := NewWatchAlarms(store, nil)
	fault := state.WatchAlarmSlot{Active: true, Severity: severity.Warning.String()}
	alarms.Observe("disk", "space", "config", fault)
	revision := silenceAlarm(t, alarms, "disk")
	epoch, allowed := alarms.notificationEpoch("disk")
	if epoch == "" || allowed {
		t.Fatal("silence did not invalidate notifications")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.OpenContextWith(t.Context(), path, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	alarms = NewWatchAlarms(store, nil)
	alarms.Observe("disk", "space", "config", fault)
	view := alarms.status("disk")
	if !view.Silenced || view.Revision != revision || view.Source != state.SourceWeb || view.SilencedAt.IsZero() {
		t.Fatalf("restart lost acknowledgement: %+v", view)
	}
	if changed, err := alarms.Silence("disk", revision, state.SourceWeb); err != nil || changed {
		t.Fatalf("duplicate acknowledgement: %v %v", changed, err)
	}
	alarms.Observe("disk", "inodes", "config", fault)
	if alarms.status("disk").Silenced {
		t.Fatal("another metric did not rearm")
	}
	if _, err := alarms.Silence("disk", revision, state.SourceWeb); err == nil {
		t.Fatal("stale page silenced new incident")
	}
	if alarms.allowsNotification("disk", "") {
		t.Fatal("pre-silence queued notification allowed after rearm")
	}
	if !alarms.allowsNotification("disk", epoch) {
		t.Fatal("new notification blocked after rearm")
	}
}

func TestWatchAlarmStateChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		next   state.WatchAlarmSlot
		config string
	}{
		{name: "recovery"},
		{name: "severity", next: state.WatchAlarmSlot{Active: true, Severity: severity.Critical.String()}},
		{name: "unavailable", next: state.WatchAlarmSlot{Active: true, Unavailable: true, Severity: severity.Warning.String()}},
		{name: "device health", next: state.WatchAlarmSlot{Active: true, Health: "missing", Severity: severity.Warning.String()}},
		{name: "configuration", config: "new", next: state.WatchAlarmSlot{Active: true, Severity: severity.Warning.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
			fault := state.WatchAlarmSlot{Active: true, Severity: severity.Warning.String()}
			alarms.Observe("watch", "metric", "", fault)
			old := silenceAlarm(t, alarms, "watch")
			alarms.Observe("watch", "metric", tc.config, tc.next)
			if view := alarms.status("watch"); view.Silenced || view.Revision == old {
				t.Fatalf("not rearmed: %+v", view)
			}
			alarms.Observe("watch", "metric", "", fault)
			if _, err := alarms.Silence("watch", old, state.SourceWeb); err == nil {
				t.Fatal("old occurrence accepted after returning to same state")
			}
		})
	}
}

type failingWatchAlarmStore struct {
	*state.Store
	fail bool
}

func (s *failingWatchAlarmStore) SetWatchAlarmState(name string, rec state.WatchAlarmRecord) error {
	if s.fail {
		return errors.New("storage unavailable")
	}
	return s.Store.SetWatchAlarmState(name, rec)
}

func TestWatchAlarmPersistenceFailure(t *testing.T) {
	store := &failingWatchAlarmStore{Store: openEventNotifyStore(t)}
	alarms := NewWatchAlarms(store, nil)
	alarms.Observe("watch", "metric", "", state.WatchAlarmSlot{Active: true})
	store.fail = true
	if _, err := alarms.Silence("watch", alarms.status("watch").Revision, state.SourceWeb); err == nil {
		t.Fatal("failed persistence reported success")
	}
	if alarms.status("watch").Silenced {
		t.Fatal("failed persistence suppressed notifications")
	}
	store.fail = false
	silenceAlarm(t, alarms, "watch")
	store.fail = true
	alarms.Observe("watch", "metric", "", state.WatchAlarmSlot{})
	if alarms.status("watch").Silenced {
		t.Fatal("persistence failure hid recovery")
	}
	store.fail = false
	alarms.Observe("watch", "metric", "", state.WatchAlarmSlot{})
	if NewWatchAlarms(store, nil).status("watch").Silenced {
		t.Fatal("dirty state was not retried")
	}
}

func TestWatchAlarmConcurrentAcknowledgement(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	fault := state.WatchAlarmSlot{Active: true}
	alarms.Observe("watch", "metric", "", fault)
	revision := alarms.status("watch").Revision
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			alarms.Observe("watch", "metric", "", fault)
			_, _ = alarms.Silence("watch", revision, state.SourceWeb)
			_ = alarms.status("watch")
		})
	}
	workers.Wait()
	if !alarms.status("watch").Silenced {
		t.Fatal("unchanged cycles overwrote operator acknowledgement")
	}
}

func TestWatchSilencePreservesActionsAndConfirmedWindows(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	target := &fakeNotifier{name: "ops"}
	hooks := 0
	w := &Watch{Name: "disk", FireOnFail: true, Alarms: alarms,
		Notifiers: []notify.Notifier{target}, Hook: HookSpec{Command: []string{"/hook"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { hooks++; return nil }),
		Window: rules.Rule{Clear: &rules.ForWindow{Cycles: 2}},
	}
	fault := checks.Result{OK: false, Severity: severity.Warning, Message: "space 90%"}
	w.observeResult(t.Context(), fault, false)
	revision := silenceAlarm(t, alarms, w.Name)
	fault.Message = "space 95%"
	w.observeResult(t.Context(), fault, false)
	if hooks != 2 || len(target.msgs) != 1 {
		t.Fatalf("hooks=%d notifications=%d", hooks, len(target.msgs))
	}
	if alarms.status(w.Name).Revision != revision {
		t.Fatal("message change rearmed")
	}
	w.observeResult(t.Context(), checks.Result{OK: true}, false)
	if !alarms.status(w.Name).Silenced {
		t.Fatal("unconfirmed recovery rearmed")
	}
	w.observeResult(t.Context(), checks.Result{OK: true}, false)
	if alarms.status(w.Name).Silenced || len(target.msgs) != 2 {
		t.Fatal("confirmed recovery did not rearm and notify")
	}
}

func TestProcessWatchSilenceKeepsHooksAndIgnoresPIDChanges(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	h := &procHarness{}
	pw := h.watcher(procCond{memOp: ">", memValue: 1}, &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 1, RSS: 10}}, {{PID: 2, RSS: 10}}, {},
	}})
	target := &fakeNotifier{name: "ops"}
	pw.notifiers = []notify.Notifier{target}
	w := &Watch{Name: pw.name, Alarms: alarms, Cycle: pw.runCycle}
	w.RunCycle(t.Context())
	silenceAlarm(t, alarms, pw.name)
	w.RunCycle(t.Context())
	if !alarms.status(pw.name).Silenced || len(h.fired) != 2 || len(target.msgs) != 1 {
		t.Fatal("PID replacement changed silence or prevented hook")
	}
	w.RunCycle(t.Context())
	if alarms.status(pw.name).Silenced {
		t.Fatal("empty healthy process sample did not rearm")
	}
}

func TestDBQueryWatchRearmsBeforeAnnouncingAnotherStatement(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	f := newDBQueryFixture(t)
	target := &fakeNotifier{name: "ops"}
	f.w.notifiers = []notify.Notifier{target}
	w := &Watch{Name: f.w.name, Alarms: alarms, Cycle: f.w.runCycle}
	f.queries = []checks.DBQuery{mariaQuery(7, 70, 400, "app")}
	w.RunCycle(t.Context())
	silenceAlarm(t, alarms, w.Name)
	w.RunCycle(t.Context())
	if !alarms.status(w.Name).Silenced {
		t.Fatal("unchanged query rearmed")
	}
	f.queries = append(f.queries, mariaQuery(8, 80, 400, "app"))
	w.RunCycle(t.Context())
	if alarms.status(w.Name).Silenced || len(target.msgs) != 2 {
		t.Fatal("new statement was suppressed")
	}
}

func TestFileWatchAlarmSurvivesValueChangesAndRearmsOnRecovery(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	path := filepath.Join(t.TempDir(), "dead.letter")
	writeSize(t, path, 10)
	target := &fakeNotifier{name: "ops"}
	w, warning := buildFileWatch("dead-letter", map[string]any{"then": map[string]any{"notify": []any{"ops"}}}, map[string]any{
		"paths": []any{path}, "size": map[string]any{"op": ">", "value": 0},
	}, Deps{WatchAlarms: alarms, Notifiers: map[string]notify.Notifier{"ops": target}}, time.Minute)
	if warning != "" {
		t.Fatal(warning)
	}
	w.RunCycle(t.Context())
	silenceAlarm(t, alarms, w.Name)
	writeSize(t, path, 20)
	w.RunCycle(t.Context())
	if !alarms.status(w.Name).Silenced || len(target.msgs) != 1 {
		t.Fatal("numeric change rearmed the file alarm")
	}
	writeSize(t, path, 0)
	w.RunCycle(t.Context())
	if alarms.status(w.Name).Silenced {
		t.Fatal("file recovery did not rearm")
	}
	writeSize(t, path, 30)
	w.RunCycle(t.Context())
	if len(target.msgs) != 2 {
		t.Fatal("new file incident was not delivered")
	}
}

func TestWatchSilenceInvalidatesDirectDeliveryBetweenNotifiers(t *testing.T) {
	alarms := NewWatchAlarms(openEventNotifyStore(t), nil)
	fault := state.WatchAlarmSlot{Active: true}
	alarms.Observe("watch", "metric", "", fault)
	w := &Watch{Name: "watch", Alarms: alarms}
	first, second := &fakeNotifier{name: "first"}, &fakeNotifier{name: "second"}
	deliverNotification(w.alarmContext(t.Context()), []notify.Notifier{first, second}, notify.Message{}, nil, func(notify.Notifier, error) {
		silenceAlarm(t, alarms, w.Name)
		alarms.Observe(w.Name, "new-metric", "", fault)
	})
	if len(first.msgs) != 1 || len(second.msgs) != 0 {
		t.Fatal("old direct delivery escaped after silence and rearm")
	}
}
