package rules

import (
	"testing"

	"sermo/internal/checks"
)

func TestAdvisoryCannotTriggerOperations(t *testing.T) {
	failed := map[string]any{ConditionFailed: map[string]any{FieldCheck: "libraries"}}
	for _, unavailable := range []bool{false, true} {
		for _, ok := range []bool{false, true} {
			for _, node := range []map[string]any{failed, {ConditionNot: failed}, {ConditionAnd: []any{failed, failed}}, {ConditionOr: []any{failed, failed}}} {
				for _, action := range []ActionType{ActionStart, ActionStop, ActionRestart, ActionReload, ActionResume} {
					e := &Evaluator{Cache: map[string]checks.Result{"libraries": {OK: ok, Unavailable: unavailable, ActionBlocked: true}}}
					match, err := e.EvalRule(t.Context(), Rule{Type: RuleRemediation, If: node, Actions: []Action{{Type: action}}})
					if match || err != nil || !e.Unknown {
						t.Fatalf("advisory authorized %s: %v, %v", action, match, err)
					}
				}
			}
		}
		e := &Evaluator{Cache: map[string]checks.Result{"libraries": {Unavailable: unavailable, ActionBlocked: true}}}
		if match, err := e.EvalRule(t.Context(), Rule{Type: RuleAlert, If: failed, Actions: []Action{{Type: ActionAlert}}}); !match || err != nil {
			t.Fatalf("advisory alert lost: %v %v", match, err)
		}
		if _, _, err := Guard(t.Context(), []Rule{{Type: RuleGuard, Blocks: []string{string(ActionRestart)}, If: failed}}, string(ActionRestart), e); err == nil {
			t.Fatal("guard did not fail closed")
		}
	}
}

// A result that keeps OK states no failure even when it is unavailable and
// blocks actions: an incomplete library inventory that found nothing must
// leave an alert on failed: unknown, not fire it.
func TestUnavailableOKResultDoesNotFireAnAlert(t *testing.T) {
	failed := map[string]any{ConditionFailed: map[string]any{FieldCheck: "libraries"}}
	e := &Evaluator{Cache: map[string]checks.Result{"libraries": {OK: true, Unavailable: true, ActionBlocked: true}}}
	match, err := e.EvalRule(t.Context(), Rule{Type: RuleAlert, If: failed, Actions: []Action{{Type: ActionAlert}}})
	if match || err != nil || !e.Unknown {
		t.Fatalf("alert = %v, %v, unknown %v; want unknown", match, err, e.Unknown)
	}
}

// EvalAdvisory reads advisory evidence the way the rule's alert would, so the
// worker can deliver that alert, but keeps a busy check unknown as the rule's
// operation does; EvalRule and guards are untouched by it.
func TestEvalAdvisoryReadsOnlyAdvisoryEvidenceAsFailure(t *testing.T) {
	failed := map[string]any{ConditionFailed: map[string]any{FieldCheck: "stale"}}
	restart := Rule{Type: RuleRemediation, If: failed, Actions: []Action{{Type: ActionAlert, Message: "stale"}, {Type: ActionRestart}}}
	for _, tc := range []struct {
		name    string
		res     checks.Result
		holds   bool
		unknown bool
	}{
		{"advisory failure", checks.Result{OK: false, ActionBlocked: true}, true, false},
		{"advisory pass", checks.Result{OK: true, ActionBlocked: true}, false, false},
		{"busy", checks.Result{OK: false, Busy: true}, false, true},
		{"busy and advisory", checks.Result{OK: false, Busy: true, ActionBlocked: true}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Evaluator{Cache: map[string]checks.Result{"stale": tc.res}}
			if match, err := e.EvalRule(t.Context(), restart); match || err != nil {
				t.Fatalf("EvalRule = %v, %v; advisory or busy evidence must not authorize the restart", match, err)
			}
			held, err := e.EvalAdvisory(t.Context(), restart)
			if held != tc.holds || err != nil || e.Unknown != tc.unknown {
				t.Fatalf("EvalAdvisory = %v, %v, unknown %v; want %v, unknown %v", held, err, e.Unknown, tc.holds, tc.unknown)
			}
			if match, _ := e.EvalRule(t.Context(), restart); match {
				t.Fatal("EvalAdvisory leaked its reading into the next EvalRule")
			}
			guard := Rule{Name: "g", Type: RuleGuard, Blocks: []string{string(ActionRestart)}, If: failed}
			if _, _, err := Guard(t.Context(), []Rule{guard}, string(ActionRestart), e); tc.res.ActionBlocked && err == nil {
				t.Fatal("a guard must still fail closed on advisory evidence")
			}
		})
	}
}
