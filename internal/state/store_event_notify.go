package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EventNotifyRecord is the last delivered state of one incident at one notifier.
// Active records can receive reminders; closed and operational-error records
// retain their last delivery time to suppress duplicates.
type EventNotifyRecord struct {
	IncidentKey string
	Notifier    string
	Phase       string
	Active      bool
	LastSentAt  time.Time
	Subject     string
	Body        string
	// Severity is the gravest level delivered for the incident at this
	// notifier; a recovery and every reminder repeat it.
	Severity string
}

// EventNotifyState returns the last delivered state for an incident and target.
func (s *Store) EventNotifyState(incidentKey, notifier string) (EventNotifyRecord, bool, error) {
	var rec EventNotifyRecord
	var active int
	var lastSentAt int64
	err := s.reads().QueryRowContext(s.sqlCtx(),
		`SELECT phase, active, last_sent_at, subject, body, severity FROM event_notify_state
		 WHERE incident_key = ? AND notifier = ?`, incidentKey, notifier,
	).Scan(&rec.Phase, &active, &lastSentAt, &rec.Subject, &rec.Body, &rec.Severity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EventNotifyRecord{}, false, nil
		}
		return EventNotifyRecord{}, false, fmt.Errorf("load event notification state: %w", err)
	}
	rec.IncidentKey, rec.Notifier = incidentKey, notifier
	rec.Active = intBool(active)
	rec.LastSentAt = time.Unix(0, lastSentAt).UTC()
	return rec, true, nil
}

// SetEventNotifyState records one successful external delivery.
func (s *Store) SetEventNotifyState(rec EventNotifyRecord) error {
	_, err := s.exec(s.sqlCtx(),
		`INSERT INTO event_notify_state (incident_key, notifier, phase, active, last_sent_at, subject, body, severity)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (incident_key, notifier) DO UPDATE SET
		 phase = excluded.phase, active = excluded.active, last_sent_at = excluded.last_sent_at,
		 subject = excluded.subject, body = excluded.body, severity = excluded.severity`,
		rec.IncidentKey, rec.Notifier, rec.Phase, boolInt(rec.Active), rec.LastSentAt.UTC().UnixNano(), rec.Subject, rec.Body, rec.Severity,
	)
	if err != nil {
		return fmt.Errorf("persist event notification state: %w", err)
	}
	return nil
}

// DueEventNotifyStates lists active incidents whose last notification is old
// enough for a reminder. The caller supplies its configured interval.
func (s *Store) DueEventNotifyStates(notifier string, before time.Time) ([]EventNotifyRecord, error) {
	rows, err := s.reads().QueryContext(s.sqlCtx(),
		`SELECT incident_key, phase, last_sent_at, subject, body, severity FROM event_notify_state
		 WHERE notifier = ? AND active = 1 AND last_sent_at <= ? ORDER BY last_sent_at`,
		notifier, before.UTC().UnixNano(),
	)
	if err != nil {
		return nil, fmt.Errorf("list due event notifications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []EventNotifyRecord
	for rows.Next() {
		var rec EventNotifyRecord
		var sent int64
		if err := rows.Scan(&rec.IncidentKey, &rec.Phase, &sent, &rec.Subject, &rec.Body, &rec.Severity); err != nil {
			return nil, fmt.Errorf("scan due event notification: %w", err)
		}
		rec.Notifier, rec.Active, rec.LastSentAt = notifier, true, time.Unix(0, sent).UTC()
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read due event notifications: %w", err)
	}
	return records, nil
}

// ActiveEventNotifyIncidents lists the distinct incident keys that still have
// an open (reminder-eligible) delivery at any notifier.
func (s *Store) ActiveEventNotifyIncidents() ([]string, error) {
	rows, err := s.reads().QueryContext(s.sqlCtx(),
		`SELECT DISTINCT incident_key FROM event_notify_state WHERE active = 1 ORDER BY incident_key`)
	if err != nil {
		return nil, fmt.Errorf("list active event notifications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan active event notification: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active event notifications: %w", err)
	}
	return keys, nil
}

// DeleteEventNotifyIncident forgets one incident at every notifier.
func (s *Store) DeleteEventNotifyIncident(incidentKey string) error {
	if _, err := s.exec(s.sqlCtx(), `DELETE FROM event_notify_state WHERE incident_key = ?`, incidentKey); err != nil {
		return fmt.Errorf("delete event notification state: %w", err)
	}
	return nil
}
