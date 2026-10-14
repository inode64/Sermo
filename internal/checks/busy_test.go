package checks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"sermo/internal/conn"
	"sermo/internal/severity"
)

// busyConnResult is what a probe returns for a saturated MySQL server.
func busyConnResult() conn.Result {
	return conn.Result{
		Failure: "server busy: Too many connections (1040)",
		Busy:    true,
		Extra:   map[string]string{conn.ExtraKeyBusyReason: "Too many connections", conn.ExtraKeyErrorCode: "1040"},
	}
}

// A health check reads busy as failing (the server is not serving); a
// condition or verdictless check reads it as unavailable (no reading). Both
// keep Busy and the server's evidence.
func TestConnCheckBusyByReportingMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		condition   bool
		reports     string
		unavailable bool
		observation ObservationState
	}{
		{"health", false, "", false, ObservationFailing},
		{"condition", true, ReportsCondition, true, ObservationUnavailable},
		{"state", false, ReportsState, true, ObservationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := connCheck{
				name: "ping", timeout: time.Second, condition: tc.condition, reports: tc.reports,
				proto: fakeProto{}, cfg: conn.Config{Host: "127.0.0.1", Port: 3306},
				probe: func(context.Context, conn.Config) (conn.Result, error) { return busyConnResult(), nil },
			}
			res := c.Run(t.Context())
			if !res.Busy || res.OK || res.Unavailable != tc.unavailable || res.Observation() != tc.observation {
				t.Fatalf("result = %+v (observation %s)", res, res.Observation())
			}
			if res.Message != "mysql 127.0.0.1:3306: server busy: Too many connections (1040)" {
				t.Fatalf("message = %q", res.Message)
			}
			if res.Data[conn.ExtraKeyErrorCode] != "1040" || res.Data[DataKeyBusyReason] != "Too many connections" {
				t.Fatalf("data = %v", res.Data)
			}
		})
	}
}

// A busy cycle carries no version or counters: it must neither read as a
// version change nor advance a max_increase window.
func TestConnCheckBusyLeavesChangeAndGrowthStateAlone(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ok := conn.Result{Version: "11.8.5", Extra: map[string]string{"keys": "100"}}
	c := connCheck{
		name: "ping", timeout: time.Second, proto: fakeProto{}, cfg: conn.Config{Host: "h", Port: 1},
		probe:           probeSeq(ok, busyConnResult(), ok),
		onVersionChange: true,
		state:           &connState{},
		increases:       []connIncrease{{field: "keys", limit: 0, state: &counterWindow{}}},
		window:          time.Hour,
		clock:           func() time.Time { return now },
	}
	for i, wantBusy := range []bool{false, true, false} {
		res := c.Run(t.Context())
		if res.Busy != wantBusy {
			t.Fatalf("cycle %d busy = %v: %+v", i, res.Busy, res)
		}
		if !wantBusy && !res.OK {
			t.Fatalf("cycle %d after a busy cycle must stay healthy (no version change, no growth): %q", i, res.Message)
		}
		now = now.Add(time.Minute)
	}
}

// A sql check whose server answers it is saturated is busy (and unavailable,
// being condition-style), with the driver's number as evidence.
func TestSQLCheckBusy(t *testing.T) {
	saturated := &mysql.MySQLError{Number: 1040, Message: "Too many connections"}
	for name, c := range map[string]sqlCheck{
		"open": {base: base{name: "conns", timeout: time.Second, condition: true}, engine: "mariadb",
			open: func(context.Context) (*sql.DB, error) { return nil, saturated }},
	} {
		res := c.Run(t.Context())
		if !res.Busy || !res.Unavailable || res.OK || res.Data[conn.ExtraKeyErrorCode] != "1040" ||
			!strings.Contains(res.Message, "server busy: Too many connections (1040)") {
			t.Fatalf("%s: result = %+v", name, res)
		}
	}
	plain := sqlCheck{name: "conns", timeout: time.Second, condition: true, engine: "mariadb",
		open: func(context.Context) (*sql.DB, error) { return nil, errors.New("dial unix: no such file") }}
	if res := plain.Run(t.Context()); res.Busy || !res.Unavailable {
		t.Fatalf("a transport error is not busy: %+v", res)
	}
}

// levels: escalate a matching sql result; a tier the value does not reach
// keeps the declared severity.
func TestSQLCheckLevels(t *testing.T) {
	path := makeSQLMetaDB(t) // rows = 42
	for _, tc := range []struct {
		tier string
		want severity.Level
	}{
		{"40", severity.Error},
		{"50", severity.Warning},
	} {
		built, warns := Build(map[string]any{
			"rows": map[string]any{
				"type": "sql", "engine": "sqlite", "path": path, "severity": "warning",
				"query": "SELECT v FROM meta WHERE k='rows'", "op": ">=", "value": "10",
				"levels": map[string]any{"error": map[string]any{"op": ">=", "value": tc.tier}},
			},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("sql check with levels should build: %v", warns)
		}
		res := built[0].Check.Run(t.Context())
		if !res.OK || res.Severity.Resolved() != tc.want {
			t.Fatalf("tier %s: severity = %q, want %q (%+v)", tc.tier, res.Severity, tc.want, res)
		}
	}
}

// A mysql sql check with a defaults_file needs no user: the option file's
// [client] account (root when it names none) and socket are used, the check's
// own fields first.
func TestSQLCheckDefaultsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "my.cnf")
	if err := os.WriteFile(file, []byte("[client]\nsocket=/run/mysqld/other.sock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := mysqlTarget(conn.Config{}, file)
	if err != nil || target.User != dbQueryDefaultUser || target.Socket != "/run/mysqld/other.sock" {
		t.Fatalf("target = %+v, %v; want root over the file's socket", target, err)
	}
	own, err := mysqlTarget(conn.Config{User: "monitor", Socket: "/run/mysqld/mysqld.sock"}, file)
	if err != nil || own.User != "monitor" || own.Socket != "/run/mysqld/mysqld.sock" {
		t.Fatalf("own fields must win: %+v, %v", own, err)
	}
	built, warns := Build(map[string]any{
		"conns": map[string]any{
			"type": "sql", "engine": "mariadb", "socket": "/run/mysqld/mysqld.sock", "defaults_file": file,
			"query": "SELECT 1", "op": ">=", "value": "80",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("a mysql sql check with a defaults_file and no user must build: %v", warns)
	}
	if _, warns := Build(map[string]any{
		"conns": map[string]any{"type": "sql", "engine": "mariadb", "query": "SELECT 1", "op": ">=", "value": "80"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("a mysql sql check without user or defaults_file must not build")
	}
}

// A replica whose server answers "too many connections" is busy, never an
// unavailable observation a restart rule's failed: would read as true.
func TestReplicationCheckBusy(t *testing.T) {
	c := replicationCheckWith(nil, fmt.Errorf("replication status: %w", &mysql.MySQLError{Number: 1040, Message: "Too many connections"}))
	res := c.Run(t.Context())
	if !res.Busy || res.Unavailable || res.Data[conn.ExtraKeyErrorCode] != "1040" ||
		res.Message != "replication mariadb: server busy: Too many connections (1040)" {
		t.Fatalf("result = %+v", res)
	}
	if res := replicationCheckWith(nil, errors.New("dial unix: no such file")).Run(t.Context()); res.Busy || !res.Unavailable {
		t.Fatalf("a transport error is not busy: %+v", res)
	}
}
