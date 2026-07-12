package state

import (
	"testing"
	"time"
)

func TestRecordSLAAccumulatesPerMinuteBucket(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 7, 10, 0, 30, 0, time.UTC)

	// Three cycles in the same minute: two up, one down -> 2/3 in that bucket.
	mustRecord(t, s, "web", true, base)
	mustRecord(t, s, "web", false, base.Add(20*time.Second))
	mustRecord(t, s, "web", true, base.Add(40*time.Second))

	up, total, err := s.SLA("web", time.Hour, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("SLA: %v", err)
	}
	if up != 2 || total != 3 {
		t.Fatalf("same-minute accumulation: up=%d total=%d, want 2/3", up, total)
	}
}

func TestRecordCheckSLAAccumulatesPerMinuteBucket(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 7, 10, 0, 30, 0, time.UTC)

	if err := s.RecordCheckSLA("web", "http", true, base); err != nil {
		t.Fatalf("RecordCheckSLA ok: %v", err)
	}
	if err := s.RecordCheckSLA("web", "http", false, base.Add(20*time.Second)); err != nil {
		t.Fatalf("RecordCheckSLA fail: %v", err)
	}
	if err := s.RecordCheckSLA("web", "tcp", true, base.Add(40*time.Second)); err != nil {
		t.Fatalf("RecordCheckSLA tcp: %v", err)
	}

	up, total, err := s.CheckSLA("web", "http", time.Hour, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("CheckSLA: %v", err)
	}
	if up != 1 || total != 2 {
		t.Fatalf("http accumulation: up=%d total=%d, want 1/2", up, total)
	}

	up, total, err = s.CheckSLA("web", "tcp", time.Hour, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("CheckSLA tcp: %v", err)
	}
	if up != 1 || total != 1 {
		t.Fatalf("tcp accumulation: up=%d total=%d, want 1/1", up, total)
	}
}

func TestSLAWindowsSumOnlyWithinSpan(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// One down sample 30 minutes ago (inside the hour, outside nothing here),
	// one up sample 2 hours ago (outside the hour window, inside the day window).
	mustRecord(t, s, "web", false, now.Add(-30*time.Minute))
	mustRecord(t, s, "web", true, now.Add(-2*time.Hour))

	hourUp, hourTotal, err := s.SLA("web", time.Hour, now)
	if err != nil {
		t.Fatalf("SLA hour: %v", err)
	}
	if hourUp != 0 || hourTotal != 1 {
		t.Fatalf("hour window: up=%d total=%d, want 0/1 (only the 30-min-ago down sample)", hourUp, hourTotal)
	}

	dayUp, dayTotal, err := s.SLA("web", 24*time.Hour, now)
	if err != nil {
		t.Fatalf("SLA day: %v", err)
	}
	if dayUp != 1 || dayTotal != 2 {
		t.Fatalf("day window: up=%d total=%d, want 1/2 (both samples)", dayUp, dayTotal)
	}
}

func TestSLAReportRatioAndNoData(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// 9 up, 1 down within the last few minutes -> 90% across every window that
	// covers them; "web" never recorded before so all windows see the same data.
	for i := range 9 {
		mustRecord(t, s, "web", true, now.Add(-time.Duration(i)*time.Minute))
	}
	mustRecord(t, s, "web", false, now.Add(-time.Minute))

	report, err := s.SLAReport("web", now)
	if err != nil {
		t.Fatalf("SLAReport: %v", err)
	}
	if len(report) != len(SLAWindows) {
		t.Fatalf("report has %d windows, want %d", len(report), len(SLAWindows))
	}
	for _, v := range report {
		ratio, ok := v.Ratio()
		if !ok {
			t.Fatalf("window %s reported no data, want 90%%", v.Window)
		}
		if ratio < 0.89 || ratio > 0.91 {
			t.Fatalf("window %s ratio = %.4f, want ~0.90", v.Window, ratio)
		}
	}

	// A service with no samples reports no data, not 0%.
	empty, err := s.SLAReport("ghost", now)
	if err != nil {
		t.Fatalf("SLAReport ghost: %v", err)
	}
	for _, v := range empty {
		if _, ok := v.Ratio(); ok {
			t.Fatalf("window %s of an unrecorded service reported data", v.Window)
		}
	}
}

