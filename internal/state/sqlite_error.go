package state

import (
	"errors"

	sqlite3 "modernc.org/sqlite/lib"
)

const sqlitePrimaryResultCodeMask = 0xff

type sqliteCodeError interface {
	Code() int
}

// IsSQLiteContention reports whether err is transient SQLite writer
// contention. Extended result codes retain their primary code in the low byte.
func IsSQLiteContention(err error) bool {
	var sqliteErr sqliteCodeError
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & sqlitePrimaryResultCodeMask {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}
