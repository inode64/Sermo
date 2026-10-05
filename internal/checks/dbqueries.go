package checks

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/hostfs"
	"sermo/internal/netutil"
)

// CheckTypeDBQueries lists the statements a MySQL/MariaDB or PostgreSQL server
// is running. It is a watch-only type built by internal/app (like
// process_policy): the watch tracks every running statement as an item, alerts
// once per statement that outlives min_duration and publishes the list the
// Sessions panel shows.
const CheckTypeDBQueries = "db_queries"

// db_queries check keys.
const (
	CheckKeyDefaultsFile     = "defaults_file"
	CheckKeyUsers            = "users"
	CheckKeyMinDuration      = "min_duration"
	CheckKeyExcludeUsers     = "exclude_users"
	CheckKeyDatabases        = "databases"
	CheckKeyExcludeDatabases = "exclude_databases"
	CheckKeyStates           = "states"
	CheckKeyMaxQueryLength   = "max_query_length"
	CheckKeyMaxRows          = "max_rows"
	// CheckKeyAfter and CheckKeyMode configure a db_queries watch's opt-in
	// then.kill_query: how long a statement must run, and what to stop.
	CheckKeyAfter = "after"
	CheckKeyMode  = DataKeyMode
)

// db_queries result data keys, and the per-statement keys a fire's summary
// template reads.
const (
	DataKeyDBQueries     = "db_queries"
	DataKeyLongCount     = "long_count"
	DataKeyOldestSeconds = "oldest_seconds"
	DataKeyID            = CheckKeyID
	DataKeyUser          = CheckKeyUser
	DataKeyElapsed       = "elapsed"
)

// Kill modes: cancel the running statement, or close its whole connection.
const (
	DBQueryKillModeQuery      = "query"
	DBQueryKillModeConnection = "connection"
	// DBQueryKillModeSummary is the user-facing list of kill modes.
	DBQueryKillModeSummary = DBQueryKillModeQuery + " or " + DBQueryKillModeConnection
)

// ValidDBQueryKillMode reports whether mode is a kill mode.
func ValidDBQueryKillMode(mode string) bool {
	return mode == DBQueryKillModeQuery || mode == DBQueryKillModeConnection
}

const (
	dbQueryDefaultMaxLength = 1000
	dbQueryDefaultMaxRows   = 50
	// dbQueryFetchLimit bounds one sample regardless of max_rows, so a server
	// with thousands of connections cannot make a cycle unbounded.
	dbQueryFetchLimit = 500
	// mysqlStartTolerance absorbs MySQL's whole-second TIME column when a
	// statement's start is derived from it.
	mysqlStartTolerance = 2 * time.Second
	msPerSecond         = 1000
	usPerMs             = 1000
	dbQueryDefaultUser  = "root"
	// dbQueryCommandKilled is the processlist command of a statement the
	// server is already stopping (rolling back before it disappears).
	dbQueryCommandKilled = "Killed"
	pgStateActive        = "active"
	mariaDBVersionTag    = "MariaDB"
	// dbQueryIdentitySep joins the parts of a statement identity.
	dbQueryIdentitySep = ":"
	dbQueryNumberBase  = 10
)

