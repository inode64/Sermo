package checks

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
)

// metricCheck reads a sampled metric and compares it to a threshold. Its OK is
// the comparison result (the threshold being met), so
// `active: {check: ...}` is true when the threshold is breached.
type metricCheck struct {
	base
	scope  string
	metric string
	op     string
	value  string
	source MetricReader
	grades grades[metricTier]
}

// metricTier is one `levels:` tier of a metric check: a stricter op +
// value on the same reading.
type metricTier struct {
	op, value string
}

// metricGrades parses the kept tiers of a metric check.
func metricGrades(specs []levelSpec) grades[metricTier] {
	return parseGrades(specs, func(tier map[string]any) (metricTier, bool) {
		return metricTier{op: cfgval.AsString(tier[CheckKeyOp]), value: cfgval.String(tier[CheckKeyValue])}, true
	})
}

// grade raises a breach to the highest tier the same reading also breaches.
func (c metricCheck) grade(res Result, reading metrics.Reading) Result {
	return raiseSeverity(res, c.grades.highest(func(t metricTier) bool {
		met, err := metrics.Compare(reading, t.op, t.value)
		return err == nil && met
	}))
}

func (c metricCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.source == nil {
		return c.unavailableResult("metric source unavailable", start)
	}
	reading, ok := c.source(c.scope, c.metric)
	if !ok {
		return c.unavailableResult(fmt.Sprintf("metric %s/%s unavailable", c.scope, c.metric), start)
	}
	// FD counts remain meaningful for an empty process tree or a process whose
	// limit cannot be read. Its percentage does not: there is no denominator. With
	// nothing open, as in an idle or stopped service, nothing can run out either.
	if _, percent, err := metrics.ParseThreshold(c.value); err == nil && percent && c.metric == metrics.MetricFds && !reading.HasPercent {
		if reading.Absolute == 0 {
			return c.result(false, fmt.Sprintf("%s/%s: no open descriptors", c.scope, c.metric), start)
		}
		return c.unavailableResult("file descriptor percentage unavailable: no process with a usable open-file limit", start)
	}
	met, err := metrics.Compare(reading, c.op, c.value)
	if err != nil {
		return c.unavailableResult(err.Error(), start)
	}
	if !reading.Ready {
		return c.unavailableResult(fmt.Sprintf("%s/%s not ready", c.scope, c.metric), start)
	}
	res := c.grade(c.result(met, fmt.Sprintf("%s/%s %s %s = %t", c.scope, c.metric, c.op, c.value, met), start), reading)
	res.Data = map[string]any{
		DataKeyType:      CheckTypeMetric,
		DataKeyScope:     c.scope,
		DataKeyMetric:    c.metric,
		DataKeyOp:        c.op,
		DataKeyThreshold: c.value,
	}
	if value, unit, ok, err := metrics.ReadingValueForThreshold(reading, c.value); err == nil && ok {
		res.Data[DataKeyValue] = value
		res.Data[DataKeyUnit] = unit
	}
	return res
}
