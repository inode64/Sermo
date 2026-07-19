package state

import (
	"testing"
	"time"
)

func TestMetricSummaryAndSeries(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)

	for _, v := range []float64{100, 200, 300} {
		if err := s.RecordMetric("disks", "speed", "read", v, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordMetric("disks", "speed", "read", 150, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A different metric on the same check is a separate series.
	if err := s.RecordMetric("disks", "speed", "cached", 9000, base); err != nil {
		t.Fatal(err)
	}

	now := base.Add(2 * time.Minute)
	stat, err := s.MetricSummary("disks", "speed", "read", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != 4 || stat.Min != 100 || stat.Max != 300 {
		t.Fatalf("read summary = %+v, want count 4 min 100 max 300", stat)
	}
	if want := (100.0 + 200 + 300 + 150) / 4; stat.Avg != want {
		t.Fatalf("read avg = %v, want %v", stat.Avg, want)
	}

	// `cached` is isolated from `read`.
	cached, err := s.MetricSummary("disks", "speed", "cached", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Count != 1 || cached.Avg != 9000 {
		t.Fatalf("cached summary = %+v, want count 1 avg 9000", cached)
	}

	points, err := s.MetricSeries("disks", "speed", "read", base.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("read points = %d, want 2 buckets", len(points))
	}
	if points[0].N != 3 || points[0].Avg != 200 || points[0].Max != 300 {
		t.Fatalf("bucket 0 = %+v", points[0])
	}
}

// assertMetricSummaryAndSeries drives the summarize + series + isolation shape
// shared by the daemon and service metric tests: a main series with three samples
// at base and one a minute later, plus a second isolated series. setup records the
// rows; summary/series/isolated query the family under test.
func assertMetricSummaryAndSeries(t *testing.T,
	setup func(s *Store, base time.Time),
	summary func(s *Store, span time.Duration, now time.Time) (MeasurementStat, error),
	series func(s *Store, from, to time.Time) ([]MeasurementPoint, error),
	isolated func(s *Store, span time.Duration, now time.Time) (MeasurementStat, error),
	wantStat MeasurementStat, wantB0, wantB1 MeasurementPoint, wantIsolated MeasurementStat) {
	t.Helper()
	s := openTemp(t)
	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	setup(s, base)

	now := base.Add(2 * time.Minute)
	stat, err := summary(s, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != wantStat.Count || stat.Min != wantStat.Min || stat.Max != wantStat.Max || stat.Avg != wantStat.Avg {
		t.Fatalf("summary = %+v, want %+v", stat, wantStat)
	}

	points, err := series(s, base.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2 buckets", len(points))
	}
	if points[0].N != wantB0.N || points[0].Avg != wantB0.Avg || points[0].Min != wantB0.Min || points[0].Max != wantB0.Max {
		t.Fatalf("bucket 0 = %+v, want %+v", points[0], wantB0)
	}
	if points[1].N != wantB1.N || points[1].Avg != wantB1.Avg {
		t.Fatalf("bucket 1 = %+v, want N=%d Avg=%v", points[1], wantB1.N, wantB1.Avg)
	}

	iso, err := isolated(s, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if iso.Count != wantIsolated.Count || iso.Avg != wantIsolated.Avg {
		t.Fatalf("isolated summary = %+v, want count %d avg %v", iso, wantIsolated.Count, wantIsolated.Avg)
	}
}
