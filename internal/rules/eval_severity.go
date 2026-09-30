package rules

import (
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/severity"
)

// RuleSeverity grades a rule's firing condition for this cycle. A declared
// `severity:` wins. Otherwise the rule takes the gravest grade among the
// failed:/active: leaves that hold on a failing check — a service watch that
// desugared into a check plus an alert rule therefore alerts at the grade its
// check's levels reached — and Error when no leaf carries one. Leaves under
// not: are skipped: a negated check that fails is the healthy branch. It reads
// only results this cycle already produced, so grading never probes.
func (e *Evaluator) RuleSeverity(r Rule) severity.Level {
	if r.Severity.Valid() {
		return r.Severity
	}
	_, graded := e.gradeNode(r.If)
	return graded.Resolved()
}

// gradeNode reports whether a condition node holds this cycle and the gravest
// grade among the leaves that make it hold. Only branches that hold carry a
// grade: an `or` takes it from its holding children, an `and` only when every
// child holds. A leaf this cycle's results cannot resolve (a `metric`, `file`
// or other non-check leaf, or a `not:`) counts as holding with no grade, so it
// never hides the grade of the check beside it.
func (e *Evaluator) gradeNode(node any) (holds bool, graded severity.Level) {
	operator, operand, err := conditionOperator(asConditionMap(node))
	if err != nil {
		return true, ""
	}
	switch operator {
	case ConditionAnd, ConditionOr:
		children, _ := operand.([]any)
		all, anyHolds := true, false
		var allGrade, anyGrade severity.Level
		for _, child := range children {
			childHolds, childGrade := e.gradeNode(child)
			all = all && childHolds
			allGrade = severity.Max(allGrade, childGrade)
			if childHolds {
				anyHolds = true
				anyGrade = severity.Max(anyGrade, childGrade)
			}
		}
		if operator == ConditionAnd {
			if all {
				return true, allGrade
			}
			return false, ""
		}
		return anyHolds, anyGrade
	case ConditionFailed, ConditionActive:
		res, ok := e.cachedResult(operand)
		if !ok {
			return true, ""
		}
		holds = res.OK
		if operator == ConditionFailed {
			holds = !res.OK
		}
		if holds && res.Observation() == checks.ObservationFailing {
			return true, res.Severity.Resolved()
		}
		return holds, ""
	default:
		return true, ""
	}
}

// asConditionMap reads a condition node, or nil for a malformed one.
func asConditionMap(node any) map[string]any {
	m, _ := node.(map[string]any)
	return m
}

// cachedResult returns a failed/active operand's result from this cycle's
// cache or inline memo, never running a probe.
func (e *Evaluator) cachedResult(operand any) (checks.Result, bool) {
	m, ok := operand.(map[string]any)
	if !ok {
		return checks.Result{}, false
	}
	if ref := cfgval.AsString(m[FieldCheck]); ref != "" {
		res, ok := e.Cache[ref]
		return res, ok
	}
	_, name, err := inlineEntry(m)
	if err != nil {
		return checks.Result{}, false
	}
	res, ok := e.memo[name+":"+normalizeKey(m)]
	return res, ok
}
