package checks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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

	c := sqliteCheck{base: base{name: "db", timeout: 5 * time.Second}, path: path}
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
	c := sqliteCheck{base: base{name: "db", timeout: 5 * time.Second}, path: path, quick: true}
	if !c.Run(context.Background()).OK {
		t.Fatal("quick_check on a healthy db should pass")
	}
}

func TestSQLiteCheckMissing(t *testing.T) {
	c := sqliteCheck{base: base{name: "db", timeout: time.Second}, path: filepath.Join(t.TempDir(), "nope.db")}
	if c.Run(context.Background()).OK {
		t.Fatal("a missing file must fail")
	}
}

func TestSQLiteCheckNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database, just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := sqliteCheck{base: base{name: "db", timeout: time.Second}, path: path}
	if c.Run(context.Background()).OK {
		t.Fatal("a non-sqlite file must fail")
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
		"db": map[string]any{"type": "sqlite3", "path": "/var/lib/app/app.db", "quick": true},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("sqlite3 with path should build: warns=%v", warns)
	}
	sc, ok := built[0].Check.(sqliteCheck)
	if !ok || sc.path != "/var/lib/app/app.db" || !sc.quick {
		t.Fatalf("built = %#v", built[0].Check)
	}
}
