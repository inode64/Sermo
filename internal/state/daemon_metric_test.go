package state

import (
	"testing"
	"time"
)

func TestDaemonMetricSummaryAndSeries(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)

	for _, v := range []float64{1, 3, 5} {
		if err := s.RecordDaemonMetric("cpu", v, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordDaemonMetric("cpu", 9, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDaemonMetric("memory", 1024, base); err != nil {
		t.Fatal(err)
	}

	now := base.Add(2 * time.Minute)
	stat, err := s.DaemonMetricSummary("cpu", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != 4 || stat.Min != 1 || stat.Max != 9 || stat.Avg != 4.5 {
		t.Fatalf("cpu summary = %+v", stat)
	}

	points, err := s.DaemonMetricSeries("cpu", base.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2 buckets", len(points))
	}
	if points[0].N != 3 || points[0].Avg != 3 || points[0].Min != 1 || points[0].Max != 5 {
		t.Fatalf("bucket 0 = %+v", points[0])
	}
	if points[1].N != 1 || points[1].Avg != 9 {
		t.Fatalf("bucket 1 = %+v", points[1])
	}

	memory, err := s.DaemonMetricSummary("memory", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if memory.Count != 1 || memory.Avg != 1024 {
		t.Fatalf("memory summary = %+v", memory)
	}
}

func TestPruneDaemonMetrics(t *testing.T) {
	s := openTemp(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RecordDaemonMetric("cpu", 10, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneDaemonMetrics(old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
}
