package checks

import (
	"context"
	"database/sql"
	"maps"
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
	if res := built[0].Check.Run(context.Background()); res.OK || !res.Unavailable {
		t.Fatalf("a failing query must be unavailable for guards: %+v", res)
	}
}

func TestBuildSQLCheckWiring(t *testing.T) {
	// MySQL retains its bound connector and requires a user.
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
	if !ok || cc.engine != "mysql" {
		t.Fatalf("built = %T %+v", built[0].Check, built[0].Check)
	}
	if cc.open == nil {
		t.Fatal("mysql sql check must retain its connector so interface binding is not lost")
	}

	// postgres engine resolves to the pgx database/sql driver.
	built, _ = Build(map[string]any{
		"q": map[string]any{
			"type": "sql", "engine": "postgres", "user": "u",
			"query": "SELECT 1", "op": ">", "value": "0",
		},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(sqlCheck); cc.engine != "postgres" || cc.open == nil {
		t.Fatalf("engine = %q opener=%v, want PostgreSQL with a bound opener", cc.engine, cc.open != nil)
	}

	// assertSQLBuildWarns builds one sql check entry and asserts it warns.
	assertSQLBuildWarns := func(q map[string]any, msg string) {
		t.Helper()
		q["type"] = "sql"
		if _, warns := Build(map[string]any{"q": q}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
			t.Fatal(msg)
		}
	}
	assertSQLBuildWarns(map[string]any{"engine": "mysql", "query": "SELECT 1", "op": "==", "value": "1"},
		"mysql sql check without user should warn")
	assertSQLBuildWarns(map[string]any{"engine": "oracle", "query": "SELECT 1", "op": "==", "value": "1"},
		"unknown engine should warn")
	assertSQLBuildWarns(map[string]any{"engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": "~~", "value": "1"},
		"bad op should warn")
	assertSQLBuildWarns(map[string]any{"engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": ">"},
		"missing value should warn")
	assertSQLBuildWarns(map[string]any{"engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": ">", "value": "many"},
		"non-numeric ordering value should warn")
	assertSQLBuildWarns(map[string]any{"engine": "sqlite", "path": "/x.db", "query": "SELECT 1", "op": "=~", "value": "["},
		"bad regex value should warn")
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

// TestSQLCheckSQLiteAbsentValue pins how a sqlite sql check reads a database
// that does not exist yet. Exim creates its hints databases on first use, so
// without absent_value a host that never deferred a message showed its cleanup
// watch failed; with it the query stands for that value, and only an absent
// file does, so a present database and any other error keep their readings.
func TestSQLCheckSQLiteAbsentValue(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "retry")
	run := func(t *testing.T, entry map[string]any) Result {
		t.Helper()
		check := map[string]any{"type": "sql", "engine": "sqlite", "query": "SELECT count(*) FROM meta", "op": ">", "value": "5000"}
		maps.Copy(check, entry)
		built, warns := Build(map[string]any{"retry": check}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("sql check should build: warns=%v", warns)
		}
		return built[0].Check.Run(context.Background())
	}

	t.Run("absent file reads as absent_value", func(t *testing.T) {
		res := run(t, map[string]any{"path": missing, "absent_value": 0})
		if res.OK || res.Unavailable || res.Observation() != ObservationHealthy {
			t.Fatalf("an absent database below the limit must be healthy: %+v", res)
		}
		if res.Data[DataKeyValue] != float64(0) || res.Data[DataKeyPath] != missing {
			t.Fatalf("data = %v, want value 0 and the path", res.Data)
		}
		if !strings.Contains(res.Message, "does not exist") {
			t.Fatalf("message %q must say the database does not exist", res.Message)
		}
	})
	t.Run("absent_value still meets the condition", func(t *testing.T) {
		if res := run(t, map[string]any{"path": missing, "absent_value": "9000"}); !res.OK || res.Unavailable {
			t.Fatalf("absent_value above the limit must match the condition: %+v", res)
		}
	})
	t.Run("absent file without absent_value cannot be read", func(t *testing.T) {
		if res := run(t, map[string]any{"path": missing}); res.OK || !res.Unavailable {
			t.Fatalf("a missing database without absent_value must stay unavailable: %+v", res)
		}
	})
	t.Run("present database is queried", func(t *testing.T) {
		res := run(t, map[string]any{"path": makeSQLMetaDB(t), "absent_value": "9000"})
		if res.OK || res.Data[DataKeyValue] != float64(2) {
			t.Fatalf("a present database must answer its query (2 rows): %+v", res)
		}
	})
}
