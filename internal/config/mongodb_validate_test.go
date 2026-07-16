package config

import "testing"

func TestValidateMongoDBQueryCheck(t *testing.T) {
	assertServiceValidation(t, `
name: mongo
service: x
checks:
  jobs: { type: mongodb-query, database: app, collection: jobs, op: "<", value: "100" }
`, "checks.jobs", `
name: mongo
service: x
checks:
  numeric: { type: mongodb-query, database: app, collection: jobs, op: "<", value: many }
  regex: { type: mongodb-query, database: app, collection: jobs, op: "=~", value: "[" }
`,
		`checks.numeric value "many" must be numeric for op <`,
		"checks.regex value is not a valid regexp")
}
