package config

import "testing"

func TestValidateSQLCheckValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  q: { type: sql, engine: sqlite, path: /var/lib/app.db, query: "SELECT count(*) FROM t", op: ">", value: "0" }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.q") {
			t.Fatalf("a valid sql check must produce no issue: %v", issues)
		}
	}
}

func TestValidateSQLCheckErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"unknown engine": {`q: { type: sql, engine: oracle, query: "SELECT 1", op: "==", value: "1" }`, "engine"},
		"missing query":  {`q: { type: sql, engine: sqlite, path: /a.db, op: ">", value: "0" }`, "query is required"},
		"bad op":         {`q: { type: sql, engine: sqlite, path: /a.db, query: "SELECT 1", op: "~~", value: "1" }`, "op"},
		"non-numeric":    {`q: { type: sql, engine: sqlite, path: /a.db, query: "SELECT 1", op: ">", value: "abc" }`, "must be numeric"},
		"bad regex":      {`q: { type: sql, engine: sqlite, path: /a.db, query: "SELECT 1", op: "=~", value: "[" }`, "valid regexp"},
		"sqlite no path": {`q: { type: sql, engine: sqlite, query: "SELECT 1", op: ">", value: "0" }`, "path is required"},
		"mysql no user":  {`q: { type: sql, engine: mysql, query: "SELECT 1", op: "==", value: "1" }`, "user is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustHave(t, validateService(t, "kind: service\nname: db\nservice: { name: x }\nchecks:\n  "+c.body+"\n"), c.want)
		})
	}
}
