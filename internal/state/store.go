// Package state is Sermo's persistent store, a SQLite database kept under
// paths.state (default /var/lib/sermo/sermo.db).
//
// Unlike the runtime locks and pause markers under /run (tmpfs, wiped on
// reboot), this store survives reboots. That durability is what lets the
// `monitor: previous` flag restore a service's or watch's last monitoring state
// across a daemon restart or a full reboot, and what keeps automatic
// remediation cooldown/backoff and rule-window progress from resetting when
// sermod restarts.
//
// The schema is versioned through PRAGMA user_version and migrated forward on
// Open, so future history and retention changes add
// a migration to the list below without any manual upgrade step. The driver is
// modernc.org/sqlite — pure Go, no CGO — to keep cross-compilation simple.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"sermo/internal/units"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Filename is the database file name placed under the state directory.
const Filename = "sermo.db"

const (
	stateDirMode        = 0o700
	secondsPerMinute    = units.SecondsPerMinute
	sqliteBusyTimeoutMS = 5000
	sqliteDriverName    = "sqlite"
)

const (
	stateTableSLASample         = "sla_sample"
	stateTableCheckSLASample    = "check_sla_sample"
	stateTableMeasurement       = "measurement"
	stateTableMeasurementMetric = "measurement_metric"
	stateTableDaemonMetric      = "daemon_metric"
	stateTableServiceMetric     = "service_metric"
)

// Sources record who last changed a monitoring state row, for inspection.
const (
	SourceConfig         = "config"           // daemon applied an entry's `monitor` flag
	SourceCLI            = "cli"              // operator ran monitor/unmonitor
	SourceDaemon         = "daemon"           // daemon changed it autonomously
	SourceWeb            = "web"              // operator used the web UI
	SourceCLIManualStop  = "cli-manual-stop"  // CLI stop paused monitoring for later restore
	SourceWebManualStop  = "web-manual-stop"  // Web UI stop paused monitoring for later restore
	SourceCLIMountUmount = "cli-mount-umount" // CLI umount paused a storage watch for later mount restore
	SourceWebMountUmount = "web-mount-umount" // Web UI umount paused a storage watch for later mount restore
)

// IsManualStopSource reports whether a paused monitoring row was created by a
// successful manual stop and should be restored after a later successful start.
func IsManualStopSource(source string) bool {
	switch source {
	case SourceCLIManualStop, SourceWebManualStop:
		return true
	default:
		return false
	}
}

// IsMountUmountSource reports whether a paused watch row was created by a
// successful storage umount and should be restored after a later successful
// mount.
func IsMountUmountSource(source string) bool {
	switch source {
	case SourceCLIMountUmount, SourceWebMountUmount:
		return true
	default:
		return false
	}
}

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
	// measurement_metric is the generic per-check NAMED-metric time series: any
	// check's numeric Result.Data fields (e.g. hdparm read/cached MB/s) accumulate
	// into one row per service+check+metric per UTC minute, mirroring `measurement`
	// but with a metric dimension and unit-agnostic columns. Reusable by any check
	// that declares graphable metrics.
	`CREATE TABLE measurement_metric (
		service    TEXT NOT NULL,
		check_name TEXT NOT NULL,
		metric     TEXT NOT NULL,
		bucket     INTEGER NOT NULL,
		n          INTEGER NOT NULL,
		sum_v      REAL NOT NULL,
		min_v      REAL NOT NULL,
		max_v      REAL NOT NULL,
		PRIMARY KEY (service, check_name, metric, bucket)
	);`,
	// daemon_metric stores sermod's own process metrics (the "Daemon / Engine
	// settings" graphs) per UTC minute. It has no service/check dimensions:
	// metric is one of cpu, memory or io.
	`CREATE TABLE daemon_metric (
		metric TEXT NOT NULL,
		bucket INTEGER NOT NULL,
		n      INTEGER NOT NULL,
		sum_v  REAL NOT NULL,
		min_v  REAL NOT NULL,
		max_v  REAL NOT NULL,
		PRIMARY KEY (metric, bucket)
	);`,
	// check_sla_sample accumulates one row per service+check per UTC minute.
	// It mirrors sla_sample but keeps the check dimension, so the dashboard can
	// show which individual check degraded over each rolling SLA window.
	`CREATE TABLE check_sla_sample (
		service     TEXT NOT NULL,
		check_name  TEXT NOT NULL,
		bucket      INTEGER NOT NULL,
		up_count    INTEGER NOT NULL,
		total_count INTEGER NOT NULL,
		PRIMARY KEY (service, check_name, bucket)
	);`,
	// service_metric stores each service process tree's runtime metrics for the
	// web detail graphs. The service dimension keeps CPU, memory and IO history
	// across daemon restarts without mixing services.
	`CREATE TABLE service_metric (
		service TEXT NOT NULL,
		metric  TEXT NOT NULL,
		bucket  INTEGER NOT NULL,
		n       INTEGER NOT NULL,
		sum_v   REAL NOT NULL,
		min_v   REAL NOT NULL,
		max_v   REAL NOT NULL,
		PRIMARY KEY (service, metric, bucket)
	);`,
	// event_log stores the operator-visible event/activity feed. Unlike the
	// runtime ring in sermod, this table survives daemon restarts so the web UI
	// and per-service detail panes can repopulate their recent history.
	`CREATE TABLE event_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			at      INTEGER NOT NULL,
			service TEXT NOT NULL DEFAULT '',
			watch   TEXT NOT NULL DEFAULT '',
			kind    TEXT NOT NULL DEFAULT '',
			rule    TEXT NOT NULL DEFAULT '',
			action  TEXT NOT NULL DEFAULT '',
			status  TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT ''
		);`,
	`CREATE INDEX event_log_at_idx ON event_log (at DESC, id DESC);`,
	`CREATE INDEX event_log_service_at_idx ON event_log (service, at DESC, id DESC);`,
	// remediation_state stores automatic remediation cooldown, rate-limit and
	// backoff state per service. It is control state, not historical metrics, so
	// daemon restarts must not reset when a rule may act again.
	`CREATE TABLE remediation_state (
		service            TEXT PRIMARY KEY,
		last_action_at     INTEGER NOT NULL DEFAULT 0,
		recent_actions     TEXT NOT NULL DEFAULT '[]',
		current_backoff_ns INTEGER NOT NULL DEFAULT 0
	);`,
	// rule_window_state stores each service rule's for/within progress so
	// restarting sermod does not make a pending rule start counting from zero.
	`CREATE TABLE rule_window_state (
		service     TEXT NOT NULL,
		rule_name   TEXT NOT NULL,
		consecutive INTEGER NOT NULL DEFAULT 0,
		history     TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (service, rule_name)
	);`,
	`ALTER TABLE rule_window_state ADD COLUMN true_since INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE rule_window_state ADD COLUMN timed_history TEXT NOT NULL DEFAULT '[]';`,
	// global_state holds daemon-wide on/off flags that are not keyed by service
	// (currently the "panic_mode" toggle). It is control state, not metrics, so it
	// survives daemon restarts — clearing panic mode must be a deliberate act.
	`CREATE TABLE global_state (
		key        TEXT PRIMARY KEY,
		value      INTEGER NOT NULL,
		source     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// event_log gains an `app` dimension (alongside service/watch) so installed
	// application monitoring can record per-app errors/recoveries queryable on
	// their own, like per-service events.
	`ALTER TABLE event_log ADD COLUMN app TEXT NOT NULL DEFAULT '';`,
	`CREATE INDEX event_log_app_at_idx ON event_log (app, at DESC, id DESC);`,
	// event_log gains an `output` column: the bounded stdout/stderr of the failing
	// command (app probe or service `command` check) behind the event, so the
	// dashboard can show why it failed.
	`ALTER TABLE event_log ADD COLUMN output TEXT NOT NULL DEFAULT '';`,
	// operation_settling suppresses service rules/alerts around manual or
	// automatic service operations. A row starts in phase "running" while the
	// operation is in progress, then moves to "settling" after a successful
	// relaunch until the worker has observed one active check cycle.
	`CREATE TABLE operation_settling (
		service    TEXT PRIMARY KEY,
		action     TEXT NOT NULL,
		phase      TEXT NOT NULL,
		source     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// service_check_snapshot stores the latest service check result published by
	// each worker. It is current observable state, not history, so the web UI can
	// show the last real daemon-cycle reading immediately after a restart.
	`CREATE TABLE service_check_snapshot (
		service    TEXT NOT NULL,
		check_name TEXT NOT NULL,
		ok         INTEGER NOT NULL,
		condition  INTEGER NOT NULL,
		optional   INTEGER NOT NULL,
		skipped    INTEGER NOT NULL,
		message    TEXT NOT NULL,
		data       TEXT NOT NULL,
		ran        INTEGER NOT NULL,
		at         INTEGER NOT NULL,
		PRIMARY KEY (service, check_name)
	);`,
	// watch_check_snapshot stores the latest host-watch result per visible slot
	// (for example one slot per metric). It keeps /api/watches backed by daemon
	// cycle data across process restarts.
	`CREATE TABLE watch_check_snapshot (
		watch      TEXT NOT NULL,
		slot       TEXT NOT NULL,
		check_type TEXT NOT NULL,
		ok         INTEGER NOT NULL,
		condition  INTEGER NOT NULL,
		optional   INTEGER NOT NULL,
		skipped    INTEGER NOT NULL,
		message    TEXT NOT NULL,
		data       TEXT NOT NULL,
		ran        INTEGER NOT NULL,
		at         INTEGER NOT NULL,
		PRIMARY KEY (watch, slot)
	);`,
	// watch_runtime_state persists one watch slot's firing episode, notification
	// pacing, condition window and automatic-action policy state. This prevents a
	// daemon restart from turning an unchanged condition into a new episode.
	`CREATE TABLE watch_runtime_state (
		watch              TEXT NOT NULL,
		slot               TEXT NOT NULL,
		firing             INTEGER NOT NULL DEFAULT 0,
		last_notify_at     INTEGER NOT NULL DEFAULT 0,
		consecutive        INTEGER NOT NULL DEFAULT 0,
		history            TEXT NOT NULL DEFAULT '[]',
		true_since         INTEGER NOT NULL DEFAULT 0,
		timed_history      TEXT NOT NULL DEFAULT '[]',
		last_action_at     INTEGER NOT NULL DEFAULT 0,
		recent_actions     TEXT NOT NULL DEFAULT '[]',
		current_backoff_ns INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (watch, slot)
	);`,
	// Service snapshots initially persisted only their check name. Keep the type
	// too: a reload may retain a name while changing its check implementation.
	// The web layer must then wait for a result from the new implementation.
	`ALTER TABLE service_check_snapshot ADD COLUMN check_type TEXT NOT NULL DEFAULT '';`,
}

