package state

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// The two snapshot tables share one column shape; each keeps its own
// compile-time SQL keyed by its own group column.
const (
	upsertServiceCheckSnapshotSQL = `INSERT INTO service_check_snapshot
	   (service, check_name, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(service, check_name) DO UPDATE SET` + checkSnapshotUpdateColumns
	deleteServiceCheckSnapshotSQL = `DELETE FROM service_check_snapshot WHERE service = ? AND check_name = ?;`
	upsertWatchCheckSnapshotSQL   = `INSERT INTO watch_check_snapshot
	   (watch, slot, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(watch, slot) DO UPDATE SET` + checkSnapshotUpdateColumns
	deleteWatchCheckSnapshotSQL = `DELETE FROM watch_check_snapshot WHERE watch = ? AND slot = ?;`
	checkSnapshotUpdateColumns  = `
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
	   severity   = excluded.severity;`
)

// CheckSnapshotDelta is the change one snapshot group (a service or a watch)
// needs in the store: the rows to upsert, keyed by check name or watch slot,
// and the names whose rows to delete.
type CheckSnapshotDelta struct {
	Upsert map[string]CheckSnapshotRecord
	Delete []string
}

// ServiceCheckSnapshots returns every persisted service check snapshot, grouped
// by service name and keyed by check name.
func (s *Store) ServiceCheckSnapshots() (map[string]map[string]CheckSnapshotRecord, error) {
	return s.groupedCheckSnapshots(
		`SELECT service, check_name, check_type, observation, ok, condition, optional, skipped, unavailable, message, data, ran, at, config_id, severity
		   FROM service_check_snapshot ORDER BY service, check_name;`,
		deleteServiceCheckSnapshotSQL,
		"service check snapshots",
	)
}

// UpdateServiceCheckSnapshots applies each service's snapshot delta in one
// transaction.
func (s *Store) UpdateServiceCheckSnapshots(deltas map[string]CheckSnapshotDelta) error {
	return s.updateCheckSnapshots(upsertServiceCheckSnapshotSQL, deleteServiceCheckSnapshotSQL, "service check snapshot", deltas)
}

// UpdateWatchCheckSnapshots applies each host watch's slot delta in one
// transaction.
func (s *Store) UpdateWatchCheckSnapshots(deltas map[string]CheckSnapshotDelta) error {
	return s.updateCheckSnapshots(upsertWatchCheckSnapshotSQL, deleteWatchCheckSnapshotSQL, "watch check snapshot", deltas)
}

// updateCheckSnapshots writes snapshot deltas in group and name order. Rows are
// validated and encoded, and both statements warmed in the store's prepared
// cache, before the transaction takes the single write connection; the
// transaction then reuses them on that connection.
func (s *Store) updateCheckSnapshots(upsertSQL, deleteSQL, what string, deltas map[string]CheckSnapshotDelta) error {
	type upsertRow struct {
		group, name string
		args        []any
	}
	var rows []upsertRow
	var deletes [][2]string
	for _, group := range slices.Sorted(maps.Keys(deltas)) {
		delta := deltas[group]
		for _, name := range slices.Sorted(slices.Values(delta.Delete)) {
			deletes = append(deletes, [2]string{group, name})
		}
		for _, name := range slices.Sorted(maps.Keys(delta.Upsert)) {
			rec := delta.Upsert[name]
			if err := validateCheckSnapshotObservation(rec.Observation); err != nil {
				return fmt.Errorf("%s %s/%s: %w", what, group, name, err)
			}
			data, err := EncodeCheckSnapshotData(rec.Data)
			if err != nil {
				return fmt.Errorf("%s %s/%s: %w", what, group, name, err)
			}
			rows = append(rows, upsertRow{group: group, name: name, args: checkSnapshotArgs(rec, data, group, name)})
		}
	}
	if len(rows) == 0 && len(deletes) == 0 {
		return nil
	}
	ctx := s.sqlCtx()
	if err := s.statements.warm(ctx, s.db.PrepareContext, upsertSQL, deleteSQL); err != nil {
		return fmt.Errorf("prepare %s update: %w", what, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s update: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range deletes {
		if err := s.statements.execInTx(ctx, tx, deleteSQL, key[0], key[1]); err != nil {
			return fmt.Errorf("delete %s %s/%s: %w", what, key[0], key[1], err)
		}
	}
	for _, row := range rows {
		if err := s.statements.execInTx(ctx, tx, upsertSQL, row.args...); err != nil {
			return fmt.Errorf("upsert %s %s/%s: %w", what, row.group, row.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s update: %w", what, err)
	}
	return nil
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
		deleteWatchCheckSnapshotSQL,
		"watch check snapshots",
	)
}

// groupedCheckSnapshots loads one snapshot table. A row that cannot be decoded
// (a legacy row with no observation, a slot a removed check left behind) is
// skipped rather than discarding every other snapshot: the result holds the
// decodable rows, and the error names each skipped one. Snapshots are a cache
// the next cycle rewrites for every slot still configured, so a skipped row is
// also deleted with deleteSQL (group, slot): a row whose target is no longer
// configured is never rewritten and would otherwise be reported on every start.
func (s *Store) groupedCheckSnapshots(query, deleteSQL, label string) (map[string]map[string]CheckSnapshotRecord, error) {
	rows, err := s.reads().QueryContext(s.sqlCtx(), query)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", label, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]CheckSnapshotRecord{}
	var skipped []error
	var undecodable [][2]string
	for rows.Next() {
		group, slot, record, err := scanCheckSnapshotRow(rows, label)
		if err != nil {
			if group == "" {
				return nil, err // the row itself could not be scanned
			}
			skipped = append(skipped, fmt.Errorf("%s/%s skipped: %w", group, slot, err))
			undecodable = append(undecodable, [2]string{group, slot})
			continue
		}
		if out[group] == nil {
			out[group] = map[string]CheckSnapshotRecord{}
		}
		out[group][slot] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	if err := s.deleteSnapshotRows(deleteSQL, undecodable); err != nil {
		skipped = append(skipped, err)
	}
	return out, errors.Join(skipped...)
}

// deleteSnapshotRows removes the (group, slot) rows a load could not decode,
// in one transaction.
func (s *Store) deleteSnapshotRows(deleteSQL string, keys [][2]string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(s.sqlCtx(), nil)
	if err != nil {
		return fmt.Errorf("begin delete of skipped snapshots: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range keys {
		if _, err := tx.ExecContext(s.sqlCtx(), deleteSQL, key[0], key[1]); err != nil {
			return fmt.Errorf("delete skipped %s/%s: %w", key[0], key[1], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete of skipped snapshots: %w", err)
	}
	return nil
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

// EncodeCheckSnapshotData is the stored form of a snapshot's data. Callers that
// compare readings against stored rows use it so a reading decoded from the
// store and one fresh from a check compare equal.
func EncodeCheckSnapshotData(data map[string]any) (string, error) {
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
