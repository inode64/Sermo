package state

import (
	"testing"
	"time"
)

func TestServiceMetricSummaryAndSeries(t *testing.T) {
	assertMetricSummaryAndSeries(t,
		func(s *Store, base time.Time) {
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
			// A different metric on the same service must not leak into web/cpu.
			if err := s.RecordServiceMetric("web", "memory", 4096, base); err != nil {
				t.Fatal(err)
			}
		},
		func(s *Store, span time.Duration, now time.Time) (MeasurementStat, error) {
			return s.ServiceMetricSummary("web", "cpu", span, now)
		},
		func(s *Store, from, to time.Time) ([]MeasurementPoint, error) {
			return s.ServiceMetricSeries("web", "cpu", from, to)
		},
		func(s *Store, span time.Duration, now time.Time) (MeasurementStat, error) {
			return s.ServiceMetricSummary("db", "cpu", span, now)
		},
		MeasurementStat{Count: 4, Min: 10, Max: 40, Avg: 25},
		MeasurementPoint{N: 3, Avg: 20, Min: 10, Max: 30},
		MeasurementPoint{N: 1, Avg: 40},
		MeasurementStat{Count: 1, Avg: 90},
	)
}
