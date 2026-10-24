package state

import (
	"math"
	"testing"
	"time"
)

func TestMeasurementSummaryAndSeries(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)

	// three samples in minute 0, two in minute 1
	for _, v := range []float64{10, 20, 30} {
		if err := s.RecordMeasurement("web", "http", v, base); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []float64{5, 45} {
		if err := s.RecordMeasurement("web", "http", v, base.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	now := base.Add(2 * time.Minute)
	stat, err := s.MeasurementSummary("web", "http", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != 5 {
		t.Fatalf("count = %d, want 5", stat.Count)
	}
	if stat.Min != 5 || stat.Max != 45 {
		t.Fatalf("min/max = %v/%v, want 5/45", stat.Min, stat.Max)
	}
	if want := (10.0 + 20 + 30 + 5 + 45) / 5; stat.Avg != want {
		t.Fatalf("avg = %v, want %v", stat.Avg, want)
	}

	points, err := s.MeasurementSeries("web", "http", base.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2 buckets", len(points))
	}
	if points[0].N != 3 || points[0].Avg != 20 || points[0].Min != 10 || points[0].Max != 30 {
		t.Fatalf("bucket 0 = %+v", points[0])
	}
	if points[1].N != 2 || points[1].Avg != 25 {
		t.Fatalf("bucket 1 = %+v", points[1])
	}
}

// TestMeasuredLatencyIsSeparateFromADeclaredLatencyMetric pins that the two
// series stay distinct. A check may declare a metric literally called "latency"
// (checks.IcmpMetricLatency), so keying the measured latency by metric name inside
// a shared check scope would silently accumulate both into one bucket.
func TestMeasuredLatencyIsSeparateFromADeclaredLatencyMetric(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)

	if err := s.RecordMeasurement("web", "http", 10, at); err != nil {
		t.Fatalf("RecordMeasurement: %v", err)
	}
	if err := s.RecordMetric("web", "http", "latency", 500, at); err != nil {
		t.Fatalf("RecordMetric: %v", err)
	}

	measured, err := s.MeasurementSummary("web", "http", time.Hour, now)
	if err != nil {
		t.Fatalf("MeasurementSummary: %v", err)
	}
	declared, err := s.MetricSummary("web", "http", "latency", time.Hour, now)
	if err != nil {
		t.Fatalf("MetricSummary: %v", err)
	}
	if measured.Count != 1 || measured.Avg != 10 {
		t.Fatalf("measured latency = %+v, want one sample of 10", measured)
	}
	if declared.Count != 1 || declared.Avg != 500 {
		t.Fatalf("declared latency metric = %+v, want one sample of 500", declared)
	}
}

func TestMeasurementSummaryNoData(t *testing.T) {
	s := openTemp(t)
	stat, err := s.MeasurementSummary("web", "http", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != 0 {
		t.Fatalf("expected no data, got %+v", stat)
	}
}

func TestMetricSummariesExcludeFutureBuckets(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 30, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		record  func(*Store, float64, time.Time) error
		summary func(*Store, time.Duration, time.Time) (MeasurementStat, error)
	}{
		{name: "latency", record: func(s *Store, v float64, at time.Time) error { return s.RecordMeasurement("web", "http", v, at) },
			summary: func(s *Store, span time.Duration, at time.Time) (MeasurementStat, error) {
				return s.MeasurementSummary("web", "http", span, at)
			}},
		{name: "check", record: func(s *Store, v float64, at time.Time) error { return s.RecordMetric("web", "http", "value", v, at) },
			summary: func(s *Store, span time.Duration, at time.Time) (MeasurementStat, error) {
				return s.MetricSummary("web", "http", "value", span, at)
			}},
		{name: "service", record: func(s *Store, v float64, at time.Time) error { return s.RecordServiceMetric("web", "cpu", v, at) },
			summary: func(s *Store, span time.Duration, at time.Time) (MeasurementStat, error) {
				return s.ServiceMetricSummary("web", "cpu", span, at)
			}},
		{name: "daemon", record: func(s *Store, v float64, at time.Time) error { return s.RecordDaemonMetric("cpu", v, at) },
			summary: func(s *Store, span time.Duration, at time.Time) (MeasurementStat, error) {
				return s.DaemonMetricSummary("cpu", span, at)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			for _, sample := range []struct {
				at    time.Time
				value float64
			}{
				{at: now.Add(-time.Minute), value: 10},
				{at: now, value: 20},
				{at: now.Add(48 * time.Hour), value: 1000},
			} {
				if err := tc.record(s, sample.value, sample.at); err != nil {
					t.Fatal(err)
				}
			}
			mustRollup(t, s, now.Add(48*time.Hour))
			for _, window := range SLAWindows {
				got, err := tc.summary(s, window.Span, now)
				want := MeasurementStat{Count: 2, Avg: 15, Min: 10, Max: 20}
				if err != nil || got != want {
					t.Errorf("%s summary = %+v, %v; want %+v", window.Name, got, err, want)
				}
			}
		})
	}
}

// TestRecordMetricDropsNonFiniteValues pins that NaN and ±Inf never reach the
// archive. NaN broke the NOT NULL aggregates and rolled back the whole cycle's
// batch; +Inf and -Inf in one 5-minute bucket summed to NaN and froze every
// rollup and prune behind that bucket.
func TestRecordMetricDropsNonFiniteValues(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	for i, v := range []float64{7, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := s.RecordMetric("web", "cmd", "value", v, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("record %v: %v", v, err)
		}
	}
	stat, err := s.MetricSummary("web", "cmd", "value", time.Hour, base.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stat.Count != 1 || stat.Avg != 7 {
		t.Fatalf("summary = %+v, want only the finite sample", stat)
	}
}
