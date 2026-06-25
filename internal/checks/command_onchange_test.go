package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
)

// runVersionCycle drives one on_change command-check cycle whose `version`
// command prints `out`, reusing `state` across calls so change detection
// persists exactly as a long-lived watch would. changeLevel mirrors the
// version monitor's major(1)/minor(2)/patch(3) granularity; 0 compares raw.
func runVersionCycle(state *cmdState, changeLevel int, out string) Result {
	c := commandCheck{
		base:        base{name: "svc:version", timeout: time.Second},
		runner:      fakeRunner{execx.Result{ExitCode: 0, Stdout: out + "\n"}},
		argv:        []string{"app", "--version"},
		expectExit:  []int{0},
		onChange:    true,
		changeLevel: changeLevel,
		state:       state,
	}
	return c.Run(context.Background())
}

func TestCommandOnChangeVersionLevel(t *testing.T) {
	// minor level (2 components): a patch bump is ignored, a minor bump fires.
	t.Run("minor ignores patch, fires on minor", func(t *testing.T) {
		state := &cmdState{}
		if r := runVersionCycle(state, 2, "app 1.4.2"); !r.OK {
			t.Fatalf("first (priming) cycle should be OK, got %+v", r)
		}
		if r := runVersionCycle(state, 2, "app 1.4.7"); !r.OK {
			t.Fatalf("patch bump must not fire at minor level, got %+v", r)
		}
		r := runVersionCycle(state, 2, "app 1.5.0")
		if r.OK {
			t.Fatalf("minor bump must fire at minor level, got OK")
		}
		if r.Data["old"] != "app 1.4.7" || r.Data["new"] != "app 1.5.0" {
			t.Fatalf("change data should show raw versions, got %+v", r.Data)
		}
	})

	// major level (1 component): minor/patch bumps ignored, major fires.
	t.Run("major ignores minor, fires on major", func(t *testing.T) {
		state := &cmdState{}
		runVersionCycle(state, 1, "app 1.4.2")
		if r := runVersionCycle(state, 1, "app 1.9.9"); !r.OK {
			t.Fatalf("minor/patch bump must not fire at major level, got %+v", r)
		}
		if r := runVersionCycle(state, 1, "app 2.0.0"); r.OK {
			t.Fatalf("major bump must fire at major level, got OK")
		}
	})

	// patch level (3 components): any numeric change fires.
	t.Run("patch fires on patch", func(t *testing.T) {
		state := &cmdState{}
		runVersionCycle(state, 3, "app 1.4.2")
		if r := runVersionCycle(state, 3, "app 1.4.3"); r.OK {
			t.Fatalf("patch bump must fire at patch level, got OK")
		}
	})

	// Fallback: output with no parseable version compares the raw line, so a
	// change is never silently missed even at a numeric level.
	t.Run("unparseable output falls back to raw compare", func(t *testing.T) {
		state := &cmdState{}
		runVersionCycle(state, 2, "build alpha")
		if r := runVersionCycle(state, 2, "build alpha"); !r.OK {
			t.Fatalf("identical unparseable output must not fire, got %+v", r)
		}
		if r := runVersionCycle(state, 2, "build beta"); r.OK {
			t.Fatalf("changed unparseable output must fire via raw fallback, got OK")
		}
	})
}
