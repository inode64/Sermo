package state

import (
	"context"
	"testing"
	"time"
)

// mustRollup consolidates the archives so windows served by a coarser resolution
// see the samples just recorded, which is what the daemon's maintenance pass does
// on its rollup interval.
func mustRollup(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	if _, err := s.Rollup(context.Background(), now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
}

// slaTestService is the service every SLA test in this package records under, so
// mustRecord and archiveRow cannot read and write different series.
const slaTestService = "web"

// archiveRow reads one stored service-level bucket directly, so a test can assert
// what consolidation actually wrote rather than what a reader chose to show. The
// service-level series is keyed by an empty check name.
func archiveRow(t *testing.T, s *Store, res, bucket int64) (up, total, down int64, found bool) {
	t.Helper()
	rows, err := s.reads().QueryContext(context.Background(),
		`SELECT up_count, total_count, down_buckets FROM sla_archive
		  WHERE res = ? AND service = ? AND check_name = ? AND bucket = ?;`,
		res, slaTestService, "", bucket)
	if err != nil {
		t.Fatalf("read %ds bucket: %v", res, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			t.Fatalf("read %ds bucket: %v", res, err)
		}
		return 0, 0, 0, false
	}
	if err := rows.Scan(&up, &total, &down); err != nil {
		t.Fatalf("scan %ds bucket: %v", res, err)
	}
	return up, total, down, true
}

// TestSingleFailureSurvivesEveryResolution is the test the whole ladder exists
// for. One failed cycle inside one minute must still be visible in the day
// archive, four consolidation steps later: its ratio is diluted to almost nothing
// by a day's worth of cycles, but down_buckets stays at 1 so the renderer can
// still colour that day as affected.
func TestSingleFailureSurvivesEveryResolution(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-30 * time.Minute)

	// One minute with two cycles, one of them failed.
	mustRecord(t, s, true, at)
	mustRecord(t, s, false, at.Add(20*time.Second))
	// A full healthy hour around it, so the coarse buckets are dominated by
	// successes and the failure has to survive on down_buckets alone.
	for i := 1; i <= 60; i++ {
		mustRecord(t, s, true, at.Add(time.Duration(i)*time.Minute))
	}

	mustRollup(t, s, now)

	for _, res := range []int64{resMinute, res5Minutes, resHour, res6Hours, resDay} {
		bucket := alignBucket(at, res)
		up, total, down, found := archiveRow(t, s, res, bucket)
		if !found {
			t.Fatalf("%ds archive has no bucket for the failure", res)
		}
		if down != 1 {
			t.Fatalf("%ds bucket down_buckets=%d, want 1 (the one affected minute)", res, down)
		}
		if total-up != 1 {
			t.Fatalf("%ds bucket up=%d total=%d, want exactly one failed cycle", res, up, total)
		}
	}
}

// TestConsolidationCountsEveryAffectedMinute pins that three separate bad minutes
// do not collapse into one incident once they share a coarse bucket.
func TestConsolidationCountsEveryAffectedMinute(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)

	for i := range 3 {
		minute := at.Add(time.Duration(i) * time.Minute)
		mustRecord(t, s, true, minute)
		mustRecord(t, s, false, minute.Add(20*time.Second))
	}

	mustRollup(t, s, now)

	_, _, down, found := archiveRow(t, s, resDay, alignBucket(at, resDay))
	if !found {
		t.Fatal("day archive has no bucket")
	}
	if down != 3 {
		t.Fatalf("day bucket down_buckets=%d, want 3 (one per affected minute)", down)
	}
}

