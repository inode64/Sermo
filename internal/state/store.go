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
// The store creates its current schema on open. The driver is modernc.org/sqlite
// — pure Go, no CGO — to keep cross-compilation simple.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"sermo/internal/checks"
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

// storageSchema defines the complete current storage layout.
var storageSchema = []string{
	`CREATE TABLE IF NOT EXISTS monitor_state (
		service    TEXT PRIMARY KEY,
		active     INTEGER NOT NULL,
		source     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// event_log stores the operator-visible event/activity feed. Unlike the
	// runtime ring in sermod, this table survives daemon restarts so the web UI
	// and per-service detail panes can repopulate their recent history.
	`CREATE TABLE IF NOT EXISTS event_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			at      INTEGER NOT NULL,
			service TEXT NOT NULL DEFAULT '',
			watch   TEXT NOT NULL DEFAULT '',
			kind    TEXT NOT NULL DEFAULT '',
			rule    TEXT NOT NULL DEFAULT '',
			action  TEXT NOT NULL DEFAULT '',
			status  TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			app     TEXT NOT NULL DEFAULT '',
			output  TEXT NOT NULL DEFAULT ''
		);`,
	`CREATE INDEX IF NOT EXISTS event_log_at_idx ON event_log (at DESC, id DESC);`,
	`CREATE INDEX IF NOT EXISTS event_log_service_at_idx ON event_log (service, at DESC, id DESC);`,
	// remediation_state stores automatic remediation cooldown, rate-limit and
	// backoff state per service. It is control state, not historical metrics, so
	// daemon restarts must not reset when a rule may act again.
	`CREATE TABLE IF NOT EXISTS remediation_state (
		service            TEXT PRIMARY KEY,
		last_action_at     INTEGER NOT NULL DEFAULT 0,
		recent_actions     TEXT NOT NULL DEFAULT '[]',
		current_backoff_ns INTEGER NOT NULL DEFAULT 0
	);`,
	// rule_window_state stores each service rule's for/within progress so
	// restarting sermod does not make a pending rule start counting from zero.
	`CREATE TABLE IF NOT EXISTS rule_window_state (
		service     TEXT NOT NULL,
		rule_name   TEXT NOT NULL,
		consecutive INTEGER NOT NULL DEFAULT 0,
		history     TEXT NOT NULL DEFAULT '[]',
		true_since        INTEGER NOT NULL DEFAULT 0,
		timed_history     TEXT NOT NULL DEFAULT '[]',
		firing            INTEGER NOT NULL DEFAULT 0,
		clear_since       INTEGER NOT NULL DEFAULT 0,
		clear_consecutive INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (service, rule_name)
	);`,
	// global_state holds daemon-wide on/off flags that are not keyed by service
	// (currently the "panic_mode" toggle). It is control state, not metrics, so it
	// survives daemon restarts — clearing panic mode must be a deliberate act.
	`CREATE TABLE IF NOT EXISTS global_state (
		key        TEXT PRIMARY KEY,
		value      INTEGER NOT NULL,
		source     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS event_log_app_at_idx ON event_log (app, at DESC, id DESC);`,
	// operation_settling suppresses service rules/alerts around manual or
	// automatic service operations. A row starts in phase "running" while the
	// operation is in progress, then moves to "settling" after a successful
	// relaunch until the worker has observed one active check cycle.
	`CREATE TABLE IF NOT EXISTS operation_settling (
		service    TEXT PRIMARY KEY,
		action     TEXT NOT NULL,
		phase      TEXT NOT NULL,
		source     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// service_restart_notice stores the principal process identity last observed
	// under the restart-notice uptime threshold. It is deliberately durable so a
	// sermod restart cannot repeat an already-delivered external-restart alert.
	`CREATE TABLE IF NOT EXISTS service_restart_notice (
		service    TEXT PRIMARY KEY,
		pid        INTEGER NOT NULL,
		started_at TEXT NOT NULL
	);`,
	// service_check_snapshot stores the latest service check result published by
	// each worker. It is current observable state, not history, so the web UI can
	// show the last real daemon-cycle reading immediately after a restart.
	`CREATE TABLE IF NOT EXISTS service_check_snapshot (
		service    TEXT NOT NULL,
		check_name TEXT NOT NULL,
		ok         INTEGER NOT NULL,
		condition  INTEGER NOT NULL,
		optional   INTEGER NOT NULL,
		skipped    INTEGER NOT NULL,
		unavailable INTEGER NOT NULL,
		observation TEXT NOT NULL,
		message    TEXT NOT NULL,
		data       TEXT NOT NULL,
		ran        INTEGER NOT NULL,
		at         INTEGER NOT NULL,
		check_type TEXT NOT NULL,
		PRIMARY KEY (service, check_name)
	);`,
	// watch_check_snapshot stores the latest host-watch result per visible slot
	// (for example one slot per metric). It keeps /api/watches backed by daemon
	// cycle data across process restarts.
	`CREATE TABLE IF NOT EXISTS watch_check_snapshot (
		watch      TEXT NOT NULL,
		slot       TEXT NOT NULL,
		check_type TEXT NOT NULL,
		ok         INTEGER NOT NULL,
		condition  INTEGER NOT NULL,
		optional   INTEGER NOT NULL,
		skipped    INTEGER NOT NULL,
		unavailable INTEGER NOT NULL,
		observation TEXT NOT NULL,
		message    TEXT NOT NULL,
		data       TEXT NOT NULL,
		ran        INTEGER NOT NULL,
		at         INTEGER NOT NULL,
		PRIMARY KEY (watch, slot)
	);`,
	// watch_runtime_state persists one watch slot's firing episode, notification
	// pacing, condition window and automatic-action policy state. This prevents a
	// daemon restart from turning an unchanged condition into a new episode.
	`CREATE TABLE IF NOT EXISTS watch_runtime_state (
		watch              TEXT NOT NULL,
		slot               TEXT NOT NULL,
		firing             INTEGER NOT NULL DEFAULT 0,
		unavailable        INTEGER NOT NULL DEFAULT 0,
		last_notify_at     INTEGER NOT NULL DEFAULT 0,
		consecutive        INTEGER NOT NULL DEFAULT 0,
		history            TEXT NOT NULL DEFAULT '[]',
		true_since         INTEGER NOT NULL DEFAULT 0,
		timed_history      TEXT NOT NULL DEFAULT '[]',
		last_action_at     INTEGER NOT NULL DEFAULT 0,
		recent_actions     TEXT NOT NULL DEFAULT '[]',
		current_backoff_ns INTEGER NOT NULL DEFAULT 0,
		clear_since        INTEGER NOT NULL DEFAULT 0,
		clear_consecutive INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (watch, slot)
	);`,
	// sla_archive holds availability at every stored resolution: res is the
	// bucket span in seconds, so the same table carries per-minute samples and
	// the coarser archives consolidated from them. check_name '' is the
	// service-level series. down_buckets counts the one-minute buckets that had
	// at least one failed cycle, so a short outage stays visible (and countable)
	// after consolidation instead of being diluted by the window's ratio.
	// res leads the primary key: every read knows it, and pruning one
	// resolution stays inside that key prefix. WITHOUT ROWID stores the key once
	// instead of duplicating it in a rowid table plus its automatic index.
	`CREATE TABLE IF NOT EXISTS sla_archive (
		res          INTEGER NOT NULL,
		service      TEXT    NOT NULL,
		check_name   TEXT    NOT NULL,
		bucket       INTEGER NOT NULL,
		up_count     INTEGER NOT NULL,
		total_count  INTEGER NOT NULL,
		down_buckets INTEGER NOT NULL,
		PRIMARY KEY (res, service, check_name, bucket)
	) WITHOUT ROWID;`,
	// metric_archive is the numeric counterpart, holding every measured series at
	// every stored resolution. scope separates the check, service and daemon
	// dimensions; metric names the series within the scope (check latency,
	// cpu/memory/io, or a check's declared metric).
	// n/sum_v keep the average weight-correct across resolutions, and min_v/max_v
	// carry the extremes through consolidation so a spike survives.
	`CREATE TABLE IF NOT EXISTS metric_archive (
		res        INTEGER NOT NULL,
		scope      TEXT    NOT NULL,
		service    TEXT    NOT NULL,
		check_name TEXT    NOT NULL,
		metric     TEXT    NOT NULL,
		bucket     INTEGER NOT NULL,
		n          INTEGER NOT NULL,
		sum_v      REAL    NOT NULL,
		min_v      REAL    NOT NULL,
		max_v      REAL    NOT NULL,
		PRIMARY KEY (res, scope, service, check_name, metric, bucket)
	) WITHOUT ROWID;`,
	// rollup_state records how far each coarser archive has consolidated its
	// source. It is the prune safety floor: a resolution is never deleted ahead
	// of the archive that still has to read it.
	`CREATE TABLE IF NOT EXISTS rollup_state (
		res       INTEGER PRIMARY KEY,
		watermark INTEGER NOT NULL
	) WITHOUT ROWID;`,
}

// Store is a handle to the persistent state database. It is safe for concurrent
// use; access is serialized onto a single connection (the store is low-traffic
// and this avoids cross-process "database is locked" surprises).
type Store struct {
	db *sql.DB
	// reader is a separate read-only connection. Under WAL a reader runs
	// concurrently with the single writer connection, so a cold rolling-year
	// SLA aggregation no longer stalls the daemon's per-cycle writes behind
	// it. Nil (tests constructing Store directly) falls back to db.
	reader *sql.DB
	now    func() time.Time
	ctx    context.Context

	// retention is the resolution ladder every read consults to pick the archive
	// answering a window, and the maintenance pass consults to prune it.
	retention Retention

	// pruneMu guards pruned, the per-(table,resolution) memo of the newest prune
	// boundary already issued. It skips DELETEs that provably match nothing; see
	// pruneArchive.
	pruneMu sync.Mutex
	pruned  map[pruneKey]int64

	// stmtMu guards stmts, the prepared-statement cache for the write paths.
	// database/sql re-prepares a db.Exec query on every call; the per-cycle
	// upsert burst repeats the same handful of statements, so preparing each
	// once on the single write connection removes that overhead.
	stmtMu sync.Mutex
	stmts  map[string]*sql.Stmt
}

// Batch records related time-series samples in one SQLite transaction. A batch
// is passed only to Store.WithBatch and must not be retained after its callback
// returns.
type Batch interface {
	RecordSLA(service string, up bool, at time.Time) error
	RecordCheckSLA(service, check string, up bool, at time.Time) error
	RecordMeasurement(service, check string, valueMs float64, at time.Time) error
	RecordMetric(service, check, metric string, value float64, at time.Time) error
	RecordDaemonMetric(metric string, value float64, at time.Time) error
	RecordServiceMetric(service, metric string, value float64, at time.Time) error
}

type batch struct {
	tx    *sql.Tx
	ctx   context.Context
	stmts map[string]*sql.Stmt
}

// exec runs a batch statement through its transaction-local prepared-statement
// cache. Store's cache cannot be used here: its one write connection is already
// pinned by the transaction.
func (b *batch) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	stmt, ok := b.stmts[query]
	if !ok {
		var err error
		stmt, err = b.tx.PrepareContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("prepare state batch statement: %w", err)
		}
		if b.stmts == nil {
			b.stmts = map[string]*sql.Stmt{}
		}
		b.stmts[query] = stmt
	}
	result, err := stmt.ExecContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("exec state batch statement: %w", err)
	}
	return result, nil
}

