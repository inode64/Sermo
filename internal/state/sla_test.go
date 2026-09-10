package state

import (
	"context"
	"testing"
	"time"
)

func TestSLAWindowsSumOnlyWithinSpan(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// One down sample 30 minutes ago (inside the hour, outside nothing here),
	// one up sample 2 hours ago (outside the hour window, inside the day window).
	mustRecord(t, s, false, now.Add(-30*time.Minute))
	mustRecord(t, s, true, now.Add(-2*time.Hour))

	hour, err := s.sumSLA(slaTestService, "", time.Hour, now)
	if err != nil {
		t.Fatalf("sum hour: %v", err)
	}
	if hour.Up != 0 || hour.Total != 1 {
		t.Fatalf("hour window: up=%d total=%d, want 0/1 (only the 30-min-ago down sample)", hour.Up, hour.Total)
	}

	// The day window is served by the 5-minute archive, so it sees the samples once
	// they have been consolidated — what the daemon's maintenance pass does.
	mustRollup(t, s, now)
	day, err := s.sumSLA(slaTestService, "", slaSpanDay, now)
	if err != nil {
		t.Fatalf("sum day: %v", err)
	}
	if day.Up != 1 || day.Total != 2 {
		t.Fatalf("day window: up=%d total=%d, want 1/2 (both samples)", day.Up, day.Total)
	}
}

func TestSLAReportRatioAndNoData(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// 9 up, 1 down within the last few minutes -> 90% across every window that
	// covers them; slaTestService never recorded before so all windows see the same data.
	for i := range 9 {
		mustRecord(t, s, true, now.Add(-time.Duration(i)*time.Minute))
	}
	mustRecord(t, s, false, now.Add(-time.Minute))

	// Every window but the hour reads a consolidated archive, so this also pins
	// that the ratio survives all four resolution steps unchanged.
	mustRollup(t, s, now)

	report, err := s.SLAReport(slaTestService, now)
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

func TestSLAPercentText(t *testing.T) {
	tests := []struct {
		name  string
		up    int64
		total int64
		want  string
	}{
		{name: "rounded percentage", up: 2, total: 3, want: "66.67%"},
		{name: "no observations", up: 0, total: 0, want: SLAUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (SLAValue{Up: tt.up, Total: tt.total}).PercentText(); got != tt.want {
				t.Errorf("SLAValue.PercentText() = %q, want %q", got, tt.want)
			}
			point := SLAPoint{Up: tt.up, Total: tt.total}
			if got := point.PercentText(); got != tt.want {
				t.Errorf("SLAPoint.PercentText() = %q, want %q", got, tt.want)
			}
			_, ok := point.Ratio()
			if ok != (tt.total > 0) {
				t.Errorf("SLAPoint.Ratio() data = %v, want %v", ok, tt.total > 0)
			}
		})
	}
}

func TestCheckSLAReportAndSeries(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	if err := s.RecordCheckSLA(slaTestService, "http", true, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA old ok: %v", err)
	}
	if err := s.RecordCheckSLA(slaTestService, "http", false, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA recent fail: %v", err)
	}
	if err := s.RecordCheckSLA(slaTestService, "tcp", true, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("RecordCheckSLA other check: %v", err)
	}

	report, err := s.CheckSLAReport(slaTestService, "http", now)
	if err != nil {
		t.Fatalf("CheckSLAReport: %v", err)
	}
	if len(report) != len(SLAWindows) {
		t.Fatalf("report has %d windows, want %d", len(report), len(SLAWindows))
	}
	if ratio, ok := report[0].Ratio(); !ok || ratio != 0.5 {
		t.Fatalf("hour ratio = %.2f ok=%v, want 0.50 true", ratio, ok)
	}

	points, err := s.CheckSLASeries(slaTestService, "http", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("CheckSLASeries: %v", err)
	}
	if len(points) != 2 || points[1].Up != 0 || points[1].Total != 1 {
		t.Fatalf("points = %+v, want two points ending with down sample", points)
	}
	ghost, err := s.CheckSLASeries(slaTestService, "ghost", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("CheckSLASeries ghost: %v", err)
	}
	if len(ghost) != 0 {
		t.Fatalf("ghost points = %+v, want no observations", ghost)
	}
}

func TestMaintainDropsHistoryPastTheDayRetention(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	mustRecord(t, s, true, now.Add(-400*24*time.Hour)) // past every retention
	mustRecord(t, s, true, now.Add(-1*time.Hour))      // recent
	if err := s.RecordCheckSLA(slaTestService, "http", true, now.Add(-400*24*time.Hour)); err != nil {
		t.Fatalf("RecordCheckSLA old: %v", err)
	}
	if err := s.RecordCheckSLA(slaTestService, "http", true, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("RecordCheckSLA recent: %v", err)
	}

	if _, err := s.Maintain(context.Background(), now); err != nil {
		t.Fatalf("Maintain: %v", err)
	}

	// A window past the day archive's retention resolves to it anyway, so the
	// 400-day-old sample is gone and the recent one survives.
	beyond := DefaultRetention1d + slaSpanDay
	service, err := s.sumSLA(slaTestService, "", beyond, now)
	if err != nil {
		t.Fatalf("sum service SLA: %v", err)
	}
	if service.Total != 1 {
		t.Fatalf("after maintenance total=%d, want 1 (recent sample kept)", service.Total)
	}
	check, err := s.sumSLA(slaTestService, "http", beyond, now)
	if err != nil {
		t.Fatalf("sum check SLA: %v", err)
	}
	if check.Total != 1 {
		t.Fatalf("after prune check total=%d, want 1 (recent sample kept)", check.Total)
	}
}

func TestSLASeriesReturnsOrderedBucketsWithGaps(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// Two adjacent monitored minutes, then a gap (service paused / Sermo down: no
	// samples), then another monitored minute. The gap must not appear as a row.
	mustRecord(t, s, true, now.Add(-10*time.Minute))
	mustRecord(t, s, false, now.Add(-9*time.Minute))
	mustRecord(t, s, true, now.Add(-2*time.Minute))

	points, err := s.SLASeries(slaTestService, now.Add(-time.Hour), now)
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
	before, err := s.SLASeries(slaTestService, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("SLASeries before: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected no points before monitoring began, got %d", len(before))
	}
}

func mustRecord(t *testing.T, s *Store, up bool, at time.Time) {
	t.Helper()
	if err := s.RecordSLA(slaTestService, up, at); err != nil {
		t.Fatalf("RecordSLA: %v", err)
	}
}
