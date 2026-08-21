package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/state"
	"sermo/internal/web"
)

// Series returns a service's SLA availability series over the window, or one of
// its checks' when check is non-empty. Both scopes share this one path so the
// service and check timelines cannot drift apart in how they report gaps.
func (b *WebBackend) Series(_ context.Context, name, check string, since time.Duration) ([]web.SeriesPoint, bool) {
	entry := b.entries[name]
	if entry == nil {
		return nil, false
	}
	if check != "" {
		if _, ok := entry.checkTypes[check]; !ok {
			return nil, false
		}
		// A verdictless check asserts no availability, so it has none to serve.
		// Withholding the series here rather than only hiding it in the dashboard
		// also drops whatever it accumulated before the mode was declared, which
		// would otherwise keep implying uptime the check never claimed.
		if checks.VerdictlessMode(entry.checkReports[check]) {
			return []web.SeriesPoint{}, true
		}
	}
	return b.availabilitySeries(name, check, since), true
}

// WatchSeries returns a host watch's per-minute availability history over since,
// in the same shape the service series uses so the dashboard renders both
// through one path. ok is false for an unknown watch and for one whose check
// asserts no availability — a condition watch has no uptime to serve, and
// withholding it here rather than only hiding it keeps the API from implying a
// figure the check never claimed.
func (b *WebBackend) WatchSeries(_ context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	w := b.watches[name]
	if w == nil || w.disabled || !watchRecordsAvailability(w) {
		return nil, false
	}
	return b.availabilitySeries(WatchMonitorKey(name), "", since), true
}

// availabilitySeries reads one availability series and renders it as the wire
// shape. Services, their checks and host watches differ only in the key they are
// stored under, so they share everything from the read down — which is what
// keeps them reporting gaps and ratios identically.
func (b *WebBackend) availabilitySeries(key, check string, since time.Duration) []web.SeriesPoint {
	if b.sla == nil {
		return []web.SeriesPoint{}
	}
	now := b.webNow()
	points, err := b.slaSeries(key, check, now.Add(-since), now)
	if err != nil {
		return []web.SeriesPoint{}
	}
	out := make([]web.SeriesPoint, 0, len(points))
	for _, point := range points {
		out = append(out, web.SeriesPoint{
			Start:       point.Start.Format(time.RFC3339),
			Up:          point.Up,
			Total:       point.Total,
			DownBuckets: point.DownBuckets,
			Ratio:       slaRatio(point.Up, point.Total, true),
		})
	}
	return out
}

// slaSeries reads the service-level series, or one check's when check is set.
// This is the only place the two scopes diverge; everything above and below it
// is shared, which is what keeps them reporting gaps identically.
func (b *WebBackend) slaSeries(name, check string, from, to time.Time) ([]state.SLAPoint, error) {
	if check == "" {
		points, err := b.sla.SLASeries(name, from, to)
		if err != nil {
			return nil, fmt.Errorf("read sla series for %s: %w", name, err)
		}
		return points, nil
	}
	points, err := b.sla.CheckSLASeries(name, check, from, to)
	if err != nil {
		return nil, fmt.Errorf("read sla series for %s check %s: %w", name, check, err)
	}
	return points, nil
}

// Metrics returns a check's measured metric series over the window.
func (b *WebBackend) Metrics(_ context.Context, name, check, metric string, since time.Duration) (web.MetricSeries, bool) {
	entry, ok := b.enabledEntry(name)
	if !ok {
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
