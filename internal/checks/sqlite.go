package checks

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
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
	// A healthy database returns a single "ok" row; a corrupt one returns up to
	// 100 problem rows. Read them all so the failure message carries the real
	// detail instead of only the first line.
	rows, err := db.QueryContext(ctx, pragma)
	if err != nil {
		return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
	}
	if len(problems) > 0 {
		return c.result(false, fmt.Sprintf("%s: %s", c.path, strings.Join(problems, "; ")), start)
	}

	res := c.result(true, c.path+": integrity ok", start)
	res.Data = map[string]any{DataKeyPath: c.path}
	return res
}
