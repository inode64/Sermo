package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mariadbRow builds a MariaDB-vocabulary status row.
func mariadbRow(conn, host, io, sqlThread, behind, lastSQLErr string) replicationRow {
	row := replicationRow{
		"Master_Host":       host,
		"Slave_IO_Running":  io,
		"Slave_SQL_Running": sqlThread,
	}
	if conn != "" {
		row["Connection_name"] = conn
	}
	if behind != "" {
		row["Seconds_Behind_Master"] = behind
	}
	if lastSQLErr != "" {
		row["Last_SQL_Error"] = lastSQLErr
	}
	return row
}

func replicationCheckWith(rows []replicationRow, err error) replicationCheck {
	return replicationCheck{
		name:   "repl",
		engine: SQLEngineMariaDB,
		sample: func(context.Context) ([]replicationRow, error) { return rows, err },
	}
}

func TestReplicationHealthyBothThreads(t *testing.T) {
	c := replicationCheckWith([]replicationRow{mariadbRow("", "172.31.27.30", "Yes", "Yes", "0", "")}, nil)
	res := c.Run(t.Context())
	if !res.OK {
		t.Fatalf("healthy replication failed: %s", res.Message)
	}
	if res.Data[DataKeyIOStopped] != 0.0 || res.Data[DataKeySQLStopped] != 0.0 {
		t.Fatalf("running threads must publish 0 (band ok): %v", res.Data)
	}
	if res.Data[DataKeyBehindSeconds] != 0.0 {
		t.Fatalf("behind = %v, want 0", res.Data[DataKeyBehindSeconds])
	}
	if res.Data[DataKeySourceHost] != "172.31.27.30" {
		t.Fatalf("source host = %v", res.Data[DataKeySourceHost])
	}
}

func TestReplicationStoppedSQLThreadQuotesServerError(t *testing.T) {
	// MySQL-vocabulary row: the same check must read Replica_/Source_ columns.
	row := replicationRow{
		"Source_Host":         "10.0.0.9",
		"Replica_IO_Running":  "Yes",
		"Replica_SQL_Running": "No",
		"Last_SQL_Error":      "Duplicate entry '7' for key 'PRIMARY'",
	}
	c := replicationCheckWith([]replicationRow{row}, nil)
	res := c.Run(t.Context())
	if res.OK {
		t.Fatal("stopped sql thread must fail the check")
	}
	if !strings.Contains(res.Message, "sql thread stopped") || !strings.Contains(res.Message, "Duplicate entry") {
		t.Fatalf("message must name the thread and quote the server error: %q", res.Message)
	}
	if res.Data[DataKeySQLStopped] != 1.0 {
		t.Fatalf("stopped thread must publish 1 (band failing): %v", res.Data)
	}
}

// The IO thread reports "Connecting" while it cannot reach the source; that is
// not replication, so it must not read as running.
func TestReplicationConnectingIsNotRunning(t *testing.T) {
	c := replicationCheckWith([]replicationRow{mariadbRow("", "h", "Connecting", "Yes", "", "")}, nil)
	if res := c.Run(t.Context()); res.OK {
		t.Fatal("a Connecting IO thread must not pass")
	}
}

func TestReplicationNoRowsFails(t *testing.T) {
	c := replicationCheckWith(nil, nil)
	res := c.Run(t.Context())
	if res.OK || !strings.Contains(res.Message, "no replication configured") {
		t.Fatalf("empty status must fail naming the cause: ok=%v %q", res.OK, res.Message)
	}
}

func TestReplicationConnectionScopeFiltersRows(t *testing.T) {
	rows := []replicationRow{
		mariadbRow("primary", "h1", "Yes", "Yes", "1", ""),
		mariadbRow("backup", "h2", "No", "Yes", "", ""),
	}
	c := replicationCheckWith(rows, nil)
	c.connection = "primary"
	if res := c.Run(t.Context()); !res.OK {
		t.Fatalf("scoped healthy connection failed: %s", res.Message)
	}
	c.connection = "backup"
	if res := c.Run(t.Context()); res.OK {
		t.Fatal("scoped broken connection must fail")
	}
	c.connection = "absent"
	res := c.Run(t.Context())
	if res.OK || !strings.Contains(res.Message, `"absent" not configured`) {
		t.Fatalf("unknown connection must fail naming it: %q", res.Message)
	}
}

