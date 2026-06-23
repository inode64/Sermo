package app

import (
	"context"
	"testing"
)

func TestSettlingMarkObservedAdvancesReadiness(t *testing.T) {
	ready := NewReadiness("systemd", 2, 0)
	ready.ExpectFirstCycles(2)
	s := NewSettling(ready)
	s.Reset([]string{"a", "b"})

	if s.Observed("a") || s.Observed("b") {
		t.Fatal("targets should start unsettled")
	}
	s.MarkObserved("a")
	if rep := ready.Report(context.Background()); rep.Ready || rep.Status != readinessStarting {
		t.Fatalf("one observed target should stay starting: %+v", rep)
	}
	s.MarkObserved("b")
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != "ok" {
		t.Fatalf("all observed targets should be ready: %+v", rep)
	}
}

func TestSettlingMarkObservedBulk(t *testing.T) {
	s := NewSettling(nil)
	s.Reset([]string{"a", "b"})
	s.MarkObservedBulk([]string{"a"})
	if !s.Observed("a") || s.Observed("b") {
		t.Fatalf("bulk mark should clear only named targets")
	}
}
