package config

import (
	"testing"
)

// TestValidateExpectStatusShapes documents that scalar and list expect_status
// values are validated (element-by-element) by the resolved-tree scalar walk,
// while the {op,value} mapping form is validated in validateHTTPFields — the two
// paths together cover every shape parseStatusMatcher accepts.
func TestValidateExpectStatusShapes(t *testing.T) {
	check := func(expect string) []Issue {
		return validateService(t, `
name: svc
service: svc
policy: { cooldown: 5m }
checks:
  - { name: h, type: http, url: "http://x", expect_status: `+expect+` }
`)
	}
	// Valid shapes produce no expect_status issue.
	for _, ok := range []string{`200`, `"2xx"`, `[200, "3xx"]`, `{op: "<", value: 500}`} {
		mustNotHave(t, check(ok), "expect_status")
	}
	// Invalid scalar and invalid list element are caught via the scalar walk.
	mustHave(t, check(`999nope`), "expect_status")
	mustHave(t, check(`[200, bogus]`), "expect_status")
}

func TestValidateCountCheck(t *testing.T) {
	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-path: { type: count, op: ">", value: 1 }
  bad-kind: { type: count, path: /var/log, of: pipe, op: ">", value: 1 }
  bad-op:   { type: count, path: /var/log, op: "=>", value: 1 }
  bad-val:  { type: count, path: /var/log, op: ">", value: lots }
  bad-rec:  { type: count, path: /var/log, recursive: "yes", op: ">", value: 1 }
  delta-no-window:
    type: count
    path: /var/log
    delta: { op: ">", value: 10 }
  delta-bad-window:
    type: count
    path: /var/log
    delta: { op: ">", value: 10 }
    within: nope
  delta-bad-op:
    type: count
    path: /var/log
    delta: { op: "=>", value: 10 }
    within: 2m
  delta-bad-val:
    type: count
    path: /var/log
    delta: { op: ">", value: many }
    within: 2m
  delta-mixed-count:
    type: count
    path: /var/log
    count: { op: ">", value: 10 }
    delta: { op: ">", value: 5 }
    within: 2m
  delta-mixed-top:
    type: count
    path: /var/log
    op: ">"
    value: 10
    delta: { op: ">", value: 5 }
    within: 2m
  window-no-delta:
    type: count
    path: /var/log
    op: ">"
    value: 10
    within: 2m
`)
	mustHave(t, bad, "count check requires a path")
	mustHave(t, bad, `count `+"`of`"+` "pipe" is not one of`)
	mustHave(t, bad, "count check requires a valid op")
	mustHave(t, bad, `count check value "lots" must be numeric`)
	mustHave(t, bad, "count recursive must be a boolean")
	mustHave(t, bad, "within is required when count delta is set")
	mustHave(t, bad, `within "nope" must be a valid positive duration`)
	mustHave(t, bad, "delta has an invalid op")
	mustHave(t, bad, `delta value "many" must be numeric`)
	mustHave(t, bad, "count check must not mix a count threshold with delta")
	mustHave(t, bad, "count check must not mix top-level op/value with delta")
	mustHave(t, bad, "within requires delta")

	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  tmp-files: { type: count, path: /tmp, of: file, recursive: true, op: "<=", value: 100 }
  tmp-growth:
    type: count
    path: /tmp
    of: file
    delta: { op: ">", value: 20 }
    within: 2m
`)
	mustNotHave(t, good, "count")
}