// Multi-source with no scope: every connection must replicate and the reported
// lag is the worst one.
func TestReplicationMultiSourceAggregatesWorst(t *testing.T) {
	rows := []replicationRow{
		mariadbRow("a", "h1", "Yes", "Yes", "3", ""),
		mariadbRow("b", "h2", "Yes", "Yes", "42", ""),
	}
	c := replicationCheckWith(rows, nil)
	res := c.Run(t.Context())
	if !res.OK {
		t.Fatalf("healthy multi-source failed: %s", res.Message)
	}
	if res.Data[DataKeyBehindSeconds] != 42.0 {
		t.Fatalf("behind = %v, want worst (42)", res.Data[DataKeyBehindSeconds])
	}
	if res.Data[DataKeyConnections] != "a, b" {
		t.Fatalf("connections = %v", res.Data[DataKeyConnections])
	}
}

func TestReplicationBehindBound(t *testing.T) {
	c := replicationCheckWith([]replicationRow{mariadbRow("", "h", "Yes", "Yes", "120", "")}, nil)
	c.behindBound = &levelPred{field: CheckKeyBehind, op: "<", value: 60}
	res := c.Run(t.Context())
	if res.OK || !strings.Contains(res.Message, "lagging") {
		t.Fatalf("lag beyond the bound must fail: ok=%v %q", res.OK, res.Message)
	}
	c.behindBound.value = 300
	if res := c.Run(t.Context()); !res.OK {
		t.Fatalf("lag within the bound failed: %s", res.Message)
	}
}

func TestBuildReplicationCheckValidation(t *testing.T) {
	if _, warn := buildReplicationCheck(base{}, map[string]any{"user": "root", "engine": "postgres"}); !strings.Contains(warn, "mysql or mariadb") {
		t.Fatalf("bad engine warn = %q", warn)
	}
	if _, warn := buildReplicationCheck(base{}, map[string]any{}); warn != replicationAccountRequired {
		t.Fatalf("missing user warn = %q", warn)
	}
	if check, warn := buildReplicationCheck(base{name: "r"}, map[string]any{"socket": "/run/mysqld/mysqld.sock", "defaults_file": "/root/.my.cnf"}); warn != "" || check == nil {
		t.Fatalf("a defaults_file must stand in for the user: %q", warn)
	}
	if _, warn := buildReplicationCheck(base{}, map[string]any{"user": "root", "behind": map[string]any{"op": "<>", "value": "60"}}); !strings.Contains(warn, "invalid op") {
		t.Fatalf("bad behind op warn = %q", warn)
	}
	if _, warn := buildReplicationCheck(base{}, map[string]any{"user": "root", "behind": map[string]any{"op": "<", "value": "x"}}); !strings.Contains(warn, "must be numeric") {
		t.Fatalf("bad behind value warn = %q", warn)
	}
	check, warn := buildReplicationCheck(base{name: "r"}, map[string]any{"user": "root", "behind": map[string]any{"op": "<", "value": "60"}})
	if warn != "" || check == nil {
		t.Fatalf("valid entry warned: %q", warn)
	}
}