// DBQuery is one running statement. The JSON names are the persisted snapshot
// and web API shape.
type DBQuery struct {
	Engine         string `json:"engine"`
	ID             int64  `json:"id"`
	QueryID        int64  `json:"query_id,omitempty"`
	User           string `json:"user"`
	Host           string `json:"host,omitempty"`
	Database       string `json:"database,omitempty"`
	Command        string `json:"command,omitempty"`
	State          string `json:"state,omitempty"`
	StartedUnixMS  int64  `json:"started_unix_ms,omitempty"`
	BackendStartUS int64  `json:"backend_start_us,omitempty"`
	QueryStartUS   int64  `json:"query_start_us,omitempty"`
	ElapsedSeconds int64  `json:"elapsed_seconds"`
	Query          string `json:"query"`
	Truncated      bool   `json:"truncated,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	Identity       string `json:"identity"`
	// Long marks a statement past its watch's min_duration and filters, the
	// one rule the alert, the Sessions panel and sermoctl share.
	Long bool `json:"long,omitempty"`
	// Alerted marks a statement its watch already announced as long-running,
	// so a restarted daemon neither repeats the alert nor loses its recovery;
	// Announced, that the alert reached a live hook or notifier, which then
	// hears the recovery too.
	Alerted   bool `json:"alerted,omitempty"`
	Announced bool `json:"announced,omitempty"`
	// OSThreadID is the host thread (MySQL/MariaDB) or process (PostgreSQL)
	// running the statement, 0 when the server does not say.
	OSThreadID int64 `json:"os_tid,omitempty"`
	// MemoryBytes is the connection's memory: MariaDB's MEMORY_USED, or the
	// PostgreSQL backend's resident memory. MemoryReady tells a measured zero
	// from an unknown.
	MemoryBytes int64 `json:"memory,omitempty"`
	MemoryReady bool  `json:"memory_ready,omitempty"`
	// CPU is the statement's thread or backend CPU share of the host since the
	// previous sample (the watch derives it from /proc for a local server).
	CPU      float64 `json:"cpu,omitempty"`
	CPUReady bool    `json:"cpu_ready,omitempty"`
	// IORead/IOWrite are the thread's or backend's storage bytes per second
	// since the previous sample.
	IORead  float64 `json:"io_read,omitempty"`
	IOWrite float64 `json:"io_write,omitempty"`
	IOReady bool    `json:"io_ready,omitempty"`
}

// Elapsed is how long the statement has been running.
func (q DBQuery) Elapsed() time.Duration { return time.Duration(q.ElapsedSeconds) * time.Second }

// Stopping reports a statement the server is already stopping: cancelled from
// Sermo, another client or the server itself, and rolling back.
func (q DBQuery) Stopping() bool { return q.Command == dbQueryCommandKilled }

// Killable reports whether the statement can be targeted by a kill: it carries
// an identity and is not already being stopped.
func (q DBQuery) Killable() bool { return q.Identity != "" && !q.Stopping() }

// ItemKey is the statement's stable tracking key across samples. MySQL derives
// the start from a whole-second counter, so its key leaves the start out and
// SameStatement compares it with a tolerance; elsewhere the identity is exact.
func (q DBQuery) ItemKey() string {
	if q.Engine == SQLEngineMySQL {
		return dbQueryIdentity(q.ID, q.Fingerprint)
	}
	return q.Identity
}

// dbQueryIdentity joins a statement identity's parts. Every engine starts with
// the connection id; MySQL appends its fingerprint and derived start, MariaDB
// its QUERY_ID, PostgreSQL its backend and statement start.
func dbQueryIdentity(id int64, rest ...string) string {
	return strings.Join(append([]string{formatInt(id)}, rest...), dbQueryIdentitySep)
}

func formatInt(n int64) string { return strconv.FormatInt(n, dbQueryNumberBase) }

// SameStatement reports whether two samples describe the same running
// statement (and not a later statement on the same connection).
func (q DBQuery) SameStatement(other DBQuery) bool {
	if q.Engine != other.Engine || q.ID != other.ID {
		return false
	}
	if q.Engine != SQLEngineMySQL {
		return q.Identity == other.Identity
	}
	if q.Fingerprint != other.Fingerprint {
		return false
	}
	diff := time.Duration(q.StartedUnixMS-other.StartedUnixMS) * time.Millisecond
	return diff.Abs() <= mysqlStartTolerance
}

// DBQuerySelector narrows which statements count: the alert's users/databases
// filters, and the automatic kill's mandatory selector.
type DBQuerySelector struct {
	Users            []string
	ExcludeUsers     []string
	Databases        []string
	ExcludeDatabases []string
}

// Matches reports whether q passes the selector. Empty include lists match
// everything.
func (s DBQuerySelector) Matches(q DBQuery) bool {
	if len(s.Users) > 0 && !slices.Contains(s.Users, q.User) {
		return false
	}
	if slices.Contains(s.ExcludeUsers, q.User) {
		return false
	}
	if len(s.Databases) > 0 && !slices.Contains(s.Databases, q.Database) {
		return false
	}
	return !slices.Contains(s.ExcludeDatabases, q.Database)
}

// DBQueryConfig is a parsed db_queries check.
type DBQueryConfig struct {
	Engine       string // SQLEngineMySQL or conn.PostgresDriverName
	Conn         conn.Config
	DefaultsFile string
	MinDuration  time.Duration
	Selector     DBQuerySelector
	States       []string
	MaxLength    int
	MaxRows      int
	Timeout      time.Duration
	// server remembers what the MySQL/MariaDB server supports, shared by the
	// copies of this config; nil re-detects on every sample.
	server *mysqlServerInfo
}

// mysqlServerInfo is what a MySQL/MariaDB server was found to support: its
// flavour (the processlist columns differ) and whether the thread-id columns
// answer. Neither changes while the server runs, so it is detected once and
// again only after a failed sample (a restart may have upgraded it).
type mysqlServerInfo struct {
	mu           sync.Mutex
	known        bool
	mariadb      bool
	threadSource bool
}

// Local reports whether the server runs on this host (a Unix socket or a
// loopback address), so its thread and process ids name local /proc entries.
func (cfg DBQueryConfig) Local() bool {
	target, err := cfg.target()
	if err != nil {
		return false
	}
	host := target.Host
	if target.Socket != "" || host == "" || host == netutil.Localhost {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Resolved returns cfg with its connection target resolved once — the
// defaults_file read — so one daemon cycle reads the option file once for
// both Local and the sample.
func (cfg DBQueryConfig) Resolved() (DBQueryConfig, error) {
	target, err := cfg.target()
	if err != nil {
		return cfg, err
	}
	cfg.Conn, cfg.DefaultsFile = target, ""
	return cfg, nil
}

// target is the connection the check dials. A mysql check's own fields come
// first, then its defaults_file, then the protocol defaults — resolved after
// the option file, so the file's socket, host and port are honoured.
func (cfg DBQueryConfig) target() (conn.Config, error) {
	if cfg.Engine != SQLEngineMySQL {
		return cfg.Conn, nil
	}
	c, err := mysqlConnWithDefaults(cfg.Conn, cfg.DefaultsFile)
	if err != nil {
		return c, err
	}
	if _, resolved, ok := conn.Prepare(conn.ProtocolNameMySQL, c); ok {
		return resolved, nil
	}
	return c, nil
}

// ParseDBQueryConfig parses a db_queries check entry. Validation reports the
// same problems with config paths; this is the builder's guard.
func ParseDBQueryConfig(entry map[string]any) (DBQueryConfig, error) {
	engine := cfgval.AsString(entry[CheckKeyEngine])
	driver, ok := sqlEngineDriver(engine)
	if !ok || driver == SQLEngineSQLite {
		return DBQueryConfig{}, fmt.Errorf("db_queries check requires an engine (%s, %s, %s or %s)",
			SQLEngineMySQL, SQLEngineMariaDB, SQLEnginePostgres, SQLEnginePostgreSQL)
	}
	minDuration := cfgval.Duration(entry[CheckKeyMinDuration])
	if minDuration <= 0 {
		return DBQueryConfig{}, errors.New("db_queries check requires min_duration as a positive duration")
	}
	cfg := DBQueryConfig{
		Engine:       driver,
		Conn:         dbQueryConnConfig(engine, driver, entry),
		DefaultsFile: cfgval.AsString(entry[CheckKeyDefaultsFile]),
		MinDuration:  minDuration,
		Selector: DBQuerySelector{
			Users:            cfgval.StringList(entry[CheckKeyUsers]),
			ExcludeUsers:     cfgval.StringList(entry[CheckKeyExcludeUsers]),
			Databases:        cfgval.StringList(entry[CheckKeyDatabases]),
			ExcludeDatabases: cfgval.StringList(entry[CheckKeyExcludeDatabases]),
		},
		States:    cfgval.StringList(entry[CheckKeyStates]),
		MaxLength: dbQueryDefaultMaxLength,
		MaxRows:   dbQueryDefaultMaxRows,
		Timeout:   cfgval.Duration(entry[CheckKeyTimeout]),
		server:    &mysqlServerInfo{},
	}
	if driver == SQLEngineMySQL {
		if len(cfg.States) > 0 {
			return DBQueryConfig{}, errors.New("db_queries states applies to postgres only")
		}
	} else {
		if cfgval.AsString(entry[CheckKeySocket]) != "" || cfg.DefaultsFile != "" {
			return DBQueryConfig{}, errors.New("db_queries socket and defaults_file apply to mysql/mariadb only")
		}
		if cfg.Conn.User == "" {
			return DBQueryConfig{}, errors.New("db_queries check (postgres) requires a user")
		}
		if len(cfg.States) == 0 {
			cfg.States = []string{pgStateActive}
		}
	}
	for key, dst := range map[string]*int{CheckKeyMaxQueryLength: &cfg.MaxLength, CheckKeyMaxRows: &cfg.MaxRows} {
		if raw, present := entry[key]; present {
			n, ok := cfgval.Int(raw)
			if !ok || n <= 0 {
				return DBQueryConfig{}, fmt.Errorf("db_queries %s must be a positive integer", key)
			}
			*dst = n
		}
	}
	return cfg, nil
}

// dbQueryConnConfig is the check's own connection fields. A mysql check is
// left unresolved: its defaults_file fills what the check leaves unset before
// the protocol defaults do (see target).
func dbQueryConnConfig(engine, driver string, entry map[string]any) conn.Config {
	if driver != SQLEngineMySQL {
		return sqlConnConfig(engine, entry)
	}
	c := databaseConnectionConfig(entry)
	c.Socket = cfgval.AsString(entry[CheckKeySocket])
	c.Port = connectionPort(entry, 0)
	return c
}

// DBQueryKillSpec is a db_queries watch's opt-in then.kill_query.
type DBQueryKillSpec struct {
	After    time.Duration
	Mode     string
	Selector DBQuerySelector
}

// ParseDBQueryKill parses then.kill_query for a check whose min_duration is
// minDuration. It is the one owner of the rules: the config validator reports
// its error under the field's path, the watch builder refuses to build.
func ParseDBQueryKill(raw any, minDuration time.Duration) (DBQueryKillSpec, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return DBQueryKillSpec{}, errors.New("must be a mapping")
	}
	spec := DBQueryKillSpec{
		After: cfgval.Duration(m[CheckKeyAfter]),
		Mode:  cmp.Or(cfgval.String(m[CheckKeyMode]), DBQueryKillModeQuery),
		Selector: DBQuerySelector{
			Users:     cfgval.StringList(m[CheckKeyUsers]),
			Databases: cfgval.StringList(m[CheckKeyDatabases]),
		},
	}
	switch {
	case spec.After <= 0:
		return DBQueryKillSpec{}, fmt.Errorf("%s is required as a positive duration", CheckKeyAfter)
	case spec.After < minDuration:
		return DBQueryKillSpec{}, fmt.Errorf("%s must be at least %s (%s)", CheckKeyAfter, CheckKeyMinDuration, minDuration)
	case !ValidDBQueryKillMode(spec.Mode):
		return DBQueryKillSpec{}, fmt.Errorf("%s must be %s", CheckKeyMode, DBQueryKillModeSummary)
	case len(spec.Selector.Users) == 0 && len(spec.Selector.Databases) == 0:
		return DBQueryKillSpec{}, fmt.Errorf("requires %s and/or %s: an automatic kill must name what it may stop", CheckKeyUsers, CheckKeyDatabases)
	}
	return spec, nil
}

// SampleDBQueries lists the statements the server is running, longest first.
// The probe's own connection, idle connections and server threads
// (replication, event scheduler) are never listed.
func SampleDBQueries(ctx context.Context, cfg DBQueryConfig) ([]DBQuery, error) {
	db, err := cfg.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return cfg.sample(ctx, db)
}

// sample lists the statements on an open connection, per engine.
func (cfg DBQueryConfig) sample(ctx context.Context, db *sql.DB) ([]DBQuery, error) {
	if cfg.Engine == SQLEngineMySQL {
		return sampleMySQLQueries(ctx, db, cfg.MaxLength, cfg.server)
	}
	return samplePostgresQueries(ctx, db, cfg.States, cfg.MaxLength)
}

func (cfg DBQueryConfig) open(ctx context.Context) (*sql.DB, error) {
	if cfg.Engine == SQLEngineMySQL {
		c, err := cfg.target()
		if err != nil {
			return nil, err
		}
		db, err := conn.OpenMySQLDB(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("db_queries: connect: %w", err)
		}
		return db, nil
	}
	db, err := conn.OpenPostgresDB(ctx, cfg.Conn)
	if err != nil {
		return nil, fmt.Errorf("db_queries: connect: %w", err)
	}
	return db, nil
}

// mysqlExcludedCommands are server threads and idle connections, never a
// statement an operator could mean to cancel.
var mysqlExcludedCommands = []string{
	"Sleep", "Daemon", "Binlog Dump", "Binlog Dump GTID",
	"Slave_IO", "Slave_SQL", "Slave_worker", "Connect", "Register Slave",
}

var mysqlExcludedUsers = []string{"system user", "event_scheduler"}

// mysqlProcesslistQuery reads the processlist. MariaDB exposes millisecond
// TIME_MS and a per-statement QUERY_ID; MySQL only whole-second TIME.
//
// The OS thread comes from MariaDB's TID, or on MySQL from performance_schema
// (threadSource false drops them for a server that lacks the columns or
// refuses that table); only MariaDB reports a connection's memory.
func mysqlProcesslistQuery(mariadb, threadSource bool) string {
	elapsedMS, queryID, tid, memory, join := fmt.Sprintf("TIME * %d", msPerSecond), "0", "NULL", "NULL", ""
	switch {
	case mariadb && threadSource:
		elapsedMS, queryID, tid, memory = "TIME_MS", "QUERY_ID", "TID", "MEMORY_USED"
	case mariadb:
		elapsedMS, queryID = "TIME_MS", "QUERY_ID"
	case threadSource:
		tid, join = "t.THREAD_OS_ID", "LEFT JOIN performance_schema.threads t ON t.PROCESSLIST_ID = p.ID"
	}
	return fmt.Sprintf(`SELECT p.ID, p.USER, IFNULL(p.HOST, ''), IFNULL(p.DB, ''), p.COMMAND, IFNULL(p.STATE, ''),
  CAST(ROUND(%[1]s) AS SIGNED), CAST(ROUND(UNIX_TIMESTAMP(NOW(3)) * %[8]d - %[1]s) AS SIGNED), %[2]s,
  LEFT(p.INFO, ?), CHAR_LENGTH(p.INFO), CRC32(p.INFO), %[6]s, %[7]s
FROM information_schema.PROCESSLIST p %[9]s
WHERE p.ID <> CONNECTION_ID() AND p.INFO IS NOT NULL
  AND p.COMMAND NOT IN (%[3]s) AND p.USER NOT IN (%[4]s)
ORDER BY 7 DESC LIMIT %[5]d`, elapsedMS, queryID, sqlStringList(mysqlExcludedCommands), sqlStringList(mysqlExcludedUsers),
		dbQueryFetchLimit, tid, memory, msPerSecond, join)
}

func sqlStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(quoted, ", ")
}

func sampleMySQLQueries(ctx context.Context, db *sql.DB, maxLength int, server *mysqlServerInfo) ([]DBQuery, error) {
	if server == nil {
		server = &mysqlServerInfo{}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	out, err := server.sample(ctx, db, maxLength)
	if err != nil {
		server.known = false // detect afresh: the server may have changed
	}
	return out, err
}

// detect reads the server flavour once; a new detection retries the thread
// columns too.
func (s *mysqlServerInfo) detect(ctx context.Context, db *sql.DB) error {
	if s.known {
		return nil
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return fmt.Errorf("db_queries: version: %w", err)
	}
	s.known, s.mariadb, s.threadSource = true, strings.Contains(version, mariaDBVersionTag), true
	return nil
}

func (s *mysqlServerInfo) sample(ctx context.Context, db *sql.DB, maxLength int) ([]DBQuery, error) {
	if err := s.detect(ctx, db); err != nil {
		return nil, err
	}
	mariadb := s.mariadb
	rows, err := db.QueryContext(ctx, mysqlProcesslistQuery(mariadb, s.threadSource), maxLength+1)
	if err != nil && s.threadSource {
		// An older MariaDB without TID, or a MySQL that refuses
		// performance_schema: list the statements without threads, and keep
		// doing so rather than paying the failing query every cycle.
		rows, err = db.QueryContext(ctx, mysqlProcesslistQuery(mariadb, false), maxLength+1)
		if err == nil {
			s.threadSource = false
		}
	}
	if err != nil {
		return nil, fmt.Errorf("db_queries: processlist: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DBQuery
	for rows.Next() {
		var r mysqlProcessRow
		if err := rows.Scan(&r.id, &r.user, &r.host, &r.db, &r.command, &r.state, &r.elapsedMS, &r.startedMS,
			&r.queryID, &r.info, &r.length, &r.crc, &r.tid, &r.memory); err != nil {
			return nil, fmt.Errorf("db_queries: scan: %w", err)
		}
		out = append(out, r.query(mariadb, maxLength))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db_queries: processlist: %w", err)
	}
	return out, nil
}

// mysqlProcessRow is one scanned processlist row, decoded by query.
type mysqlProcessRow struct {
	id, elapsedMS, startedMS          int64
	user, host, db, command, state    string
	queryID, length, crc, tid, memory sql.NullInt64
	info                              sql.NullString
}

func (r mysqlProcessRow) query(mariadb bool, maxLength int) DBQuery {
	q := DBQuery{
		Engine: SQLEngineMySQL, ID: r.id, User: r.user, Host: r.host, Database: r.db,
		Command: r.command, State: r.state, StartedUnixMS: r.startedMS,
		ElapsedSeconds: max(r.elapsedMS, 0) / msPerSecond,
		Fingerprint:    fmt.Sprintf("%08x%s%d", uint32(r.crc.Int64), dbQueryIdentitySep, r.length.Int64), //nolint:gosec // CRC32 fits uint32
		OSThreadID:     r.tid.Int64, MemoryBytes: r.memory.Int64, MemoryReady: r.memory.Valid,
	}
	q.Query, q.Truncated = dbQueryText(r.info.String, maxLength)
	if r.length.Int64 > int64(maxLength) {
		q.Truncated = true
	}
	if mariadb && r.queryID.Int64 > 0 {
		q.Engine, q.QueryID = SQLEngineMariaDB, r.queryID.Int64
		q.Identity = dbQueryIdentity(r.id, formatInt(r.queryID.Int64))
	} else {
		q.Identity = dbQueryIdentity(r.id, q.Fingerprint, formatInt(r.startedMS))
	}
	return q
}

// pgEpochMicros renders a timestamp column as epoch microseconds, the form
// statement identities and the kill's re-verification compare.
func pgEpochMicros(column string) string {
	return fmt.Sprintf("(extract(epoch FROM %s) * %d)::bigint", column, msPerSecond*usPerMs)
}

var postgresActivityQuery = fmt.Sprintf(`SELECT pid, COALESCE(usename, ''), COALESCE(datname, ''),
  COALESCE(host(client_addr) || ':' || client_port, ''), COALESCE(state, ''),
  COALESCE(wait_event_type || '/' || wait_event, ''),
  %s, %s, (extract(epoch FROM (now() - query_start)) * %d)::bigint,
  left(query, $2), char_length(query)
FROM pg_stat_activity
WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()
  AND query_start IS NOT NULL AND state = ANY($1)
ORDER BY query_start LIMIT %d`, pgEpochMicros("backend_start"), pgEpochMicros("query_start"), msPerSecond, dbQueryFetchLimit)

func samplePostgresQueries(ctx context.Context, db *sql.DB, states []string, maxLength int) ([]DBQuery, error) {
	rows, err := db.QueryContext(ctx, postgresActivityQuery, states, maxLength+1)
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
		// The backend state is pg's "command"; its wait event the "state".
		if err := rows.Scan(&q.ID, &q.User, &q.Database, &q.Host, &q.Command, &q.State, &q.BackendStartUS, &q.QueryStartUS, &ms, &text, &length); err != nil {
			return nil, fmt.Errorf("db_queries: scan: %w", err)
		}
		q.StartedUnixMS, q.ElapsedSeconds = q.QueryStartUS/usPerMs, max(ms, 0)/msPerSecond
		q.Query, q.Truncated = dbQueryText(text, maxLength)
		if length.Int64 > int64(maxLength) {
			q.Truncated = true
		}
		q.Identity = dbQueryIdentity(q.ID, formatInt(q.BackendStartUS), formatInt(q.QueryStartUS))
		q.OSThreadID = q.ID // a PostgreSQL backend is its own process
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db_queries: pg_stat_activity: %w", err)
	}
	return out, nil
}

// dbQueryText redacts credentials, collapses whitespace and truncates a
// statement to maxLength runes. Redaction runs first so a cut never splits a
// password literal into an unredacted fragment.
func dbQueryText(raw string, maxLength int) (string, bool) {
	text := strings.Join(strings.Fields(redactSQL(raw)), " ")
	if utf8.RuneCountInString(text) <= maxLength {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:maxLength]), true
}

// sqlSecretLiteral is a quoted literal, terminated or cut off at the end of a
// truncated statement.
//
//nolint:gosec // G101: a pattern that finds credentials to redact, not one.
const sqlSecretLiteral = `(?:'(?:[^'\\]|\\.|'')*(?:'|$)|"(?:[^"\\]|\\.|"")*(?:"|$))`

var sqlSecretPatterns = []*regexp.Regexp{
	// CREATE/ALTER USER … IDENTIFIED [WITH plugin] BY|AS '…'
	regexp.MustCompile(`(?is)(\bIDENTIFIED\s+(?:WITH\s+\S+\s+)?(?:BY|AS)\s+(?:PASSWORD\s+)?)` + sqlSecretLiteral),
	// PASSWORD('…'), OLD_PASSWORD('…')
	regexp.MustCompile(`(?is)(\b(?:OLD_)?PASSWORD\s*\(\s*)` + sqlSecretLiteral),
	// SET PASSWORD [FOR u] = '…'
	regexp.MustCompile(`(?is)(\bSET\s+PASSWORD\b[^=]*=\s*)` + sqlSecretLiteral),
	// CHANGE MASTER/REPLICATION SOURCE TO MASTER_PASSWORD='…', and
	// START REPLICA|SLAVE … PASSWORD='…'
	regexp.MustCompile(`(?is)(\b(?:MASTER_|SOURCE_)?PASSWORD\s*=\s*)` + sqlSecretLiteral),
	// ALTER USER … IDENTIFIED BY '…' REPLACE '<current password>'
	regexp.MustCompile(`(?is)(\bIDENTIFIED\b[^;]*?\bREPLACE\s+)` + sqlSecretLiteral),
	// PostgreSQL: [ENCRYPTED] PASSWORD '…'
	regexp.MustCompile(`(?is)(\bPASSWORD\s+)` + sqlSecretLiteral),
}

const sqlRedacted = "'***'"

// redactSQL masks password literals in a statement: running statements are
// shown to read-only web users and sent to notifiers.
func redactSQL(text string) string {
	for _, pattern := range sqlSecretPatterns {
		text = pattern.ReplaceAllString(text, "${1}"+sqlRedacted)
	}
	return text
}

// mysqlConnWithDefaults fills the connection fields the check left unset from
// the [client] group of a MySQL option file — the procedure the mysql client
// itself uses as root (/root/.my.cnf). A missing file is not an error: the
// check then connects with its own fields. The user defaults to root, as the
// client's does for the root-run daemon.
func mysqlConnWithDefaults(cfg conn.Config, path string) (conn.Config, error) {
	if path != "" {
		opts, err := readMySQLOptionFile(path)
		if err != nil {
			return cfg, err
		}
		if cfg.User == "" {
			cfg.User = opts[mysqlOptionUser]
		}
		if cfg.Password == "" {
			cfg.Password = opts[mysqlOptionPassword]
		}
		if cfg.Socket == "" && cfg.Host == "" {
			cfg.Socket = opts[mysqlOptionSocket]
			cfg.Host = opts[mysqlOptionHost]
		}
		if cfg.Port == 0 {
			if port, err := strconv.Atoi(opts[mysqlOptionPort]); err == nil {
				cfg.Port = port
			}
		}
	}
	if cfg.User == "" {
		cfg.User = dbQueryDefaultUser
	}
	return cfg, nil
}

const (
	mysqlOptionUser     = "user"
	mysqlOptionPassword = "password"
	mysqlOptionSocket   = "socket"
	mysqlOptionHost     = "host"
	mysqlOptionPort     = "port"
)

// mysqlClientGroups are the option-file groups the mysql client reads, in
// increasing precedence.
var mysqlClientGroups = []string{"client", "client-server", "client-mariadb", "mysql"}

// readMySQLOptionFile reads the client connection options of a MySQL option
// file natively. Later groups in mysqlClientGroups override earlier ones;
// !include directives are not followed. A missing file yields no options.
func readMySQLOptionFile(path string) (map[string]string, error) {
	data, err := readFileIfExists(path)
	if err != nil || data == nil {
		return map[string]string{}, err
	}
	byGroup := map[string]map[string]string{}
	group := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '!' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			group = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "_", "-")
		if byGroup[group] == nil {
			byGroup[group] = map[string]string{}
		}
		byGroup[group][key] = mysqlOptionValue(value)
	}
	out := map[string]string{}
	for _, g := range mysqlClientGroups {
		for _, key := range []string{mysqlOptionUser, mysqlOptionPassword, mysqlOptionSocket, mysqlOptionHost, mysqlOptionPort} {
			if v, ok := byGroup[g][key]; ok {
				out[key] = v
			}
		}
	}
	return out, nil
}

