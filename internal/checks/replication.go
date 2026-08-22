package checks

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// The replication check watches MySQL/MariaDB replication — the master-master
// pairs above all — through the server's own status rows: both replica threads
// must run and the lag may carry an explicit bound. It is a health check:
// OK means every watched connection replicates.
//
// Status queries are tried newest-vocabulary-first so one check covers both
// engines: MariaDB answers SHOW ALL SLAVES STATUS (every multi-source
// connection at once), MySQL 8 answers SHOW REPLICA STATUS, and older servers
// answer SHOW SLAVE STATUS. Column spellings differ per era (Master_Host vs
// Source_Host, Slave_IO_Running vs Replica_IO_Running); replicationField reads
// whichever the server used.
const (
	replicationThreadRunning = "Yes"

	// replicationStartVerifyAttempts bounds how many times StartReplication
	// re-reads status while the IO thread hands off from Connecting to Yes.
	replicationStartVerifyAttempts = 10
)

// replicationStatusQueries are tried in order; the first that answers wins.
var replicationStatusQueries = []string{
	"SHOW ALL SLAVES STATUS",
	"SHOW REPLICA STATUS",
	"SHOW SLAVE STATUS",
}

// replicationRow is one replication connection's status keyed by column name.
type replicationRow map[string]string

// replicationSampler fetches the current replication status rows. Injected in
// tests; production samples through the mysql driver.
type replicationSampler func(ctx context.Context) ([]replicationRow, error)

// replicationCheck verifies replication health for one server, optionally
// scoped to one named multi-source connection and optionally bounding the lag.
type replicationCheck struct {
	base
	engine      string
	connection  string
	behindOp    string
	behindValue float64
	hasBehind   bool
	sample      replicationSampler
}

// replicationState is the aggregate verdict over the selected status rows.
type replicationState struct {
	connections []string
	sourceHost  string
	ioRunning   bool
	sqlRunning  bool
	behind      float64
	hasBehind   bool
	lastIOErr   string
	lastSQLErr  string
}

func (c replicationCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	rows, err := c.sample(ctx)
	if err != nil {
		return c.unavailableResult("replication "+c.engine+": "+err.Error(), start)
	}
	rows = filterReplicationRows(rows, c.connection)
	if len(rows) == 0 {
		msg := "no replication configured"
		if c.connection != "" {
			msg = "replication connection " + strconv.Quote(c.connection) + " not configured"
		}
		return c.result(false, msg, start)
	}

	state := aggregateReplication(rows)
	res := c.result(c.verdict(state), c.message(state), start)
	res.Data = replicationData(c.engine, state)
	return res
}

// verdict is true when every selected connection has both threads running and
// the lag bound, when configured, holds.
func (c replicationCheck) verdict(s replicationState) bool {
	if !s.ioRunning || !s.sqlRunning {
		return false
	}
	if c.hasBehind && s.hasBehind && !cfgval.CompareFloat(s.behind, c.behindOp, c.behindValue) {
		return false
	}
	return true
}

func (c replicationCheck) message(s replicationState) string {
	scope := "replication"
	if len(s.connections) > 0 {
		scope += " " + strings.Join(s.connections, ", ")
	}
	if !s.ioRunning || !s.sqlRunning {
		return scope + ": " + replicationFailureText(s)
	}
	behind := "lag unknown"
	if s.hasBehind {
		behind = fmt.Sprintf("%.0fs behind", s.behind)
	}
	msg := fmt.Sprintf("%s ok: io and sql running, %s (source %s)", scope, behind, s.sourceHost)
	if c.hasBehind && s.hasBehind && !cfgval.CompareFloat(s.behind, c.behindOp, c.behindValue) {
		return fmt.Sprintf("%s lagging: %.0fs behind (limit %s %.0f, source %s)",
			scope, s.behind, c.behindOp, c.behindValue, s.sourceHost)
	}
	return msg
}

