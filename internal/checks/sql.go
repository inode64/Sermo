package checks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// sqlCheck is condition-style: OK means the scalar query result matches
// op/value. The query is run as given; use a read-only user.
type sqlCheck struct {
	base
	engine string
	driver string // database/sql driver name: mysql | pgx | sqlite
	dsn    string
	open   func(context.Context) (*sql.DB, error)
	query  string
	op     string
	value  string
}

func (c sqlCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	db, err := c.openDB(ctx)
	if err != nil {
		return c.base.unavailableResult(fmt.Sprintf("sql %s: %v", c.engine, err), start)
	}
	defer func() { _ = db.Close() }()

	result, isNull, err := sqlScalarDB(ctx, db, c.query)
	if err != nil {
		return c.base.unavailableResult(fmt.Sprintf("sql %s: %v", c.engine, err), start)
	}
	if isNull {
		return c.base.unavailableResult(fmt.Sprintf("sql %s: query returned NULL", c.engine), start)
	}

	return finishScalarCompare(c.base, "sql "+c.engine, result, c.op, c.value, start, map[string]any{
		DataKeyEngine: c.engine,
		DataKeyQuery:  c.query,
	})
}

func (c sqlCheck) openDB(ctx context.Context) (*sql.DB, error) {
	if c.open != nil {
		return c.open(ctx)
	}
	db, err := sql.Open(c.driver, c.dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", c.engine, err)
	}
	return db, nil
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

// buildSQLCheck builds a sql check, resolving the driver and connection DSN from
// the engine: mysql/postgres reuse the conn DSN builders and host/port/user/
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

	var dsn string
	var open func(context.Context) (*sql.DB, error)
	switch driver {
	case SQLEngineSQLite:
		path := cfgval.AsString(entry[CheckKeyPath])
		if path == "" {
			return nil, "sql check (sqlite) requires a path"
		}
		dsn = sqliteReadOnlyDSN(path)
	default:
		if cfgval.AsString(entry[CheckKeyUser]) == "" {
			return nil, "sql check (" + engine + ") requires a user"
		}
		cfg := sqlConnConfig(engine, entry)
		if driver == SQLEngineMySQL {
			dsn = conn.MySQLDSN(cfg)
			open = func(ctx context.Context) (*sql.DB, error) { return conn.OpenMySQLDB(ctx, cfg) }
		} else {
			dsn = conn.PostgresDSN(cfg)
			open = func(ctx context.Context) (*sql.DB, error) { return conn.OpenPostgresDB(ctx, cfg) }
		}
	}
	return sqlCheck{base: b, engine: engine, driver: driver, dsn: dsn, open: open, query: query, op: op, value: value}, ""
}

// sqlConnConfig builds a conn.Config for a mysql/postgres sql check, defaulting
// the port to the engine's standard port (via the conn registry).
func sqlConnConfig(engine string, entry map[string]any) conn.Config {
	cfg := databaseConnectionConfig(entry)
	cfg.Port = connectionPort(entry, 0)
	if _, resolved, ok := conn.Prepare(engine, cfg); ok {
		return resolved
	}
	return cfg
}