func TestCheckSLAReportAndSeries(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	if err := s.RecordCheckSLA("web", "http", true, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA old ok: %v", err)
	}
	if err := s.RecordCheckSLA("web", "http", false, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA recent fail: %v", err)
	}

	report, err := s.CheckSLAReport("web", "http", now)
	if err != nil {
		t.Fatalf("CheckSLAReport: %v", err)
	}
	if len(report) != len(SLAWindows) {
		t.Fatalf("report has %d windows, want %d", len(report), len(SLAWindows))
	}
	if ratio, ok := report[0].Ratio(); !ok || ratio != 0.5 {
		t.Fatalf("hour ratio = %.2f ok=%v, want 0.50 true", ratio, ok)
	}

	points, err := s.CheckSLASeries("web", "http", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("CheckSLASeries: %v", err)
	}
	if len(points) != 2 || points[1].Up != 0 || points[1].Total != 1 {
		t.Fatalf("points = %+v, want two points ending with down sample", points)
	}
}

func TestPruneSLARemovesOldBuckets(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	mustRecord(t, s, "web", true, now.Add(-400*24*time.Hour)) // old
	mustRecord(t, s, "web", true, now.Add(-1*time.Hour))      // recent
	if err := s.RecordCheckSLA("web", "http", true, now.Add(-400*24*time.Hour)); err != nil {
		t.Fatalf("RecordCheckSLA old: %v", err)
	}
	if err := s.RecordCheckSLA("web", "http", true, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("RecordCheckSLA recent: %v", err)
	}

	removed, err := s.PruneSLA(now.Add(-366 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("PruneSLA: %v", err)
	}
	if removed != 2 {
		t.Fatalf("pruned %d rows, want 2", removed)
	}

	_, total, err := s.SLA("web", 367*24*time.Hour, now)
	if err != nil {
		t.Fatalf("SLA: %v", err)
	}
	if total != 1 {
		t.Fatalf("after prune total=%d, want 1 (recent sample kept)", total)
	}
	_, total, err = s.CheckSLA("web", "http", 367*24*time.Hour, now)
	if err != nil {
		t.Fatalf("CheckSLA: %v", err)
	}
	if total != 1 {
		t.Fatalf("after prune check total=%d, want 1 (recent sample kept)", total)
	}
}

func TestSLASeriesReturnsOrderedBucketsWithGaps(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// Two adjacent monitored minutes, then a gap (service paused / Sermo down: no
	// samples), then another monitored minute. The gap must not appear as a row.
	mustRecord(t, s, "web", true, now.Add(-10*time.Minute))
	mustRecord(t, s, "web", false, now.Add(-9*time.Minute))
	mustRecord(t, s, "web", true, now.Add(-2*time.Minute))

	points, err := s.SLASeries("web", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("SLASeries: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3 (gap minutes excluded, not zero-filled)", len(points))
	}
	// Ordered oldest first, and buckets aligned to the minute.
	for i := 1; i < len(points); i++ {
		if !points[i].Start.After(points[i-1].Start) {
			t.Fatalf("points not strictly ordered: %v then %v", points[i-1].Start, points[i].Start)
		}
	}
	if points[1].Up != 0 || points[1].Total != 1 {
		t.Fatalf("middle point = %+v, want the down sample (up=0 total=1)", points[1])
	}

	// A range before any sample is empty (the excluded/unmonitored period).
	before, err := s.SLASeries("web", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("SLASeries before: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected no points before monitoring began, got %d", len(before))
	}
}

func TestSLATimelinesBucketsSegmentsIntoSubSpans(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// Two samples inside the hour window (12 five-minute segments): a down sample
	// 2 minutes ago lands in the newest segment, an up sample 58 minutes ago lands
	// in the oldest. The minutes between them are gaps (no samples recorded).
	mustRecord(t, s, "web", false, now.Add(-2*time.Minute))
	mustRecord(t, s, "web", true, now.Add(-58*time.Minute))

	tls, err := s.SLATimelines("web", now)
	if err != nil {
		t.Fatalf("SLATimelines: %v", err)
	}
	if len(tls) != len(SLAWindows) {
		t.Fatalf("got %d windows, want %d", len(tls), len(SLAWindows))
	}

	hour := tls[0]
	if hour.Window != "hour" || len(hour.Segments) != 12 {
		t.Fatalf("hour window = %q with %d segments, want \"hour\" with 12", hour.Window, len(hour.Segments))
	}
	// Window totals are the sum of the segments — no separate report query needed.
	if hour.Up != 1 || hour.Total != 2 {
		t.Fatalf("hour totals up=%d total=%d, want 1/2", hour.Up, hour.Total)
	}
	if oldest := hour.Segments[0]; oldest.Up != 1 || oldest.Total != 1 {
		t.Fatalf("oldest segment = %+v, want the up sample (1/1)", oldest)
	}
	if newest := hour.Segments[11]; newest.Up != 0 || newest.Total != 1 {
		t.Fatalf("newest segment = %+v, want the down sample (0/1)", newest)
	}
	if mid := hour.Segments[5]; mid.Total != 0 {
		t.Fatalf("a segment with no samples = %+v, want a gap (total 0)", mid)
	}

	// Per-window segment counts come from SLAWindows.
	for i, w := range SLAWindows {
		if len(tls[i].Segments) != w.Segments {
			t.Fatalf("window %s has %d segments, want %d", w.Name, len(tls[i].Segments), w.Segments)
		}
	}
}

// TestSLATimelinesIncludeCurrentMinuteMatchingSLA pins the window total of the
// timeline to SLA() for the same span: both must count the current partial
// minute, otherwise the strip and the report disagree.
func TestSLATimelinesIncludeCurrentMinuteMatchingSLA(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 30, 30, 0, time.UTC) // mid-minute

	mustRecord(t, s, "web", true, now) // current (partial) minute
	mustRecord(t, s, "web", false, now.Add(-10*time.Minute))

	tls, err := s.SLATimelines("web", now)
	if err != nil {
		t.Fatalf("SLATimelines: %v", err)
	}
	hourWin := SLAWindows[0]
	up, total, err := s.SLA("web", hourWin.Span, now)
	if err != nil {
		t.Fatalf("SLA: %v", err)
	}
	if total != 2 {
		t.Fatalf("SLA hour total=%d, want 2 (current minute must be counted)", total)
	}
	if tls[0].Up != up || tls[0].Total != total {
		t.Fatalf("timeline hour up=%d total=%d disagrees with SLA up=%d total=%d", tls[0].Up, tls[0].Total, up, total)
	}
}

