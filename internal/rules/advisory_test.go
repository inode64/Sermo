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
