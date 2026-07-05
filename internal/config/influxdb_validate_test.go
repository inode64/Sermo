package config

import "testing"

func TestValidateInfluxDBQueryCheck(t *testing.T) {
	good := validateService(t, `
name: tsdb
service: x
checks:
  cpu: { type: influxdb-query, database: telegraf, query: "SELECT mean(load) FROM cpu", op: "<", value: "80" }
`)
	if hasIssue(good, "checks.cpu") {
		t.Fatalf("valid influxdb-query check flagged: %v", good)
	}

	bad := validateService(t, `
name: tsdb
service: x
checks:
  numeric: { type: influxdb-query, database: telegraf, query: "SELECT mean(load) FROM cpu", op: "<", value: many }
  regex: { type: influxdb-query, database: telegraf, query: "SELECT last(status) FROM health", op: "=~", value: "[" }
`)
	mustHave(t, bad, `checks.numeric value "many" must be numeric for op <`)
	mustHave(t, bad, "checks.regex value is not a valid regexp")
}
