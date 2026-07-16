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
	"sermo/internal/emission"
)

// RuleType classifies a rule.
type RuleType string

// Rule types.
const (
	RuleRemediation RuleType = "remediation"
	RuleGuard       RuleType = "guard"
	RuleAlert       RuleType = "alert"
	// RuleTypeSummary is the user-facing list of rule types.
	RuleTypeSummary = string(RuleRemediation) + ", " + string(RuleGuard) + ", " + string(RuleAlert)
)

const (
	referencedChecksSectionChecks    = "checks"
	referencedChecksSectionPreflight = "preflight"
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
	// RuleActionSummary is the user-facing list of rule action types.
	RuleActionSummary = string(ActionRestart) + ", " +
		string(ActionStart) + ", " +
		string(ActionStop) + ", " +
		string(ActionReload) + ", " +
		string(ActionResume) + ", " +
		string(ActionAlert) + ", " +
		string(ActionBlock)
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
	// Emission optionally overrides the global event/notification cadence for this
	// rule. Empty fields inherit from the daemon's global emission policy.
	Emission emission.Policy
}

// Primary is the action other code treats as the rule's main one: the operation
// (restart/start/stop/reload/resume) if present, else the first.
func (r Rule) Primary() Action {
	for _, a := range r.Actions {
		switch a.Type {
		case ActionRestart, ActionStart, ActionStop, ActionReload, ActionResume:
			return a
		default: // alert/block actions are never the primary one
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
		default: // alert/block actions are not operations
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
func ConditionUsesSystemMetric(node, refChecks map[string]any) bool {
	for key, value := range node {
		if conditionEntryUsesSystemMetric(key, value, refChecks) {
			return true
		}
	}
	return false
}

func conditionEntryUsesSystemMetric(key string, value any, refChecks map[string]any) bool {
	switch key {
	case ConditionAnd, ConditionOr:
		return conditionListUsesSystemMetric(value, refChecks)
	case ConditionNot:
		return conditionNodeUsesSystemMetric(value, refChecks)
	case ConditionMetric:
		return metricNodeUsesSystemScope(value)
	case ConditionFailed, ConditionActive:
		return conditionReferenceUsesSystemMetric(value, refChecks)
	}
	return false
}

func conditionListUsesSystemMetric(value any, refChecks map[string]any) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, child := range list {
		if conditionNodeUsesSystemMetric(child, refChecks) {
			return true
		}
	}
	return false
}

func conditionNodeUsesSystemMetric(value any, refChecks map[string]any) bool {
	node, ok := value.(map[string]any)
	return ok && ConditionUsesSystemMetric(node, refChecks)
}

func metricNodeUsesSystemScope(value any) bool {
	metric, ok := value.(map[string]any)
	return ok && cfgval.AsString(metric[FieldScope]) == checks.MetricScopeSystem
}

func conditionReferenceUsesSystemMetric(value any, refChecks map[string]any) bool {
	condition, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if ref := cfgval.AsString(condition[FieldCheck]); ref != "" {
		return referencedCheckUsesSystemMetric(ref, refChecks)
	}
	return metricNodeUsesSystemScope(condition[ConditionMetric])
}

func referencedCheckUsesSystemMetric(name string, refChecks map[string]any) bool {
	if refChecks == nil {
		return false
	}
	check, ok := refChecks[name].(map[string]any)
	if !ok {
		return false
	}
	return cfgval.AsString(check[FieldType]) == checks.CheckTypeMetric && cfgval.AsString(check[FieldScope]) == checks.MetricScopeSystem
}

// ReferencedChecks merges the sections a rule's failed/active references may
// point at (checks and preflight), for system-metric detection.
func ReferencedChecks(tree map[string]any) map[string]any {
	out := map[string]any{}
	for _, section := range []string{referencedChecksSectionChecks, referencedChecksSectionPreflight} {
		if m, ok := tree[section].(map[string]any); ok {
			maps.Copy(out, m)
		}
	}
	return out
}

// ruleSubjectPrefix names a rule as the subject of a parse warning, e.g.
// "rule <name> is not a mapping".
const ruleSubjectPrefix = "rule "

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
			warnings = append(warnings, ruleSubjectPrefix+name+" is not a mapping")
			continue
		}
		if cfgval.Disabled(entry) {
			continue
		}
		ifNode, ok := entry[RuleFieldIf].(map[string]any)
		if !ok {
			warnings = append(warnings, ruleSubjectPrefix+name+" has no if condition")
			continue
		}
		thenNode, ok := entry[RuleFieldThen].(map[string]any)
		if !ok {
			warnings = append(warnings, ruleSubjectPrefix+name+" has no then action")
			continue
		}
		if RuleType(cfgval.AsString(entry[RuleFieldType])) != RuleAlert && ConditionUsesSystemMetric(ifNode, refChecks) {
			warnings = append(warnings, ruleSubjectPrefix+name+": a scope: system metric may only drive alert rules; rule dropped (safety invariant)")
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
			Name:     name,
			Type:     RuleType(cfgval.AsString(entry[RuleFieldType])),
			If:       ifNode,
			For:      forWin,
			Within:   withinWin,
			Actions:  actions,
			Blocks:   cfgval.StringList(entry[RuleFieldBlocks]),
			Notify:   cfgval.StringList(entry[RuleFieldNotify]),
			Emission: emission.Merge(entry[emission.Section], emission.Policy{}),
		})
	}
	return rules, warnings
}