func (b *batch) close() {
	for _, stmt := range b.stmts {
		_ = stmt.Close()
	}
	b.stmts = nil
}

// WithBatch runs record in one transaction. Returning an error from record
// rolls back every sample in the batch; a later cycle can continue recording.
// Callers must pass a non-nil ctx (typically the cycle or request context).
func (s *Store) WithBatch(ctx context.Context, record func(Batch) error) error {
	if record == nil {
		return errors.New("record state batch: callback is nil")
	}
	if ctx == nil {
		return errors.New("record state batch: nil context")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin state batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txBatch := &batch{tx: tx, ctx: ctx}
	defer txBatch.close()
	if err := record(txBatch); err != nil {
		return fmt.Errorf("record state batch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state batch: %w", err)
	}
	return nil
}

// exec runs a write statement through the prepared-statement cache.
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.stmtMu.Lock()
	stmt, ok := s.stmts[query]
	if !ok {
		var err error
		stmt, err = s.db.PrepareContext(ctx, query)
		if err != nil {
			s.stmtMu.Unlock()
			return nil, fmt.Errorf("prepare state statement: %w", err)
		}
		if s.stmts == nil {
			s.stmts = map[string]*sql.Stmt{}
		}
		s.stmts[query] = stmt
	}
	s.stmtMu.Unlock()
	res, err := stmt.ExecContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("exec state statement: %w", err)
	}
	return res, nil
}

// reads returns the connection SELECT-only paths should use.
func (s *Store) reads() *sql.DB {
	if s.reader != nil {
		return s.reader
	}
	return s.db
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

// DefaultSeriesWindow is the normal lookback used when a series request omits
// its `since` window.
const DefaultSeriesWindow = hoursPerDay * time.Hour

// DefaultCacheBytes is the SQLite page-cache size used when the caller does not
// override it. The archive tables and their indexes grow into the tens of MB;
// 64 MiB keeps the hot index pages resident so a per-cycle upsert burst does not
// thrash them from disk. Reads run on their own
// connection (see Store.reader), so the budget applies per connection.
const DefaultCacheBytes = 64 * units.BytesPerMiB

// Options tunes an opened Store.
type Options struct {
	// CacheBytes sets the SQLite page cache. Values <= 0 use DefaultCacheBytes.
	CacheBytes int64
	// Retention is the per-resolution history window. A zero value, or any
	// non-positive field in it, falls back to DefaultRetention.
	Retention Retention
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

	s := &Store{db: db, now: time.Now, ctx: ctx, retention: opts.Retention.normalized()}
	if err := s.initializeSchema(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize state db %s: %w", path, err)
	}
	// A dedicated read-only connection keeps heavy aggregations (rolling-year
	// SLA windows) off the write connection; query_only guards against any
	// read path accidentally writing through it.
	reader, err := sql.Open(sqliteDriverName, dsn+"&_pragma=query_only(on)")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open state db reader %s: %w", path, err)
	}
	reader.SetMaxOpenConns(1)
	s.reader = reader
	return s, nil
}

// Close releases the database handles.
func (s *Store) Close() error {
	s.stmtMu.Lock()
	for _, stmt := range s.stmts {
		_ = stmt.Close()
	}
	s.stmts = nil
	s.stmtMu.Unlock()
	var readerErr error
	if s.reader != nil {
		readerErr = s.reader.Close()
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state store: %w", err)
	}
	if readerErr != nil {
		return fmt.Errorf("close state store reader: %w", readerErr)
	}
	return nil
}

