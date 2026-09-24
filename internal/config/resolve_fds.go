package config

import (
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/rules"
)

const (
	// fdsCheckName is the injected service metric comparing the process of the
	// tree closest to its own soft RLIMIT_NOFILE against fds_limit.
	fdsCheckName = "fds"
	// fdsRuleName is the rule it drives.
	fdsRuleName = "restart-if-fds-high"
	// defaultFDsLimit leaves room to act before accept() fails with EMFILE
	// while staying above what a healthy daemon holds: across a fleet of 45
	// hosts no daemon sat above a third of its limit, and the collector that
	// leaked one socket per connection crossed 80% hours before it stopped
	// accepting anything.
	defaultFDsLimit = "80%"
	// fdsRuleDuration filters a burst of connections from a leak: a daemon that
	// holds the level for three minutes is not going to release it.
	fdsRuleDuration = "3m"
	// fdsMessageSuffix names the two facts an operator needs: how close the
	// worst process is to its own limit, and what happens at 100%.
	// ${check.threshold} and ${check.value} are filled by the worker.
	fdsMessageSuffix = " file descriptor usage stayed above ${check.threshold} of a process's " +
		"open-files limit for ${rule.duration} (current ${check.value}); at 100% it stops accepting connections"
)

// expandFDs injects the fds check and its rule into every service whose
// processes discovery can attribute. Descriptor exhaustion is a failure mode
// of any daemon that accepts connections and the only remedy a daemon has for
// a leak is a restart, so the check is not opt-in; what is configurable is the
// threshold (`fds_limit`, a percentage, or `false` to inject nothing) and
// whether the rule may restart (`restart_on_fds_high`).
//
// Services whose selectors mark workload as `delegated: true` get nothing: the
// metric is measured over every process discovery attributes, and a container
// or a user's session near its own limit says nothing about the daemon.
func expandFDs(tree map[string]any) []string {
	limit, allowRestart, errs := fdsSettings(tree)
	delete(tree, keyFDsLimit)
	delete(tree, keyRestartOnFDsHigh)
	if len(errs) > 0 {
		return errs
	}
	if limit == "" || !fdsApplies(tree) {
		return nil
	}
	// A checks section in any shape but a mapping is the operator's to have
	// validated; the check cannot be added to it, and a rule without its check
	// would only add a second error on top of theirs.
	if raw, present := tree[sectionChecks]; present && raw != nil {
		if _, isMap := raw.(map[string]any); !isMap {
			return nil
		}
	}
	checkEntry := map[string]any{
		checks.CheckKeyType:  checks.CheckTypeMetric,
		checks.CheckKeyScope: checks.MetricScopeService,
		checks.CheckKeyName:  fdsCheckName,
		checks.CheckKeyOp:    cfgval.CompareOpGreater,
		checks.CheckKeyValue: limit,
		// A tree with no readable limit, or no process yet, is a warning for
		// the check and never a failed service.
		checks.CheckKeyOptional: true,
	}
	if err := injectGenerated(tree, sectionChecks, fdsCheckName, "check", fdsCheckName, checkEntry); err != "" {
		return []string{err}
	}
	rule := fdsRule(allowRestart, restartOnChangeDisplayName(tree)+fdsMessageSuffix)
	if err := injectGenerated(tree, rules.SectionRules, fdsRuleName, "rule", fdsCheckName, rule); err != "" {
		return []string{err}
	}
	return nil
}

// fdsSettings reads the two keys. Absent means the default threshold and a
// restart, the convention restart_on_stale_binary uses. An empty limit means
// the operator disabled the sensor with `fds_limit: false`.
func fdsSettings(tree map[string]any) (limit string, allowRestart bool, errs []string) {
	limit = defaultFDsLimit
	if raw, present := tree[keyFDsLimit]; present {
		switch v := raw.(type) {
		case bool:
			if v {
				errs = append(errs, fmt.Sprintf("%s must be a percentage such as %s, or false to disable the check", keyFDsLimit, defaultFDsLimit))
			}
			limit = ""
		default:
			if _, ok := cfgval.Percent(raw); !ok {
				errs = append(errs, fmt.Sprintf("%s must be a percentage such as %s, or false to disable the check", keyFDsLimit, defaultFDsLimit))
			}
			limit = cfgval.String(raw)
		}
	}
	allowRestart = true
	if raw, present := tree[keyRestartOnFDsHigh]; present {
		v, ok := raw.(bool)
		if !ok {
			errs = append(errs, fmt.Sprintf(validationBooleanFormat, keyRestartOnFDsHigh))
		}
		allowRestart = v
	}
	return limit, allowRestart, errs
}

// fdsApplies mirrors staleBinaryApplies — discovery must be able to attribute
// a process — and additionally leaves out a service that delegates part of its
// tree, because the peak would then be measured over workload the daemon does
// not own.
func fdsApplies(tree map[string]any) bool {
	if !staleBinaryApplies(tree) {
		return false
	}
	selectors, _ := process.ParseSelectors(tree)
	for _, selector := range selectors {
		if selector.Delegated {
			return false
		}
	}
	return true
}

// fdsRule builds the rule: alert first, then restart, the canonical generated
// shape. A metric is a condition check, so the rule fires while it is active.
func fdsRule(allowRestart bool, message string) map[string]any {
	then := generatedRestartActions(allowRestart, message)
	return map[string]any{
		rules.RuleFieldType: string(generatedRuleType(then)),
		rules.RuleFieldIf: map[string]any{
			rules.ConditionActive: map[string]any{rules.FieldCheck: fdsCheckName},
		},
		rules.RuleFieldFor:  map[string]any{rules.WindowKeyDuration: fdsRuleDuration},
		rules.RuleFieldThen: then,
	}
}
