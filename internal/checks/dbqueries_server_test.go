package checks

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMySQLServer answers the db_queries sampler's statements and records
// them: a MariaDB without the TID column, which can be told to fail once.
type fakeMySQLServer struct {
	mu       sync.Mutex
	queries  []string
	noTID    bool
	failNext bool
	query    func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
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

func (fakeMySQLConn) CheckNamedValue(v *driver.NamedValue) error {
	if _, ok := v.Value.([]string); ok {
		return nil // PostgreSQL's states parameter.
	}
	return driver.ErrSkip
}

func (c fakeMySQLConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.server
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	if s.query != nil {
		return s.query(ctx, query, args)
	}
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
	t.Cleanup(func() { fakeMySQLServers.Delete(t.Name()) })
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
		if _, err := sampleMySQLQueries(context.Background(), db, 100, info, 0); err != nil {
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
	if _, err := sampleMySQLQueries(context.Background(), db, 100, info, 0); err == nil {
		t.Fatal("want the failed sample's error")
	}
	if _, err := sampleMySQLQueries(context.Background(), db, 100, info, 0); err != nil {
		t.Fatalf("sample after the error: %v", err)
	}
	if got := server.count("SELECT VERSION()"); got != 2 {
		t.Fatalf("a failed sample must re-detect the server: version queried %d times", got)
	}
}

func TestDBQuerySampleRestrictsTheTargetBeforeTheLimit(t *testing.T) {
	for _, tt := range []struct {
		name, engine   string
		mariadb, noTID bool
		row            []driver.Value
	}{
		{name: "mysql", engine: SQLEngineMySQL,
			row: []driver.Value{int64(7), "app", "localhost", "app", "Query", "", int64(400000), int64(1000), nil, "SELECT 1", int64(8), int64(42), nil, nil}},
		{name: "mariadb without TID", engine: SQLEngineMySQL, mariadb: true, noTID: true,
			row: []driver.Value{int64(7), "app", "localhost", "app", "Query", "", int64(400000), int64(0), int64(70), "SELECT 1", int64(8), nil, nil, nil}},
		{name: "mariadb without QUERY_ID", engine: SQLEngineMySQL, mariadb: true,
			row: []driver.Value{int64(7), "app", "localhost", "app", "Query", "", int64(400000), int64(1000), nil, "SELECT 1", int64(8), int64(42), nil, nil}},
		{name: "postgres", engine: SQLEnginePostgres,
			row: []driver.Value{int64(7), "app", "app", "localhost", "active", "", int64(1000000), int64(2000000), int64(400000), "SELECT 1", int64(8)}},
	} {
		for _, id := range []int64{0, 7} {
			t.Run(fmt.Sprintf("%s/id=%d", tt.name, id), func(t *testing.T) {
				cfg := DBQueryConfig{Engine: tt.engine, MaxLength: 100, States: []string{pgStateActive},
					server: &mysqlServerInfo{known: true, mariadb: tt.mariadb, threadSource: true}}
				calls := 0
				server := &fakeMySQLServer{query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
					calls++
					wantArgs := []any{int64(101), id, id}
					predicate := "AND (? = 0 OR p.ID = ?)"
					if tt.engine == SQLEnginePostgres {
						wantArgs = []any{cfg.States, int64(101), id}
						predicate = "AND ($3::bigint = 0 OR pid = $3)"
						for _, filter := range []string{"backend_type = 'client backend'", "pid <> pg_backend_pid()", "query_start IS NOT NULL", "state = ANY($1)"} {
							if !strings.Contains(query, filter) {
								t.Errorf("target read lost filter %q: %s", filter, query)
							}
						}
					}
					var gotArgs []any
					for _, arg := range args {
						gotArgs = append(gotArgs, arg.Value)
					}
					if !reflect.DeepEqual(gotArgs, wantArgs) {
						t.Errorf("query args = %v, want %v", gotArgs, wantArgs)
					}
					if at := strings.Index(query, predicate); at < 0 || at > strings.Index(query, "ORDER BY") {
						t.Errorf("target must be filtered before ordering and LIMIT: %s", query)
					}
					if tt.noTID && strings.Contains(query, fakeTIDColumn) {
						return nil, errors.New("Unknown column 'TID'")
					}
					return &fakeRows{columns: make([]string, len(tt.row)), rows: [][]driver.Value{tt.row}}, nil
				}}
				got, err := cfg.sample(t.Context(), openFakeMySQL(t, server), id)
				if err != nil || len(got) != 1 || got[0].ID != 7 || got[0].ElapsedSeconds != 400 || got[0].Query != "SELECT 1" || !got[0].Killable() {
					t.Fatalf("sample = %+v, %v", got, err)
				}
				wantCalls := 1
				if tt.noTID {
					wantCalls++
				}
				if calls != wantCalls {
					t.Fatalf("queries = %d, want %d", calls, wantCalls)
				}
			})
		}
	}
}

func TestDBQueryTargetReadFailure(t *testing.T) {
	readErr := errors.New("read denied")
	for _, engine := range []string{SQLEngineMySQL, SQLEnginePostgres} {
		for _, tt := range []struct {
			name string
			err  error
		}{
			{name: "ended"},
			{name: "failed", err: readErr},
			{name: "timeout", err: context.DeadlineExceeded},
		} {
			t.Run(engine+"/"+tt.name, func(t *testing.T) {
				cfg := DBQueryConfig{Engine: engine, MaxLength: 100, States: []string{pgStateActive}, server: &mysqlServerInfo{known: true}}
				server := &fakeMySQLServer{query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
					if errors.Is(tt.err, context.DeadlineExceeded) {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					if tt.err != nil {
						return nil, tt.err
					}
					return &fakeRows{}, nil
				}}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
				defer cancel()
				got, err := cfg.sample(ctx, openFakeMySQL(t, server), 7)
				if !errors.Is(err, tt.err) {
					t.Fatalf("sample error = %v, want %v", err, tt.err)
				}
				if err == nil {
					if _, err := verifyDBQueryTarget(got, DBQueryKill{ID: 7, Identity: "7:70"}); !errors.Is(err, ErrDBQueryChanged) {
						t.Fatalf("an absent target must be refused: %v", err)
					}
				}
			})
		}
	}
}

func TestDBQueryListReportsCompleteness(t *testing.T) {
	for _, engine := range []string{SQLEngineMySQL, SQLEnginePostgres} {
		for _, count := range []int{0, 499, 500, 501} {
			t.Run(fmt.Sprintf("%s/rows=%d", engine, count), func(t *testing.T) {
				cfg := DBQueryConfig{Engine: engine, MaxLength: 100, States: []string{pgStateActive}, server: &mysqlServerInfo{known: true, mariadb: true}}
				server := &fakeMySQLServer{query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
					if !strings.Contains(query, "LIMIT 501") {
						t.Fatalf("listing needs an extra row to detect truncation: %s", query)
					}
					columns := 14
					if engine == SQLEnginePostgres {
						columns = 11
					}
					rows := make([][]driver.Value, count)
					for i := range rows {
						id := int64(i + 1)
						rows[i] = []driver.Value{id, "app", "localhost", "app", "Query", "", int64(400000), int64(0), id, "SELECT 1", int64(8), nil, nil, nil}
						if engine == SQLEnginePostgres {
							rows[i] = []driver.Value{id, "app", "app", "localhost", "active", "", int64(1000000), int64(2000000), int64(400000), "SELECT 1", int64(8)}
						}
					}
					return &fakeRows{columns: make([]string, columns), rows: rows}, nil
				}}
				got, err := cfg.sampleList(t.Context(), openFakeMySQL(t, server))
				if err != nil || got.Complete != (count <= 500) || len(got.Queries) != min(count, 500) {
					t.Fatalf("sample complete=%v count=%d err=%v", got.Complete, len(got.Queries), err)
				}
			})
		}
	}
}

func TestDBQueryListFailureIsNotACompleteEmptySample(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint("timeout=", timeout), func(t *testing.T) {
			cfg := DBQueryConfig{Engine: SQLEnginePostgres, MaxLength: 100}
			server := &fakeMySQLServer{query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
				if timeout {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, errors.New("read denied")
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			got, err := cfg.sampleList(ctx, openFakeMySQL(t, server))
			if err == nil || got.Complete || len(got.Queries) != 0 {
				t.Fatalf("failed read claimed an observation: %+v %v", got, err)
			}
			if timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
