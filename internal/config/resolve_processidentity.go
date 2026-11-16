package config

import (
	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

const (
	// ProcessIdentityCheckName is the injected check that fails while the
	// service's processes match none of its strict main selectors.
	ProcessIdentityCheckName = "process-identity"
	// processIdentityRuleName is the alert it drives.
	processIdentityRuleName = "alert-if-process-identity-unverified"
	// processIdentityCycles keeps a start out of the alert: a launcher that has
	// not yet handed over to its daemon is attributed and matches no selector.
	processIdentityCycles = 3
	// processIdentityMessageSuffix sends the reader to the check, whose message
	// names the selector and the processes found.
	processIdentityMessageSuffix = " runs no process that its main selector names; " +
		checks.ProcessIdentityHint + " (see the " + ProcessIdentityCheckName + " check)"
)

// expandProcessIdentity injects the process-identity check and its alert into
// every service that declares a strict principal selector. An operation
// refuses to start or restart an active service whose processes match none
// (the profile names a launcher script instead of its interpreter, or the
// unit runs as another user), and nothing else reports that before the first
// restart that matters.
//
// The check is optional and graded warning: the daemon is serving, so it must
// not count against availability, yet the service reads warning instead of
// healthy. It never acts: correcting a selector is the operator's.
func expandProcessIdentity(tree map[string]any) []string {
	if !staleBinaryApplies(tree) {
		return nil
	}
	if selectors, _ := process.ParseSelectors(tree); !process.HasStrictPrincipal(selectors) {
		return nil
	}
	checkEntry := map[string]any{
		checks.CheckKeyType:     checks.CheckTypeProcessIdentity,
		checks.CheckKeyOptional: true,
		checks.CheckKeySeverity: string(severity.Warning),
	}
	if err := injectGenerated(tree, sectionChecks, ProcessIdentityCheckName, "check", checks.CheckTypeProcessIdentity, checkEntry); err != "" {
		return []string{err}
	}
	rule := generatedSensorRule(
		map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: ProcessIdentityCheckName}},
		map[string]any{rules.WindowKeyCycles: processIdentityCycles},
		false, restartOnChangeDisplayName(tree)+processIdentityMessageSuffix,
	)
	rule[rules.RuleFieldSeverity] = string(severity.Warning)
	if err := injectGenerated(tree, rules.SectionRules, processIdentityRuleName, "rule", checks.CheckTypeProcessIdentity, rule); err != "" {
		return []string{err}
	}
	return nil
}
