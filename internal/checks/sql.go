package checks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// sqlCheck is condition-style: OK means the scalar query result matches
// op/value. The query is run as given; use a read-only user. levels: tiers
// (op/value) escalate a match's severity.
type sqlCheck struct {
	base
	engine string
	open   func(context.Context) (*sql.DB, error)
	query  string
	valueMatcher
	grades grades[valueMatcher]
}

func (c sqlCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	data := map[string]any{
		DataKeyEngine: c.engine,
		DataKeyQuery:  c.query,
	}
	db, err := c.open(ctx)
	if err != nil {
		return c.failure(err, start, data)
	}
	defer func() { _ = db.Close() }()

	result, isNull, err := sqlScalarDB(ctx, db, c.query)
	if err != nil {
		return c.failure(err, start, data)
	}
	if isNull {
		return c.base.unavailableResult(fmt.Sprintf("sql %s: query returned NULL", c.engine), start)
	}

	res := finishScalarCompare(c.base, "sql "+c.engine, result, c.valueMatcher, start, data)
	return c.grade(res, result)
}

// failure reports a connection or query error (see driverFailure).
func (c sqlCheck) failure(err error, start time.Time, data map[string]any) Result {
	return c.base.driverFailure("sql "+c.engine, err, start, data)
}

// grade raises a matching result to the gravest levels: tier its value also
// matches; an unmatched or unavailable result keeps its declared severity.
func (c sqlCheck) grade(res Result, value string) Result {
	return raiseSeverity(res, c.grades.highest(func(m valueMatcher) bool {
		ok, err := m.compare(value)
		return err == nil && ok
	}))
}

// sqlScalarDB runs query and returns the first column of the first row as a
// string. The second return reports a NULL result.
func sqlScalarDB(ctx context.Context, db *sql.DB, query string) (string, bool, error) {
	var raw any
	if err := db.QueryRowContext(ctx, query).Scan(&raw); err != nil {
		return "", false, fmt.Errorf("sql query: %w", err)
	}
	if raw == nil {
		return "", true, nil
	}
	return sqlValueString(raw), false, nil
}

// sqlValueString renders a scanned SQL value as a string. Drivers return numbers
// as []byte (mysql), int64/float64 (sqlite/pgx) or strings; captured text is
// trimmed so values from queries are stable in messages, data and hook env.
func sqlValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return strings.TrimSpace(string(t))
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

// IsMySQLEngine reports whether engine speaks the MySQL protocol (mysql or
// mariadb): the engines that accept a socket and a defaults_file.
func IsMySQLEngine(engine string) bool {
	driver, _ := sqlEngineDriver(engine)
	return driver == SQLEngineMySQL
}

// sqlEngineDriver maps an engine token to its database/sql driver name.
func sqlEngineDriver(engine string) (string, bool) {
	switch engine {
	case SQLEngineMySQL, SQLEngineMariaDB:
		return SQLEngineMySQL, true
	case SQLEnginePostgres, SQLEnginePostgreSQL:
		return conn.PostgresDriverName, true
	case SQLEngineSQLite, SQLEngineSQLite3:
		return SQLEngineSQLite, true
	default:
		return "", false
	}
}

// buildSQLCheck builds a sql check with an opener for its engine.
// MySQL/Postgres reuse the conn connectors and host/port/user/
// password/database/tls fields; sqlite opens `path` read-only.
func buildSQLCheck(b base, entry map[string]any) (Check, string) {
	engine := cfgval.AsString(entry[CheckKeyEngine])
	driver, ok := sqlEngineDriver(engine)
	if !ok {
		return nil, "sql check requires an engine (" + SQLEngineSummary + ")"
	}
	query := cfgval.AsString(entry[CheckKeyQuery])
	if query == "" {
		return nil, "sql check requires a query"
	}
	op, value, msg := assertOpValue(entry, CheckTypeSQL)
	if msg != "" {
		return nil, msg
	}

	var open func(context.Context) (*sql.DB, error)
	switch driver {
	case SQLEngineSQLite:
		path := cfgval.AsString(entry[CheckKeyPath])
		if path == "" {
			return nil, "sql check (sqlite) requires a path"
		}
		dsn := sqliteReadOnlyDSN(path)
		open = func(context.Context) (*sql.DB, error) {
			db, err := sql.Open(SQLEngineSQLite, dsn)
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", engine, err)
			}
			return db, nil
		}
	default:
		var msg string
		if open, msg = sqlServerOpener(engine, driver, entry); msg != "" {
			return nil, msg
		}
	}
	return sqlCheck{base: b, engine: engine, open: open, query: query, valueMatcher: newValueMatcher(op, value),
		grades: sqlGrades(b.levels)}, ""
}

// sqlServerOpener returns the opener for a mysql or postgres sql check; a mysql
// check connects through mysqlServerOpener.
func sqlServerOpener(engine, driver string, entry map[string]any) (func(context.Context) (*sql.DB, error), string) {
	requiresUser := "sql check (" + engine + ") requires a user"
	if driver == SQLEngineMySQL {
		open, ok := mysqlServerOpener(engine, entry)
		if !ok {
			return nil, requiresUser
		}
		return open, ""
	}
	if cfgval.AsString(entry[CheckKeyUser]) == "" {
		return nil, requiresUser
	}
	cfg := sqlConnConfig(engine, entry)
	return func(ctx context.Context) (*sql.DB, error) { return conn.OpenPostgresDB(ctx, cfg) }, ""
}

// mysqlServerOpener returns the opener for a mysql/mariadb check (sql,
// replication). With a defaults_file the target resolves per run, like
// db_queries: its own fields first, then the option file (whose [client] user
// defaults to root, the account that keeps a reserved connection on a
// saturated server), then the protocol defaults. Without one the check must
// name its user; ok is false when it names neither.
func mysqlServerOpener(engine string, entry map[string]any) (open func(context.Context) (*sql.DB, error), ok bool) {
	if defaultsFile := cfgval.AsString(entry[CheckKeyDefaultsFile]); defaultsFile != "" {
		own := ownConnConfig(engine, SQLEngineMySQL, entry)
		return func(ctx context.Context) (*sql.DB, error) {
			target, err := mysqlTarget(own, defaultsFile)
			if err != nil {
				return nil, err
			}
			return conn.OpenMySQLDB(ctx, target)
		}, true
	}
	if cfgval.AsString(entry[CheckKeyUser]) == "" {
		return nil, false
	}
	cfg := sqlConnConfig(engine, entry)
	return func(ctx context.Context) (*sql.DB, error) { return conn.OpenMySQLDB(ctx, cfg) }, true
}

// sqlGrades parses the kept levels: tiers of a sql check, each restating the
// op/value comparison with a stricter bound.
func sqlGrades(specs []levelSpec) grades[valueMatcher] {
	return parseGrades(specs, func(tier map[string]any) (valueMatcher, bool) {
		op := cfgval.String(tier[CheckKeyOp])
		if op == "" {
			return valueMatcher{}, false
		}
		return newValueMatcher(op, cfgval.String(tier[CheckKeyValue])), true
	})
}

// sqlConnConfig builds a conn.Config for a mysql/postgres sql check, defaulting
// the port to the engine's standard port (via the conn registry). A mysql
// check may connect over a Unix socket instead of host:port.
func sqlConnConfig(engine string, entry map[string]any) conn.Config {
	cfg := databaseConnectionConfig(entry)
	if driver, _ := sqlEngineDriver(engine); driver == SQLEngineMySQL {
		cfg.Socket = cfgval.AsString(entry[CheckKeySocket])
	}
	return preparedConnectionConfig(engine, cfg, entry)
}
