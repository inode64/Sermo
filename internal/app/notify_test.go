package app

import (
	"context"
	"testing"

	"sermo/internal/notify"
)

// fakeNotifier records the messages it is asked to send (and can fail on demand).
type fakeNotifier struct {
	name string
	fail bool
	msgs []notify.Message
}

func (f *fakeNotifier) Name() string { return f.name }
func (f *fakeNotifier) Type() string { return "fake" }
func (f *fakeNotifier) Send(_ context.Context, m notify.Message) error {
	f.msgs = append(f.msgs, m)
	if f.fail {
		return context.DeadlineExceeded
	}
	return nil
}

func TestWatchDispatchesNotifyOnFire(t *testing.T) {
	n := &fakeNotifier{name: "ops-email"}
	var events []Event
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Notifiers: []notify.Notifier{n},
		Emit:      func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())

	if len(n.msgs) != 1 {
		t.Fatalf("notifier should receive one message, got %d", len(n.msgs))
	}
	msg := n.msgs[0]
	if msg.Subject == "" || msg.Fields["SERMO_PATH"] != "/" {
		t.Fatalf("notification missing context: %+v", msg)
	}
	if !hasEvent(events, "notify") {
		t.Fatalf("expected a notify event, got %v", events)
	}
}

func TestWatchPanicSuppressesNotify(t *testing.T) {
	n := &fakeNotifier{name: "ops-email"}
	var events []Event
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Notifiers: []notify.Notifier{n},
		InPanic:   func() bool { return true },
		Emit:      func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())

	if len(n.msgs) != 0 {
		t.Fatalf("panic mode must suppress notifications, sent %d", len(n.msgs))
	}
	if !hasEvent(events, "panic-suppressed") {
		t.Fatalf("expected a panic-suppressed event, got %v", events)
	}
}

func TestWatchNotifyOnlyNoHook(t *testing.T) {
	n := &fakeNotifier{name: "ops-email"}
	w := &Watch{
		Name:  "storage-root",
		Check: stubCheck{name: "storage", ok: true},
		// no Hook, no Runner — notify-only watch must still work.
		Notifiers: []notify.Notifier{n},
		Emit:      func(Event) {},
	}
	w.RunCycle(context.Background())
	if len(n.msgs) != 1 {
		t.Fatalf("notify-only watch should send, got %d", len(n.msgs))
	}
}

func TestWatchNotifyFailureEmitsEvent(t *testing.T) {
	n := &fakeNotifier{name: "ops-email", fail: true}
	var events []Event
	w := &Watch{
		Name:      "storage-root",
		Check:     stubCheck{name: "storage", ok: true},
		Notifiers: []notify.Notifier{n},
		Emit:      func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	if !hasEvent(events, "notify-failed") {
		t.Fatalf("a failed delivery should emit notify-failed, got %v", events)
	}
}

func TestWatchDoesNotNotifyWhenNotFiring(t *testing.T) {
	n := &fakeNotifier{name: "ops-email"}
	w := &Watch{
		Name:       "health",
		CheckType:  "http",
		Check:      stubCheck{name: "http", ok: true}, // healthy
		FireOnFail: true,                              // health check: fires only on failure
		Notifiers:  []notify.Notifier{n},
		Emit:       func(Event) {},
	}
	w.RunCycle(context.Background())
	if len(n.msgs) != 0 {
		t.Fatalf("a passing health check must not notify, got %d", len(n.msgs))
	}
}

func hasEvent(events []Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
