package checks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// makeSQLiteDB creates a small valid SQLite database at path.
func makeSQLiteDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('x')"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteCheckHealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.db")
	makeSQLiteDB(t, path)

	c := sqliteCheck{name: "db", timeout: 5 * time.Second, path: path}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a healthy db should pass: %q", res.Message)
	}
	if res.Data["path"] != path {
		t.Fatalf("data = %v", res.Data)
	}
}

func TestSQLiteCheckQuick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.db")
	makeSQLiteDB(t, path)
	c := sqliteCheck{name: "db", timeout: 5 * time.Second, path: path, quick: true}
	if !c.Run(context.Background()).OK {
		t.Fatal("quick_check on a healthy db should pass")
	}
}

func TestSQLiteCheckMissing(t *testing.T) {
	c := sqliteCheck{name: "db", timeout: time.Second, path: filepath.Join(t.TempDir(), "nope.db")}
	if c.Run(context.Background()).OK {
		t.Fatal("a missing file must fail")
	}
}

func TestSQLiteCheckNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database, just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := sqliteCheck{name: "db", timeout: time.Second, path: path}
	if c.Run(context.Background()).OK {
		t.Fatal("a non-sqlite file must fail")
	}
}

// A path holding URI syntax ('?' starts SQLite's query, '#' a fragment, '%' an
// escape) must open exactly that file, read-only: a prefix opened read-write
// would be created, and an operator query could write to it.
func TestSQLiteCheckOpensURISyntaxPathReadOnly(t *testing.T) {
	// One parent directory: a subtest's own TempDir would carry these
	// characters into every path, the fixture's included.
	root := t.TempDir()
	for i, name := range []string{"a?b.db", "a#b.db", "50%25.db", "a?_pragma=user_version(7)"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, strconv.Itoa(i))
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			plain := filepath.Join(dir, "plain.db")
			makeSQLiteDB(t, plain)
			path := filepath.Join(dir, name)
			if err := os.Rename(plain, path); err != nil {
				t.Fatal(err)
			}

			c := sqliteCheck{name: "db", timeout: 5 * time.Second, path: path}
			if res := c.Run(context.Background()); !res.OK {
				t.Fatalf("check of %q failed: %q", path, res.Message)
			}
			db, err := sql.Open(SQLEngineSQLite, sqliteReadOnlyDSN(path))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var rows int
			if err := db.QueryRow("SELECT count(*) FROM t").Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("read of %q = %d, %v; want the configured database", path, rows, err)
			}
			if _, err := db.Exec("CREATE TABLE w (x)"); err == nil {
				t.Fatal("write succeeded through a read-only DSN")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("directory holds %v, want only %q", entries, name)
			}
		})
	}
}

func TestBuildSQLiteCheck(t *testing.T) {
	_, warns := Build(map[string]any{
		"db": map[string]any{"type": "sqlite"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 || !strings.Contains(warns[0], "path") {
		t.Fatalf("missing path should warn: %v", warns)
	}

	built, warns := Build(map[string]any{
		"db": map[string]any{"type": "sqlite", "path": "/var/lib/app/app.db", "quick": true},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("sqlite with path should build: warns=%v", warns)
	}
	sc, ok := unguarded(built[0].Check).(sqliteCheck)
	if !ok || sc.path != "/var/lib/app/app.db" || !sc.quick {
		t.Fatalf("built = %#v", built[0].Check)
	}
}
