package checks

import (
	"context"
	"strings"
	"testing"
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
		base:   base{name: "repl"},
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
	c.behindOp, c.behindValue, c.hasBehind = "<", 60, true
	res := c.Run(t.Context())
	if res.OK || !strings.Contains(res.Message, "lagging") {
		t.Fatalf("lag beyond the bound must fail: ok=%v %q", res.OK, res.Message)
	}
	c.behindValue = 300
	if res := c.Run(t.Context()); !res.OK {
		t.Fatalf("lag within the bound failed: %s", res.Message)
	}
}

func TestBuildReplicationCheckValidation(t *testing.T) {
	if _, warn := buildReplicationCheck(base{}, map[string]any{"user": "root", "engine": "postgres"}); !strings.Contains(warn, "mysql or mariadb") {
		t.Fatalf("bad engine warn = %q", warn)
	}
	if _, warn := buildReplicationCheck(base{}, map[string]any{}); !strings.Contains(warn, "requires a user") {
		t.Fatalf("missing user warn = %q", warn)
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

// A connection name that cannot be embedded safely in the quoted START SLAVE
// statement is refused before any connection is opened.
func TestStartReplicationRefusesUnsafeConnectionName(t *testing.T) {
	res := StartReplication(t.Context(), map[string]any{"user": "root", "connection": "bad'name"})
	if res.OK || !strings.Contains(res.Message, "not startable") {
		t.Fatalf("unsafe connection name must be refused: %+v", res)
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
	graphs := GraphMetrics(CheckTypeReplication)
	if len(graphs) != 1 || graphs[0].Key != DataKeyBehindSeconds {
		t.Fatalf("replication graphs = %+v", graphs)
	}
}
