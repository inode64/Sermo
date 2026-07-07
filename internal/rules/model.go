// Package rules models and evaluates Sermo's guard/remediation/alert rules:
// the condition tree (and/or/not; failed/active over named
// checks or inline probes; file/command/service/process/metric leaves), guard
// evaluation, for/within windows, the remediation policy (cooldown, rate limit,
// backoff), and single- or multi-action `then` blocks.
package rules

import (
	"maps"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// RuleType classifies a rule.
type RuleType string

// Rule types.
const (
	RuleRemediation RuleType = "remediation"
	RuleGuard       RuleType = "guard"
	RuleAlert       RuleType = "alert"
)

// ActionType is a rule's then.action.
type ActionType string

// Rule action types.
const (
	ActionRestart ActionType = "restart"
	ActionStart   ActionType = "start"
	ActionStop    ActionType = "stop"
	ActionReload  ActionType = "reload"
	ActionResume  ActionType = "resume"
	ActionAlert   ActionType = "alert"
	ActionBlock   ActionType = "block"
)

// Action is one resolved entry from a rule's then block.
type Action struct {
	Type    ActionType
	Message string
}

// ForWindow requires the condition to hold for N consecutive cycles or for a
// wall-clock duration.
type ForWindow struct {
	Cycles   int
	Duration time.Duration
}

// WithinWindow requires the condition to be true at least MinMatches times in
// the last Cycles cycles or within the last wall-clock Duration.
type WithinWindow struct {
	Cycles     int
	Duration   time.Duration
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
	Actions []Action // all actions in order
	Blocks  []string
	// Notify selects which notifiers receive this rule's alert messages: explicit
	// names, the `none` sentinel to suppress, or empty to inherit the global
	// default. Resolution and delivery happen in the worker (the rules package has
	// no notifier dependency).
	Notify []string
}

// Primary is the action other code treats as the rule's main one: the operation
// (restart/start/stop/reload/resume) if present, else the first.
func (r Rule) Primary() Action {
	for _, a := range r.Actions {
		switch a.Type {
		case ActionRestart, ActionStart, ActionStop, ActionReload, ActionResume:
			return a
		}
	}
	if len(r.Actions) > 0 {
		return r.Actions[0]
	}
	return Action{}
}

// OperationAction returns the rule's restart/start/stop/reload/resume action, if any.
func (r Rule) OperationAction() (ActionType, bool) {
	for _, a := range r.Actions {
		switch a.Type {
		case ActionRestart, ActionStart, ActionStop, ActionReload, ActionResume:
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
// accepted.
func parseActions(then map[string]any) []Action {
	if list, ok := then[RuleFieldActions].([]any); ok {
		var out []Action
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, Action{Type: ActionType(cfgval.AsString(m[RuleFieldType])), Message: cfgval.AsString(m[RuleFieldMessage])})
			}
		}
		return out
	}
	return []Action{{Type: ActionType(cfgval.AsString(then[RuleFieldAction])), Message: cfgval.AsString(then[RuleFieldMessage])}}
}

// ConditionUsesSystemMetric walks a condition tree and reports whether any
// leaf reads a `scope: system` metric — directly, or (when checks is non-nil)
// through a failed/active reference to a `type: metric, scope: system` check.
// Runtime defense-in-depth for safety invariant 13: a system-wide metric may
// only drive alert rules, never remediation, even if a rule slips past static
// validation (catalog bug, partial reload, hand-built Rule).
func ConditionUsesSystemMetric(node map[string]any, refChecks map[string]any) bool {
	for key, v := range node {
		switch key {
		case ConditionAnd, ConditionOr:
			list, ok := v.([]any)
			if !ok {
				continue
			}
			for _, c := range list {
				if m, ok := c.(map[string]any); ok && ConditionUsesSystemMetric(m, refChecks) {
					return true
				}
			}
		case ConditionNot:
			if m, ok := v.(map[string]any); ok && ConditionUsesSystemMetric(m, refChecks) {
				return true
			}
		case ConditionMetric:
			if m, ok := v.(map[string]any); ok && cfgval.AsString(m[FieldScope]) == checks.MetricScopeSystem {
				return true
			}
		case ConditionFailed, ConditionActive:
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if ref := cfgval.AsString(m[FieldCheck]); ref != "" {
				if refChecks == nil {
					continue
				}
				if c, ok := refChecks[ref].(map[string]any); ok &&
					cfgval.AsString(c[FieldType]) == checks.CheckTypeMetric && cfgval.AsString(c[FieldScope]) == checks.MetricScopeSystem {
					return true
				}
				continue
			}
			if c, ok := m[ConditionMetric].(map[string]any); ok && cfgval.AsString(c[FieldScope]) == checks.MetricScopeSystem {
				return true
			}
		}
	}
	return false
}

// ReferencedChecks merges the sections a rule's failed/active references may
// point at (checks and preflight), for system-metric detection.
func ReferencedChecks(tree map[string]any) map[string]any {
	out := map[string]any{}
	for _, section := range []string{"checks", "preflight"} {
		if m, ok := tree[section].(map[string]any); ok {
			for name, entry := range m {
				out[name] = entry
			}
		}
	}
	return out
}

// ParseRules extracts the resolved `rules` section into Rules, skipping
// `enabled: false` entries and reporting malformed ones as warnings. Rules are
// returned in name order (guards are evaluated in this order).
func ParseRules(tree map[string]any) ([]Rule, []string) {
	raw, ok := tree[SectionRules].(map[string]any)
	if !ok {
		return nil, nil
	}

	// Fallback window applied to any rule that declares neither `for` nor
	// `within`, from the merged `rule_window` block. Absent or
	// default-equivalent, both are nil and rules keep the built-in immediate
	// default.
	fbFor, fbWithin := ParseRuleWindow(tree[SectionRuleWindow])

	refChecks := ReferencedChecks(tree)
	var rules []Rule
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "rule "+name+" is not a mapping")
			continue
		}
		if cfgval.Disabled(entry) {
			continue
		}
		ifNode, ok := entry[RuleFieldIf].(map[string]any)
		if !ok {
			warnings = append(warnings, "rule "+name+" has no if condition")
			continue
		}
		thenNode, ok := entry[RuleFieldThen].(map[string]any)
		if !ok {
			warnings = append(warnings, "rule "+name+" has no then action")
			continue
		}
		if RuleType(cfgval.AsString(entry[RuleFieldType])) != RuleAlert && ConditionUsesSystemMetric(ifNode, refChecks) {
			warnings = append(warnings, "rule "+name+": a scope: system metric may only drive alert rules; rule dropped (safety invariant)")
			continue
		}
		actions := parseActions(thenNode)
		forWin, withinWin := ParseWindow(entry)
		if _, hasFor := entry[RuleFieldFor]; !hasFor {
			if _, hasWithin := entry[RuleFieldWithin]; !hasWithin {
				forWin, withinWin = fbFor, fbWithin
			}
		}
		rules = append(rules, Rule{
			Name:    name,
			Type:    RuleType(cfgval.AsString(entry[RuleFieldType])),
			If:      ifNode,
			For:     forWin,
			Within:  withinWin,
			Actions: actions,
			Blocks:  cfgval.StringList(entry[RuleFieldBlocks]),
			Notify:  cfgval.StringList(entry[RuleFieldNotify]),
		})
	}
	return rules, warnings
}
