package state

// Consolidation and pruning of the resolution ladder.
//
// Live samples land in the finest archive (one bucket per UTC minute). Each
// coarser archive is consolidated from the one immediately below it, so a long
// daemon outage cannot lose a resolution: the day archive is rebuilt from the
// 6-hour archive, which still exists long after per-minute rows are gone.
//
// Consolidation is exact, not approximate. Availability sums its counters,
// metrics sum n and sum_v (keeping the average weight-correct) and carry min_v
// and max_v through, and down_buckets sums the one-minute buckets that saw a
// failure — so a short outage stays visible and countable at every resolution
// even though its exact minute is no longer recorded.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	// rollupRefreshBuckets is how many trailing target buckets each pass
	// re-consolidates, so the newest (still filling) bucket stays current. The
	// upsert replaces rather than accumulates, which is what makes redoing that
	// work idempotent.
	rollupRefreshBuckets = 2
	// rollupChunkBuckets bounds one INSERT…SELECT. Writes share a single
	// connection with the daemon's per-cycle upserts, so a long catch-up must not
	// hold it for the whole backlog.
	rollupChunkBuckets = 512
)

// Consolidation statements. `bucket / ? * ?` re-keys a source bucket to the start
// of the target bucket containing it; the GROUP BY divides by the same span. The
// WHERE clause is also what disambiguates ON CONFLICT in an INSERT…SELECT for
// SQLite's parser.
const (
	slaRollupStmt = `INSERT INTO sla_archive
			(res, service, check_name, bucket, up_count, total_count, down_buckets)
		SELECT ?, service, check_name, bucket / ? * ?,
		       SUM(up_count), SUM(total_count), SUM(down_buckets)
		  FROM sla_archive
		 WHERE res = ? AND bucket >= ? AND bucket < ?
		 GROUP BY service, check_name, bucket / ?
		ON CONFLICT(res, service, check_name, bucket) DO UPDATE SET
		  up_count     = excluded.up_count,
		  total_count  = excluded.total_count,
		  down_buckets = excluded.down_buckets;`

	metricRollupStmt = `INSERT INTO metric_archive
			(res, scope, service, check_name, metric, bucket, n, sum_v, min_v, max_v)
		SELECT ?, scope, service, check_name, metric, bucket / ? * ?,
		       SUM(n), SUM(sum_v), MIN(min_v), MAX(max_v)
		  FROM metric_archive
		 WHERE res = ? AND bucket >= ? AND bucket < ?
		 GROUP BY scope, service, check_name, metric, bucket / ?
		ON CONFLICT(res, scope, service, check_name, metric, bucket) DO UPDATE SET
		  n     = excluded.n,
		  sum_v = excluded.sum_v,
		  min_v = excluded.min_v,
		  max_v = excluded.max_v;`
)

// Per-archive prune and oldest-bucket statements.
const (
	slaPruneStmt     = `DELETE FROM ` + stateTableSLAArchive + ` WHERE res = ? AND bucket < ?;`
	metricPruneStmt  = `DELETE FROM ` + stateTableMetricArchive + ` WHERE res = ? AND bucket < ?;`
	slaOldestStmt    = `SELECT MIN(bucket) FROM ` + stateTableSLAArchive + ` WHERE res = ?;`
	metricOldestStmt = `SELECT MIN(bucket) FROM ` + stateTableMetricArchive + ` WHERE res = ?;`
)

// Watermark statements. The watermark is the start of the newest target bucket a
// consolidation pass reached; everything below it has been consolidated.
const (
	rollupWatermarkSelect = `SELECT watermark FROM rollup_state WHERE res = ?;`
	rollupWatermarkUpsert = `INSERT INTO rollup_state (res, watermark) VALUES (?, ?)
		ON CONFLICT(res) DO UPDATE SET watermark = excluded.watermark;`
)

// pruneKey identifies one archive table's resolution partition.
type pruneKey struct {
	table string
	res   int64
}

// prunedTo reports the newest prune boundary already issued for one partition, or
// 0 when none has been.
func (s *Store) prunedTo(table string, res int64) int64 {
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	return s.pruned[pruneKey{table: table, res: res}]
}

func (s *Store) setPrunedTo(table string, res, boundary int64) {
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	if s.pruned == nil {
		s.pruned = map[pruneKey]int64{}
	}
	s.pruned[pruneKey{table: table, res: res}] = boundary
}

// archiveTable is one archive table and the statements operating on it.
type archiveTable struct {
	name   string
	rollup string
	prune  string
	oldest string
}

// archiveTables is every table carrying the resolution ladder.
var archiveTables = [2]archiveTable{
	{name: stateTableSLAArchive, rollup: slaRollupStmt, prune: slaPruneStmt, oldest: slaOldestStmt},
	{name: stateTableMetricArchive, rollup: metricRollupStmt, prune: metricPruneStmt, oldest: metricOldestStmt},
}

// MaintainResult summarizes one maintenance pass over the archives.
type MaintainResult struct {
	// Rolled is the rows written into coarser archives.
	Rolled int64
	// Archives is the archive rows deleted as past their resolution's retention.
	Archives int64
	// Events is the event_log rows deleted.
	Events int64
}