// Store is a handle to the persistent state database. It is safe for concurrent
// use; access is serialized onto a single connection (the store is low-traffic
// and this avoids cross-process "database is locked" surprises).
type Store struct {
	db  *sql.DB
	now func() time.Time
	ctx context.Context
}

// sqlCtx is the context passed to database/sql *Context methods.
func (s *Store) sqlCtx() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

const (
	hoursPerDay          = units.HoursPerDay
	historyRetentionDays = 366
	eventQueryMaxArgs    = 2
)

// DefaultHistoryRetention is the normal SLA/metrics/event history window kept
// unless an operator runs state compact with an explicit --before cutoff.
const DefaultHistoryRetention = historyRetentionDays * hoursPerDay * time.Hour

// DefaultSeriesWindow is the normal lookback used when a series request omits
// its `since` window.
const DefaultSeriesWindow = hoursPerDay * time.Hour

// PruneHistoryResult summarizes old persisted history removed from time-series
// and event tables.
type PruneHistoryResult struct {
	SLA            int64
	Measurements   int64
	Metrics        int64
	DaemonMetrics  int64
	ServiceMetrics int64
	Events         int64
	Rows           int64
}

// OpenContext opens (creating if needed) the database at path, creating the
// parent directory and running any pending migrations. WAL mode plus a busy
// timeout let the daemon (long-lived reader/writer) and sermoctl (short-lived
// writer) coexist across processes.
func OpenContext(ctx context.Context, path string) (*Store, error) {
	return OpenContextWith(ctx, path, Options{})
}

// DefaultCacheBytes is the SQLite page-cache size used when the caller does not
// override it. The time-series tables (measurement/metric/sla) and their indexes
// grow into the tens of MB; 64 MiB keeps the hot index pages resident so a
// per-cycle upsert burst does not thrash them from disk and — because every
// statement shares the single connection — stall interactive control writes
// (monitor/unmonitor) behind it for seconds.
const DefaultCacheBytes = 64 * units.BytesPerMiB

// Options tunes an opened Store.
type Options struct {
	// CacheBytes sets the SQLite page cache. Values <= 0 use DefaultCacheBytes.
	CacheBytes int64
}

// OpenContextWith opens the store with explicit context and options.
func OpenContextWith(ctx context.Context, path string, opts Options) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		// Owner-only (root): the state DB holds control state and history, not
		// secrets, but there is no reason for it to be world-traversable. Matches the
		// packaging (tmpfiles.d / OpenRC) mode. MkdirAll leaves an existing dir's
		// mode untouched, so a pre-created 0700 dir is preserved.
		if err := os.MkdirAll(dir, stateDirMode); err != nil {
			return nil, fmt.Errorf("create state dir %s: %w", dir, err)
		}
	}

	cacheBytes := opts.CacheBytes
	if cacheBytes <= 0 {
		cacheBytes = DefaultCacheBytes
	}
	// SQLite reads a negative cache_size as a KiB budget (a positive value would be
	// a page count); convert the byte budget to KiB.
	cacheKiB := cacheBytes / units.BytesPerKiB

	// synchronous=NORMAL is safe under WAL (no corruption risk; at worst the last
	// few committed cycles are lost on a power cut) and avoids an fsync on every
	// commit — the per-cycle SLA/measurement writes would otherwise each force a
	// disk sync.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)&_pragma=cache_size(-%d)",
		path, sqliteBusyTimeoutMS, cacheKiB,
	)
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", path, err)
	}
	// One connection keeps PRAGMAs and writes consistent and dodges intra-process
	// lock contention; the state store sees little traffic so this costs nothing.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now, ctx: ctx}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate state db %s: %w", path, err)
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state store: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&version); err != nil {
		return fmt.Errorf("read state db user_version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin state db migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply state db migration %d: %w", i+1, err)
		}
		// user_version cannot be parameterized; i+1 is a trusted integer.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d;", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set state db user_version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit state db migration %d: %w", i+1, err)
		}
	}
	return nil
}

// MonitorRecord is one persisted monitoring state row.
type MonitorRecord struct {
	Active    bool
	Source    string
	UpdatedAt time.Time
}

// Operation settling phases.
const (
	OperationSettlingRunning  = "running"
	OperationSettlingSettling = "settling"
)

// OperationSettlingRecord is one persisted service-operation settling row.
type OperationSettlingRecord struct {
	Action    string
	Phase     string
	Source    string
	UpdatedAt time.Time
}

// CheckSnapshotRecord is one persisted latest check result. Name is the service
// check name or host-watch slot; CheckType identifies the check that produced
// the data so callers never decode a prior result as a new check type.
type CheckSnapshotRecord struct {
	Name      string
	CheckType string
	OK        bool
	Condition bool
	Optional  bool
	Skipped   bool
	Message   string
	Data      map[string]any
	Ran       bool
	At        time.Time
}

// MonitorState returns a persisted monitoring row. found is false when the entry
// has no recorded state yet.
func (s *Store) MonitorState(service string) (MonitorRecord, bool, error) {
	on, source, at, found, err := s.loadFlagRow(
		`SELECT active, source, updated_at FROM monitor_state WHERE service = ?;`,
		service, "load monitor state for "+service)
	if !found || err != nil {
		return MonitorRecord{}, false, err
	}
	return MonitorRecord{Active: on, Source: source, UpdatedAt: at}, true, nil
}

// loadFlagRow runs a single-row (flag, source, updated_at) query and decodes
// it; found is false when no row exists and errContext labels failures. It is
// the read half shared by the boolean flag tables (monitor_state, global_state).
func (s *Store) loadFlagRow(query string, key any, errContext string) (on bool, source string, at time.Time, found bool, err error) {
	var v int
	var updated string
	err = s.db.QueryRowContext(s.sqlCtx(), query, key).Scan(&v, &source, &updated)
	switch {
	case err == sql.ErrNoRows:
		return false, "", time.Time{}, false, nil
	case err != nil:
		return false, "", time.Time{}, false, fmt.Errorf("%s: %w", errContext, err)
	default:
		at, _ = time.Parse(time.RFC3339, updated)
		return v != 0, source, at, true, nil
	}
}

