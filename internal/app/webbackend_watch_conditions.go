package app

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

const (
	watchConditionFieldGrowth  = "growth"
	watchMetricFieldSeparator  = "."
	watchMetricSuffixChange    = checks.CheckKeyChange
	watchMetricSuffixDelta     = checks.CheckKeyDelta
	watchMetricSuffixExpect    = checks.CheckKeyExpect
	watchMetricSuffixOn        = checks.CheckKeyOn
	watchMetricSuffixThreshold = checks.CheckKeyThreshold
)

func checkMap(entry map[string]any) map[string]any {
	check, _ := entry[config.WatchKeyCheck].(map[string]any)
	return check
}

func metricsMap(entry map[string]any) map[string]any {
	metricEntries, _ := entry[config.SectionMetrics].(map[string]any)
	return metricEntries
}

func watchConditionText(c web.WatchCondition) string {
	return strings.Join(slices.DeleteFunc([]string{c.Field, c.Op, c.Value}, func(s string) bool {
		return strings.TrimSpace(s) == ""
	}), " ")
}

func watchConditions(check, metricEntries map[string]any) []web.WatchCondition {
	if check == nil {
		return nil
	}
	var out []web.WatchCondition
	for _, field := range watchConditionFields(check) {
		m, ok := check[field].(map[string]any)
		if !ok {
			continue
		}
		out = append(out, comparisonCondition(field, m))
	}
	out = append(out, watchTypeConditions(check)...)
	out = append(out, watchCommonConditions(check)...)
	out = append(out, watchMetricConditions(metricEntries)...)
	return out
}

func watchTypeConditions(check map[string]any) []web.WatchCondition {
	if builder := watchTypeConditionBuilders[cfgval.AsString(check[checks.CheckKeyType])]; builder != nil {
		return builder(check)
	}
	return nil
}

// Every per-check-type builder below shapes the same four condition forms: a
// bare "field value", a "field op value" comparison, a boolean flag rendered as
// an equality, and a comparison read from a {op, value} mapping. The shaping
// lives here once so each builder only names the fields it owns; the caller
// keeps the cfgval.AsString/cfgval.String choice, which is per-field semantics
// (strict string vs. any scalar), not shaping.

// appendValue appends a bare "field value" condition unless value is empty.
func appendValue(out []web.WatchCondition, field, value string) []web.WatchCondition {
	if value == "" {
		return out
	}
	return append(out, web.WatchCondition{Field: field, Value: value})
}

// appendCompare appends a "field op value" condition unless value is empty.
func appendCompare(out []web.WatchCondition, field, op, value string) []web.WatchCondition {
	if value == "" {
		return out
	}
	return append(out, web.WatchCondition{Field: field, Op: op, Value: value})
}

// appendFlag appends a "field == bool" condition when check carries key as a
// boolean, so an explicit false is reported as configured.
func appendFlag(out []web.WatchCondition, check map[string]any, key, field string) []web.WatchCondition {
	value, ok := check[key].(bool)
	if !ok {
		return out
	}
	return append(out, flagCondition(field, value))
}

// appendEnabledFlag appends a "field == true" condition only when the flag is
// set to true, for flags whose false form is the default and carries no
// information for the dashboard.
func appendEnabledFlag(out []web.WatchCondition, check map[string]any, key, field string) []web.WatchCondition {
	if value, ok := check[key].(bool); !ok || !value {
		return out
	}
	return append(out, flagCondition(field, true))
}

func flagCondition(field string, value bool) web.WatchCondition {
	return web.WatchCondition{Field: field, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(value)}
}

// comparisonCondition renders a {op: ..., value: ...} mapping as one condition.
func comparisonCondition(field string, values map[string]any) web.WatchCondition {
	return web.WatchCondition{
		Field: field,
		Op:    cfgval.AsString(values[checks.CheckKeyOp]),
		Value: cfgval.String(values[checks.CheckKeyValue]),
	}
}

// conditionValueOr reports value, or fallback when the check left it unset.
func conditionValueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

