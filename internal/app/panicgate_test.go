package app

import (
	"context"
	"testing"
	"time"
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
	r := NewReadiness("systemd", 3, 1)
	r.MarkReady()
	r.WatchPanic(func() bool { return panicking })

	rep := r.Report(context.Background())
	if rep.Panic || rep.Status != "ok" || !rep.Ready {
		t.Fatalf("baseline report = %+v, want ok/not-panic/ready", rep)
	}

	panicking = true
	rep = r.Report(context.Background())
	if !rep.Panic || rep.Status != "panic mode" || !rep.Ready {
		t.Fatalf("panic report = %+v, want panic mode + ready", rep)
	}

	panicking = false
	if rep = r.Report(context.Background()); rep.Panic || rep.Status != "ok" {
		t.Fatalf("cleared report = %+v, want ok", rep)
	}
}

// TestReadinessPanicDoesNotOverrideLifecycle ensures starting/shutting_down keep
// precedence over panic mode.
func TestReadinessPanicDoesNotOverrideLifecycle(t *testing.T) {
	r := NewReadiness("systemd", 1, 0)
	r.WatchPanic(func() bool { return true })

	if rep := r.Report(context.Background()); rep.Status != "starting" || rep.Panic {
		t.Fatalf("starting report = %+v, want starting and no panic override", rep)
	}
	r.MarkShuttingDown()
	if rep := r.Report(context.Background()); rep.Status != "shutting_down" || rep.Panic {
		t.Fatalf("shutting down report = %+v, want shutting_down and no panic override", rep)
	}
}
