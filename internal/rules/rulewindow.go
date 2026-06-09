package rules

import (
	"context"
	"fmt"
	"strings"
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
		case "failed", "active":
			if m, ok := body.(map[string]any); ok {
				if c := asString(m["check"]); c != "" {
					return op + ":" + c
				}
			}
		case "metric":
			if m, ok := body.(map[string]any); ok {
				name := asString(m["name"])
				if name == "" {
					name = asString(m["metric"])
				}
				if name != "" {
					return "metric:" + name
				}
			}
		case "service":
			if m, ok := body.(map[string]any); ok {
				if s := asString(m["service"]); s != "" {
					return "service:" + s
				}
			}
		case "process":
			if m, ok := body.(map[string]any); ok {
				if n := asString(m["name"]); n != "" {
					return "process:" + n
				}
			}
		case "file":
			if m, ok := body.(map[string]any); ok {
				if p := asString(m["path"]); p != "" {
					return "file:" + p
				}
			}
		case "command":
			return "command"
		case "changed":
			if m, ok := body.(map[string]any); ok {
				if p := asString(m["path"]); p != "" {
					return "changed:" + p
				}
			}
		case "and", "or":
			if list, ok := body.([]any); ok {
				parts := make([]string, 0, len(list))
				for _, item := range list {
					if sub, ok := item.(map[string]any); ok {
						parts = append(parts, FormatCondition(sub))
					}
				}
				return op + "(" + strings.Join(parts, ", ") + ")"
			}
		case "not":
			if sub, ok := body.(map[string]any); ok {
				return "not(" + FormatCondition(sub) + ")"
			}
		}
		return op
	}
	return ""
}

// BuildRuleWindowReports snapshots remediation and alert rules after their
// windows were updated for the cycle. eval may be nil (condition stays false).
func BuildRuleWindowReports(ruleSet []Rule, windows map[string]*WindowState, eval func(context.Context, Rule) (bool, error)) []RuleWindowReport {
	var out []RuleWindowReport
	for _, r := range ruleSet {
		if r.Type != RuleRemediation && r.Type != RuleAlert {
			continue
		}
		ws := windows[r.Name]
		cond := false
		if eval != nil {
			var err error
			cond, err = eval(context.Background(), r)
			if err != nil {
				cond = false
			}
		}
		// Then is already the primary action (the operation if any, else the
		// first), so its type is the reported action.
		out = append(out, RuleWindowReport{
			Name:          r.Name,
			Type:          string(r.Type),
			Action:        string(r.Then.Type),
			Condition:     FormatCondition(r.If),
			ConditionTrue: cond,
			Window:        WindowDescription(r),
			Progress:      ws.Progress(r), // Progress/IsFiring nil-guard the receiver
			Firing:        ws.IsFiring(r),
		})
	}
	return out
}

// String returns a debug-friendly summary.
func (r RuleWindowReport) String() string {
	return fmt.Sprintf("%s %s %s progress=%s firing=%v", r.Name, r.Type, r.Action, r.Progress, r.Firing)
}
