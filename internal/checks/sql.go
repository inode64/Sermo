package checks

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/output"
)

// sqlCheck is condition-style: OK means the scalar query result matches
// op/value. The query is run as given; use a read-only user.
type sqlCheck struct {
	base
	engine string
	driver string // database/sql driver name: mysql | postgres | sqlite
	dsn    string
	query  string
	op     string
	value  string
}

func (c sqlCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	result, isNull, err := sqlScalar(ctx, c.driver, c.dsn, c.query)
	if err != nil {
		return c.result(false, fmt.Sprintf("sql %s: %v", c.engine, err), start)
	}
	if isNull {
		return c.result(false, fmt.Sprintf("sql %s: query returned NULL", c.engine), start)
	}

	return finishScalarCompare(c.base, "sql "+c.engine, result, c.op, c.value, start, map[string]any{
		DataKeyEngine: c.engine,
		DataKeyQuery:  c.query,
	})
}

// sqlScalar opens the database, runs query and returns the first column of the
// first row as a string. The second return reports a NULL result.
func sqlScalar(ctx context.Context, driver, dsn, query string) (string, bool, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = db.Close() }()

	var raw any
	if err := db.QueryRowContext(ctx, query).Scan(&raw); err != nil {
		return "", false, err
	}
	if raw == nil {
		return "", true, nil
	}
	return sqlValueString(raw), false, nil
}

// sqlValueString renders a scanned SQL value as a string. Drivers return numbers
// as []byte (mysql), int64/float64 (sqlite/pq) or strings; captured text is
// trimmed so values from queries are stable in messages, data and hook env.
func sqlValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return output.Trim(string(t))
	case string:
		return output.Trim(t)
	default:
		return output.Trim(fmt.Sprintf("%v", t))
	}
}

// sqlEngineDriver maps an engine token to its database/sql driver name.
func sqlEngineDriver(engine string) (string, bool) {
	switch engine {
	case SQLEngineMySQL, SQLEngineMariaDB:
		return SQLEngineMySQL, true
	case SQLEnginePostgres, SQLEnginePostgreSQL:
		return SQLEnginePostgres, true
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
	op := cfgval.AsString(entry[CheckKeyOp])
	if !validCompareOp(op) {
		return nil, "sql check op must be one of " + cfgval.AssertOpSummary
	}
	value := cfgval.String(entry[CheckKeyValue])
	if value == "" {
		return nil, "sql check requires a value"
	}
	if err := ValidateAssertionValue(CheckKeyValue, op, value); err != nil {
		return nil, "sql check " + err.Error()
	}

	var dsn string
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
		} else {
			dsn = conn.PostgresDSN(cfg)
		}
	}
	return sqlCheck{base: b, engine: engine, driver: driver, dsn: dsn, query: query, op: op, value: value}, ""
}

// sqlConnConfig builds a conn.Config for a mysql/postgres sql check, defaulting
// the port to the engine's standard port (via the conn registry).
func sqlConnConfig(engine string, entry map[string]any) conn.Config {
	cfg := databaseConnectionConfig(entry)
	if proto, ok := conn.Lookup(engine); ok {
		cfg.Port = connectionPort(entry, proto.DefaultPort())
	} else {
		cfg.Port = connectionPort(entry, cfg.Port)
	}
	return cfg
}
