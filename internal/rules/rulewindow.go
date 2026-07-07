package rules

import (
	"context"
	"fmt"
	"sermo/internal/cfgval"
	"strings"
	"time"
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
	if len(node) == 0 {
		return ""
	}
	if len(node) != 1 {
		return "?"
	}
	for op, body := range node {
		switch op {
		case ConditionFailed, ConditionActive:
			if m, ok := body.(map[string]any); ok {
				if c := cfgval.AsString(m[FieldCheck]); c != "" {
					return op + ":" + c
				}
			}
		case ConditionMetric:
			if m, ok := body.(map[string]any); ok {
				name := cfgval.AsString(m[FieldName])
				if name == "" {
					name = cfgval.AsString(m[FieldMetric])
				}
				if name != "" {
					return "metric:" + name
				}
			}
		case ConditionService:
			if m, ok := body.(map[string]any); ok {
				if s := cfgval.AsString(m[ConditionService]); s != "" {
					return "service:" + s
				}
			}
		case ConditionProcess:
			if m, ok := body.(map[string]any); ok {
				if n := cfgval.AsString(m[FieldName]); n != "" {
					return "process:" + n
				}
			}
		case ConditionFile:
			if m, ok := body.(map[string]any); ok {
				if p := cfgval.AsString(m[FieldPath]); p != "" {
					return "file:" + p
				}
			}
		case ConditionCommand:
			return "command"
		case ConditionChanged:
			if m, ok := body.(map[string]any); ok {
				if p := cfgval.AsString(m[FieldPath]); p != "" {
					return "changed:" + p
				}
			}
		case ConditionAnd, ConditionOr:
			if list, ok := body.([]any); ok {
				parts := make([]string, 0, len(list))
				for _, item := range list {
					if sub, ok := item.(map[string]any); ok {
						parts = append(parts, FormatCondition(sub))
					}
				}
				return op + "(" + strings.Join(parts, ", ") + ")"
			}
		case ConditionNot:
			if sub, ok := body.(map[string]any); ok {
				return "not(" + FormatCondition(sub) + ")"
			}
		}
		return op
	}
	return ""
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
