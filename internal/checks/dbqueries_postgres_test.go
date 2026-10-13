package checks

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestPostgresActivityCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name            string
		version         int
		present, absent []string
	}{
		{"8.2", 80223, []string{"procpid AS pid", "current_query AS query", "WHEN waiting"}, []string{"backend_type", "wait_event", "left("}},
		{"9.2", 90200, []string{"WHEN waiting"}, []string{"procpid", "backend_type", "wait_event"}},
		{"9.6", 90600, []string{"wait_event_type"}, []string{"procpid", "backend_type", "WHEN waiting"}},
		{"10", 100000, []string{"backend_type = 'client backend'", "wait_event_type"}, []string{"procpid", "WHEN waiting"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query := postgresActivityQuery(tt.version)
			for _, part := range tt.present {
				if !strings.Contains(query, part) {
					t.Errorf("missing %q", part)
				}
			}
			for _, part := range tt.absent {
				if strings.Contains(query, part) {
					t.Errorf("unsupported %q", part)
				}
			}
		})
	}
}

func TestPostgresSamplerCachesCapabilitiesAndPreservesIdentity(t *testing.T) {
	info := &postgresServerInfo{}
	fail := false
	server := &fakeMySQLServer{query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
		if query == postgresVersionQuery {
			return &fakeRows{columns: []string{"version"}, rows: [][]driver.Value{{"80223"}}}, nil
		}
		if fail {
			return nil, errors.New("connection lost")
		}
		return &fakeRows{columns: make([]string, 11), rows: [][]driver.Value{
			{int64(7), "postgres", "app", "local", "active", "", int64(1000000), int64(2000000), int64(400000), "SELECT 1", int64(8)},
		}}, nil
	}}
	db := openFakeMySQL(t, server)
	for range 2 {
		rows, err := samplePostgresQueries(t.Context(), db, []string{pgStateActive}, 100, 0, info)
		if err != nil || len(rows) != 1 || rows[0].Identity != dbQueryIdentity(7, "1000000", "2000000") || rows[0].ElapsedSeconds != 400 {
			t.Fatalf("legacy sample = %+v, %v", rows, err)
		}
	}
	if server.count(postgresVersionQuery) != 1 {
		t.Fatal("version must be cached")
	}
	if _, err := samplePostgresQueries(t.Context(), db, []string{pgStateActive}, 100, 7, info); err == nil || !strings.Contains(err.Error(), "cancellation requires") {
		t.Fatalf("legacy cancellation must fail closed: %v", err)
	}
	fail = true
	if _, err := samplePostgresQueries(t.Context(), db, nil, 100, 0, info); err == nil || info.version != 0 {
		t.Fatalf("failed samples must invalidate capabilities: %v", err)
	}
}
