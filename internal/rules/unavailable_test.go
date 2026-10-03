package rules

import (
	"os"
	"path/filepath"
	"testing"

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
