package config

import (
	"maps"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

// validateCheckLevels reports the shape errors of a check's `levels:` block.
// declared is the narrowest severity the chain declares (unset when none does):
// a level must rise above it. Inert tiers are not errors; Warnings reports them.
func validateCheckLevels(path, typ string, entry map[string]any, declared severity.Level, add addFunc) {
	for _, msg := range checks.ValidateLevels(typ, entry, declared).Errors {
		add("%s.%s", path, msg)
	}
}

// validateServiceWatchGrading checks the gravity a service watch entry
// declares beside its check: a valid severity, which the generated check
// inherits, and no levels — tiers restate the check's own thresholds, so they
// belong inside check:, where a host watch requires them too.
func validateServiceWatchGrading(name string, entry map[string]any, add addFunc) {
	validateSeverityField(watchPath(name), entry, add)
	if _, present := entry[checks.CheckKeyLevels]; present {
		add("%s.%s grade the check's thresholds: declare them inside %s", watchPath(name), checks.CheckKeyLevels, WatchKeyCheck)
	}
}

// rejectLevels reports a `levels:` block where nothing grades it: a preflight
// check or an inline rule probe reads only the verdict.
func rejectLevels(path string, entry map[string]any, where string, add addFunc) {
	if _, present := entry[checks.CheckKeyLevels]; present {
		add("%s.%s are not supported on %s: it reads only the verdict", path, checks.CheckKeyLevels, where)
	}
}

// validateWatchCheckLevels validates the levels of a watch's check block. A
// multi-metric type grades one metric at a time, so its levels live in the
// metric block (validateMetricLevels), never on the shared check block.
func validateWatchCheckLevels(checkPath, typ string, entry, check map[string]any, add addFunc) {
	if checks.IsMultiMetricType(typ) {
		if _, present := check[checks.CheckKeyLevels]; present {
			add("%s.%s grade one metric: declare them inside the metric block", checkPath, checks.CheckKeyLevels)
		}
		return
	}
	validateCheckLevels(checkPath, typ, check, checks.DeclaredSeverity(entry, check), add)
}

// MetricCheckEntry is the check entry one metric block of a multi-metric
// watch builds into: the block's condition keys (not its then/for/within/
// clear), overridden by the shared check block, plus the metric it selects.
// Levels grade one metric's threshold, so only the metric block's `levels`
// count. The daemon builds each metric watch from it and validation grades
// the same entry, so the two cannot disagree about a tier.
func MetricCheckEntry(check map[string]any, key string, metric map[string]any) map[string]any {
	entry := map[string]any{}
	for k, v := range metric {
		switch k {
		case rules.RuleFieldThen, rules.RuleFieldFor, rules.RuleFieldWithin, rules.RuleFieldClear:
		default:
			entry[k] = v
		}
	}
	maps.Copy(entry, check)
	entry[checks.CheckKeyMetric] = key
	if levels, ok := metric[checks.CheckKeyLevels]; ok {
		entry[checks.CheckKeyLevels] = levels
	} else {
		delete(entry, checks.CheckKeyLevels)
	}
	return entry
}

// validateMetricLevels validates one metric block's levels.
func validateMetricLevels(prefix, typ, key string, entry, metric map[string]any, add addFunc) {
	if _, present := metric[checks.CheckKeyLevels]; !present {
		return
	}
	check, _ := entry[WatchKeyCheck].(map[string]any)
	validateCheckLevels(prefix, typ, MetricCheckEntry(check, key, metric), checks.DeclaredSeverity(entry, check, metric), add)
}

// Warnings returns the advisory findings of a configuration that Validate
// accepts: configuration that loads and runs, but not the way its author
// likely meant. Today that is a `levels:` tier Sermo ignores because it is not
// stricter than the threshold below it, or not above the check's own severity
// — typically a catalog tier left behind when an operator raised the base
// threshold. The daemon logs these and `sermoctl config validate` prints them;
// neither refuses the configuration.
func Warnings(cfg *Config) []Issue {
	var issues []Issue
	warn := func(scope, path, typ string, entry map[string]any, declared severity.Level) {
		for _, msg := range checks.ValidateLevels(typ, entry, declared).Ignored {
			issues = append(issues, Issue{Scope: scope, Msg: path + "." + msg})
		}
	}
	// Pruned, like the daemon: a watch gated off on this host grades nothing.
	watches, _ := cfg.resolveWatches(true)
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok || cfgval.Disabled(entry) {
			continue
		}
		watchLevelWarnings(name, entry, func(path, typ string, check map[string]any, declared severity.Level) {
			warn(globalScope, path, typ, check, declared)
		})
	}
	inputs := cfg.newResolutionInputs()
	for _, name := range cfg.ServiceNames {
		resolved, errs := cfg.resolveServiceWithInputs(name, true, inputs)
		if len(errs) > 0 || resolved.Tree == nil || cfgval.Disabled(resolved.Tree) {
			continue
		}
		serviceLevelWarnings(resolved.Tree, cfg.promotedWatchChecks(resolved.Name, resolved.Tree), func(path, typ string, entry map[string]any, declared severity.Level) {
			warn(name, path, typ, entry, declared)
		})
	}
	return issues
}

