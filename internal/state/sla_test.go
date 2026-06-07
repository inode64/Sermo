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
	for i := 0; i < 9; i++ {
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

func TestPruneSLARemovesOldBuckets(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	mustRecord(t, s, "web", true, now.Add(-400*24*time.Hour)) // old
	mustRecord(t, s, "web", true, now.Add(-1*time.Hour))      // recent

	removed, err := s.PruneSLA(now.Add(-366 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("PruneSLA: %v", err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d rows, want 1", removed)
	}

	_, total, err := s.SLA("web", 367*24*time.Hour, now)
	if err != nil {
		t.Fatalf("SLA: %v", err)
	}
	if total != 1 {
		t.Fatalf("after prune total=%d, want 1 (recent sample kept)", total)
	}
}

func mustRecord(t *testing.T, s *Store, service string, up bool, at time.Time) {
	t.Helper()
	if err := s.RecordSLA(service, up, at); err != nil {
		t.Fatalf("RecordSLA: %v", err)
	}
}