func TestCheckSLATimelinesScopeToCheck(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	if err := s.RecordCheckSLA("web", "http", true, now.Add(-3*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA up: %v", err)
	}
	if err := s.RecordCheckSLA("web", "http", false, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA down: %v", err)
	}
	if err := s.RecordCheckSLA("web", "tcp", true, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA other check: %v", err)
	}

	tls, err := s.CheckSLATimelines("web", "http", now)
	if err != nil {
		t.Fatalf("CheckSLATimelines: %v", err)
	}
	if tls[0].Up != 1 || tls[0].Total != 2 {
		t.Fatalf("http hour totals up=%d total=%d, want 1/2 (tcp excluded)", tls[0].Up, tls[0].Total)
	}

	// A service/check with no samples reports zeroed segments, not an error.
	ghost, err := s.CheckSLATimelines("web", "ghost", now)
	if err != nil {
		t.Fatalf("CheckSLATimelines ghost: %v", err)
	}
	if ghost[0].Total != 0 || len(ghost[0].Segments) != 12 {
		t.Fatalf("ghost hour = %+v, want zero totals with 12 segments", ghost[0])
	}
}

func mustRecord(t *testing.T, s *Store, service string, up bool, at time.Time) {
	t.Helper()
	if err := s.RecordSLA(service, up, at); err != nil {
		t.Fatalf("RecordSLA: %v", err)
	}
}
