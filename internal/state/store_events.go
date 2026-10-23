package state

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

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
	// Severity grades the event ("" for events that carry no grade).
	Severity string
}

// RecordEvent appends one event to the persistent event/activity feed.
func (s *Store) RecordEvent(e EventRecord) (int64, error) {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	result, err := s.exec(s.sqlCtx(),
		`INSERT INTO event_log (at, service, watch, app, kind, rule, action, status, message, output, severity)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		at.UTC().UnixNano(), e.Service, e.Watch, e.App, e.Kind, e.Rule, e.Action, e.Status, e.Message, e.Output, e.Severity,
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
const eventSelectPrefix = `SELECT id, at, service, watch, app, kind, rule, action, status, message, output, severity
		   FROM event_log`

// RecentEventsBefore returns persisted events newest first. beforeID <= 0
// starts at the newest event; otherwise only rows with a smaller ID are read.
// A non-positive limit returns all matching events.
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
	rows, err := s.reader.QueryContext(s.sqlCtx(), query, args...)
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
// column recentEventsQueries knows; anything else is rejected rather than
// interpolated into a query.
func (s *Store) RecentEventsForColumn(column, name string, limit int) ([]EventRecord, error) {
	query, ok := recentEventsQueries[column]
	if !ok {
		return nil, fmt.Errorf("load recent events: unsupported event target column %q", column)
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.reader.QueryContext(s.sqlCtx(), query, name, limit)
	if err != nil {
		return nil, fmt.Errorf("load recent events for %s %q: %w", column, name, err)
	}
	defer func() { _ = rows.Close() }()
	return scanEventRows(rows)
}

// LatestOperationOutcomes returns each service's newest event that records
// the outcome of one of actions: a row naming the service, one of those
// actions and a result status. Later events of other kinds never hide it. An
// empty service reads every service in one query; a named one reads only its
// own. Both go through event_log_service_outcome_idx.
func (s *Store) LatestOperationOutcomes(service string, actions []string) ([]EventRecord, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(actions)+1)
	if service != "" {
		args = append(args, service)
	}
	for _, action := range actions {
		args = append(args, action)
	}
	rows, err := s.reader.QueryContext(s.sqlCtx(), latestOperationOutcomesQuery(service != "", len(actions)), args...)
	if err != nil {
		return nil, fmt.Errorf("load latest operation outcomes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanEventRows(rows)
}

// latestOperationOutcomesQuery selects each service's newest row among those
// naming one of n actions with a status, optionally for one service.
func latestOperationOutcomesQuery(oneService bool, n int) string {
	target := `service <> ''`
	if oneService {
		target = `service = ?`
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
	return eventSelectPrefix + ` WHERE id IN (
		SELECT MAX(id) FROM event_log
		 WHERE status <> '' AND ` + target + ` AND action IN (` + placeholders + `)
		 GROUP BY service);`
}

// IsOutcomeOf reports whether r records the outcome of one of actions on a
// service: the rows LatestOperationOutcomes selects.
func (r EventRecord) IsOutcomeOf(actions []string) bool {
	return r.Service != "" && r.Status != "" && slices.Contains(actions, r.Action)
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

// recentEventsQueries is the allow-list RecentEventsForColumn reads from: one
// query per target column, each a constant, so no caller can reach the SQL with
// an arbitrary identifier.
var recentEventsQueries = map[string]string{
	EventColumnService: eventSelectPrefix + ` WHERE ` + EventColumnService + ` = ? ORDER BY id DESC LIMIT ?;`,
	EventColumnApp:     eventSelectPrefix + ` WHERE ` + EventColumnApp + ` = ? ORDER BY id DESC LIMIT ?;`,
}

func scanEventRows(rows *sql.Rows) ([]EventRecord, error) {
	var out []EventRecord
	for rows.Next() {
		var rec EventRecord
		var at int64
		if err := rows.Scan(&rec.ID, &at, &rec.Service, &rec.Watch, &rec.App, &rec.Kind, &rec.Rule, &rec.Action, &rec.Status, &rec.Message, &rec.Output, &rec.Severity); err != nil {
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
