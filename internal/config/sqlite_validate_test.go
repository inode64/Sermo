package config

import "testing"

func TestValidateSQLiteCheck(t *testing.T) {
	good := validateService(t, `
name: db
service: x
checks:
  dbfile: { type: sqlite, path: /var/lib/app/app.db, quick: true }
`)
	if hasIssue(good, "checks.dbfile") {
		t.Fatalf("valid sqlite check flagged: %v", good)
	}

	bad := validateService(t, `
name: db
service: x
checks:
  missing: { type: sqlite3 }
  quick: { type: sqlite, path: /var/lib/app/app.db, quick: "yes" }
`)
	mustHave(t, bad, "checks.missing.path is required for a sqlite check")
	mustHave(t, bad, "checks.quick.quick must be a boolean")
}
