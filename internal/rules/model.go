// Package rules models and evaluates Sermo's guard/remediation/alert rules
// (sections 13-17): the condition tree (and/or/not; failed/active over named
// checks or inline probes; file/command/service/process/metric leaves), guard
// evaluation, for/within windows, the remediation policy (cooldown, rate limit,
// backoff), and single- or multi-action `then` blocks.
package rules

import (
	"maps"
	"slices"
)

// RuleType classifies a rule (section 16).
type RuleType string

// Rule types (section 16).
const (
	RuleRemediation RuleType = "remediation"
	RuleGuard       RuleType = "guard"
	RuleAlert       RuleType = "alert"
)

// ActionType is a rule's then.action (section 16).
type ActionType string

// Rule action types (section 16).
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
	Name    string
	Type    RuleType
	If      map[string]any
	For     *ForWindow
	Within  *WithinWindow
	Actions []Action // all actions in order (post-MVP multi-action then)
	Blocks  []string
}

// Primary is the action other code treats as the rule's main one: the operation
// (restart/start/stop) if present, else the first.
func (r Rule) Primary() Action {
	for _, a := range r.Actions {
		switch a.Type {
		case ActionRestart, ActionStart, ActionStop:
			return a
		}
	}
	if len(r.Actions) > 0 {
		return r.Actions[0]
	}
	return Action{}
}

// OperationAction returns the rule's restart/start/stop action, if any.
func (r Rule) OperationAction() (ActionType, bool) {
	for _, a := range r.Actions {
		switch a.Type {
		case ActionRestart, ActionStart, ActionStop:
			return a.Type, true
		}
	}
	return "", false
}

// AlertMessages returns the messages of the rule's alert actions, in order.
func (r Rule) AlertMessages() []string {
	var out []string
	for _, a := range r.Actions {
		if a.Type == ActionAlert {
			out = append(out, a.Message)
		}
	}
	return out
}

// parseActions parses a `then` block into one or more actions. The single form
// `then: {action, message}` and the multi form `then: {actions: [...]}` are both
// accepted (section 16).
func parseActions(then map[string]any) []Action {
	if list, ok := then["actions"].([]any); ok {
		var out []Action
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, Action{Type: ActionType(asString(m["type"])), Message: asString(m["message"])})
			}
		}
		return out
	}
	return []Action{{Type: ActionType(asString(then["action"])), Message: asString(then["message"])}}
}

// ParseRules extracts the resolved `rules` section into Rules, skipping
// `enabled: false` entries and reporting malformed ones as warnings. Rules are
// returned in name order (guards are evaluated in this order, section 13).
func ParseRules(tree map[string]any) ([]Rule, []string) {
	raw, ok := tree["rules"].(map[string]any)
	if !ok {
		return nil, nil
	}

	// Fallback window applied to any rule that declares neither `for` nor
	// `within`, from the merged `rule_window` block (section 13). Absent or
	// default-equivalent, both are nil and rules keep the built-in immediate
	// default.
	fbFor, fbWithin := ParseRuleWindow(tree["rule_window"])

	var rules []Rule
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(raw)) {
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
		actions := parseActions(thenNode)
		forWin, withinWin := ParseWindow(entry)
		if _, hasFor := entry["for"]; !hasFor {
			if _, hasWithin := entry["within"]; !hasWithin {
				forWin, withinWin = fbFor, fbWithin
			}
		}
		rules = append(rules, Rule{
			Name:    name,
			Type:    RuleType(asString(entry["type"])),
			If:      ifNode,
			For:     forWin,
			Within:  withinWin,
			Actions: actions,
			Blocks:  stringList(entry["blocks"]),
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

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}
