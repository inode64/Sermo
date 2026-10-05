package config

import "testing"

// Connection-check validation: each case loads one service YAML and asserts
// whether Validate reports issues against the named check. Same want/absent
// shape as TestServiceValidationIssues in validate30_test.go; kept here because
// these cases are the per-protocol connection schemas this file owns.
func TestConnCheckValidationIssues(t *testing.T) {
	tests := []struct {
		name    string
		service string
		want    []string
		absent  []string
	}{
		{
			name: "mysql check valid",
			service: `
name: db
service: x
checks:
  conn: { type: mysql, user: monitor, password: secret, port: 3306, tls: false }
`,
			absent: []string{"checks.conn"},
		},
		{
			name: "postgres sslmode accepted",
			service: `
name: db
service: x
checks:
  conn: { type: postgres, user: monitor, tls: verify-full }
`,
			absent: []string{"checks.conn"},
		},
		{
			name: "postgres alias sslmode disable accepted",
			service: `
name: db
service: x
checks:
  conn: { type: postgresql, user: monitor, tls: disable }
`,
			absent: []string{"checks.conn"},
		},
		{
			name: "sslmode rejected outside postgres",
			service: `
name: cache
service: x
checks:
  conn: { type: redis, tls: disable }
`,
			want: []string{`checks.conn.tls "disable" must be a boolean or skip-verify`},
		},
		{
			name: "sslmode rejected for mysql sql check",
			service: `
name: db
service: x
checks:
  rows: { type: sql, engine: mysql, user: u, query: "select 1", op: "==", value: 1, tls: verify-full }
`,
			want: []string{`checks.rows.tls "verify-full" must be a boolean or skip-verify`},
		},
		{
			name: "sslmode accepted for postgres sql check",
			service: `
name: db
service: x
checks:
  rows: { type: sql, engine: postgres, user: u, query: "select 1", op: "==", value: 1, tls: verify-full }
`,
			absent: []string{"checks.rows"},
		},
		{
			name: "sslmode rejected for mongodb query check",
			service: `
name: db
service: x
checks:
  docs: { type: mongodb-query, command: '{"ping":1}', result: ok, op: "==", value: 1, tls: prefer }
`,
			want: []string{`checks.docs.tls "prefer" must be a boolean or skip-verify`},
		},
		{
			name: "socket accepted where the probe dials it",
			service: `
name: db
service: x
checks:
  conn: { type: mariadb, user: monitor, socket: /run/mysqld/mysqld.sock }
`,
			absent: []string{"checks.conn"},
		},
		{
			name: "socket rejected where the probe ignores it",
			service: `
name: db
service: x
checks:
  conn: { type: postgres, user: monitor, socket: /run/postgresql/.s.PGSQL.5432 }
`,
			want: []string{"checks.conn.socket is not supported by a postgres check; use host and port"},
		},
		{
			name: "empty socket ignored",
			service: `
name: filter
service: x
checks:
  conn: { type: rspamd, socket: "" }
`,
			absent: []string{"checks.conn"},
		},
		{
			name: "cloudflared check valid",
			service: `
name: tunnel
service: cloudflared
checks:
  protocol: { type: cloudflared, host: 127.0.0.1, port: 60123, tls: false }
`,
			absent: []string{"checks.protocol"},
		},
		{
			name: "dhclient check valid",
			service: `
name: dhclient
service: dhclient
checks:
  protocol: { type: dhclient, host: 0.0.0.0, port: 68, lease_file: /var/lib/dhcp/dhclient.leases }
`,
			absent: []string{"checks.protocol"},
		},
		{
			name: "mysql check bad tls",
			service: `
name: db
service: x
checks:
  conn: { type: mariadb, user: u, tls: maybe }
`,
			want: []string{"tls"},
		},
		{
			name: "conn expect valid",
			service: `
name: dns
service: x
checks:
  resolver:
    type: dns
    host: 1.1.1.1
    expect:
      rcode: NOERROR
      answers: { op: ">", value: 0 }
`,
			absent: []string{"checks.resolver"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := validateService(t, tt.service)
			for _, want := range tt.want {
				mustHave(t, issues, want)
			}
			for _, absent := range tt.absent {
				mustNotHave(t, issues, absent)
			}
		})
	}
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

func TestValidateSMTPAcceptanceCheck(t *testing.T) {
	valid := validateService(t, `
name: mail-egress
service: exim
checks:
  gmail:
    type: smtp_acceptance
    helo: mail.sender.example
    mail_from: probe@sender.example
    recipient: canary@gmail.example
    starttls: required
    interface: eth0
    timeout: 15s
`)
	mustNotHave(t, valid, "checks.gmail")

	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{name: "missing fields", entry: "type: smtp_acceptance", want: "helo is required"},
		{name: "helo not fqdn", entry: "type: smtp_acceptance, helo: localhost, mail_from: probe@sender.example, recipient: canary@gmail.example", want: "helo must be a fully-qualified"},
		{name: "display-name sender", entry: `type: smtp_acceptance, helo: mail.sender.example, mail_from: "Probe <probe@sender.example>", recipient: canary@gmail.example`, want: "mail_from must be a bare"},
		{name: "invalid recipient domain", entry: "type: smtp_acceptance, helo: mail.sender.example, mail_from: probe@sender.example, recipient: canary@localhost", want: "recipient domain must be"},
		{name: "invalid starttls", entry: "type: smtp_acceptance, helo: mail.sender.example, mail_from: probe@sender.example, recipient: canary@gmail.example, starttls: disabled", want: "starttls must be required or opportunistic"},
		{name: "explicit host", entry: "type: smtp_acceptance, host: mx.example, helo: mail.sender.example, mail_from: probe@sender.example, recipient: canary@gmail.example", want: "host is not supported"},
		{name: "implicit tls", entry: "type: smtp_acceptance, tls: true, helo: mail.sender.example, mail_from: probe@sender.example, recipient: canary@gmail.example", want: "tls is not supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustHave(t, validateService(t, "name: mail-egress\nservice: exim\nchecks:\n  gmail: {"+test.entry+"}\n"), test.want)
		})
	}
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

func TestValidateConnMaxIncrease(t *testing.T) {
	if issues := validateService(t, `
name: cache
service: x
checks:
  growth:
    type: redis
    max_increase: { keys: 20000, evicted_keys: 0 }
    within: 10m
`); len(issues) != 0 {
		t.Fatalf("valid max_increase rejected: %v", issues)
	}

	for body, want := range map[string]string{
		"max_increase: 5\n    within: 10m":            "max_increase must be a mapping of field -> non-negative integer",
		"max_increase: { keys: -1 }\n    within: 10m": "max_increase.keys must be a non-negative integer",
		"max_increase: { keys: 10 }":                  "max_increase requires within",
		"max_increase: { keys: 10 }\n    within: no":  "within must be a valid positive duration",
		"within: 10m": "within is only accepted with max_increase",
	} {
		mustHave(t, validateService(t, "\nname: cache\nservice: x\nchecks:\n  growth:\n    type: redis\n    "+body+"\n"), want)
	}
}
