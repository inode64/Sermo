package rules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/process"
)

func TestUnavailableConditionsNeverMatch(t *testing.T) {
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ev   Evaluator
		node map[string]any
	}{
		{"missing metric", Evaluator{}, map[string]any{ConditionMetric: map[string]any{FieldName: "cpu", FieldOp: ">", FieldValue: "90%"}}},
		{"unready metric", Evaluator{Deps: checks.Deps{Metrics: func(string, string) (metrics.Reading, bool) { return metrics.Reading{}, true }}}, map[string]any{ConditionMetric: map[string]any{FieldName: "cpu", FieldOp: ">", FieldValue: "90%"}}},
		{"unknown process", Evaluator{Deps: checks.Deps{Processes: func(string, string) string { return process.StateUnknown }}}, map[string]any{ConditionProcess: map[string]any{FieldExe: "/usr/bin/demo", FieldUser: "demo"}}},
		{"missing change source", Evaluator{}, map[string]any{ConditionChanged: map[string]any{FieldPath: "/etc/demo"}}},
		{"missing version source", Evaluator{}, map[string]any{ConditionChanged: map[string]any{FieldApp: "demo"}}},
		{"file absent unreadable", Evaluator{}, map[string]any{ConditionFile: map[string]any{FieldPath: loop, FieldExists: false}}},
		{"file present unreadable", Evaluator{}, map[string]any{ConditionFile: map[string]any{FieldPath: loop, FieldExists: true}}},
		{"skipped active", Evaluator{Cache: map[string]checks.Result{"probe": {OK: true, Skipped: true}}}, map[string]any{ConditionActive: map[string]any{FieldCheck: "probe"}}},
		{"skipped failed", Evaluator{Cache: map[string]checks.Result{"probe": {OK: true, Skipped: true}}}, map[string]any{ConditionFailed: map[string]any{FieldCheck: "probe"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, node := range []map[string]any{
				tc.node,
				{ConditionNot: tc.node},
				{ConditionNot: map[string]any{ConditionNot: tc.node}},
				{ConditionNot: map[string]any{ConditionAnd: []any{tc.node, tc.node}}},
				{ConditionNot: map[string]any{ConditionOr: []any{tc.node, tc.node}}},
			} {
				ev := tc.ev
				if got, err := ev.Eval(t.Context(), node); got || err != nil {
					t.Fatalf("ordinary Eval(%v) = %v, %v", node, got, err)
				}
				ev.FailOnUnavailable = true
				if got, err := ev.Eval(t.Context(), node); got || err == nil {
					t.Fatalf("guard Eval(%v) = %v, %v", node, got, err)
				}
			}
		})
	}
}

func TestFailedReportsObservationErrorsButNotSkippedChecks(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		ev := &Evaluator{Cache: map[string]checks.Result{"probe": {Unavailable: true, Skipped: skipped}}}
		failed := map[string]any{ConditionFailed: map[string]any{FieldCheck: "probe"}}
		if got, err := ev.Eval(t.Context(), failed); err != nil || got == skipped {
			t.Fatalf("skipped=%v: failed=%v error=%v", skipped, got, err)
		}
		active := map[string]any{ConditionNot: map[string]any{ConditionActive: map[string]any{FieldCheck: "probe"}}}
		if got, err := ev.Eval(t.Context(), active); err != nil || got {
			t.Fatalf("not active turned unavailable into true: %v %v", got, err)
		}
		guard := []Rule{{Type: RuleGuard, Name: "guard", Blocks: []string{string(ActionRestart)}, If: failed}}
		if blocked, _, err := Guard(t.Context(), guard, string(ActionRestart), ev); blocked || err == nil {
			t.Fatalf("guard accepted uncertainty: blocked=%v error=%v", blocked, err)
		}
	}
}

