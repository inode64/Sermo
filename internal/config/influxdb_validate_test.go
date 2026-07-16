package config

import "testing"

func TestValidateInfluxDBQueryCheck(t *testing.T) {
	assertServiceValidation(t, `
name: tsdb
service: x
checks:
  cpu: { type: influxdb-query, database: telegraf, query: "SELECT mean(load) FROM cpu", op: "<", value: "80" }
`, "checks.cpu", `
name: tsdb
service: x
checks:
  numeric: { type: influxdb-query, database: telegraf, query: "SELECT mean(load) FROM cpu", op: "<", value: many }
  regex: { type: influxdb-query, database: telegraf, query: "SELECT last(status) FROM health", op: "=~", value: "[" }
`,
		`checks.numeric value "many" must be numeric for op <`,
		"checks.regex value is not a valid regexp")
}
