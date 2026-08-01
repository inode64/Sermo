package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/rules"
)

// recordedSteps captures the sockets a watch asked to step, so a test never
// commands a real chronyd.
type recordedSteps struct {
	sockets []string
	err     error
}

func (r *recordedSteps) stepper() ClockStepper {
	return func(_ context.Context, socket string) error {
		r.sockets = append(r.sockets, socket)
		return r.err
	}
}

// makeStepWatchAt is the instant makeStepWatch pins its clock to, so a cooldown
// test can advance it.
var makeStepWatchAt = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// makeStepWatch builds a firing clock watch whose sample carries the given
// failure code. FireOnFail is implied by the clock check being a health type.
func makeStepWatch(steps *recordedSteps, failure string, events *[]Event) *Watch {
	data := map[string]any{checks.DataKeyOffsetSeconds: 7.5}
	if failure != "" {
		data[checks.DataKeyClockFailure] = failure
	}
	return &Watch{
		Name:       "clock-step",
		CheckType:  checks.CheckTypeClock,
		Check:      stubCheck{name: "clock", ok: false, data: data},
		FireOnFail: true,
		MakeStep:   &MakeStepSpec{Socket: "/run/chrony/chronyd.sock"},
		Stepper:    steps.stepper(),
		Emission:   emission.Policy{Events: emission.ModeEveryCycle},
		Policy:     rules.Policy{Cooldown: 30 * time.Minute},
		Now:        func() time.Time { return makeStepWatchAt },
		Emit:       func(e Event) { *events = append(*events, e) },
	}
}

func TestWatchMakeStepFiresOnceThenCooldown(t *testing.T) {
	steps := &recordedSteps{}
	at := makeStepWatchAt
	var events []Event
	w := makeStepWatch(steps, checks.ClockFailureOffset, &events)
	w.Now = func() time.Time { return at }

	w.RunCycle(context.Background())
	if len(steps.sockets) != 1 || steps.sockets[0] != "/run/chrony/chronyd.sock" {
		t.Fatalf("step calls = %v, want one on the command socket", steps.sockets)
	}
	if !hasEventKind(events, eventKindMakeStep) {
		t.Fatalf("expected a 'makestep' event, got %v", events)
	}

	// Same instant, inside the cooldown: a clock step must not repeat.
	w.RunCycle(context.Background())
	if len(steps.sockets) != 1 {
		t.Fatalf("makestep must be suppressed within cooldown, calls = %v", steps.sockets)
	}
	if !hasEventKind(events, eventKindMakeStepSkipped) {
		t.Fatalf("expected a 'makestep-skipped' event on cooldown, got %v", events)
	}

	at = at.Add(31 * time.Minute)
	w.RunCycle(context.Background())
	if len(steps.sockets) != 2 {
		t.Fatalf("makestep must run again after the cooldown, calls = %v", steps.sockets)
	}
}

func TestWatchMakeStepFailureStillRecordsCooldown(t *testing.T) {
	// A daemon that refuses must not be hammered every cycle.
	steps := &recordedSteps{err: errors.New("chrony makestep refused: unauthorized")}
	var events []Event
	w := makeStepWatch(steps, checks.ClockFailureOffset, &events)

	w.RunCycle(context.Background())
	if !hasEventKind(events, eventKindMakeStepFailed) {
		t.Fatalf("expected a 'makestep-failed' event, got %v", events)
	}
	w.RunCycle(context.Background())
	if len(steps.sockets) != 1 {
		t.Fatalf("a failed step must still take the cooldown, calls = %v", steps.sockets)
	}
}

func TestWatchMakeStepOnlyActsOnAnOffsetBreach(t *testing.T) {
	// Stepping because the source is unsynchronized would jump the clock by an
	// unknown or zero correction — the harm the gate exists to prevent.
	cases := []struct {
		name    string
		failure string
	}{
		{"unsynchronized source", checks.ClockFailureUnsynchronized},
		{"stratum breach", checks.ClockFailureStratum},
		{"root dispersion breach", checks.ClockFailureRootDispersion},
		{"no usable sample", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := &recordedSteps{}
			var events []Event
			w := makeStepWatch(steps, tc.failure, &events)

			w.RunCycle(context.Background())
			if len(steps.sockets) != 0 {
				t.Fatalf("must not step on %s, calls = %v", tc.name, steps.sockets)
			}
			if !hasEventKind(events, eventKindMakeStepSkipped) {
				t.Fatalf("expected a 'makestep-skipped' event naming the reason, got %v", events)
			}
		})
	}
}

func TestWatchMakeStepSuppressedByDryRunAndPanic(t *testing.T) {
	cases := []struct {
		name        string
		apply       func(w *Watch)
		wantKind    string
		wantMessage string
	}{
		{"dry run", func(w *Watch) { w.DryRun = true }, eventKindDryRun, "dry-run: would run makestep"},
		{"panic mode", func(w *Watch) { w.InPanic = func() bool { return true } }, eventKindPanicSuppressed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := &recordedSteps{}
			var events []Event
			w := makeStepWatch(steps, checks.ClockFailureOffset, &events)
			tc.apply(w)

			w.RunCycle(context.Background())
			if len(steps.sockets) != 0 {
				t.Fatalf("%s must not command the daemon, calls = %v", tc.name, steps.sockets)
			}
			if !hasEventKind(events, tc.wantKind) {
				t.Fatalf("expected a %q event, got %v", tc.wantKind, events)
			}
			if tc.wantMessage == "" {
				return
			}
			for _, e := range events {
				if e.Kind == tc.wantKind && e.Message != tc.wantMessage {
					t.Fatalf("%s message = %q, want %q", tc.wantKind, e.Message, tc.wantMessage)
				}
			}
		})
	}
}

func TestWatchMakeStepIdleWhenTheCheckPasses(t *testing.T) {
	steps := &recordedSteps{}
	var events []Event
	w := makeStepWatch(steps, "", &events)
	w.Check = stubCheck{name: "clock", ok: true, data: map[string]any{checks.DataKeyOffsetSeconds: 0.001}}

	w.RunCycle(context.Background())
	if len(steps.sockets) != 0 {
		t.Fatalf("a passing clock check must not step, calls = %v", steps.sockets)
	}
}
