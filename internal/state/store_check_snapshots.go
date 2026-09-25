package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"sermo/internal/checks"
)

// CheckSnapshotRecord is one persisted latest check result. CheckType identifies
// the check that produced the data so callers never decode a prior result as a
// new check type; the enclosing map owns the service check name or watch slot.
type CheckSnapshotRecord struct {
	CheckType   string
	ConfigID    string
	Observation checks.ObservationState
	OK          bool
	Condition   bool
	Optional    bool
	Skipped     bool
	Unavailable bool
	Message     string
	Data        map[string]any
	Ran         bool // executed this cycle, rather than reused from the interval cache
	At          time.Time
	// Severity is the grade the check gave its own result ("" means the
	// declaration decides, and is what rows persisted before the column existed
	// carry).
	Severity string
}

// ServiceCheckSnapshots returns every persisted service check snapshot, grouped
// by service name and keyed by check name.
func (s *Store) ServiceCheckSnapshots() (map[string]map[string]CheckSnapshotRecord, error) {
	return s.groupedCheckSnapshots(
		`SELECT service, check_name, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity
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
				   (service, check_name, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
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
	for _, name := range slices.Sorted(maps.Keys(records)) {
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
		`SELECT watch, slot, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity
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
		record      CheckSnapshotRecord
		group       string
		slot        string
		ok          int
		cond        int
		optional    int
		skipped     int
		unavailable int
		rawData     string
		ran         int
		at          int64
	)
	if err := rows.Scan(&group, &slot, &record.CheckType, &record.Observation, &ok, &cond, &optional, &skipped, &unavailable, &record.Message, &rawData, &ran, &at, &record.ConfigID, &record.Severity); err != nil {
		return "", "", CheckSnapshotRecord{}, fmt.Errorf("scan %s: %w", label, err)
	}
	if err := validateCheckSnapshotObservation(record.Observation); err != nil {
		return group, slot, CheckSnapshotRecord{}, fmt.Errorf("decode check snapshot %s: %w", slot, err)
	}
	data, err := decodeSnapshotData(rawData)
	if err != nil {
		return group, slot, CheckSnapshotRecord{}, err
	}
	record.OK, record.Condition, record.Optional = intBool(ok), intBool(cond), intBool(optional)
	record.Skipped, record.Unavailable, record.Ran = intBool(skipped), intBool(unavailable), intBool(ran)
	record.Data, record.At = data, unixNanoTime(at)
	return group, slot, record, nil
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
		   (watch, slot, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watch, slot) DO UPDATE SET
		   check_type = excluded.check_type,
		   config_id  = excluded.config_id,
		   observation = excluded.observation,
		   ok         = excluded.ok,
		   condition  = excluded.condition,
		   optional   = excluded.optional,
		   skipped    = excluded.skipped,
		   unavailable = excluded.unavailable,
		   message    = excluded.message,
		   data       = excluded.data,
		   ran        = excluded.ran,
		   at         = excluded.at,
		   severity   = excluded.severity;`,
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
		boolInt(rec.Unavailable), rec.Message, data, boolInt(rec.Ran), timeUnixNano(rec.At), rec.ConfigID, rec.Severity,
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
