package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

const (
	benchmarkFleetServices    = 30
	benchmarkChecksPerService = 5
)

// benchStore opens a store backed by a temp file (not :memory:) so the
// benchmarks pay the real WAL/synchronous=NORMAL write cost.
func benchStore(b *testing.B) *Store {
	b.Helper()
	s, err := OpenContext(context.Background(), filepath.Join(b.TempDir(), Filename))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

// BenchmarkRecordCycle compares direct writes with one transaction per service
// for a 30-service × 5-check fleet: service SLA + per-check SLA,
// the dominant per-cycle write pattern.
func BenchmarkRecordCycle(b *testing.B) {
	b.Run("direct", func(b *testing.B) {
		s := benchStore(b)
		benchmarkRecordCycle(b, func(service string, at time.Time) error {
			return recordBenchmarkSLACycle(s, service, at)
		})
	})
	b.Run("batch", func(b *testing.B) {
		s := benchStore(b)
		benchmarkRecordCycle(b, func(service string, at time.Time) error {
			return s.WithBatch(context.Background(), func(records Batch) error {
				return recordBenchmarkSLACycle(records, service, at)
			})
		})
	})
}

type benchmarkSLARecorder interface {
	RecordSLA(service string, up bool, at time.Time) error
	RecordCheckSLA(service, check string, up bool, at time.Time) error
}

func benchmarkRecordCycle(b *testing.B, record func(service string, at time.Time) error) {
	b.Helper()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		at := now.Add(time.Duration(i) * 30 * time.Second)
		for svc := range benchmarkFleetServices {
			name := fmt.Sprintf("svc-%02d", svc)
			if err := record(name, at); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func recordBenchmarkSLACycle(records benchmarkSLARecorder, service string, at time.Time) error {
	if err := records.RecordSLA(service, true, at); err != nil {
		return err
	}
	for check := range benchmarkChecksPerService {
		if err := records.RecordCheckSLA(service, fmt.Sprintf("check-%d", check), true, at); err != nil {
			return err
		}
	}
	return nil
}

// seedSLAYear fills one service with a year of per-minute buckets — the
// worst-case row count the rolling-year reads aggregate.
func seedSLAYear(b *testing.B, s *Store, service string, now time.Time) {
	b.Helper()
	start := now.Add(-365 * 24 * time.Hour)
	tx := 0
	for at := start; at.Before(now); at = at.Add(time.Minute) {
		if err := s.RecordSLA(service, true, at); err != nil {
			b.Fatal(err)
		}
		tx++
	}
	b.Logf("seeded %d buckets", tx)
}

// BenchmarkSLAReportYearSeeded measures the five-window (hour..year) rollup a
// CLI `sermoctl sla` run or web panel refresh triggers for one service with a
// full year of history.
func BenchmarkSLAReportYearSeeded(b *testing.B) {
	s := benchStore(b)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedSLAYear(b, s, "web", now)
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.SLAReport("web", now); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSLATimelinesYearSeeded measures the segmented timeline strips the
// web service detail requests (5 windows × segments, grouped aggregation).
func BenchmarkSLATimelinesYearSeeded(b *testing.B) {
	s := benchStore(b)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedSLAYear(b, s, "web", now)
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.SLATimelines("web", now); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRecordDuringYearTimeline measures write latency while a cold
// year-window timeline read holds the store's single connection — the
// contention a detail-view refresh can impose on the daemon's cycle writes.
func BenchmarkRecordDuringYearTimeline(b *testing.B) {
	s := benchStore(b)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedSLAYear(b, s, "web", now)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = s.SLATimelines("web", now)
			}
		}
	}()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := s.RecordSLA("other", true, now.Add(time.Duration(i)*time.Second)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	close(stop)
	<-done
}