func TestValidateResourceChecksAsServiceChecks(t *testing.T) {
	// Host-resource checks (storage/load/…) are usable in a service's checks: and
	// referenceable from rules, just like tcp/http/metric.
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  rootfs: { type: storage, path: /, used_pct: { op: ">=", value: 90 } }
  sysload: { type: load, per_cpu: true, load5: { op: ">", value: 2 } }
  oomkills: { type: oom }
rules:
  alert-load:
    type: alert
    if: { active: { check: sysload } }
    then: { action: alert, message: "load high" }
`)
	if len(issues) != 0 {
		t.Fatalf("resource checks should be valid service checks, got: %v", issues)
	}
}

func TestValidateCertCheck(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api: { type: cert, host: api.example.com, port: 443, expires_in_days: 14, on_algorithm_change: true, cert_verify: true }
`, "checks.api", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-host: { type: cert }
  bad-days: { type: cert, host: x, expires_in_days: 0 }
  bad-port: { type: cert, host: x, port: 70000 }
  bad-bool: { type: cert, host: x, cert_verify: "yes" }
`,
		"checks.no-host requires a host or a path",
		"checks.bad-days.expires_in_days must be a positive integer",
		"checks.bad-port.port must be an integer in 1..65535",
		"checks.bad-bool.cert_verify must be a boolean")
}

func TestValidateVerifyFlag(t *testing.T) {
	// verify: true on a health check (http) is valid.
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/", verify: true }
`, "checks.http", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  cond: { type: memory, used_pct: { op: ">", value: 90 }, verify: true }
  notbool: { type: http, url: "http://x/", verify: "yes" }
`,
		"checks.cond.verify is only valid on a health check",
		"checks.notbool.verify must be a boolean")
}

func TestValidateHTTPFields(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api:
    type: http
    url: "https://api/health"
    method: POST
    headers: { Authorization: "Bearer t" }
    json: { ping: true }
    expect_status: 200
    expect_json: { status: ok }
    expect_body: { op: contains, value: ok }
`, "checks.api", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-url: { type: http, method: POST }
  bad-headers: { type: http, url: "http://x", headers: "nope" }
  bad-json: { type: http, url: "http://x", expect_json: "nope" }
  bad-op: { type: http, url: "http://x", expect_json: { n: { op: "=>", value: 1 } } }
`,
		"checks.no-url.url is required for an http check",
		"checks.bad-headers.headers must be a mapping",
		"checks.bad-json.expect_json must be a mapping",
		"checks.bad-op.expect_json.n op \"=>\" is not one of")
}

func TestValidatePortsCheck(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  scan: { type: ports, host: 127.0.0.1, ports: "80,443,1024-4000", expect: open, match: any, on_change: true, connect_timeout: 500ms }
`, "checks.scan", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-ports:   { type: ports, host: x }
  bad-range:  { type: ports, ports: "100-50" }
  bad-port:   { type: ports, ports: "70000" }
  too-many:   { type: ports, ports: "1-20000" }
  bad-expect: { type: ports, ports: "80", expect: weird }
  bad-match:  { type: ports, ports: "80", match: most }
  bad-timeout: { type: ports, ports: "80", connect_timeout: fast }
`,
		"checks.no-ports.ports is required",
		`checks.bad-range.ports range "100-50" is out of 1..65535`,
		`checks.bad-port.ports range "70000" is out of 1..65535`,
		"checks.too-many.ports too many ports",
		"checks.bad-expect.expect must be open, closed or any",
		"checks.bad-match.match must be all, any or none",
		"checks.bad-timeout.connect_timeout must be a valid positive duration")
}

func TestValidateCheckGate(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  tcp:   { type: tcp, host: 127.0.0.1, port: 3306 }
  query: { type: command, command: ["/bin/true"], requires: [tcp], skip_when_changed: ["/etc/my.cnf"] }
`, "checks.query", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  tcp:   { type: tcp, host: 127.0.0.1, port: 3306 }
  self:  { type: tcp, host: 127.0.0.1, port: 80, requires: [self] }
  ghost: { type: tcp, host: 127.0.0.1, port: 80, requires: [missing] }
  badsk: { type: tcp, host: 127.0.0.1, port: 80, skip_when_changed: 5 }
  badreqlist: { type: tcp, host: 127.0.0.1, port: 80, requires: [123] }
  badsklist: { type: tcp, host: 127.0.0.1, port: 80, skip_when_changed: [123] }
`,
		"checks.self.requires cannot reference itself",
		`checks.ghost.requires references unknown check "missing"`,
		"checks.badsk.skip_when_changed must be a file path or a list",
		"checks.badreqlist.requires must be a check name or a list of check names",
		"checks.badsklist.skip_when_changed must be a file path or a list")
}

// assertCheckRequiresPath asserts that a check of the given type reports
// "path is required for a <type> check" both when path is omitted and when path
// is a list containing a non-string element.
func assertCheckRequiresPath(t *testing.T, checkType string) {
	t.Helper()
	want := "path is required for a " + checkType + " check"
	mustHave(t, validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  c: { type: `+checkType+` }
`), want)
	mustHave(t, validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  c: { type: `+checkType+`, path: [/run/svc, 7] }
`), want)
}

func TestValidatePidfileCheckRequiresPath(t *testing.T) {
	assertCheckRequiresPath(t, "pidfile")
}

func TestValidateSocketCheckRequiresPath(t *testing.T) {
	assertCheckRequiresPath(t, "socket")
}

func TestValidateRequiredScalarCheckFields(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{
			name:  "file missing path",
			entry: "type: file",
			want:  "path is required for a file check",
		},
		{
			name:  "file empty path",
			entry: `type: file, path: ""`,
			want:  "path is required for a file check",
		},
		{
			name:  "binary missing path",
			entry: "type: binary",
			want:  "path is required for a binary check",
		},
		{
			name:  "binary empty path",
			entry: `type: binary, path: ""`,
			want:  "path is required for a binary check",
		},
		{
			name:  "libraries missing binary",
			entry: "type: libraries",
			want:  "binary is required for a libraries check",
		},
		{
			name:  "libraries empty binary",
			entry: `type: libraries, binary: ""`,
			want:  "binary is required for a libraries check",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  target: { `+tt.entry+` }
`)
			mustHave(t, issues, tt.want)
		})
	}
}