// TestConsolidationIsExact pins that a coarse bucket reports exactly what its
// per-minute buckets do: same counters, and a weight-correct average rather than
// an average of averages.
func TestConsolidationIsExact(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)

	// Deliberately uneven sample counts per minute: one sample in the first minute
	// and three in the second, so an unweighted mean would differ from the true one.
	if err := s.RecordMeasurement(slaTestService, "http", 100, at); err != nil {
		t.Fatalf("RecordMeasurement: %v", err)
	}
	for _, v := range []float64{10, 20, 30} {
		if err := s.RecordMeasurement(slaTestService, "http", v, at.Add(time.Minute)); err != nil {
			t.Fatalf("RecordMeasurement: %v", err)
		}
	}

	fine, err := s.MeasurementSummary(slaTestService, "http", 2*time.Hour, at.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("MeasurementSummary before rollup: %v", err)
	}
	mustRollup(t, s, now)

	// A day-wide window reads the day archive; a 2-hour one reads the hour archive.
	coarse, err := s.MeasurementSummary(slaTestService, "http", 300*24*time.Hour, now)
	if err != nil {
		t.Fatalf("MeasurementSummary after rollup: %v", err)
	}
	if coarse.Count != 4 {
		t.Fatalf("day archive count=%d, want 4", coarse.Count)
	}
	wantAvg := (100.0 + 10 + 20 + 30) / 4
	if coarse.Avg != wantAvg {
		t.Fatalf("day archive avg=%v, want %v (weighted by sample count, not a mean of means)", coarse.Avg, wantAvg)
	}
	if coarse.Min != 10 || coarse.Max != 100 {
		t.Fatalf("day archive min/max=%v/%v, want 10/100 (extremes carried through)", coarse.Min, coarse.Max)
	}
	// The finest resolution already agreed; consolidation must not have changed it.
	if fine.Count != coarse.Count || fine.Avg != coarse.Avg || fine.Min != coarse.Min || fine.Max != coarse.Max {
		t.Fatalf("per-minute summary %+v disagrees with consolidated %+v", fine, coarse)
	}
}

// TestRollupIsIdempotent pins that re-running consolidation replaces rather than
// accumulates, which is what lets every pass refresh the newest partial bucket.
func TestRollupIsIdempotent(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-30 * time.Minute)

	mustRecord(t, s, true, at)
	mustRecord(t, s, false, at.Add(time.Minute))

	mustRollup(t, s, now)
	first, firstTotal, firstDown, _ := archiveRow(t, s, resDay, alignBucket(at, resDay))
	for range 3 {
		mustRollup(t, s, now)
	}
	up, total, down, _ := archiveRow(t, s, resDay, alignBucket(at, resDay))
	if up != first || total != firstTotal || down != firstDown {
		t.Fatalf("after repeated rollups up/total/down = %d/%d/%d, want %d/%d/%d",
			up, total, down, first, firstTotal, firstDown)
	}
}

// TestRollupCascadesFromTheCoarserSource pins that each archive is derived from the
// one below it rather than from the per-minute samples, so a daemon that was down
// long enough for those to be pruned still fills the day archive.
func TestRollupCascadesFromTheCoarserSource(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	recorded := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	mustRecord(t, s, true, recorded)
	mustRecord(t, s, false, recorded.Add(time.Minute))
	mustRollup(t, s, recorded.Add(2*time.Minute))

	// Drop the per-minute rows the first pass consolidated, as retention would.
	if _, err := s.pruneArchive(ctx, archiveTables[0], resMinute, recorded.Add(time.Hour).Unix()); err != nil {
		t.Fatalf("prune per-minute archive: %v", err)
	}
	if _, _, _, found := archiveRow(t, s, resMinute, alignBucket(recorded, resMinute)); found {
		t.Fatal("per-minute bucket still present after prune")
	}

	// A later pass must still be able to rebuild the coarse archives.
	mustRollup(t, s, recorded.Add(2*time.Hour))
	up, total, down, found := archiveRow(t, s, resDay, alignBucket(recorded, resDay))
	if !found {
		t.Fatal("day archive lost its bucket once the per-minute rows were gone")
	}
	if up != 1 || total != 2 || down != 1 {
		t.Fatalf("day bucket up/total/down = %d/%d/%d, want 1/2/1", up, total, down)
	}
}

