package checks

import (
	"context"
	"testing"

	"sermo/internal/metrics"
)

func TestMetricCheckRun(t *testing.T) {
	src := func(ready bool, v float64) MetricReader {
		return func(_, _ string) (metrics.Reading, bool) {
			return metrics.Reading{Absolute: v, HasAbsolute: true, Ready: ready}, true
		}
	}
	mk := func(s MetricReader) metricCheck {
		return metricCheck{base: base{name: "m"}, scope: "service", metric: "cpu", op: ">", value: "50", source: s}
	}

	if mk(nil).Run(context.Background()).OK {
		t.Error("nil source must not fire")
	}
	miss := mk(func(_, _ string) (metrics.Reading, bool) { return metrics.Reading{}, false })
	if miss.Run(context.Background()).OK {
		t.Error("an unavailable metric must not fire")
	}
	if mk(src(false, 90)).Run(context.Background()).OK {
		t.Error("a not-ready metric must not fire even when the threshold would hold")
	}
	if !mk(src(true, 90)).Run(context.Background()).OK {
		t.Error("a ready breach (90 > 50) should fire")
	}
	res := mk(src(true, 90)).Run(context.Background())
	if res.Data[DataKeyType] != CheckTypeMetric ||
		res.Data[DataKeyMetric] != "cpu" ||
		res.Data[DataKeyScope] != "service" ||
		res.Data[DataKeyOp] != ">" ||
		res.Data[DataKeyThreshold] != "50" ||
		res.Data[DataKeyValue] != float64(90) ||
		res.Data[DataKeyUnit] != metrics.MetricUnitNone {
		t.Fatalf("metric result data = %#v", res.Data)
	}
	if mk(src(true, 10)).Run(context.Background()).OK {
		t.Error("a ready non-breach (10 > 50) must not fire")
	}
}
