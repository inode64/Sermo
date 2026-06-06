// Package rules models and evaluates Sermo's guard/remediation/alert rules
// (sections 13-17).
//
// This slice implements the condition tree (and/or/not, failed/active over
// named checks or inline probes, plus file/command/service leaves) and guard
// evaluation, which the operation engine consults before acting. Rule windows
// (for/within) and metric/process conditions are not yet implemented.
package rules

import "sort"

// RuleType classifies a rule (section 16).
type RuleType string

const (
	RuleRemediation RuleType = "remediation"
	RuleGuard       RuleType = "guard"
	RuleAlert       RuleType = "alert"
)

// ActionType is a rule's then.action (section 16).
type ActionType string

const (
	ActionRestart ActionType = "restart"
	ActionStart   ActionType = "start"
	ActionStop    ActionType = "stop"
	ActionAlert   ActionType = "alert"
	ActionBlock   ActionType = "block"
)

// Action is the resolved then: block of a rule (single action in the MVP).
type Action struct {
	Type    ActionType
	Message string
}

// ForWindow requires the condition to hold for N consecutive cycles (section 15).
type ForWindow struct {
	Cycles int
	Mode   string // only "consecutive" in the MVP
}

// WithinWindow requires the condition to be true at least MinMatches times in the
// last Cycles cycles (sliding window, section 15).
type WithinWindow struct {
	Cycles     int
	MinMatches int
}

// Rule is a resolved rule. If is kept as the generic condition tree; the
// evaluator walks it directly so a parse step does not duplicate the model.
type Rule struct {
	Name   string
	Type   RuleType
	If     map[string]any
	For    *ForWindow
	Within *WithinWindow
	Then   Action
	Blocks []string
}

// ParseRules extracts the resolved `rules` section into Rules, skipping
// `enabled: false` entries and reporting malformed ones as warnings. Rules are
// returned in name order (guards are evaluated in this order, section 13).
func ParseRules(tree map[string]any) ([]Rule, []string) {
	raw, ok := tree["rules"].(map[string]any)
	if !ok {
		return nil, nil
	}

	var rules []Rule
	var warnings []string
	for _, name := range sortedKeys(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "rule "+name+" is not a mapping")
			continue
		}
		if disabled(entry) {
			continue
		}
		ifNode, ok := entry["if"].(map[string]any)
		if !ok {
			warnings = append(warnings, "rule "+name+" has no if condition")
			continue
		}
		thenNode, ok := entry["then"].(map[string]any)
		if !ok {
			warnings = append(warnings, "rule "+name+" has no then action")
			continue
		}
		rules = append(rules, Rule{
			Name:   name,
			Type:   RuleType(asString(entry["type"])),
			If:     ifNode,
			For:    parseForWindow(entry["for"]),
			Within: parseWithinWindow(entry["within"]),
			Then:   Action{Type: ActionType(asString(thenNode["action"])), Message: asString(thenNode["message"])},
			Blocks: stringList(entry["blocks"]),
		})
	}
	return rules, warnings
}

func disabled(entry map[string]any) bool {
	v, ok := entry["enabled"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && !b
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func stringList(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s := asString(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
