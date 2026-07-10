// Package metrics samples service and system metrics for metric checks and
// conditions. Rate metrics (cpu, total_cpu) are deltas between two
// samples, so the Collector is stateful and long-lived, owned by the daemon and
// sampled once per cycle.
package metrics

import (
	"fmt"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
)

const (
	// MetricUnitPercent is the canonical UI/API unit for percentage metrics.
	MetricUnitPercent = "%"
	// MetricUnitBytes is the canonical UI/API unit for byte gauges.
	MetricUnitBytes = "bytes"
	// MetricUnitBytesPerSecond is the canonical UI/API unit for byte-rate metrics.
	MetricUnitBytesPerSecond = "B/s"
	// MetricUnitBits is the canonical UI/API unit for bit counts.
	MetricUnitBits = "bits"
	// MetricUnitMilliseconds is the canonical UI/API unit for latency/duration metrics.
	MetricUnitMilliseconds = "ms"

	// MetricUnitMegabytesPerSecond is the canonical UI/API unit for disk throughput.
	MetricUnitMegabytesPerSecond = "MB/s"
	// MetricUnitMegabitsPerSecond is the canonical UI/API unit for link speed.
	MetricUnitMegabitsPerSecond = "Mbps"
	// MetricUnitCelsius is the canonical UI/API unit for temperatures.
	MetricUnitCelsius = "°C"
	// MetricUnitRPM is the canonical UI/API unit for fan speed.
	MetricUnitRPM = "RPM"
	// MetricUnitVolt is the canonical UI/API unit for voltage readings.
	MetricUnitVolt = "V"
	// MetricUnitHours is the canonical UI/API unit for hour counters.
	MetricUnitHours = "h"
	// MetricUnitUsers is the canonical UI/API unit for user counts.
	MetricUnitUsers = "users"
	// MetricUnitProcesses is the canonical UI/API unit for process counts.
	MetricUnitProcesses = "processes"
	// MetricUnitNone marks unitless graph metrics.
	MetricUnitNone = ""

	// PercentScale converts ratios into percentage points.
	PercentScale = 100.0

	metricFloatBits = 64
)

// Reading is one metric's sampled value. A metric may expose an absolute form,
// a percentage form, or both. Rate metrics are not Ready on the
// first cycle, before a delta can be computed.
type Reading struct {
	Absolute    float64
	Percent     float64
	HasAbsolute bool
	HasPercent  bool
	Ready       bool
	// Total is the capacity behind a usage metric (memory/swap bytes), so a
	// consumer can derive free space; HasTotal reports whether it applies.
	Total    float64
	HasTotal bool
}

// Snapshot holds one scope's metrics for a cycle, keyed by metric name.
type Snapshot map[string]Reading

// Compare evaluates a metric reading against a threshold and operator.
// A not-ready reading is false (a rate metric must never fire on a
// value the collector could not compute yet). A "%" threshold compares against
// the percentage form, a bare number against the absolute form; using a form the
// metric does not expose is an error.
func Compare(r Reading, op, threshold string) (bool, error) {
	if !r.Ready {
		return false, nil
	}
	value, isPercent, err := parseThreshold(threshold)
	if err != nil {
		return false, err
	}

	actual, err := metricValue(r, isPercent, threshold)
	if err != nil {
		return false, err
	}
	return applyOp(actual, op, value)
}

// ReadingValueForThreshold returns the same numeric form Compare uses for a
// threshold: percentage thresholds read Percent, bare thresholds read Absolute.
// The bool is false only when the reading is not ready yet.
func ReadingValueForThreshold(r Reading, threshold string) (float64, string, bool, error) {
	if !r.Ready {
		return 0, MetricUnitNone, false, nil
	}
	_, isPercent, err := parseThreshold(threshold)
	if err != nil {
		return 0, MetricUnitNone, false, err
	}
	actual, err := metricValue(r, isPercent, threshold)
	if err != nil {
		return 0, MetricUnitNone, false, err
	}
	if isPercent {
		return actual, MetricUnitPercent, true, nil
	}
	return actual, MetricUnitNone, true, nil
}

func metricValue(r Reading, isPercent bool, threshold string) (float64, error) {
	if isPercent {
		if !r.HasPercent {
			return 0, fmt.Errorf("percentage threshold %q on a metric with no percentage form", threshold)
		}
		return r.Percent, nil
	}
	if !r.HasAbsolute {
		return 0, fmt.Errorf("absolute threshold %q on a metric with no absolute form", threshold)
	}
	return r.Absolute, nil
}

func parseThreshold(s string) (value float64, isPercent bool, err error) {
	s = strings.TrimSpace(s)
	if raw, ok := strings.CutSuffix(s, MetricUnitPercent); ok {
		v, perr := strconv.ParseFloat(strings.TrimSpace(raw), metricFloatBits)
		if perr != nil {
			return 0, false, fmt.Errorf("invalid percentage %q", s)
		}
		return v, true, nil
	}
	v, perr := strconv.ParseFloat(s, metricFloatBits)
	if perr != nil {
		return 0, false, fmt.Errorf("invalid number %q", s)
	}
	return v, false, nil
}

func applyOp(actual float64, op string, threshold float64) (bool, error) {
	if !cfgval.IsCompareOp(op) {
		return false, fmt.Errorf("unsupported metric operator %q", op)
	}
	return cfgval.CompareFloat(actual, op, threshold), nil
}
