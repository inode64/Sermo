package state

import (
	"context"
	"fmt"
	"slices"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/units"
)

// SLAWindow names a rolling availability window and its length. The windows are
// rolling (ending "now"), so week/month/year use fixed 7/30/365-day spans rather
// than calendar boundaries.
type SLAWindow struct {
	Name string
	Span time.Duration
}

const (
	slaWindowHour  = "hour"
	slaWindowDay   = "day"
	slaWindowWeek  = "week"
	slaWindowMonth = "month"
	slaWindowYear  = "year"

	slaRollingWeekDays  = 7
	slaRollingMonthDays = 30
	slaRollingYearDays  = 365

	slaSpanDay   = units.HoursPerDay * time.Hour
	slaSpanWeek  = slaRollingWeekDays * slaSpanDay
	slaSpanMonth = slaRollingMonthDays * slaSpanDay
	slaSpanYear  = slaRollingYearDays * slaSpanDay
)

// SLAWindows are the reported rolling windows, shortest first.
var SLAWindows = []SLAWindow{
	{Name: slaWindowHour, Span: time.Hour},
	{Name: slaWindowDay, Span: slaSpanDay},
	{Name: slaWindowWeek, Span: slaSpanWeek},
	{Name: slaWindowMonth, Span: slaSpanMonth},
	{Name: slaWindowYear, Span: slaSpanYear},
}

// SLACounts is the availability evidence shared by a rolling SLA value and one
// stored SLA point. DownBuckets survives consolidation, so a ratio that rounds
// to 100% can still report affected minutes.
type SLACounts struct {
	Up          int64 `json:"up"`
	Total       int64 `json:"total"`
	DownBuckets int64 `json:"down_buckets"`
}

// slaUnavailable is the text representation for an SLA window with no observed
// cycles. Missing observations are unknown, not zero availability.
const slaUnavailable = "n/a"

// Ratio returns the availability fraction in [0,1] and whether the window has any
// observed cycles. With no data (total==0) availability is unknown, not 0%.
func (c SLACounts) Ratio() (float64, bool) {
	if c.Total <= 0 {
		return 0, false
	}
	return float64(c.Up) / float64(c.Total), true
}

// PercentText renders the availability as a percentage, or slaUnavailable when
// the window has no observations.
func (c SLACounts) PercentText() string {
	ratio, ok := c.Ratio()
	if !ok {
		return slaUnavailable
	}
	return fmt.Sprintf("%.2f%%", ratio*metrics.PercentScale)
}

// SLAValue is the availability of one service over one rolling window.
type SLAValue struct {
	Window string `json:"window"`
	SLACounts
}

// recordSLABucket records one observed cycle into the per-minute archive. An
// empty check is the service-level series.
func (r recorder) recordSLABucket(service, check string, up bool, at time.Time) error {
	bucket, agg := alignBucket(at, resMinute), slaCycle(up)
	if r.open(bucket) {
		r.pending.addSLA(service, check, bucket, agg)
		return nil
	}
	return writeSLABucket(r.ctx(), r.exec, service, check, bucket, agg)
}