func (s *Store) initializeSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin state schema: %w", err)
	}
	for _, stmt := range storageSchema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("create state schema: %w", err)
		}
	}
	if err := ensureSnapshotColumns(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state schema: %w", err)
	}
	return nil
}

// snapshotColumnMigrations are the additive columns the snapshot cache tables
// have grown since their first shipped shape. CREATE TABLE IF NOT EXISTS never
// alters an existing table, so a database created before a column existed keeps
// failing every insert forever unless the column is added here — which is
// exactly what happened when `observation` shipped without this list: every
// snapshot persist on a pre-existing database logged "has no column named
// observation" until the daemon was pointed at a fresh file. The defaults make
// old rows readable; they are overwritten on the next cycle anyway, because
// these tables cache current state, not history.
var snapshotColumnMigrations = []struct {
	table  string
	column string
	decl   string
}{
	{"service_check_snapshot", "check_type", "check_type TEXT NOT NULL DEFAULT ''"},
	{"service_check_snapshot", "unavailable", "unavailable INTEGER NOT NULL DEFAULT 0"},
	{"service_check_snapshot", "observation", "observation TEXT NOT NULL DEFAULT ''"},
	{"watch_check_snapshot", "check_type", "check_type TEXT NOT NULL DEFAULT ''"},
	{"watch_check_snapshot", "unavailable", "unavailable INTEGER NOT NULL DEFAULT 0"},
	{"watch_check_snapshot", "observation", "observation TEXT NOT NULL DEFAULT ''"},
}

// ensureSnapshotColumns adds any missing cache-table columns to a database
// created under an older schema.
func ensureSnapshotColumns(ctx context.Context, tx *sql.Tx) error {
	for _, m := range snapshotColumnMigrations {
		present, err := columnExists(ctx, tx, m.table, m.column)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+m.table+" ADD COLUMN "+m.decl); err != nil {
			return fmt.Errorf("add %s.%s: %w", m.table, m.column, err)
		}
	}
	return nil
}

func columnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s columns: %w", table, err)
	}
	return false, nil
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

// ServiceRestartNoticeRecord is the principal process identity last handled by
// the external-restart notice monitor for one service.
type ServiceRestartNoticeRecord struct {
	PID       int
	StartedAt time.Time
}

// CheckSnapshotRecord is one persisted latest check result. Name is the service
// check name or host-watch slot; CheckType identifies the check that produced
// the data so callers never decode a prior result as a new check type.
type CheckSnapshotRecord struct {
	Name        string
	CheckType   string
	Observation checks.ObservationState
	OK          bool
	Condition   bool
	Optional    bool
	Skipped     bool
	Unavailable bool
	Message     string
	Data        map[string]any
	Ran         bool
	At          time.Time
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
	err = s.reads().QueryRowContext(s.sqlCtx(), query, key).Scan(&v, &source, &updated)
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
	if _, err := s.exec(s.sqlCtx(), query, key, boolInt(on), source, s.now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("%s: %w", errContext, err)
	}
	return nil
}