var watchTypeConditionBuilders = map[string]func(map[string]any) []web.WatchCondition{
	checks.CheckTypeRAID:          raidWatchConditions,
	checks.CheckTypeReplication:   replicationWatchConditions,
	checks.CheckTypeCount:         countWatchConditions,
	checks.CheckTypeFile:          fileWatchConditions,
	checks.CheckTypeProcess:       processWatchConditions,
	checks.CheckTypeProcessPolicy: processPolicyWatchConditions,
	checks.CheckTypeRoute:         routeWatchConditions,
	checks.CheckTypeFirewallRules: firewallWatchConditions,
	checks.CheckTypeFailedUnits:   failedUnitsWatchConditions,
	checks.CheckTypeSize:          sizeWatchConditions,
}

func replicationWatchConditions(check map[string]any) []web.WatchCondition {
	out := []web.WatchCondition{
		{Field: checks.DataKeyIOStopped, Op: cfgval.CompareOpEqual, Value: "0"},
		{Field: checks.DataKeySQLStopped, Op: cfgval.CompareOpEqual, Value: "0"},
	}
	if behind, ok := check[checks.CheckKeyBehind].(map[string]any); ok {
		out = append(out, web.WatchCondition{
			Field: checks.DataKeyBehindSeconds,
			Op:    cfgval.AsString(behind[checks.CheckKeyOp]),
			Value: cfgval.String(behind[checks.CheckKeyValue]),
		})
	}
	return out
}

func raidWatchConditions(check map[string]any) []web.WatchCondition {
	out := appendValue(nil, checks.DataKeyArray, cfgval.AsString(check[checks.CheckKeyArray]))
	return appendEnabledFlag(out, check, checks.CheckKeySysfsChanges, checks.CheckKeySysfsChanges)
}

func countWatchConditions(check map[string]any) []web.WatchCondition {
	out := appendValue(nil, checks.DataKeyPath, cfgval.AsString(check[checks.CheckKeyPath]))
	out = appendValue(out, checks.DataKeyOf, cfgval.AsString(check[checks.CheckKeyOf]))
	out = appendFlag(out, check, checks.CheckKeyRecursive, checks.DataKeyRecursive)
	out = appendFlag(out, check, checks.CheckKeyIncludeHidden, checks.CheckKeyIncludeHidden)
	if count, ok := check[checks.CheckKeyCount].(map[string]any); ok {
		out = append(out, comparisonCondition(checks.DataKeyCount, count))
	} else if op := cfgval.AsString(check[checks.CheckKeyOp]); op != "" {
		// The flat `op:`/`value:` spelling: gated on the operator, not the
		// value, so `op: > ` with an absent value still reports the operator.
		out = append(out, web.WatchCondition{Field: checks.DataKeyCount, Op: op, Value: cfgval.String(check[checks.CheckKeyValue])})
	}
	if delta, ok := check[checks.CheckKeyDelta].(map[string]any); ok {
		out = append(out, comparisonCondition(checks.CheckKeyDelta, delta))
	}
	return appendValue(out, checks.CheckKeyWithin, cfgval.String(check[checks.CheckKeyWithin]))
}

func processWatchConditions(check map[string]any) []web.WatchCondition {
	out := appendCompare(nil, checks.CheckKeyFor, cfgval.CompareOpGreaterEqual, cfgval.String(check[checks.CheckKeyFor]))
	return appendEnabledFlag(out, check, checks.CheckKeyGone, checks.CheckKeyGone)
}

func processPolicyWatchConditions(check map[string]any) []web.WatchCondition {
	return appendValue(nil, checks.CheckKeyUser, cfgval.AsString(check[checks.CheckKeyUser]))
}

func routeWatchConditions(check map[string]any) []web.WatchCondition {
	family := conditionValueOr(cfgval.AsString(check[checks.CheckKeyFamily]), checks.FamilyIPv4)
	out := []web.WatchCondition{{Field: checks.DataKeyFamily, Op: cfgval.CompareOpEqual, Value: family}}
	return appendCompare(out, checks.DataKeyInterface, cfgval.CompareOpEqual, cfgval.AsString(check[checks.CheckKeyInterface]))
}

