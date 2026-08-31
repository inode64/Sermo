package state

// This file holds the archive resolutions and the SQL that reads and writes
// them. Sermo keeps one archive per web UI window instead of per-minute history
// for a year: the charts draw a fixed number of columns — 120 for the metric
// charts, 90 for the SLA strip — so a bucket finer than window/columns would be
// stored but never displayed. Both archive tables carry `res` (the bucket span
// in seconds) as the first primary-key column, so one table holds every
// resolution: reads always know the resolution they want, and pruning one
// resolution stays inside that key prefix.

import (
	"time"
)

// SLA archive resolutions preserve a useful investigation granularity for each
// report span without retaining per-minute history for the whole year.
const (
	slaHourArchiveSpan  = 5 * time.Minute
	slaDayArchiveSpan   = time.Hour
	slaWeekArchiveSpan  = 6 * time.Hour
	slaMonthArchiveSpan = 24 * time.Hour
)

// The stored bucket spans, in seconds. Each coarser archive advances to the
// useful resolution of the next shorter SLA report span. resMinute is the
// collection floor: a monitoring cycle is recorded into its UTC minute, so
// nothing finer exists to store. TestArchiveLadderIsConsolidatable pins that
// every rung consolidates exactly into the next one.
const (
	resMinute   int64 = secondsPerMinute
	res5Minutes int64 = int64(slaHourArchiveSpan / time.Second)
	resHour     int64 = int64(slaDayArchiveSpan / time.Second)
	res6Hours   int64 = int64(slaWeekArchiveSpan / time.Second)
	resDay      int64 = int64(slaMonthArchiveSpan / time.Second)
)

// archiveCount is the length of the resolution ladder.
const archiveCount = 5

// Default retention per archive. Each window is covered with a 25% margin so a
// request for exactly `since=window` does not land on a just-pruned edge, and
// the result is rounded to a whole number of hours or days.
const (
	// DefaultRetention1m keeps per-minute samples long enough to investigate
	// a fresh incident at full resolution. It is also the cascade's slack: the
	// consolidation job must not fall behind by more than this, or per-minute
	// rows would be pruned before the 5-minute archive has read them. The prune
	// safety floor makes that a delay rather than data loss.
	DefaultRetention1m = 3 * time.Hour
	// DefaultRetention5m covers the 24-hour window.
	DefaultRetention5m = 30 * time.Hour
	// DefaultRetention1h covers the 7-day window.
	DefaultRetention1h = 9 * slaSpanDay
	// DefaultRetention6h covers the 30-day window.
	DefaultRetention6h = 38 * slaSpanDay
	// DefaultRetention1d covers the rolling-year window the SLA report needs.
	DefaultRetention1d = historyRetentionDays * slaSpanDay
	// DefaultRetentionEvents bounds the operator-visible event feed. It is the
	// only history that still records the exact timestamp of an old incident,
	// which the coarse archives deliberately no longer localize.
	DefaultRetentionEvents = 30 * slaSpanDay
	// DefaultRollupInterval is the consolidation and prune cadence: the span of the
	// second-finest archive, so one pass per bucket keeps every coarser archive at
	// most one interval behind the live samples. Derived from the ladder rather than
	// restated, so the two cannot drift.
	DefaultRollupInterval = time.Duration(res5Minutes) * time.Second
)

// archive is one stored resolution: the bucket span in seconds, and how long
// buckets at that span are kept.
type archive struct {
	Res       int64
	Retention time.Duration
}

// Retention is the history window kept per stored resolution. A non-positive
// field falls back to that archive's default.
type Retention struct {
	Minute      time.Duration
	FiveMinutes time.Duration
	Hour        time.Duration
	SixHours    time.Duration
	Day         time.Duration
	Events      time.Duration
}

// DefaultRetention returns the built-in resolution ladder.
func DefaultRetention() Retention {
	return Retention{
		Minute:      DefaultRetention1m,
		FiveMinutes: DefaultRetention5m,
		Hour:        DefaultRetention1h,
		SixHours:    DefaultRetention6h,
		Day:         DefaultRetention1d,
		Events:      DefaultRetentionEvents,
	}
}

// normalized replaces every non-positive window with its default, so a
// zero-value Retention behaves like DefaultRetention.
func (r Retention) normalized() Retention {
	fallback := DefaultRetention()
	return Retention{
		Minute:      orDefaultWindow(r.Minute, fallback.Minute),
		FiveMinutes: orDefaultWindow(r.FiveMinutes, fallback.FiveMinutes),
		Hour:        orDefaultWindow(r.Hour, fallback.Hour),
		SixHours:    orDefaultWindow(r.SixHours, fallback.SixHours),
		Day:         orDefaultWindow(r.Day, fallback.Day),
		Events:      orDefaultWindow(r.Events, fallback.Events),
	}
}