// With a defaults_file and no own socket the check dials the option file's
// socket, the way db_queries and sql do.
func TestReplicationDefaultsFileSuppliesTarget(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "absent.sock")
	file := filepath.Join(dir, "my.cnf")
	if err := os.WriteFile(file, []byte("[client]\nsocket="+socket+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	check, warn := buildReplicationCheck(base{name: "r", timeout: time.Second}, map[string]any{"defaults_file": file})
	if warn != "" {
		t.Fatalf("build warned: %q", warn)
	}
	if res := check.Run(t.Context()); res.OK || !strings.Contains(res.Message, socket) {
		t.Fatalf("must dial the option file's socket: %+v", res)
	}
}

// Without a user or a defaults_file the manual start is refused before any
// connection is opened.
func TestStartReplicationRequiresAccount(t *testing.T) {
	if res := StartReplication(t.Context(), map[string]any{}); res.OK || res.Message != replicationAccountRequired {
		t.Fatalf("start without an account must be refused: %+v", res)
	}
}

// A connection name that cannot be embedded safely in the quoted START SLAVE
// statement is refused before any connection is opened.
func TestStartReplicationRefusesUnsafeConnectionName(t *testing.T) {
	res := StartReplication(t.Context(), map[string]any{"user": "root", "connection": "bad'name"})
	if res.OK || !strings.Contains(res.Message, "not startable") {
		t.Fatalf("unsafe connection name must be refused: %+v", res)
	}
}

// Without `connection` the check covers every MariaDB connection, but plain
// START SLAVE only starts @@default_master_connection; named connections need
// START ALL SLAVES. MySQL channels all start with plain START REPLICA.
func TestReplicationStartStatements(t *testing.T) {
	mariaNamed := []replicationRow{{"Connection_name": ""}, {"Connection_name": "east"}}
	mariaDefault := []replicationRow{{"Connection_name": ""}}
	mysqlChannels := []replicationRow{{"Channel_Name": ""}, {"Channel_Name": "east"}}
	tests := []struct {
		name       string
		connection string
		rows       []replicationRow
		want       []string
	}{
		{name: "mariadb named connections", rows: mariaNamed, want: []string{"START ALL REPLICAS", "START ALL SLAVES"}},
		{name: "mariadb default connection", rows: mariaDefault, want: []string{"START REPLICA", "START SLAVE"}},
		{name: "mysql channels", rows: mysqlChannels, want: []string{"START REPLICA", "START SLAVE"}},
		{name: "scoped connection", connection: "east", rows: mariaNamed, want: []string{"START SLAVE 'east'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replicationStartStatements(tt.connection, tt.rows); !slices.Equal(got, tt.want) {
				t.Fatalf("statements = %q, want %q", got, tt.want)
			}
		})
	}
}

// positionedRow is a healthy MariaDB row whose IO thread has read up to
// readFile:readPos and whose SQL thread has applied up to execFile:execPos.
func positionedRow(host, readFile string, readPos int, execFile string, execPos int) replicationRow {
	row := mariadbRow("", host, "Yes", "Yes", "0", "")
	row["Master_Port"] = "3306"
	row["Master_Log_File"] = readFile
	row["Read_Master_Log_Pos"] = strconv.Itoa(readPos)
	row["Relay_Master_Log_File"] = execFile
	row["Exec_Master_Log_Pos"] = strconv.Itoa(execPos)
	return row
}

// sourceLogs is a fixed source binlog listing that counts how often it is asked.
func sourceLogs(calls *int, logs []binlogFile, err error) binlogSampler {
	return func(context.Context, string, int) ([]binlogFile, error) {
		*calls++
		return logs, err
	}
}

var testSourceLogs = []binlogFile{
	{name: "mariadb-bin.000010", size: 1000},
	{name: "mariadb-bin.000011", size: 500},
	{name: "mariadb-bin.000012", size: 300},
}

