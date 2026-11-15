package config

import (
	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

const (
	// PrivateTmpCheckName is the injected check that reports a systemd
	// PrivateTmp directory deleted on the host under a running unit.
	PrivateTmpCheckName = "private-tmp"
	// privateTmpRuleName is the rule it drives.
	privateTmpRuleName = "restart-if-private-tmp-lost"
	// privateTmpCheckInterval keeps the mount-table reads off most cycles: the
	// loss is rare, and it costs nothing until the unit next reloads or stops.
	privateTmpCheckInterval = "5m"
	// privateTmpMessageSuffix states what broke and why the restart is the
	// remedy; ${check.value} is filled by the worker at alert time.
	privateTmpMessageSuffix = " lost its private /tmp (${check.value} mount(s) deleted on the host); " +
		"systemd cannot run its reload or stop commands until a restart recreates them"
)

// expandPrivateTmp injects the private-tmp check and its restart rule into
// every service whose processes discovery can attribute, the same set the
// stale-binary check covers. A cleanup that deletes /tmp/systemd-private-*
// under a running unit (systemd-tmpfiles excludes them; a hand-written one may
// not) leaves the daemon serving, but systemd rebuilds the unit's namespace
// from those directories for every ExecReload or ExecStop, so each fails with
// 226/NAMESPACE until the unit restarts and gets new ones. Under OpenRC, and
// for units without PrivateTmp=yes, the check never finds a private mount and
// stays OK.
//
// The check is a verdictless state sensor, like stale-binary: the service is
// still serving, so it must not count against its availability. A service
// with a delegated selector only alerts: its processes belong to another
// manager (a VM's qemu), which the operator restarts.
func expandPrivateTmp(tree map[string]any) []string {
	if !staleBinaryApplies(tree) {
		return nil
	}
	checkEntry := map[string]any{
		checks.CheckKeyType:    checks.CheckTypePrivateTmp,
		checks.CheckKeyReports: checks.ReportsState,
		EntryKeyInterval:       privateTmpCheckInterval,
	}
	if err := injectGenerated(tree, sectionChecks, PrivateTmpCheckName, "check", checks.CheckTypePrivateTmp, checkEntry); err != "" {
		return []string{err}
	}
	rule := generatedSensorRule(
		map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: PrivateTmpCheckName}},
		nil, !hasDelegatedSelector(tree), restartOnChangeDisplayName(tree)+privateTmpMessageSuffix,
	)
	rule[rules.RuleFieldSeverity] = string(severity.Warning)
	if err := injectGenerated(tree, rules.SectionRules, privateTmpRuleName, "rule", checks.CheckTypePrivateTmp, rule); err != "" {
		return []string{err}
	}
	return nil
}
