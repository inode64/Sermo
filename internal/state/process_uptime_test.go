package state

import (
	"testing"
	"time"
)

func TestRecordProcessUptimeExtendsSameProcess(t *testing.T) {
	s := openTemp(t)
	started := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	first := started.Add(time.Hour)
	last := first.Add(10 * time.Minute)

	if err := s.RecordProcessUptime("web", started, first); err != nil {
		t.Fatalf("RecordProcessUptime first: %v", err)
	}
	if err := s.RecordProcessUptime("web", started, last); err != nil {
		t.Fatalf("RecordProcessUptime extend: %v", err)
	}

	spans, err := s.ProcessUptimeSpans("web", started.Add(-time.Minute), last.Add(time.Minute))
	if err != nil {
		t.Fatalf("ProcessUptimeSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
	if got := spans[0]; !got.StartedAt.Equal(started) || !got.ConfirmedAt.Equal(last) {
		t.Fatalf("span = %+v, want start=%s confirmed=%s", got, started, last)
	}
}

func TestProcessUptimeSpansOnlyReturnsIntersectingRanges(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	if err := s.RecordProcessUptime("web", base, base.Add(time.Hour)); err != nil {
		t.Fatalf("RecordProcessUptime web: %v", err)
	}
	if err := s.RecordProcessUptime("web", base.Add(2*time.Hour), base.Add(3*time.Hour)); err != nil {
		t.Fatalf("RecordProcessUptime later: %v", err)
	}
	if err := s.RecordProcessUptime("db", base, base.Add(3*time.Hour)); err != nil {
		t.Fatalf("RecordProcessUptime other service: %v", err)
	}

	spans, err := s.ProcessUptimeSpans("web", base.Add(30*time.Minute), base.Add(2*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatalf("ProcessUptimeSpans: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
}

func TestRecordProcessUptimeRejectsInvalidSpan(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		service   string
		startedAt time.Time
		confirmed time.Time
	}{
		{name: "empty service", startedAt: at, confirmed: at},
		{name: "zero start", service: "web", confirmed: at},
		{name: "zero confirmation", service: "web", startedAt: at},
		{name: "reversed", service: "web", startedAt: at, confirmed: at.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.RecordProcessUptime(tc.service, tc.startedAt, tc.confirmed); err == nil {
				t.Fatal("RecordProcessUptime error = nil, want validation error")
			}
		})
	}
}

func TestPruneProcessUptimeKeepsSpanConfirmedAtCutoff(t *testing.T) {
	s := openTemp(t)
	cutoff := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if err := s.RecordProcessUptime("old", cutoff.Add(-2*time.Hour), cutoff.Add(-time.Second)); err != nil {
		t.Fatalf("RecordProcessUptime old: %v", err)
	}
	if err := s.RecordProcessUptime("current", cutoff.Add(-time.Hour), cutoff); err != nil {
		t.Fatalf("RecordProcessUptime current: %v", err)
	}

	removed, err := s.PruneProcessUptime(cutoff)
	if err != nil {
		t.Fatalf("PruneProcessUptime: %v", err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d rows, want 1", removed)
	}
	spans, err := s.ProcessUptimeSpans("current", cutoff.Add(-2*time.Hour), cutoff.Add(time.Hour))
	if err != nil {
		t.Fatalf("ProcessUptimeSpans current: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("current spans = %+v, want one retained span", spans)
	}
}

func TestProcessUptimeReportShowsWindowCoverageWithoutDoubleCounting(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if err := s.RecordProcessUptime("web", now.Add(-30*time.Minute), now); err != nil {
		t.Fatalf("RecordProcessUptime first: %v", err)
	}
	// An overlapping confirmation for the same service must not make the 1-hour
	// coverage exceed the actual 30-minute process lifetime.
	if err := s.RecordProcessUptime("web", now.Add(-20*time.Minute), now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("RecordProcessUptime overlap: %v", err)
	}

	windows, err := s.ProcessUptimeReport("web", now)
	if err != nil {
		t.Fatalf("ProcessUptimeReport: %v", err)
	}
	if len(windows) != len(SLAWindows) {
		t.Fatalf("got %d windows, want %d", len(windows), len(SLAWindows))
	}
	// The denominator is the knowable period (since the earliest process
	// start), so a fully covered 30-minute lifetime reads 100% of 30 minutes,
	// not 50% of the hour.
	hour := windows[0]
	if !hour.Known || hour.CoveredSeconds != int64((30*time.Minute).Seconds()) || hour.TotalSeconds != int64((30*time.Minute).Seconds()) {
		t.Fatalf("hour coverage = %+v, want 1800/1800 seconds", hour)
	}
	if len(hour.Segments) != SLAWindows[0].Segments {
		t.Fatalf("hour segments = %d, want %d", len(hour.Segments), SLAWindows[0].Segments)
	}
	if hour.Segments[0] != 0 || hour.Segments[len(hour.Segments)-1] != 1 {
		t.Fatalf("hour segment coverage = %v, want oldest gap and newest full", hour.Segments)
	}
}

func TestProcessUptimeReportExcludesTimeBeforeFirstEvidence(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	// One process alive for the last 30 days, continuously confirmed.
	if err := s.RecordProcessUptime("web", now.Add(-30*24*time.Hour), now); err != nil {
		t.Fatalf("RecordProcessUptime: %v", err)
	}
	windows, err := s.ProcessUptimeReport("web", now)
	if err != nil {
		t.Fatalf("ProcessUptimeReport: %v", err)
	}
	for _, w := range windows {
		if !w.Known {
			t.Fatalf("window %s should be known: %+v", w.Window, w)
		}
		// Every window — including year — must read full coverage: the
		// denominator stops at the earliest evidence, never the window span.
		if w.CoveredSeconds != w.TotalSeconds {
			t.Fatalf("window %s coverage = %d/%d, want a fully covered knowable period", w.Window, w.CoveredSeconds, w.TotalSeconds)
		}
	}
	year := windows[len(windows)-1]
	if year.TotalSeconds != int64((30 * 24 * time.Hour).Seconds()) {
		t.Fatalf("year knowable period = %ds, want 30 days", year.TotalSeconds)
	}
}

func TestProcessUptimeReportCountsGapsInsideKnownPeriod(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	// A restart inside the knowable period: first instance covered 40m..30m
	// ago, the replacement has run for the last 10 minutes. The 20-minute hole
	// is broken continuity and must count against the ratio.
	if err := s.RecordProcessUptime("web", now.Add(-40*time.Minute), now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("RecordProcessUptime old instance: %v", err)
	}
	if err := s.RecordProcessUptime("web", now.Add(-10*time.Minute), now); err != nil {
		t.Fatalf("RecordProcessUptime new instance: %v", err)
	}
	windows, err := s.ProcessUptimeReport("web", now)
	if err != nil {
		t.Fatalf("ProcessUptimeReport: %v", err)
	}
	hour := windows[0]
	if hour.TotalSeconds != int64((40 * time.Minute).Seconds()) {
		t.Fatalf("hour knowable period = %ds, want 40 minutes", hour.TotalSeconds)
	}
	if hour.CoveredSeconds != int64((20 * time.Minute).Seconds()) {
		t.Fatalf("hour covered = %ds, want 20 minutes (10m + 10m around the restart hole)", hour.CoveredSeconds)
	}
}

func TestProcessUptimeReportKeepsFullWindowWhenEvidencePredatesIt(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	// A span older than the hour window: the whole window is knowable, so the
	// denominator stays the full span exactly as before.
	if err := s.RecordProcessUptime("web", now.Add(-3*time.Hour), now); err != nil {
		t.Fatalf("RecordProcessUptime: %v", err)
	}
	hour := mustProcessUptimeWindow(t, s, now, 0)
	if hour.TotalSeconds != int64(time.Hour.Seconds()) || hour.CoveredSeconds != hour.TotalSeconds {
		t.Fatalf("hour coverage = %d/%d, want the full 3600/3600", hour.CoveredSeconds, hour.TotalSeconds)
	}
}

func mustProcessUptimeWindow(t *testing.T, s *Store, now time.Time, index int) ProcessUptimeWindow {
	t.Helper()
	windows, err := s.ProcessUptimeReport("web", now)
	if err != nil {
		t.Fatalf("ProcessUptimeReport: %v", err)
	}
	return windows[index]
}