func TestBinlogDistance(t *testing.T) {
	at := func(file string, offset int64) binlogPosition { return binlogPosition{file: file, offset: offset} }
	tests := []struct {
		name     string
		from, to binlogPosition
		want     int64
		wantErr  string
	}{
		{name: "same file", from: at("mariadb-bin.000012", 100), to: at("mariadb-bin.000012", 300), want: 200},
		{name: "same file never negative", from: at("mariadb-bin.000012", 300), to: at("mariadb-bin.000012", 100), want: 0},
		{name: "across files", from: at("mariadb-bin.000010", 900), to: at("mariadb-bin.000012", 250), want: 100 + 500 + 250},
		{name: "purged on the source", from: at("mariadb-bin.000009", 4), to: at("mariadb-bin.000012", 300), wantErr: "no longer lists mariadb-bin.000009"},
		{name: "unknown target", from: at("mariadb-bin.000010", 4), to: at("mariadb-bin.000099", 4), wantErr: "does not list mariadb-bin.000099"},
		{name: "out of order", from: at("mariadb-bin.000012", 4), to: at("mariadb-bin.000010", 4), wantErr: "lists mariadb-bin.000012 after mariadb-bin.000010"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := binlogDistance(testSourceLogs, tt.from, tt.to)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("distance = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

// The backlog splits into what the IO thread has not read from the source yet
// and what the SQL thread has not applied; both count source binlog bytes.
func TestReplicationPendingMeasuresSourceBacklog(t *testing.T) {
	calls := 0
	c := replicationCheckWith([]replicationRow{
		positionedRow("fr1.vrack", "mariadb-bin.000011", 200, "mariadb-bin.000010", 950),
	}, nil)
	c.binlogs = sourceLogs(&calls, testSourceLogs, nil)
	res := c.Run(t.Context())
	if !res.OK {
		t.Fatalf("an unbounded backlog must not fail: %s", res.Message)
	}
	if res.Data[DataKeyUnreceivedBytes] != int64(300+300) || res.Data[DataKeyUnappliedBytes] != int64(50+200) {
		t.Fatalf("unreceived/unapplied = %v/%v, want 600/250", res.Data[DataKeyUnreceivedBytes], res.Data[DataKeyUnappliedBytes])
	}
	if res.Data[DataKeyPendingBytes] != int64(850) {
		t.Fatalf("pending = %v, want 850", res.Data[DataKeyPendingBytes])
	}
	if !strings.Contains(res.Message, "850 B pending") {
		t.Fatalf("message must state the backlog: %q", res.Message)
	}
}

func TestReplicationPendingBound(t *testing.T) {
	calls := 0
	c := replicationCheckWith([]replicationRow{
		positionedRow("fr1.vrack", "mariadb-bin.000010", 0, "mariadb-bin.000010", 0),
	}, nil)
	c.binlogs = sourceLogs(&calls, testSourceLogs, nil)
	c.pendingBound = &levelPred{field: CheckKeyPendingBytes, op: "<", value: 1024}
	res := c.Run(t.Context())
	if res.OK || !strings.Contains(res.Message, "backlog: 1.76 KiB of source binlog pending, 1.76 KiB not received (limit < 1 KiB") {
		t.Fatalf("a backlog beyond the bound must fail naming it: ok=%v %q", res.OK, res.Message)
	}
	c.pendingBound.value = 4096
	if res := c.Run(t.Context()); !res.OK {
		t.Fatalf("a backlog within the bound failed: %s", res.Message)
	}
}

// A source that cannot be asked leaves the backlog unknown: informative without
// a bound, failing with one, since an unverifiable bound does not pass.
func TestReplicationPendingUnknown(t *testing.T) {
	calls := 0
	c := replicationCheckWith([]replicationRow{
		positionedRow("fr1.vrack", "mariadb-bin.000011", 0, "mariadb-bin.000011", 0),
	}, nil)
	c.binlogs = sourceLogs(&calls, nil, errors.New("access denied"))
	res := c.Run(t.Context())
	if !res.OK || !strings.Contains(res.Message, "pending unknown: source fr1.vrack: access denied") {
		t.Fatalf("without a bound the check passes and says why: ok=%v %q", res.OK, res.Message)
	}
	if _, ok := res.Data[DataKeyPendingBytes]; ok || res.Data[DataKeyPendingError] == nil {
		t.Fatalf("an unknown backlog must publish the error, not a number: %v", res.Data)
	}
	c.pendingBound = &levelPred{field: CheckKeyPendingBytes, op: "<", value: 1024}
	if res := c.Run(t.Context()); res.OK || !strings.Contains(res.Message, "pending unknown") {
		t.Fatalf("a bound that cannot be verified must fail: ok=%v %q", res.OK, res.Message)
	}

	c.binlogs = sourceLogs(&calls, testSourceLogs, nil)
	c.sample = func(context.Context) ([]replicationRow, error) {
		return []replicationRow{positionedRow("fr1.vrack", "mariadb-bin.000009", 4, "mariadb-bin.000009", 4)}, nil
	}
	if res := c.Run(t.Context()); res.OK || !strings.Contains(res.Message, "source fr1.vrack: no longer lists mariadb-bin.000009") {
		t.Fatalf("a purged read position must fail the bound naming the file: %q", res.Message)
	}
}

// A stopped IO thread still reports how much the replica has to catch up.
func TestReplicationStoppedReportsPending(t *testing.T) {
	calls := 0
	row := positionedRow("fr1.vrack", "mariadb-bin.000011", 500, "mariadb-bin.000011", 500)
	row["Slave_IO_Running"] = "No"
	c := replicationCheckWith([]replicationRow{row}, nil)
	c.binlogs = sourceLogs(&calls, testSourceLogs, nil)
	res := c.Run(t.Context())
	if res.OK || !strings.Contains(res.Message, "io thread stopped, 300 B pending") {
		t.Fatalf("a stopped replica must state its backlog: %q", res.Message)
	}
}

// Multi-source: each source is asked once and the worst backlog is published.
func TestReplicationPendingWorstSourceOnce(t *testing.T) {
	calls := 0
	a := positionedRow("h1", "mariadb-bin.000012", 300, "mariadb-bin.000012", 300)
	b := positionedRow("h1", "mariadb-bin.000012", 100, "mariadb-bin.000012", 100)
	a["Connection_name"], b["Connection_name"] = "a", "b"
	c := replicationCheckWith([]replicationRow{a, b}, nil)
	c.binlogs = sourceLogs(&calls, testSourceLogs, nil)
	res := c.Run(t.Context())
	if calls != 1 {
		t.Fatalf("source asked %d times, want once", calls)
	}
	if res.Data[DataKeyPendingBytes] != int64(200) {
		t.Fatalf("pending = %v, want the worst connection (200)", res.Data[DataKeyPendingBytes])
	}
}

func TestBuildReplicationCheckPending(t *testing.T) {
	if _, warn := buildReplicationCheck(base{}, map[string]any{"user": "root", "pending_bytes": map[string]any{"op": "<", "value": "1G"}}); warn != replicationPendingNeedsSource {
		t.Fatalf("pending without a source warn = %q", warn)
	}
	if _, warn := buildReplicationCheck(base{}, map[string]any{
		"user": "root", "source_defaults_file": "/etc/source.cnf", "pending_bytes": map[string]any{"op": "<", "value": "lots"},
	}); !strings.Contains(warn, "size suffix") {
		t.Fatalf("bad pending value warn = %q", warn)
	}
	check, warn := buildReplicationCheck(base{name: "r"}, map[string]any{
		"user": "root", "source_defaults_file": "/etc/source.cnf", "pending_bytes": map[string]any{"op": "<", "value": "1G"},
	})
	rc, ok := check.(replicationCheck)
	if warn != "" || !ok || rc.binlogs == nil || rc.pendingBound == nil || rc.pendingBound.value != 1<<30 {
		t.Fatalf("valid pending entry: warn=%q check=%+v", warn, check)
	}
}

func TestParseBinaryLogs(t *testing.T) {
	logs, err := parseBinaryLogs([]replicationRow{
		{"Log_name": "mariadb-bin.000011", "File_size": "500"},
		{"Log_name": "mariadb-bin.000012", "File_size": "300", "Encrypted": "No"},
	})
	if err != nil || !slices.Equal(logs, testSourceLogs[1:]) {
		t.Fatalf("logs = %+v, %v", logs, err)
	}
	if _, err := parseBinaryLogs([]replicationRow{{"Log_name": "mariadb-bin.000011"}}); err == nil {
		t.Fatal("a row without File_size must be refused")
	}
}

func TestReplicationBandsAndGraphDeclared(t *testing.T) {
	bands := DeclaredBandMetrics(CheckTypeReplication, map[string]any{})
	keys := map[string]bool{}
	for _, b := range bands {
		keys[b.Key] = true
		if !b.OKFor(0) || b.OKFor(1) {
			t.Fatalf("band %s must be ok at 0 and failing at 1", b.Key)
		}
	}
	if !keys[DataKeyIOStopped] || !keys[DataKeySQLStopped] {
		t.Fatalf("replication bands = %v", keys)
	}
	graphKeys := func(entry map[string]any) []string {
		var out []string
		for _, m := range ResolvedGraphMetrics(CheckTypeReplication, "", entry) {
			out = append(out, m.Key)
		}
		return out
	}
	if got := graphKeys(map[string]any{}); !slices.Equal(got, []string{DataKeyBehindSeconds}) {
		t.Fatalf("replication graphs without a source = %v", got)
	}
	withSource := map[string]any{CheckKeySourceDefaultsFile: "/etc/source.cnf"}
	if got := graphKeys(withSource); !slices.Equal(got, []string{DataKeyBehindSeconds, DataKeyPendingBytes}) {
		t.Fatalf("replication graphs with a source = %v", got)
	}
}