// Pruned is the total number of rows the pass removed.
func (r MaintainResult) Pruned() int64 {
	return r.Archives + r.Events
}

// plus totals two passes, so a caller combining a maintenance pass with an explicit
// cutoff cannot forget a counter when one is added.
func (r MaintainResult) plus(other MaintainResult) MaintainResult {
	return MaintainResult{
		Rolled:   r.Rolled + other.Rolled,
		Archives: r.Archives + other.Archives,
		Events:   r.Events + other.Events,
	}
}

// Maintain consolidates the archives and then prunes them, the single pass the
// daemon repeats on its rollup interval and that state compact runs once.
//
// The order is load-bearing: pruning first could delete rows a coarser archive
// still had to read. Pruning additionally refuses to go past the next archive's
// consolidation watermark, so a stalled or interrupted pass delays reclaiming
// space instead of losing history.
func (s *Store) Maintain(ctx context.Context, now time.Time) (MaintainResult, error) {
	var out MaintainResult
	var err error
	if out.Rolled, err = s.Rollup(ctx, now); err != nil {
		return out, err
	}
	if out.Archives, err = s.PruneArchives(ctx, now); err != nil {
		return out, err
	}
	if out.Events, err = s.PruneEvents(ctx, now.Add(-s.retention.normalized().Events)); err != nil {
		return out, err
	}
	return out, nil
}

// CompactHistory runs one maintenance pass, applies an optional explicit operator
// cutoff, then vacuums so the freed pages return to the filesystem. A zero `before`
// skips the cutoff.
//
// It exists so `sermoctl state compact` and the web API cannot drift on the order,
// the accumulation or which steps the caller's deadline covers: the whole sequence
// runs under ctx.
func (s *Store) CompactHistory(ctx context.Context, now, before time.Time) (MaintainResult, error) {
	out, err := s.Maintain(ctx, now)
	if err != nil {
		return out, fmt.Errorf("consolidate state history: %w", err)
	}
	if !before.IsZero() {
		extra, err := s.PruneBefore(ctx, before)
		out = out.plus(extra)
		if err != nil {
			return out, fmt.Errorf("prune state history: %w", err)
		}
	}
	if err := s.Compact(ctx); err != nil {
		return out, fmt.Errorf("compact state database: %w", err)
	}
	return out, nil
}

// Rollup consolidates every archive from the one below it, finest first so a
// single pass can carry a fresh sample all the way up the ladder. It returns the
// rows written.
//
// The watermark belongs to the resolution, not to a table, so it is read once
// before both tables are consolidated and written once after: advancing it
// between them would make the second table skip everything below the first
// table's new watermark. An error leaves it untouched, so the next pass redoes the
// whole step.
func (s *Store) Rollup(ctx context.Context, now time.Time) (int64, error) {
	ladder := s.retention.archives()
	var rolled int64
	for i := 1; i < archiveCount; i++ {
		sourceRes, targetRes := ladder[i-1].Res, ladder[i].Res
		watermark, err := s.rollupWatermark(ctx, targetRes)
		if err != nil {
			return rolled, err
		}
		for _, table := range archiveTables {
			n, err := s.rollupTable(ctx, table, sourceRes, targetRes, watermark, now)
			rolled += n
			if err != nil {
				return rolled, err
			}
		}
		if err := s.setRollupWatermark(ctx, targetRes, alignBucket(now, targetRes)); err != nil {
			return rolled, err
		}
	}
	return rolled, nil
}

// rollupTable consolidates one table from sourceRes into targetRes, starting from
// the resolution's watermark.
func (s *Store) rollupTable(ctx context.Context, table archiveTable, sourceRes, targetRes, watermark int64, now time.Time) (int64, error) {
	// max(…, 0) keeps a small-but-non-zero watermark from walking the chunk loop
	// back towards the epoch; only a hand-edited rollup_state can produce one, but
	// the oldest-bucket probe used to bound this and no longer runs here.
	from := max(watermark-rollupRefreshBuckets*targetRes, 0)
	if watermark == 0 {
		// Never consolidated: without a watermark to start from, the chunk loop
		// would walk from the epoch. The oldest stored bucket bounds it to the data
		// that exists. This probe scans the whole source partition (bucket is the
		// last primary-key column, so SQLite cannot seek to the minimum), which is
		// why it is confined to the first pass instead of running on every one.
		oldest, ok, err := s.oldestBucket(ctx, table, sourceRes)
		if err != nil {
			return 0, err
		}
		if !ok {
			// Nothing stored at the source resolution yet.
			return 0, nil
		}
		from = oldest
	}
	// The upper bound covers the newest source bucket, so the partial target
	// bucket is refreshed on every pass rather than appearing only once complete.
	to := alignBucket(now, sourceRes) + sourceRes

	var rolled int64
	step := rollupChunkBuckets * targetRes
	for start := from; start < to; start += step {
		// A cancelled context surfaces from the statement itself, wrapped the same
		// way, so no separate ctx.Err() guard is needed between chunks.
		n, err := s.rollupChunk(ctx, table, sourceRes, targetRes, start, min(start+step, to))
		rolled += n
		if err != nil {
			return rolled, err
		}
	}
	return rolled, nil
}

