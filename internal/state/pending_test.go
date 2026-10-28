package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// storedMinute reads the stored per-minute latency and service SLA rows of one
// bucket, bypassing the pending overlay.
func storedMinute(t *testing.T, s *Store, bucket int64) (n, total int64) {
	t.Helper()
	if err := s.reader.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(n), 0) FROM metric_archive WHERE res = ? AND scope = ? AND service = ? AND check_name = ? AND bucket = ?`,
		resMinute, metricScopeLatency, slaTestService, "http", bucket).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := s.reader.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(total_count), 0) FROM sla_archive WHERE res = ? AND service = ? AND check_name = '' AND bucket = ?`,
		resMinute, slaTestService, bucket).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return n, total
}

func recordCycle(t *testing.T, s *Store, ctx context.Context, at time.Time) error {
	t.Helper()
	return s.WithBatch(ctx, func(b Batch) error {
		if err := b.RecordMeasurement(slaTestService, "http", 10, at); err != nil {
			return err
		}
		return b.RecordSLA(slaTestService, true, at)
	})
}

// The open minute is accumulated in memory and written once when it has ended,
// while this process's reads already include it.
func TestOpenMinuteIsWrittenOnceItEnds(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 7, 12, 0, 10, 0, time.UTC)
	minute := alignBucket(start, resMinute)
	clock := start
	s.now = func() time.Time { return clock }

	for _, at := range []time.Time{start, start.Add(30 * time.Second)} {
		clock = at
		if err := recordCycle(t, s, context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}
	if n, total := storedMinute(t, s, minute); n != 0 || total != 0 {
		t.Fatalf("open minute stored n=%d total=%d, want nothing written yet", n, total)
	}
	points, err := s.MeasurementSeries(slaTestService, "http", start.Add(-time.Minute), clock)
	if err != nil || len(points) != 1 || points[0].N != 2 {
		t.Fatalf("series = %+v, %v; want the open minute's two samples", points, err)
	}
	sla, err := s.sumSLA(slaTestService, time.Hour, clock)
	if err != nil || sla.Total != 2 || sla.Up != 2 {
		t.Fatalf("SLA = %+v, %v; want the open minute's two cycles", sla, err)
	}

	clock = start.Add(time.Minute)
	if err := recordCycle(t, s, context.Background(), clock); err != nil {
		t.Fatal(err)
	}
	if n, total := storedMinute(t, s, minute); n != 2 || total != 2 {
		t.Fatalf("ended minute stored n=%d total=%d, want both cycles in one row", n, total)
	}
	if n, _ := storedMinute(t, s, minute+resMinute); n != 0 {
		t.Fatalf("new open minute stored n=%d, want it still pending", n)
	}
}

// Merging a committed batch into a minute with nothing pending yet keeps the
// samples' own extremes, in the overlay and in the row written when it ends.
func TestOpenMinuteKeepsSampleExtremes(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 7, 12, 0, 10, 0, time.UTC)
	clock := start
	s.now = func() time.Time { return clock }
	record := func(value float64, at time.Time) {
		t.Helper()
		clock = at
		if err := s.WithBatch(context.Background(), func(b Batch) error {
			return b.RecordMeasurement(slaTestService, "http", value, at)
		}); err != nil {
			t.Fatal(err)
		}
	}

	record(10, start)
	record(30, start.Add(30*time.Second))
	want := MeasurementPoint{Start: time.Unix(alignBucket(start, resMinute), 0).UTC(), N: 2, Avg: 20, Min: 10, Max: 30}
	if points, err := s.MeasurementSeries(slaTestService, "http", start.Add(-time.Minute), clock); err != nil || len(points) != 1 || points[0] != want {
		t.Fatalf("open minute series = %+v, %v; want %+v", points, err, want)
	}
	record(50, start.Add(time.Minute))
	if points, err := s.MeasurementSeries(slaTestService, "http", start.Add(-time.Minute), clock); err != nil || len(points) != 2 || points[0] != want {
		t.Fatalf("stored minute series = %+v, %v; want %+v first", points, err, want)
	}
}

// A sample for a minute that has already ended cannot accumulate further and is
// written at once.
func TestEndedMinuteSampleIsWrittenAtOnce(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 6, 7, 12, 0, 10, 0, time.UTC)
	s.now = func() time.Time { return at.Add(5 * time.Minute) }
	if err := recordCycle(t, s, context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if n, total := storedMinute(t, s, alignBucket(at, resMinute)); n != 1 || total != 1 {
		t.Fatalf("stored n=%d total=%d, want the late sample written", n, total)
	}
}

// A batch that fails records none of its samples, as when every sample was a
// write in its transaction.
func TestFailedBatchRecordsNoOpenMinuteSample(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 6, 7, 12, 0, 10, 0, time.UTC)
	s.now = func() time.Time { return at }
	err := s.WithBatch(context.Background(), func(b Batch) error {
		if err := b.RecordMeasurement(slaTestService, "http", 10, at); err != nil {
			return err
		}
		return errors.New("cycle failed")
	})
	if err == nil {
		t.Fatal("WithBatch error = nil, want the callback's")
	}
	if points, _ := s.MeasurementSeries(slaTestService, "http", at.Add(-time.Minute), at); len(points) != 0 {
		t.Fatalf("series = %+v, want no sample from the failed batch", points)
	}
}

// Ended minutes a failed write took stay pending for the next one.
func TestFailedFlushKeepsEndedMinutesPending(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 7, 12, 0, 10, 0, time.UTC)
	clock := start
	s.now = func() time.Time { return clock }
	if err := recordCycle(t, s, context.Background(), start); err != nil {
		t.Fatal(err)
	}

	clock = start.Add(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	err := s.WithBatch(ctx, func(Batch) error {
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("flush with a cancelled context succeeded")
	}
	if n, _ := storedMinute(t, s, alignBucket(start, resMinute)); n != 0 {
		t.Fatalf("stored n=%d after a failed flush, want nothing", n)
	}
	if err := recordCycle(t, s, context.Background(), clock); err != nil {
		t.Fatal(err)
	}
	if n, total := storedMinute(t, s, alignBucket(start, resMinute)); n != 1 || total != 1 {
		t.Fatalf("stored n=%d total=%d, want the kept minute written by the next batch", n, total)
	}
}

// Consolidation reads the ended minutes still in memory, and FlushPending writes
// the open one.
func TestRollupAndCloseWritePendingMinutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	s, err := OpenContextWith(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 7, 12, 0, 10, 0, time.UTC)
	clock := start
	s.now = func() time.Time { return clock }
	if err := recordCycle(t, s, context.Background(), start); err != nil {
		t.Fatal(err)
	}

	clock = start.Add(time.Minute)
	mustRollup(t, s, clock)
	if _, total, _, found := archiveRow(t, s, res5Minutes, alignBucket(start, res5Minutes)); !found || total != 1 {
		t.Fatalf("5-minute bucket total=%d found=%v, want the ended minute consolidated", total, found)
	}

	if err := recordCycle(t, s, context.Background(), clock); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushPending(context.Background()); err != nil {
		t.Fatalf("FlushPending: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s, err = OpenContextWith(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if n, total := storedMinute(t, s, alignBucket(clock, resMinute)); n != 1 || total != 1 {
		t.Fatalf("open minute after the flush n=%d total=%d, want it written", n, total)
	}
}
