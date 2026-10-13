package checks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

const (
	postgresVersionActivityState = 90200
	postgresVersionWaitEvents    = 90600
	postgresVersionBackendType   = 100000
	postgresVersionQuery         = "SHOW server_version_num"
	pgStateActive                = "active"
	// Share the client-backend restriction between sampling and atomic cancellation.
	postgresClientBackendPredicate = "backend_type = 'client backend'"
)

// postgresServerInfo caches the activity view's capabilities, just as the
// MySQL sampler caches its processlist capabilities. Errors force re-detection.
type postgresServerInfo struct {
	mu      sync.Mutex
	version int
}

func postgresActivityQuery(version int) string {
	activity := "pg_stat_activity"
	client := postgresClientBackendPredicate
	wait := "COALESCE(wait_event_type || '/' || wait_event, '')"
	if version < postgresVersionBackendType {
		client = "query NOT LIKE 'autovacuum:%'"
	}
	if version < postgresVersionWaitEvents {
		wait = "CASE WHEN waiting THEN 'Lock' ELSE '' END"
	}
	if version < postgresVersionActivityState {
		// Before 9.2, idle states were sentinel strings in current_query.
		activity = `(SELECT procpid AS pid, usename, datname, client_addr, client_port,
  backend_start, query_start, waiting, current_query AS query,
  CASE current_query WHEN '<IDLE>' THEN 'idle'
    WHEN '<IDLE> in transaction' THEN 'idle in transaction'
    WHEN '<IDLE> in transaction (aborted)' THEN 'idle in transaction (aborted)'
    WHEN '<insufficient privilege>' THEN NULL WHEN '<command string not enabled>' THEN NULL
    ELSE 'active' END AS state FROM pg_stat_activity) AS activity`
	}
	return fmt.Sprintf(`SELECT pid, COALESCE(usename, ''), COALESCE(datname, ''),
  COALESCE(host(client_addr) || ':' || client_port, ''), COALESCE(state, ''),
  %s, %s, %s, (extract(epoch FROM (now() - query_start)) * %d)::bigint,
  substring(query FROM 1 FOR $2), char_length(query)
FROM %s
WHERE %s AND pid <> pg_backend_pid()
  AND query_start IS NOT NULL AND state = ANY($1)
  AND ($3::bigint = 0 OR pid = $3)
ORDER BY query_start LIMIT %d`, wait, pgEpochMicros("backend_start"), pgEpochMicros("query_start"),
		msPerSecond, activity, client, dbQueryFetchLimit+1)
}

func samplePostgresQueries(ctx context.Context, db *sql.DB, states []string, maxLength int, id int64, server *postgresServerInfo) ([]DBQuery, error) {
	if server == nil {
		server = &postgresServerInfo{}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.version == 0 {
		if err := db.QueryRowContext(ctx, postgresVersionQuery).Scan(&server.version); err != nil {
			return nil, fmt.Errorf("db_queries: PostgreSQL version: %w", err)
		}
	}
	// Older views cannot enforce the existing client-backend authorization.
	// Monitoring compatibility must not silently broaden cancellation rights.
	if id > 0 && server.version < postgresVersionBackendType {
		return nil, fmt.Errorf("db_queries: cancellation requires PostgreSQL 10 or newer; server version is %d", server.version)
	}
	out, err := readPostgresQueries(ctx, db, states, maxLength, id, server.version)
	if err != nil {
		server.version = 0
	}
	return out, err
}

func readPostgresQueries(ctx context.Context, db *sql.DB, states []string, maxLength int, id int64, version int) ([]DBQuery, error) {
	rows, err := db.QueryContext(ctx, postgresActivityQuery(version), states, maxLength+1, id)
	if err != nil {
		return nil, fmt.Errorf("db_queries: pg_stat_activity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DBQuery
	for rows.Next() {
		var (
			q      = DBQuery{Engine: SQLEnginePostgres}
			text   string
			ms     int64
			length sql.NullInt64
		)
		if err := rows.Scan(&q.ID, &q.User, &q.Database, &q.Host, &q.Command, &q.State, &q.BackendStartUS, &q.QueryStartUS, &ms, &text, &length); err != nil {
			return nil, fmt.Errorf("db_queries: scan: %w", err)
		}
		q.StartedUnixMS, q.ElapsedSeconds = q.QueryStartUS/usPerMs, max(ms, 0)/msPerSecond
		q.Query, q.Truncated = dbQueryText(text, maxLength)
		if length.Int64 > int64(maxLength) {
			q.Truncated = true
		}
		q.Identity = dbQueryIdentity(q.ID, formatInt(q.BackendStartUS), formatInt(q.QueryStartUS))
		q.OSThreadID = q.ID
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db_queries: pg_stat_activity: %w", err)
	}
	return out, nil
}

// pgEpochMicros renders a timestamp column as epoch microseconds, the form
// statement identities and the kill's re-verification compare.
func pgEpochMicros(column string) string {
	return fmt.Sprintf("(extract(epoch FROM %s) * %d)::bigint", column, msPerSecond*usPerMs)
}

// killPostgresQuery verifies the backend and signals it in one statement, so
// the identity cannot change between the check and the signal.
func killPostgresQuery(ctx context.Context, db *sql.DB, target DBQuery, mode string) error {
	fn := "pg_cancel_backend"
	if mode == DBQueryKillModeConnection {
		fn = "pg_terminate_backend"
	} else if target.Command != pgStateActive {
		// pg_cancel_backend cancels a running statement; a session idle in a
		// transaction runs none, so cancelling would report success and leave
		// it holding its locks.
		return fmt.Errorf("backend %d is %q with no running statement to cancel; kill its connection instead", target.ID, target.Command)
	}
	var signalled bool
	err := db.QueryRowContext(ctx, `SELECT `+fn+`(pid) FROM pg_stat_activity
WHERE pid = $1 AND `+postgresClientBackendPredicate+`
  AND `+pgEpochMicros("backend_start")+` = $2 AND `+pgEpochMicros("query_start")+` = $3`,
		target.ID, target.BackendStartUS, target.QueryStartUS).Scan(&signalled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDBQueryChanged
	}
	if err != nil {
		return fmt.Errorf("%s(%d): %w", fn, target.ID, err)
	}
	if !signalled {
		return fmt.Errorf("%s(%d) was refused by the server", fn, target.ID)
	}
	return nil
}
