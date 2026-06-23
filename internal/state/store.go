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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Filename is the database file name placed under the state directory.
const Filename = "sermo.db"

// Sources record who last changed a monitoring state row, for inspection.
const (
	SourceConfig = "config" // daemon applied an entry's `monitor` flag
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
}

// Store is a handle to the persistent state database. It is safe for concurrent
// use; access is serialized onto a single connection (the store is low-traffic
// and this avoids cross-process "database is locked" surprises).
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// PruneUnconfiguredControlStatesResult summarizes stale control state removed
// from the persistent state database.
type PruneUnconfiguredControlStatesResult struct {
	Services []string
	Rows     int64
}

// DefaultHistoryRetention is the normal SLA/metrics/event history window kept
// unless an operator runs state compact with an explicit --before cutoff.
const DefaultHistoryRetention = 366 * 24 * time.Hour

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

// Open opens (creating if needed) the database at path, creating the parent
// directory and running any pending migrations. WAL mode plus a busy timeout let
// the daemon (long-lived reader/writer) and sermoctl (short-lived writer)
// coexist across processes.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		// Owner-only (root): the state DB holds control state and history, not
		// secrets, but there is no reason for it to be world-traversable. Matches the
		// packaging (tmpfiles.d / OpenRC) mode. MkdirAll leaves an existing dir's
		// mode untouched, so a pre-created 0700 dir is preserved.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create state dir %s: %w", dir, err)
		}
	}

	// synchronous=NORMAL is safe under WAL (no corruption risk; at worst the last
	// few committed cycles are lost on a power cut) and avoids an fsync on every
	// commit — the per-cycle SLA/measurement writes would otherwise each force a
	// disk sync.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)"
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

// MonitorRecord is one persisted monitoring state row.
type MonitorRecord struct {
	Active    bool
	Source    string
	UpdatedAt time.Time
}

// MonitorState returns a persisted monitoring row. found is false when the entry
// has no recorded state yet.
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

// Active reports whether monitoring is currently active for an entry. found is
// false when the entry has no recorded state yet (the caller decides the
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

// SetActive records an entry's monitoring state, upserting the row. source notes
// who set it (SourceConfig, SourceCLI, SourceDaemon, SourceWeb) for inspection.
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
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO global_state (key, value, source, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   value      = excluded.value,
		   source     = excluded.source,
		   updated_at = excluded.updated_at;`,
		panicFlagKey, v, source, s.now().UTC().Format(time.RFC3339),
	)
	return err
}

// Panic returns the persisted panic-mode flag. found is false when no row has
// been written yet (the caller treats that as panic off).
func (s *Store) Panic() (rec GlobalRecord, found bool, err error) {
	var v int
	var source, updated string
	err = s.db.QueryRow(
		`SELECT value, source, updated_at FROM global_state WHERE key = ?;`,
		panicFlagKey,
	).Scan(&v, &source, &updated)
	switch {
	case err == sql.ErrNoRows:
		return GlobalRecord{}, false, nil
	case err != nil:
		return GlobalRecord{}, false, err
	default:
		at, _ := time.Parse(time.RFC3339, updated)
		return GlobalRecord{On: v != 0, Source: source, UpdatedAt: at}, true, nil
	}
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

// RemediationState returns a service's persisted automatic-remediation state.
// found is false when no action state has been recorded yet.
func (s *Store) RemediationState(service string) (RemediationRecord, bool, error) {
	var (
		lastActionAt     int64
		recentActions    string
		currentBackoffNS int64
	)
	err := s.db.QueryRow(
		`SELECT last_action_at, recent_actions, current_backoff_ns
		   FROM remediation_state WHERE service = ?;`,
		service,
	).Scan(&lastActionAt, &recentActions, &currentBackoffNS)
	switch {
	case err == sql.ErrNoRows:
		return RemediationRecord{}, false, nil
	case err != nil:
		return RemediationRecord{}, false, err
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
		_, err := s.db.Exec(`DELETE FROM remediation_state WHERE service = ?;`, service)
		return err
	}
	recent, err := encodeUnixNanos(rec.RecentActions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO remediation_state (service, last_action_at, recent_actions, current_backoff_ns)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   last_action_at     = excluded.last_action_at,
		   recent_actions     = excluded.recent_actions,
		   current_backoff_ns = excluded.current_backoff_ns;`,
		service, timeUnixNano(rec.LastActionAt), recent, int64(rec.CurrentBackoff),
	)
	return err
}

