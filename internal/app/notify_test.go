package app

import (
	"context"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/notify"
)

// fakeNotifier records the messages it is asked to send (and can fail on demand).
type fakeNotifier struct {
	name string
	typ  string
	fail bool
	msgs []notify.Message
}

func (f *fakeNotifier) Name() string { return f.name }
func (f *fakeNotifier) Type() string {
	if f.typ != "" {
		return f.typ
	}
	return "fake"
}
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
	if !hasEvent(events, eventKindNotify) {
		t.Fatalf("expected a notify event, got %v", events)
	}
}

func TestWatchDispatchesSelectedRaidTransitions(t *testing.T) {
	degraded := &fakeNotifier{name: "raid-critical"}
	changes := &fakeNotifier{name: "raid-audit"}
	check := &scriptedCheck{results: []checks.Result{{
		Check: "raid", OK: false, Data: map[string]any{
			checks.DataKeyRaidTransitions: []checks.RaidTransition{
				{Event: checks.RaidNotifyOnDegraded, Array: "md0"},
				{Event: checks.RaidNotifyOnArrayChange, Array: "md0", Member: "sda1", Field: "errors", Old: "0", New: "1"},
				{Event: checks.RaidNotifyOnArrayChange, Array: "md0", Member: "sda1", Field: "bad_blocks", Old: "none", New: "8"},
			},
		},
	}}}
	w := &Watch{
		Name: "raid-md0", CheckType: checks.CheckTypeRAID, Check: check,
		Notifiers: []notify.Notifier{degraded, changes},
		RaidNotifyEvents: map[string]bool{
			checks.RaidNotifyOnDegraded: true, checks.RaidNotifyOnArrayChange: true,
		},
	}
	w.RunCycle(context.Background())
	if len(degraded.msgs) != 2 || len(changes.msgs) != 2 {
		t.Fatalf("messages degraded=%d changes=%d", len(degraded.msgs), len(changes.msgs))
	}
	if got := changes.msgs[1].Fields["SERMO_RAID_FIELD"]; got != "sda1.errors,sda1.bad_blocks" {
		t.Fatalf("raid transition fields = %+v", changes.msgs[1].Fields)
	}
	if got := changes.msgs[1].Fields["SERMO_OLD"]; got != "sda1.errors=0; sda1.bad_blocks=none" {
		t.Fatalf("old value = %q, fields=%+v", got, changes.msgs[1].Fields)
	}
}

func TestWatchDispatchesLVMHealthChange(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	w := &Watch{
		Name:              "lvm-vg0-root",
		CheckType:         checks.CheckTypeLVM,
		LVMNotifyOnChange: true,
		Notifiers:         []notify.Notifier{n},
		Check: &scriptedCheck{results: []checks.Result{{
			Check: "lvm", Data: map[string]any{
				"lvm_transition": checks.LVMTransition{OldState: checks.LVMHealthOK, NewState: checks.LVMHealthError, Reasons: "partial"},
			}}}},
	}
	w.RunCycle(context.Background())
	if len(n.msgs) != 1 {
		t.Fatalf("LVM notification messages = %d", len(n.msgs))
	}
	if got := n.msgs[0].Fields["SERMO_NEW_STATE"]; got != checks.LVMHealthError {
		t.Fatalf("LVM new state = %q, fields=%+v", got, n.msgs[0].Fields)
	}
	if got := n.msgs[0].Fields["SERMO_LVM_REASONS"]; got != "partial" {
		t.Fatalf("LVM reasons = %q, fields=%+v", got, n.msgs[0].Fields)
	}
}

func TestWatchReportsSuppressedLVMHealthChangeInPanic(t *testing.T) {
	tests := []struct {
		name       string
		resultOK   bool
		transition checks.LVMTransition
		want       string
	}{
		{
			name:       "degraded",
			resultOK:   false,
			transition: checks.LVMTransition{OldState: checks.LVMHealthOK, NewState: checks.LVMHealthError, Reasons: "partial"},
			want:       "panic mode: LVM notification suppressed: lvm state ok -> error",
		},
		{
			name:       "recovered",
			resultOK:   true,
			transition: checks.LVMTransition{OldState: checks.LVMHealthError, NewState: checks.LVMHealthOK, PreviousReasons: "partial"},
			want:       "panic mode: LVM notification suppressed: lvm state error -> ok",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := &fakeNotifier{name: "ops"}
			var events []Event
			w := &Watch{
				Name:              "lvm-vg0-root",
				CheckType:         checks.CheckTypeLVM,
				LVMNotifyOnChange: true,
				FireOnFail:        true,
				Notifiers:         []notify.Notifier{n},
				InPanic:           func() bool { return true },
				Emit:              func(event Event) { events = append(events, event) },
				Check: &scriptedCheck{results: []checks.Result{{
					Check: "lvm",
					OK:    tc.resultOK,
					Data:  map[string]any{"lvm_transition": tc.transition},
				}}},
			}

			w.RunCycle(t.Context())

			if len(n.msgs) != 0 {
				t.Fatalf("panic mode delivered %d LVM notifications, want none", len(n.msgs))
			}
			matches := 0
			for _, event := range events {
				if event.Kind == eventKindPanicSuppressed && event.Message == tc.want {
					matches++
					if event.Watch != w.Name {
						t.Errorf("suppression event watch = %q, want %q", event.Watch, w.Name)
					}
				}
			}
			if matches != 1 {
				t.Fatalf("matching suppression events = %d, want 1; events=%+v", matches, events)
			}
		})
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
	if !hasEvent(events, eventKindPanicSuppressed) {
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
	if !hasEvent(events, eventKindNotifyFail) {
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