// orDefaultWindow keeps a positive retention window, else falls back.
func orDefaultWindow(window, fallback time.Duration) time.Duration {
	if window <= 0 {
		return fallback
	}
	return window
}

// archives returns the resolution ladder, finest first. The array is returned by
// value so read paths pick a resolution without allocating.
func (r Retention) archives() [archiveCount]archive {
	windows := r.normalized()
	return [archiveCount]archive{
		{Res: resMinute, Retention: windows.Minute},
		{Res: res5Minutes, Retention: windows.FiveMinutes},
		{Res: resHour, Retention: windows.Hour},
		{Res: res6Hours, Retention: windows.SixHours},
		{Res: resDay, Retention: windows.Day},
	}
}

// MaxWindow is the longest history any archive still holds, and so the furthest
// back a request can reach. It is the maximum over the ladder rather than the daily
// rung's window: a rung added later, or a retention raised past the coarsest one,
// must not silently leave the cap too low.
func (r Retention) MaxWindow() time.Duration {
	var longest time.Duration
	for _, a := range r.archives() {
		longest = max(longest, a.Retention)
	}
	return longest
}

// archiveFor picks the resolution a request for [from, now] can be answered at:
// the finest archive whose retention still reaches back to from. A range older
// than every archive falls back to the coarsest one, which either answers it or
// legitimately returns no rows.
//
// Every read of one logical window must go through this, so a windowed total and
// its per-segment breakdown are aggregated from the same rows and keep agreeing.
func (r Retention) archiveFor(from, now time.Time) archive {
	ladder := r.archives()
	age := now.Sub(from)
	for _, a := range ladder {
		if age <= a.Retention {
			return a
		}
	}
	return ladder[archiveCount-1]
}

// alignBucket truncates t to the start of its bucket at resolution res. The
// floor is explicit because Go integer division truncates toward zero, which
// would round pre-epoch instants the wrong way.
func alignBucket(t time.Time, res int64) int64 {
	secs := t.UTC().Unix()
	if secs < 0 {
		secs -= res - 1
	}
	return secs / res * res
}

// Archive table names. They are compile-time literals, never operator input.
const (
	stateTableSLAArchive    = "sla_archive"
	stateTableMetricArchive = "metric_archive"
)

// Metric scopes name the four series families within metric_archive. They are
// deliberately disjoint rather than sharing a "check" scope split by
// metric name: a check may declare a metric literally called "latency"
// (checks.IcmpMetricLatency), which would otherwise collide with the measured
// check latency and double-count both into one bucket.
//
// These are on-disk schema values owned by this package. Do not conflate them with
// the identically spelled checks.MetricScopeService (a metric check's `scope:`
// selector): renaming that must not touch these, or stored rows would be
// orphaned.
const (
	metricScopeLatency     = "latency" // a check's measured latency; metric is ''
	metricScopeCheckMetric = "metric"  // a check's declared numeric metric
	metricScopeService     = "service" // a service process tree's runtime metric
	metricScopeDaemon      = "daemon"  // sermod's own process metric
)

// SLA archive statements. Every series is keyed by (service, check_name); the
// service-level series uses an empty check_name, so one statement serves both.
const (
	slaRecordStmt = `INSERT INTO sla_archive
			(res, service, check_name, bucket, up_count, total_count, down_buckets)
		 VALUES (?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT(res, service, check_name, bucket) DO UPDATE SET
		   up_count     = up_count + excluded.up_count,
		   total_count  = total_count + excluded.total_count,
		   down_buckets = CASE
		                    WHEN (total_count + excluded.total_count)
		                       > (up_count + excluded.up_count)
		                    THEN 1 ELSE 0
		                  END;`

	slaSumStmt = `SELECT COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0),
			     COALESCE(SUM(down_buckets), 0)
		  FROM sla_archive
		 WHERE res = ? AND service = ? AND check_name = ? AND bucket >= ?;`

	slaSeriesStmt = `SELECT bucket, up_count, total_count, down_buckets
		   FROM sla_archive
		  WHERE res = ? AND service = ? AND check_name = ?
		    AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`
)

// Metric archive statements. One statement per shape covers check latency, a
// check's declared metrics, service runtime metrics and the daemon's own.
const (
	metricRecordStmt = `INSERT INTO metric_archive
			(res, scope, service, check_name, metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(res, scope, service, check_name, metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`

	metricSummaryStmt = `SELECT COALESCE(SUM(n), 0), SUM(sum_v), MIN(min_v), MAX(max_v)
		  FROM metric_archive
		 WHERE res = ? AND scope = ? AND service = ? AND check_name = ? AND metric = ?
		   AND bucket >= ?;`

	metricSeriesStmt = `SELECT bucket, n, sum_v, min_v, max_v
		   FROM metric_archive
		  WHERE res = ? AND scope = ? AND service = ? AND check_name = ? AND metric = ?
		    AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`
)
