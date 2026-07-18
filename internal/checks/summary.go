package checks

import (
	"context"
	"fmt"
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/units"
)

const (
	summaryNumberBase      = 10
	summaryNumberPrecision = 2
	summaryFloatBits       = 64
	summaryByteBase        = 1024
	summaryByteRateBase    = 1000 // sizes are IEC binary (KiB…), rates are SI decimal (KB/s…)
	summaryNumberGroupSize = 3
)

var summaryReference = regexp.MustCompile(`\$\{([^}]+)\}`)

// summaryCheck decorates a built check without changing the checker-specific
// result contract. It makes one configured summary available to every caller of
// a check: services, preflight, watches, events, hooks and the web snapshot.
type summaryCheck struct {
	Check
	template string
	entry    map[string]any
}

func withSummary(check Check, entry map[string]any) Check {
	template, _ := entry[CheckKeySummary].(string)
	if template == "" {
		return check
	}
	return summaryCheck{Check: check, template: template, entry: maps.Clone(entry)}
}

func (c summaryCheck) Run(ctx context.Context) Result {
	return ApplySummary(c.template, c.entry, c.Check.Run(ctx))
}

// ApplySummary replaces a result's message with a configured template. The
// observed value is available as ${value}; any resolved check field is available
// directly (${older_than}) or under ${check.<field>}; and result data is exposed
// as ${result.<field>}. Unknown references remain visible in the message so a
// configuration mistake is not silently hidden.
func ApplySummary(template string, entry map[string]any, result Result) Result {
	if template == "" {
		return result
	}
	result.Message = summaryReference.ReplaceAllStringFunc(template, func(match string) string {
		name := strings.TrimSpace(summaryReference.FindStringSubmatch(match)[1])
		value, ok := summaryValue(name, entry, result.Data)
		if !ok {
			return match
		}
		return FormatDisplayValueWithUnit(name, value, summaryValueUnit(name, result.Data))
	})
	return result
}

func summaryValueUnit(name string, data map[string]any) string {
	switch name {
	case DataKeyValue, DataKeyThreshold, "check." + DataKeyValue,
		"result." + DataKeyValue, "result." + DataKeyThreshold:
		unit, _ := data[DataKeyUnit].(string)
		return unit
	default:
		return ""
	}
}

func summaryValue(name string, entry, data map[string]any) (any, bool) {
	switch name {
	case DataKeyValue:
		if value, ok := data[DataKeyValue]; ok {
			return value, true
		}
		value, ok := data[DataKeyResult]
		return value, ok
	case DataKeyTrigger:
		value, ok := data[DataKeyTrigger]
		return value, ok
	case DataKeyNumberFiles:
		value, ok := data[DataKeyNumberFiles]
		return value, ok
	}
	if key, ok := strings.CutPrefix(name, "check."); ok {
		return jsonPath(entry, key)
	}
	if key, ok := strings.CutPrefix(name, "result."); ok {
		return jsonPath(data, key)
	}
	if value, ok := data[name]; ok {
		return value, true
	}
	return jsonPath(entry, name)
}

// FormatDisplayValue renders a check value consistently for summaries, rule
// events and notifications. Numeric values use grouped thousands and at most
// two decimal places; duration and timestamp values retain their type-aware
// presentation.
func FormatDisplayValue(name string, value any) string {
	switch v := value.(type) {
	case time.Duration:
		return units.HumanizeDuration(v)
	case time.Time:
		return v.UTC().Format("2006-01-02 15:04:05 UTC")
	case string:
		return formatSummaryString(name, v)
	case int:
		return formatSummaryInteger(int64(v))
	case int8:
		return formatSummaryInteger(int64(v))
	case int16:
		return formatSummaryInteger(int64(v))
	case int32:
		return formatSummaryInteger(int64(v))
	case int64:
		return formatSummaryInteger(v)
	case uint:
		return formatSummaryUnsigned(uint64(v))
	case uint8:
		return formatSummaryUnsigned(uint64(v))
	case uint16:
		return formatSummaryUnsigned(uint64(v))
	case uint32:
		return formatSummaryUnsigned(uint64(v))
	case uint64:
		return formatSummaryUnsigned(v)
	case float32:
		return formatSummaryNumber(float64(v))
	case float64:
		return formatSummaryNumber(v)
	case []string:
		return strings.Join(v, ", ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, FormatDisplayValue(name, item))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(value)
	}
}

// FormatDisplayValueWithUnit renders a check value with its canonical unit.
// It keeps summaries, rule events and notifications consistent for percentage,
// byte and byte-rate values.
func FormatDisplayValueWithUnit(name string, value any, unit string) string {
	if number, ok := displayNumber(value); ok {
		switch unit {
		case metrics.MetricUnitBytes:
			return formatSummaryBytes(number)
		case metrics.MetricUnitBytesPerSecond:
			return formatSummaryBytesPerSecond(number)
		}
	}

	rendered := FormatDisplayValue(name, value)
	if rendered == "" || unit == "" || strings.HasSuffix(rendered, unit) {
		return rendered
	}
	if unit == metrics.MetricUnitPercent {
		return rendered + unit
	}
	return rendered + " " + unit
}

func displayNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case string:
		number, err := strconv.ParseFloat(v, summaryFloatBits)
		return number, err == nil
	default:
		return 0, false
	}
}