func TestValidateLockfileCheckRequiresPath(t *testing.T) {
	assertCheckRequiresPath(t, "lockfile")
}

func TestValidateCertServerNameAndFileScope(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  bad-sni:  { type: cert, host: api.example.com, server_name: 443 }
  pem-file: { type: cert, path: /etc/ssl/api.pem, port: 443, server_name: api.example.com }
`)
	mustHave(t, issues, "server_name must be a string")
	mustHave(t, issues, "port does not apply to a PEM file path")
	mustHave(t, issues, "server_name does not apply to a PEM file path")

	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api: { type: cert, host: 10.0.0.5, port: 8443, server_name: api.example.com, expires_in_days: 14 }
  pem: { type: cert, path: /etc/ssl/api.pem, expires_in_days: 14 }
`)
	mustNotHave(t, good, "cert")
	mustNotHave(t, good, "server_name")
}

func TestValidateMemoryCheckBothSurfaces(t *testing.T) {
	// In a service's checks: (unified check types — same validator as watches).
	assertServiceValidationTokens(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  ram: { type: memory, used_pct: { op: ">=", value: "90%" } }
`, []string{"memory", "ram"}, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-pred:  { type: memory }
  bad-size: { type: memory, available_bytes: { op: "<", value: 1024 } }
`,
		"checks.no-pred requires at least one of used_pct/available_pct/available_bytes",
		`available_bytes value "1024" must include a size suffix`)
}

func TestValidatePressureCheck(t *testing.T) {
	assertServiceValidationTokens(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  mem-stall: { type: pressure, resource: memory, some_avg10: { op: ">", value: 10 } }
`, []string{"pressure", "mem-stall"}, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  bad-res: { type: pressure, resource: disk, some_avg10: { op: ">", value: 10 } }
  no-pred: { type: pressure, resource: cpu }
`,
		"checks.bad-res.resource must be cpu, memory or io",
		"checks.no-pred requires at least one of some_avg10/some_avg60/some_avg300/full_avg10/full_avg60/full_avg300")
}

func TestValidateDiskIOCheck(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  db-disk: { type: diskio, device: nvme0n1, util_pct: { op: ">=", value: "90%" }, write_bytes: { op: ">", value: 50M } }
`, "db-disk", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-dev:  { type: diskio, util_pct: { op: ">=", value: 90 } }
  no-pred: { type: diskio, device: sda }
  raw-bps: { type: diskio, device: sda, read_bytes: { op: ">", value: 1048576 } }
`,
		"checks.no-dev.device is required for a diskio check",
		"checks.no-pred requires at least one of util_pct/read_bytes/write_bytes/await_ms",
		`read_bytes value "1048576" must include a size suffix`)
}
