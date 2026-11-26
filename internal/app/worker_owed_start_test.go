package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/state"
)

// owedStartHarness is a service whose restart left two residual sessions
// alive: the engine refused to start beside them (orphan_processes).
type owedStartHarness struct {
	workerHarness
	exited    bool
	exitedErr error
}

// newOwedStartHarness runs a remediation rule with action under a 30-minute
// cooldown the owed start must not wait for.
func newOwedStartHarness(action string) (*owedStartHarness, *Worker) {
	h := &owedStartHarness{cache: failedCache("http"), opResult: orphanedRestart()}
	w := h.worker(remediationTree("restart-if-down", "http", action), rules.Policy{Cooldown: 30 * time.Minute}, nil)
	w.generationExited = func(int, uint64) (bool, error) { return h.exited, h.exitedErr }
	return h, w
}

func orphanedRestart() operation.Result {
	return operation.Result{Status: operation.ResultOrphanProcesses, Message: "2 residual process(es) remain after stop",
		Processes: []process.Process{{PID: 200, StartTicks: 20}, {PID: 201, StartTicks: 21}}}
}

func TestOwedStartRunsOnceResidualsExit(t *testing.T) {
	h, w := newOwedStartHarness("restart")

	w.RunCycle(context.Background())
	if !slices.Equal(h.ops, []string{"restart"}) || w.owedStart == nil {
		t.Fatalf("ops = %v, owed = %v; want one orphaned restart owing a start", h.ops, w.owedStart)
	}
	if e, _ := h.eventOf(eventKindError); !strings.HasSuffix(e.Message, owedStartPending) {
		t.Fatalf("orphan event message = %q, want the owed start announced", e.Message)
	}

	w.RunCycle(context.Background()) // residuals alive; the restart rule is in cooldown
	if len(h.ops) != 1 {
		t.Fatalf("ops = %v while residuals live", h.ops)
	}

	h.exited, h.opResult = true, operation.Result{Status: operation.ResultOK}
	w.RunCycle(context.Background())
	if !slices.Equal(h.ops, []string{"restart", "start"}) || w.owedStart != nil {
		t.Fatalf("ops = %v, owed = %v; want one start within the cooldown", h.ops, w.owedStart)
	}
	if len(w.State.RecentActions) != 2 {
		t.Fatalf("recent actions = %v, want the start recorded", w.State.RecentActions)
	}

	w.RunCycle(context.Background())
	if len(h.ops) != 2 {
		t.Fatalf("ops = %v, want no second start", h.ops)
	}
}

func TestOwedStartWaitsForProvenExit(t *testing.T) {
	for name, exitedErr := range map[string]error{"alive": nil, "unreadable": errors.New("cannot verify exit of pid 200")} {
		t.Run(name, func(t *testing.T) {
			h, w := newOwedStartHarness("restart")
			w.RunCycle(context.Background())
			h.exitedErr = exitedErr
			for range 3 {
				w.RunCycle(context.Background())
			}
			if len(h.ops) != 1 || w.owedStart == nil {
				t.Fatalf("ops = %v, owed = %v; a start must wait for a proven exit", h.ops, w.owedStart)
			}
		})
	}
}

// A refused or failed start leaves the service to its rules: an owed start
// that itself ends in orphan_processes owes nothing more.
func TestOwedStartIsASingleAttempt(t *testing.T) {
	h, w := newOwedStartHarness("restart")
	w.RunCycle(context.Background())
	h.exited = true
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	if !slices.Equal(h.ops, []string{"restart", "start"}) || w.owedStart != nil {
		t.Fatalf("ops = %v, owed = %v; want a single start attempt", h.ops, w.owedStart)
	}
}

func TestOwedStartHonoursDryRunPanicAndRateLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*Worker)
		kind    string
		message string
	}{
		{name: "dry run", prepare: func(w *Worker) { w.DryRun = true }, kind: eventKindDryRun, message: "would start"},
		{name: "panic", prepare: func(w *Worker) { w.InPanic = func() bool { return true } }, kind: eventKindSuppressed, message: "panic mode"},
		{name: "max_actions", prepare: func(w *Worker) { w.Policy.MaxActions, w.Policy.MaxActionsWindow = 1, time.Hour },
			kind: eventKindSuppressed, message: "rate limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, w := newOwedStartHarness("restart")
			w.RunCycle(context.Background())
			h.exited = true
			tc.prepare(w)
			w.RunCycle(context.Background())
			if len(h.ops) != 1 || w.owedStart != nil {
				t.Fatalf("ops = %v, owed = %v; want no start and the owed start spent", h.ops, w.owedStart)
			}
			if !slices.ContainsFunc(h.events, func(e Event) bool {
				return e.Kind == tc.kind && e.Action == "start" && strings.Contains(e.Message, tc.message)
			}) {
				t.Fatalf("events = %+v, want a %s start event saying %q", h.events, tc.kind, tc.message)
			}
		})
	}
}