func formatSummaryBytes(number float64) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "-"
	}
	byteUnits := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	unit := 0
	for number >= summaryByteBase && unit < len(byteUnits)-1 {
		number /= summaryByteBase
		unit++
	}
	return formatSummaryNumber(number) + " " + byteUnits[unit]
}

// formatSummaryBytesPerSecond renders a byte rate in SI decimal units (B/s,
// KB/s, MB/s, GB/s, TB/s). Rates are decimal on purpose while sizes stay IEC
// binary (formatSummaryBytes); the web fmtBytesPerSecond mirrors this exactly.
func formatSummaryBytesPerSecond(number float64) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "-"
	}
	byteRateUnits := []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}
	unit := 0
	for number >= summaryByteRateBase && unit < len(byteRateUnits)-1 {
		number /= summaryByteRateBase
		unit++
	}
	return formatSummaryNumber(number) + " " + byteRateUnits[unit]
}

func formatSummaryString(name, value string) string {
	if duration, err := time.ParseDuration(value); err == nil && summaryDurationName(name) {
		return units.HumanizeDuration(duration)
	}
	if timestamp, err := time.Parse(time.RFC3339, value); err == nil && summaryTimestampName(name) {
		return timestamp.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil && summaryNumericName(name) {
		return formatSummaryNumber(number)
	}
	return value
}

func summaryDurationName(name string) bool {
	return name == DataKeyValue || strings.HasSuffix(name, "age") || strings.HasSuffix(name, "_age") || strings.HasSuffix(name, "_than") || strings.HasSuffix(name, "_duration") || strings.HasSuffix(name, "_timeout") || strings.HasSuffix(name, ".for")
}

func summaryTimestampName(name string) bool {
	return strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_time") || strings.HasSuffix(name, "_date")
}

func summaryNumericName(name string) bool {
	return name == DataKeyValue || name == DataKeyNumberFiles || strings.HasSuffix(name, ".value") || strings.HasSuffix(name, "_count") || strings.HasSuffix(name, "_bytes") || strings.HasSuffix(name, "_seconds")
}

func formatSummaryNumber(number float64) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "-"
	}
	raw := strconv.FormatFloat(number, 'f', summaryNumberPrecision, 64)
	raw = strings.TrimRight(strings.TrimRight(raw, "0"), ".")
	return formatSummaryDecimal(raw)
}

func formatSummaryInteger(number int64) string {
	return formatSummaryDecimal(strconv.FormatInt(number, summaryNumberBase))
}

func formatSummaryUnsigned(number uint64) string {
	return formatSummaryDecimal(strconv.FormatUint(number, summaryNumberBase))
}

// formatSummaryDecimal renders the canonical numeric convention for every
// operator-facing surface (events, notifications, CLI, web meters): comma as
// the thousands separator and dot as the decimal mark — `12,345.68`. One type,
// one formatter: do not hand-format numbers with fmt verbs at call sites; route
// them through FormatDisplayValue/FormatDisplayValueWithUnit so the convention
// cannot drift between surfaces.
func formatSummaryDecimal(raw string) string {
	negative := strings.HasPrefix(raw, "-")
	raw = strings.TrimPrefix(raw, "-")
	whole, fraction, hasFraction := strings.Cut(raw, ".")
	for index := len(whole) - summaryNumberGroupSize; index > 0; index -= summaryNumberGroupSize {
		whole = whole[:index] + "," + whole[index:]
	}
	if negative {
		whole = "-" + whole
	}
	if hasFraction {
		return whole + "." + fraction
	}
	return whole
}