func readFileIfExists(path string) ([]byte, error) {
	data, err := hostfs.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// mysqlOptionValue unquotes an option value and drops a trailing comment.
func mysqlOptionValue(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return value[1 : end+1]
		}
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return value
}

// DBQueriesFromData rehydrates the statement list a db_queries snapshot
// published, in memory ([]DBQuery) or decoded from the persisted JSON. The
// DBQuery JSON tags are the one schema.
//
// It returns a new slice: the decoded one may be the live slice a watch
// published, which readers share and must never modify.
func DBQueriesFromData(data map[string]any) []DBQuery {
	decoded := DecodeDataSlice[DBQuery](data[DataKeyDBQueries])
	out := make([]DBQuery, 0, len(decoded))
	for i := range decoded {
		if decoded[i].Engine != "" && decoded[i].ID > 0 {
			out = append(out, decoded[i])
		}
	}
	return out
}

// DBQueryKill is a request to stop one listed statement. Identity is the
// statement's identity as listed; Require, set by the automatic path, is
// re-checked against the fresh sample before acting.
type DBQueryKill struct {
	ID       int64
	Identity string
	Mode     string
	Require  *DBQueryKillRequirement
}

// DBQueryKillRequirement is the automatic kill's condition: the statement must
// still match the selector and have run at least After.
type DBQueryKillRequirement struct {
	After    time.Duration
	Selector DBQuerySelector
	// Filter is the check's own users/databases filter: the watch never
	// stops a statement it was told to ignore.
	Filter DBQuerySelector
}