// An operator who pauses the service or runs an operation takes it over.
func TestPauseOrOperationDropsOwedStart(t *testing.T) {
	for name, intervene := range map[string]func(*Worker, *fakeStore){
		"unmonitor": func(w *Worker, _ *fakeStore) {
			paused := true
			w.IsPaused = func() bool { return paused }
			w.RunCycle(context.Background())
			paused = false
		},
		"operation": func(_ *Worker, store *fakeStore) {
			if err := store.SetOperationSettling("web", state.OperationSettlingSettling); err != nil {
				t.Fatal(err)
			}
		},
		// A manual stop that ends in orphan_processes clears its marker and
		// pauses nothing: only a cycle that sees it running can tell.
		"operation that leaves no marker": func(w *Worker, store *fakeStore) {
			if err := store.SetOperationSettling("web", state.OperationSettlingRunning); err != nil {
				t.Fatal(err)
			}
			w.RunCycle(context.Background())
			if err := store.ClearOperationSettling("web"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, w := newOwedStartHarness("restart")
			store := newFakeStore()
			w.OperationSettling = store
			w.RunCycle(context.Background())
			if w.owedStart == nil {
				t.Fatal("orphaned restart owed no start")
			}
			intervene(w, store)
			h.exited = true
			w.RunCycle(context.Background())
			w.RunCycle(context.Background())
			if slices.Contains(h.ops, "start") || w.owedStart != nil {
				t.Fatalf("ops = %v, owed = %v; the operator's intervention must drop the owed start", h.ops, w.owedStart)
			}
		})
	}
}

func TestOnlyResidualBlockedStartsOweAStart(t *testing.T) {
	unknownGeneration := orphanedRestart()
	unknownGeneration.Processes[1].StartTicks = 0
	for _, tc := range []struct {
		name   string
		action string
		result operation.Result
		owes   bool
	}{
		{name: "orphaned restart", action: "restart", result: orphanedRestart(), owes: true},
		{name: "orphaned start", action: "start", result: orphanedRestart(), owes: true},
		{name: "orphaned stop", action: "stop", result: orphanedRestart()},
		{name: "failed restart", action: "restart", result: operation.Result{Status: operation.ResultFailed}},
		{name: "residual of unknown generation", action: "restart", result: unknownGeneration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, w := newOwedStartHarness(tc.action)
			h.opResult = tc.result
			w.RunCycle(context.Background())
			if (w.owedStart != nil) != tc.owes {
				t.Fatalf("owed = %v, want %v", w.owedStart, tc.owes)
			}
		})
	}
}

// The owed start runs under the configuration in force when the residuals exit:
// a reload that removes the rule drops it, and one that makes it dry-run only
// records it.
func TestOwedStartFollowsTheReloadedRule(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reload map[string]any
		kind   string
	}{
		{name: "rule removed", reload: remediationTree("other", "http", "restart")},
		{name: "rule now stops", reload: remediationTree("restart-if-down", "http", "stop")},
		{name: "rule now dry-run", reload: func() map[string]any {
			tree := remediationTree("restart-if-down", "http", "restart")
			tree["rules"].(map[string]any)["restart-if-down"].(map[string]any)["dry_run"] = true
			return tree
		}(), kind: eventKindDryRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, w := newOwedStartHarness("restart")
			w.RunCycle(context.Background())
			h.exited, h.opResult = true, operation.Result{Status: operation.ResultOK}
			w.Rules, _ = rules.ParseRules(tc.reload)
			w.RunCycle(context.Background())
			if slices.Contains(h.ops, "start") || w.owedStart != nil {
				t.Fatalf("ops = %v, owed = %v; the reloaded rule must decide", h.ops, w.owedStart)
			}
			if tc.kind != "" && !slices.ContainsFunc(h.events, func(e Event) bool { return e.Kind == tc.kind && e.Action == "start" }) {
				t.Fatalf("events = %+v, want a %s start", h.events, tc.kind)
			}
		})
	}
}
