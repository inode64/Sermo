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

func TestValidateMySQLCheckNoUserOK(t *testing.T) {
	// mysql no longer requires a user: a credential-free greeting probe is valid.
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mysql, port: 3306 }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.conn") {
			t.Fatalf("mysql without a user must be valid (greeting mode): %v", issues)
		}
	}
}

func TestValidatePostgresCheckRequiresUser(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: postgres }
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

func TestValidateDHClientCheckValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: dhclient
service: { name: dhclient }
checks:
  protocol: { type: dhclient, host: 0.0.0.0, port: 68, lease_file: /var/lib/dhcp/dhclient.leases }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.protocol") {
			t.Fatalf("a valid dhclient check must produce no issue: %v", issues)
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

func TestValidateAnalyzeRulesShape(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, match: "(", severity: warning } ] }
`), "invalid regex")

	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, match: "x", severity: fatal } ] }
`), "severity must be")

	// A valid analyze block produces no checks.config issue.
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, match: "(?i)deprecated", severity: warning } ] }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.config") {
			t.Fatalf("a valid analyze block must produce no issue: %v", issues)
		}
	}
}

func TestValidateCascadeTargets(t *testing.T) {
	// also_apply referencing an unknown service errors.
	mustHave(t, validateService(t, `
kind: service
name: web
service: { name: x }
also_apply: [nope]
`), "not a configured service")

	// self-reference errors.
	mustHave(t, validateService(t, `
kind: service
name: web
service: { name: x }
also_apply: [web]
`), "the service itself")
}
