package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

// A manual restart already loaded the replaced libraries, but the library check
// runs every ten cycles: the cycle that observed the restart reused its reading
// from before it, and the next rule cycle restarted the database a second time
// on it. That cycle now runs every check, so the rule sees the restart's result.
func TestWorkerRefreshesIntervalChecksAfterAnOperation(t *testing.T) {
	stale := checks.Result{Check: "scripted", OK: false, Message: "4 stale library mapping(s)"}
	fresh := checks.Result{Check: "scripted", OK: true, Message: "no stale libraries"}
	libs := &scriptedCheck{results: []checks.Result{stale, fresh, fresh, fresh}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	tree := remediationTreeFor("restart-if-loaded-libraries-stale", "cycles", 2)
	tree["rules"].(map[string]any)["restart-if-loaded-libraries-stale"].(map[string]any)["if"] = map[string]any{"failed": map[string]any{"check": "scripted"}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	store := newFakeStore()
	w.OperationSettling = store
	w.Checks = workerCheckRunner(w, []checks.Built{{Check: libs}}, map[string]int{"scripted": 10}, 1, nil, func(checks.MetricReader) {})

	w.RunCycle(context.Background()) // the stale reading: the window opens
	if err := store.SetOperationSettling("web", state.OperationSettlingSettling); err != nil {
		t.Fatal(err)
	}
	w.RunCycle(context.Background()) // observes the operator's restart
	if !w.cycleRan["scripted"] {
		t.Fatal("the cycle after an operation must run the interval check again")
	}
	w.RunCycle(context.Background()) // first rule cycle after the restart
	if len(h.ops) != 0 {
		t.Fatalf("ops = %v: a reading from before the restart drove another restart", h.ops)
	}

	w.RunCycle(context.Background())
	if w.cycleRan["scripted"] {
		t.Fatal("outside the cycle after an operation the interval still applies")
	}
}
