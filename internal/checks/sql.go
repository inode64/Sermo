package checks

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sermo/internal/conn"
)

// sqlCheck runs a query against a database and compares the scalar result (first
// column of the first row) against a value. It is condition-style: OK == true
// means the comparison holds. It reuses the conn DSN builders (mysql/postgres)
// and the read-only SQLite open of the sqlite check; all three drivers share the
// database/sql API. The query is read at face value — use a read-only user.
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

	ok, err := sqlCompare(result, c.op, c.value)
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
		data["value"] = f
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
// as []byte (mysql), int64/float64 (sqlite/pq) or strings; []byte/string are
// kept verbatim, the rest formatted with %v.
func sqlValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

// sqlCompare evaluates "result op value". Numeric ops (> >= < <=) parse both as
// floats; == and != compare numerically when both parse as numbers, else as
// strings; =~ matches result against value as a Go (RE2) regexp.
func sqlCompare(result, op, value string) (bool, error) {
	switch op {
	case ">", ">=", "<", "<=":
		rf, err := strconv.ParseFloat(strings.TrimSpace(result), 64)
		if err != nil {
			return false, fmt.Errorf("result %q is not numeric for op %s", result, op)
		}
		vf, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return false, fmt.Errorf("value %q is not numeric", value)
		}
		return compareFloat(rf, op, vf), nil
	case "==", "!=":
		if rf, err := strconv.ParseFloat(strings.TrimSpace(result), 64); err == nil {
			if vf, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				return compareFloat(rf, op, vf), nil
			}
		}
		if op == "==" {
			return result == value, nil
		}
		return result != value, nil
	case "=~":
		re, err := regexp.Compile(value)
		if err != nil {
			return false, fmt.Errorf("invalid regex %q: %v", value, err)
		}
		return re.MatchString(result), nil
	default:
		return false, fmt.Errorf("unsupported op %q", op)
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
	engine := asString(entry["engine"])
	driver, ok := sqlEngineDriver(engine)
	if !ok {
		return nil, "sql check requires an engine (mysql, mariadb, postgres, postgresql, sqlite)"
	}
	query := asString(entry["query"])
	if query == "" {
		return nil, "sql check requires a query"
	}
	op := asString(entry["op"])
	if !validSQLOp(op) {
		return nil, "sql check op must be one of ==, !=, >, >=, <, <=, =~"
	}
	value := scalarString(entry["value"])

	var dsn string
	switch driver {
	case "sqlite":
		path := asString(entry["path"])
		if path == "" {
			return nil, "sql check (sqlite) requires a path"
		}
		dsn = "file:" + path + "?mode=ro&_pragma=busy_timeout(2000)"
	default:
		if asString(entry["user"]) == "" {
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
		Host:     asString(entry["host"]),
		User:     asString(entry["user"]),
		Password: asString(entry["password"]),
		Database: asString(entry["database"]),
		TLS:      tlsString(entry["tls"]),
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if proto, ok := conn.Lookup(engine); ok {
		cfg.Port = proto.DefaultPort()
	}
	if p, ok := intField(entry["port"]); ok {
		cfg.Port = p
	}
	return cfg
}

// validSQLOp reports whether op is a supported sql comparison operator.
func validSQLOp(op string) bool {
	switch op {
	case "==", "!=", ">", ">=", "<", "<=", "=~":
		return true
	default:
		return false
	}
}