// Active reports whether monitoring is currently active for an entry. found is
// false when the entry has no recorded state yet (the caller decides the
// default — typically "monitor on").
func (s *Store) Active(service string) (active, found bool, err error) {
	var v int
	err = s.reads().QueryRowContext(s.sqlCtx(), "SELECT active FROM monitor_state WHERE service = ?;", service).Scan(&v)
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
	_, err := s.exec(s.sqlCtx(),
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
	err := s.reads().QueryRowContext(s.sqlCtx(),
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
	_, err := s.exec(s.sqlCtx(), `DELETE FROM operation_settling WHERE service = ?;`, service)
	if err != nil {
		return fmt.Errorf("clear operation settling for %s: %w", service, err)
	}
	return nil
}

// ServiceRestartNotice returns the principal process identity already handled
// by the external-restart notice monitor. found is false when the service has
// not been observed below its configured uptime threshold yet.
func (s *Store) ServiceRestartNotice(service string) (ServiceRestartNoticeRecord, bool, error) {
	var pid int
	var started string
	err := s.reads().QueryRowContext(s.sqlCtx(),
		`SELECT pid, started_at FROM service_restart_notice WHERE service = ?;`, service,
	).Scan(&pid, &started)
	switch {
	case err == sql.ErrNoRows:
		return ServiceRestartNoticeRecord{}, false, nil
	case err != nil:
		return ServiceRestartNoticeRecord{}, false, fmt.Errorf("load service restart notice for %s: %w", service, err)
	default:
		at, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return ServiceRestartNoticeRecord{}, false, fmt.Errorf("parse service restart notice for %s: %w", service, err)
		}
		return ServiceRestartNoticeRecord{PID: pid, StartedAt: at}, true, nil
	}
}

// SetServiceRestartNotice persists the principal process identity handled by
// the external-restart notice monitor, whether delivery was sent or intentionally
// suppressed for a Sermo-initiated operation.
func (s *Store) SetServiceRestartNotice(service string, record ServiceRestartNoticeRecord) error {
	_, err := s.exec(s.sqlCtx(),
		`INSERT INTO service_restart_notice (service, pid, started_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   pid        = excluded.pid,
		   started_at = excluded.started_at;`,
		service, record.PID, record.StartedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("set service restart notice for %s: %w", service, err)
	}
	return nil
}

// ServiceCheckSnapshots returns every persisted service check snapshot, grouped
// by service name and keyed by check name.
func (s *Store) ServiceCheckSnapshots() (map[string]map[string]CheckSnapshotRecord, error) {
	return s.groupedCheckSnapshots(
		`SELECT service, check_name, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at
		   FROM service_check_snapshot ORDER BY service, check_name;`,
		"service check snapshots",
	)
}

// SetServiceCheckSnapshots replaces one service's latest check snapshots.
func (s *Store) SetServiceCheckSnapshots(service string, records map[string]CheckSnapshotRecord) error {
	return replaceServiceRows(s, service, `DELETE FROM service_check_snapshot WHERE service = ?;`,
		"service check snapshot", records, func(tx *sql.Tx, name string, rec CheckSnapshotRecord) error {
			if err := validateCheckSnapshotObservation(rec.Observation); err != nil {
				return fmt.Errorf("service check snapshot %s/%s: %w", service, name, err)
			}
			data, err := encodeSnapshotData(rec.Data)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(s.sqlCtx(),
				`INSERT INTO service_check_snapshot
				   (service, check_name, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
				checkSnapshotArgs(rec, data, service, name)...,
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
		`SELECT watch, slot, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at
		   FROM watch_check_snapshot ORDER BY watch, slot;`,
		"watch check snapshots",
	)
}

func (s *Store) groupedCheckSnapshots(query, label string) (map[string]map[string]CheckSnapshotRecord, error) {
	rows, err := s.reads().QueryContext(s.sqlCtx(), query)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", label, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]CheckSnapshotRecord{}
	for rows.Next() {
		group, slot, record, err := scanCheckSnapshotRow(rows, label)
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

// scanCheckSnapshotRow scans one (group, slot) check-snapshot row. The service
// and watch snapshot tables deliberately keep separate compile-time SQL, but
// their column shape is identical, so one scanner serves both; label names the
// table in scan errors.
func scanCheckSnapshotRow(rows *sql.Rows, label string) (string, string, CheckSnapshotRecord, error) {
	var (
		group       string
		slot        string
		checkType   string
		observation string
		ok          int
		cond        int
		optional    int
		skipped     int
		unavailable int
		message     string
		rawData     string
		ran         int
		at          int64
	)
	if err := rows.Scan(&group, &slot, &checkType, &observation, &ok, &cond, &optional, &skipped, &unavailable, &message, &rawData, &ran, &at); err != nil {
		return "", "", CheckSnapshotRecord{}, fmt.Errorf("scan %s: %w", label, err)
	}
	record, err := newCheckSnapshotRecord(slot, checkType, observation, ok, cond, optional, skipped, unavailable, message, rawData, ran, at)
	return group, slot, record, err
}

func newCheckSnapshotRecord(name, checkType, observation string, ok, condition, optional, skipped, unavailable int, message, rawData string, ran int, at int64) (CheckSnapshotRecord, error) {
	observationState := checks.ObservationState(observation)
	if err := validateCheckSnapshotObservation(observationState); err != nil {
		return CheckSnapshotRecord{}, fmt.Errorf("decode check snapshot %s: %w", name, err)
	}
	data, err := decodeSnapshotData(rawData)
	if err != nil {
		return CheckSnapshotRecord{}, err
	}
	return CheckSnapshotRecord{
		Name: name, CheckType: checkType, Observation: observationState, OK: intBool(ok), Condition: intBool(condition), Optional: intBool(optional),
		Skipped: intBool(skipped), Unavailable: intBool(unavailable), Message: message, Data: data, Ran: intBool(ran), At: unixNanoTime(at),
	}, nil
}

// SetWatchCheckSnapshot upserts one host-watch snapshot slot.
func (s *Store) SetWatchCheckSnapshot(watch, slot string, rec CheckSnapshotRecord) error {
	if err := validateCheckSnapshotObservation(rec.Observation); err != nil {
		return fmt.Errorf("watch check snapshot %s/%s: %w", watch, slot, err)
	}
	data, err := encodeSnapshotData(rec.Data)
	if err != nil {
		return err
	}
	_, err = s.exec(s.sqlCtx(),
		`INSERT INTO watch_check_snapshot
		   (watch, slot, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watch, slot) DO UPDATE SET
		   check_type = excluded.check_type,
		   observation = excluded.observation,
		   ok         = excluded.ok,
		   condition  = excluded.condition,
		   optional   = excluded.optional,
		   skipped    = excluded.skipped,
		   unavailable = excluded.unavailable,
		   message    = excluded.message,
		   data       = excluded.data,
		   ran        = excluded.ran,
		   at         = excluded.at;`,
		checkSnapshotArgs(rec, data, watch, slot)...,
	)
	if err != nil {
		return fmt.Errorf("set watch check snapshot %s/%s: %w", watch, slot, err)
	}
	return nil
}

// checkSnapshotArgs builds the bind arguments both check-snapshot tables take:
// the caller's key columns followed by the shared reading payload, in the column
// order the two INSERTs declare. One definition keeps the service and watch
// tables from drifting when a snapshot column is added.
func checkSnapshotArgs(rec CheckSnapshotRecord, data string, keys ...any) []any {
	return append(keys,
		rec.CheckType, string(rec.Observation), boolInt(rec.OK), boolInt(rec.Condition), boolInt(rec.Optional), boolInt(rec.Skipped),
		boolInt(rec.Unavailable), rec.Message, data, boolInt(rec.Ran), timeUnixNano(rec.At),
	)
}

func validateCheckSnapshotObservation(observation checks.ObservationState) error {
	if !observation.Valid() {
		return fmt.Errorf("invalid observation %q", observation)
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

// RuleWindowRecord is the persisted for/within progress for one rule, plus its
// firing episode and clear-window progress.
type RuleWindowRecord struct {
	Consecutive      int
	History          []bool
	TrueSince        time.Time
	TimedHistory     []RuleWindowSample
	Firing           bool
	ClearConsecutive int
	ClearSince       time.Time
}

// RuleWindowSample is one persisted sample for a duration-based within window.
type RuleWindowSample struct {
	At    time.Time
	Match bool
}

// WatchRuntimeRecord is the durable control state for one watch result slot.
type WatchRuntimeRecord struct {
	Firing       bool
	Unavailable  bool
	LastNotifyAt time.Time
	Window       RuleWindowRecord
	Policy       RemediationRecord
}

// WatchRuntimeState returns one watch slot's persisted episode and pacing state.
func (s *Store) WatchRuntimeState(watch, slot string) (WatchRuntimeRecord, bool, error) {
	var (
		firing             int
		unavailable        int
		lastNotifyAt       int64
		consecutive        int
		rawHistory         string
		trueSince          int64
		rawTimed           string
		lastActionAt       int64
		rawRecentActions   string
		currentBackoffNano int64
		clearSince         int64
		clearConsecutive   int
	)
	err := s.reads().QueryRowContext(s.sqlCtx(),
		`SELECT firing, unavailable, last_notify_at, consecutive, history, true_since,
		        timed_history, last_action_at, recent_actions, current_backoff_ns,
		        clear_since, clear_consecutive
		   FROM watch_runtime_state WHERE watch = ? AND slot = ?;`,
		watch, slot,
	).Scan(
		&firing, &unavailable, &lastNotifyAt, &consecutive, &rawHistory, &trueSince,
		&rawTimed, &lastActionAt, &rawRecentActions, &currentBackoffNano,
		&clearSince, &clearConsecutive,
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
		Unavailable:  unavailable != 0,
		LastNotifyAt: unixNanoTime(lastNotifyAt),
		Window: RuleWindowRecord{
			Consecutive:  consecutive,
			History:      history,
			TrueSince:    unixNanoTime(trueSince),
			TimedHistory: timed,
			// The watch's episode column doubles as the window's: both track the
			// same firing episode, kept in sync by Watch.evaluateFiring.
			Firing:           firing != 0,
			ClearSince:       unixNanoTime(clearSince),
			ClearConsecutive: clearConsecutive,
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
		_, err := s.exec(s.sqlCtx(), `DELETE FROM watch_runtime_state WHERE watch = ? AND slot = ?;`, watch, slot)
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
	firing := boolInt(rec.Firing)
	_, err = s.exec(s.sqlCtx(),
		`INSERT INTO watch_runtime_state (
		   watch, slot, firing, unavailable, last_notify_at, consecutive, history, true_since,
		   timed_history, last_action_at, recent_actions, current_backoff_ns,
		   clear_since, clear_consecutive
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watch, slot) DO UPDATE SET
		   firing             = excluded.firing,
		   unavailable        = excluded.unavailable,
		   last_notify_at      = excluded.last_notify_at,
		   consecutive         = excluded.consecutive,
		   history             = excluded.history,
		   true_since          = excluded.true_since,
		   timed_history       = excluded.timed_history,
		   last_action_at      = excluded.last_action_at,
		   recent_actions      = excluded.recent_actions,
		   current_backoff_ns  = excluded.current_backoff_ns,
		   clear_since         = excluded.clear_since,
		   clear_consecutive   = excluded.clear_consecutive;`,
		watch, slot, firing, boolInt(rec.Unavailable), timeUnixNano(rec.LastNotifyAt), rec.Window.Consecutive,
		string(history), timeUnixNano(rec.Window.TrueSince), timed,
		timeUnixNano(rec.Policy.LastActionAt), recent, int64(rec.Policy.CurrentBackoff),
		timeUnixNano(rec.Window.ClearSince), rec.Window.ClearConsecutive,
	)
	if err != nil {
		return fmt.Errorf("set watch runtime state for %s/%s: %w", watch, slot, err)
	}
	return nil
}

func watchRuntimeRecordEmpty(rec WatchRuntimeRecord) bool {
	return !rec.Firing && !rec.Unavailable && rec.LastNotifyAt.IsZero() &&
		rec.Window.Consecutive == 0 && len(rec.Window.History) == 0 &&
		rec.Window.TrueSince.IsZero() && len(rec.Window.TimedHistory) == 0 &&
		!rec.Window.Firing && rec.Window.ClearSince.IsZero() && rec.Window.ClearConsecutive == 0 &&
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
	err := s.reads().QueryRowContext(s.sqlCtx(),
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
		_, err := s.exec(s.sqlCtx(), `DELETE FROM remediation_state WHERE service = ?;`, service)
		if err != nil {
			return fmt.Errorf("clear remediation state for %s: %w", service, err)
		}
		return nil
	}
	recent, err := encodeUnixNanos(rec.RecentActions)
	if err != nil {
		return err
	}
	_, err = s.exec(s.sqlCtx(),
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
	rows, err := s.reads().QueryContext(s.sqlCtx(),
		`SELECT rule_name, consecutive, history, true_since, timed_history,
		        firing, clear_since, clear_consecutive
		   FROM rule_window_state WHERE service = ? ORDER BY rule_name;`,
		service,
	)
	if err != nil {
		return nil, fmt.Errorf("load rule window states for %s: %w", service, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]RuleWindowRecord{}
	for rows.Next() {
		var (
			name             string
			consecutive      int
			rawHistory       string
			trueSince        int64
			rawTimed         string
			firing           int
			clearSince       int64
			clearConsecutive int
		)
		if err := rows.Scan(&name, &consecutive, &rawHistory, &trueSince, &rawTimed, &firing, &clearSince, &clearConsecutive); err != nil {
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
			Consecutive:      consecutive,
			History:          history,
			TrueSince:        unixNanoTime(trueSince),
			TimedHistory:     timed,
			Firing:           firing != 0,
			ClearSince:       unixNanoTime(clearSince),
			ClearConsecutive: clearConsecutive,
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
			firing := boolInt(rec.Firing)
			if _, err := tx.ExecContext(s.sqlCtx(),
				`INSERT INTO rule_window_state (service, rule_name, consecutive, history, true_since, timed_history,
				                                firing, clear_since, clear_consecutive)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`,
				service, name, rec.Consecutive, string(history), timeUnixNano(rec.TrueSince), timed,
				firing, timeUnixNano(rec.ClearSince), rec.ClearConsecutive,
			); err != nil {
				return fmt.Errorf("insert rule window state for %s/%s: %w", service, name, err)
			}
			return nil
		})
}

// The two JSON-encoded history columns (rule window timestamps and rule window
// samples) share one round-trip shape: marshal the rows that carry a timestamp,
// and on the way back drop any row whose timestamp did not survive. encodeRows
// and decodeRows own that shape so each column only describes its own row.

// encodeRows marshals the non-zero rows of values as a JSON column. what names
// the column in errors.
func encodeRows[T, R any](what string, values []T, row func(T) (R, bool)) (string, error) {
	rows := make([]R, 0, len(values))
	for _, value := range values {
		if r, ok := row(value); ok {
			rows = append(rows, r)
		}
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", what, err)
	}
	return string(b), nil
}

// decodeRows unmarshals a JSON column written by encodeRows, skipping rows that
// value rejects. An empty column decodes to no values, not an error.
func decodeRows[R, T any](what, raw string, value func(R) (T, bool)) ([]T, error) {
	if raw == "" {
		return nil, nil
	}
	var rows []R
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", what, err)
	}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		if v, ok := value(r); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func encodeUnixNanos(times []time.Time) (string, error) {
	return encodeRows(columnUnixNanos, times, func(t time.Time) (int64, bool) {
		return t.UTC().UnixNano(), !t.IsZero()
	})
}

func decodeUnixNanos(raw string) ([]time.Time, error) {
	return decodeRows(columnUnixNanos, raw, func(n int64) (time.Time, bool) {
		return time.Unix(0, n).UTC(), n != 0
	})
}

// column* name the JSON-encoded history columns in encode/decode errors.
const (
	columnUnixNanos         = "unix nanos"
	columnRuleWindowSamples = "rule window samples"
)

type ruleWindowSampleJSON struct {
	At    int64 `json:"at"`
	Match bool  `json:"match"`
}

func encodeRuleWindowSamples(samples []RuleWindowSample) (string, error) {
	return encodeRows(columnRuleWindowSamples, samples, func(s RuleWindowSample) (ruleWindowSampleJSON, bool) {
		return ruleWindowSampleJSON{At: s.At.UTC().UnixNano(), Match: s.Match}, !s.At.IsZero()
	})
}

func decodeRuleWindowSamples(raw string) ([]RuleWindowSample, error) {
	return decodeRows(columnRuleWindowSamples, raw, func(s ruleWindowSampleJSON) (RuleWindowSample, bool) {
		return RuleWindowSample{At: time.Unix(0, s.At).UTC(), Match: s.Match}, s.At != 0
	})
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
	result, err := s.exec(s.sqlCtx(),
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

// eventSelectPrefix is the column list every event_log read shares, so the two
// queries below cannot drift out of step with scanEventRows.
const eventSelectPrefix = `SELECT id, at, service, watch, app, kind, rule, action, status, message, output
		   FROM event_log`

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
	query := eventSelectPrefix
	args := make([]any, 0, eventQueryMaxArgs)
	if beforeID > 0 {
		query += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?;`
	args = append(args, limit)
	rows, err := s.reads().QueryContext(s.sqlCtx(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("load recent events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanEventRows(rows)
}

// RecentEventsForColumn returns the newest persisted events for one target,
// filtered on the event_log column naming that dimension ("service", "watch" or
// "app"). It exists because the daemon's in-memory ring is not authoritative:
// sermoctl runs a service operation in its own process and records the event
// straight to this store, so a ring-only per-target view silently omits every
// operator action until the daemon restarts and rehydrates. Callers pass a
// column from eventTargetColumns; anything else is rejected rather than
// interpolated into the query.
func (s *Store) RecentEventsForColumn(column, name string, limit int) ([]EventRecord, error) {
	if !slices.Contains(eventTargetColumns, column) {
		return nil, fmt.Errorf("load recent events: unsupported event target column %q", column)
	}
	if limit <= 0 {
		limit = -1
	}
	query := eventSelectPrefix + ` WHERE ` + column + ` = ? ORDER BY id DESC LIMIT ?;`
	rows, err := s.reads().QueryContext(s.sqlCtx(), query, name, limit)
	if err != nil {
		return nil, fmt.Errorf("load recent events for %s %q: %w", column, name, err)
	}
	defer func() { _ = rows.Close() }()
	return scanEventRows(rows)
}

// EventColumnService and EventColumnApp name the event_log dimension a
// per-target query filters on. `watch` is deliberately absent: nothing reads
// watch events per target today, and unlike these two it has no covering index,
// so allowing it would ship a full table scan waiting for its first caller. Add
// `event_log_watch_at_idx` alongside the entry before opening that door.
const (
	EventColumnService = "service"
	EventColumnApp     = "app"
)

// eventTargetColumns is the allow-list RecentEventsForColumn validates against,
// so no caller can reach the query builder with an arbitrary identifier.
var eventTargetColumns = []string{EventColumnService, EventColumnApp}

func scanEventRows(rows *sql.Rows) ([]EventRecord, error) {
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
// persisted event is deleted. ctx bounds the DELETE (and any busy waits).
func (s *Store) PruneEvents(ctx context.Context, before time.Time) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if before.IsZero() {
		res, err = s.exec(ctx, `DELETE FROM event_log;`)
	} else {
		res, err = s.exec(ctx, `DELETE FROM event_log WHERE at < ?;`, before.UTC().UnixNano())
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
// observed cycle counts, plus how many one-minute buckets in the window saw a
// failure. Ratio derives the fraction (and whether any data exists).
//
// DownBuckets survives consolidation, so a window whose ratio rounds to 100% can
// still be reported as having had incidents, and they can still be counted.
type SLAValue struct {
	Window      string `json:"window"`
	Up          int64  `json:"up"`
	Total       int64  `json:"total"`
	DownBuckets int64  `json:"down_buckets"`
}

// Ratio returns the availability fraction in [0,1] and whether the window has any
// observed cycles. With no data (total==0) availability is unknown, not 0%.
func (v SLAValue) Ratio() (float64, bool) {
	if v.Total <= 0 {
		return 0, false
	}
	return float64(v.Up) / float64(v.Total), true
}

// RecordSLA accumulates one observed monitoring cycle into a service's current
// UTC-minute bucket: total_count +1, and up_count +1 when up. Paused or
// unobserved cycles are simply never recorded, so they do not count as downtime.
func (s *Store) RecordSLA(service string, up bool, at time.Time) error {
	return s.recordSLABucket(service, "", up, at)
}

// RecordSLA accumulates one observed monitoring cycle in this batch.
func (b *batch) RecordSLA(service string, up bool, at time.Time) error {
	return b.recordSLABucket(service, "", up, at)
}

// RecordCheckSLA accumulates one observed check execution into its current
// UTC-minute bucket. Interval-deferred checks are not recorded by callers, so
// the per-check SLA reflects only real check runs.
func (s *Store) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	return s.recordSLABucket(service, check, up, at)
}

// RecordCheckSLA accumulates one observed check execution in this batch.
func (b *batch) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	return b.recordSLABucket(service, check, up, at)
}

// recordSLABucket writes one observed cycle into the per-minute archive. An empty
// check is the service-level series. down_buckets is recomputed rather than
// accumulated: at this resolution the bucket is the unit it counts, so it is 1 as
// soon as any cycle in the minute failed.
func (s *Store) recordSLABucket(service, check string, up bool, at time.Time) error {
	return recordSLABucket(s.sqlCtx(), s.exec, service, check, up, at)
}

func (b *batch) recordSLABucket(service, check string, up bool, at time.Time) error {
	return recordSLABucket(b.ctx, b.exec, service, check, up, at)
}

type statementExecutor func(context.Context, string, ...any) (sql.Result, error)

func recordSLABucket(ctx context.Context, exec statementExecutor, service, check string, up bool, at time.Time) error {
	if _, err := exec(ctx, slaRecordStmt,
		resMinute, service, check, alignBucket(at, resMinute), boolInt(up), boolInt(!up),
	); err != nil {
		return fmt.Errorf("record %s for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	return nil
}

// slaKind and slaTarget name one SLA series for an error message. They are called
// only from the error branches: the per-cycle write path runs for every service
// and check on every cycle, so building the target string eagerly would allocate
// once per call for a message that is almost never used.
func slaKind(check string) string {
	if check == "" {
		return "SLA"
	}
	return "check SLA"
}

func slaTarget(service, check string) string {
	if check == "" {
		return service
	}
	return service + "/" + check
}

// SLAPoint is one time bucket of a service's availability series: the up and
// total observed cycles in that bucket, plus how many of its one-minute
// sub-buckets saw a failure. A missing point means the service was not monitored
// then (Sermo down, or the service paused/disabled) — excluded, not counted as
// down. The bucket span is the archive the window resolved to, so a point covers
// one minute on the hour window and one day on the year window.
type SLAPoint struct {
	Start       time.Time `json:"start"`
	Up          int64     `json:"up"`
	Total       int64     `json:"total"`
	DownBuckets int64     `json:"down_buckets"`
}

// SLASeries returns a service's availability points in [from, to), oldest first,
// at the resolution that window is stored at. Unmonitored buckets are absent
// (gaps) rather than zero rows, so a caller can render excluded periods
// distinctly from downtime.
func (s *Store) SLASeries(service string, from, to time.Time) ([]SLAPoint, error) {
	return s.loadSLASeries(service, "", from, to)
}

// CheckSLASeries returns one check's availability points in [from, to), oldest
// first. Unobserved buckets are absent.
func (s *Store) CheckSLASeries(service, check string, from, to time.Time) ([]SLAPoint, error) {
	return s.loadSLASeries(service, check, from, to)
}

// sumSLA totals one series over the rolling window ending at now. The archive is
// chosen from the requested span, and every read of the same window resolves to
// the same one, which is what keeps a window total and its segment breakdown in
// agreement.
func (s *Store) sumSLA(service, check string, span time.Duration, now time.Time) (SLAValue, error) {
	from := now.Add(-span)
	stored := s.retention.archiveFor(from, now)
	var value SLAValue
	if err := s.reads().QueryRowContext(s.sqlCtx(), slaSumStmt,
		stored.Res, service, check, alignBucket(from, stored.Res),
	).Scan(&value.Up, &value.Total, &value.DownBuckets); err != nil {
		return SLAValue{}, fmt.Errorf("sum %s for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	return value, nil
}

func (s *Store) loadSLASeries(service, check string, from, to time.Time) ([]SLAPoint, error) {
	// The resolution is chosen relative to `to`, the caller's reference instant —
	// every caller passes now. A range that both starts and ends in the past is
	// therefore resolved by its span, which may name an archive whose retention has
	// already dropped it; it then legitimately returns no rows.
	stored := s.retention.archiveFor(from, to)
	// The upper bound covers the bucket holding `to` rather than truncating to
	// its start, so the newest (still filling) bucket is not dropped from the
	// series. A bucket start is never in the future, so nothing beyond `to` can
	// appear.
	rows, err := s.reads().QueryContext(s.sqlCtx(), slaSeriesStmt,
		stored.Res, service, check,
		alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res)
	if err != nil {
		return nil, fmt.Errorf("load %s series for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	defer func() { _ = rows.Close() }()

	var out []SLAPoint
	for rows.Next() {
		var bucket int64
		var point SLAPoint
		if err := rows.Scan(&bucket, &point.Up, &point.Total, &point.DownBuckets); err != nil {
			return nil, fmt.Errorf("scan %s series row for %s: %w", slaKind(check), slaTarget(service, check), err)
		}
		point.Start = time.Unix(bucket, 0).UTC()
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s series for %s: %w", slaKind(check), slaTarget(service, check), err)
	}
	return out, nil
}

// SeriesResolution is the bucket span a series request for [from, to) resolves to.
// Callers rendering a series report it, so a reader knows whether a row is a minute
// or a day; inferring it back from the returned points cannot work for a series with
// a single point.
func (s *Store) SeriesResolution(from, to time.Time) time.Duration {
	return time.Duration(s.retention.archiveFor(from, to).Res) * time.Second
}

// SLAReport returns a service's availability across every SLAWindow, ordered as
// SLAWindows (hour..year).
func (s *Store) SLAReport(service string, now time.Time) ([]SLAValue, error) {
	return reportWindows(func(span time.Duration) (SLAValue, error) {
		return s.sumSLA(service, "", span, now)
	})
}

// CheckSLAReport returns one check's availability across every SLAWindow,
// ordered as SLAWindows (hour..year).
func (s *Store) CheckSLAReport(service, check string, now time.Time) ([]SLAValue, error) {
	return reportWindows(func(span time.Duration) (SLAValue, error) {
		return s.sumSLA(service, check, span, now)
	})
}

// reportWindows collects one SLAValue per SLAWindow from the given sum reader;
// the loop shared by the service- and check-level reports.
func reportWindows(sum func(span time.Duration) (SLAValue, error)) ([]SLAValue, error) {
	out := make([]SLAValue, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		value, err := sum(w.Span)
		if err != nil {
			return nil, err
		}
		value.Window = w.Name
		out = append(out, value)
	}
	return out, nil
}

// SLASegment is one equal sub-span of a windowed SLA timeline: the up and total
// observed cycles within it, plus how many of its one-minute sub-buckets saw a
// failure. Total==0 marks a gap (an unmonitored sub-span), which renders
// distinctly from downtime — the same gap convention as SLASeries.
//
// DownBuckets is what keeps a short outage visible after consolidation: a
// 40-second failure inside a day-long segment barely moves Up/Total, but it
// leaves DownBuckets at 1, so the renderer can colour the whole segment as
// affected instead of rounding it to healthy.
type SLASegment struct {
	Up          int64 `json:"up"`
	Total       int64 `json:"total"`
	DownBuckets int64 `json:"down_buckets"`
}

// SLAWindowTimeline is a service's availability over one rolling window plus the
// window divided into equal sub-spans (oldest first) for the web timeline strip.
// Up/Total are the window totals (the sum of the segments), so a caller rendering
// the strip needs no separate SLAReport query.
type SLAWindowTimeline struct {
	Window      string
	Up          int64
	Total       int64
	DownBuckets int64
	Segments    []SLASegment
}

// SLATimelines returns a service's availability for every SLAWindow split into
// equal sub-spans for the web timeline strip, ordered as SLAWindows (hour..year).
func (s *Store) SLATimelines(service string, now time.Time) ([]SLAWindowTimeline, error) {
	return s.slaTimelines(service, "", now)
}

// CheckSLATimelines returns one check's windowed availability split into sub-spans
// for the web timeline strip, ordered as SLAWindows (hour..year).
func (s *Store) CheckSLATimelines(service, check string, now time.Time) ([]SLAWindowTimeline, error) {
	return s.slaTimelines(service, check, now)
}

// slaTimelines reports every SLAWindow for one series. An empty check is the
// service-level series.
func (s *Store) slaTimelines(service, check string, now time.Time) ([]SLAWindowTimeline, error) {
	out := make([]SLAWindowTimeline, 0, len(SLAWindows))
	for _, w := range SLAWindows {
		timeline, err := s.slaTimeline(service, check, w, now)
		if err != nil {
			return nil, err
		}
		out = append(out, timeline)
	}
	return out, nil
}

// slaTimeline divides one window into Segments equal sub-spans ending at now and
// aggregates the stored buckets with a single grouped query — the same indexed
// range scan sumSLA does, returning the per-segment breakdown as well.
//
// The archive is picked exactly as sumSLA picks it, so the window totals here and
// SLAReport's for the same window are aggregated from the same rows and agree.
// Each window's segment span is a whole number of buckets in that archive (see
// the resolution ladder), so no bucket contributes to two segments.
func (s *Store) slaTimeline(service, check string, w SLAWindow, now time.Time) (SLAWindowTimeline, error) {
	segCount := max(w.Segments, 1)
	from := now.Add(-w.Span)
	stored := s.retention.archiveFor(from, now)
	startBucket := alignBucket(from, stored.Res)
	// Include the current (partial) bucket so the window total matches sumSLA,
	// which lower-bounds on the same start bucket but has no upper bound. Stopping
	// at startBucket+span would exclude it and make Up/Total disagree with
	// SLAReport for the same window. That bucket clamps into the last segment.
	endBucket := alignBucket(now, stored.Res) + stored.Res
	segSpan := max(int64(w.Span/time.Second)/int64(segCount), 1)

	rows, err := s.reads().QueryContext(s.sqlCtx(), slaTimelineStmt,
		startBucket, segSpan, stored.Res, service, check, startBucket, endBucket)
	if err != nil {
		return SLAWindowTimeline{}, fmt.Errorf("load SLA timeline for %s: %w", w.Name, err)
	}
	defer func() { _ = rows.Close() }()

	timeline := SLAWindowTimeline{Window: w.Name, Segments: make([]SLASegment, segCount)}
	for rows.Next() {
		var seg, up, total, down int64
		if err := rows.Scan(&seg, &up, &total, &down); err != nil {
			return SLAWindowTimeline{}, fmt.Errorf("scan SLA timeline row for %s: %w", w.Name, err)
		}
		seg = min(max(seg, 0), int64(segCount)-1)
		timeline.Segments[seg].Up += up
		timeline.Segments[seg].Total += total
		timeline.Segments[seg].DownBuckets += down
		timeline.Up += up
		timeline.Total += total
		timeline.DownBuckets += down
	}
	if err := rows.Err(); err != nil {
		return SLAWindowTimeline{}, fmt.Errorf("iterate SLA timeline for %s: %w", w.Name, err)
	}
	return timeline, nil
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

// metricSeries identifies one stored numeric series in metric_archive. The four
// families that used to have a table each — check latency, a check's declared
// metrics, service runtime metrics and the daemon's own — differ only in these
// key columns, so one statement per shape serves all of them.
type metricSeries struct {
	scope   string
	service string
	check   string
	metric  string
}

// checkLatencySeries is the measured latency of one service check.
func checkLatencySeries(service, check string) metricSeries {
	return metricSeries{scope: metricScopeLatency, service: service, check: check}
}

// checkMetricSeries is one of a check's declared numeric metrics.
func checkMetricSeries(service, check, metric string) metricSeries {
	return metricSeries{scope: metricScopeCheckMetric, service: service, check: check, metric: metric}
}

// serviceRuntimeSeries is one of a service process tree's runtime metrics.
func serviceRuntimeSeries(service, metric string) metricSeries {
	return metricSeries{scope: metricScopeService, service: service, metric: metric}
}

// daemonRuntimeSeries is one of sermod's own process metrics.
func daemonRuntimeSeries(metric string) metricSeries {
	return metricSeries{scope: metricScopeDaemon, metric: metric}
}

// kind and target name the series for an error message, derived from the scope and
// the key columns. They are called only from the error branches: record runs for
// every service, check and declared metric on every cycle, so building the target
// eagerly would allocate once per call for a message that is almost never used.
func (m metricSeries) kind() string {
	switch m.scope {
	case metricScopeLatency:
		return "measurement"
	case metricScopeService:
		return "service metric"
	case metricScopeDaemon:
		return "daemon metric"
	default:
		return "metric"
	}
}

func (m metricSeries) target() string {
	keyParts := []string{m.service, m.check, m.metric}
	parts := make([]string, 0, len(keyParts))
	for _, part := range keyParts {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}

// record accumulates one observation into the series' current per-minute bucket:
// n+1, sum+value and the running min/max.
func (s *Store) record(m metricSeries, value float64, at time.Time) error {
	return recordMetric(s.sqlCtx(), s.exec, m, value, at)
}

func (b *batch) record(m metricSeries, value float64, at time.Time) error {
	return recordMetric(b.ctx, b.exec, m, value, at)
}

func recordMetric(ctx context.Context, exec statementExecutor, m metricSeries, value float64, at time.Time) error {
	if _, err := exec(ctx, metricRecordStmt,
		resMinute, m.scope, m.service, m.check, m.metric, alignBucket(at, resMinute),
		value, value, value,
	); err != nil {
		return fmt.Errorf("record %s for %s: %w", m.kind(), m.target(), err)
	}
	return nil
}

// summary returns the series' average/min/max and sample count over the rolling
// window ending at now. The average stays weight-correct at every resolution
// because n and sum_v are consolidated together.
func (s *Store) summary(m metricSeries, span time.Duration, now time.Time) (MeasurementStat, error) {
	from := now.Add(-span)
	stored := s.retention.archiveFor(from, now)
	return summaryFromRow(s.reads().QueryRowContext(s.sqlCtx(), metricSummaryStmt,
		stored.Res, m.scope, m.service, m.check, m.metric, alignBucket(from, stored.Res)))
}

// series returns the series' points in [from, to), oldest first, at the
// resolution that window is stored at. Buckets with no observation are absent
// (gaps), as in SLASeries.
func (s *Store) series(m metricSeries, from, to time.Time) ([]MeasurementPoint, error) {
	stored := s.retention.archiveFor(from, to)
	// As in loadSLASeries the upper bound covers the bucket holding `to`, so the
	// newest still-filling bucket stays in the series.
	rows, err := s.reads().QueryContext(s.sqlCtx(), metricSeriesStmt,
		stored.Res, m.scope, m.service, m.check, m.metric,
		alignBucket(from, stored.Res), alignBucket(to, stored.Res)+stored.Res)
	kind, target := m.kind(), m.target()
	description := kind + " series for " + target
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", description, err)
	}
	return measurementPointsFromRows(rows, kind+" series row for "+target, description)
}

// RecordMeasurement accumulates one numeric observation (milliseconds) for a
// service+check into its current UTC-minute bucket.
func (s *Store) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	return s.record(checkLatencySeries(service, check), valueMs, at)
}

// RecordMeasurement accumulates one latency observation in this batch.
func (b *batch) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	return b.record(checkLatencySeries(service, check), valueMs, at)
}

// MeasurementSummary returns the average/min/max and sample count for a check over
// the rolling window ending at now.
func (s *Store) MeasurementSummary(service, check string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(checkLatencySeries(service, check), span, now)
}

// MeasurementSeries returns a check's latency points in [from, to), oldest first.
func (s *Store) MeasurementSeries(service, check string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(checkLatencySeries(service, check), from, to)
}

// RecordMetric accumulates one observation of a named per-check metric (e.g.
// hdparm "read" MB/s) into its current UTC-minute bucket. It is the generic
// counterpart of RecordMeasurement (latency).
func (s *Store) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	return s.record(checkMetricSeries(service, check, metric), value, at)
}

// RecordMetric accumulates one named check metric in this batch.
func (b *batch) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	return b.record(checkMetricSeries(service, check, metric), value, at)
}

// MetricSummary returns a named metric's average/min/max and sample count over the
// rolling window ending at now.
func (s *Store) MetricSummary(service, check, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(checkMetricSeries(service, check, metric), span, now)
}

// MetricSeries returns a named metric's points in [from, to), oldest first.
func (s *Store) MetricSeries(service, check, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(checkMetricSeries(service, check, metric), from, to)
}

// RecordDaemonMetric accumulates one sermod process metric observation into its
// current UTC-minute bucket.
func (s *Store) RecordDaemonMetric(metric string, value float64, at time.Time) error {
	return s.record(daemonRuntimeSeries(metric), value, at)
}

// RecordDaemonMetric accumulates one daemon metric in this batch.
func (b *batch) RecordDaemonMetric(metric string, value float64, at time.Time) error {
	return b.record(daemonRuntimeSeries(metric), value, at)
}

// DaemonMetricSummary returns a daemon metric's average/min/max and sample count
// over the rolling window ending at now.
func (s *Store) DaemonMetricSummary(metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(daemonRuntimeSeries(metric), span, now)
}

// DaemonMetricSeries returns a daemon metric's points in [from, to), oldest first.
func (s *Store) DaemonMetricSeries(metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(daemonRuntimeSeries(metric), from, to)
}

// RecordServiceMetric accumulates one service process-tree metric observation
// into its current UTC-minute bucket.
func (s *Store) RecordServiceMetric(service, metric string, value float64, at time.Time) error {
	return s.record(serviceRuntimeSeries(service, metric), value, at)
}

// RecordServiceMetric accumulates one service runtime metric in this batch.
func (b *batch) RecordServiceMetric(service, metric string, value float64, at time.Time) error {
	return b.record(serviceRuntimeSeries(service, metric), value, at)
}

// ServiceMetricSummary returns a service runtime metric's average/min/max and
// sample count over the rolling window ending at now.
func (s *Store) ServiceMetricSummary(service, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return s.summary(serviceRuntimeSeries(service, metric), span, now)
}

// ServiceMetricSeries returns a service runtime metric's points in [from, to),
// oldest first.
func (s *Store) ServiceMetricSeries(service, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	return s.series(serviceRuntimeSeries(service, metric), from, to)
}

// summaryFromRow scans the COALESCE(SUM(n),0), SUM, MIN, MAX aggregate row into a
// MeasurementStat (avg = sum/count, guarded against an empty bucket set).
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

// measurementPointsFromRows scans per-minute aggregate rows shared by every
// metric history table. The callers keep their distinct SQL and error context.
func measurementPointsFromRows(rows *sql.Rows, scanContext, iterateContext string) ([]MeasurementPoint, error) {
	defer func() { _ = rows.Close() }()

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

// Compact checkpoints the WAL and vacuums the SQLite state database so space
// freed by pruning can be returned to the filesystem.
//
// The order is load-bearing, and the trailing checkpoint is the step that actually
// shrinks the file. Under WAL, VACUUM rebuilds the database into the write-ahead log
// rather than the main file: it correctly drops the freelist to zero, but the main
// file keeps every page it had until a checkpoint writes back and truncates it. With
// only the leading checkpoint, `sermoctl state compact` reported success and returned
// no space at all — a fleet of hosts sat at 99% free pages, one of them holding 1.4 GB
// of file for 11 MB of data.
//
// The leading checkpoint still earns its place: it bounds the WAL before VACUUM
// rewrites the whole database through it.
func (s *Store) Compact(ctx context.Context) error {
	if _, err := s.exec(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return fmt.Errorf("checkpoint state db WAL: %w", err)
	}
	if _, err := s.exec(ctx, `VACUUM;`); err != nil {
		return fmt.Errorf("vacuum state db: %w", err)
	}
	if _, err := s.exec(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return fmt.Errorf("checkpoint state db WAL after vacuum: %w", err)
	}
	return nil
}