// ErrDBQueryChanged reports that the listed statement is no longer running (it
// finished, or its connection now runs another statement).
var ErrDBQueryChanged = errors.New("the statement is no longer running; refresh the list")

// KillDBQuery stops one statement after re-verifying, against a fresh sample,
// that the connection still runs exactly the listed statement. MariaDB cancels
// by QUERY_ID and PostgreSQL verifies and signals in one statement, so neither
// can hit a later statement; MySQL's KILL QUERY targets the connection, which
// leaves a documented sub-second race. It returns the statement it stopped.
func KillDBQuery(ctx context.Context, cfg DBQueryConfig, req DBQueryKill) (DBQuery, error) {
	if req.ID <= 0 || req.Identity == "" {
		return DBQuery{}, errors.New("a kill needs the statement id and identity")
	}
	if !ValidDBQueryKillMode(req.Mode) {
		return DBQuery{}, fmt.Errorf("kill mode must be %s", DBQueryKillModeSummary)
	}
	db, err := cfg.open(ctx)
	if err != nil {
		return DBQuery{}, err
	}
	defer func() { _ = db.Close() }()
	current, err := cfg.sample(ctx, db)
	if err != nil {
		return DBQuery{}, err
	}
	target, err := verifyDBQueryTarget(current, req)
	if err != nil {
		return DBQuery{}, err
	}
	if cfg.Engine == SQLEngineMySQL {
		return target, killMySQLQuery(ctx, db, target, req.Mode)
	}
	return target, killPostgresQuery(ctx, db, target, req.Mode)
}

