package checks

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLValueString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{[]byte("42"), "42"},
		{"text", "text"},
		{[]byte("\n42\n"), "42"},
		{"\nfirst\n\nlast\n", "first\n\nlast"},
		{int64(7), "7"},
		{3.5, "3.5"},
	}
	for _, c := range cases {
		if got := sqlValueString(c.in); got != c.want {
			t.Errorf("sqlValueString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// makeSQLMetaDB creates a temp SQLite database with a meta table for end-to-end
// tests and returns its path.
func makeSQLMetaDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE meta (k TEXT, v TEXT); INSERT INTO meta VALUES ('schema','v3'),('rows','42');`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildSQLCheckSQLiteEndToEnd(t *testing.T) {
	path := makeSQLMetaDB(t)

	// Numeric comparison: 42 rows > 10 -> OK (condition met).
	built, warns := Build(map[string]any{
		"rows": map[string]any{
			"type": "sql", "engine": "sqlite", "path": path,
			"query": "SELECT v FROM meta WHERE k='rows'", "op": ">", "value": "10",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("sql sqlite check should build: warns=%v", warns)
	}
	res := built[0].Check.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected OK (42 > 10): %q", res.Message)
	}
	if res.Data["result"] != "42" || res.Data["value"].(float64) != 42 {
		t.Fatalf("data = %v", res.Data)
	}

	// Regex comparison on a string column.
	built, _ = Build(map[string]any{
		"schema": map[string]any{
			"type": "sql", "engine": "sqlite", "path": path,
			"query": "SELECT v FROM meta WHERE k='schema'", "op": "=~", "value": "^v[0-9]+$",
		},
	}, Deps{DefaultTimeout: time.Second})
	if res := built[0].Check.Run(context.Background()); !res.OK {
		t.Fatalf("expected regex match for v3: %q", res.Message)
	}

	// Comparison that does not hold -> OK=false (condition-style, no error).
	built, _ = Build(map[string]any{
		"rows": map[string]any{
			"type": "sql", "engine": "sqlite", "path": path,
			"query": "SELECT v FROM meta WHERE k='rows'", "op": "<", "value": "10",
		},
	}, Deps{DefaultTimeout: time.Second})
	if res := built[0].Check.Run(context.Background()); res.OK {
		t.Fatalf("expected not-OK (42 < 10 is false): %q", res.Message)
	}

	// A bad query fails the check (error path).
	built, _ = Build(map[string]any{
		"bad": map[string]any{
			"type": "sql", "engine": "sqlite", "path": path,
			"query": "SELECT nope FROM missing", "op": ">", "value": "0",
		},
	}, Deps{DefaultTimeout: time.Second})
	if res := built[0].Check.Run(context.Background()); res.OK {
		t.Fatalf("a failing query must fail the check: %q", res.Message)
	}
}

func TestBuildSQLCheckWiring(t *testing.T) {
	// mysql engine: default port and DSN resolved; needs a user.
	built, warns := Build(map[string]any{
		"q": map[string]any{
			"type": "sql", "engine": "mysql", "user": "monitor", "password": "p",
			"database": "app", "query": "SELECT 1", "op": "==", "value": "1",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("mysql sql check should build: warns=%v", warns)
	}
	cc, ok := built[0].Check.(sqlCheck)
	if !ok || cc.driver != "mysql" || cc.engine != "mysql" {
		t.Fatalf("built = %T %+v", built[0].Check, built[0].Check)
	}
	if !strings.Contains(cc.dsn, "@tcp(") {
		t.Fatalf("mysql dsn = %q, want a go-sql-driver @tcp(...) DSN", cc.dsn)
	}

	// postgres engine resolves to the postgres driver.
	built, _ = Build(map[string]any{
		"q": map[string]any{
			"type": "sql", "engine": "postgresql", "user": "u",
			"query": "SELECT 1", "op": ">", "value": "0",
		},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(sqlCheck); cc.driver != "postgres" || !strings.Contains(cc.dsn, "postgres://") {
		t.Fatalf("driver = %q dsn = %q, want postgres driver and postgres:// DSN", cc.driver, cc.dsn)
	}

	// Missing user (mysql) warns.
	if _, warns := Build(map[string]any{
		"q": map[string]any{"type": "sql", "engine": "mysql", "query": "SELECT 1", "op": "==", "value": "1"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("mysql sql check without user should warn")
	}

	// Unknown engine warns.
	if _, warns := Build(map[string]any{
		"q": map[string]any{"type": "sql", "engine": "oracle", "query": "SELECT 1", "op": "==", "value": "1"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("unknown engine should warn")
	}

	// Bad op warns.
	if _, warns := Build(map[string]any{
		"q": map[string]any{"type": "sql", "engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": "~~", "value": "1"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("bad op should warn")
	}
	if _, warns := Build(map[string]any{
		"q": map[string]any{"type": "sql", "engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": ">"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("missing value should warn")
	}
	if _, warns := Build(map[string]any{
		"q": map[string]any{"type": "sql", "engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": ">", "value": "many"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("non-numeric ordering value should warn")
	}
	if _, warns := Build(map[string]any{
		"q": map[string]any{"type": "sql", "engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": "=~", "value": "["},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("bad regex value should warn")
	}
}

func TestSQLConnConfigHostDefault(t *testing.T) {
	// An unset host defaults to loopback; an explicit host is preserved.
	if got := sqlConnConfig("mysql", map[string]any{"user": "u"}).Host; got != "127.0.0.1" {
		t.Errorf("default host = %q, want 127.0.0.1", got)
	}
	if got := sqlConnConfig("mysql", map[string]any{"user": "u", "host": "db.internal"}).Host; got != "db.internal" {
		t.Errorf("explicit host = %q, want db.internal", got)
	}
}