// rollupChunk consolidates the source buckets in [from, to) into target buckets.
func (s *Store) rollupChunk(ctx context.Context, table archiveTable, sourceRes, targetRes, from, to int64) (int64, error) {
	res, err := s.exec(ctx, table.rollup,
		targetRes, targetRes, targetRes, sourceRes, from, to, targetRes)
	if err != nil {
		return 0, fmt.Errorf("consolidate %s into %ds buckets: %w", table.name, targetRes, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read consolidated %s row count: %w", table.name, err)
	}
	return n, nil
}

// PruneArchives deletes buckets past their resolution's retention and returns the
// rows removed. Every archive but the coarsest is additionally floored at the next
// archive's consolidation watermark, so no resolution is deleted ahead of the one
// that still has to read it.
func (s *Store) PruneArchives(ctx context.Context, now time.Time) (int64, error) {
	ladder := s.retention.archives()
	var pruned int64
	// Walk coarsest first so each archive already knows the one that consumes it:
	// the previously visited resolution. The coarsest has no consumer.
	var consumerRes int64
	for _, stored := range slices.Backward(ladder[:]) {
		before := now.Add(-stored.Retention).Unix()
		if consumerRes > 0 {
			watermark, err := s.rollupWatermark(ctx, consumerRes)
			if err != nil {
				return pruned, err
			}
			before = min(before, watermark)
		}
		for _, table := range archiveTables {
			n, err := s.pruneArchive(ctx, table, stored.Res, before)
			pruned += n
			if err != nil {
				return pruned, err
			}
		}
		consumerRes = stored.Res
	}
	return pruned, nil
}

// PruneBefore deletes every archive bucket and event that starts before the
// cutoff, at every resolution. It is the explicit operator cutoff behind
// `state compact --before`, and deliberately ignores the consolidation
// watermarks the automatic prune respects: the operator asked for that history to
// be gone, not to be consolidated first.
func (s *Store) PruneBefore(ctx context.Context, before time.Time) (MaintainResult, error) {
	var out MaintainResult
	cutoff := before.Unix()
	for _, stored := range s.retention.archives() {
		for _, table := range archiveTables {
			n, err := s.pruneArchive(ctx, table, stored.Res, cutoff)
			out.Archives += n
			if err != nil {
				return out, err
			}
		}
	}
	events, err := s.PruneEvents(ctx, before)
	out.Events = events
	if err != nil {
		return out, err
	}
	return out, nil
}

// pruneArchive deletes one resolution's buckets that lie entirely before the
// cutoff. A bucket straddling the cutoff is kept: it cannot be split, and
// dropping it would take the newer half of its samples with it — so a cutoff never
// deletes data more recent than itself, at the cost of over-keeping at most one
// bucket per series.
//
// A DELETE that matches nothing still scans the whole resolution partition, on the
// same single connection the daemon's per-cycle upserts use. The cutoff only
// crosses a bucket boundary once per res, so most passes have nothing to do for the
// coarse resolutions — the daily archive can only lose a bucket once a day. The
// boundary memo skips those, and clears on restart at the cost of one extra pass.
func (s *Store) pruneArchive(ctx context.Context, table archiveTable, res, cutoff int64) (int64, error) {
	boundary := cutoff - res + 1
	if s.prunedTo(table.name, res) >= boundary {
		return 0, nil
	}
	result, err := s.exec(ctx, table.prune, res, boundary)
	if err != nil {
		return 0, fmt.Errorf("prune %s %ds buckets before %d: %w", table.name, res, cutoff, err)
	}
	s.setPrunedTo(table.name, res, boundary)
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned %s row count: %w", table.name, err)
	}
	return n, nil
}

// oldestBucket returns the earliest bucket stored at res, and whether any exists.
// It bounds the first consolidation pass to the data actually present instead of
// walking from the epoch.
func (s *Store) oldestBucket(ctx context.Context, table archiveTable, res int64) (int64, bool, error) {
	var oldest sql.NullInt64
	if err := s.reads().QueryRowContext(ctx, table.oldest, res).Scan(&oldest); err != nil {
		return 0, false, fmt.Errorf("read oldest %s %ds bucket: %w", table.name, res, err)
	}
	return oldest.Int64, oldest.Valid, nil
}

// rollupWatermark returns how far the archive at res has consolidated its source,
// or 0 when it never has.
func (s *Store) rollupWatermark(ctx context.Context, res int64) (int64, error) {
	var watermark int64
	err := s.reads().QueryRowContext(ctx, rollupWatermarkSelect, res).Scan(&watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %ds rollup watermark: %w", res, err)
	}
	return watermark, nil
}

func (s *Store) setRollupWatermark(ctx context.Context, res, watermark int64) error {
	if _, err := s.exec(ctx, rollupWatermarkUpsert, res, watermark); err != nil {
		return fmt.Errorf("record %ds rollup watermark: %w", res, err)
	}
	return nil
}
