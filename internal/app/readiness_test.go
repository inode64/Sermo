package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestReadinessLifecycle(t *testing.T) {
	r := NewReadiness("systemd", 3, 1)

	if rep := r.Report(context.Background()); rep.Ready || rep.Status != readinessStarting {
		t.Fatalf("initial = %+v", rep)
	}

	r.MarkReady()
	if rep := r.Report(context.Background()); !rep.Ready || rep.Status != "ok" || rep.Services != 3 || rep.Watches != 1 {
		t.Fatalf("ready = %+v", rep)
	}

	r.MarkShuttingDown()
	if rep := r.Report(context.Background()); rep.Ready || rep.Status != readinessShuttingDown {
		t.Fatalf("shutting down = %+v", rep)
	}
}

func TestReadinessFirstCycleGate(t *testing.T) {
	r := NewReadiness("systemd", 2, 0)
	r.ExpectFirstCycles(2)

	rep := r.Report(context.Background())
	if rep.Ready || rep.Status != readinessStarting || rep.Message != "starting: 0/2 monitored targets have reported" {
		t.Fatalf("armed gate = %+v", rep)
	}

	r.markFirstCycle()
	if rep := r.Report(context.Background()); rep.Ready {
		t.Fatalf("should stay starting after 1/2: %+v", rep)
	}
	if rep := r.Report(context.Background()); rep.Message != "starting: 1/2 monitored targets have reported" {
		t.Fatalf("progress message = %q", rep.Message)
	}

	r.markFirstCycle()
	if rep := r.Report(context.Background()); !rep.Ready || rep.Status != "ok" {
		t.Fatalf("should be ready after 2/2: %+v", rep)
	}
}

func TestReadinessGateZeroTargetsReadyImmediately(t *testing.T) {
	r := NewReadiness("systemd", 0, 0)
	r.ExpectFirstCycles(0)
	if rep := r.Report(context.Background()); !rep.Ready {
		t.Fatalf("zero targets must be ready immediately: %+v", rep)
	}
}

func TestReadinessMarkReadyDoesNotUndoShutdown(t *testing.T) {
	r := NewReadiness("systemd", 1, 0)
	r.ExpectFirstCycles(1)
	r.MarkShuttingDown()
	// A late first-cycle signal (or MarkReady) must not revive the daemon.
	r.markFirstCycle()
	r.MarkReady()
	if rep := r.Report(context.Background()); rep.Ready || rep.Status != readinessShuttingDown {
		t.Fatalf("shutdown must stick: %+v", rep)
	}
}

func TestSchedulerMarksReadiness(t *testing.T) {
	ready := NewReadiness("systemd", 1, 0)
	workers := []*Worker{
		{Service: "a", Checks: func(context.Context, checks.Deps) map[string]checks.Result { return nil }},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 10 * time.Millisecond, StartupDelay: 40 * time.Millisecond}.Run(ctx, workers, nil, nil, ready, true, true)
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	if rep := ready.Report(context.Background()); rep.Ready {
		t.Fatalf("should not be ready during startup delay: %+v", rep)
	}

	time.Sleep(60 * time.Millisecond)
	if rep := ready.Report(context.Background()); !rep.Ready {
		t.Fatalf("should be ready after startup delay: %+v", rep)
	}

	cancel()
	<-done
	if rep := ready.Report(context.Background()); rep.Ready || rep.Status != readinessShuttingDown {
		t.Fatalf("after shutdown = %+v", rep)
	}
}
