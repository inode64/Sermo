package config

import "testing"

func TestValidateHTTPComparisonsValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: web
service: { name: x }
checks:
  api:
    type: http
    url: "http://127.0.0.1/health"
    expect_status: { op: "<", value: 500 }
    expect_body: { op: "=~", value: "^OK" }
    expect_latency: { op: "<", value: 800 }
    expect_json:
      version: { op: "=~", value: "^v[0-9]+" }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.api") {
			t.Fatalf("valid http comparisons must produce no issue: %v", issues)
		}
	}
}

func TestValidateHTTPComparisonErrors(t *testing.T) {
	cases := map[string]struct {
		field string
		want  string
	}{
		"bad body op":      {`expect_body: { op: "~~", value: "x" }`, "expect_body op"},
		"non-numeric body": {`expect_body: { op: ">", value: "abc" }`, "must be numeric"},
		"bad body regex":   {`expect_body: { op: "=~", value: "[" }`, "valid regexp"},
		"bad status op":    {`expect_status: { op: "between", value: "1" }`, "expect_status op"},
		"non-numeric stat": {`expect_status: { op: "<", value: "abc" }`, "must be numeric"},
		"latency not map":  {`expect_latency: "fast"`, "expect_latency must be an"},
		"bad json op":      {"expect_json:\n      v: { op: \"~~\", value: \"1\" }", "op"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			yaml := "kind: service\nname: web\nservice: { name: x }\nchecks:\n  api:\n    type: http\n    url: \"http://127.0.0.1/h\"\n    " + c.field + "\n"
			mustHave(t, validateService(t, yaml), c.want)
		})
	}
}
