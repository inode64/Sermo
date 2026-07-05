package checks

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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

	ok, err := compareValue(result, c.op, c.value)
	if err != nil {
		return c.result(false, fmt.Sprintf("sql %s: %v", c.engine, err), start)
	}

	res := c.result(ok, fmt.Sprintf("sql %s: %q %s %q = %t", c.engine, result, c.op, c.value, ok), start)
	data := map[string]any{
		"engine":    c.engine,
		"query":     c.query,
		"op":        c.op,
		"threshold": c.value,
		"result":    result,
	}
	if f, perr := strconv.ParseFloat(strings.TrimSpace(result), 64); perr == nil {
		data[fieldValue] = f
	}
	res.Data = data
	return res
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
	case "mysql", "mariadb":
		return "mysql", true
	case "postgres", "postgresql":
		return "postgres", true
	case "sqlite", "sqlite3":
		return "sqlite", true
	default:
		return "", false
	}
}

// buildSQLCheck builds a sql check, resolving the driver and connection DSN from
// the engine: mysql/postgres reuse the conn DSN builders and host/port/user/
// password/database/tls fields; sqlite opens `path` read-only.
func buildSQLCheck(b base, entry map[string]any) (Check, string) {
	engine := cfgval.AsString(entry["engine"])
	driver, ok := sqlEngineDriver(engine)
	if !ok {
		return nil, "sql check requires an engine (mysql, mariadb, postgres, postgresql, sqlite)"
	}
	query := cfgval.AsString(entry["query"])
	if query == "" {
		return nil, "sql check requires a query"
	}
	op := cfgval.AsString(entry["op"])
	if !validCompareOp(op) {
		return nil, "sql check op must be one of ==, !=, >, >=, <, <=, contains, =~"
	}
	value := cfgval.String(entry["value"])
	if value == "" {
		return nil, "sql check requires a value"
	}
	if err := ValidateAssertionValue("value", op, value); err != nil {
		return nil, "sql check " + err.Error()
	}

	var dsn string
	switch driver {
	case "sqlite":
		path := cfgval.AsString(entry["path"])
		if path == "" {
			return nil, "sql check (sqlite) requires a path"
		}
		dsn = "file:" + path + "?mode=ro&_pragma=busy_timeout(2000)"
	default:
		if cfgval.AsString(entry["user"]) == "" {
			return nil, "sql check (" + engine + ") requires a user"
		}
		cfg := sqlConnConfig(engine, entry)
		if driver == "mysql" {
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
	cfg := conn.Config{
		Host:     cfgval.AsString(entry["host"]),
		User:     cfgval.AsString(entry["user"]),
		Password: cfgval.AsString(entry["password"]),
		Database: cfgval.AsString(entry["database"]),
		TLS:      tlsString(entry["tls"]),
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if proto, ok := conn.Lookup(engine); ok {
		cfg.Port = proto.DefaultPort()
	}
	if p, ok := cfgval.Int(entry["port"]); ok {
		cfg.Port = p
	}
	return cfg
}