// writeSLABucket adds an aggregate to the series' stored per-minute bucket.
// down_buckets is recomputed rather than accumulated: at this resolution the
// bucket is the unit it counts, so it is 1 as soon as any cycle in the minute
// failed.
func writeSLABucket(ctx context.Context, exec statementExecutor, service, check string, bucket int64, agg slaAggregate) error {
	if _, err := exec(ctx, slaRecordStmt,
		resMinute, service, check, bucket, agg.up, agg.total, agg.down(),
	); err != nil {
		return fmt.Errorf("record %s for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	return nil
}

// slaKind and slaTarget name one SLA series for an error message. They are called
// only from the error branches: the per-cycle write path runs for every service
// and check on every cycle, so building the target string eagerly would allocate
// once per call for a message that is almost never used.
func slaKind(check string) string {
	if check == "" {
		return "SLA"
	}
	return "check SLA"
}

func slaTarget(service, check string) string {
	if check == "" {
		return service
	}
	return service + "/" + check
}

// SLAPoint is one time bucket of a service's availability series: the up and
// total observed cycles in that bucket, plus how many of its one-minute
// sub-buckets saw a failure. A missing point means the service was not monitored
// then (Sermo down, or the service paused/disabled) — excluded, not counted as
// down. The bucket span is the archive the window resolved to, so a point covers
// one minute on the hour window and one day on the year window.
type SLAPoint struct {
	Start time.Time `json:"start"`
	SLACounts
}

// SLASeries returns a service's availability points in [from, to), oldest first,
// at the resolution that window is stored at. Unmonitored buckets are absent
// (gaps) rather than zero rows, so a caller can render excluded periods
// distinctly from downtime.
func (s *Store) SLASeries(service string, from, to time.Time) ([]SLAPoint, error) {
	return s.loadSLASeries(service, "", from, to)
}

// CheckSLASeries returns one check's availability points in [from, to), oldest
// first. Unobserved buckets are absent.
func (s *Store) CheckSLASeries(service, check string, from, to time.Time) ([]SLAPoint, error) {
	return s.loadSLASeries(service, check, from, to)
}

// sumSLA totals a service's series over the rolling window ending at now. The
// archive is chosen from the requested span, so a report window and a series
// for the same range resolve to the same stored buckets.
func (s *Store) sumSLA(service string, span time.Duration, now time.Time) (SLAValue, error) {
	from := now.Add(-span)
	stored := s.retention.archiveFor(from, now)
	lo, hi := alignBucket(from, stored.Res), alignBucket(now, stored.Res)+stored.Res
	var value SLAValue
	if err := s.reader.QueryRowContext(s.sqlCtx(), slaSumStmt,
		stored.Res, service, "", lo, hi,
	).Scan(&value.Up, &value.Total, &value.DownBuckets); err != nil {
		return SLAValue{}, fmt.Errorf("sum %s for %s: %w", slaKind(""), slaTarget(service, ""), err)
	}
	if stored.Res == resMinute {
		// A pending bucket is normally not stored yet; one a previous process
		// wrote at its stop may count its failed minute twice until it ends.
		for _, agg := range s.pending.slaBuckets(service, "", lo, hi) {
			value.Up += agg.up
			value.Total += agg.total
			value.DownBuckets += agg.down()
		}
	}
	return value, nil
}

func (s *Store) loadSLASeries(service, check string, from, to time.Time) ([]SLAPoint, error) {
	// The resolution is chosen relative to `to`, the caller's reference instant —
	// every caller passes now. A range that both starts and ends in the past is
	// therefore resolved by its span, which may name an archive whose retention has
	// already dropped it; it then legitimately returns no rows.
	stored := s.retention.archiveFor(from, to)
	// The upper bound covers the bucket holding `to` rather than truncating to
	// its start, so the newest (still filling) bucket is not dropped from the
	// series. A bucket start is never in the future, so nothing beyond `to` can
	// appear.
	lo, hi := alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res
	rows, err := s.reader.QueryContext(s.sqlCtx(), slaSeriesStmt, stored.Res, service, check, lo, hi)
	if err != nil {
		return nil, fmt.Errorf("load %s series for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	defer func() { _ = rows.Close() }()

	var pending map[int64]slaAggregate
	if stored.Res == resMinute {
		pending = s.pending.slaBuckets(service, check, lo, hi)
	}
	var out []SLAPoint
	for rows.Next() {
		var bucket int64
		var point SLAPoint
		if err := rows.Scan(&bucket, &point.Up, &point.Total, &point.DownBuckets); err != nil {
			return nil, fmt.Errorf("scan %s series row for %s: %w", slaKind(check), slaTarget(service, check), err)
		}
		point.Start = time.Unix(bucket, 0).UTC()
		if extra, ok := pending[bucket]; ok {
			point = slaPoint(bucket, slaAggregate{up: point.Up, total: point.Total}.plus(extra))
			delete(pending, bucket)
		}
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s series for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	if len(pending) == 0 {
		return out, nil
	}
	for bucket, agg := range pending {
		out = append(out, slaPoint(bucket, agg))
	}
	slices.SortFunc(out, func(a, b SLAPoint) int { return a.Start.Compare(b.Start) })
	return out, nil
}

// slaPoint is a per-minute bucket as a series point.
func slaPoint(bucket int64, agg slaAggregate) SLAPoint {
	return SLAPoint{Start: time.Unix(bucket, 0).UTC(), Up: agg.up, Total: agg.total, DownBuckets: agg.down()}
}

// SeriesResolution is the bucket span a series request for [from, to) resolves to.
// Callers rendering a series report it, so a reader knows whether a row is a minute
// or a day; inferring it back from the returned points cannot work for a series with
// a single point.
func (s *Store) SeriesResolution(from, to time.Time) time.Duration {
	return time.Duration(s.retention.archiveFor(from, to).Res) * time.Second
}

// SLAReport returns a service's availability across every SLAWindow, ordered as
// SLAWindows (hour..year).
func (s *Store) SLAReport(service string, now time.Time) ([]SLAValue, error) {
	out := make([]SLAValue, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		value, err := s.sumSLA(service, w.Span, now)
		if err != nil {
			return nil, err
		}
		value.Window = w.Name
		out = append(out, value)
	}
	return out, nil
}
