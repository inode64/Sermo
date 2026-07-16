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

	if err := s.RecordProcessUptime("web", started, first, "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime first: %v", err)
	}
	if err := s.RecordProcessUptime("web", started, last, "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime extend: %v", err)
	}

	spans, err := s.ProcessUptimeSpans("web", started.Add(-time.Minute), last.Add(time.Minute))
	if err != nil {
		t.Fatalf("ProcessUptimeSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
	if got := spans[0]; !got.StartedAt.Equal(started) || !got.ConfirmedAt.Equal(last) || got.Source != "backend-main" {
		t.Fatalf("span = %+v, want start=%s confirmed=%s source=backend-main", got, started, last)
	}
}

func TestProcessUptimeSpansOnlyReturnsIntersectingRanges(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	if err := s.RecordProcessUptime("web", base, base.Add(time.Hour), "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime web: %v", err)
	}
	if err := s.RecordProcessUptime("web", base.Add(2*time.Hour), base.Add(3*time.Hour), "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime later: %v", err)
	}
	if err := s.RecordProcessUptime("db", base, base.Add(3*time.Hour), "backend-main"); err != nil {
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
		source    string
	}{
		{name: "empty service", startedAt: at, confirmed: at, source: "backend-main"},
		{name: "zero start", service: "web", confirmed: at, source: "backend-main"},
		{name: "zero confirmation", service: "web", startedAt: at, source: "backend-main"},
		{name: "reversed", service: "web", startedAt: at, confirmed: at.Add(-time.Second), source: "backend-main"},
		{name: "empty source", service: "web", startedAt: at, confirmed: at},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.RecordProcessUptime(tc.service, tc.startedAt, tc.confirmed, tc.source); err == nil {
				t.Fatal("RecordProcessUptime error = nil, want validation error")
			}
		})
	}
}

func TestPruneProcessUptimeKeepsSpanConfirmedAtCutoff(t *testing.T) {
	s := openTemp(t)
	cutoff := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if err := s.RecordProcessUptime("old", cutoff.Add(-2*time.Hour), cutoff.Add(-time.Second), "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime old: %v", err)
	}
	if err := s.RecordProcessUptime("current", cutoff.Add(-time.Hour), cutoff, "backend-main"); err != nil {
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
	if err := s.RecordProcessUptime("web", now.Add(-30*time.Minute), now, "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime first: %v", err)
	}
	// An overlapping confirmation for the same service must not make the 1-hour
	// coverage exceed the actual 30-minute process lifetime.
	if err := s.RecordProcessUptime("web", now.Add(-20*time.Minute), now.Add(-10*time.Minute), "backend-main"); err != nil {
		t.Fatalf("RecordProcessUptime overlap: %v", err)
	}

	windows, err := s.ProcessUptimeReport("web", now)
	if err != nil {
		t.Fatalf("ProcessUptimeReport: %v", err)
	}
	if len(windows) != len(SLAWindows) {
		t.Fatalf("got %d windows, want %d", len(windows), len(SLAWindows))
	}
	hour := windows[0]
	if !hour.Known || hour.CoveredSeconds != int64((30*time.Minute).Seconds()) || hour.TotalSeconds != int64(time.Hour.Seconds()) {
		t.Fatalf("hour coverage = %+v, want 1800/3600 seconds", hour)
	}
	if len(hour.Segments) != SLAWindows[0].Segments {
		t.Fatalf("hour segments = %d, want %d", len(hour.Segments), SLAWindows[0].Segments)
	}
	if hour.Segments[0] != 0 || hour.Segments[len(hour.Segments)-1] != 1 {
		t.Fatalf("hour segment coverage = %v, want oldest gap and newest full", hour.Segments)
	}
}
