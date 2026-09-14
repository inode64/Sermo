package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

func TestPanicGateNilSafe(t *testing.T) {
	var g *PanicGate
	if g.Active() {
		t.Fatal("nil gate must report not in panic")
	}
	if NewPanicGate(nil).Active() {
		t.Fatal("gate over a nil store must report not in panic")
	}
}

func TestPanicGateReadsAndCaches(t *testing.T) {
	store := newFakeStore()
	store.panicFound = true
	store.panicOn = true
	g := NewPanicGate(store)
	now := time.Unix(0, 0)
	g.now = func() time.Time { return now }

	if !g.Active() {
		t.Fatal("want active when store reports panic on")
	}
	// Within the TTL the gate keeps the cached value even if the store flips.
	store.panicOn = false
	if !g.Active() {
		t.Fatal("want cached active within ttl")
	}
	// Past the TTL it re-reads the store.
	now = now.Add(2 * time.Second)
	if g.Active() {
		t.Fatal("want inactive after ttl refresh")
	}
}

// TestReadinessReportsPanic verifies the daemon status switches to "panic mode"
// while the gate is active, and back to "ok" when it clears.
func TestReadinessReportsPanic(t *testing.T) {
	panicking := false
	r := NewReadiness(string(servicemgr.BackendSystemd), 3, 1)
	r.MarkReady()
	r.WatchPanic(func() bool { return panicking })

	rep := r.Report(context.Background())
	if rep.Panic || rep.Status != TargetStateOK || !rep.Ready {
		t.Fatalf("baseline report = %+v, want ok/not-panic/ready", rep)
	}

	panicking = true
	rep = r.Report(context.Background())
	if !rep.Panic || rep.Status != "panic mode" || !rep.Ready {
		t.Fatalf("panic report = %+v, want panic mode + ready", rep)
	}

	panicking = false
	if rep = r.Report(context.Background()); rep.Panic || rep.Status != TargetStateOK {
		t.Fatalf("cleared report = %+v, want ok", rep)
	}
}

// TestReadinessPanicDoesNotOverrideLifecycle ensures starting/shutting_down keep
// precedence over panic mode.
func TestReadinessPanicDoesNotOverrideLifecycle(t *testing.T) {
	r := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	r.WatchPanic(func() bool { return true })

	if rep := r.Report(context.Background()); rep.Status != "starting" || rep.Panic {
		t.Fatalf("starting report = %+v, want starting and no panic override", rep)
	}
	r.MarkShuttingDown()
	if rep := r.Report(context.Background()); rep.Status != "shutting_down" || rep.Panic {
		t.Fatalf("shutting down report = %+v, want shutting_down and no panic override", rep)
	}
}

type panicReaderFunc func() (state.GlobalRecord, bool, error)

func (f panicReaderFunc) Panic() (state.GlobalRecord, bool, error) {
	return f()
}

func TestPanicGateInitialReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found bool
		on    bool
	}{
		{name: "missing"},
		{name: "off", found: true},
		{name: "on", found: true, on: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readErr := errors.New("state unavailable")
			g := NewPanicGate(panicReaderFunc(func() (state.GlobalRecord, bool, error) {
				return state.GlobalRecord{On: tc.on}, tc.found, readErr
			}))
			now := time.Unix(0, 0)
			g.now = func() time.Time { return now }
			if !g.Active() {
				t.Fatal("initial read failure must suspend automatic side effects")
			}
			readErr = nil
			now = now.Add(2 * defaultPanicGateTTL)
			want := tc.found && tc.on
			if got := g.Active(); got != want {
				t.Fatalf("recovered state = %v, want %v", got, want)
			}
			readErr = errors.New("state unavailable again")
			now = now.Add(2 * defaultPanicGateTTL)
			if got := g.Active(); got != want {
				t.Fatalf("later failure state = %v, want last known %v", got, want)
			}
		})
	}
}
