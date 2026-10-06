package checks

import (
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
)

// DBQueryResourceFields returns the optional per-statement resource predicates.
// CPU values are percentages; memory is the connection/backend's bytes.
func DBQueryResourceFields() []string {
	return []string{metrics.MetricCPU, metrics.MetricCPUThread, metrics.MetricMemory}
}

// HasResourceThresholds distinguishes a resource watch from a duration-only one.
func (cfg DBQueryConfig) HasResourceThresholds() bool { return len(cfg.resources) > 0 }

// QueryMatches evaluates one statement, reusing its existing sample. The
// selector and optional minimum duration gate all resource predicates; any
// matching resource predicate fires. Unknown readings never count as zero.
// ready is false only when a missing reading could change a false result.
func (cfg DBQueryConfig) QueryMatches(q DBQuery) (matched, ready bool) {
	if !cfg.Selector.Matches(q) || q.Elapsed() < cfg.MinDuration {
		return false, true
	}
	if !cfg.HasResourceThresholds() {
		return true, true
	}
	values := q.ResourceValues()
	ready = true
	for _, pred := range cfg.resources {
		value, known := values[pred.field]
		if !known {
			ready = false
		} else if cfgval.CompareFloat(value, pred.op, pred.value) {
			return true, true
		}
	}
	return false, ready
}

// ResourceValues contains only measured resources, for evaluation and summaries.
func (q DBQuery) ResourceValues() map[string]float64 {
	values := make(map[string]float64)
	if q.CPUReady {
		values[metrics.MetricCPU] = q.CPU
		values[metrics.MetricCPUThread] = q.CPUThread
	}
	if q.MemoryReady {
		values[metrics.MetricMemory] = float64(q.MemoryBytes)
	}
	return values
}

// ResourceMatchSummary names the thresholds that this sample meets.
func (cfg DBQueryConfig) ResourceMatchSummary(q DBQuery) string {
	values := q.ResourceValues()
	var parts []string
	for _, pred := range cfg.resources {
		value, known := values[pred.field]
		if known && cfgval.CompareFloat(value, pred.op, pred.value) {
			parts = append(parts, pred.field+" "+dbQueryResourceValue(pred.field, value)+" "+pred.op+" "+dbQueryResourceValue(pred.field, pred.value))
		}
	}
	return strings.Join(parts, "; ")
}

func dbQueryResourceValue(field string, value float64) string {
	if field == metrics.MetricMemory {
		return formatSummaryBytes(value)
	}
	return FormatDisplayValue(field, value) + cfgval.PercentSuffix
}
