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
	"sermo/internal/web"
)

const (
	watchConditionFieldGrowth  = "growth"
	watchMetricFieldSeparator  = "."
	watchMetricSuffixChange    = "change"
	watchMetricSuffixDelta     = checks.CheckKeyDelta
	watchMetricSuffixExpect    = checks.CheckKeyExpect
	watchMetricSuffixOn        = checks.CheckKeyOn
	watchMetricSuffixThreshold = "threshold"
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
		out = append(out, web.WatchCondition{
			Field: field,
			Op:    cfgval.AsString(m[checks.CheckKeyOp]),
			Value: cfgval.String(m[checks.CheckKeyValue]),
		})
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

var watchTypeConditionBuilders = map[string]func(map[string]any) []web.WatchCondition{
	checks.CheckTypeRAID:          raidWatchConditions,
	checks.CheckTypeAutofs:        autofsWatchConditions,
	checks.CheckTypeCount:         countWatchConditions,
	checks.CheckTypeFile:          fileWatchConditions,
	checks.CheckTypeProcess:       processWatchConditions,
	checks.CheckTypeRoute:         routeWatchConditions,
	checks.CheckTypeFirewallRules: firewallWatchConditions,
	checks.CheckTypeSize:          sizeWatchConditions,
}

func raidWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if array := cfgval.AsString(check[checks.CheckKeyArray]); array != "" {
		out = append(out, web.WatchCondition{Field: checks.DataKeyArray, Value: array})
	}
	if changes, ok := check[checks.CheckKeySysfsChanges].(bool); ok && changes {
		out = append(out, web.WatchCondition{Field: checks.CheckKeySysfsChanges, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(changes)})
	}
	return out
}

func autofsWatchConditions(check map[string]any) []web.WatchCondition {
	if path := cfgval.AsString(check[checks.CheckKeyPath]); path != "" {
		return []web.WatchCondition{{Field: checks.DataKeyPath, Op: cfgval.CompareOpEqual, Value: path}}
	}
	if _, ok := check[checks.CheckKeyCount].(map[string]any); !ok {
		return []web.WatchCondition{{Field: checks.DataKeyCount, Op: cfgval.CompareOpGreaterEqual, Value: watchConditionDefaultMinimum}}
	}
	return nil
}

func countWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	for _, condition := range []struct{ field, value string }{
		{checks.DataKeyPath, cfgval.AsString(check[checks.CheckKeyPath])},
		{checks.DataKeyOf, cfgval.AsString(check[checks.CheckKeyOf])},
	} {
		if condition.value != "" {
			out = append(out, web.WatchCondition{Field: condition.field, Value: condition.value})
		}
	}
	for _, condition := range []struct{ source, field string }{
		{checks.CheckKeyRecursive, checks.DataKeyRecursive},
		{checks.CheckKeyIncludeHidden, checks.CheckKeyIncludeHidden},
	} {
		if value, ok := check[condition.source].(bool); ok {
			out = append(out, web.WatchCondition{Field: condition.field, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(value)})
		}
	}
	if count, ok := check[checks.CheckKeyCount].(map[string]any); ok {
		out = append(out, web.WatchCondition{Field: checks.DataKeyCount, Op: cfgval.AsString(count[checks.CheckKeyOp]), Value: cfgval.String(count[checks.CheckKeyValue])})
	} else if op := cfgval.AsString(check[checks.CheckKeyOp]); op != "" {
		out = append(out, web.WatchCondition{Field: checks.DataKeyCount, Op: op, Value: cfgval.String(check[checks.CheckKeyValue])})
	}
	if delta, ok := check[checks.CheckKeyDelta].(map[string]any); ok {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyDelta, Op: cfgval.AsString(delta[checks.CheckKeyOp]), Value: cfgval.String(delta[checks.CheckKeyValue])})
	}
	if within := cfgval.String(check[checks.CheckKeyWithin]); within != "" {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyWithin, Value: within})
	}
	return out
}

func processWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if value := cfgval.String(check[checks.CheckKeyFor]); value != "" {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyFor, Op: cfgval.CompareOpGreaterEqual, Value: value})
	}
	if gone, ok := check[checks.CheckKeyGone].(bool); ok && gone {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyGone, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(true)})
	}
	return out
}

