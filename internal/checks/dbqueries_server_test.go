package checks

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakeMySQLServer answers the db_queries sampler's statements and records
// them: a MariaDB without the TID column, which can be told to fail once.
type fakeMySQLServer struct {
	mu       sync.Mutex
	queries  []string
	noTID    bool
	failNext bool
}

func (s *fakeMySQLServer) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.queries {
		if strings.HasPrefix(strings.TrimSpace(q), prefix) {
			n++
		}
	}
	return n
}

// fakeTIDColumn is how the MariaDB thread column appears in the select list
// (a bare "TID" also matches 'Binlog Dump GTID').
const fakeTIDColumn = "TID, MEMORY_USED"

var fakeMySQLServers sync.Map

type fakeMySQLDriver struct{}

func (fakeMySQLDriver) Open(name string) (driver.Conn, error) {
	server, _ := fakeMySQLServers.Load(name)
	return fakeMySQLConn{server.(*fakeMySQLServer)}, nil
}

type fakeMySQLConn struct{ server *fakeMySQLServer }

func (fakeMySQLConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (fakeMySQLConn) Close() error                        { return nil }
func (fakeMySQLConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c fakeMySQLConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	s := c.server
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	if s.failNext {
		s.failNext = false
		return nil, errors.New("server gone away")
	}
	if strings.HasPrefix(query, "SELECT VERSION()") {
		return &fakeRows{columns: []string{"VERSION()"}, rows: [][]driver.Value{{"11.8.5-MariaDB-log"}}}, nil
	}
	if s.noTID && strings.Contains(query, fakeTIDColumn) {
		return nil, errors.New("Unknown column 'TID'")
	}
	return &fakeRows{columns: make([]string, 14)}, nil
}

type fakeRows struct {
	columns []string
	rows    [][]driver.Value
}

func (r *fakeRows) Columns() []string { return r.columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

var registerFakeMySQL sync.Once

func openFakeMySQL(t *testing.T, server *fakeMySQLServer) *sql.DB {
	t.Helper()
	registerFakeMySQL.Do(func() { sql.Register("fakemysql", fakeMySQLDriver{}) })
	fakeMySQLServers.Store(t.Name(), server)
	db, err := sql.Open("fakemysql", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The server flavour and the thread columns are detected once, and again only
// after a failed sample.
func TestMySQLSamplerRemembersWhatTheServerSupports(t *testing.T) {
	server := &fakeMySQLServer{noTID: true}
	db := openFakeMySQL(t, server)
	info := &mysqlServerInfo{}
	for range 3 {
		if _, err := sampleMySQLQueries(context.Background(), db, 100, info); err != nil {
			t.Fatalf("sample: %v", err)
		}
	}
	if got := server.count("SELECT VERSION()"); got != 1 {
		t.Fatalf("version queried %d times, want once", got)
	}
	failing := 0
	for _, q := range server.queries {
		if strings.Contains(q, fakeTIDColumn) {
			failing++
		}
	}
	if failing != 1 {
		t.Fatalf("the unsupported thread column was tried %d times, want once", failing)
	}

	server.mu.Lock()
	server.failNext = true
	server.mu.Unlock()
	if _, err := sampleMySQLQueries(context.Background(), db, 100, info); err == nil {
		t.Fatal("want the failed sample's error")
	}
	if _, err := sampleMySQLQueries(context.Background(), db, 100, info); err != nil {
		t.Fatalf("sample after the error: %v", err)
	}
	if got := server.count("SELECT VERSION()"); got != 2 {
		t.Fatalf("a failed sample must re-detect the server: version queried %d times", got)
	}
}