// TestRollupConsolidatesBothArchivesOverTheSameRange pins that the watermark is
// read once per resolution and applied to both tables. Advancing it after the
// first table made the second start from the new watermark, so metric history was
// only ever consolidated for the last few minutes and every window past the
// per-minute retention came back empty.
func TestRollupConsolidatesBothArchivesOverTheSameRange(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-30 * slaSpanDay) // far older than any coarse refresh window

	mustRecord(t, s, true, at)
	if err := s.RecordServiceMetric(slaTestService, "cpu", 42, at); err != nil {
		t.Fatalf("RecordServiceMetric: %v", err)
	}

	mustRollup(t, s, now)

	if _, total, _, found := archiveRow(t, s, resDay, alignBucket(at, resDay)); !found || total != 1 {
		t.Fatalf("sla day bucket found=%v total=%d, want the consolidated sample", found, total)
	}
	// The metric archive is consolidated second; it must cover the same range.
	stat, err := s.ServiceMetricSummary(slaTestService, "cpu", slaSpanYear, now)
	if err != nil {
		t.Fatalf("ServiceMetricSummary: %v", err)
	}
	if stat.Count != 1 || stat.Avg != 42 {
		t.Fatalf("metric day archive = %+v, want one sample of 42", stat)
	}
}

// TestPruneFloorsAtTheConsolidationWatermark pins the safety floor: an archive is
// never deleted ahead of the one that still has to read it, so a stalled
// consolidation delays reclaiming space instead of losing history.
func TestPruneFloorsAtTheConsolidationWatermark(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	at := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	mustRecord(t, s, true, at)

	// Long past the per-minute retention, but nothing has ever been consolidated,
	// so the watermark is zero and the prune must not touch the samples.
	now := at.Add(DefaultRetention1m + time.Hour)
	if _, err := s.PruneArchives(ctx, now); err != nil {
		t.Fatalf("PruneArchives: %v", err)
	}
	if _, _, _, found := archiveRow(t, s, resMinute, alignBucket(at, resMinute)); !found {
		t.Fatal("per-minute bucket pruned before it was consolidated")
	}

	// Once consolidated, the same prune reclaims it.
	mustRollup(t, s, now)
	if _, err := s.PruneArchives(ctx, now); err != nil {
		t.Fatalf("PruneArchives after rollup: %v", err)
	}
	if _, _, _, found := archiveRow(t, s, resMinute, alignBucket(at, resMinute)); found {
		t.Fatal("per-minute bucket kept after it was consolidated and aged out")
	}
	if _, _, _, found := archiveRow(t, s, resDay, alignBucket(at, resDay)); !found {
		t.Fatal("day archive lost the consolidated bucket")
	}
}

// TestPruneMemoDoesNotStrandRows pins that the skip-if-already-issued memo cannot
// leave rows behind. The memo makes a prune a no-op when an equal or higher boundary
// already ran, so a later pass with a *higher* cutoff must still delete, and a later
// pass with a lower one must be a genuine no-op rather than a silent gap.
func TestPruneMemoDoesNotStrandRows(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	at := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// Two per-minute buckets an hour apart, both consolidated so the safety floor
	// cannot be what keeps them.
	mustRecord(t, s, true, at)
	mustRecord(t, s, true, at.Add(time.Hour))
	mustRollup(t, s, at.Add(2*time.Hour))

	// A cutoff between them removes the older one only.
	if _, err := s.PruneBefore(ctx, at.Add(30*time.Minute)); err != nil {
		t.Fatalf("PruneBefore first: %v", err)
	}
	if _, _, _, found := archiveRow(t, s, resMinute, alignBucket(at, resMinute)); found {
		t.Fatal("older bucket survived a cutoff past it")
	}
	if _, _, _, found := archiveRow(t, s, resMinute, alignBucket(at.Add(time.Hour), resMinute)); !found {
		t.Fatal("newer bucket was removed by a cutoff before it")
	}

	// A lower cutoff now has nothing to do; the memo may skip it.
	if result, err := s.PruneBefore(ctx, at.Add(10*time.Minute)); err != nil || result.Archives != 0 {
		t.Fatalf("PruneBefore lower cutoff = %+v err=%v, want no rows removed", result, err)
	}

	// A higher cutoff must still delete, memo or not.
	if _, err := s.PruneBefore(ctx, at.Add(90*time.Minute)); err != nil {
		t.Fatalf("PruneBefore higher: %v", err)
	}
	if _, _, _, found := archiveRow(t, s, resMinute, alignBucket(at.Add(time.Hour), resMinute)); found {
		t.Fatal("the memo skipped a prune with a higher cutoff, stranding the bucket")
	}
}

