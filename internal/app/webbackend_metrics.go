package app

import (
	"context"
	"time"

	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/state"
	"sermo/internal/web"
)

// Series returns a service's SLA availability series over the window.
func (b *WebBackend) Series(_ context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	entry := b.entries[name]
	if entry == nil {
		return nil, false
	}
	if b.sla == nil {
		return []web.SeriesPoint{}, true
	}
	now := b.webNow()
	points, err := b.sla.SLASeries(name, now.Add(-since), now)
	if err != nil {
		return []web.SeriesPoint{}, true
	}
	out := make([]web.SeriesPoint, 0, len(points))
	for _, point := range points {
		seriesPoint := web.SeriesPoint{Start: point.Start.Format(time.RFC3339), Up: point.Up, Total: point.Total}
		if point.Total > 0 {
			ratio := float64(point.Up) / float64(point.Total)
			seriesPoint.Ratio = &ratio
		}
		out = append(out, seriesPoint)
	}
	return out, true
}

// Metrics returns a check's measured metric series over the window.
func (b *WebBackend) Metrics(_ context.Context, name, check, metric string, since time.Duration) (web.MetricSeries, bool) {
	entry := b.entries[name]
	if entry == nil || entry.disabled {
		return web.MetricSeries{}, false
	}
	checkType, ok := entry.checkTypes[check]
	if !ok {
		return web.MetricSeries{}, false
	}
	now := b.webNow()

	if metric == "" {
		if !measuredCheckTypes[checkType] {
			return web.MetricSeries{}, false
		}
		out := web.MetricSeries{Check: check, Since: since.String(), Unit: metrics.MetricUnitMilliseconds}
		if b.measure == nil {
			return out, true
		}
		if summary, err := b.measure.MeasurementSummary(name, check, since, now); err == nil {
			out.Summary = metricSummary(summary)
		}
		points, err := b.measure.MeasurementSeries(name, check, now.Add(-since), now)
		if err == nil {
			out.Points = measurementPoints(points)
		}
		return out, true
	}

	unit := checks.GraphMetricUnit(checkType, metric)
	if unit == "" {
		return web.MetricSeries{}, false
	}
	out := web.MetricSeries{Check: check, Metric: metric, Since: since.String(), Unit: unit}
	if b.measure == nil {
		return out, true
	}
	if summary, err := b.measure.MetricSummary(name, check, metric, since, now); err == nil {
		out.Summary = metricSummary(summary)
	}
	if points, err := b.measure.MetricSeries(name, check, metric, now.Add(-since), now); err == nil {
		out.Points = measurementPoints(points)
	}
	return out, true
}

func measurementPoints(points []state.MeasurementPoint) []web.MetricPoint {
	out := make([]web.MetricPoint, 0, len(points))
	for _, point := range points {
		out = append(out, web.MetricPoint{
			Start: point.Start.Format(time.RFC3339),
			N:     point.N,
			Avg:   point.Avg,
			Min:   point.Min,
			Max:   point.Max,
		})
	}
	return out
}