// RuleWindowStates returns the persisted for/within progress for a service's
// rules, keyed by rule name.
func (s *Store) RuleWindowStates(service string) (map[string]RuleWindowRecord, error) {
	rows, err := s.db.Query(
		`SELECT rule_name, consecutive, history, true_since, timed_history
		   FROM rule_window_state WHERE service = ? ORDER BY rule_name;`,
		service,
	)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		var history []bool
		if err := json.Unmarshal([]byte(rawHistory), &history); err != nil {
			return nil, err
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
	return out, rows.Err()
}

// SetRuleWindowStates replaces the persisted rule-window state for a service.
// Passing an empty map removes stale rows for rules that no longer exist.
func (s *Store) SetRuleWindowStates(service string, records map[string]RuleWindowRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM rule_window_state WHERE service = ?;`, service); err != nil {
		return err
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rec := records[name]
		history, err := json.Marshal(rec.History)
		if err != nil {
			return err
		}
		timed, err := encodeRuleWindowSamples(rec.TimedHistory)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO rule_window_state (service, rule_name, consecutive, history, true_since, timed_history)
			 VALUES (?, ?, ?, ?, ?, ?);`,
			service, name, rec.Consecutive, string(history), timeUnixNano(rec.TrueSince), timed,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		return "", err
	}
	return string(b), nil
}

func decodeUnixNanos(raw string) ([]time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	var nanos []int64
	if err := json.Unmarshal([]byte(raw), &nanos); err != nil {
		return nil, err
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
		return "", err
	}
	return string(b), nil
}

func decodeRuleWindowSamples(raw string) ([]RuleWindowSample, error) {
	if raw == "" {
		return nil, nil
	}
	var encoded []ruleWindowSampleJSON
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, err
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
	At      time.Time
	Service string
	Watch   string
	App     string
	Kind    string
	Rule    string
	Action  string
	Status  string
	Message string
}

