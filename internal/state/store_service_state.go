package state

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

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
	Phase     string
	UpdatedAt time.Time
}

// ServiceRestartNoticeRecord is the principal process identity last handled by
// the external-restart notice monitor for one service.
type ServiceRestartNoticeRecord struct {
	PID       int
	StartedAt time.Time
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
// who set it (SourceConfig, SourceCLI, SourceWeb) for inspection.
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
func (s *Store) SetOperationSettling(service, phase string) error {
	_, err := s.exec(s.sqlCtx(),
		`INSERT INTO operation_settling (service, action, phase, source, updated_at)
		 VALUES (?, '', ?, '', ?)
		 ON CONFLICT(service) DO UPDATE SET
		   phase      = excluded.phase,
		   updated_at = excluded.updated_at;`,
		service, phase, s.now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("set operation settling for %s: %w", service, err)
	}
	return nil
}

// OperationSettling returns a service's current operation-settling row.
func (s *Store) OperationSettling(service string) (OperationSettlingRecord, bool, error) {
	var phase, updated string
	err := s.reads().QueryRowContext(s.sqlCtx(),
		`SELECT phase, updated_at FROM operation_settling WHERE service = ?;`,
		service,
	).Scan(&phase, &updated)
	switch {
	case err == sql.ErrNoRows:
		return OperationSettlingRecord{}, false, nil
	case err != nil:
		return OperationSettlingRecord{}, false, fmt.Errorf("load operation settling for %s: %w", service, err)
	default:
		at, _ := time.Parse(time.RFC3339, updated)
		return OperationSettlingRecord{Phase: phase, UpdatedAt: at}, true, nil
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
