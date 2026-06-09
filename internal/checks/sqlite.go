package checks

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver (pure Go)
)

// sqliteCheck verifies a SQLite database file is healthy by running SQLite's
// integrity check. It passes (health-style, OK==true) when the check reports
// "ok"; a missing/unreadable file, a non-database file, or reported corruption
// fail it. The file is opened read-only so the check never modifies it. With
// quick set it runs the faster PRAGMA quick_check (skips some per-row checks).
type sqliteCheck struct {
	base
	path  string
	quick bool
}

func (c sqliteCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	if _, err := os.Stat(c.path); err != nil {
		return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
	}

	db, err := sql.Open("sqlite", "file:"+c.path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return c.result(false, fmt.Sprintf("open %s: %v", c.path, err), start)
	}
	defer func() { _ = db.Close() }()

	pragma := "PRAGMA integrity_check;"
	if c.quick {
		pragma = "PRAGMA quick_check;"
	}
	var result string
	if err := db.QueryRowContext(ctx, pragma).Scan(&result); err != nil {
		return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
	}
	if result != "ok" {
		return c.result(false, fmt.Sprintf("%s: %s", c.path, result), start)
	}

	res := c.result(true, c.path+": integrity ok", start)
	res.Data = map[string]any{"path": c.path}
	return res
}
