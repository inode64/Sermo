package checks

import (
	"context"
	"fmt"
	"time"

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
}

func (c metricCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.source == nil {
		return c.result(false, "metric source unavailable", start)
	}
	reading, ok := c.source(c.scope, c.metric)
	if !ok {
		return c.result(false, fmt.Sprintf("metric %s/%s unavailable", c.scope, c.metric), start)
	}
	met, err := metrics.Compare(reading, c.op, c.value)
	if err != nil {
		return c.result(false, err.Error(), start)
	}
	if !reading.Ready {
		return c.result(false, fmt.Sprintf("%s/%s not ready", c.scope, c.metric), start)
	}
	res := c.result(met, fmt.Sprintf("%s/%s %s %s = %t", c.scope, c.metric, c.op, c.value, met), start)
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
