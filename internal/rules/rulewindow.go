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
	op, body, ok := conditionOperator(node)
	if !ok {
		return invalidCondition(node)
	}
	if formatted := formatConditionLeaf(op, body); formatted != "" {
		return formatted
	}
	return formatConditionBranch(op, body)
}

func conditionOperator(node map[string]any) (string, any, bool) {
	if len(node) != 1 {
		return "", nil, false
	}
	for op, body := range node {
		return op, body, true
	}
	return "", nil, false
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
		if name := formatConditionField("metric", body, FieldName); name != "" {
			return name
		}
		return formatConditionField("metric", body, FieldMetric)
	case ConditionService:
		return formatConditionField("service", body, ConditionService)
	case ConditionProcess:
		return formatConditionField("process", body, FieldName)
	case ConditionFile:
		return formatConditionField("file", body, FieldPath)
	case ConditionCommand:
		return "command"
	case ConditionChanged:
		return formatConditionField("changed", body, FieldPath)
	default:
		return ""
	}
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

// BuildRuleWindowReports snapshots remediation and alert rules after their
// windows were updated for the cycle, evaluating conditions under the caller's
// cycle context (the probes are memoized, but a cancelled cycle must not be
// outlived). eval may be nil (condition stays false).
func BuildRuleWindowReports(ctx context.Context, ruleSet []Rule, windows map[string]*WindowState, eval func(context.Context, Rule) (bool, error)) []RuleWindowReport {
	return BuildRuleWindowReportsAt(ctx, ruleSet, windows, time.Now(), eval)
}

// BuildRuleWindowReportsAt is BuildRuleWindowReports with an explicit read time
// for duration-based windows.
func BuildRuleWindowReportsAt(ctx context.Context, ruleSet []Rule, windows map[string]*WindowState, at time.Time, eval func(context.Context, Rule) (bool, error)) []RuleWindowReport {
	var out []RuleWindowReport
	for _, r := range ruleSet {
		if r.Type != RuleRemediation && r.Type != RuleAlert {
			continue
		}
		ws := windows[r.Name]
		cond := false
		if eval != nil {
			var err error
			cond, err = eval(ctx, r)
			if err != nil {
				cond = false
			}
		}
		// Primary is the operation if any, else the first action; its type is the
		// reported action.
		out = append(out, RuleWindowReport{
			Name:          r.Name,
			Type:          string(r.Type),
			Action:        string(r.Primary().Type),
			Condition:     FormatCondition(r.If),
			ConditionTrue: cond,
			Window:        WindowDescription(r),
			Progress:      ws.ProgressAt(r, at), // ProgressAt/IsFiringAt are nil-safe
			Firing:        ws.IsFiringAt(r, at),
		})
	}
	return out
}

// String returns a debug-friendly summary.
func (r RuleWindowReport) String() string {
	return fmt.Sprintf("%s %s %s progress=%s firing=%v", r.Name, r.Type, r.Action, r.Progress, r.Firing)
}