// busyCache holds one probe in each reporting mode: a health probe reads busy
// as failing, a condition probe as unavailable (it never got its reading).
func busyCache() map[string]checks.Result {
	return map[string]checks.Result{
		"health":    {Busy: true, Message: "mysql 127.0.0.1:3306: server busy: Too many connections (1040)"},
		"condition": {Busy: true, Unavailable: true, Condition: true, Message: "sql mysql: server busy: Too many connections (1040)"},
	}
}

// A rule that would restart, start, stop, reload or resume never fires on a
// busy server; an alert rule (or a remediation with no operation) sees the
// failure; a guard fails closed exactly as it does on an unavailable check.
func TestBusyChecksNeverTriggerOperations(t *testing.T) {
	operations := []ActionType{ActionRestart, ActionStart, ActionStop, ActionReload, ActionResume}
	for _, check := range []string{"health", "condition"} {
		failed := map[string]any{ConditionFailed: map[string]any{FieldCheck: check}}
		for _, node := range []map[string]any{
			failed,
			{ConditionAnd: []any{failed, failed}},
			{ConditionOr: []any{failed, map[string]any{ConditionFailed: map[string]any{FieldCheck: check}}}},
		} {
			for _, op := range operations {
				ev := &Evaluator{Cache: busyCache()}
				rule := Rule{Name: "restart-if-ping-failed", Type: RuleRemediation, If: node, Actions: []Action{{Type: op}}}
				if got, err := ev.EvalRule(t.Context(), rule); got || err != nil {
					t.Fatalf("%s %s on busy %s check = %v, %v; want no match", op, nodeName(node), check, got, err)
				}
				mixed := Rule{Name: "alert-and-restart", Type: RuleRemediation, If: node,
					Actions: []Action{{Type: ActionAlert, Message: "down"}, {Type: op}}}
				if got, err := ev.EvalRule(t.Context(), mixed); got || err != nil {
					t.Fatalf("an alert beside %s must not let busy trigger it: %v, %v", op, got, err)
				}
			}
			ev := &Evaluator{Cache: busyCache()}
			alert := Rule{Name: "alert-if-ping-failed", Type: RuleAlert, If: node, Actions: []Action{{Type: ActionAlert, Message: "down"}}}
			if got, err := ev.EvalRule(t.Context(), alert); !got || err != nil {
				t.Fatalf("alert %s on busy %s check = %v, %v; want a match", nodeName(node), check, got, err)
			}
			if got, err := ev.Eval(t.Context(), node); got || err != nil {
				t.Fatalf("plain Eval must keep busy unknown: %v, %v", got, err)
			}
		}
		guard := []Rule{{Type: RuleGuard, Name: "guard", Blocks: []string{string(ActionRestart)}, If: failed}}
		ev := &Evaluator{Cache: busyCache()}
		// An alert evaluated first on the same evaluator must not leak its
		// reading of busy into the guard.
		if _, err := ev.EvalRule(t.Context(), Rule{Type: RuleAlert, If: failed, Actions: []Action{{Type: ActionAlert, Message: "x"}}}); err != nil {
			t.Fatal(err)
		}
		if blocked, _, err := Guard(t.Context(), guard, string(ActionRestart), ev); blocked || err == nil {
			t.Fatalf("guard on busy %s check: blocked=%v error=%v; want fail closed", check, blocked, err)
		}
		notActive := map[string]any{ConditionNot: map[string]any{ConditionActive: map[string]any{FieldCheck: check}}}
		if got, err := ev.EvalRule(t.Context(), Rule{If: notActive, Actions: []Action{{Type: ActionRestart}}}); got || err != nil {
			t.Fatalf("not active on busy %s check must stay unknown for an operation: %v, %v", check, got, err)
		}
	}
}

