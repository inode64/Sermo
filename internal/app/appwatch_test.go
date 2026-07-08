package app

import (
	"context"
	"testing"

	"sermo/internal/appinspect"
	"sermo/internal/checks"
	"sermo/internal/notify"
)

func TestAppCheckMapsStatus(t *testing.T) {
	errc := appCheck{name: "x", inspect: func(context.Context) appinspect.Report {
		return appinspect.Report{Status: "error: exit 1 (want 0): boom", Output: "stderr:\nboom"}
	}}
	r := errc.Run(context.Background())
	if r.OK || r.Message == "" {
		t.Fatalf("error status must map to not-OK with the detail message: %+v", r)
	}
	if resultOutput(r) != "stderr:\nboom" {
		t.Fatalf("error result must carry the probe output, got %q", resultOutput(r))
	}
	okc := appCheck{name: "x", inspect: func(context.Context) appinspect.Report {
		return appinspect.Report{Status: appinspect.StatusOK}
	}}
	if r := okc.Run(context.Background()); !r.OK {
		t.Fatal("ok status must map to OK")
	}
}

// TestAppWatchNotifiesOnceAndRecovers verifies the app-watch reuses the Watch
// cycle: it notifies once on the rising edge (first error), does not re-notify
// while it stays in error, emits recovered when it returns to ok, and tags every
// event on the App dimension (not Watch).
func TestAppWatchStartupObserveOnlySkipsNotify(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	var events []Event
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingAppKey("salt-minion")})
	check := &scriptedCheck{results: []checks.Result{
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
	}}
	w := &Watch{
		Name:       "salt-minion",
		App:        "salt-minion",
		CheckType:  "app",
		Check:      check,
		FireOnFail: true,
		Notifiers:  []notify.Notifier{n},
		Settling:   settling,
		Emit:       func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background())
	if len(n.msgs) != 0 || hasEventKind(events, eventKindFiring) {
		t.Fatalf("observe-only app-watch must not notify or fire, events=%v msgs=%d", events, len(n.msgs))
	}
	if !settling.Observed(SettlingAppKey("salt-minion")) {
		t.Fatal("observe-only app-watch must mark the app observed")
	}

	w.RunCycle(context.Background())
	if len(n.msgs) != 1 {
		t.Fatalf("second cycle must notify once, got %d", len(n.msgs))
	}
}

func TestAppWatchNotifiesOnceAndRecovers(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	var events []Event
	check := &scriptedCheck{results: []checks.Result{
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
		{Check: "salt-minion", OK: true, Message: "ok"},
	}}
	w := &Watch{
		Name:       "salt-minion",
		App:        "salt-minion",
		CheckType:  "app",
		Check:      check,
		FireOnFail: true,
		Notifiers:  []notify.Notifier{n},
		Emit:       func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background()) // error  -> firing + notify (rising edge)
	w.RunCycle(context.Background()) // error  -> firing, no re-notify
	w.RunCycle(context.Background()) // ok     -> recovered

	if len(n.msgs) != 1 {
		t.Fatalf("app-watch must notify once on the rising edge, got %d", len(n.msgs))
	}
	for _, e := range events {
		if e.App != "salt-minion" || e.Watch != "" {
			t.Fatalf("app-watch event must be on the App dimension, got %+v", e)
		}
	}
	if !hasEventKind(events, eventKindFiring) || !hasEventKind(events, eventKindRecovered) {
		t.Fatalf("want firing and recovered, kinds = %v", eventKinds(events))
	}
	if !hasEvent(events, eventKindNotify) {
		t.Fatalf("expected a notify event, got %v", events)
	}
	if !hasEventMessage(events, eventKindFiring, "error: exit 1") {
		t.Fatalf("firing event must carry the error detail, got %v", events)
	}
}
