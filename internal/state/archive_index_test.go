package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestArchiveIndexCostAndBenefit records both sides of the (res, bucket) indexes:
// what they cost on disk, and that they turn the maintenance scans into seeks.
//
// It is a measurement, not a threshold — it asserts only the query plans, which are
// the reason the indexes exist. The sizes are logged so `go test -v` shows the real
// tradeoff on the current schema instead of leaving it to an estimate.
func TestArchiveIndexCostAndBenefit(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenContext(context.Background(), filepath.Join(dir, Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// A fleet-shaped corpus: 40 services × 5 checks of per-minute history over the
	// per-minute retention, then consolidated up the ladder.
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	start := now.Add(-DefaultRetention1m)
	for minute := 0; start.Add(time.Duration(minute) * time.Minute).Before(now); minute++ {
		at := start.Add(time.Duration(minute) * time.Minute)
		for svc := range 40 {
			service := fmt.Sprintf("svc-%02d", svc)
			if err := s.RecordSLA(service, true, at); err != nil {
				t.Fatalf("RecordSLA: %v", err)
			}
			for check := range 5 {
				name := fmt.Sprintf("check-%d", check)
				if err := s.RecordCheckSLA(service, name, true, at); err != nil {
					t.Fatalf("RecordCheckSLA: %v", err)
				}
				if err := s.RecordMeasurement(service, name, float64(minute), at); err != nil {
					t.Fatalf("RecordMeasurement: %v", err)
				}
			}
		}
	}
	if _, err := s.Rollup(context.Background(), now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	logArchiveSizes(t, s, filepath.Join(dir, Filename))

	// The reason the indexes exist: every maintenance statement filters on
	// (res, bucket) with no series key, which the primary key cannot serve.
	for _, tc := range []struct {
		name string
		stmt string
		args []any
	}{
		{"sla consolidation range", `SELECT SUM(up_count) FROM sla_archive
			WHERE res = ? AND bucket >= ? AND bucket < ? GROUP BY service, check_name`,
			[]any{resMinute, alignBucket(start, resMinute), alignBucket(now, resMinute)}},
		{"metric consolidation range", `SELECT SUM(n) FROM metric_archive
			WHERE res = ? AND bucket >= ? AND bucket < ? GROUP BY scope, service, check_name, metric`,
			[]any{resMinute, alignBucket(start, resMinute), alignBucket(now, resMinute)}},
		{"sla prune", `DELETE FROM sla_archive WHERE res = ? AND bucket < ?`,
			[]any{resMinute, alignBucket(start, resMinute)}},
		{"metric oldest bucket", `SELECT MIN(bucket) FROM metric_archive WHERE res = ?`,
			[]any{res5Minutes}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, s, tc.stmt, tc.args...)
			t.Logf("plan: %s", plan)
			if !containsAny(plan, "USING INDEX", "USING COVERING INDEX") {
				t.Fatalf("maintenance statement does not use the (res, bucket) index; plan: %s", plan)
			}
		})
	}

	// The read paths must still seek on the primary key: it leads with the series
	// columns, so the new indexes are irrelevant to them and must not be chosen.
	t.Run("series read still seeks the primary key", func(t *testing.T) {
		plan := queryPlan(t, s, metricSeriesStmt,
			resMinute, metricScopeLatency, "svc-00", "check-0", "",
			alignBucket(start, resMinute), alignBucket(now, resMinute))
		t.Logf("plan: %s", plan)
		if !containsAny(plan, "USING PRIMARY KEY") {
			t.Fatalf("series read no longer seeks the primary key; plan: %s", plan)
		}
	})
}

// logArchiveSizes reports how much of the database each archive table and each of
// its indexes occupies, using dbstat when the driver exposes it and the file size
// otherwise.
func logArchiveSizes(t *testing.T, s *Store, path string) {
	t.Helper()
	if info, err := os.Stat(path); err == nil {
		t.Logf("database file: %.1f MiB", float64(info.Size())/(1<<20))
	}
	rows, err := s.reads().QueryContext(context.Background(),
		`SELECT name, SUM(pgsize) FROM dbstat GROUP BY name ORDER BY 2 DESC;`)
	if err != nil {
		t.Logf("dbstat unavailable (%v); per-object sizes not reported", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var bytes int64
		if err := rows.Scan(&name, &bytes); err != nil {
			t.Fatalf("scan dbstat: %v", err)
		}
		t.Logf("  %-34s %8.2f MiB", name, float64(bytes)/(1<<20))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dbstat: %v", err)
	}
}

func queryPlan(t *testing.T, s *Store, stmt string, args ...any) string {
	t.Helper()
	rows, err := s.reads().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+stmt, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail + "; ")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	return plan.String()
}

func containsAny(haystack string, needles ...string) bool {
	return slices.ContainsFunc(needles, func(needle string) bool {
		return strings.Contains(haystack, needle)
	})
}
