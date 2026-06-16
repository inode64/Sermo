// Package metrics samples service and system metrics for metric checks and
// conditions (section 12). Rate metrics (cpu, total_cpu) are deltas between two
// samples, so the Collector is stateful and long-lived, owned by the daemon and
// sampled once per cycle.
package metrics

import (
	"fmt"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
)

// Reading is one metric's sampled value. A metric may expose an absolute form,
// a percentage form, or both (section 14). Rate metrics are not Ready on the
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

// Compare evaluates a metric reading against a threshold and operator
// (section 14). A not-ready reading is false (a rate metric must never fire on a
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

	var actual float64
	if isPercent {
		if !r.HasPercent {
			return false, fmt.Errorf("percentage threshold %q on a metric with no percentage form", threshold)
		}
		actual = r.Percent
	} else {
		if !r.HasAbsolute {
			return false, fmt.Errorf("absolute threshold %q on a metric with no absolute form", threshold)
		}
		actual = r.Absolute
	}
	return applyOp(actual, op, value)
}

func parseThreshold(s string) (value float64, isPercent bool, err error) {
	s = strings.TrimSpace(s)
	if raw, ok := strings.CutSuffix(s, "%"); ok {
		v, perr := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if perr != nil {
			return 0, false, fmt.Errorf("invalid percentage %q", s)
		}
		return v, true, nil
	}
	v, perr := strconv.ParseFloat(s, 64)
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
