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
)

const (
	summarySecondsPerMinute = 60
	summaryMinutesPerHour   = 60
	summaryHoursPerDay      = 24
	summaryDaysPerWeek      = 7
	summaryDaysPerMonth     = 30
	summarySecondsPerHour   = summaryMinutesPerHour * summarySecondsPerMinute
	summarySecondsPerDay    = summaryHoursPerDay * summarySecondsPerHour
	summarySecondsPerWeek   = summaryDaysPerWeek * summarySecondsPerDay
	summarySecondsPerMonth  = summaryDaysPerMonth * summarySecondsPerDay
	summaryNumberBase       = 10
	summaryNumberPrecision  = 2
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
		return FormatDisplayValue(name, value)
	})
	return result
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
		return summaryMapValue(entry, key)
	}
	if key, ok := strings.CutPrefix(name, "result."); ok {
		return summaryMapValue(data, key)
	}
	if value, ok := data[name]; ok {
		return value, true
	}
	return summaryMapValue(entry, name)
}

func summaryMapValue(values map[string]any, path string) (any, bool) {
	var value any = values
	for key := range strings.SplitSeq(path, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

// FormatDisplayValue renders a check value consistently for summaries, rule
// events and notifications. Numeric values use grouped thousands and at most
// two decimal places; duration and timestamp values retain their type-aware
// presentation.
func FormatDisplayValue(name string, value any) string {
	switch v := value.(type) {
	case time.Duration:
		return formatSummaryDuration(v)
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

func formatSummaryString(name, value string) string {
	if duration, err := time.ParseDuration(value); err == nil && summaryDurationName(name) {
		return formatSummaryDuration(duration)
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

func formatSummaryDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration%time.Second != 0 {
		return duration.String()
	}
	total := int64(duration / time.Second)
	units := []struct {
		seconds int64
		suffix  string
	}{
		{summarySecondsPerMonth, "mo"},
		{summarySecondsPerWeek, "w"},
		{summarySecondsPerDay, "d"},
		{summarySecondsPerHour, "h"},
		{summarySecondsPerMinute, "m"},
		{1, "s"},
	}
	var rendered strings.Builder
	for _, unit := range units {
		if total >= unit.seconds {
			fmt.Fprintf(&rendered, "%d%s", total/unit.seconds, unit.suffix)
			total %= unit.seconds
		}
	}
	return rendered.String()
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

func formatSummaryDecimal(raw string) string {
	negative := strings.HasPrefix(raw, "-")
	raw = strings.TrimPrefix(raw, "-")
	whole, fraction, hasFraction := strings.Cut(raw, ".")
	for index := len(whole) - 3; index > 0; index -= 3 {
		whole = whole[:index] + "." + whole[index:]
	}
	if negative {
		whole = "-" + whole
	}
	if hasFraction {
		return whole + "," + fraction
	}
	return whole
}
