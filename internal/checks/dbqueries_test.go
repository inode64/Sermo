package checks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/conn"
)

func TestParseDBQueryConfig(t *testing.T) {
	cfg, err := ParseDBQueryConfig(map[string]any{
		"engine": "mariadb", "socket": "/run/mysqld/mysqld.sock", "defaults_file": "/root/.my.cnf",
		"min_duration": "5m", "exclude_users": []any{"backup"}, "max_query_length": 200,
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Engine != SQLEngineMySQL || cfg.Conn.Socket != "/run/mysqld/mysqld.sock" || cfg.MinDuration != 5*time.Minute ||
		cfg.MaxLength != 200 || cfg.MaxRows != dbQueryDefaultMaxRows || cfg.Selector.ExcludeUsers[0] != "backup" {
		t.Fatalf("cfg = %+v", cfg)
	}
	pg, err := ParseDBQueryConfig(map[string]any{"engine": "postgres", "user": "postgres", "min_duration": "1m"})
	if err != nil || len(pg.States) != 1 || pg.States[0] != pgStateActive {
		t.Fatalf("postgres default states: %+v, %v", pg, err)
	}

	for name, entry := range map[string]map[string]any{
		"no engine":          {"min_duration": "5m"},
		"sqlite":             {"engine": "sqlite", "min_duration": "5m"},
		"no min_duration":    {"engine": "mysql"},
		"zero length":        {"engine": "mysql", "min_duration": "5m", "max_query_length": 0},
		"mysql states":       {"engine": "mysql", "min_duration": "5m", "states": []any{"active"}},
		"postgres socket":    {"engine": "postgres", "user": "p", "min_duration": "5m", "socket": "/x"},
		"postgres w/o user":  {"engine": "postgres", "min_duration": "5m"},
		"negative max_rows":  {"engine": "mysql", "min_duration": "5m", "max_rows": -1},
		"bad min_duration":   {"engine": "mysql", "min_duration": "soon"},
		"defaults_file + pg": {"engine": "postgres", "user": "p", "min_duration": "5m", "defaults_file": "/root/.my.cnf"},
	} {
		if _, err := ParseDBQueryConfig(entry); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReadMySQLOptionFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my.cnf")
	body := "# root client\n[client]\nuser = monitor\npassword=\"s3 cret\"\nsocket=/run/mysqld/mysqld.sock\n" +
		"[mysqladmin]\npassword=other\n[mysql]\nport=3307 # comment\n!includedir /etc/mysql/conf.d\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := readMySQLOptionFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if opts["user"] != "monitor" || opts["password"] != "s3 cret" || opts["socket"] != "/run/mysqld/mysqld.sock" || opts["port"] != "3307" {
		t.Fatalf("opts = %v", opts)
	}

	// Explicit fields win; the file fills the rest.
	cfg, err := mysqlConnWithDefaults(conn.Config{User: "explicit"}, path)
	if err != nil || cfg.User != "explicit" || cfg.Password != "s3 cret" || cfg.Socket != "/run/mysqld/mysqld.sock" || cfg.Port != 3307 {
		t.Fatalf("with defaults = %+v, %v", cfg, err)
	}
	// A missing file is not an error, and the user defaults to root.
	cfg, err = mysqlConnWithDefaults(conn.Config{}, filepath.Join(t.TempDir(), "absent.cnf"))
	if err != nil || cfg.User != "root" || cfg.Password != "" {
		t.Fatalf("missing file = %+v, %v", cfg, err)
	}
}

func TestRedactSQL(t *testing.T) {
	for _, tt := range []struct{ in, secret string }{
		{"CREATE USER 'a'@'%' IDENTIFIED BY 'hunter2'", "hunter2"},
		{"ALTER USER a IDENTIFIED WITH mysql_native_password BY \"hunter2\"", "hunter2"},
		{"SELECT PASSWORD('hunter2')", "hunter2"},
		{"SET PASSWORD FOR 'a'@'%' = 'hunter2'", "hunter2"},
		{"CHANGE MASTER TO MASTER_HOST='h', MASTER_PASSWORD='hunter2'", "hunter2"},
		{"ALTER ROLE app WITH ENCRYPTED PASSWORD 'hunter2'", "hunter2"},
		{"ALTER USER a IDENTIFIED BY 'hunter2", "hunter2"}, // truncated mid-literal
		{`CREATE USER a IDENTIFIED BY 'it\'s hunter2'`, "hunter2"},
		{"START REPLICA USER='r' PASSWORD='hunter2'", "hunter2"},
		{"START SLAVE USER = 'r' PASSWORD = 'hunter2'", "hunter2"},
		{"ALTER USER u IDENTIFIED BY 'n3w' REPLACE 'hunter2'", "hunter2"},
		{"ALTER ROLE app PASSWORD 'ab''hunter2'", "hunter2"},
	} {
		got := redactSQL(tt.in)
		if strings.Contains(got, tt.secret) || !strings.Contains(got, sqlRedacted) {
			t.Errorf("redactSQL(%q) = %q", tt.in, got)
		}
	}
	if got := redactSQL("SELECT password FROM users WHERE id = 1"); got != "SELECT password FROM users WHERE id = 1" {
		t.Errorf("a column named password must survive: %q", got)
	}
}

func TestDBQueryTextRedactsBeforeTruncating(t *testing.T) {
	text, truncated := dbQueryText("ALTER USER a IDENTIFIED BY 'hunter2'   -- and\n more", 30)
	if !truncated || strings.Contains(text, "hunter") || strings.Contains(text, "\n") {
		t.Fatalf("text = %q truncated = %v", text, truncated)
	}
	if text, truncated := dbQueryText("SELECT  1", 30); truncated || text != "SELECT 1" {
		t.Fatalf("short text = %q, %v", text, truncated)
	}
}

func TestMySQLProcessRowIdentity(t *testing.T) {
	row := mysqlProcessRow{id: 36762377, elapsedMS: 240831466, startedMS: 1_700_000_000_000,
		queryID: sql.NullInt64{Int64: 279932773, Valid: true}, length: sql.NullInt64{Int64: 1500, Valid: true},
		crc: sql.NullInt64{Int64: 0xdeadbeef, Valid: true}, user: "tac_prod", host: "localhost:45732", db: "tac_prod",
		command: "Query", state: "Sending data", info: sql.NullString{String: "SELECT ga.geoare_id FROM geographic_area_polygon", Valid: true}}
	maria := row.query(true, 1000)
	if maria.Engine != SQLEngineMariaDB || maria.Identity != "36762377:279932773" || maria.ElapsedSeconds != 240831 || !maria.Truncated {
		t.Fatalf("mariadb row = %+v", maria)
	}
	mysql := row.query(false, 1000)
	if mysql.Engine != SQLEngineMySQL || mysql.Identity != "36762377:deadbeef:1500:1700000000000" || mysql.ItemKey() != "36762377:deadbeef:1500" {
		t.Fatalf("mysql row = %+v", mysql)
	}
	// The whole-second counter jitters the derived start: a second later is the
	// same statement, five seconds later a new one on the same connection.
	later := mysql
	later.StartedUnixMS += 1000
	if !identityMatches(later, mysql.Identity) {
		t.Fatal("a 1s start jitter must match")
	}
	later.StartedUnixMS += 4000
	if identityMatches(later, mysql.Identity) || mysql.SameStatement(later) {
		t.Fatal("a 5s start shift is another statement")
	}
}

func TestMySQLProcesslistQueryExcludesServerThreads(t *testing.T) {
	q := mysqlProcesslistQuery(true, true)
	for _, want := range []string{"CONNECTION_ID()", "'Sleep'", "'Binlog Dump'", "'Slave_SQL'", "'system user'", "'event_scheduler'", "TIME_MS", "QUERY_ID", "INFO IS NOT NULL"} {
		if !strings.Contains(q, want) {
			t.Errorf("mariadb processlist query lacks %s:\n%s", want, q)
		}
	}
	if !strings.Contains(q, "TID") || !strings.Contains(q, "MEMORY_USED") {
		t.Errorf("mariadb query lacks the thread and memory columns:\n%s", q)
	}
	if q := mysqlProcesslistQuery(false, true); strings.Contains(q, "TIME_MS") || strings.Contains(q, "QUERY_ID") || !strings.Contains(q, "performance_schema.threads") {
		t.Errorf("mysql query uses MariaDB-only columns or lacks the thread source:\n%s", q)
	}
	if q := mysqlProcesslistQuery(false, false); strings.Contains(q, "performance_schema") {
		t.Errorf("the fallback must not read performance_schema:\n%s", q)
	}
}

func TestVerifyDBQueryTarget(t *testing.T) {
	q := DBQuery{Engine: SQLEngineMariaDB, ID: 7, QueryID: 70, User: "report", Database: "app", Command: "Query", ElapsedSeconds: 3600, Identity: "7:70"}
	current := []DBQuery{q}
	if got, err := verifyDBQueryTarget(current, DBQueryKill{ID: 7, Identity: "7:70", Mode: "query"}); err != nil || got.QueryID != 70 {
		t.Fatalf("listed statement: %+v, %v", got, err)
	}
	if _, err := verifyDBQueryTarget(current, DBQueryKill{ID: 7, Identity: "7:71"}); !errors.Is(err, ErrDBQueryChanged) {
		t.Fatalf("a later statement on the connection must be refused: %v", err)
	}
	if _, err := verifyDBQueryTarget(current, DBQueryKill{ID: 8, Identity: "8:80"}); !errors.Is(err, ErrDBQueryChanged) {
		t.Fatalf("a finished statement must be refused: %v", err)
	}
	killed := q
	killed.Command = dbQueryCommandKilled
	if _, err := verifyDBQueryTarget([]DBQuery{killed}, DBQueryKill{ID: 7, Identity: "7:70"}); err == nil {
		t.Fatal("a statement already being killed must be refused")
	}
	require := &DBQueryKillRequirement{After: 2 * time.Hour, Selector: DBQuerySelector{Users: []string{"report"}}}
	if _, err := verifyDBQueryTarget(current, DBQueryKill{ID: 7, Identity: "7:70", Require: require}); err == nil {
		t.Fatal("the automatic kill must re-check the duration")
	}
	require.After, require.Selector.Users = time.Minute, []string{"other"}
	if _, err := verifyDBQueryTarget(current, DBQueryKill{ID: 7, Identity: "7:70", Require: require}); err == nil {
		t.Fatal("the automatic kill must re-check the selector")
	}
}

func TestDBQuerySelector(t *testing.T) {
	q := DBQuery{User: "tac_prod", Database: "tac_prod"}
	for _, tt := range []struct {
		sel  DBQuerySelector
		want bool
	}{
		{DBQuerySelector{}, true},
		{DBQuerySelector{Users: []string{"tac_prod"}}, true},
		{DBQuerySelector{Users: []string{"report"}}, false},
		{DBQuerySelector{ExcludeUsers: []string{"tac_prod"}}, false},
		{DBQuerySelector{Databases: []string{"other"}}, false},
		{DBQuerySelector{ExcludeDatabases: []string{"tac_prod"}}, false},
	} {
		if got := tt.sel.Matches(q); got != tt.want {
			t.Errorf("%+v.Matches = %v, want %v", tt.sel, got, tt.want)
		}
	}
}

func TestDBQueriesFromPersistedData(t *testing.T) {
	in := []DBQuery{{Engine: SQLEnginePostgres, ID: 4711, User: "app", ElapsedSeconds: 600, Query: "SELECT pg_sleep(900)", Identity: "4711:1:2", BackendStartUS: 1,
		OSThreadID: 4711, MemoryBytes: 1 << 20, MemoryReady: true, CPU: 12.5, CPUReady: true}}
	raw, err := json.Marshal(map[string]any{DataKeyDBQueries: in})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := DBQueriesFromData(decoded)
	if len(got) != 1 || got[0] != in[0] {
		t.Fatalf("round trip = %+v", got)
	}
	if got := DBQueriesFromData(map[string]any{DataKeyDBQueries: in}); len(got) != 1 {
		t.Fatalf("in-memory = %+v", got)
	}
}

func TestParseDBQueryKill(t *testing.T) {
	spec, err := ParseDBQueryKill(map[string]any{"after": "30m", "users": []any{"report"}}, 5*time.Minute)
	if err != nil || spec.After != 30*time.Minute || spec.Mode != DBQueryKillModeQuery || spec.Selector.Users[0] != "report" {
		t.Fatalf("spec = %+v, %v", spec, err)
	}
	for name, raw := range map[string]any{
		"not a mapping":   "30m",
		"no after":        map[string]any{"users": []any{"r"}},
		"after too short": map[string]any{"after": "1m", "users": []any{"r"}},
		"bad mode":        map[string]any{"after": "30m", "mode": "nuke", "users": []any{"r"}},
		"no selector":     map[string]any{"after": "30m"},
	} {
		if _, err := ParseDBQueryKill(raw, 5*time.Minute); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestDBQueryConfigLocal(t *testing.T) {
	for host, want := range map[string]bool{"": true, "localhost": true, "127.0.0.1": true, "::1": true, "127.0.0.2": true, "10.0.0.5": false, "db.example": false} {
		if got := (DBQueryConfig{Conn: conn.Config{Host: host}}).Local(); got != want {
			t.Errorf("host %q: Local = %v, want %v", host, got, want)
		}
	}
	if !(DBQueryConfig{Conn: conn.Config{Host: "10.0.0.5", Socket: "/run/mysqld/mysqld.sock"}}).Local() {
		t.Error("a Unix socket is always local")
	}
}

func TestDBQueryStoppingIsNotKillable(t *testing.T) {
	q := DBQuery{Identity: "7:70", Command: "Query"}
	if q.Stopping() || !q.Killable() {
		t.Fatalf("a running statement: %+v", q)
	}
	q.Command = dbQueryCommandKilled
	if !q.Stopping() || q.Killable() {
		t.Fatalf("a statement being stopped: %+v", q)
	}
}

func TestWatchOnlyTypesAreHealthAndUnknownToChecks(t *testing.T) {
	for typ := range watchOnlyTypes {
		if !IsHealthType(typ) {
			t.Errorf("%s must fire on failure", typ)
		}
		if _, ok := TypeInfoFor(typ); ok {
			t.Errorf("%s must not be a single-shot check type", typ)
		}
	}
	if _, ok := WatchOnlyTypeInfo(CheckTypeDBQueries); !ok {
		t.Fatal("db_queries is a watch-only type")
	}
}

// The option file's socket, host and port apply when the check leaves them
// unset: the protocol defaults come after it, not before.
func TestDBQueryTargetHonoursDefaultsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my.cnf")
	if err := os.WriteFile(path, []byte("[client]\nsocket=/var/lib/mysql/mysql.sock\npassword=pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseDBQueryConfig(map[string]any{"engine": "mariadb", "defaults_file": path, "min_duration": "5m"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := cfg.target()
	if err != nil || target.Socket != "/var/lib/mysql/mysql.sock" || target.Password != "pw" || target.User != "root" {
		t.Fatalf("target = %+v, %v", target, err)
	}
	if err := os.WriteFile(path, []byte("[client]\nhost=10.0.0.5\nport=3307\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, _ = cfg.target()
	if target.Host != "10.0.0.5" || target.Port != 3307 || target.Socket != "" || cfg.Local() {
		t.Fatalf("a remote option-file host: %+v local=%v", target, cfg.Local())
	}
	// An explicit socket still wins over the file.
	cfg, _ = ParseDBQueryConfig(map[string]any{"engine": "mariadb", "socket": "/run/mysqld/mysqld.sock", "defaults_file": path, "min_duration": "5m"})
	if target, _ := cfg.target(); target.Socket != "/run/mysqld/mysqld.sock" || !cfg.Local() {
		t.Fatalf("explicit socket = %+v", target)
	}
}

// Readers get a fresh slice: the published one is shared.
func TestDBQueriesFromDataDoesNotModifyThePublishedSlice(t *testing.T) {
	published := []DBQuery{{Engine: "", ID: 0}, {Engine: SQLEngineMariaDB, ID: 7}}
	got := DBQueriesFromData(map[string]any{DataKeyDBQueries: published})
	if len(got) != 1 || got[0].ID != 7 || published[0].ID != 0 || published[1].ID != 7 {
		t.Fatalf("got = %+v published = %+v", got, published)
	}
}

func TestPostgresQueryModeRefusesAnIdleTransaction(t *testing.T) {
	err := killPostgresQuery(context.Background(), nil, DBQuery{ID: 9, Command: "idle in transaction"}, DBQueryKillModeQuery)
	if err == nil || !strings.Contains(err.Error(), "kill its connection") {
		t.Fatalf("err = %v", err)
	}
}
