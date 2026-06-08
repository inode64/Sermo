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

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Filename is the database file name placed under the state directory.
const Filename = "sermo.db"

// Sources record who last changed a service's monitoring state, for inspection.
const (
	SourceConfig = "config" // daemon applied the service's `monitor` flag
	SourceCLI    = "cli"    // operator ran monitor/unmonitor
	SourceDaemon = "daemon" // daemon changed it autonomously
	SourceWeb    = "web"    // operator used the web UI
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
	// sla_sample accumulates one row per service per UTC minute: total_count is
	// the observed monitoring cycles in that minute and up_count the subset where
	// the service was healthy. Availability over any rolling window is the ratio
	// of the two summed across that window's buckets (SLA tracking).
	`CREATE TABLE sla_sample (
		service     TEXT NOT NULL,
		bucket      INTEGER NOT NULL,
		up_count    INTEGER NOT NULL,
		total_count INTEGER NOT NULL,
		PRIMARY KEY (service, bucket)
	);`,
	// measurement accumulates a numeric per-check observation (currently the check
	// latency in milliseconds for tcp/ports/http/service checks) into one row per
	// service+check per UTC minute: n samples whose sum/min/max let any rolling
	// window report an average, minimum and maximum.
	`CREATE TABLE measurement (
		service    TEXT NOT NULL,
		check_name TEXT NOT NULL,
		bucket     INTEGER NOT NULL,
		n          INTEGER NOT NULL,
		sum_ms     REAL NOT NULL,
		min_ms     REAL NOT NULL,
		max_ms     REAL NOT NULL,
		PRIMARY KEY (service, check_name, bucket)
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

// MonitorRecord is one service's persisted monitoring state.
type MonitorRecord struct {
	Active    bool
	Source    string
	UpdatedAt time.Time
}

// MonitorState returns a service's persisted monitoring row. found is false when
// the service has no recorded state yet.
func (s *Store) MonitorState(service string) (MonitorRecord, bool, error) {
	var active int
	var source, updated string
	err := s.db.QueryRow(
		`SELECT active, source, updated_at FROM monitor_state WHERE service = ?;`,
		service,
	).Scan(&active, &source, &updated)
	switch {
	case err == sql.ErrNoRows:
		return MonitorRecord{}, false, nil
	case err != nil:
		return MonitorRecord{}, false, err
	default:
		at, _ := time.Parse(time.RFC3339, updated)
		return MonitorRecord{
			Active: active != 0, Source: source, UpdatedAt: at,
		}, true, nil
	}
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

// SLAWindow names a rolling availability window and its length. The windows are
// rolling (ending "now"), so week/month/year use fixed 7/30/365-day spans rather
// than calendar boundaries.
type SLAWindow struct {
	Name string
	Span time.Duration
}

// SLAWindows are the reported rolling windows, shortest first.
var SLAWindows = []SLAWindow{
	{"hour", time.Hour},
	{"day", 24 * time.Hour},
	{"week", 7 * 24 * time.Hour},
	{"month", 30 * 24 * time.Hour},
	{"year", 365 * 24 * time.Hour},
}

// SLAValue is the availability of one service over one window: the up and total
// observed cycle counts. Ratio derives the fraction (and whether any data exists).
type SLAValue struct {
	Window string `json:"window"`
	Up     int64  `json:"up"`
	Total  int64  `json:"total"`
}

// Ratio returns the availability fraction in [0,1] and whether the window has any
// observed cycles. With no data (total==0) availability is unknown, not 0%.
func (v SLAValue) Ratio() (float64, bool) {
	if v.Total <= 0 {
		return 0, false
	}
	return float64(v.Up) / float64(v.Total), true
}

// minuteBucket truncates t to the start of its UTC minute as a unix epoch — the
// bucket key shared by every cycle observed in that minute.
func minuteBucket(t time.Time) int64 {
	return t.UTC().Truncate(time.Minute).Unix()
}

// RecordSLA accumulates one observed monitoring cycle into a service's current
// UTC-minute bucket: total_count +1, and up_count +1 when up. Paused or
// unobserved cycles are simply never recorded, so they do not count as downtime.
func (s *Store) RecordSLA(service string, up bool, at time.Time) error {
	u := 0
	if up {
		u = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO sla_sample (service, bucket, up_count, total_count)
		 VALUES (?, ?, ?, 1)
		 ON CONFLICT(service, bucket) DO UPDATE SET
		   up_count    = up_count + excluded.up_count,
		   total_count = total_count + excluded.total_count;`,
		service, minuteBucket(at), u,
	)
	return err
}

