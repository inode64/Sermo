package config

import "testing"

func TestValidateMySQLCheckValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mysql, user: monitor, password: secret, port: 3306, tls: false }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.conn") {
			t.Fatalf("a valid mysql check must produce no issue: %v", issues)
		}
	}
}

func TestValidateMySQLCheckRequiresUser(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mysql }
`), "user is required")
}

func TestValidatePostgresSSLModeAccepted(t *testing.T) {
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: postgres, user: monitor, tls: verify-full }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.conn") {
			t.Fatalf("postgres tls=verify-full must be valid: %v", issues)
		}
	}
}

func TestValidateCloudflaredCheckValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: tunnel
service: { name: cloudflared }
checks:
  protocol: { type: cloudflared, host: 127.0.0.1, port: 60123, tls: false }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.protocol") {
			t.Fatalf("a valid cloudflared check must produce no issue: %v", issues)
		}
	}
}

func TestValidateMySQLCheckBadTLS(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mariadb, user: u, tls: maybe }
`), "tls")
}

func TestValidateConnExpectValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: dns
service: { name: x }
checks:
  resolver:
    type: dns
    host: 1.1.1.1
    expect:
      rcode: NOERROR
      answers: { op: ">", value: 0 }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.resolver") {
			t.Fatalf("valid conn expect must produce no issue: %v", issues)
		}
	}
}

func TestValidateConnExpectErrors(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: dns
service: { name: x }
checks:
  resolver:
    type: dns
    expect:
      answers: { op: "~~", value: 0 }
`), "expect.answers op")
	mustHave(t, validateService(t, `
kind: service
name: dns
service: { name: x }
checks:
  resolver:
    type: dns
    expect:
      answers: { op: ">", value: "abc" }
`), "must be numeric")
	mustHave(t, validateService(t, `
kind: service
name: dns
service: { name: x }
checks:
  resolver:
    type: dns
    expect_latency: { op: "<", value: "abc" }
`), "must be numeric")
}
