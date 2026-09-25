package config

import (
	"fmt"

	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/rules"
)

const (
	// staleBinaryCheckName is the injected check that reports processes still
	// running a binary replaced on disk.
	staleBinaryCheckName = "stale-binary"
	// staleBinaryRuleName is the rule it drives.
	staleBinaryRuleName = "restart-on-stale-binary"
	// staleBinaryMessageSuffix is deliberately explicit about the two facts an
	// operator needs: the service is healthy, and it is not running the version
	// that is installed. ${check.value} is filled by the worker at alert time.
	staleBinaryMessageSuffix = " is running a binary that was replaced on disk " +
		"(${check.value} process(es)); it keeps serving the previous version until it restarts"
)

// expandStaleBinary injects the stale-binary check and its rule into every
// service whose processes discovery can attribute: the ones it declares, or
// the ones the init backend names for the unit when the service declares none.
// The condition can hit any service — it is the ordinary result of upgrading a
// package without restarting — so this is not opt-in; what is configurable is
// whether the rule may restart.
//
// `restart_on_stale_binary: false` keeps the alert and the notification and
// drops only the restart action, which also downgrades the rule from
// remediation to alert (a remediation rule must carry an operation action).
// The flag governs this trigger alone: a manual restart, and remediation for a
// real failure, are unaffected.
func expandStaleBinary(tree map[string]any) []string {
	allowRestart, flagErrs := staleBinaryRestartAllowed(tree)
	if len(flagErrs) > 0 {
		return flagErrs
	}
	delete(tree, keyRestartOnStaleBinary)
	if !staleBinaryApplies(tree) {
		return nil
	}

	// A replaced executable needs operator attention (and may trigger the
	// generated alert/restart rule), but it does not make the running service
	// unavailable. Keep the raw result for the rule's `failed:` condition while
	// marking the injected check as a verdictless state sensor for health and
	// SLA accounting.
	checkEntry := map[string]any{
		checks.CheckKeyType:    checks.CheckTypeStaleBinary,
		checks.CheckKeyReports: checks.ReportsState,
	}
	if err := injectGenerated(tree, sectionChecks, staleBinaryCheckName, "check", checks.CheckTypeStaleBinary, checkEntry); err != "" {
		return []string{err}
	}
	rule := staleBinaryRule(allowRestart, staleBinaryAlertMessage(tree))
	if err := injectGenerated(tree, rules.SectionRules, staleBinaryRuleName, "rule", checks.CheckTypeStaleBinary, rule); err != "" {
		return []string{err}
	}
	return nil
}

// injectGenerated adds a generated entry to a named section, creating the
// section when absent and refusing to shadow an entry the operator wrote. It
// returns "" on success. The names it claims are reserved, so the refusal has
// to say what is claiming them — the operator never asked for this entry, so
// feature names which sugar is responsible.
func injectGenerated(tree map[string]any, section, name, noun, feature string, value any) string {
	entries, isMap := tree[section].(map[string]any)
	if _, present := tree[section]; present && !isMap {
		// Preserve the malformed value so validation can also identify it.
		return fmt.Sprintf(validationMappingFormat, section)
	}
	if entries == nil {
		entries = map[string]any{}
	}
	if _, exists := entries[name]; exists {
		return fmt.Sprintf("the injected %s %s would overwrite %s %q; rename that %s", feature, noun, noun, name, noun)
	}
	entries[name] = value
	tree[section] = entries
	return ""
}

// staleBinaryRule builds the rule. Alert first, then restart, matching the
// shape restart_on_change already uses so the operator is told before anything
// acts.
func staleBinaryRule(allowRestart bool, message string) map[string]any {
	// Stale binaries use failed: because a healthy sensor is not a restart trigger.
	return generatedSensorRule(
		map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: staleBinaryCheckName}},
		nil, allowRestart, message,
	)
}

// generatedSensorRule keeps alert-before-restart ordering and the rule type
// consistent for generated sensors. A nil window uses the service fallback.
func generatedSensorRule(condition, window map[string]any, allowRestart bool, message string) map[string]any {
	then := generatedRestartActions(allowRestart, message)
	rule := map[string]any{
		rules.RuleFieldType: string(generatedRuleType(then)),
		rules.RuleFieldIf:   condition,
		rules.RuleFieldThen: then,
	}
	if window != nil {
		rule[rules.RuleFieldFor] = window
	}
	return rule
}

// staleBinaryRestartAllowed reads the flag. Absent means allowed, the same
// convention restart_on_change uses for its config/version permissions.
func staleBinaryRestartAllowed(tree map[string]any) (bool, []string) {
	v, present := tree[keyRestartOnStaleBinary]
	if !present {
		return true, nil
	}
	allowed, ok := v.(bool)
	if !ok {
		return false, []string{fmt.Sprintf(validationBooleanFormat, keyRestartOnStaleBinary)}
	}
	return allowed, nil
}

// staleBinaryApplies reports whether discovery can attribute a process to the
// service at all, which is what the stale-binary check needs. Declared
// selectors qualify; so does declaring none, because the worker then derives
// the unit's own processes from the init backend and finds a replaced binary
// there just the same — a service left out on that account showed
// restart_required without ever alerting or restarting. Only an explicit
// `processes: {}` (nothing resident to attribute) and an external control
// backend that declares no processes (a container's or domain's PID set is
// not something the init unit names) are left out; a domain or container
// whose profile names its host processes (a VM's qemu) keeps the check, as it
// always had. This mirrors the lifecycle's process mode so the two cannot
// drift apart.
func staleBinaryApplies(tree map[string]any) bool {
	mode := resolvedProcessMode(tree)
	if mode == ServiceProcessNone {
		return false
	}
	if _, external := tree[SectionControl]; external && mode == ServiceProcessInit {
		return false
	}
	return true
}

// serviceDeclaresProcesses reports whether the service gives discovery
// selectors of its own. The strays sensor needs that: it asks the parser
// discovery itself uses, so this cannot drift into accepting a declaration
// that yields no selector (an empty pidfiles map, or a path list that resolves
// to nothing).
func serviceDeclaresProcesses(tree map[string]any) bool {
	selectors, _ := process.ParseSelectors(tree)
	return len(selectors) > 0
}

// staleBinaryAlertMessage names the service the same way restart_on_change
// does; the ${check.value} placeholder stays for the worker to fill.
func staleBinaryAlertMessage(tree map[string]any) string {
	return restartOnChangeDisplayName(tree) + staleBinaryMessageSuffix
}

func generatedRestartActions(allowRestart bool, message string) map[string]any {
	if allowRestart {
		return restartOnChangeThen(message)
	}
	return map[string]any{rules.RuleFieldActions: []any{
		map[string]any{rules.RuleFieldType: string(rules.ActionAlert), rules.RuleFieldMessage: message},
	}}
}
