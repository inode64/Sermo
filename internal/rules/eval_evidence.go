package rules

import (
	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// explain reports whether a condition node holds by this cycle's results and
// the readings that decide it, as "<check>: <message>", without probing. An
// `and` that fails is explained by its false children and an `or` that holds
// by its true ones; otherwise every child decides. A leaf with no usable
// result this cycle (metric, changed, never evaluated, or a reading a guard
// cannot rely on) is unknown and names nothing.
func (e *Evaluator) explain(node any) (holds, known bool, readings []string) {
	operator, operand, err := conditionOperator(asConditionMap(node))
	if err != nil {
		return false, false, nil
	}
	switch operator {
	case ConditionNot:
		holds, known, readings = e.explain(operand)
		return !holds, known, readings
	case ConditionAnd, ConditionOr:
		children, _ := operand.([]any)
		and := operator == ConditionAnd
		known = true
		var decisive, all []string
		decided := false
		for _, child := range children {
			childHolds, childKnown, childReadings := e.explain(child)
			if !childKnown {
				known = false
				continue
			}
			all = append(all, childReadings...)
			if childHolds != and {
				decided = true
				decisive = append(decisive, childReadings...)
			}
		}
		if decided {
			return !and, true, decisive
		}
		return and, known, all
	default:
		res, name, holds, ok := e.cachedLeaf(operator, operand)
		if !ok || e.unavailableCheckError(name, res) != nil {
			return false, false, nil
		}
		return holds, true, []string{name + ": " + res.Message}
	}
}

// cachedLeaf reads a check leaf from this cycle's check cache or inline memo,
// never running a probe: its result, the name it is reported under (the
// check, or the inline probe's type) and whether the leaf holds. ok is false
// for a leaf with no result this cycle and for metric and changed leaves,
// which keep none.
func (e *Evaluator) cachedLeaf(operator string, operand any) (res checks.Result, name string, holds, ok bool) {
	m, isMap := operand.(map[string]any)
	if !isMap {
		return checks.Result{}, "", false, false
	}
	switch operator {
	case ConditionFailed, ConditionActive:
		if name = cfgval.AsString(m[FieldCheck]); name != "" {
			res, ok = e.Cache[name]
		} else if _, inline, err := inlineEntry(m); err == nil {
			name = inline
			res, ok = e.memo[inlineMemoKey(name, m)]
		}
		return res, name, res.OK == (operator == ConditionActive), ok
	// The leaves eval memoizes under their check type (see runInline).
	case ConditionFile:
		name = checks.CheckTypeFile
	case ConditionProcess:
		name = checks.CheckTypeProcess
	case ConditionService:
		name = checks.CheckTypeService
	case ConditionCommand:
		name = checks.CheckTypeCommand
	default:
		return checks.Result{}, "", false, false
	}
	res, ok = e.memo[inlineMemoKey(name, m)]
	holds = res.OK
	if operator == ConditionFile {
		holds = res.OK == fileWantsExists(m)
	}
	return res, name, holds, ok
}
