package state

import (
	"testing"
	"time"
)

func TestServiceMetricSummaryAndSeries(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)

	for _, v := range []float64{10, 20, 30} {
		if err := s.RecordServiceMetric("web", "cpu", v, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordServiceMetric("web", "cpu", 40, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordServiceMetric("db", "cpu", 90, base); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordServiceMetric("web", "memory", 4096, base); err != nil {
		t.Fatal(err)
	}

	now := base.Add(2 * time.Minute)
	stat, err := s.ServiceMetricSummary("web", "cpu", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != 4 || stat.Min != 10 || stat.Max != 40 || stat.Avg != 25 {
		t.Fatalf("web cpu summary = %+v", stat)
	}

	points, err := s.ServiceMetricSeries("web", "cpu", base.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2 buckets", len(points))
	}
	if points[0].N != 3 || points[0].Avg != 20 || points[0].Min != 10 || points[0].Max != 30 {
		t.Fatalf("bucket 0 = %+v", points[0])
	}
	if points[1].N != 1 || points[1].Avg != 40 {
		t.Fatalf("bucket 1 = %+v", points[1])
	}

	db, err := s.ServiceMetricSummary("db", "cpu", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if db.Count != 1 || db.Avg != 90 {
		t.Fatalf("db cpu summary = %+v, want isolated service metric", db)
	}
}

func TestPruneServiceMetrics(t *testing.T) {
	s := openTemp(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RecordServiceMetric("web", "cpu", 10, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneServiceMetrics(old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
}
