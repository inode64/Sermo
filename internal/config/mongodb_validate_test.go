package config

import "testing"

func TestValidateMongoDBQueryCheck(t *testing.T) {
	good := validateService(t, `
name: mongo
service: x
checks:
  jobs: { type: mongodb-query, database: app, collection: jobs, op: "<", value: "100" }
`)
	if hasIssue(good, "checks.jobs") {
		t.Fatalf("valid mongodb-query check flagged: %v", good)
	}

	bad := validateService(t, `
name: mongo
service: x
checks:
  numeric: { type: mongodb-query, database: app, collection: jobs, op: "<", value: many }
  regex: { type: mongodb-query, database: app, collection: jobs, op: "=~", value: "[" }
`)
	mustHave(t, bad, `checks.numeric value "many" must be numeric for op <`)
	mustHave(t, bad, "checks.regex value is not a valid regexp")
}