type levelVisitor func(path, typ string, entry map[string]any, declared severity.Level)

// promotedWatchChecks names the checks a resolved service generated from its
// own watches (one without then, or with a rule-class action): the watch is
// gone from the resolved tree and a check of its name took its place.
func (c *Config) promotedWatchChecks(service string, tree map[string]any) map[string]bool {
	merged, err := c.mergedService(service, nil)
	if err != nil {
		return nil
	}
	declared, _ := merged[sectionWatches].(map[string]any)
	remaining, _ := tree[sectionWatches].(map[string]any)
	generated, _ := tree[sectionChecks].(map[string]any)
	promoted := map[string]bool{}
	for name := range declared {
		_, kept := remaining[name]
		_, checked := generated[name]
		if !kept && checked {
			promoted[name] = true
		}
	}
	return promoted
}

// watchLevelWarnings visits every graded check entry of one host watch: its
// check block, or each metric block of a multi-metric check.
func watchLevelWarnings(name string, entry map[string]any, visit levelVisitor) {
	check, ok := entry[WatchKeyCheck].(map[string]any)
	if !ok {
		return
	}
	typ := cfgval.String(check[checks.CheckKeyType])
	if !checks.IsMultiMetricType(typ) {
		visit(watchCheckPath(name), typ, check, checks.DeclaredSeverity(entry, check))
		return
	}
	metrics, _ := entry[sectionMetrics].(map[string]any)
	for _, key := range slices.Sorted(maps.Keys(metrics)) {
		if metric, ok := metrics[key].(map[string]any); ok {
			visit(watchMetricPath(name, key), typ, MetricCheckEntry(check, key, metric), checks.DeclaredSeverity(entry, check, metric))
		}
	}
}

// serviceLevelWarnings visits every graded check entry of one resolved service:
// its checks and the check blocks of the fire-and-forget watches that remain.
// A check promoted from a service watch is reported under the watch's check
// block, the path the operator wrote.
func serviceLevelWarnings(tree map[string]any, promoted map[string]bool, visit levelVisitor) {
	checkEntries, _ := tree[sectionChecks].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(checkEntries)) {
		entry, ok := checkEntries[name].(map[string]any)
		if !ok {
			continue
		}
		path := sectionChecks + "." + name
		if promoted[name] {
			path = watchCheckPath(name)
		}
		visit(path, cfgval.String(entry[checks.CheckKeyType]), entry, checks.DeclaredSeverity(entry))
	}
	watches, _ := tree[sectionWatches].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			continue
		}
		if check, ok := entry[WatchKeyCheck].(map[string]any); ok {
			visit(watchCheckPath(name), cfgval.String(check[checks.CheckKeyType]), check, checks.DeclaredSeverity(entry, check))
		}
	}
}
