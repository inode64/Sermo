package state

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// MeasurementPoint is one time bucket of a check's measurement series: the sample
// count and the average/minimum/maximum value (milliseconds) in that UTC minute.
type MeasurementPoint struct {
	Start time.Time `json:"start"`
	N     int64     `json:"n"`
	Avg   float64   `json:"avg"`
	Min   float64   `json:"min"`
	Max   float64   `json:"max"`
}

// MeasurementStat summarizes a check's measurements over a window: the sample
// count and the average/minimum/maximum (milliseconds). Count==0 means no data.
type MeasurementStat struct {
	Count int64   `json:"count"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// metricSeries identifies one stored numeric series in metric_archive. The four
// families that used to have a table each — check latency, a check's declared
// metrics, service runtime metrics and the daemon's own — differ only in these
// key columns, so one statement per shape serves all of them.
type metricSeries struct {
	scope   string
	service string
	check   string
	metric  string
}

// checkLatencySeries is the measured latency of one service check.
func checkLatencySeries(service, check string) metricSeries {
	return metricSeries{scope: metricScopeLatency, service: service, check: check}
}

// checkMetricSeries is one of a check's declared numeric metrics.
func checkMetricSeries(service, check, metric string) metricSeries {
	return metricSeries{scope: metricScopeCheckMetric, service: service, check: check, metric: metric}
}

// serviceRuntimeSeries is one of a service process tree's runtime metrics.
func serviceRuntimeSeries(service, metric string) metricSeries {
	return metricSeries{scope: metricScopeService, service: service, metric: metric}
}

// daemonRuntimeSeries is one of sermod's own process metrics.
func daemonRuntimeSeries(metric string) metricSeries {
	return metricSeries{scope: metricScopeDaemon, metric: metric}
}

// kind and target name the series for an error message, derived from the scope and
// the key columns. record calls them only from its error branches: it runs for
// every service, check and declared metric on every cycle, so building the target
// eagerly there would allocate once per call for a message that is almost never
// used.
func (m metricSeries) kind() string {
	switch m.scope {
	case metricScopeLatency:
		return "measurement"
	case metricScopeService:
		return "service metric"
	case metricScopeDaemon:
		return "daemon metric"
	default:
		return "metric"
	}
}

func (m metricSeries) target() string {
	keyParts := []string{m.service, m.check, m.metric}
	parts := make([]string, 0, len(keyParts))
	for _, part := range keyParts {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}

func (r recorder) recordMetric(m metricSeries, value float64, at time.Time) error {
	// A non-finite sample is dropped, not stored: NaN violates the NOT NULL
	// aggregates and rolls back the cycle's whole batch, and +Inf beside -Inf
	// sums to NaN in the rollup, which then stops consolidating and pruning
	// every series behind that bucket.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	bucket := alignBucket(at, resMinute)
	if r.open(bucket) {
		r.pending.addMetric(m, bucket, value)
		return nil
	}
	return writeMetricBucket(r.ctx(), r.exec, m, bucket, metricAggregate{n: 1, sum: value, min: value, max: value})
}

// writeMetricBucket adds an aggregate to the series' stored per-minute bucket.
func writeMetricBucket(ctx context.Context, exec statementExecutor, m metricSeries, bucket int64, agg metricAggregate) error {
	if _, err := exec(ctx, metricRecordStmt,
		resMinute, m.scope, m.service, m.check, m.metric, bucket,
		agg.n, agg.sum, agg.min, agg.max,
	); err != nil {
		return fmt.Errorf("record %s for %s: %w", m.kind(), m.target(), err)
	}
	return nil
}

// summary returns the series' average/min/max and sample count over the rolling
// window ending at now. The average stays weight-correct at every resolution
// because n and sum_v are consolidated together.
func (s *Store) summary(m metricSeries, span time.Duration, now time.Time) (MeasurementStat, error) {
	from := now.Add(-span)
	stored := s.retention.archiveFor(from, now)
	lo, hi := alignBucket(from, stored.Res), alignBucket(now, stored.Res)+stored.Res
	agg, err := summaryFromRow(s.reader.QueryRowContext(s.sqlCtx(), metricSummaryStmt,
		stored.Res, m.scope, m.service, m.check, m.metric, lo, hi))
	if err != nil {
		return MeasurementStat{}, err
	}
	if stored.Res == resMinute {
		for _, pending := range s.pending.metricBuckets(m, lo, hi) {
			agg = agg.plus(pending)
		}
	}
	stat := MeasurementStat{Count: agg.n}
	if agg.n > 0 {
		stat.Avg, stat.Min, stat.Max = agg.sum/float64(agg.n), agg.min, agg.max
	}
	return stat, nil
}

// series returns the series' points in [from, to), oldest first, at the
// resolution that window is stored at. Buckets with no observation are absent
// (gaps), as in SLASeries.
func (s *Store) series(m metricSeries, from, to time.Time) ([]MeasurementPoint, error) {
	stored := s.retention.archiveFor(from, to)
	// As in loadSLASeries the upper bound covers the bucket holding `to`, so the
	// newest still-filling bucket stays in the series.
	rows, err := s.reader.QueryContext(s.sqlCtx(), metricSeriesStmt,
		stored.Res, m.scope, m.service, m.check, m.metric,
		alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res)
	if err != nil {
		return nil, fmt.Errorf("load %s series for %s: %w", m.kind(), m.target(), err)
	}
	defer func() { _ = rows.Close() }()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minValue, maxValue float64
		if err := rows.Scan(&bucket, &n, &sum, &minValue, &maxValue); err != nil {
			return nil, fmt.Errorf("scan %s series row for %s: %w", m.kind(), m.target(), err)
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minValue, Max: maxValue})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s series for %s: %w", m.kind(), m.target(), err)
	}
	if stored.Res == resMinute {
		out = withPendingMetricPoints(out, s.pending.metricBuckets(m, alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res))
	}
	return out, nil
}

// withPendingMetricPoints adds the pending minute buckets to a stored series,
// merging a bucket both hold, and keeps it oldest first.
func withPendingMetricPoints(points []MeasurementPoint, pending map[int64]metricAggregate) []MeasurementPoint {
	if len(pending) == 0 {
		return points
	}
	for i, p := range points {
		if agg, ok := pending[p.Start.Unix()]; ok {
			points[i] = metricPoint(p.Start.Unix(), metricAggregate{n: p.N, sum: p.Avg * float64(p.N), min: p.Min, max: p.Max}.plus(agg))
			delete(pending, p.Start.Unix())
		}
	}
	for bucket, agg := range pending {
		points = append(points, metricPoint(bucket, agg))
	}
	slices.SortFunc(points, func(a, b MeasurementPoint) int { return a.Start.Compare(b.Start) })
	return points
}

func metricPoint(bucket int64, agg metricAggregate) MeasurementPoint {
	return MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: agg.n, Avg: agg.sum / float64(agg.n), Min: agg.min, Max: agg.max}
}

// MeasurementSummary returns the average/min/max and sample count for a check over
// the rolling window ending at now.
func (s *Store) MeasurementSummary(service, check string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(checkLatencySeries(service, check), span, now)
}

// MeasurementSeries returns a check's latency points in [from, to), oldest first.
func (s *Store) MeasurementSeries(service, check string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(checkLatencySeries(service, check), from, to)
}

// MetricSummary returns a named metric's average/min/max and sample count over the
// rolling window ending at now.
func (s *Store) MetricSummary(service, check, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(checkMetricSeries(service, check, metric), span, now)
}

// MetricSeries returns a named metric's points in [from, to), oldest first.
func (s *Store) MetricSeries(service, check, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(checkMetricSeries(service, check, metric), from, to)
}

// DaemonMetricSummary returns a daemon metric's average/min/max and sample count
// over the rolling window ending at now.
func (s *Store) DaemonMetricSummary(metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(daemonRuntimeSeries(metric), span, now)
}

// DaemonMetricSeries returns a daemon metric's points in [from, to), oldest first.
func (s *Store) DaemonMetricSeries(metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(daemonRuntimeSeries(metric), from, to)
}

// ServiceMetricSummary returns a service runtime metric's average/min/max and
// sample count over the rolling window ending at now.
func (s *Store) ServiceMetricSummary(service, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(serviceRuntimeSeries(service, metric), span, now)
}

// ServiceMetricSeries returns a service runtime metric's points in [from, to),
// oldest first.
func (s *Store) ServiceMetricSeries(service, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(serviceRuntimeSeries(service, metric), from, to)
}

// summaryFromRow scans the COALESCE(SUM(n),0), SUM, MIN, MAX aggregate row into a
// MeasurementStat (avg = sum/count, guarded against an empty bucket set).
func summaryFromRow(row *sql.Row) (metricAggregate, error) {
	var count int64
	var sum, minV, maxV sql.NullFloat64
	if err := row.Scan(&count, &sum, &minV, &maxV); err != nil {
		return metricAggregate{}, fmt.Errorf("scan measurement summary: %w", err)
	}
	if count <= 0 || !sum.Valid {
		return metricAggregate{}, nil
	}
	return metricAggregate{n: count, sum: sum.Float64, min: minV.Float64, max: maxV.Float64}, nil
}
