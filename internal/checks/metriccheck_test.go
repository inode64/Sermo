package checks

import (
	"context"
	"strings"
	"testing"

	"sermo/internal/metrics"
)

func TestMetricCheckFDsWithoutDenominator(t *testing.T) {
	for _, count := range []float64{0, 7} {
		c := metricCheck{name: "fds", scope: "service", metric: metrics.MetricFds, op: ">", value: "80%",
			source: func(_, _ string) (metrics.Reading, bool) {
				return metrics.Reading{Absolute: count, HasAbsolute: true, Ready: true}, true
			}}
		res := c.Run(t.Context())
		if res.Observation() != ObservationUnavailable || !strings.Contains(res.Message, "no process with a usable open-file limit") {
			t.Fatalf("count=%v: %+v", count, res)
		}
		c.value = "5"
		res = c.Run(t.Context())
		if res.Unavailable || res.OK != (count > 5) {
			t.Fatalf("absolute count must remain usable: %+v", res)
		}
	}
}

func TestMetricCheckRun(t *testing.T) {
	src := func(ready bool, v float64) MetricReader {
		return func(_, _ string) (metrics.Reading, bool) {
			return metrics.Reading{Absolute: v, HasAbsolute: true, Ready: ready}, true
		}
	}
	mk := func(s MetricReader) metricCheck {
		return metricCheck{name: "m", scope: "service", metric: "cpu", op: ">", value: "50", source: s}
	}

	if res := mk(nil).Run(context.Background()); res.OK || !res.Unavailable {
		t.Errorf("nil source must be unavailable without firing: %+v", res)
	}
	miss := mk(func(_, _ string) (metrics.Reading, bool) { return metrics.Reading{}, false })
	if res := miss.Run(context.Background()); res.OK || !res.Unavailable {
		t.Errorf("a missing metric must be unavailable without firing: %+v", res)
	}
	if res := mk(src(false, 90)).Run(context.Background()); res.OK || !res.Unavailable {
		t.Errorf("a not-ready metric must be unavailable without firing: %+v", res)
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
	if res := mk(src(true, 10)).Run(context.Background()); res.OK || res.Unavailable {
		t.Errorf("a ready non-breach must be a valid false observation: %+v", res)
	}
}
