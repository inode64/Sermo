package checks

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// StartReplication is the manual repair a DBA performs on a stopped replica,
// executed by Sermo instead of a shell: revalidate live status first, issue
// START REPLICA (or the engine's older spelling, or the MariaDB named-connection
// form), then re-read status until both threads run or the wait budget ends.
// Like SetRaidRebuildState it is an explicitly requested operator action, not
// remediation the daemon takes on its own.

// replicationStartVerifyInterval paces the post-start status re-reads while the
// IO thread hands off from Connecting to Yes.
const replicationStartVerifyInterval = 500 * time.Millisecond

// replicationConnectionName confines a MariaDB connection name to the
// characters that can be embedded in a quoted START SLAVE statement without
// escaping ambiguity.
var replicationConnectionName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ReplicationControlResult is the verified outcome of a manual replication
// start.
type ReplicationControlResult struct {
	OK      bool
	Message string
}

// StartReplication starts the stopped replica threads described by a
// replication check entry and verifies the result against live status.
func StartReplication(ctx context.Context, entry map[string]any) ReplicationControlResult {
	connection := cfgval.AsString(entry[CheckKeyConnection])
	if connection != "" && !replicationConnectionName.MatchString(connection) {
		return ReplicationControlResult{Message: "replication connection name " + strconv.Quote(connection) + " is not startable"}
	}
	cfg := sqlConnConfig(SQLEngineMySQL, entry)
	db, err := conn.OpenMySQLDB(ctx, cfg)
	if err != nil {
		return ReplicationControlResult{Message: "replication: open: " + err.Error()}
	}
	defer func() { _ = db.Close() }()
	return startReplicationDB(ctx, db, connection)
}

func startReplicationDB(ctx context.Context, db *sql.DB, connection string) ReplicationControlResult {
	rows, err := queryReplicationRows(ctx, db)
	if err != nil {
		return ReplicationControlResult{Message: err.Error()}
	}
	state := scopedReplicationState(rows, connection)
	if state == nil {
		return ReplicationControlResult{Message: "no replication configured to start"}
	}
	if state.ioRunning && state.sqlRunning {
		return ReplicationControlResult{OK: true, Message: "replication already running (source " + state.sourceHost + ")"}
	}

	if err := execStartReplica(ctx, db, replicationStartStatements(connection, rows)); err != nil {
		return ReplicationControlResult{Message: err.Error()}
	}

	for range replicationStartVerifyAttempts {
		select {
		case <-ctx.Done():
			return ReplicationControlResult{Message: "replication start: " + ctx.Err().Error()}
		case <-time.After(replicationStartVerifyInterval):
		}
		state, err = replicationStateNow(ctx, db, connection)
		if err != nil {
			return ReplicationControlResult{Message: err.Error()}
		}
		if state != nil && state.ioRunning && state.sqlRunning {
			return ReplicationControlResult{OK: true, Message: "replication started: io and sql running (source " + state.sourceHost + ")"}
		}
	}
	msg := "replication start issued but threads are not running yet"
	if state != nil {
		msg += ": " + replicationFailureText(*state)
	}
	return ReplicationControlResult{Message: msg}
}

// replicationStateNow reads the current aggregate state for the scoped
// connection; nil state means no matching replication row exists.
func replicationStateNow(ctx context.Context, db *sql.DB, connection string) (*replicationState, error) {
	rows, err := queryReplicationRows(ctx, db)
	if err != nil {
		return nil, err
	}
	return scopedReplicationState(rows, connection), nil
}

// scopedReplicationState aggregates the rows of the scoped connection; nil
// means no matching replication row exists.
func scopedReplicationState(rows []replicationRow, connection string) *replicationState {
	rows = filterReplicationRows(rows, connection)
	if len(rows) == 0 {
		return nil
	}
	state := aggregateReplication(rows)
	return &state
}

// replicationStartStatements lists the start statements to try, newest
// vocabulary first. A named MariaDB connection has exactly one form. Without
// a connection the check covers every row, but MariaDB's plain START SLAVE
// only starts @@default_master_connection, so a server with named
// connections needs START ALL SLAVES; MySQL's START REPLICA already starts
// every channel.
func replicationStartStatements(connection string, rows []replicationRow) []string {
	if connection != "" {
		return []string{"START SLAVE '" + connection + "'"}
	}
	for _, row := range rows {
		if row["Connection_name"] != "" {
			return []string{"START ALL REPLICAS", "START ALL SLAVES"}
		}
	}
	return []string{"START REPLICA", "START SLAVE"}
}

// execStartReplica issues the first start statement the server accepts.
func execStartReplica(ctx context.Context, db *sql.DB, statements []string) error {
	var lastErr error
	for _, statement := range statements {
		_, err := db.ExecContext(ctx, statement)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("replication start: %w", lastErr)
}
