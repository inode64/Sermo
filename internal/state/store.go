// Package state is Sermo's persistent store, a SQLite database kept under
// paths.state (default /var/lib/sermo/sermo.db).
//
// Unlike the runtime locks and pause markers under /run (tmpfs, wiped on
// reboot), this store survives reboots. That durability is what lets the
// `monitor: previous` flag restore a service's last monitoring state across a
// daemon restart or a full reboot.
//
// The schema is versioned through PRAGMA user_version and migrated forward on
// Open, so future features (action history, metric retention, audit trails) add
// a migration to the list below without any manual upgrade step. The driver is
// modernc.org/sqlite — pure Go, no CGO — to keep cross-compilation simple.
package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Filename is the database file name placed under the state directory.
const Filename = "sermo.db"

// Sources record who last changed a service's monitoring state, for inspection.
const (
	SourceConfig = "config" // daemon applied the service's `monitor` flag
	SourceCLI    = "cli"    // operator ran monitor/unmonitor
	SourceDaemon = "daemon" // daemon changed it autonomously
)

// migrations are applied in order; index i upgrades the schema from version i to
// i+1. Never edit or reorder an existing entry once released — only append.
var migrations = []string{
	`CREATE TABLE monitor_state (
		service    TEXT PRIMARY KEY,
		active     INTEGER NOT NULL,
		source     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
}

// Store is a handle to the persistent state database. It is safe for concurrent
// use; access is serialized onto a single connection (the store is low-traffic
// and this avoids cross-process "database is locked" surprises).
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path, creating the parent
// directory and running any pending migrations. WAL mode plus a busy timeout let
// the daemon (long-lived reader/writer) and sermoctl (short-lived writer)
// coexist across processes.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create state dir %s: %w", dir, err)
		}
	}

	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", path, err)
	}
	// One connection keeps PRAGMAs and writes consistent and dodges intra-process
	// lock contention; the state store sees little traffic so this costs nothing.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate state db %s: %w", path, err)
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version;").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return err
		}
		// user_version cannot be parameterized; i+1 is a trusted integer.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d;", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Active reports whether monitoring is currently active for a service. found is
// false when the service has no recorded state yet (the caller decides the
// default — typically "monitor on").
func (s *Store) Active(service string) (active, found bool, err error) {
	var v int
	err = s.db.QueryRow("SELECT active FROM monitor_state WHERE service = ?;", service).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		return false, false, nil
	case err != nil:
		return false, false, err
	default:
		return v != 0, true, nil
	}
}

// SetActive records a service's monitoring state, upserting the row. source
// notes who set it (SourceConfig, SourceCLI, SourceDaemon) for inspection.
func (s *Store) SetActive(service string, active bool, source string) error {
	v := 0
	if active {
		v = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO monitor_state (service, active, source, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   active     = excluded.active,
		   source     = excluded.source,
		   updated_at = excluded.updated_at;`,
		service, v, source, s.now().UTC().Format(time.RFC3339),
	)
	return err
}
