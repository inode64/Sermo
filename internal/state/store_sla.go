package state

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/metrics"
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

	slaSpanDay   = hoursPerDay * time.Hour
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

// SLAUnavailable is the text representation for an SLA window with no observed
// cycles. Missing observations are unknown, not zero availability.
const SLAUnavailable = "n/a"

// Ratio returns the availability fraction in [0,1] and whether the window has any
// observed cycles. With no data (total==0) availability is unknown, not 0%.
func (c SLACounts) Ratio() (float64, bool) {
	if c.Total <= 0 {
		return 0, false
	}
	return float64(c.Up) / float64(c.Total), true
}

// PercentText renders the availability as a percentage, or SLAUnavailable when
// the window has no observations.
func (c SLACounts) PercentText() string {
	return slaPercentText(c.Up, c.Total)
}

// SLAValue is the availability of one service over one rolling window.
type SLAValue struct {
	Window string `json:"window"`
	SLACounts
}

// recordSLABucket writes one observed cycle into the per-minute archive. An empty
// check is the service-level series. down_buckets is recomputed rather than
// accumulated: at this resolution the bucket is the unit it counts, so it is 1 as
// soon as any cycle in the minute failed.
func recordSLABucket(ctx context.Context, exec statementExecutor, service, check string, up bool, at time.Time) error {
	if _, err := exec(ctx, slaRecordStmt,
		resMinute, service, check, alignBucket(at, resMinute), boolInt(up), boolInt(!up),
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

func slaPercentText(up, total int64) string {
	if total <= 0 {
		return SLAUnavailable
	}
	return fmt.Sprintf("%.2f%%", float64(up)/float64(total)*metrics.PercentScale)
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

// sumSLA totals one series over the rolling window ending at now. The archive is
// chosen from the requested span, so a report window and a series for the same
// range resolve to the same stored buckets.
func (s *Store) sumSLA(service, check string, span time.Duration, now time.Time) (SLAValue, error) {
	from := now.Add(-span)
	stored := s.retention.archiveFor(from, now)
	var value SLAValue
	if err := s.reads().QueryRowContext(s.sqlCtx(), slaSumStmt,
		stored.Res, service, check, alignBucket(from, stored.Res),
	).Scan(&value.Up, &value.Total, &value.DownBuckets); err != nil {
		return SLAValue{}, fmt.Errorf("sum %s for %s: %w", slaKind(check), slaTarget(service, check), err)
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
	rows, err := s.reads().QueryContext(s.sqlCtx(), slaSeriesStmt,
		stored.Res, service, check,
		alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res)
	if err != nil {
		return nil, fmt.Errorf("load %s series for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	defer func() { _ = rows.Close() }()

	var out []SLAPoint
	for rows.Next() {
		var bucket int64
		var point SLAPoint
		if err := rows.Scan(&bucket, &point.Up, &point.Total, &point.DownBuckets); err != nil {
			return nil, fmt.Errorf("scan %s series row for %s: %w", slaKind(check), slaTarget(service, check), err)
		}
		point.Start = time.Unix(bucket, 0).UTC()
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s series for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	return out, nil
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
	return reportWindows(func(span time.Duration) (SLAValue, error) {
		return s.sumSLA(service, "", span, now)
	})
}

// reportWindows collects one SLAValue per SLAWindow from the given sum reader;
// the loop shared by the service- and check-level reports.
func reportWindows(sum func(span time.Duration) (SLAValue, error)) ([]SLAValue, error) {
	out := make([]SLAValue, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		value, err := sum(w.Span)
		if err != nil {
			return nil, err
		}
		value.Window = w.Name
		out = append(out, value)
	}
	return out, nil
}
