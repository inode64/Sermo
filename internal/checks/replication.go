package checks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"slices"
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
//
// With a source_defaults_file the check also measures the backlog: it asks
// each connection's source for SHOW BINARY LOGS and counts, in bytes of the
// source's binary log, what the replica has not received yet (from the IO
// thread's read position to the end of the source's current log) and what it
// has received but not applied (from the SQL thread's position to the read
// position). Seconds_Behind_Master cannot see the first part: an IO thread
// that stopped receiving without noticing still reads Yes, and with its relay
// log applied the replica reports 0 seconds behind.
const (
	replicationThreadRunning = "Yes"

	// replicationAccountRequired refuses an entry that names neither the
	// account nor an option file that supplies it.
	replicationAccountRequired = "replication check requires a user or a defaults_file"

	// replicationPendingNeedsSource refuses a pending_bytes bound with no
	// account to read the source's binary logs.
	replicationPendingNeedsSource = "replication check pending_bytes requires a source_defaults_file"

	// replicationBinaryLogsQuery lists the source's binary logs with their
	// sizes, the file being written last; MariaDB and MySQL both answer it, with
	// the same two columns.
	replicationBinaryLogsQuery = "SHOW BINARY LOGS"
	binaryLogsNameColumn       = "Log_name"
	binaryLogsSizeColumn       = "File_size"

	// mariadbConnectionColumn names a MariaDB multi-source connection; MySQL
	// calls its channels Channel_Name.
	mariadbConnectionColumn = "Connection_name"

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

// binlogFile is one row of the source's SHOW BINARY LOGS.
type binlogFile struct {
	name string
	size int64
}

// binlogSampler lists the binary logs of the source at host:port, oldest
// first and the file being written last. Injected in tests; production asks
// the source with the source_defaults_file account.
type binlogSampler func(ctx context.Context, host string, port int) ([]binlogFile, error)

// binlogPosition is a coordinate in the source's binary log.
type binlogPosition struct {
	file   string
	offset int64
}

// binlogColumns names a status row's file and offset columns for one thread
// position, MariaDB/older spelling first.
type binlogColumns struct {
	file   []string
	offset []string
}

// The status columns read in more than one place, MariaDB/older spelling
// first and MySQL 8 second.
var (
	replicationConnectionColumns = []string{mariadbConnectionColumn, "Channel_Name"}
	replicationSourceHostColumns = []string{"Master_Host", "Source_Host"}
	replicationSourcePortColumns = []string{"Master_Port", "Source_Port"}
)

var (
	// replicationReadColumns locate what the IO thread has received.
	replicationReadColumns = binlogColumns{
		file:   []string{"Master_Log_File", "Source_Log_File"},
		offset: []string{"Read_Master_Log_Pos", "Read_Source_Log_Pos"},
	}
	// replicationExecColumns locate what the SQL thread has applied.
	replicationExecColumns = binlogColumns{
		file:   []string{"Relay_Master_Log_File", "Relay_Source_Log_File"},
		offset: []string{"Exec_Master_Log_Pos", "Exec_Source_Log_Pos"},
	}
)

// replicationPending is what one connection still owes the replica, in bytes
// of the source's binary log.
type replicationPending struct {
	unreceived int64 // written on the source, not yet read by the IO thread
	unapplied  int64 // read by the IO thread, not yet applied by the SQL thread
}

func (p replicationPending) total() int64 { return p.unreceived + p.unapplied }

// replicationCheck verifies replication health for one server, optionally
// scoped to one named multi-source connection, optionally bounding the lag
// and, with a source account, measuring and bounding the pending binlog.
type replicationCheck struct {
	base
	engine       string
	connection   string
	behindBound  *levelPred // nil without a behind bound
	pendingBound *levelPred // nil without a pending_bytes bound
	sample       replicationSampler
	binlogs      binlogSampler // nil without a source_defaults_file
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
	// pending is the largest backlog across connections; hasPending is false
	// when it was not measured, and pendingErr says why it could not be.
	pending    replicationPending
	hasPending bool
	pendingErr string
}

func (c replicationCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	rows, err := c.sample(ctx)
	if err != nil {
		// A saturated server is busy, so no rule restarts a replica over it.
		return c.driverFailure("replication "+c.engine, err, start, nil)
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
	if c.binlogs != nil {
		c.measurePending(ctx, rows, &state)
	}
	lagging := c.lagExceeded(state)
	backlogged := c.pendingExceeded(state)
	res := c.result(replicationVerdict(state, lagging || backlogged), c.message(state, lagging, backlogged), start)
	res.Data = replicationData(c.engine, state)
	return res
}

// verdict is true when every selected connection has both threads running and
// the lag and pending bounds, when configured, hold.
func replicationVerdict(s replicationState, overBound bool) bool {
	if !s.ioRunning || !s.sqlRunning {
		return false
	}
	return !overBound
}

func (c replicationCheck) lagExceeded(s replicationState) bool {
	return c.behindBound != nil && s.hasBehind && !cfgval.CompareFloat(s.behind, c.behindBound.op, c.behindBound.value)
}

// pendingExceeded is true when a pending_bytes bound is configured and the
// backlog breaks it or could not be measured: an unverifiable bound does not
// pass.
func (c replicationCheck) pendingExceeded(s replicationState) bool {
	if c.pendingBound == nil {
		return false
	}
	return !s.hasPending || !cfgval.CompareFloat(float64(s.pending.total()), c.pendingBound.op, c.pendingBound.value)
}

func (c replicationCheck) message(s replicationState, lagging, backlogged bool) string {
	scope := "replication"
	if len(s.connections) > 0 {
		scope += " " + strings.Join(s.connections, ", ")
	}
	switch {
	case !s.ioRunning || !s.sqlRunning:
		return scope + ": " + replicationFailureText(s) + c.pendingNote(s)
	case lagging:
		return fmt.Sprintf("%s lagging: %.0fs behind (limit %s %.0f, source %s)",
			scope, s.behind, c.behindBound.op, c.behindBound.value, s.sourceHost)
	case backlogged && !s.hasPending:
		return fmt.Sprintf("%s pending unknown: %s (source %s)", scope, s.pendingErr, s.sourceHost)
	case backlogged:
		return fmt.Sprintf("%s backlog: %s of source binlog pending, %s not received (limit %s %s, source %s)",
			scope, HumanizeSignedBytes(s.pending.total()), HumanizeSignedBytes(s.pending.unreceived),
			c.pendingBound.op, HumanizeSignedBytes(int64(c.pendingBound.value)), s.sourceHost)
	}
	behind := "lag unknown"
	if s.hasBehind {
		behind = fmt.Sprintf("%.0fs behind", s.behind)
	}
	return fmt.Sprintf("%s ok: io and sql running, %s%s (source %s)", scope, behind, c.pendingNote(s), s.sourceHost)
}

// pendingNote is the backlog clause of a message: empty when the check does
// not measure it.
func (c replicationCheck) pendingNote(s replicationState) string {
	switch {
	case c.binlogs == nil:
		return ""
	case s.hasPending:
		return ", " + HumanizeSignedBytes(s.pending.total()) + " pending"
	default:
		return ", pending unknown: " + s.pendingErr
	}
}

// measurePending records the largest backlog across the selected connections,
// or why it could not be measured.
func (c replicationCheck) measurePending(ctx context.Context, rows []replicationRow, s *replicationState) {
	worst, err := c.worstPending(ctx, rows)
	if err != nil {
		s.pendingErr = err.Error()
		return
	}
	s.pending, s.hasPending = worst, true
}

// worstPending lists each connection's source binary logs once and returns the
// largest backlog. A connection that cannot be measured fails the whole
// measure, since it might be the worst one.
func (c replicationCheck) worstPending(ctx context.Context, rows []replicationRow) (replicationPending, error) {
	logsBySource := map[string][]binlogFile{}
	var worst replicationPending
	for _, row := range rows {
		host := replicationField(row, replicationSourceHostColumns...)
		if host == "" {
			return replicationPending{}, errors.New("status row names no source host")
		}
		port, _ := strconv.Atoi(replicationField(row, replicationSourcePortColumns...))
		source := net.JoinHostPort(host, strconv.Itoa(port))
		logs, seen := logsBySource[source]
		if !seen {
			var err error
			if logs, err = c.binlogs(ctx, host, port); err != nil {
				return replicationPending{}, fmt.Errorf("source %s: %w", host, err)
			}
			logsBySource[source] = logs
		}
		pending, err := pendingFromSource(row, logs)
		if err != nil {
			return replicationPending{}, fmt.Errorf("source %s: %w", host, err)
		}
		if pending.total() > worst.total() {
			worst = pending
		}
	}
	return worst, nil
}

// pendingFromSource measures one connection against its source's binary logs:
// unreceived runs from the IO thread's read position to the end of the
// source's current log, unapplied from the SQL thread's position to the read
// position.
func pendingFromSource(row replicationRow, logs []binlogFile) (replicationPending, error) {
	if len(logs) == 0 {
		return replicationPending{}, errors.New("lists no binary logs")
	}
	read, err := rowBinlogPosition(row, replicationReadColumns)
	if err != nil {
		return replicationPending{}, err
	}
	exec, err := rowBinlogPosition(row, replicationExecColumns)
	if err != nil {
		return replicationPending{}, err
	}
	current := logs[len(logs)-1]
	unreceived, err := binlogDistance(logs, read, binlogPosition{file: current.name, offset: current.size})
	if err != nil {
		return replicationPending{}, err
	}
	unapplied, err := binlogDistance(logs, exec, read)
	if err != nil {
		return replicationPending{}, err
	}
	return replicationPending{unreceived: unreceived, unapplied: unapplied}, nil
}

// rowBinlogPosition reads one thread's position from a status row.
func rowBinlogPosition(row replicationRow, cols binlogColumns) (binlogPosition, error) {
	file := replicationField(row, cols.file...)
	offset, err := strconv.ParseInt(replicationField(row, cols.offset...), numericBaseDecimal, numericBits64)
	if file == "" || err != nil {
		return binlogPosition{}, fmt.Errorf("status row has no %s/%s position", cols.file[0], cols.offset[0])
	}
	return binlogPosition{file: file, offset: offset}, nil
}

// binlogDistance counts the source binlog bytes from one position to a later
// one, across files when they differ. A position in a file the source no
// longer lists cannot be measured: the source purged it.
func binlogDistance(logs []binlogFile, from, to binlogPosition) (int64, error) {
	if from.file == to.file {
		return max(to.offset-from.offset, 0), nil
	}
	fromIdx := slices.IndexFunc(logs, func(f binlogFile) bool { return f.name == from.file })
	toIdx := slices.IndexFunc(logs, func(f binlogFile) bool { return f.name == to.file })
	switch {
	case fromIdx < 0:
		return 0, fmt.Errorf("no longer lists %s", from.file)
	case toIdx < 0:
		return 0, fmt.Errorf("does not list %s", to.file)
	case fromIdx > toIdx:
		return 0, fmt.Errorf("lists %s after %s", from.file, to.file)
	}
	n := max(logs[fromIdx].size-from.offset, 0)
	for _, f := range logs[fromIdx+1 : toIdx] {
		n += f.size
	}
	return n + to.offset, nil
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
	if s.hasPending {
		data[DataKeyPendingBytes] = s.pending.total()
		data[DataKeyUnreceivedBytes] = s.pending.unreceived
		data[DataKeyUnappliedBytes] = s.pending.unapplied
	}
	if s.pendingErr != "" {
		data[DataKeyPendingError] = s.pendingErr
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
		if replicationField(row, replicationConnectionColumns...) == connection {
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
		if name := replicationField(row, replicationConnectionColumns...); name != "" {
			state.connections = append(state.connections, name)
		}
		if host := replicationField(row, replicationSourceHostColumns...); host != "" && !slices.Contains(hosts, host) {
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
	slices.Sort(state.connections)
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
// same wire protocol), a user or a defaults_file, optional connection scope,
// the optional ReplicationPredFields bounds and, for the pending binlog, a
// source_defaults_file.
func buildReplicationCheck(b base, entry map[string]any) (Check, string) {
	engine := replicationEngine(entry)
	if engine != SQLEngineMySQL && engine != SQLEngineMariaDB {
		return nil, "replication check engine must be mysql or mariadb"
	}
	open, ok := mysqlServerOpener(engine, entry)
	if !ok {
		return nil, replicationAccountRequired
	}
	check := replicationCheck{
		base:       b,
		engine:     engine,
		connection: cfgval.AsString(entry[CheckKeyConnection]),
	}
	bounds, err := parseLevelPreds(entry, ReplicationPredFields)
	if err != nil {
		return nil, "replication check " + err.Error()
	}
	for _, bound := range bounds {
		switch bound.field {
		case CheckKeyBehind:
			check.behindBound = &bound
		case CheckKeyPendingBytes:
			check.pendingBound = &bound
		}
	}
	if sourceDefaultsFile := cfgval.AsString(entry[CheckKeySourceDefaultsFile]); sourceDefaultsFile != "" {
		check.binlogs = sourceBinlogSampler(sourceDefaultsFile)
	} else if check.pendingBound != nil {
		return nil, replicationPendingNeedsSource
	}
	check.sample = func(ctx context.Context) ([]replicationRow, error) {
		return sampleReplication(ctx, open)
	}
	return check, ""
}

// replicationEngine is a replication entry's engine, mariadb when unset.
func replicationEngine(entry map[string]any) string {
	if engine := cfgval.AsString(entry[CheckKeyEngine]); engine != "" {
		return engine
	}
	return SQLEngineMariaDB
}

// sampleReplication opens a driver connection and returns the first status
// vocabulary the server answers.
func sampleReplication(ctx context.Context, open func(context.Context) (*sql.DB, error)) ([]replicationRow, error) {
	db, err := open(ctx)
	if err != nil {
		return nil, fmt.Errorf("replication: open: %w", err)
	}
	defer func() { _ = db.Close() }()
	return queryReplicationRows(ctx, db)
}

// sourceBinlogSampler asks a connection's source for its binary logs with the
// account of the source_defaults_file option file. The source host and port
// come from the status row, so the option file's own host, port and socket are
// not used. The account needs BINLOG MONITOR (REPLICATION CLIENT on MySQL).
// The caller names the source in the error.
func sourceBinlogSampler(defaultsFile string) binlogSampler {
	return func(ctx context.Context, host string, port int) ([]binlogFile, error) {
		target, err := mysqlTarget(conn.Config{Host: host, Port: port}, defaultsFile)
		if err != nil {
			return nil, err
		}
		db, err := conn.OpenMySQLDB(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}
		defer func() { _ = db.Close() }()
		rows, err := replicationQuery(ctx, db, replicationBinaryLogsQuery)
		if err != nil {
			return nil, err
		}
		return parseBinaryLogs(rows)
	}
}

// parseBinaryLogs reads the name and size columns of SHOW BINARY LOGS.
func parseBinaryLogs(rows []replicationRow) ([]binlogFile, error) {
	logs := make([]binlogFile, 0, len(rows))
	for _, row := range rows {
		name := row[binaryLogsNameColumn]
		size, err := strconv.ParseInt(row[binaryLogsSizeColumn], numericBaseDecimal, numericBits64)
		if name == "" || err != nil {
			return nil, fmt.Errorf("%s answered a row without %s and %s",
				replicationBinaryLogsQuery, binaryLogsNameColumn, binaryLogsSizeColumn)
		}
		logs = append(logs, binlogFile{name: name, size: size})
	}
	return logs, nil
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
