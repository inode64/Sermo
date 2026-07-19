package state

import (
	"testing"
	"time"
)

func TestDaemonMetricSummaryAndSeries(t *testing.T) {
	assertMetricSummaryAndSeries(t,
		func(s *Store, base time.Time) {
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
		},
		func(s *Store, span time.Duration, now time.Time) (MeasurementStat, error) {
			return s.DaemonMetricSummary("cpu", span, now)
		},
		func(s *Store, from, to time.Time) ([]MeasurementPoint, error) {
			return s.DaemonMetricSeries("cpu", from, to)
		},
		func(s *Store, span time.Duration, now time.Time) (MeasurementStat, error) {
			return s.DaemonMetricSummary("memory", span, now)
		},
		MeasurementStat{Count: 4, Min: 1, Max: 9, Avg: 4.5},
		MeasurementPoint{N: 3, Avg: 3, Min: 1, Max: 5},
		MeasurementPoint{N: 1, Avg: 9},
		MeasurementStat{Count: 1, Avg: 1024},
	)
}