// TestPruneBeforeKeepsBucketsStraddlingTheCutoff pins that an explicit operator
// cutoff never deletes samples newer than itself: a coarse bucket containing the
// cutoff cannot be split, so it is kept rather than dropped whole.
func TestPruneBeforeKeepsBucketsStraddlingTheCutoff(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 23, 0, 0, 0, time.UTC)
	mustRecord(t, s, true, now)
	mustRollup(t, s, now)

	// Midday cuts the day bucket in half; the sample itself is at 23:00, after it.
	cutoff := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	if _, err := s.PruneBefore(context.Background(), cutoff); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if _, _, _, found := archiveRow(t, s, resDay, alignBucket(now, resDay)); !found {
		t.Fatal("a cutoff inside a day bucket deleted a sample newer than the cutoff")
	}
}

// TestConsolidationKeepsGapsAsGaps pins that an unmonitored period stays absent
// rather than becoming a zero-total bucket, which would render as a total outage.
func TestConsolidationKeepsGapsAsGaps(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-2 * time.Hour)

	mustRecord(t, s, true, at)
	mustRollup(t, s, now)

	// The hour after the sample was never monitored; it must have no bucket at all.
	if _, _, _, found := archiveRow(t, s, resHour, alignBucket(at.Add(time.Hour), resHour)); found {
		t.Fatal("an unmonitored hour produced a bucket; a gap would render as downtime")
	}
}

// TestArchiveLadderIsConsolidatable turns the two properties the cascade silently
// depends on into gates. Consolidation re-keys with `bucket / target * target` and
// groups by the same span, which is only exact when each rung is a whole multiple of
// the one below it — inserting a plausible 4-hour rung (14400, not a divisor of
// 21600) would produce wrong coarse buckets with no test failing.
func TestArchiveLadderIsConsolidatable(t *testing.T) {
	ladder := DefaultRetention().archives()
	for i := 1; i < len(ladder); i++ {
		source, target := ladder[i-1].Res, ladder[i].Res
		if target <= source {
			t.Fatalf("rung %d res=%d is not coarser than its source %d", i, target, source)
		}
		if target%source != 0 {
			t.Fatalf("rung %d res=%d is not a whole multiple of its source %d", i, target, source)
		}
	}
}

// TestSLAWindowSegmentsAreWholeBuckets pins the other half of the derivation: each
// window's timeline segment must be a whole number of buckets in the archive that
// window resolves to, or a bucket would contribute to two cells. The year window is
// the documented exception — its segment is 365/12 days, not a whole day count.
func TestSLAWindowSegmentsAreWholeBuckets(t *testing.T) {
	retention := DefaultRetention()
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	for _, w := range SLAWindows {
		segSpan := int64(w.Span/time.Duration(w.Segments)) / int64(time.Second)
		res := retention.archiveFor(now.Add(-w.Span), now).Res
		remainder := segSpan % res
		if w.Name == slaWindowYear {
			if remainder == 0 {
				t.Fatalf("year window segment %ds is a whole number of %ds buckets; "+
					"the documented exception no longer applies", segSpan, res)
			}
			continue
		}
		if remainder != 0 {
			t.Fatalf("window %s segment %ds is not a whole number of %ds buckets", w.Name, segSpan, res)
		}
	}
}