func routeWatchConditions(check map[string]any) []web.WatchCondition {
	family := cfgval.AsString(check[checks.CheckKeyFamily])
	if family == "" {
		family = checks.FamilyIPv4
	}
	out := []web.WatchCondition{{Field: checks.DataKeyFamily, Op: cfgval.CompareOpEqual, Value: family}}
	if iface := cfgval.AsString(check[checks.CheckKeyInterface]); iface != "" {
		out = append(out, web.WatchCondition{Field: checks.DataKeyInterface, Op: cfgval.CompareOpEqual, Value: iface})
	}
	return out
}

func firewallWatchConditions(check map[string]any) []web.WatchCondition {
	backend := cfgval.AsString(check[checks.CheckKeyBackend])
	if backend == "" {
		backend = checks.FirewallBackendAuto
	}
	minRules := cfgval.String(check[checks.CheckKeyMinRules])
	if minRules == "" {
		minRules = strconv.FormatUint(watchFirewallDefaultMinRules, watchReadingNumericBase)
	}
	return []web.WatchCondition{
		{Field: checks.DataKeyBackend, Op: cfgval.CompareOpEqual, Value: backend},
		{Field: checks.DataKeyRules, Op: cfgval.CompareOpGreaterEqual, Value: minRules},
	}
}

func sizeWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if path := cfgval.AsString(check[checks.CheckKeyPath]); path != "" {
		out = append(out, web.WatchCondition{Field: checks.DataKeyPath, Value: path})
	}
	if growBy := cfgval.String(check[checks.CheckKeyGrowBy]); growBy != "" {
		out = append(out, web.WatchCondition{Field: watchConditionFieldGrowth, Op: cfgval.CompareOpGreaterEqual, Value: growBy})
	}
	if within := cfgval.String(check[checks.CheckKeyWithin]); within != "" {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyWithin, Value: within})
	}
	if includeHidden, ok := check[checks.CheckKeyIncludeHidden].(bool); ok {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyIncludeHidden, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(includeHidden)})
	}
	return out
}

func watchCommonConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if mounted, ok := check[checks.CheckKeyMounted].(bool); ok {
		out = append(out, web.WatchCondition{Field: checks.DataKeyMounted, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(mounted)})
	}
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
	case checks.CheckTypeEntropy:
		return checks.EntropyPredFields
	case checks.CheckTypeZombies:
		return checks.ZombiePredFields
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
	case checks.CheckTypeRAID:
		return checks.RaidPredFields
	case checks.CheckTypeLVM:
		return checks.LVMPredFields
	case checks.CheckTypeEDAC:
		return checks.EdacPredFields
	case checks.CheckTypeAutofs:
		return []string{checks.CheckKeyCount}
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
	if recursive, ok := check[checks.CheckKeyRecursive].(bool); ok {
		out = append(out, web.WatchCondition{Field: checks.DataKeyRecursive, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(recursive)})
	}
	if includeHidden, ok := check[checks.CheckKeyIncludeHidden].(bool); ok {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyIncludeHidden, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(includeHidden)})
	}
	if size, ok := check[checks.CheckKeySize].(map[string]any); ok {
		if on := cfgval.AsString(size[checks.CheckKeyOn]); on != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeySize, Value: on})
		} else {
			out = append(out, web.WatchCondition{Field: checks.DataKeySize, Op: cfgval.AsString(size[checks.CheckKeyOp]), Value: cfgval.String(size[checks.CheckKeyValue])})
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
	if olderThan := cfgval.String(check[checks.CheckKeyOlderThan]); olderThan != "" {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyOlderThan, Op: cfgval.CompareOpGreater, Value: olderThan})
	}
	return out
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
		if delta, ok := entry[checks.CheckKeyDelta].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, watchMetricSuffixDelta),
				Op:    cfgval.AsString(delta[checks.CheckKeyOp]),
				Value: cfgval.String(delta[checks.CheckKeyValue]),
			})
		}
		if threshold, ok := entry[checks.CheckKeyThreshold].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, watchMetricSuffixThreshold),
				Op:    cfgval.AsString(threshold[checks.CheckKeyOp]),
				Value: cfgval.String(threshold[checks.CheckKeyValue]),
			})
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
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, field),
				Op:    cfgval.AsString(m[checks.CheckKeyOp]),
				Value: cfgval.String(m[checks.CheckKeyValue]),
			})
		}
	}
	return out
}

func watchMetricConditionField(metric, suffix string) string {
	return metric + watchMetricFieldSeparator + suffix
}
