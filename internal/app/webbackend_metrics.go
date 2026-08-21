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

// slaRatio returns up/total as an optional ratio: nil when the bucket is unknown
// or nothing was observed (total 0), which renders as a gap rather than as a
// measured zero.
func slaRatio(up, total int64, known bool) *float64 {
	if !known || total <= 0 {
		return nil
	}
	ratio := float64(up) / float64(total)
	return &ratio
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

	// The declared unit, not just the type's static set: a check that publishes a
	// scalar under its own `unit:` is offered as a graph in the payload, so
	// refusing it here would leave a panel that can only report a failure.
	unit, published := checks.DeclaredGraphMetricUnit(checkType, entry.checkUnits[check], metric)
	if !published {
		return web.MetricSeries{}, false
	}
	return b.metricSeries(name, check, metric, unit, since, now), true
}

// WatchMetrics returns one numeric series a host watch's check publishes, in the
// shape the service metrics route serves so the dashboard draws both with the
// same panel. ok is false for an unknown or disabled watch and for a metric its
// check does not publish.
//
// A watch has exactly one check, so its type names the series the way a service's
// check name does, and the series is keyed by WatchMonitorKey — the same
// "watch:<name>" spelling availability uses, so a watch and a service of the same
// name never share a series.
func (b *WebBackend) WatchMetrics(_ context.Context, name, metric string, since time.Duration) (web.MetricSeries, bool) {
	w := b.watches[name]
	if w == nil || w.disabled || metric == "" {
		return web.MetricSeries{}, false
	}
	unit, published := checks.DeclaredGraphMetricUnit(w.checkType, w.checkUnit, metric)
	if !published {
		return web.MetricSeries{}, false
	}
	return b.metricSeries(WatchMonitorKey(name), w.checkType, metric, unit, since, b.webNow()), true
}

// metricSeries reads one named metric series and renders it as the wire shape.
// Services and host watches differ only in the key they are stored under and in
// what names the check, so they share everything from the read down.
func (b *WebBackend) metricSeries(key, check, metric, unit string, since time.Duration, now time.Time) web.MetricSeries {
	out := web.MetricSeries{Check: check, Metric: metric, Since: since.String(), Unit: unit}
	if b.measure == nil {
		return out
	}
	if summary, err := b.measure.MetricSummary(key, check, metric, since, now); err == nil {
		out.Summary = metricSummary(summary)
	}
	if points, err := b.measure.MetricSeries(key, check, metric, now.Add(-since), now); err == nil {
		out.Points = measurementPoints(points)
	}
	return out
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