// A busy cycle is a false cycle for an operation rule: for: needs consecutive
// real failures after the busy phase, so a single timeout in the middle of a
// connection storm cannot complete the window.
func TestBusyResetsOperationWindow(t *testing.T) {
	rule := Rule{Name: "restart-if-ping-failed", Type: RuleRemediation,
		If:      map[string]any{ConditionFailed: map[string]any{FieldCheck: "ping"}},
		Actions: []Action{{Type: ActionRestart}}, For: &ForWindow{Cycles: 2}}
	dead := checks.Result{Unavailable: true, Message: "connection refused"}
	busy := checks.Result{Busy: true, Message: "server busy"}
	state := &WindowState{}
	at := time.Unix(0, 0)
	for i, res := range []checks.Result{dead, busy, dead, busy} {
		ev := &Evaluator{Cache: map[string]checks.Result{"ping": res}}
		cond, err := ev.EvalRule(t.Context(), rule)
		if err != nil {
			t.Fatal(err)
		}
		if state.FiresAt(rule, cond, at.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("cycle %d: busy between failures must reset for: 2", i)
		}
	}
	for i, res := range []checks.Result{dead, dead} {
		ev := &Evaluator{Cache: map[string]checks.Result{"ping": res}}
		cond, _ := ev.EvalRule(t.Context(), rule)
		if fires := state.FiresAt(rule, cond, at.Add(time.Hour+time.Duration(i)*time.Minute)); fires != (i == 1) {
			t.Fatalf("dead cycle %d fires=%v; a dead server must still restart", i, fires)
		}
	}
}

func nodeName(node map[string]any) string {
	for k := range node {
		return k
	}
	return ""
}

// An OR holds as soon as one operand holds: a busy port beside a dead one
// restarts in either order, and with nothing holding it stays unknown.
func TestOrHoldsDespiteUnknownSibling(t *testing.T) {
	cache := map[string]checks.Result{
		"smtp_25":  {Busy: true, Message: "smtp 127.0.0.1:25: server busy: Too many connections (421)"},
		"smtp_587": {Unavailable: true, Message: "connection refused"},
		"skipped":  {OK: true, Skipped: true},
	}
	busy := map[string]any{ConditionFailed: map[string]any{FieldCheck: "smtp_25"}}
	dead := map[string]any{ConditionFailed: map[string]any{FieldCheck: "smtp_587"}}
	for _, order := range [][]any{{busy, dead}, {dead, busy}} {
		ev := &Evaluator{Cache: cache}
		rule := Rule{Name: "restart", Type: RuleRemediation, If: map[string]any{ConditionOr: order}, Actions: []Action{{Type: ActionRestart}}}
		if got, err := ev.EvalRule(t.Context(), rule); !got || err != nil || ev.Unknown {
			t.Fatalf("or %v = %v, %v (unknown %v); want a restart in either order", order, got, err, ev.Unknown)
		}
	}
	ev := &Evaluator{Cache: cache}
	onlyBusy := Rule{Name: "restart", Type: RuleRemediation, If: map[string]any{ConditionOr: []any{busy, busy}}, Actions: []Action{{Type: ActionRestart}}}
	if got, err := ev.EvalRule(t.Context(), onlyBusy); got || err != nil || !ev.Unknown {
		t.Fatalf("or of busy operands = %v, %v (unknown %v); want an unknown no-match", got, err, ev.Unknown)
	}
	// A guard whose OR holds still blocks; one that holds nothing fails closed.
	holds := map[string]any{ConditionOr: []any{
		map[string]any{ConditionActive: map[string]any{FieldCheck: "skipped"}},
		map[string]any{ConditionFailed: map[string]any{FieldCheck: "smtp_587"}},
	}}
	guard := []Rule{{Type: RuleGuard, Name: "guard", Blocks: []string{string(ActionRestart)}, If: holds}}
	if blocked, _, err := Guard(t.Context(), guard, string(ActionRestart), &Evaluator{Cache: map[string]checks.Result{
		"skipped": {OK: true, Skipped: true}, "smtp_587": {OK: false},
	}}); !blocked || err != nil {
		t.Fatalf("a guard whose or holds must block: blocked=%v err=%v", blocked, err)
	}
}
