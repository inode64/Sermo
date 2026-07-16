package config

import "testing"

func TestValidateSQLiteCheck(t *testing.T) {
	assertServiceValidation(t, `
name: db
service: x
checks:
  dbfile: { type: sqlite, path: /var/lib/app/app.db, quick: true }
`, "checks.dbfile", `
name: db
service: x
checks:
  missing: { type: sqlite3 }
  quick: { type: sqlite, path: /var/lib/app/app.db, quick: "yes" }
`,
		"checks.missing.path is required for a sqlite check",
		"checks.quick.quick must be a boolean")
}
