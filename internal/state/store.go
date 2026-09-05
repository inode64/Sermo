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
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"sermo/internal/units"
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
	ctx    context.Context //nolint:containedctx // store Open cancel; SQL methods use this instead of a per-query argument.

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
	ctx   context.Context //nolint:containedctx // transaction-scoped cancel derived from Store.ctx for one WithBatch callback.
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
	tableServiceSnapshot = "service_check_snapshot"
	tableWatchSnapshot   = "watch_check_snapshot"
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

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intBool(v int) bool {
	return v != 0
}

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

type statementExecutor func(context.Context, string, ...any) (sql.Result, error)

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
