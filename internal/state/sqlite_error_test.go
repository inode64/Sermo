package state

import (
	"errors"
	"fmt"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

type sqliteTestError int

func (e sqliteTestError) Error() string { return "sqlite test error" }
func (e sqliteTestError) Code() int     { return int(e) }

func TestIsSQLiteContention(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil"},
		{name: "generic", err: errors.New("database unavailable")},
		{name: "busy", err: sqliteTestError(sqlite3.SQLITE_BUSY), want: true},
		{name: "wrapped locked", err: fmt.Errorf("record event: %w", sqliteTestError(sqlite3.SQLITE_LOCKED)), want: true},
		{name: "extended busy", err: sqliteTestError(sqlite3.SQLITE_BUSY | 2<<8), want: true},
		{name: "read only", err: sqliteTestError(sqlite3.SQLITE_READONLY)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSQLiteContention(tc.err); got != tc.want {
				t.Fatalf("IsSQLiteContention(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