// upsertFlagRow writes an on/off flag row keyed by key with source and the
// current timestamp; the write half shared by the boolean flag tables.
func (s *Store) upsertFlagRow(query string, key any, on bool, source, errContext string) error {
	v := 0
	if on {
		v = 1
	}
	if _, err := s.db.ExecContext(s.sqlCtx(), query, key, v, source, s.now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("%s: %w", errContext, err)
	}
	return nil
}

// Active reports whether monitoring is currently active for an entry. found is
// false when the entry has no recorded state yet (the caller decides the
// default — typically "monitor on").
func (s *Store) Active(service string) (active, found bool, err error) {
	var v int
	err = s.db.QueryRowContext(s.sqlCtx(), "SELECT active FROM monitor_state WHERE service = ?;", service).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("load active monitor flag for %s: %w", service, err)
	default:
		return v != 0, true, nil
	}
}

// SetActive records an entry's monitoring state, upserting the row. source notes
// who set it (SourceConfig, SourceCLI, SourceDaemon, SourceWeb) for inspection.
func (s *Store) SetActive(service string, active bool, source string) error {
	return s.upsertFlagRow(
		`INSERT INTO monitor_state (service, active, source, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   active     = excluded.active,
		   source     = excluded.source,
		   updated_at = excluded.updated_at;`,
		service, active, source, "set monitor state for "+service)
}

// SetOperationSettling records that a service operation is running or awaiting
// its first post-operation observation cycle.
func (s *Store) SetOperationSettling(service, action, phase, source string) error {
	_, err := s.db.ExecContext(s.sqlCtx(),
		`INSERT INTO operation_settling (service, action, phase, source, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   action     = excluded.action,
		   phase      = excluded.phase,
		   source     = excluded.source,
		   updated_at = excluded.updated_at;`,
		service, action, phase, source, s.now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("set operation settling for %s: %w", service, err)
	}
	return nil
}

// OperationSettling returns a service's current operation-settling row.
func (s *Store) OperationSettling(service string) (OperationSettlingRecord, bool, error) {
	var action, phase, source, updated string
	err := s.db.QueryRowContext(s.sqlCtx(),
		`SELECT action, phase, source, updated_at FROM operation_settling WHERE service = ?;`,
		service,
	).Scan(&action, &phase, &source, &updated)
	switch {
	case err == sql.ErrNoRows:
		return OperationSettlingRecord{}, false, nil
	case err != nil:
		return OperationSettlingRecord{}, false, fmt.Errorf("load operation settling for %s: %w", service, err)
	default:
		at, _ := time.Parse(time.RFC3339, updated)
		return OperationSettlingRecord{Action: action, Phase: phase, Source: source, UpdatedAt: at}, true, nil
	}
}

// ClearOperationSettling removes a service's operation-settling row.
func (s *Store) ClearOperationSettling(service string) error {
	_, err := s.db.ExecContext(s.sqlCtx(), `DELETE FROM operation_settling WHERE service = ?;`, service)
	if err != nil {
		return fmt.Errorf("clear operation settling for %s: %w", service, err)
	}
	return nil
}

// ServiceCheckSnapshots returns every persisted service check snapshot, grouped
// by service name and keyed by check name.
func (s *Store) ServiceCheckSnapshots() (map[string]map[string]CheckSnapshotRecord, error) {
	return s.groupedCheckSnapshots(
		`SELECT service, check_name, check_type, ok, condition, optional, skipped, message, data, ran, at
		   FROM service_check_snapshot ORDER BY service, check_name;`,
		"service check snapshots", scanServiceCheckSnapshot,
	)
}

