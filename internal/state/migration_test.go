package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestSnapshotColumnMigrationsHealAnOldDatabase pins the additive migration: a
// database whose snapshot tables predate the observation column opens cleanly
// and accepts inserts, instead of failing every persist forever — which is what
// production did when the column shipped behind CREATE TABLE IF NOT EXISTS.
func TestSnapshotColumnMigrationsHealAnOldDatabase(t *testing.T) {
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
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenContext(context.Background(), path)
	if err != nil {
		t.Fatalf("open over an old schema: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetServiceCheckSnapshots("web", map[string]CheckSnapshotRecord{
		"http": {Name: "http", CheckType: "http", Observation: "healthy", OK: true,
			Message: "ok", Ran: true},
	}); err != nil {
		t.Fatalf("persist into the migrated table: %v", err)
	}
}
