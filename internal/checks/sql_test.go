package checks

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLCompare(t *testing.T) {
	cases := []struct {
		result, op, value string
		want              bool
		wantErr           bool
	}{
		// numeric ordering
		{"42", ">", "10", true, false},
		{"42", ">", "100", false, false},
		{"10", ">=", "10", true, false},
		{"5", "<", "10", true, false},
		{"10", "<=", "9", false, false},
		// numeric equality
		{"7", "==", "7", true, false},
		{"7.0", "==", "7", true, false}, // numeric, not string
		{"7", "!=", "8", true, false},
		// string equality (non-numeric)
		{"ok", "==", "ok", true, false},
		{"ok", "==", "fail", false, false},
		{"ok", "!=", "fail", true, false},
		// regex
		{"v1.2.3", "=~", `^v[0-9]+\.[0-9]+`, true, false},
		{"nope", "=~", `^v[0-9]+`, false, false},
		// errors
		{"notnum", ">", "10", false, true}, // non-numeric result for ordering op
		{"10", ">", "notnum", false, true}, // non-numeric threshold
		{"x", "=~", "[", false, true},      // invalid regexp
		{"x", "><", "1", false, true},      // unsupported op
	}
	for _, c := range cases {
		got, err := sqlCompare(c.result, c.op, c.value)
		if c.wantErr {
			if err == nil {
				t.Errorf("sqlCompare(%q,%q,%q): expected error", c.result, c.op, c.value)
			}
			continue
		}
		if err != nil {
			t.Errorf("sqlCompare(%q,%q,%q): %v", c.result, c.op, c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("sqlCompare(%q,%q,%q) = %v, want %v", c.result, c.op, c.value, got, c.want)
		}
	}
}

func TestSQLValueString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{[]byte("42"), "42"},
		{"text", "text"},
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

	// postgres engine resolves to the postgres driver.
	built, _ = Build(map[string]any{
		"q": map[string]any{
			"type": "sql", "engine": "postgresql", "user": "u",
			"query": "SELECT 1", "op": ">", "value": "0",
		},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(sqlCheck); cc.driver != "postgres" {
		t.Fatalf("driver = %q, want postgres", cc.driver)
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
}
