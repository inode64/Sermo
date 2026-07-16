package config

import "testing"

func TestValidateMySQLCheckValid(t *testing.T) {
	issues := validateService(t, `
name: db
service: x
checks:
  conn: { type: mysql, user: monitor, password: secret, port: 3306, tls: false }
`)
	mustNotHave(t, issues, "checks.conn")
}

func TestValidateMySQLCheckNoUserOK(t *testing.T) {
	// mysql no longer requires a user: a credential-free greeting probe is valid.
	issues := validateService(t, `
name: db
service: x
checks:
  conn: { type: mysql, port: 3306 }
`)
	mustNotHave(t, issues, "checks.conn")
}

func TestValidatePostgresCheckRequiresUser(t *testing.T) {
	mustHave(t, validateService(t, `
name: db
service: x
checks:
  conn: { type: postgres }
`), "user is required")
}

func TestValidatePostgresSSLModeAccepted(t *testing.T) {
	issues := validateService(t, `
name: db
service: x
checks:
  conn: { type: postgres, user: monitor, tls: verify-full }
`)
	mustNotHave(t, issues, "checks.conn")
}

func TestValidateCloudflaredCheckValid(t *testing.T) {
	issues := validateService(t, `
name: tunnel
service: cloudflared
checks:
  protocol: { type: cloudflared, host: 127.0.0.1, port: 60123, tls: false }
`)
	mustNotHave(t, issues, "checks.protocol")
}

func TestValidateDHClientCheckValid(t *testing.T) {
	issues := validateService(t, `
name: dhclient
service: dhclient
checks:
  protocol: { type: dhclient, host: 0.0.0.0, port: 68, lease_file: /var/lib/dhcp/dhclient.leases }
`)
	mustNotHave(t, issues, "checks.protocol")
}

func TestValidateMySQLCheckBadTLS(t *testing.T) {
	issues := validateService(t, `
name: db
service: x
checks:
  conn: { type: mariadb, user: u, tls: maybe }
`)
	mustHave(t, issues, "tls")
}

func TestValidateConnSharedFieldErrors(t *testing.T) {
	issues := validateService(t, `
name: dns
service: x
checks:
  resolver:
    type: dns
    port: 0
    tls: 1
    expect: scalar
    expect_latency: fast
    on_change: yes
    on_version_change: no
`)
	for _, want := range []string{
		"port \"0\" must be an integer",
		"tls must be a boolean or a string",
		"expect must be a mapping",
		"expect_latency must be an {op, value} mapping",
		"on_change must be a boolean",
		"on_version_change must be a boolean",
	} {
		mustHave(t, issues, want)
	}
}

func TestValidateConnExpectValid(t *testing.T) {
	issues := validateService(t, `
name: dns
service: x
checks:
  resolver:
    type: dns
    host: 1.1.1.1
    expect:
      rcode: NOERROR
      answers: { op: ">", value: 0 }
`)
	mustNotHave(t, issues, "checks.resolver")
}

func TestValidateConnExpectErrors(t *testing.T) {
	mustHave(t, validateService(t, `
name: dns
service: x
checks:
  resolver:
    type: dns
    expect:
      answers: { op: "~~", value: 0 }
`), "expect.answers op")
	mustHave(t, validateService(t, `
name: dns
service: x
checks:
  resolver:
    type: dns
    expect:
      answers: { op: ">", value: "abc" }
`), "must be numeric")
	mustHave(t, validateService(t, `
name: dns
service: x
checks:
  resolver:
    type: dns
    expect_latency: { op: "<", value: "abc" }
`), "must be numeric")
}

func TestValidateAnalyzeRulesShape(t *testing.T) {
	mustHave(t, validateService(t, `
name: db
service: x
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, match: "(", severity: warning } ] }
`), "invalid regex")

	mustHave(t, validateService(t, `
name: db
service: x
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, match: "x", severity: fatal } ] }
`), "severity must be")

	mustHave(t, validateService(t, `
name: db
service: x
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, severity: warning } ] }
`), "missing a match")

	// A valid analyze block produces no checks.config issue.
	issues := validateService(t, `
name: db
service: x
checks:
  config:
    type: command
    command: ["true"]
    analyze: { rules: [ { id: a, match: "(?i)deprecated", severity: warning } ] }
`)
	mustNotHave(t, issues, "checks.config")
}

func TestValidateCascadeTargets(t *testing.T) {
	// also_apply referencing an unknown service errors.
	mustHave(t, validateService(t, `
name: web
service: x
also_apply: [nope]
`), "not a configured service")

	// self-reference errors.
	mustHave(t, validateService(t, `
name: web
service: x
also_apply: [web]
`), "the service itself")

	mustHave(t, validateService(t, `
name: web
service: x
also_apply: [api, 7]
`), "also_apply must be a string or list of strings")
}

func TestValidateCleanOnStop(t *testing.T) {
	bad := validateService(t, `
name: s
service: x
stop_policy:
  clean_on_stop:
    - relative/path
    - { path: /var, recursive: true }
    - { path: "/var/cache/*", recursive: true }
    - { path: /var/cache/svc, recursive: yes }
`)
	mustHave(t, bad, "must be absolute")
	mustHave(t, bad, "refuses to recursively delete")
	mustHave(t, bad, "must not be a glob")
	mustHave(t, bad, "recursive must be a boolean")

	ok := validateService(t, `
name: s
service: x
stop_policy:
  clean_on_stop:
    - /run/svc/foo.tmp
    - /tmp/svc-*.lock
    - { path: /var/cache/svc, recursive: true }
`)
	mustNotHave(t, ok, "clean_on_stop")
}