// RecordEvent appends one event to the persistent event/activity feed.
func (s *Store) RecordEvent(e EventRecord) error {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.Exec(
		`INSERT INTO event_log (at, service, watch, app, kind, rule, action, status, message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		at.UTC().UnixNano(), e.Service, e.Watch, e.App, e.Kind, e.Rule, e.Action, e.Status, e.Message,
	)
	return err
}

// RecentEvents returns the newest persisted events first. limit <= 0 returns all
// persisted events.
func (s *Store) RecentEvents(limit int) ([]EventRecord, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.Query(
		`SELECT at, service, watch, app, kind, rule, action, status, message
		   FROM event_log ORDER BY at DESC, id DESC LIMIT ?;`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRecord
	for rows.Next() {
		var rec EventRecord
		var at int64
		if err := rows.Scan(&at, &rec.Service, &rec.Watch, &rec.App, &rec.Kind, &rec.Rule, &rec.Action, &rec.Status, &rec.Message); err != nil {
			return nil, err
		}
		rec.At = time.Unix(0, at).UTC()
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PruneEvents deletes event rows older than before. If before is zero, every
// persisted event is deleted.
func (s *Store) PruneEvents(before time.Time) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if before.IsZero() {
		res, err = s.db.Exec(`DELETE FROM event_log;`)
	} else {
		res, err = s.db.Exec(`DELETE FROM event_log WHERE at < ?;`, before.UTC().UnixNano())
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// SLAWindows are the reported rolling windows, shortest first. Segment counts
// pick a natural human sub-span per window (5-minute, hourly, 6-hourly, daily,
// monthly) so each timeline cell reads as a meaningful slice of time.
var SLAWindows = []SLAWindow{
	{"hour", time.Hour, 12},
	{"day", 24 * time.Hour, 24},
	{"week", 7 * 24 * time.Hour, 28},
	{"month", 30 * 24 * time.Hour, 30},
	{"year", 365 * 24 * time.Hour, 12},
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

// RecordCheckSLA accumulates one observed check execution into its current
// UTC-minute bucket. Interval-deferred checks are not recorded by callers, so
// the per-check SLA reflects only real check runs.
func (s *Store) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	u := 0
	if up {
		u = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO check_sla_sample (service, check_name, bucket, up_count, total_count)
		 VALUES (?, ?, ?, ?, 1)
		 ON CONFLICT(service, check_name, bucket) DO UPDATE SET
		   up_count    = up_count + excluded.up_count,
		   total_count = total_count + excluded.total_count;`,
		service, check, minuteBucket(at), u,
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

// CheckSLA sums one check's up and total observed executions over the rolling
// window ending at now. total==0 means no data.
func (s *Store) CheckSLA(service, check string, span time.Duration, now time.Time) (up, total int64, err error) {
	from := minuteBucket(now.Add(-span))
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)
		 FROM check_sla_sample WHERE service = ? AND check_name = ? AND bucket >= ?;`,
		service, check, from,
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

// CheckSLASeries returns one check's per-minute availability points in [from,
// to), oldest first. Unobserved minutes are absent.
func (s *Store) CheckSLASeries(service, check string, from, to time.Time) ([]SLAPoint, error) {
	rows, err := s.db.Query(
		`SELECT bucket, up_count, total_count
		   FROM check_sla_sample
		  WHERE service = ? AND check_name = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		service, check, minuteBucket(from), minuteBucket(to),
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
		endBucket := minuteBucket(now) + 60
		segSpan := spanSec / int64(segCount)
		if segSpan <= 0 {
			segSpan = 1
		}

		// Placeholder order matches the SQL left-to-right: the SELECT segment
		// expression (start, span) first, then the WHERE key args, then the range.
		args := make([]any, 0, len(keyArgs)+4)
		args = append(args, startBucket, segSpan)
		args = append(args, keyArgs...)
		args = append(args, startBucket, endBucket)
		rows, err := s.db.Query(query, args...)
		if err != nil {
			return nil, err
		}

		segs := make([]SLASegment, segCount)
		var winUp, winTotal int64
		for rows.Next() {
			var seg, up, total int64
			if err := rows.Scan(&seg, &up, &total); err != nil {
				rows.Close()
				return nil, err
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
			return nil, err
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
	return summaryFromRow(s.db.QueryRow(
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
		return MeasurementStat{}, err
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
	return s.pruneBuckets("measurement", before)
}

// pruneBuckets deletes rows with a bucket older than before from one of the
// per-minute bucket tables. table is always a compile-time literal, never
// operator input.
func (s *Store) pruneBuckets(table string, before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM `+table+` WHERE bucket < ?;`, minuteBucket(before)) //nolint:gosec // table is a package-internal literal
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecordMetric accumulates one observation of a named per-check metric (e.g.
// hdparm "read" MB/s) into its current UTC-minute bucket: n+1, sum+value and the
// running min/max. It is the generic counterpart of RecordMeasurement (latency).
func (s *Store) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO measurement_metric (service, check_name, metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(service, check_name, metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`,
		service, check, metric, minuteBucket(at), value, value, value,
	)
	return err
}

// MetricSummary returns a named metric's average/min/max and sample count over the
// rolling window ending at now.
func (s *Store) MetricSummary(service, check, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRow(
		`SELECT COALESCE(SUM(n),0), SUM(sum_v), MIN(min_v), MAX(max_v)
		   FROM measurement_metric WHERE service = ? AND check_name = ? AND metric = ? AND bucket >= ?;`,
		service, check, metric, minuteBucket(now.Add(-span))))
}

// MetricSeries returns a named metric's per-minute points in [from, to), oldest
// first (minutes with no observation are absent).
func (s *Store) MetricSeries(service, check, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	rows, err := s.db.Query(
		`SELECT bucket, n, sum_v, min_v, max_v
		   FROM measurement_metric
		  WHERE service = ? AND check_name = ? AND metric = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		service, check, metric, minuteBucket(from), minuteBucket(to),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minV, maxV float64
		if err := rows.Scan(&bucket, &n, &sum, &minV, &maxV); err != nil {
			return nil, err
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minV, Max: maxV})
	}
	return out, rows.Err()
}

// PruneMetrics deletes named-metric buckets older than before. Returns rows removed.
func (s *Store) PruneMetrics(before time.Time) (int64, error) {
	return s.pruneBuckets("measurement_metric", before)
}

// RecordDaemonMetric accumulates one sermod process metric observation into its
// current UTC-minute bucket: n+1, sum+value and running min/max.
func (s *Store) RecordDaemonMetric(metric string, value float64, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO daemon_metric (metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, 1, ?, ?, ?)
		 ON CONFLICT(metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`,
		metric, minuteBucket(at), value, value, value,
	)
	return err
}

// DaemonMetricSummary returns a daemon metric's average/min/max and sample count
// over the rolling window ending at now.
func (s *Store) DaemonMetricSummary(metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRow(
		`SELECT COALESCE(SUM(n),0), SUM(sum_v), MIN(min_v), MAX(max_v)
		   FROM daemon_metric WHERE metric = ? AND bucket >= ?;`,
		metric, minuteBucket(now.Add(-span))))
}

// DaemonMetricSeries returns a daemon metric's per-minute points in [from, to),
// oldest first.
func (s *Store) DaemonMetricSeries(metric string, from, to time.Time) ([]MeasurementPoint, error) {
	rows, err := s.db.Query(
		`SELECT bucket, n, sum_v, min_v, max_v
		   FROM daemon_metric
		  WHERE metric = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		metric, minuteBucket(from), minuteBucket(to),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minV, maxV float64
		if err := rows.Scan(&bucket, &n, &sum, &minV, &maxV); err != nil {
			return nil, err
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minV, Max: maxV})
	}
	return out, rows.Err()
}

// PruneDaemonMetrics deletes daemon metric buckets older than before. Returns rows removed.
func (s *Store) PruneDaemonMetrics(before time.Time) (int64, error) {
	return s.pruneBuckets("daemon_metric", before)
}

// RecordServiceMetric accumulates one service process-tree metric observation
// into its current UTC-minute bucket: n+1, sum+value and running min/max.
func (s *Store) RecordServiceMetric(service, metric string, value float64, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO service_metric (service, metric, bucket, n, sum_v, min_v, max_v)
		 VALUES (?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(service, metric, bucket) DO UPDATE SET
		   n     = n + 1,
		   sum_v = sum_v + excluded.sum_v,
		   min_v = min(min_v, excluded.min_v),
		   max_v = max(max_v, excluded.max_v);`,
		service, metric, minuteBucket(at), value, value, value,
	)
	return err
}

// ServiceMetricSummary returns a service runtime metric's average/min/max and
// sample count over the rolling window ending at now.
func (s *Store) ServiceMetricSummary(service, metric string, span time.Duration, now time.Time) (MeasurementStat, error) {
	return summaryFromRow(s.db.QueryRow(
		`SELECT COALESCE(SUM(n),0), SUM(sum_v), MIN(min_v), MAX(max_v)
		   FROM service_metric WHERE service = ? AND metric = ? AND bucket >= ?;`,
		service, metric, minuteBucket(now.Add(-span))))
}

// ServiceMetricSeries returns a service runtime metric's per-minute points in
// [from, to), oldest first.
func (s *Store) ServiceMetricSeries(service, metric string, from, to time.Time) ([]MeasurementPoint, error) {
	rows, err := s.db.Query(
		`SELECT bucket, n, sum_v, min_v, max_v
		   FROM service_metric
		  WHERE service = ? AND metric = ? AND bucket >= ? AND bucket < ?
		  ORDER BY bucket;`,
		service, metric, minuteBucket(from), minuteBucket(to),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MeasurementPoint
	for rows.Next() {
		var bucket, n int64
		var sum, minV, maxV float64
		if err := rows.Scan(&bucket, &n, &sum, &minV, &maxV); err != nil {
			return nil, err
		}
		avg := 0.0
		if n > 0 {
			avg = sum / float64(n)
		}
		out = append(out, MeasurementPoint{Start: time.Unix(bucket, 0).UTC(), N: n, Avg: avg, Min: minV, Max: maxV})
	}
	return out, rows.Err()
}

// PruneServiceMetrics deletes service runtime metric buckets older than before.
// Returns rows removed.
func (s *Store) PruneServiceMetrics(before time.Time) (int64, error) {
	return s.pruneBuckets("service_metric", before)
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
// (control state, SLA samples, check measurements or runtime metrics), so
// diagnostics can flag rows for services no longer in the configuration.
func (s *Store) TrackedServices() ([]string, error) {
	return s.queryNames(`SELECT service FROM monitor_state
		UNION SELECT service FROM remediation_state
		UNION SELECT service FROM rule_window_state
		UNION SELECT service FROM sla_sample
		UNION SELECT service FROM check_sla_sample
		UNION SELECT service FROM measurement
		UNION SELECT service FROM measurement_metric
		UNION SELECT service FROM service_metric ORDER BY service;`)
}

// TrackedControlStates returns the distinct service/watch names that have
// persisted runtime control state. Diagnostics use this narrower view so
// historical metrics do not make a removed target look like a live problem.
func (s *Store) TrackedControlStates() ([]string, error) {
	return s.queryNames(`SELECT service FROM monitor_state
		UNION SELECT service FROM remediation_state
		UNION SELECT service FROM rule_window_state ORDER BY service;`)
}

func (s *Store) queryNames(query string) ([]string, error) {
	rows, err := s.db.Query(query)
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

// PruneUnconfiguredControlStates removes persisted runtime control state for
// services or watches absent from the configured target list. It intentionally
// leaves SLA, measurements, events and runtime metrics untouched; history
// maintenance is handled by PruneHistory and Compact.
func (s *Store) PruneUnconfiguredControlStates(configured []string) (PruneUnconfiguredControlStatesResult, error) {
	tracked, err := s.TrackedControlStates()
	if err != nil {
		return PruneUnconfiguredControlStatesResult{}, err
	}
	known := make(map[string]struct{}, len(configured))
	for _, name := range configured {
		known[name] = struct{}{}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return PruneUnconfiguredControlStatesResult{}, err
	}
	defer tx.Rollback()

	var result PruneUnconfiguredControlStatesResult
	for _, service := range tracked {
		if _, ok := known[service]; ok {
			continue
		}
		rows, err := deleteControlState(tx, service)
		if err != nil {
			return PruneUnconfiguredControlStatesResult{}, err
		}
		result.Services = append(result.Services, service)
		result.Rows += rows
	}
	if err := tx.Commit(); err != nil {
		return PruneUnconfiguredControlStatesResult{}, err
	}
	return result, nil
}

func deleteControlState(tx *sql.Tx, service string) (int64, error) {
	var rows int64
	for _, stmt := range [...]string{
		`DELETE FROM monitor_state WHERE service = ?;`,
		`DELETE FROM remediation_state WHERE service = ?;`,
		`DELETE FROM rule_window_state WHERE service = ?;`,
	} {
		res, err := tx.Exec(stmt, service)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		rows += n
	}
	return rows, nil
}

// PruneSLA deletes SLA buckets older than before, bounding the table to roughly
// one year of per-minute samples per service. Returns the rows removed.
func (s *Store) PruneSLA(before time.Time) (int64, error) {
	total, err := s.pruneBuckets("sla_sample", before)
	if err != nil {
		return 0, err
	}
	rows, err := s.pruneBuckets("check_sla_sample", before)
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
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM;`); err != nil {
		return err
	}
	return nil
}
