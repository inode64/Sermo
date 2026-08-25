package rules

import (
	"slices"
	"testing"
)

func TestWalkConditionLeaves(t *testing.T) {
	node := map[string]any{ConditionAnd: []any{
		map[string]any{ConditionFailed: map[string]any{FieldCheck: "http"}},
		map[string]any{ConditionNot: map[string]any{
			ConditionOr: []any{
				map[string]any{ConditionMetric: map[string]any{FieldName: "memory"}},
				map[string]any{ConditionChanged: map[string]any{FieldPath: "/etc/app.conf"}},
			},
		}},
		"malformed child",
	}}

	var operators []string
	stopped := WalkConditionLeaves(node, func(operator string, _ any) bool {
		operators = append(operators, operator)
		return false
	})
	if stopped {
		t.Fatal("complete walk reported an early stop")
	}
	if want := []string{ConditionFailed, ConditionMetric, ConditionChanged}; !slices.Equal(operators, want) {
		t.Fatalf("operators = %v, want %v", operators, want)
	}
}

func TestWalkConditionLeavesStopsEarly(t *testing.T) {
	node := map[string]any{ConditionOr: []any{
		map[string]any{ConditionFailed: map[string]any{FieldCheck: "http"}},
		map[string]any{ConditionActive: map[string]any{FieldCheck: "backup"}},
	}}

	visits := 0
	stopped := WalkConditionLeaves(node, func(_ string, _ any) bool {
		visits++
		return true
	})
	if !stopped || visits != 1 {
		t.Fatalf("stopped = %v, visits = %d, want true, 1", stopped, visits)
	}
}

func TestWalkConditionLeavesVisitsEveryRootLeaf(t *testing.T) {
	// Runtime safety discovery must inspect all recognized leaves even when a
	// hand-built condition bypasses validation and contains multiple operators.
	node := map[string]any{
		ConditionMetric:  map[string]any{FieldName: "memory"},
		ConditionChanged: map[string]any{FieldPath: "/etc/app.conf"},
	}
	seen := map[string]bool{}
	WalkConditionLeaves(node, func(operator string, _ any) bool {
		seen[operator] = true
		return false
	})
	if !seen[ConditionMetric] || !seen[ConditionChanged] {
		t.Fatalf("visited operators = %v, want metric and changed", seen)
	}
}