// SLA sums a service's up and total observed cycles over the rolling window
// ending at now (buckets with start >= now-span). total==0 means no data.
func (s *Store) SLA(service string, span time.Duration, now time.Time) (up, total int64, err error) {
	from := minuteBucket(now.Add(-span))
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)
		 FROM sla_sample WHERE service = ? AND bucket >= ?;`,
		service, from,
	).Scan(&up, &total)
	if err != nil {
		return 0, 0, err
	}
	return up, total, nil
}

// SLAPoint is one time bucket of a service's availability series: the up and
// total observed cycles in that UTC minute. It is the unit a future availability
// graph plots. A minute with no point means the service was not monitored then
// (Sermo down, or the service paused/disabled) — excluded, not counted as down.
type SLAPoint struct {
	Start time.Time `json:"start"`
	Up    int64     `json:"up"`
	Total int64     `json:"total"`
}

// SLASeries returns a service's per-minute availability points in [from, to),
// oldest first. Unmonitored minutes are absent (gaps) rather than zero rows, so a
// caller can render excluded periods distinctly from downtime. This is the stored
// "control" a graph is built from later.
func (s *Store) SLASeries(service string, from, to time.Time) ([]SLAPoint, error) {
	rows, err := s.db.Query(
		`SELECT bucket, up_count, total_count
		   FROM sla_sample
		  WHERE service = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		service, minuteBucket(from), minuteBucket(to),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SLAPoint
	for rows.Next() {
		var bucket, up, total int64
		if err := rows.Scan(&bucket, &up, &total); err != nil {
			return nil, err
		}
		out = append(out, SLAPoint{Start: time.Unix(bucket, 0).UTC(), Up: up, Total: total})
	}
	return out, rows.Err()
}

// SLAReport returns a service's availability across every SLAWindow, ordered as
// SLAWindows (hour..year).
func (s *Store) SLAReport(service string, now time.Time) ([]SLAValue, error) {
	out := make([]SLAValue, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		up, total, err := s.SLA(service, w.Span, now)
		if err != nil {
			return nil, err
		}
		out = append(out, SLAValue{Window: w.Name, Up: up, Total: total})
	}
	return out, nil
}

// MeasurementPoint is one time bucket of a check's measurement series: the sample
// count and the average/minimum/maximum value (milliseconds) in that UTC minute.
type MeasurementPoint struct {
	Start time.Time `json:"start"`
	N     int64     `json:"n"`
	Avg   float64   `json:"avg"`
	Min   float64   `json:"min"`
	Max   float64   `json:"max"`
}

// MeasurementStat summarizes a check's measurements over a window: the sample
// count and the average/minimum/maximum (milliseconds). Count==0 means no data.
type MeasurementStat struct {
	Count int64   `json:"count"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// RecordMeasurement accumulates one numeric observation (milliseconds) for a
// service+check into its current UTC-minute bucket: n+1, sum+value, and the
// running min/max.
func (s *Store) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO measurement (service, check_name, bucket, n, sum_ms, min_ms, max_ms)
		 VALUES (?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(service, check_name, bucket) DO UPDATE SET
		   n      = n + 1,
		   sum_ms = sum_ms + excluded.sum_ms,
		   min_ms = min(min_ms, excluded.min_ms),
		   max_ms = max(max_ms, excluded.max_ms);`,
		service, check, minuteBucket(at), valueMs, valueMs, valueMs,
	)
	return err
}

// MeasurementSummary returns the average/min/max and sample count for a check over
// the rolling window ending at now (buckets with start >= now-span).
func (s *Store) MeasurementSummary(service, check string, span time.Duration, now time.Time) (MeasurementStat, error) {
	var count sql.NullInt64
	var sum, minMs, maxMs sql.NullFloat64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(n),0), SUM(sum_ms), MIN(min_ms), MAX(max_ms)
		   FROM measurement WHERE service = ? AND check_name = ? AND bucket >= ?;`,
		service, check, minuteBucket(now.Add(-span)),
	).Scan(&count, &sum, &minMs, &maxMs)
	if err != nil {
		return MeasurementStat{}, err
	}
	stat := MeasurementStat{Count: count.Int64}
	if count.Int64 > 0 && sum.Valid {
		stat.Avg = sum.Float64 / float64(count.Int64)
		stat.Min = minMs.Float64
		stat.Max = maxMs.Float64
	}
	return stat, nil
}

// MeasurementSeries returns a check's per-minute points in [from, to), oldest
// first. Minutes with no observation are absent (gaps), as in SLASeries.
func (s *Store) MeasurementSeries(service, check string, from, to time.Time) ([]MeasurementPoint, error) {
	rows, err := s.db.Query(
		`SELECT bucket, n, sum_ms, min_ms, max_ms
		   FROM measurement
		  WHERE service = ? AND check_name = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		service, check, minuteBucket(from), minuteBucket(to),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minMs, maxMs float64
		if err := rows.Scan(&bucket, &n, &sum, &minMs, &maxMs); err != nil {
			return nil, err
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minMs, Max: maxMs})
	}
	return out, rows.Err()
}

// PruneMeasurements deletes measurement buckets older than before. Returns rows removed.
func (s *Store) PruneMeasurements(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM measurement WHERE bucket < ?;`, minuteBucket(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IntegrityCheck runs SQLite's PRAGMA integrity_check and returns an error when
// the database is not "ok" (corruption), for diagnostics.
func (s *Store) IntegrityCheck() error {
	var result string
	if err := s.db.QueryRow("PRAGMA integrity_check;").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check: %s", result)
	}
	return nil
}

// TrackedServices returns the distinct service names that have stored data
// (monitoring state, SLA samples, or check measurements), so diagnostics can
// flag rows for services no longer in the configuration.
func (s *Store) TrackedServices() ([]string, error) {
	rows, err := s.db.Query(`SELECT service FROM monitor_state
		UNION SELECT service FROM sla_sample
		UNION SELECT service FROM measurement ORDER BY service;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// PruneSLA deletes SLA buckets older than before, bounding the table to roughly
// one year of per-minute samples per service. Returns the rows removed.
func (s *Store) PruneSLA(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sla_sample WHERE bucket < ?;`, minuteBucket(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
