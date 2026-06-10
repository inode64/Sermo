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

func TestPruneMetrics(t *testing.T) {
	s := openTemp(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RecordMetric("disks", "speed", "read", 100, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneMetrics(old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
}
