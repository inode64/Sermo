package state

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

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
		config_id  TEXT NOT NULL,
		severity   TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (service, check_name)
	);`,
	// watch_check_snapshot stores the latest host-watch result per visible slot
	// (for example one slot per metric). It keeps /api/watches backed by daemon
	// cycle data across process restarts.
	`CREATE TABLE IF NOT EXISTS watch_check_snapshot (
		watch      TEXT NOT NULL,
		slot       TEXT NOT NULL,
		check_type TEXT NOT NULL,
		config_id  TEXT NOT NULL,
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
		severity   TEXT NOT NULL DEFAULT '',
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
	if err := ensureStateColumns(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state schema: %w", err)
	}
	return nil
}

// stateColumnMigrations are additive columns introduced after their tables'
// first shipped shapes. CREATE TABLE IF NOT EXISTS never alters an existing
// table, so a pre-existing database otherwise keeps failing every write that
// names a new column. Defaults make old cache/control rows readable until the
// next cycle overwrites them.
var stateColumnMigrations = []struct {
	table  string
	column string
	decl   string
}{
	{tableServiceSnapshot, "check_type", "check_type TEXT NOT NULL DEFAULT ''"},
	{tableServiceSnapshot, "unavailable", "unavailable INTEGER NOT NULL DEFAULT 0"},
	{tableServiceSnapshot, "observation", "observation TEXT NOT NULL DEFAULT ''"},
	{tableServiceSnapshot, "config_id", "config_id TEXT NOT NULL DEFAULT ''"},
	{tableServiceSnapshot, "severity", "severity TEXT NOT NULL DEFAULT ''"},
	{tableWatchSnapshot, "check_type", "check_type TEXT NOT NULL DEFAULT ''"},
	{tableWatchSnapshot, "unavailable", "unavailable INTEGER NOT NULL DEFAULT 0"},
	{tableWatchSnapshot, "observation", "observation TEXT NOT NULL DEFAULT ''"},
	{tableWatchSnapshot, "config_id", "config_id TEXT NOT NULL DEFAULT ''"},
	{tableWatchSnapshot, "severity", "severity TEXT NOT NULL DEFAULT ''"},
	{"watch_runtime_state", "unavailable", "unavailable INTEGER NOT NULL DEFAULT 0"},
}

// ensureStateColumns adds any missing cache/control-table columns to a database
// created under an older schema.
func ensureStateColumns(ctx context.Context, tx *sql.Tx) error {
	for _, m := range stateColumnMigrations {
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