func TestArchiveForPicksTheFinestCoveringResolution(t *testing.T) {
	retention := DefaultRetention()
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		span time.Duration
		want int64
	}{
		{"hour window", time.Hour, resMinute},
		{"per-minute retention edge", DefaultRetention1m, resMinute},
		{"just past per-minute retention", DefaultRetention1m + time.Second, res5Minutes},
		{"day window", slaSpanDay, res5Minutes},
		{"week window", slaSpanWeek, resHour},
		{"month window", slaSpanMonth, res6Hours},
		{"year window", slaSpanYear, resDay},
		{"past every retention", 10 * slaSpanYear, resDay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retention.archiveFor(now.Add(-tc.span), now).Res; got != tc.want {
				t.Fatalf("archiveFor(span %s) = %ds, want %ds", tc.span, got, tc.want)
			}
		})
	}
}

// TestSLAWindowTotalsMatchTheirTimelines pins the invariant that a window's
// reported availability and its timeline strip are aggregated from the same rows.
// They resolve to the same archive, so the two must agree exactly.
func TestSLAWindowTotalsMatchTheirTimelines(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	// Samples spread widely enough that the five windows resolve to five different
	// archives, including a failure so the counters are not trivially equal.
	for _, ago := range []time.Duration{
		time.Minute, 30 * time.Minute, 6 * time.Hour, 3 * slaSpanDay, 20 * slaSpanDay, 200 * slaSpanDay,
	} {
		mustRecord(t, s, ago != 30*time.Minute, now.Add(-ago))
	}
	mustRollup(t, s, now)

	timelines, err := s.SLATimelines(slaTestService, now)
	if err != nil {
		t.Fatalf("SLATimelines: %v", err)
	}
	report, err := s.SLAReport(slaTestService, now)
	if err != nil {
		t.Fatalf("SLAReport: %v", err)
	}
	for i, w := range SLAWindows {
		if timelines[i].Up != report[i].Up || timelines[i].Total != report[i].Total {
			t.Fatalf("window %s: timeline %d/%d disagrees with report %d/%d",
				w.Name, timelines[i].Up, timelines[i].Total, report[i].Up, report[i].Total)
		}
		if timelines[i].DownBuckets != report[i].DownBuckets {
			t.Fatalf("window %s: timeline down_buckets=%d disagrees with report %d",
				w.Name, timelines[i].DownBuckets, report[i].DownBuckets)
		}
		var segUp, segTotal, segDown int64
		for _, seg := range timelines[i].Segments {
			segUp += seg.Up
			segTotal += seg.Total
			segDown += seg.DownBuckets
		}
		if segUp != timelines[i].Up || segTotal != timelines[i].Total || segDown != timelines[i].DownBuckets {
			t.Fatalf("window %s segments sum to %d/%d/%d, want the window totals %d/%d/%d",
				w.Name, segUp, segTotal, segDown,
				timelines[i].Up, timelines[i].Total, timelines[i].DownBuckets)
		}
	}
}

// TestRecordSLATracksDownBucketsWithinAMinute covers the upsert's CASE: SQLite
// evaluates every SET expression against the pre-update row, so the accumulated
// counters have to be recomputed from excluded rather than read back.
func TestRecordSLATracksDownBucketsWithinAMinute(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	bucket := alignBucket(base, resMinute)

	mustRecord(t, s, true, base)
	if _, _, down, _ := archiveRow(t, s, resMinute, bucket); down != 0 {
		t.Fatalf("after one healthy cycle down_buckets=%d, want 0", down)
	}

	// Second cycle in the same minute fails: the minute becomes affected.
	mustRecord(t, s, false, base.Add(20*time.Second))
	if _, _, down, _ := archiveRow(t, s, resMinute, bucket); down != 1 {
		t.Fatalf("after a failed cycle down_buckets=%d, want 1", down)
	}

	// A third healthy cycle must not clear it: the minute still saw a failure.
	mustRecord(t, s, true, base.Add(40*time.Second))
	up, total, down, _ := archiveRow(t, s, resMinute, bucket)
	if up != 2 || total != 3 || down != 1 {
		t.Fatalf("up/total/down = %d/%d/%d, want 2/3/1", up, total, down)
	}
}
