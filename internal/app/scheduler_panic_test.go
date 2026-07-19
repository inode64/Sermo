package app

import (
	"context"
	"testing"
)

type panicCycler struct{ ran int }

func (p *panicCycler) RunCycle(context.Context) { p.ran++; panic("cycle boom") }
func (*panicCycler) cycleTarget() string        { return "test target" }

// A panic inside a cycle must be recovered so the daemon survives; runCycler
// keeps ticking the next cycle rather than crashing the process.
func TestRunCycleGuardedRecoversPanic(t *testing.T) {
	c := &panicCycler{}
	// Would crash the test binary if the recover were missing.
	runCycleGuarded(context.Background(), c)
	runCycleGuarded(context.Background(), c)
	if c.ran != 2 {
		t.Fatalf("guarded cycles ran = %d, want 2 (both recovered)", c.ran)
	}
}