// SetServiceCheckSnapshots replaces one service's latest check snapshots.
func (s *Store) SetServiceCheckSnapshots(service string, records map[string]CheckSnapshotRecord) error {
	return replaceServiceRows(s, service, `DELETE FROM service_check_snapshot WHERE service = ?;`,
		"service check snapshot", records, func(tx *sql.Tx, name string, rec CheckSnapshotRecord) error {
			data, err := encodeSnapshotData(rec.Data)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(s.sqlCtx(),
				`INSERT INTO service_check_snapshot
				   (service, check_name, check_type, ok, condition, optional, skipped, message, data, ran, at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
				service, name, rec.CheckType, boolInt(rec.OK), boolInt(rec.Condition), boolInt(rec.Optional), boolInt(rec.Skipped),
				rec.Message, data, boolInt(rec.Ran), timeUnixNano(rec.At),
			); err != nil {
				return fmt.Errorf("insert service check snapshot %s/%s: %w", service, name, err)
			}
			return nil
		})
}

// replaceServiceRows swaps one service's rows in a name-keyed table inside a
// transaction: DELETE via deleteSQL, then insert each record in sorted-name
// order. what labels the transaction-step errors ("<what> update",
// "clear <what>s", "commit <what>s"); insert keeps each table's own SQL and
// per-row error context.
func replaceServiceRows[T any](s *Store, service, deleteSQL, what string, records map[string]T, insert func(tx *sql.Tx, name string, rec T) error) error {
	tx, err := s.db.BeginTx(s.sqlCtx(), nil)
	if err != nil {
		return fmt.Errorf("begin %s update for %s: %w", what, service, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(s.sqlCtx(), deleteSQL, service); err != nil {
		return fmt.Errorf("clear %ss for %s: %w", what, service, err)
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := insert(tx, name, records[name]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %ss for %s: %w", what, service, err)
	}
	return nil
}

// WatchCheckSnapshots returns every persisted host-watch snapshot, grouped by
// watch name and keyed by the stable result slot.
func (s *Store) WatchCheckSnapshots() (map[string]map[string]CheckSnapshotRecord, error) {
	return s.groupedCheckSnapshots(
		`SELECT watch, slot, check_type, ok, condition, optional, skipped, message, data, ran, at
		   FROM watch_check_snapshot ORDER BY watch, slot;`,
		"watch check snapshots", scanWatchCheckSnapshot,
	)
}

type checkSnapshotScanner func(*sql.Rows) (group, slot string, record CheckSnapshotRecord, err error)

func (s *Store) groupedCheckSnapshots(query, label string, scan checkSnapshotScanner) (map[string]map[string]CheckSnapshotRecord, error) {
	rows, err := s.db.QueryContext(s.sqlCtx(), query)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", label, err)
	}
	defer rows.Close()

	out := map[string]map[string]CheckSnapshotRecord{}
	for rows.Next() {
		group, slot, record, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if out[group] == nil {
			out[group] = map[string]CheckSnapshotRecord{}
		}
		out[group][slot] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return out, nil
}

func scanServiceCheckSnapshot(rows *sql.Rows) (string, string, CheckSnapshotRecord, error) {
	var (
		service   string
		name      string
		checkType string
		ok        int
		cond      int
		optional  int
		skipped   int
		message   string
		rawData   string
		ran       int
		at        int64
	)
	if err := rows.Scan(&service, &name, &checkType, &ok, &cond, &optional, &skipped, &message, &rawData, &ran, &at); err != nil {
		return "", "", CheckSnapshotRecord{}, fmt.Errorf("scan service check snapshot: %w", err)
	}
	record, err := newCheckSnapshotRecord(name, checkType, ok, cond, optional, skipped, message, rawData, ran, at)
	return service, name, record, err
}

func scanWatchCheckSnapshot(rows *sql.Rows) (string, string, CheckSnapshotRecord, error) {
	var (
		watch     string
		slot      string
		checkType string
		ok        int
		cond      int
		optional  int
		skipped   int
		message   string
		rawData   string
		ran       int
		at        int64
	)
	if err := rows.Scan(&watch, &slot, &checkType, &ok, &cond, &optional, &skipped, &message, &rawData, &ran, &at); err != nil {
		return "", "", CheckSnapshotRecord{}, fmt.Errorf("scan watch check snapshot: %w", err)
	}
	record, err := newCheckSnapshotRecord(slot, checkType, ok, cond, optional, skipped, message, rawData, ran, at)
	return watch, slot, record, err
}

func newCheckSnapshotRecord(name, checkType string, ok, condition, optional, skipped int, message, rawData string, ran int, at int64) (CheckSnapshotRecord, error) {
	data, err := decodeSnapshotData(rawData)
	if err != nil {
		return CheckSnapshotRecord{}, err
	}
	return CheckSnapshotRecord{
		Name: name, CheckType: checkType, OK: intBool(ok), Condition: intBool(condition), Optional: intBool(optional),
		Skipped: intBool(skipped), Message: message, Data: data, Ran: intBool(ran), At: unixNanoTime(at),
	}, nil
}

// SetWatchCheckSnapshot upserts one host-watch snapshot slot.
func (s *Store) SetWatchCheckSnapshot(watch, slot string, rec CheckSnapshotRecord) error {
	data, err := encodeSnapshotData(rec.Data)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(s.sqlCtx(),
		`INSERT INTO watch_check_snapshot
		   (watch, slot, check_type, ok, condition, optional, skipped, message, data, ran, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watch, slot) DO UPDATE SET
		   check_type = excluded.check_type,
		   ok         = excluded.ok,
		   condition  = excluded.condition,
		   optional   = excluded.optional,
		   skipped    = excluded.skipped,
		   message    = excluded.message,
		   data       = excluded.data,
		   ran        = excluded.ran,
		   at         = excluded.at;`,
		watch, slot, rec.CheckType, boolInt(rec.OK), boolInt(rec.Condition), boolInt(rec.Optional), boolInt(rec.Skipped),
		rec.Message, data, boolInt(rec.Ran), timeUnixNano(rec.At),
	)
	if err != nil {
		return fmt.Errorf("set watch check snapshot %s/%s: %w", watch, slot, err)
	}
	return nil
}

func encodeSnapshotData(data map[string]any) (string, error) {
	if data == nil {
		return "{}", nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode check snapshot data: %w", err)
	}
	return string(b), nil
}

func decodeSnapshotData(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil // empty persisted data represents an absent snapshot
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("decode check snapshot data: %w", err)
	}
	return data, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intBool(v int) bool {
	return v != 0
}

// panicFlagKey is the global_state key for the panic-mode toggle.
const panicFlagKey = "panic_mode"

// GlobalRecord is one persisted daemon-wide flag row.
type GlobalRecord struct {
	On        bool
	Source    string
	UpdatedAt time.Time
}

// SetPanic records the daemon-wide panic-mode flag, upserting the row. source
// notes who set it (SourceCLI, SourceWeb) for inspection.
func (s *Store) SetPanic(on bool, source string) error {
	return s.upsertFlagRow(
		`INSERT INTO global_state (key, value, source, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   value      = excluded.value,
		   source     = excluded.source,
		   updated_at = excluded.updated_at;`,
		panicFlagKey, on, source, "set panic mode")
}

// Panic returns the persisted panic-mode flag. found is false when no row has
// been written yet (the caller treats that as panic off).
func (s *Store) Panic() (rec GlobalRecord, found bool, err error) {
	on, source, at, found, err := s.loadFlagRow(
		`SELECT value, source, updated_at FROM global_state WHERE key = ?;`,
		panicFlagKey, "load panic mode")
	if !found || err != nil {
		return GlobalRecord{}, false, err
	}
	return GlobalRecord{On: on, Source: source, UpdatedAt: at}, true, nil
}

// RemediationRecord is the persisted automatic-remediation control state for one
// service.
type RemediationRecord struct {
	LastActionAt   time.Time
	RecentActions  []time.Time
	CurrentBackoff time.Duration
}

// RuleWindowRecord is the persisted for/within progress for one rule.
type RuleWindowRecord struct {
	Consecutive  int
	History      []bool
	TrueSince    time.Time
	TimedHistory []RuleWindowSample
}

// RuleWindowSample is one persisted sample for a duration-based within window.
type RuleWindowSample struct {
	At    time.Time
	Match bool
}

// WatchRuntimeRecord is the durable control state for one watch result slot.
type WatchRuntimeRecord struct {
	Firing       bool
	LastNotifyAt time.Time
	Window       RuleWindowRecord
	Policy       RemediationRecord
}

// WatchRuntimeState returns one watch slot's persisted episode and pacing state.
func (s *Store) WatchRuntimeState(watch, slot string) (WatchRuntimeRecord, bool, error) {
	var (
		firing             int
		lastNotifyAt       int64
		consecutive        int
		rawHistory         string
		trueSince          int64
		rawTimed           string
		lastActionAt       int64
		rawRecentActions   string
		currentBackoffNano int64
	)
	err := s.db.QueryRowContext(s.sqlCtx(),
		`SELECT firing, last_notify_at, consecutive, history, true_since,
		        timed_history, last_action_at, recent_actions, current_backoff_ns
		   FROM watch_runtime_state WHERE watch = ? AND slot = ?;`,
		watch, slot,
	).Scan(
		&firing, &lastNotifyAt, &consecutive, &rawHistory, &trueSince,
		&rawTimed, &lastActionAt, &rawRecentActions, &currentBackoffNano,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return WatchRuntimeRecord{}, false, nil
	case err != nil:
		return WatchRuntimeRecord{}, false, fmt.Errorf("load watch runtime state for %s/%s: %w", watch, slot, err)
	}

	var history []bool
	if err := json.Unmarshal([]byte(rawHistory), &history); err != nil {
		return WatchRuntimeRecord{}, false, fmt.Errorf("decode watch runtime history for %s/%s: %w", watch, slot, err)
	}
	timed, err := decodeRuleWindowSamples(rawTimed)
	if err != nil {
		return WatchRuntimeRecord{}, false, err
	}
	recent, err := decodeUnixNanos(rawRecentActions)
	if err != nil {
		return WatchRuntimeRecord{}, false, err
	}
	return WatchRuntimeRecord{
		Firing:       firing != 0,
		LastNotifyAt: unixNanoTime(lastNotifyAt),
		Window: RuleWindowRecord{
			Consecutive:  consecutive,
			History:      history,
			TrueSince:    unixNanoTime(trueSince),
			TimedHistory: timed,
		},
		Policy: RemediationRecord{
			LastActionAt:   unixNanoTime(lastActionAt),
			RecentActions:  recent,
			CurrentBackoff: time.Duration(currentBackoffNano),
		},
	}, true, nil
}

// SetWatchRuntimeState upserts one watch slot's episode and pacing state. An
// empty record deletes any existing row.
func (s *Store) SetWatchRuntimeState(watch, slot string, rec WatchRuntimeRecord) error {
	if watchRuntimeRecordEmpty(rec) {
		_, err := s.db.ExecContext(s.sqlCtx(), `DELETE FROM watch_runtime_state WHERE watch = ? AND slot = ?;`, watch, slot)
		if err != nil {
			return fmt.Errorf("clear watch runtime state for %s/%s: %w", watch, slot, err)
		}
		return nil
	}
	history, err := json.Marshal(rec.Window.History)
	if err != nil {
		return fmt.Errorf("encode watch runtime history for %s/%s: %w", watch, slot, err)
	}
	timed, err := encodeRuleWindowSamples(rec.Window.TimedHistory)
	if err != nil {
		return err
	}
	recent, err := encodeUnixNanos(rec.Policy.RecentActions)
	if err != nil {
		return err
	}
	firing := 0
	if rec.Firing {
		firing = 1
	}
	_, err = s.db.ExecContext(s.sqlCtx(),
		`INSERT INTO watch_runtime_state (
		   watch, slot, firing, last_notify_at, consecutive, history, true_since,
		   timed_history, last_action_at, recent_actions, current_backoff_ns
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watch, slot) DO UPDATE SET
		   firing             = excluded.firing,
		   last_notify_at      = excluded.last_notify_at,
		   consecutive         = excluded.consecutive,
		   history             = excluded.history,
		   true_since          = excluded.true_since,
		   timed_history       = excluded.timed_history,
		   last_action_at      = excluded.last_action_at,
		   recent_actions      = excluded.recent_actions,
		   current_backoff_ns  = excluded.current_backoff_ns;`,
		watch, slot, firing, timeUnixNano(rec.LastNotifyAt), rec.Window.Consecutive,
		string(history), timeUnixNano(rec.Window.TrueSince), timed,
		timeUnixNano(rec.Policy.LastActionAt), recent, int64(rec.Policy.CurrentBackoff),
	)
	if err != nil {
		return fmt.Errorf("set watch runtime state for %s/%s: %w", watch, slot, err)
	}
	return nil
}

func watchRuntimeRecordEmpty(rec WatchRuntimeRecord) bool {
	return !rec.Firing && rec.LastNotifyAt.IsZero() &&
		rec.Window.Consecutive == 0 && len(rec.Window.History) == 0 &&
		rec.Window.TrueSince.IsZero() && len(rec.Window.TimedHistory) == 0 &&
		rec.Policy.LastActionAt.IsZero() && len(rec.Policy.RecentActions) == 0 &&
		rec.Policy.CurrentBackoff == 0
}

// RemediationState returns a service's persisted automatic-remediation state.
// found is false when no action state has been recorded yet.
func (s *Store) RemediationState(service string) (RemediationRecord, bool, error) {
	var (
		lastActionAt     int64
		recentActions    string
		currentBackoffNS int64
	)
	err := s.db.QueryRowContext(s.sqlCtx(),
		`SELECT last_action_at, recent_actions, current_backoff_ns
		   FROM remediation_state WHERE service = ?;`,
		service,
	).Scan(&lastActionAt, &recentActions, &currentBackoffNS)
	switch {
	case err == sql.ErrNoRows:
		return RemediationRecord{}, false, nil
	case err != nil:
		return RemediationRecord{}, false, fmt.Errorf("load remediation state for %s: %w", service, err)
	default:
		recent, err := decodeUnixNanos(recentActions)
		if err != nil {
			return RemediationRecord{}, false, err
		}
		return RemediationRecord{
			LastActionAt:   unixNanoTime(lastActionAt),
			RecentActions:  recent,
			CurrentBackoff: time.Duration(currentBackoffNS),
		}, true, nil
	}
}

// SetRemediationState upserts a service's automatic-remediation state. An empty
// record deletes any existing row.
func (s *Store) SetRemediationState(service string, rec RemediationRecord) error {
	if rec.LastActionAt.IsZero() && len(rec.RecentActions) == 0 && rec.CurrentBackoff == 0 {
		_, err := s.db.ExecContext(s.sqlCtx(), `DELETE FROM remediation_state WHERE service = ?;`, service)
		if err != nil {
			return fmt.Errorf("clear remediation state for %s: %w", service, err)
		}
		return nil
	}
	recent, err := encodeUnixNanos(rec.RecentActions)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(s.sqlCtx(),
		`INSERT INTO remediation_state (service, last_action_at, recent_actions, current_backoff_ns)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   last_action_at     = excluded.last_action_at,
		   recent_actions     = excluded.recent_actions,
		   current_backoff_ns = excluded.current_backoff_ns;`,
		service, timeUnixNano(rec.LastActionAt), recent, int64(rec.CurrentBackoff),
	)
	if err != nil {
		return fmt.Errorf("set remediation state for %s: %w", service, err)
	}
	return nil
}

// RuleWindowStates returns the persisted for/within progress for a service's
// rules, keyed by rule name.
func (s *Store) RuleWindowStates(service string) (map[string]RuleWindowRecord, error) {
	rows, err := s.db.QueryContext(s.sqlCtx(),
		`SELECT rule_name, consecutive, history, true_since, timed_history
		   FROM rule_window_state WHERE service = ? ORDER BY rule_name;`,
		service,
	)
	if err != nil {
		return nil, fmt.Errorf("load rule window states for %s: %w", service, err)
	}
	defer rows.Close()

	out := map[string]RuleWindowRecord{}
	for rows.Next() {
		var (
			name        string
			consecutive int
			rawHistory  string
			trueSince   int64
			rawTimed    string
		)
		if err := rows.Scan(&name, &consecutive, &rawHistory, &trueSince, &rawTimed); err != nil {
			return nil, fmt.Errorf("scan rule window state for %s: %w", service, err)
		}
		var history []bool
		if err := json.Unmarshal([]byte(rawHistory), &history); err != nil {
			return nil, fmt.Errorf("decode rule window history for %s/%s: %w", service, name, err)
		}
		timed, err := decodeRuleWindowSamples(rawTimed)
		if err != nil {
			return nil, err
		}
		out[name] = RuleWindowRecord{
			Consecutive:  consecutive,
			History:      history,
			TrueSince:    unixNanoTime(trueSince),
			TimedHistory: timed,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rule window states for %s: %w", service, err)
	}
	return out, nil
}

// SetRuleWindowStates replaces the persisted rule-window state for a service.
// Passing an empty map removes stale rows for rules that no longer exist.
func (s *Store) SetRuleWindowStates(service string, records map[string]RuleWindowRecord) error {
	return replaceServiceRows(s, service, `DELETE FROM rule_window_state WHERE service = ?;`,
		"rule window state", records, func(tx *sql.Tx, name string, rec RuleWindowRecord) error {
			history, err := json.Marshal(rec.History)
			if err != nil {
				return fmt.Errorf("encode rule window history for %s/%s: %w", service, name, err)
			}
			timed, err := encodeRuleWindowSamples(rec.TimedHistory)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(s.sqlCtx(),
				`INSERT INTO rule_window_state (service, rule_name, consecutive, history, true_since, timed_history)
				 VALUES (?, ?, ?, ?, ?, ?);`,
				service, name, rec.Consecutive, string(history), timeUnixNano(rec.TrueSince), timed,
			); err != nil {
				return fmt.Errorf("insert rule window state for %s/%s: %w", service, name, err)
			}
			return nil
		})
}

func encodeUnixNanos(times []time.Time) (string, error) {
	nanos := make([]int64, 0, len(times))
	for _, t := range times {
		if !t.IsZero() {
			nanos = append(nanos, t.UTC().UnixNano())
		}
	}
	b, err := json.Marshal(nanos)
	if err != nil {
		return "", fmt.Errorf("encode unix nanos: %w", err)
	}
	return string(b), nil
}

func decodeUnixNanos(raw string) ([]time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	var nanos []int64
	if err := json.Unmarshal([]byte(raw), &nanos); err != nil {
		return nil, fmt.Errorf("decode unix nanos: %w", err)
	}
	out := make([]time.Time, 0, len(nanos))
	for _, n := range nanos {
		if n != 0 {
			out = append(out, time.Unix(0, n).UTC())
		}
	}
	return out, nil
}

type ruleWindowSampleJSON struct {
	At    int64 `json:"at"`
	Match bool  `json:"match"`
}

func encodeRuleWindowSamples(samples []RuleWindowSample) (string, error) {
	raw := make([]ruleWindowSampleJSON, 0, len(samples))
	for _, sample := range samples {
		if sample.At.IsZero() {
			continue
		}
		raw = append(raw, ruleWindowSampleJSON{At: sample.At.UTC().UnixNano(), Match: sample.Match})
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encode rule window samples: %w", err)
	}
	return string(b), nil
}

func decodeRuleWindowSamples(raw string) ([]RuleWindowSample, error) {
	if raw == "" {
		return nil, nil
	}
	var encoded []ruleWindowSampleJSON
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("decode rule window samples: %w", err)
	}
	out := make([]RuleWindowSample, 0, len(encoded))
	for _, sample := range encoded {
		if sample.At == 0 {
			continue
		}
		out = append(out, RuleWindowSample{At: time.Unix(0, sample.At).UTC(), Match: sample.Match})
	}
	return out, nil
}

func timeUnixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

func unixNanoTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// EventRecord is one persisted operator-visible event. Service is set for
// service events, Watch for host-watch events; both are empty only for daemon-wide
// events such as config reload failures.
type EventRecord struct {
	ID      int64
	At      time.Time
	Service string
	Watch   string
	App     string
	Kind    string
	Rule    string
	Action  string
	Status  string
	Message string
	Output  string
}

// RecordEvent appends one event to the persistent event/activity feed.
func (s *Store) RecordEvent(e EventRecord) (int64, error) {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	result, err := s.db.ExecContext(s.sqlCtx(),
		`INSERT INTO event_log (at, service, watch, app, kind, rule, action, status, message, output)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		at.UTC().UnixNano(), e.Service, e.Watch, e.App, e.Kind, e.Rule, e.Action, e.Status, e.Message, e.Output,
	)
	if err != nil {
		return 0, fmt.Errorf("insert event log row: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read event log last insert id: %w", err)
	}
	return id, nil
}

// RecentEvents returns the newest persisted events first. limit <= 0 returns all
// persisted events.
func (s *Store) RecentEvents(limit int) ([]EventRecord, error) {
	return s.RecentEventsBefore(0, limit)
}

// RecentEventsBefore returns persisted events newest first. beforeID <= 0
// starts at the newest event; otherwise only rows with a smaller ID are read.
func (s *Store) RecentEventsBefore(beforeID int64, limit int) ([]EventRecord, error) {
	if limit <= 0 {
		limit = -1
	}
	query := `SELECT id, at, service, watch, app, kind, rule, action, status, message, output
		   FROM event_log`
	args := make([]any, 0, eventQueryMaxArgs)
	if beforeID > 0 {
		query += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?;`
	args = append(args, limit)
	rows, err := s.db.QueryContext(s.sqlCtx(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("load recent events: %w", err)
	}
	defer rows.Close()

	var out []EventRecord
	for rows.Next() {
		var rec EventRecord
		var at int64
		if err := rows.Scan(&rec.ID, &at, &rec.Service, &rec.Watch, &rec.App, &rec.Kind, &rec.Rule, &rec.Action, &rec.Status, &rec.Message, &rec.Output); err != nil {
			return nil, fmt.Errorf("scan event log row: %w", err)
		}
		rec.At = time.Unix(0, at).UTC()
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event log rows: %w", err)
	}
	return out, nil
}

// PruneEvents deletes event rows older than before. If before is zero, every
// persisted event is deleted.
func (s *Store) PruneEvents(before time.Time) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if before.IsZero() {
		res, err = s.db.ExecContext(s.sqlCtx(), `DELETE FROM event_log;`)
	} else {
		res, err = s.db.ExecContext(s.sqlCtx(), `DELETE FROM event_log WHERE at < ?;`, before.UTC().UnixNano())
	}
	if err != nil {
		return 0, fmt.Errorf("prune event log: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned event log row count: %w", err)
	}
	return n, nil
}

// SLAWindow names a rolling availability window and its length. The windows are
// rolling (ending "now"), so week/month/year use fixed 7/30/365-day spans rather
// than calendar boundaries. Segments is how many equal sub-spans the window is
// split into for the web timeline strip (a status-page style availability band).
type SLAWindow struct {
	Name     string
	Span     time.Duration
	Segments int
}

const (
	slaWindowHour  = "hour"
	slaWindowDay   = "day"
	slaWindowWeek  = "week"
	slaWindowMonth = "month"
	slaWindowYear  = "year"

	slaRollingWeekDays  = 7
	slaRollingMonthDays = 30
	slaRollingYearDays  = 365

	slaSpanDay   = hoursPerDay * time.Hour
	slaSpanWeek  = slaRollingWeekDays * slaSpanDay
	slaSpanMonth = slaRollingMonthDays * slaSpanDay
	slaSpanYear  = slaRollingYearDays * slaSpanDay

	slaSegmentsHour  = 12
	slaSegmentsDay   = 24
	slaSegmentsWeek  = 28
	slaSegmentsMonth = 30
	slaSegmentsYear  = 12

	slaTimelineFixedQueryArgs = 4
)

// SLAWindows are the reported rolling windows, shortest first. Segment counts
// pick a natural human sub-span per window (5-minute, hourly, 6-hourly, daily,
// monthly) so each timeline cell reads as a meaningful slice of time.
var SLAWindows = []SLAWindow{
	{slaWindowHour, time.Hour, slaSegmentsHour},
	{slaWindowDay, slaSpanDay, slaSegmentsDay},
	{slaWindowWeek, slaSpanWeek, slaSegmentsWeek},
	{slaWindowMonth, slaSpanMonth, slaSegmentsMonth},
	{slaWindowYear, slaSpanYear, slaSegmentsYear},
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

type slaQueries struct {
	record string
	sum    string
	series string
}

var serviceSLAQueries = slaQueries{
	record: `INSERT INTO sla_sample (service, bucket, up_count, total_count)
		 VALUES (?, ?, ?, 1)
		 ON CONFLICT(service, bucket) DO UPDATE SET
		   up_count    = up_count + excluded.up_count,
		   total_count = total_count + excluded.total_count;`,
	sum: `SELECT COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)
		  FROM sla_sample WHERE service = ? AND bucket >= ?;`,
	series: `SELECT bucket, up_count, total_count
		   FROM sla_sample
		  WHERE service = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
}

var checkSLAQueries = slaQueries{
	record: `INSERT INTO check_sla_sample (service, check_name, bucket, up_count, total_count)
		 VALUES (?, ?, ?, ?, 1)
		 ON CONFLICT(service, check_name, bucket) DO UPDATE SET
		   up_count    = up_count + excluded.up_count,
		   total_count = total_count + excluded.total_count;`,
	sum: `SELECT COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)
		  FROM check_sla_sample WHERE service = ? AND check_name = ? AND bucket >= ?;`,
	series: `SELECT bucket, up_count, total_count
		   FROM check_sla_sample
		  WHERE service = ? AND check_name = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
}

// RecordSLA accumulates one observed monitoring cycle into a service's current
// UTC-minute bucket: total_count +1, and up_count +1 when up. Paused or
// unobserved cycles are simply never recorded, so they do not count as downtime.
func (s *Store) RecordSLA(service string, up bool, at time.Time) error {
	return s.recordSLABucket(serviceSLAQueries.record, []any{service}, up, at, "SLA", service)
}

// RecordCheckSLA accumulates one observed check execution into its current
// UTC-minute bucket. Interval-deferred checks are not recorded by callers, so
// the per-check SLA reflects only real check runs.
func (s *Store) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	return s.recordSLABucket(checkSLAQueries.record, []any{service, check}, up, at, "check SLA", service+"/"+check)
}

// SLA sums a service's up and total observed cycles over the rolling window
// ending at now (buckets with start >= now-span). total==0 means no data.
func (s *Store) SLA(service string, span time.Duration, now time.Time) (up, total int64, err error) {
	return s.sumSLA(serviceSLAQueries.sum, []any{service}, span, now, "SLA", service)
}

// CheckSLA sums one check's up and total observed executions over the rolling
// window ending at now. total==0 means no data.
func (s *Store) CheckSLA(service, check string, span time.Duration, now time.Time) (up, total int64, err error) {
	return s.sumSLA(checkSLAQueries.sum, []any{service, check}, span, now, "check SLA", service+"/"+check)
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
	return s.loadSLASeries(serviceSLAQueries.series, []any{service}, from, to, "SLA", service)
}

// CheckSLASeries returns one check's per-minute availability points in [from,
// to), oldest first. Unobserved minutes are absent.
func (s *Store) CheckSLASeries(service, check string, from, to time.Time) ([]SLAPoint, error) {
	return s.loadSLASeries(checkSLAQueries.series, []any{service, check}, from, to, "check SLA", service+"/"+check)
}

func (s *Store) recordSLABucket(query string, keys []any, up bool, at time.Time, kind, target string) error {
	upCount := 0
	if up {
		upCount = 1
	}
	args := append(append([]any{}, keys...), minuteBucket(at), upCount)
	if _, err := s.db.ExecContext(s.sqlCtx(), query, args...); err != nil {
		return fmt.Errorf("record %s for %s: %w", kind, target, err)
	}
	return nil
}

func (s *Store) sumSLA(query string, keys []any, span time.Duration, now time.Time, kind, target string) (up, total int64, err error) {
	args := append(append([]any{}, keys...), minuteBucket(now.Add(-span)))
	err = s.db.QueryRowContext(s.sqlCtx(), query, args...).Scan(&up, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("sum %s for %s: %w", kind, target, err)
	}
	return up, total, nil
}

func (s *Store) loadSLASeries(query string, keys []any, from, to time.Time, kind, target string) ([]SLAPoint, error) {
	args := append(append([]any{}, keys...), minuteBucket(from), minuteBucket(to))
	rows, err := s.db.QueryContext(s.sqlCtx(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("load %s series for %s: %w", kind, target, err)
	}
	defer rows.Close()

	var out []SLAPoint
	for rows.Next() {
		var bucket, up, total int64
		if err := rows.Scan(&bucket, &up, &total); err != nil {
			return nil, fmt.Errorf("scan %s series row for %s: %w", kind, target, err)
		}
		out = append(out, SLAPoint{Start: time.Unix(bucket, 0).UTC(), Up: up, Total: total})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s series for %s: %w", kind, target, err)
	}
	return out, nil
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

// CheckSLAReport returns one check's availability across every SLAWindow,
// ordered as SLAWindows (hour..year).
func (s *Store) CheckSLAReport(service, check string, now time.Time) ([]SLAValue, error) {
	out := make([]SLAValue, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		up, total, err := s.CheckSLA(service, check, w.Span, now)
		if err != nil {
			return nil, err
		}
		out = append(out, SLAValue{Window: w.Name, Up: up, Total: total})
	}
	return out, nil
}

// SLASegment is one equal sub-span of a windowed SLA timeline: the up and total
// observed cycles within it. Total==0 marks a gap (an unmonitored sub-span),
// which renders distinctly from downtime — the same gap convention as SLASeries.
type SLASegment struct {
	Up    int64 `json:"up"`
	Total int64 `json:"total"`
}

// SLAWindowTimeline is a service's availability over one rolling window plus the
// window divided into equal sub-spans (oldest first) for the web timeline strip.
// Up/Total are the window totals (the sum of the segments), so a caller rendering
// the strip needs no separate SLAReport query.
type SLAWindowTimeline struct {
	Window   string
	Up       int64
	Total    int64
	Segments []SLASegment
}

// SLATimelines returns a service's availability for every SLAWindow split into
// equal sub-spans for the web timeline strip, ordered as SLAWindows (hour..year).
func (s *Store) SLATimelines(service string, now time.Time) ([]SLAWindowTimeline, error) {
	return s.slaTimelines(slaTimelineQuery, []any{service}, now)
}

// CheckSLATimelines returns one check's windowed availability split into sub-spans
// for the web timeline strip, ordered as SLAWindows (hour..year).
func (s *Store) CheckSLATimelines(service, check string, now time.Time) ([]SLAWindowTimeline, error) {
	return s.slaTimelines(checkSLATimelineQuery, []any{service, check}, now)
}

const (
	slaTimelineQuery = `SELECT (bucket - ?)/? AS seg, COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)
			   FROM sla_sample
			  WHERE service = ? AND bucket >= ? AND bucket < ?
			  GROUP BY seg ORDER BY seg;`
	checkSLATimelineQuery = `SELECT (bucket - ?)/? AS seg, COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)
			   FROM check_sla_sample
			  WHERE service = ? AND check_name = ? AND bucket >= ? AND bucket < ?
			  GROUP BY seg ORDER BY seg;`
)

// slaTimelines divides each SLAWindow into Segments equal sub-spans ending at
// now and aggregates the per-minute buckets with a single grouped query per
// window — the same indexed range scan SLA() already does, returning the
// per-segment breakdown as well. query is one of the package constants above;
// keyArgs select the service (and check) rows.
func (s *Store) slaTimelines(query string, keyArgs []any, now time.Time) ([]SLAWindowTimeline, error) {
	out := make([]SLAWindowTimeline, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		segCount := w.Segments
		if segCount <= 0 {
			segCount = 1
		}
		spanSec := int64(w.Span / time.Second)
		startBucket := minuteBucket(now.Add(-w.Span))
		// Include the current (partial) minute so the window total matches SLA(),
		// which lower-bounds on the same start bucket but has no upper bound. Using
		// startBucket+spanSec would stop one bucket short and exclude the current
		// minute, making SLAWindowTimeline.Up/Total disagree with SLAReport for the
		// same window. The current minute clamps into the last segment below.
		endBucket := minuteBucket(now) + secondsPerMinute
		segSpan := spanSec / int64(segCount)
		if segSpan <= 0 {
			segSpan = 1
		}

		// Placeholder order matches the SQL left-to-right: the SELECT segment
		// expression (start, span) first, then the WHERE key args, then the range.
		args := make([]any, 0, len(keyArgs)+slaTimelineFixedQueryArgs)
		args = append(args, startBucket, segSpan)
		args = append(args, keyArgs...)
		args = append(args, startBucket, endBucket)
		rows, err := s.db.QueryContext(s.sqlCtx(), query, args...)
		if err != nil {
			return nil, fmt.Errorf("load SLA timeline for %s: %w", w.Name, err)
		}

		segs := make([]SLASegment, segCount)
		var winUp, winTotal int64
		for rows.Next() {
			var seg, up, total int64
			if err := rows.Scan(&seg, &up, &total); err != nil {
				//nolint:sqlclosecheck // this loop opens rows per SLA window; defer would retain every cursor until return.
				rows.Close()
				return nil, fmt.Errorf("scan SLA timeline row for %s: %w", w.Name, err)
			}
			if seg < 0 {
				seg = 0
			} else if seg >= int64(segCount) {
				seg = int64(segCount) - 1
			}
			segs[seg].Up += up
			segs[seg].Total += total
			winUp += up
			winTotal += total
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate SLA timeline for %s: %w", w.Name, err)
		}
		rows.Close()

		out = append(out, SLAWindowTimeline{Window: w.Name, Up: winUp, Total: winTotal, Segments: segs})
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

type aggregateRecordSpec struct {
	query, kind, targetPrefix string
}

var (
	measurementRecordSpec = aggregateRecordSpec{
		query: `INSERT INTO measurement (service, check_name, bucket, n, sum_ms, min_ms, max_ms)
		 VALUES (?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(service, check_name, bucket) DO UPDATE SET
		   n      = n + 1,
		   sum_ms = sum_ms + excluded.sum_ms,
		   min_ms = min(min_ms, excluded.min_ms),
		   max_ms = max(max_ms, excluded.max_ms);`,
		kind: "measurement", targetPrefix: " for ",
	}
	metricRecordSpec = aggregateRecordSpec{
		query: `INSERT INTO measurement_metric (service, check_name, metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(service, check_name, metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`,
		kind: "metric", targetPrefix: " for ",
	}
	daemonMetricRecordSpec = aggregateRecordSpec{
		query: `INSERT INTO daemon_metric (metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, 1, ?, ?, ?)
		 ON CONFLICT(metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`,
		kind: "daemon metric", targetPrefix: " ",
	}
	serviceMetricRecordSpec = aggregateRecordSpec{
		query: `INSERT INTO service_metric (service, metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(service, metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`,
		kind: "service metric", targetPrefix: " for ",
	}
)

func (s *Store) recordAggregate(spec aggregateRecordSpec, first, second, third string, values ...any) error {
	if _, err := s.db.ExecContext(s.sqlCtx(), spec.query, values...); err != nil {
		return fmt.Errorf("record %s%s%s: %w", spec.kind, spec.targetPrefix, aggregateRecordTarget(first, second, third), err)
	}
	return nil
}

func aggregateRecordTarget(first, second, third string) string {
	if third != "" {
		return first + "/" + second + "/" + third
	}
	if second != "" {
		return first + "/" + second
	}
	return first
}

// RecordMeasurement accumulates one numeric observation (milliseconds) for a
// service+check into its current UTC-minute bucket: n+1, sum+value, and the
// running min/max.
func (s *Store) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	return s.recordAggregate(measurementRecordSpec, service, check, "", service, check, minuteBucket(at), valueMs, valueMs, valueMs)
}

// MeasurementSummary returns the average/min/max and sample count for a check over
// the rolling window ending at now (buckets with start >= now-span).
func (s *Store) MeasurementSummary(service, check string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRowContext(s.sqlCtx(),
		`SELECT COALESCE(SUM(n),0), SUM(sum_ms), MIN(min_ms), MAX(max_ms)
		   FROM measurement WHERE service = ? AND check_name = ? AND bucket >= ?;`,
		service, check, minuteBucket(now.Add(-span))))
}

// summaryFromRow scans the COALESCE(SUM(n),0), SUM, MIN, MAX aggregate row
// shared by the measurement and metric summaries into a MeasurementStat (avg =
// sum/count, guarded against an empty bucket set), so both summaries express
// only their differing query.
func summaryFromRow(row *sql.Row) (MeasurementStat, error) {
	var count sql.NullInt64
	var sum, minV, maxV sql.NullFloat64
	if err := row.Scan(&count, &sum, &minV, &maxV); err != nil {
		return MeasurementStat{}, fmt.Errorf("scan measurement summary: %w", err)
	}
	stat := MeasurementStat{Count: count.Int64}
	if count.Int64 > 0 && sum.Valid {
		stat.Avg = sum.Float64 / float64(count.Int64)
		stat.Min = minV.Float64
		stat.Max = maxV.Float64
	}
	return stat, nil
}

// MeasurementSeries returns a check's per-minute points in [from, to), oldest
// first. Minutes with no observation are absent (gaps), as in SLASeries.
func (s *Store) MeasurementSeries(service, check string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.aggregateSeries(
		`SELECT bucket, n, sum_ms, min_ms, max_ms
		   FROM measurement
		  WHERE service = ? AND check_name = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		"measurement", service+"/"+check,
		service, check, minuteBucket(from), minuteBucket(to),
	)
}

// PruneMeasurements deletes measurement buckets older than before. Returns rows removed.
func (s *Store) PruneMeasurements(before time.Time) (int64, error) {
	return s.pruneBuckets(stateTableMeasurement, before)
}

// pruneBuckets deletes rows with a bucket older than before from one of the
// per-minute bucket tables. table is always a compile-time literal, never
// operator input.
func (s *Store) pruneBuckets(table string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(s.sqlCtx(), `DELETE FROM `+table+` WHERE bucket < ?;`, minuteBucket(before)) //nolint:gosec // table is a package-internal literal
	if err != nil {
		return 0, fmt.Errorf("prune %s buckets before %s: %w", table, before.UTC().Format(time.RFC3339), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned %s row count: %w", table, err)
	}
	return n, nil
}

// RecordMetric accumulates one observation of a named per-check metric (e.g.
// hdparm "read" MB/s) into its current UTC-minute bucket: n+1, sum+value and the
// running min/max. It is the generic counterpart of RecordMeasurement (latency).
func (s *Store) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	return s.recordAggregate(metricRecordSpec, service, check, metric, service, check, metric, minuteBucket(at), value, value, value)
}

// MetricSummary returns a named metric's average/min/max and sample count over the
// rolling window ending at now.
func (s *Store) MetricSummary(service, check, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRowContext(s.sqlCtx(),
		`SELECT COALESCE(SUM(n),0), SUM(sum_v), MIN(min_v), MAX(max_v)
		   FROM measurement_metric WHERE service = ? AND check_name = ? AND metric = ? AND bucket >= ?;`,
		service, check, metric, minuteBucket(now.Add(-span))))
}

// MetricSeries returns a named metric's per-minute points in [from, to), oldest
// first (minutes with no observation are absent).
func (s *Store) MetricSeries(service, check, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.aggregateSeries(
		`SELECT bucket, n, sum_v, min_v, max_v
		   FROM measurement_metric
		  WHERE service = ? AND check_name = ? AND metric = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		"metric", service+"/"+check+"/"+metric,
		service, check, metric, minuteBucket(from), minuteBucket(to),
	)
}

// PruneMetrics deletes named-metric buckets older than before. Returns rows removed.
func (s *Store) PruneMetrics(before time.Time) (int64, error) {
	return s.pruneBuckets(stateTableMeasurementMetric, before)
}

// RecordDaemonMetric accumulates one sermod process metric observation into its
// current UTC-minute bucket: n+1, sum+value and running min/max.
func (s *Store) RecordDaemonMetric(metric string, value float64, at time.Time) error {
	return s.recordAggregate(daemonMetricRecordSpec, metric, "", "", metric, minuteBucket(at), value, value, value)
}

// DaemonMetricSummary returns a daemon metric's average/min/max and sample count
// over the rolling window ending at now.
func (s *Store) DaemonMetricSummary(metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRowContext(s.sqlCtx(),
		`SELECT COALESCE(SUM(n),0), SUM(sum_v), MIN(min_v), MAX(max_v)
		   FROM daemon_metric WHERE metric = ? AND bucket >= ?;`,
		metric, minuteBucket(now.Add(-span))))
}

// DaemonMetricSeries returns a daemon metric's per-minute points in [from, to),
// oldest first.
func (s *Store) DaemonMetricSeries(metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.aggregateSeries(
		`SELECT bucket, n, sum_v, min_v, max_v
		   FROM daemon_metric
		  WHERE metric = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		"daemon metric", metric,
		metric, minuteBucket(from), minuteBucket(to),
	)
}

// PruneDaemonMetrics deletes daemon metric buckets older than before. Returns rows removed.
func (s *Store) PruneDaemonMetrics(before time.Time) (int64, error) {
	return s.pruneBuckets(stateTableDaemonMetric, before)
}

// RecordServiceMetric accumulates one service process-tree metric observation
// into its current UTC-minute bucket: n+1, sum+value and running min/max.
func (s *Store) RecordServiceMetric(service, metric string, value float64, at time.Time) error {
	return s.recordAggregate(serviceMetricRecordSpec, service, metric, "", service, metric, minuteBucket(at), value, value, value)
}

// ServiceMetricSummary returns a service runtime metric's average/min/max and
// sample count over the rolling window ending at now.
func (s *Store) ServiceMetricSummary(service, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRowContext(s.sqlCtx(),
		`SELECT COALESCE(SUM(n),0), SUM(sum_v), MIN(min_v), MAX(max_v)
		   FROM service_metric WHERE service = ? AND metric = ? AND bucket >= ?;`,
		service, metric, minuteBucket(now.Add(-span))))
}

// ServiceMetricSeries returns a service runtime metric's per-minute points in
// [from, to), oldest first.
func (s *Store) ServiceMetricSeries(service, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.aggregateSeries(
		`SELECT bucket, n, sum_v, min_v, max_v
		   FROM service_metric
		  WHERE service = ? AND metric = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		"service metric", service+"/"+metric,
		service, metric, minuteBucket(from), minuteBucket(to),
	)

}

// aggregateSeries executes a static aggregate-series query and converts its
// minute buckets. query is always a package literal; values remain bound query
// parameters. kind and target preserve the callers' load, scan and iteration
// context.
func (s *Store) aggregateSeries(query, kind, target string, args ...any) ([]MeasurementPoint, error) {
	description := kind + " series for " + target
	rows, err := s.db.QueryContext(s.sqlCtx(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", description, err)
	}
	return measurementPointsFromRows(
		rows,
		kind+" series row for "+target,
		description,
	)
}

// measurementPointsFromRows scans per-minute aggregate rows shared by every
// metric history table. The callers keep their distinct SQL and error context.
func measurementPointsFromRows(rows *sql.Rows, scanContext, iterateContext string) ([]MeasurementPoint, error) {
	defer rows.Close()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minValue, maxValue float64
		if err := rows.Scan(&bucket, &n, &sum, &minValue, &maxValue); err != nil {
			return nil, fmt.Errorf("scan %s: %w", scanContext, err)
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minValue, Max: maxValue})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", iterateContext, err)
	}
	return out, nil
}

// PruneServiceMetrics deletes service runtime metric buckets older than before.
// Returns rows removed.
func (s *Store) PruneServiceMetrics(before time.Time) (int64, error) {
	return s.pruneBuckets(stateTableServiceMetric, before)
}

// PruneSLA deletes SLA buckets older than before, bounding the table to roughly
// one year of per-minute samples per service. Returns the rows removed.
func (s *Store) PruneSLA(before time.Time) (int64, error) {
	total, err := s.pruneBuckets(stateTableSLASample, before)
	if err != nil {
		return 0, err
	}
	rows, err := s.pruneBuckets(stateTableCheckSLASample, before)
	if err != nil {
		return 0, err
	}
	return total + rows, nil
}

// PruneHistory deletes old history from SLA, measurement, daemon runtime,
// service runtime metric and event tables.
func (s *Store) PruneHistory(before time.Time) (PruneHistoryResult, error) {
	var out PruneHistoryResult
	var err error
	if out.SLA, err = s.PruneSLA(before); err != nil {
		return PruneHistoryResult{}, err
	}
	if out.Measurements, err = s.PruneMeasurements(before); err != nil {
		return PruneHistoryResult{}, err
	}
	if out.Metrics, err = s.PruneMetrics(before); err != nil {
		return PruneHistoryResult{}, err
	}
	if out.DaemonMetrics, err = s.PruneDaemonMetrics(before); err != nil {
		return PruneHistoryResult{}, err
	}
	if out.ServiceMetrics, err = s.PruneServiceMetrics(before); err != nil {
		return PruneHistoryResult{}, err
	}
	if out.Events, err = s.PruneEvents(before); err != nil {
		return PruneHistoryResult{}, err
	}
	out.Rows = out.SLA + out.Measurements + out.Metrics + out.DaemonMetrics + out.ServiceMetrics + out.Events
	return out, nil
}

// Compact checkpoints the WAL and vacuums the SQLite state database so space
// freed by pruning can be returned to the filesystem.
func (s *Store) Compact(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return fmt.Errorf("checkpoint state db WAL: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM;`); err != nil {
		return fmt.Errorf("vacuum state db: %w", err)
	}
	return nil
}