// verifyDBQueryTarget finds the requested statement in a fresh sample and
// checks it is still the listed one and, on the automatic path, still eligible.
func verifyDBQueryTarget(current []DBQuery, req DBQueryKill) (DBQuery, error) {
	for i := range current {
		q := current[i]
		if q.ID != req.ID {
			continue
		}
		if !identityMatches(q, req.Identity) {
			return DBQuery{}, ErrDBQueryChanged
		}
		if q.Stopping() {
			return DBQuery{}, fmt.Errorf("statement %d is already being stopped", q.ID)
		}
		if r := req.Require; r != nil {
			if q.Elapsed() < r.After || !r.Selector.Matches(q) || !r.Filter.Matches(q) {
				return DBQuery{}, fmt.Errorf("statement %d no longer meets the kill condition", q.ID)
			}
		}
		return q, nil
	}
	return DBQuery{}, ErrDBQueryChanged
}

// identityMatches compares a fresh statement with a listed identity: exactly,
// except MySQL's derived start, which tolerates the whole-second counter.
func identityMatches(q DBQuery, identity string) bool {
	if q.Engine != SQLEngineMySQL {
		return q.Identity == identity
	}
	rest, ok := strings.CutPrefix(identity, q.ItemKey()+dbQueryIdentitySep)
	if !ok {
		return false
	}
	started, err := strconv.ParseInt(rest, dbQueryNumberBase, 64)
	if err != nil {
		return false
	}
	// The key matched id and fingerprint; only the derived start can differ.
	return (time.Duration(q.StartedUnixMS-started) * time.Millisecond).Abs() <= mysqlStartTolerance
}

func killMySQLQuery(ctx context.Context, db *sql.DB, target DBQuery, mode string) error {
	var stmt string
	switch {
	case mode == DBQueryKillModeConnection:
		stmt = "KILL CONNECTION " + formatInt(target.ID)
	case target.QueryID > 0:
		stmt = "KILL QUERY ID " + formatInt(target.QueryID)
	default:
		stmt = "KILL QUERY " + formatInt(target.ID)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("%s: %w", stmt, err)
	}
	return nil
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
WHERE pid = $1 AND backend_type = 'client backend'
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