// replicationFailureText names the stopped thread and quotes the server's own
// last error, which is what a DBA acts on.
func replicationFailureText(s replicationState) string {
	var parts []string
	if !s.ioRunning {
		text := "io thread stopped"
		if s.lastIOErr != "" {
			text += ": " + s.lastIOErr
		}
		parts = append(parts, text)
	}
	if !s.sqlRunning {
		text := "sql thread stopped"
		if s.lastSQLErr != "" {
			text += ": " + s.lastSQLErr
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ")
}

func replicationData(engine string, s replicationState) map[string]any {
	data := map[string]any{
		DataKeyEngine:       engine,
		DataKeyIOStopped:    stoppedMetric(s.ioRunning),
		DataKeySQLStopped:   stoppedMetric(s.sqlRunning),
		DataKeySourceHost:   s.sourceHost,
		DataKeyLastIOError:  s.lastIOErr,
		DataKeyLastSQLError: s.lastSQLErr,
	}
	if s.hasBehind {
		data[DataKeyBehindSeconds] = s.behind
	}
	if len(s.connections) > 0 {
		data[DataKeyConnections] = strings.Join(s.connections, ", ")
	}
	return data
}

// stoppedMetric renders a thread state as the 0-ok/1-failed number the band
// metrics grade: bands and readings both treat zero as the healthy level.
func stoppedMetric(running bool) float64 {
	if running {
		return 0
	}
	return 1
}

// filterReplicationRows keeps the row whose MariaDB Connection_name or MySQL
// Channel_Name matches; with no filter every row stays.
func filterReplicationRows(rows []replicationRow, connection string) []replicationRow {
	if connection == "" {
		return rows
	}
	var out []replicationRow
	for _, row := range rows {
		if replicationField(row, "Connection_name", "Channel_Name") == connection {
			out = append(out, row)
		}
	}
	return out
}

// aggregateReplication folds the selected rows into one verdict: every thread
// must run, the reported lag is the worst across connections, and the first
// error text of each kind is kept for the message and readings.
func aggregateReplication(rows []replicationRow) replicationState {
	state := replicationState{ioRunning: true, sqlRunning: true}
	var hosts []string
	for _, row := range rows {
		if name := replicationField(row, "Connection_name", "Channel_Name"); name != "" {
			state.connections = append(state.connections, name)
		}
		if host := replicationField(row, "Master_Host", "Source_Host"); host != "" && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
		if replicationField(row, "Slave_IO_Running", "Replica_IO_Running") != replicationThreadRunning {
			state.ioRunning = false
		}
		if replicationField(row, "Slave_SQL_Running", "Replica_SQL_Running") != replicationThreadRunning {
			state.sqlRunning = false
		}
		if state.lastIOErr == "" {
			state.lastIOErr = replicationField(row, "Last_IO_Error")
		}
		if state.lastSQLErr == "" {
			state.lastSQLErr = replicationField(row, "Last_SQL_Error")
		}
		if raw := replicationField(row, "Seconds_Behind_Master", "Seconds_Behind_Source"); raw != "" {
			if v, err := strconv.ParseFloat(raw, 64); err == nil && (!state.hasBehind || v > state.behind) {
				state.behind, state.hasBehind = v, true
			}
		}
	}
	sort.Strings(state.connections)
	state.sourceHost = strings.Join(hosts, ", ")
	return state
}

// replicationField returns the first present, non-empty column among the era
// spellings of one field.
func replicationField(row replicationRow, names ...string) string {
	for _, name := range names {
		if v, ok := row[name]; ok && v != "" {
			return v
		}
	}
	return ""
}

// buildReplicationCheck builds a replication check: engine mysql/mariadb (the
// same wire protocol), required user, optional connection scope and optional
// behind bound.
func buildReplicationCheck(b base, entry map[string]any) (Check, string) {
	engine := cfgval.AsString(entry[CheckKeyEngine])
	if engine == "" {
		engine = SQLEngineMariaDB
	}
	if engine != SQLEngineMySQL && engine != SQLEngineMariaDB {
		return nil, "replication check engine must be mysql or mariadb"
	}
	if cfgval.AsString(entry[CheckKeyUser]) == "" {
		return nil, "replication check requires a user"
	}
	check := replicationCheck{
		base:       b,
		engine:     engine,
		connection: cfgval.AsString(entry[CheckKeyConnection]),
	}
	if m, ok := entry[CheckKeyBehind].(map[string]any); ok {
		op := cfgval.AsString(m[CheckKeyOp])
		if !cfgval.IsCompareOp(op) {
			return nil, "replication check behind has an invalid op (" + cfgval.CompareOpSummary + ")"
		}
		v, err := strconv.ParseFloat(cfgval.String(m[CheckKeyValue]), numericBits64)
		if err != nil {
			return nil, "replication check behind value must be numeric"
		}
		check.behindOp, check.behindValue, check.hasBehind = op, v, true
	}
	cfg := sqlConnConfig(SQLEngineMySQL, entry)
	check.sample = func(ctx context.Context) ([]replicationRow, error) {
		return sampleReplication(ctx, cfg)
	}
	return check, ""
}

// sampleReplication opens a driver connection and returns the first status
// vocabulary the server answers.
func sampleReplication(ctx context.Context, cfg conn.Config) ([]replicationRow, error) {
	db, err := conn.OpenMySQLDB(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("replication: open: %w", err)
	}
	defer func() { _ = db.Close() }()
	return queryReplicationRows(ctx, db)
}

func queryReplicationRows(ctx context.Context, db *sql.DB) ([]replicationRow, error) {
	var lastErr error
	for _, query := range replicationStatusQueries {
		rows, err := replicationQuery(ctx, db, query)
		if err == nil {
			return rows, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("replication: status query: %w", lastErr)
}

// replicationQuery runs one status query and materializes every row keyed by
// column name; all values arrive as driver bytes and are kept as strings.
func replicationQuery(ctx context.Context, db *sql.DB, query string) ([]replicationRow, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("replication: %s: %w", query, err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("replication: columns: %w", err)
	}
	var out []replicationRow
	for rows.Next() {
		values := make([]any, len(columns))
		for i := range values {
			values[i] = new(sql.NullString)
		}
		if err := rows.Scan(values...); err != nil {
			return nil, fmt.Errorf("replication: scan: %w", err)
		}
		row := make(replicationRow, len(columns))
		for i, col := range columns {
			if ns, ok := values[i].(*sql.NullString); ok && ns.Valid {
				row[col] = ns.String
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("replication: rows: %w", err)
	}
	return out, nil
}
