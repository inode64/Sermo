package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sermo/internal/rules"
	"testing"
	"time"
)

// TestStateColumnMigrationsHealAnOldDatabase pins the additive migrations: a
// database whose cache/control tables predate their newest columns opens
// cleanly and accepts writes, instead of failing every persist forever.
func TestStateColumnMigrationsHealAnOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Build the pre-observation shape by hand: the first shipped columns only.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE service_check_snapshot (
			service TEXT NOT NULL, check_name TEXT NOT NULL,
			ok INTEGER NOT NULL, condition INTEGER NOT NULL,
			optional INTEGER NOT NULL, skipped INTEGER NOT NULL,
			message TEXT NOT NULL, data TEXT NOT NULL,
			ran INTEGER NOT NULL, at INTEGER NOT NULL,
			PRIMARY KEY (service, check_name));`,
		`INSERT INTO service_check_snapshot VALUES ('web','http',1,0,0,0,'ok','{}',1,0);`,
		`CREATE TABLE watch_runtime_state (
			watch TEXT NOT NULL, slot TEXT NOT NULL,
			firing INTEGER NOT NULL DEFAULT 0,
			last_notify_at INTEGER NOT NULL DEFAULT 0,
			consecutive INTEGER NOT NULL DEFAULT 0,
			history TEXT NOT NULL DEFAULT '[]',
			true_since INTEGER NOT NULL DEFAULT 0,
			timed_history TEXT NOT NULL DEFAULT '[]',
			last_action_at INTEGER NOT NULL DEFAULT 0,
			recent_actions TEXT NOT NULL DEFAULT '[]',
			current_backoff_ns INTEGER NOT NULL DEFAULT 0,
			clear_since INTEGER NOT NULL DEFAULT 0,
			clear_consecutive INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (watch, slot));`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenContextWith(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("open over an old schema: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetServiceCheckSnapshots("web", map[string]CheckSnapshotRecord{
		"http": {CheckType: "http", Observation: "healthy", OK: true,
			Message: "ok", Ran: true, Severity: "warning"},
	}); err != nil {
		t.Fatalf("persist into the migrated table: %v", err)
	}
	snapshots, err := s.ServiceCheckSnapshots()
	if err != nil {
		t.Fatalf("read the migrated table: %v", err)
	}
	if got := snapshots["web"]["http"].Severity; got != "warning" {
		t.Fatalf("severity after migration = %q, want warning", got)
	}
	if err := s.SetWatchRuntimeState("storage-root", "result", WatchRuntimeRecord{
		Unavailable: true,
	}); err != nil {
		t.Fatalf("persist into the migrated watch runtime table: %v", err)
	}
}

func TestRuleWindowMigrationPreservesProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE rule_window_state (
		service TEXT NOT NULL, rule_name TEXT NOT NULL,
		consecutive INTEGER NOT NULL DEFAULT 0,
		history TEXT NOT NULL DEFAULT '[]',
		true_since INTEGER NOT NULL DEFAULT 0,
		timed_history TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (service, rule_name));
		INSERT INTO rule_window_state (service, rule_name, consecutive, history)
		VALUES ('web', 'unhealthy', 2, '[true,true]');`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("create legacy database: %v; close: %v", err, closeErr)
	}
	want := RuleWindowRecord{Consecutive: 2, History: []bool{true, true}, TimedHistory: []rules.WindowSample{}}
	for range 2 {
		s, err := OpenContextWith(t.Context(), path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.RuleWindowStates("web")
		if err != nil || !reflect.DeepEqual(got["unhealthy"], want) {
			_ = s.Close()
			t.Fatalf("migrated progress = %+v, error = %v; want %+v", got, err, want)
		}
		want.Firing = true
		want.ClearSince = time.Unix(100, 0).UTC()
		want.ClearConsecutive = 1
		err = s.SetRuleWindowStates("web", map[string]RuleWindowRecord{"unhealthy": want})
		closeErr = s.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("persist migrated progress: %v; close: %v", err, closeErr)
		}
	}
}
