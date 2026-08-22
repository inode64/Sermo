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
	state, err := replicationStateNow(ctx, db, connection)
	if err != nil {
		return ReplicationControlResult{Message: err.Error()}
	}
	if state == nil {
		return ReplicationControlResult{Message: "no replication configured to start"}
	}
	if state.ioRunning && state.sqlRunning {
		return ReplicationControlResult{OK: true, Message: "replication already running (source " + state.sourceHost + ")"}
	}

	if err := execStartReplica(ctx, db, connection); err != nil {
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
	rows = filterReplicationRows(rows, connection)
	if len(rows) == 0 {
		return nil, nil //nolint:nilnil // no rows is a distinct, documented outcome, not an error
	}
	state := aggregateReplication(rows)
	return &state, nil
}

// execStartReplica issues the start statement in the newest vocabulary the
// server accepts. A named MariaDB connection has exactly one form.
func execStartReplica(ctx context.Context, db *sql.DB, connection string) error {
	statements := []string{"START REPLICA", "START SLAVE"}
	if connection != "" {
		statements = []string{"START SLAVE '" + connection + "'"}
	}
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