func firewallWatchConditions(check map[string]any) []web.WatchCondition {
	backend := conditionValueOr(cfgval.AsString(check[checks.CheckKeyBackend]), checks.FirewallBackendAuto)
	minRules := conditionValueOr(cfgval.String(check[checks.CheckKeyMinRules]),
		strconv.FormatUint(watchFirewallDefaultMinRules, watchReadingNumericBase))
	return []web.WatchCondition{
		{Field: checks.DataKeyBackend, Op: cfgval.CompareOpEqual, Value: backend},
		{Field: checks.DataKeyRules, Op: cfgval.CompareOpGreaterEqual, Value: minRules},
	}
}

// failedUnitsWatchConditions names the init backend the check asks for. The
// count predicate is rendered generically; its default (> 0) is added here so a
// check that omits it still shows what fires.
func failedUnitsWatchConditions(check map[string]any) []web.WatchCondition {
	backend := conditionValueOr(cfgval.AsString(check[checks.CheckKeyBackend]), string(servicemgr.BackendAuto))
	out := appendCompare(nil, checks.DataKeyBackend, cfgval.CompareOpEqual, backend)
	if _, present := check[checks.CheckKeyCount].(map[string]any); present {
		return out
	}
	return appendCompare(out, checks.DataKeyCount, cfgval.CompareOpGreater, watchFailedUnitsDefaultCount)
}

func sizeWatchConditions(check map[string]any) []web.WatchCondition {
	out := appendValue(nil, checks.DataKeyPath, cfgval.AsString(check[checks.CheckKeyPath]))
	out = appendCompare(out, watchConditionFieldGrowth, cfgval.CompareOpGreaterEqual, cfgval.String(check[checks.CheckKeyGrowBy]))
	out = appendValue(out, checks.CheckKeyWithin, cfgval.String(check[checks.CheckKeyWithin]))
	return appendFlag(out, check, checks.CheckKeyIncludeHidden, checks.CheckKeyIncludeHidden)
}

func watchCommonConditions(check map[string]any) []web.WatchCondition {
	out := appendFlag(nil, check, checks.CheckKeyMounted, checks.DataKeyMounted)
	if cfgval.AsString(check[checks.CheckKeyType]) == checks.CheckTypeOOM {
		if _, ok := check[checks.CheckKeyDelta].(map[string]any); !ok {
			out = append(out, web.WatchCondition{Field: checks.CheckKeyDelta, Op: cfgval.CompareOpGreater, Value: watchConditionDefaultDelta})
		}
	}
	return out
}

func watchConditionFields(check map[string]any) []string {
	checkType := cfgval.AsString(check[checks.CheckKeyType])
	switch checkType {
	case checks.CheckTypeStorage:
		return checks.StoragePredFields
	case checks.CheckTypeMemory:
		return checks.MemoryPredFields
	case checks.CheckTypePressure:
		return checks.PressurePredFields
	case checks.CheckTypeLoad:
		return checks.LoadPredFields
	case checks.CheckTypeFDS:
		return checks.FdsPredFields
	case checks.CheckTypePIDs:
		return checks.PidsPredFields
	case checks.CheckTypeConntrack:
		return checks.ConntrackPredFields
	case checks.CheckTypeZombies:
		return checks.ZombiePredFields
	case checks.CheckTypeFailedUnits:
		return checks.FailedUnitsPredFields
	case checks.CheckTypeInotify:
		return checks.InotifyPredFields
	case checks.CheckTypeOOM:
		return []string{checks.CheckKeyDelta}
	case checks.CheckTypeProcess:
		return []string{metrics.MetricCPU, metrics.MetricMemory, metrics.MetricIO}
	case checks.CheckTypeDiskIO:
		return checks.DiskIOPredFields
	case checks.CheckTypeSensors:
		return checks.SensorPredFields
	case checks.CheckTypeHdparm:
		return checks.HdparmPredFields
	case checks.CheckTypeSmart:
		return checks.SmartPredFields
	case checks.CheckTypeStorCLI, checks.CheckTypeSSACLI:
		return checks.HardwareRAIDPredFields
	case checks.CheckTypeRAID:
		return checks.RaidPredFields
	case checks.CheckTypeLVM:
		return checks.LVMPredFields
	case checks.CheckTypeEDAC:
		return checks.EdacPredFields
	default:
		return nil
	}
}

func fileWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if paths, err := config.FileWatchPaths(check); err == nil && len(paths) > 0 {
		field := checks.DataKeyPaths
		if len(paths) == 1 {
			field = checks.DataKeyPath
		}
		out = append(out, web.WatchCondition{Field: field, Value: strings.Join(paths, displayListSeparator)})
	}
	out = appendFlag(out, check, checks.CheckKeyRecursive, checks.DataKeyRecursive)
	out = appendFlag(out, check, checks.CheckKeyIncludeHidden, checks.CheckKeyIncludeHidden)
	if size, ok := check[checks.CheckKeySize].(map[string]any); ok {
		if on := cfgval.AsString(size[checks.CheckKeyOn]); on != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeySize, Value: on})
		} else {
			out = append(out, comparisonCondition(checks.DataKeySize, size))
		}
	}
	for _, field := range []string{checks.CheckKeyPermissions, checks.CheckKeyOwner} {
		if m, ok := check[field].(map[string]any); ok {
			out = append(out, web.WatchCondition{Field: field, Value: cfgval.AsString(m[checks.CheckKeyOn])})
		}
	}
	if m, ok := check[checks.CheckKeyExistence].(map[string]any); ok {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyExistence, Value: cfgval.AsString(m[checks.CheckKeyOn])})
	}
	return appendCompare(out, checks.CheckKeyOlderThan, cfgval.CompareOpGreater, cfgval.String(check[checks.CheckKeyOlderThan]))
}

func watchMetricConditions(metricEntries map[string]any) []web.WatchCondition {
	if len(metricEntries) == 0 {
		return nil
	}
	var out []web.WatchCondition
	for _, metric := range slices.Sorted(maps.Keys(metricEntries)) {
		entry, _ := metricEntries[metric].(map[string]any)
		if len(entry) == 0 {
			continue
		}
		if on := cfgval.AsString(entry[checks.CheckKeyOn]); on != "" {
			out = append(out, web.WatchCondition{Field: watchMetricConditionField(metric, watchMetricSuffixOn), Value: on})
		}
		if expect := cfgval.AsString(entry[checks.CheckKeyExpect]); expect != "" {
			out = append(out, web.WatchCondition{Field: watchMetricConditionField(metric, watchMetricSuffixExpect), Op: cfgval.CompareOpEqual, Value: expect})
		}
		for _, comparison := range []struct{ source, suffix string }{
			{checks.CheckKeyDelta, watchMetricSuffixDelta},
			{checks.CheckKeyThreshold, watchMetricSuffixThreshold},
		} {
			if values, ok := entry[comparison.source].(map[string]any); ok {
				out = append(out, watchMetricComparisonCondition(metric, comparison.suffix, values))
			}
		}
		if change, ok := entry[checks.CheckKeyChange].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, watchMetricSuffixChange),
				Op:    cfgval.CompareOpGreater,
				Value: cfgval.String(change[checks.CheckKeyDelta]),
			})
		}
		for _, field := range []string{checks.LevelFieldUsedPct, checks.LevelFieldFreePct, checks.LevelFieldFreeBytes} {
			m, ok := entry[field].(map[string]any)
			if !ok {
				continue
			}
			out = append(out, watchMetricComparisonCondition(metric, field, m))
		}
	}
	return out
}

func watchMetricComparisonCondition(metric, suffix string, values map[string]any) web.WatchCondition {
	return comparisonCondition(watchMetricConditionField(metric, suffix), values)
}

func watchMetricConditionField(metric, suffix string) string {
	return metric + watchMetricFieldSeparator + suffix
}
