package rules

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
)

// RuleWindowReport is a read-only operator view of one rule's window progress.
type RuleWindowReport struct {
	Name          string
	Type          string // remediation | alert
	Action        string
	Condition     string
	ConditionTrue bool
	Window        string
	Progress      string
	Firing        bool
}

// FormatCondition renders a rule's if-tree as a compact one-line summary.
func FormatCondition(node map[string]any) string {
	op, body, err := conditionOperator(node)
	if err != nil {
		return invalidCondition(node)
	}
	if formatted := formatConditionLeaf(op, body); formatted != "" {
		return formatted
	}
	return formatConditionBranch(op, body)
}

func invalidCondition(node map[string]any) string {
	if len(node) == 0 {
		return ""
	}
	return "?"
}

func formatConditionLeaf(op string, body any) string {
	switch op {
	case ConditionFailed, ConditionActive:
		return formatConditionField(op, body, FieldCheck)
	case ConditionMetric:
		return formatConditionField(op, body, FieldName)
	case ConditionService:
		return formatConditionField(op, body, FieldState)
	case ConditionProcess:
		return formatConditionFirstField(op, body, FieldExe, FieldUser, FieldState)
	case ConditionFile:
		return formatConditionField(op, body, FieldPath)
	case ConditionCommand:
		return ConditionCommand
	case ConditionChanged:
		return formatConditionFirstField(op, body, FieldPath, FieldApp)
	default:
		return ""
	}
}

func formatConditionFirstField(label string, body any, fields ...string) string {
	for _, field := range fields {
		if formatted := formatConditionField(label, body, field); formatted != "" {
			return formatted
		}
	}
	return ""
}

func formatConditionField(label string, body any, field string) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	if value := cfgval.AsString(m[field]); value != "" {
		return label + ":" + value
	}
	return ""
}

func formatConditionBranch(op string, body any) string {
	switch op {
	case ConditionAnd, ConditionOr:
		return formatConditionList(op, body)
	case ConditionNot:
		if sub, ok := body.(map[string]any); ok {
			return "not(" + FormatCondition(sub) + ")"
		}
	}
	return op
}

func formatConditionList(op string, body any) string {
	list, ok := body.([]any)
	if !ok {
		return op
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		if sub, ok := item.(map[string]any); ok {
			parts = append(parts, FormatCondition(sub))
		}
	}
	return op + "(" + strings.Join(parts, ", ") + ")"
}

// BuildRuleWindowReportsAt snapshots remediation and alert rules after their
// windows were updated for the cycle, evaluating conditions under the caller's
// cycle context (the probes are memoized, but a cancelled cycle must not be
// outlived). at is the read time for duration-based windows. eval may be nil
// (condition stays false).
func BuildRuleWindowReportsAt(ctx context.Context, ruleSet []Rule, windows map[string]*WindowState, at time.Time, eval func(context.Context, Rule) (bool, error)) []RuleWindowReport {
	var out []RuleWindowReport
	for i := range ruleSet {
		if ruleSet[i].Type != RuleRemediation && ruleSet[i].Type != RuleAlert {
			continue
		}
		ws := windows[ruleSet[i].Name]
		cond := false
		if eval != nil {
			var err error
			cond, err = eval(ctx, ruleSet[i])
			if err != nil {
				cond = false
			}
		}
		status := ws.statusAt(ruleSet[i], at)
		// Primary is the operation if any, else the first action; its type is the
		// reported action.
		out = append(out, RuleWindowReport{
			Name:          ruleSet[i].Name,
			Type:          string(ruleSet[i].Type),
			Action:        string(ruleSet[i].Primary().Type),
			Condition:     FormatCondition(ruleSet[i].If),
			ConditionTrue: cond,
			Window:        WindowDescription(ruleSet[i]),
			Progress:      status.progress,
			Firing:        status.firing,
		})
	}
	return out
}

// String returns a debug-friendly summary.
func (r RuleWindowReport) String() string {
	return fmt.Sprintf("%s %s %s progress=%s firing=%v", r.Name, r.Type, r.Action, r.Progress, r.Firing)
}
