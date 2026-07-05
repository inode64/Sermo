package config

import "testing"

func TestValidateHTTPComparisonsValid(t *testing.T) {
	issues := validateService(t, `
name: web
service: x
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

func TestValidateHTTPMethods(t *testing.T) {
	// Every standard verb (any case) validates cleanly.
	for _, m := range []string{"GET", "head", "POST", "Put", "PATCH", "delete", "OPTIONS", "TRACE", "CONNECT"} {
		issues := validateService(t, "name: web\nservice: x\nchecks:\n  api: { type: http, url: \"http://h/\", method: "+m+" }\n")
		for _, is := range issues {
			if hasIssue([]Issue{is}, "checks.api") {
				t.Fatalf("method %q must be valid: %v", m, issues)
			}
		}
	}
	// A typo / non-standard verb warns.
	mustHave(t, validateService(t, `
name: web
service: x
checks:
  api: { type: http, url: "http://h/", method: GTE }
`), "not a standard HTTP method")
}

func TestValidateHTTP3(t *testing.T) {
	// Valid: http3 over https.
	issues := validateService(t, `
name: web
service: x
checks:
  h3: { type: http, url: "https://h/", http3: true }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.h3") {
			t.Fatalf("valid http3 check must produce no issue: %v", issues)
		}
	}
	// http3 over http:// is rejected.
	mustHave(t, validateService(t, `
name: web
service: x
checks:
  h3: { type: http, url: "http://h/", http3: true }
`), "http3 requires an https url")
	// http3 + proxy is rejected.
	mustHave(t, validateService(t, `
name: web
service: x
checks:
  h3: { type: http, url: "https://h/", http3: true, proxy: "http://squid:3128" }
`), "mutually exclusive")
}

func TestValidateHTTPProxySchemeMessageIncludesSocks5h(t *testing.T) {
	mustHave(t, validateService(t, `
name: web
service: x
checks:
  proxy: { type: http, url: "http://h/", proxy: "ftp://proxy:21" }
`), "http, https, socks5 or socks5h")
}

func TestValidateHTTPComparisonErrors(t *testing.T) {
	cases := map[string]struct {
		field string
		want  string
	}{
		"bad body op":      {`expect_body: { op: "~~", value: "x" }`, "expect_body op"},
		"non-numeric body": {`expect_body: { op: ">", value: "abc" }`, "must be numeric"},
		"bad body regex":   {`expect_body: { op: "=~", value: "[" }`, "valid regexp"},
		"body string":      {`expect_body: "ok"`, "expect_body must be an"},
		"bad status op":    {`expect_status: { op: "between", value: "1" }`, "expect_status op"},
		"non-numeric stat": {`expect_status: { op: "<", value: "abc" }`, "must be numeric"},
		"latency not map":  {`expect_latency: "fast"`, "expect_latency must be an"},
		"bad json op":      {"expect_json:\n      v: { op: \"~~\", value: \"1\" }", "op"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			yaml := "name: web\nservice: x\nchecks:\n  api:\n    type: http\n    url: \"http://127.0.0.1/h\"\n    " + c.field + "\n"
			mustHave(t, validateService(t, yaml), c.want)
		})
	}
}
