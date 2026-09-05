package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
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
// the key columns. They are called only from the error branches: record runs for
// every service, check and declared metric on every cycle, so building the target
// eagerly would allocate once per call for a message that is almost never used.
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

func recordMetric(ctx context.Context, exec statementExecutor, m metricSeries, value float64, at time.Time) error {
	if _, err := exec(ctx, metricRecordStmt,
		resMinute, m.scope, m.service, m.check, m.metric, alignBucket(at, resMinute),
		value, value, value,
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
	return summaryFromRow(s.reads().QueryRowContext(s.sqlCtx(), metricSummaryStmt,
		stored.Res, m.scope, m.service, m.check, m.metric, alignBucket(from, stored.Res)))
}

// series returns the series' points in [from, to), oldest first, at the
// resolution that window is stored at. Buckets with no observation are absent
// (gaps), as in SLASeries.
func (s *Store) series(m metricSeries, from, to time.Time) ([]MeasurementPoint, error) {
	stored := s.retention.archiveFor(from, to)
	// As in loadSLASeries the upper bound covers the bucket holding `to`, so the
	// newest still-filling bucket stays in the series.
	rows, err := s.reads().QueryContext(s.sqlCtx(), metricSeriesStmt,
		stored.Res, m.scope, m.service, m.check, m.metric,
		alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res)
	kind, target := m.kind(), m.target()
	description := kind + " series for " + target
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", description, err)
	}
	return measurementPointsFromRows(rows, kind+" series row for "+target, description)
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
func summaryFromRow(row *sql.Row) (MeasurementStat, error) {
	var count sql.NullInt64
	var sum, minV, maxV sql.NullFloat64
	if err := row.Scan(&count, &sum, &minV, &maxV); err != nil {
		return MeasurementStat{}, fmt.Errorf("scan measurement summary: %w", err)
	}
	stat := MeasurementStat{Count: count.Int64}
	if count.Int64 > 0 && sum.Valid {
		stat.Avg = sum.Float64 / float64(count.Int64)
		stat.Min = minV.Float64
		stat.Max = maxV.Float64
	}
	return stat, nil
}

// measurementPointsFromRows scans per-minute aggregate rows shared by every
// metric history table. The callers keep their distinct SQL and error context.
func measurementPointsFromRows(rows *sql.Rows, scanContext, iterateContext string) ([]MeasurementPoint, error) {
	defer func() { _ = rows.Close() }()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minValue, maxValue float64
		if err := rows.Scan(&bucket, &n, &sum, &minValue, &maxValue); err != nil {
			return nil, fmt.Errorf("scan %s: %w", scanContext, err)
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minValue, Max: maxValue})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", iterateContext, err)
	}
	return out, nil
}
