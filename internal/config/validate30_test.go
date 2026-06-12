package config

import (
	"strings"
	"testing"
)

// validateService builds a single-service config (merged onto baseGlobal) and
// returns the issues for that service.
func validateService(t *testing.T, serviceYAML string) []Issue {
	t.Helper()
	global := writeConfig(t, map[string]string{
		"sermo.yml":       baseGlobal,
		"enabled/svc.yml": serviceYAML,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return Validate(cfg)
}

func mustHave(t *testing.T, issues []Issue, substr string) {
	t.Helper()
	if !hasIssue(issues, substr) {
		t.Fatalf("missing issue %q in %v", substr, issues)
	}
}

func TestValidateEngineDurations(t *testing.T) {
	global := writeConfig(t, map[string]string{"sermo.yml": `
engine:
  interval: notaduration
  default_timeout: 0s
  operation_timeout: bad
  max_parallel_checks: 0
  max_parallel_operations: -1
paths:
  includes: [ @ROOT@/enabled ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(cfg)
	for _, want := range []string{
		"engine.interval",
		"engine.default_timeout",
		"engine.operation_timeout",
		"engine.max_parallel_checks",
		"engine.max_parallel_operations",
	} {
		mustHave(t, issues, want)
	}
}

func TestValidateEngineOperationTimeoutAcceptsPositive(t *testing.T) {
	global := writeConfig(t, map[string]string{"sermo.yml": `
engine:
  operation_timeout: 90s
paths:
  includes: [ @ROOT@/enabled ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range Validate(cfg) {
		if strings.Contains(is.Msg, "operation_timeout") {
			t.Fatalf("unexpected issue: %v", is)
		}
	}
}

func TestValidateRuleStructure(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  bad-action:
    type: remediation
    if: { failed: { check: http } }
    then: { action: explode }
  guard-no-blocks:
    type: guard
    if: { failed: { check: http } }
    then: { action: block, message: "x" }
  remediation-with-blocks:
    type: remediation
    blocks: [restart]
    if: { failed: { check: http } }
    then: { action: restart }
  block-no-message:
    type: guard
    blocks: [restart]
    if: { failed: { check: http } }
    then: { action: block }
`)
	mustHave(t, issues, "then.action \"explode\" is not one of")
	mustHave(t, issues, "guard requires a non-empty blocks list")
	mustHave(t, issues, "only guard rules may set blocks")
	mustHave(t, issues, "action block requires a non-empty message")
}

func TestValidateMultiAction(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  ok-multi:
    type: remediation
    if: { failed: { check: http } }
    then:
      actions:
        - { type: alert, message: "down, restarting" }
        - { type: restart }
  bad-multi:
    type: remediation
    if: { failed: { check: http } }
    then:
      actions:
        - { type: alert }
        - { type: explode }
`)
	// The valid multi-action rule must not be flagged.
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-multi") {
			t.Fatalf("valid multi-action rule wrongly flagged: %v", is)
		}
	}
	mustHave(t, issues, "action alert requires a non-empty message")
	mustHave(t, issues, `then.action "explode" is not one of`)
}

func TestValidateRuleWindows(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  both:
    type: remediation
    if: { failed: { check: http } }
    for: { cycles: 0 }
    within: { cycles: 5, min_matches: 9 }
    then: { action: restart }
`)
	mustHave(t, issues, "cannot define both for and within")
	mustHave(t, issues, "for.cycles must be > 0")
	mustHave(t, issues, "within.min_matches must be <= within.cycles")
}

func TestValidateUnknownCheckReference(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  r:
    type: remediation
    if: { failed: { check: nonexistent } }
    then: { action: restart }
`)
	mustHave(t, issues, `references unknown check "nonexistent"`)
}

func TestValidateConditionExactlyOneOperator(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  r:
    type: remediation
    if:
      failed: { check: http }
      active: { check: http }
    then: { action: restart }
`)
	mustHave(t, issues, "must contain exactly one condition/operator")
}

func TestValidateInlineCommandConditionNeedsTimeout(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
rules:
  r:
    type: remediation
    if:
      command:
        command: ["can-restart"]
    then: { action: restart }
`)
	mustHave(t, issues, "command condition must declare a timeout")
}

func TestValidateSystemMetricOnlyInAlert(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
rules:
  bad:
    type: remediation
    if: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } }
    then: { action: restart }
  ok-alert:
    type: alert
    if: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } }
    then: { action: alert, message: "machine hot" }
`)
	mustHave(t, issues, "scope: system metric is only allowed in alert rules")
	// The alert rule's identical condition must NOT be flagged.
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-alert") && strings.Contains(is.Msg, "system metric") {
			t.Fatalf("alert rule wrongly flagged: %v", is)
		}
	}
}

func TestValidateMetricFormMismatch(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
rules:
  pct-on-count:
    type: alert
    if: { metric: { scope: service, name: process_count, op: ">", value: 50% } }
    then: { action: alert, message: m }
  abs-on-cpu:
    type: alert
    if: { metric: { scope: service, name: cpu, op: ">", value: 30 } }
    then: { action: alert, message: m }
`)
	mustHave(t, issues, `% threshold but metric "process_count" has no percentage form`)
	mustHave(t, issues, `absolute threshold but metric "cpu" has no absolute form`)
}

func TestValidateMetricFormValidCombinations(t *testing.T) {
	// memory has both forms; cpu accepts %; load accepts absolute.
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
rules:
  mem-pct: { type: alert, if: { metric: { name: memory, op: ">", value: 40% } }, then: { action: alert, message: m } }
  mem-abs: { type: alert, if: { metric: { name: memory, op: ">", value: 1000000 } }, then: { action: alert, message: m } }
  cpu-pct: { type: alert, if: { metric: { name: cpu, op: ">", value: 80% } }, then: { action: alert, message: m } }
  load:    { type: alert, if: { metric: { scope: system, name: load1, op: ">", value: 4 } }, then: { action: alert, message: m } }
`)
	for _, is := range issues {
		if strings.Contains(is.Msg, "threshold") && strings.Contains(is.Msg, "form") {
			t.Fatalf("valid metric form wrongly flagged: %v", is)
		}
	}
}

func TestValidateIndirectSystemMetricInRemediation(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  machine-hot: { type: metric, scope: system, name: total_cpu, op: ">", value: 90% }
rules:
  bad:
    type: remediation
    if: { active: { check: machine-hot } }
    then: { action: restart }
  ok-alert:
    type: alert
    if: { active: { check: machine-hot } }
    then: { action: alert, message: m }
`)
	mustHave(t, issues, `references system metric check "machine-hot", which is only allowed in alert rules`)
	// The alert rule referencing the same check must not be flagged.
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-alert") && strings.Contains(is.Msg, "system metric") {
			t.Fatalf("alert rule wrongly flagged: %v", is)
		}
	}
}

func TestValidateMetricCatalogAndValue(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
rules:
  r:
    type: alert
    if: { metric: { scope: service, name: not_a_metric, op: "~", value: abc } }
    then: { action: alert, message: "m" }
`)
	mustHave(t, issues, `metric "not_a_metric" is not in the service catalog`)
	mustHave(t, issues, "op \"~\" is not one of")
	mustHave(t, issues, "value \"abc\" must be a number")
}

func TestValidateStopPolicyKillSelector(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
stop_policy:
  force_kill: true
  kill_only_if:
    users: [mysql]
`)
	mustHave(t, issues, "kill_only_if must define both users and exe_any")
}

func TestValidateForceKillRequiresSelector(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
stop_policy:
  force_kill: true
`)
	mustHave(t, issues, "force_kill=true requires kill_only_if")
}

func TestValidateStopPolicyDurations(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
stop_policy:
  graceful_timeout: nope
  term_timeout: 0s
  kill_timeout: -1s
`)
	mustHave(t, issues, `stop_policy.graceful_timeout "nope" must be a valid positive duration`)
	mustHave(t, issues, `stop_policy.term_timeout "0s" must be a valid positive duration`)
	mustHave(t, issues, `stop_policy.kill_timeout "-1s" must be a valid positive duration`)
}

func TestValidateCheckEntrySchemas(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  cmd: { type: command, command: "echo hi" }
  svc-state: { type: service, expect: bogus }
  proc: { type: process, exe: /x, state: weird }
  opt: { type: binary, path: /x, optional: "yes" }
preflight:
  lockfile: { type: file_exists, path: /run/sermo/locks/x.lock }
`)
	mustHave(t, issues, "command must be an array, not a shell string")
	mustHave(t, issues, `expect "bogus" is not one of`)
	mustHave(t, issues, `state "weird" is not one of`)
	mustHave(t, issues, "optional must be a boolean")
	mustHave(t, issues, "must not point under the runtime lock dir")
}

func TestValidateCountCheck(t *testing.T) {
	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  no-path: { type: count, op: ">", value: 1 }
  bad-kind: { type: count, path: /var/log, of: pipe, op: ">", value: 1 }
  bad-op:   { type: count, path: /var/log, op: "=>", value: 1 }
  bad-val:  { type: count, path: /var/log, op: ">", value: lots }
  bad-rec:  { type: count, path: /var/log, recursive: "yes", op: ">", value: 1 }
`)
	mustHave(t, bad, "count check requires a path")
	mustHave(t, bad, `count `+"`of`"+` "pipe" is not one of`)
	mustHave(t, bad, "count check requires a valid op")
	mustHave(t, bad, `count check value "lots" must be numeric`)
	mustHave(t, bad, "count recursive must be a boolean")

	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  tmp-files: { type: count, path: /tmp, of: file, recursive: true, op: "<=", value: 100 }
`)
	if hasIssue(good, "count") {
		t.Fatalf("valid count check flagged: %v", good)
	}
}

func TestValidateResourceChecksAsServiceChecks(t *testing.T) {
	// Host-resource checks (storage/load/…) are usable in a service's checks: and
	// referenceable from rules, just like tcp/http/metric.
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
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

func TestValidateDiskMountIntegration(t *testing.T) {
	// A storage check carries space/inode predicates and/or a mounted condition in one
	// entry (no separate mount type) — including a mount-only storage check.
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  data: { type: storage, path: /data, used_pct: { op: ">=", value: 90 }, mounted: true }
  mountonly: { type: storage, path: /srv, mounted: true }
`)
	if hasIssue(good, "checks.data") || hasIssue(good, "checks.mountonly") {
		t.Fatalf("valid storage+mount checks flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  empty: { type: storage, path: /data }
  bad-mounted: { type: storage, path: /data, mounted: "yes" }
  old-mount-controls: { type: storage, path: /data, mounted: true, fstype: ext4, device: /dev/sdb1, options: [rw] }
`)
	mustHave(t, bad, "checks.empty requires a space/inode predicate")
	mustHave(t, bad, "checks.bad-mounted.mounted must be a boolean")
	mustHave(t, bad, "checks.old-mount-controls.fstype is not supported for a storage check")
	mustHave(t, bad, "checks.old-mount-controls.device is not supported for a storage check")
	mustHave(t, bad, "checks.old-mount-controls.options is not supported for a storage check")
}

func TestValidateResourceServiceCheckErrors(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  rootfs: { type: storage }
  sysload: { type: load }
`)
	mustHave(t, issues, "checks.rootfs.path is required for a storage check")
	mustHave(t, issues, "checks.sysload requires at least one of load1/load5/load15")
}

func TestValidateCheckInterval(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/health", interval: 30m }
`)
	if hasIssue(good, "interval") {
		t.Fatalf("a valid per-check interval was flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/health", interval: soon }
`)
	mustHave(t, bad, `checks.http.interval "soon" must be a valid positive duration`)
}

func TestValidateCertCheck(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  api: { type: cert, host: api.example.com, port: 443, expires_in_days: 14, on_algorithm_change: true, verify: true }
`)
	if hasIssue(good, "checks.api") {
		t.Fatalf("a valid cert check was flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  no-host: { type: cert }
  bad-days: { type: cert, host: x, expires_in_days: 0 }
  bad-port: { type: cert, host: x, port: 70000 }
  bad-bool: { type: cert, host: x, verify: "yes" }
`)
	mustHave(t, bad, "checks.no-host requires a host or a path")
	mustHave(t, bad, "checks.bad-days.expires_in_days must be a positive integer")
	mustHave(t, bad, "checks.bad-port.port must be an integer in 1..65535")
	mustHave(t, bad, "checks.bad-bool.verify must be a boolean")
}

func TestValidateHTTPFields(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
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
    expect_body: "ok"
`)
	if hasIssue(good, "checks.api") {
		t.Fatalf("a valid http check was flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  no-url: { type: http, method: POST }
  bad-headers: { type: http, url: "http://x", headers: "nope" }
  bad-json: { type: http, url: "http://x", expect_json: "nope" }
  bad-op: { type: http, url: "http://x", expect_json: { n: { op: "=>", value: 1 } } }
`)
	mustHave(t, bad, "checks.no-url.url is required for an http check")
	mustHave(t, bad, "checks.bad-headers.headers must be a mapping")
	mustHave(t, bad, "checks.bad-json.expect_json must be a mapping")
	mustHave(t, bad, "checks.bad-op.expect_json.n op \"=>\" is not one of")
}

func TestValidatePortsCheck(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  scan: { type: ports, host: 127.0.0.1, ports: "80,443,1024-4000", expect: open, match: any, on_change: true }
`)
	if hasIssue(good, "checks.scan") {
		t.Fatalf("a valid ports check was flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  no-ports:   { type: ports, host: x }
  bad-range:  { type: ports, ports: "100-50" }
  bad-port:   { type: ports, ports: "70000" }
  bad-expect: { type: ports, ports: "80", expect: weird }
  bad-match:  { type: ports, ports: "80", match: most }
`)
	mustHave(t, bad, "checks.no-ports.ports is required")
	mustHave(t, bad, `checks.bad-range.ports range "100-50" is out of 1..65535`)
	mustHave(t, bad, `checks.bad-port.ports range "70000" is out of 1..65535`)
	mustHave(t, bad, "checks.bad-expect.expect must be open, closed or any")
	mustHave(t, bad, "checks.bad-match.match must be all, any or none")
}

func TestValidateCheckGate(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  tcp:   { type: tcp, host: 127.0.0.1, port: 3306 }
  query: { type: command, command: ["/bin/true"], requires: [tcp], skip_when_changed: ["/etc/my.cnf"] }
`)
	if hasIssue(good, "checks.query") {
		t.Fatalf("a valid gated check was flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  tcp:   { type: tcp, host: 127.0.0.1, port: 3306 }
  self:  { type: tcp, host: 127.0.0.1, port: 80, requires: [self] }
  ghost: { type: tcp, host: 127.0.0.1, port: 80, requires: [missing] }
  badsk: { type: tcp, host: 127.0.0.1, port: 80, skip_when_changed: 5 }
`)
	mustHave(t, bad, "checks.self.requires cannot reference itself")
	mustHave(t, bad, `checks.ghost.requires references unknown check "missing"`)
	mustHave(t, bad, "checks.badsk.skip_when_changed must be a file path or a list")
}

func TestValidatePolicyMaxActions(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy:
  cooldown: 5m
  max_actions: 0
`)
	mustHave(t, issues, "max_actions must be an integer > 0")
}

func TestValidateDescriptionMustBeString(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
description: [not, a, string]
service: { name: x }
`)
	mustHave(t, issues, "description must be a string")
}

func TestValidateDescriptionStringPasses(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
description: "A friendly label"
service: { name: x }
`)
	for _, is := range issues {
		if strings.Contains(is.Msg, "description") {
			t.Fatalf("valid description wrongly flagged: %v", is)
		}
	}
}

func TestValidateCommandExpectExit(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
preflight:
  cfg: { type: command, command: ["check"], expect_exit: notanint }
  ok: { type: command, command: ["check"], expect_exit: 1 }
`)
	mustHave(t, issues, "expect_exit must be an integer")
	for _, is := range issues {
		if strings.Contains(is.Msg, "preflight.ok") {
			t.Fatalf("integer expect_exit wrongly flagged: %v", is)
		}
	}
}

func TestValidateCommands(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
commands:
  version: { command: "apachectl -v" }
  slow: { command: ["x"], timeout: nope }
  ok: { command: ["apachectl", "-v"], timeout: 5s }
`)
	mustHave(t, issues, "commands.version command must be an array, not a shell string")
	mustHave(t, issues, "commands.slow timeout")
	for _, is := range issues {
		if strings.Contains(is.Msg, "commands.ok") {
			t.Fatalf("valid command wrongly flagged: %v", is)
		}
	}
}

func TestValidateServiceField(t *testing.T) {
	// Per-init form: an unknown init key and an empty list are flagged.
	issues := validateService(t, `
kind: service
name: svc
service:
  upstart: [foo]
  systemd: []
`)
	mustHave(t, issues, `service key "upstart" is not one of systemd, openrc, name`)
	mustHave(t, issues, "service.systemd must be a non-empty list")

	// Mixing the legacy name with per-init lists is rejected.
	mixed := validateService(t, `
kind: service
name: svc
service:
  name: x
  systemd: [x]
`)
	mustHave(t, mixed, "service must not mix name with systemd/openrc")
}

func TestValidateProcessSelectorsRequireExeOrCmd(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
processes:
  main: { type: command_match, user: mysql }
  badcmd: { type: command_match, cmd: "(" }
  pid: { type: pidfile }
  bad: { type: by_name, name: mysqld }
`)
	mustHave(t, issues, "processes.main command_match requires exe or cmd")
	mustHave(t, issues, "processes.badcmd command_match cmd is not a valid regex")
	mustHave(t, issues, "processes.pid.path is required for a pidfile selector")
	mustHave(t, issues, `processes.bad.type "by_name" is not one of pidfile, command_match`)

	// exe-only and cmd-only selectors are now valid (user/group optional).
	ok := validateService(t, `
kind: service
name: svc
service: { name: x }
processes:
  worker: { type: command_match, exe: /usr/sbin/mysqld }
  unifi: { type: command_match, cmd: "java .*unifi", group: unifi }
`)
	for _, is := range ok {
		if hasIssue([]Issue{is}, "processes.worker") || hasIssue([]Issue{is}, "processes.unifi") {
			t.Fatalf("exe-only / cmd-only selectors must be valid: %v", ok)
		}
	}
}

func TestValidateCleanServicePasses(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
variables:
  host: 127.0.0.1
  port: 8080
checks:
  http: { type: http, url: "http://${host}:${port}/health", expect_status: 200 }
preflight:
  binary: { type: binary, path: /usr/sbin/x, optional: true }
stop_policy:
  force_kill: true
  kill_only_if:
    users: [www-data]
    exe_any: [/usr/sbin/x]
policy:
  cooldown: 5m
  max_actions: 3
  max_actions_window: 1h
rules:
  restart-if-down:
    type: remediation
    if:
      and:
        - failed: { check: http }
        - not: { active: { check: http } }
    for: { cycles: 3 }
    then: { action: restart }
  block-during-backup:
    type: guard
    blocks: [restart, stop]
    if: { file: { path: /run/backup/flag, exists: true } }
    then: { action: block, message: "backup running" }
  warn-cpu:
    type: alert
    if: { metric: { scope: service, name: cpu, op: ">", value: 80% } }
    then: { action: alert, message: "cpu high" }
`)
	if len(issues) != 0 {
		t.Fatalf("clean service should have no issues, got: %v", issues)
	}
}

func TestValidateServiceInterval(t *testing.T) {
	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
interval: notaduration
policy: { cooldown: 5m }
`)
	mustHave(t, bad, "interval")

	good := validateService(t, `
kind: service
name: svc
service: { name: x }
interval: 10s
policy: { cooldown: 5m }
`)
	if hasIssue(good, "interval") {
		t.Fatalf("valid service interval flagged: %v", good)
	}
}

func TestValidateCountCheckNestedThreshold(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  backlog: { type: count, path: /var/spool, count: { op: ">", value: 1000 } }
`)
	if hasIssue(good, "count") {
		t.Fatalf("nested count threshold flagged: %v", good)
	}

	mixed := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  backlog: { type: count, path: /var/spool, op: ">", value: 5, count: { op: ">", value: 1000 } }
`)
	mustHave(t, mixed, "count check must not mix a nested count {op, value} with top-level op/value")
}

func TestValidatePidfileCheckRequiresPath(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  pid: { type: pidfile }
`)
	mustHave(t, issues, "path is required for a pidfile check")
}

func TestValidatePercentBound(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  rootfs: { type: storage, path: /, used_pct: { op: ">=", value: "150%" } }
`)
	mustHave(t, issues, `used_pct value "150%" must be a percentage in 0..100`)
}

func TestValidateRuleWindowMinMatchesOptional(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
rule_window: { cycles: 5, mode: within }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`)
	if hasIssue(issues, "rule_window") {
		t.Fatalf("rule_window within without min_matches should default to 1, got %v", issues)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
rule_window: { cycles: 5, mode: within, min_matches: 0 }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`)
	mustHave(t, bad, "rule_window.min_matches must be > 0")
}

func TestValidateCertServerNameAndFileScope(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  bad-sni:  { type: cert, host: api.example.com, server_name: 443 }
  pem-file: { type: cert, path: /etc/ssl/api.pem, port: 443, server_name: api.example.com }
`)
	mustHave(t, issues, "server_name must be a string")
	mustHave(t, issues, "port does not apply to a PEM file path")
	mustHave(t, issues, "server_name does not apply to a PEM file path")

	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  api: { type: cert, host: 10.0.0.5, port: 8443, server_name: api.example.com, expires_in_days: 14 }
  pem: { type: cert, path: /etc/ssl/api.pem, expires_in_days: 14 }
`)
	if hasIssue(good, "cert") || hasIssue(good, "server_name") {
		t.Fatalf("valid cert checks flagged: %v", good)
	}
}

func TestValidateContainsOp(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  q: { type: sql, engine: sqlite, path: /var/db/x.db, query: "select status from t", op: contains, value: ok }
  redis: { type: redis, expect: { role: { op: contains, value: master } } }
`)
	if hasIssue(issues, "op") {
		t.Fatalf("contains should be a valid op, got %v", issues)
	}
}

func TestValidateScalarWindowRejected(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  bad:
    type: remediation
    if: { failed: { check: http } }
    for: 3
    then: { action: restart }
`)
	mustHave(t, issues, "for must be a mapping")
}

func TestValidateFileConditionExistsBoolean(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
rules:
  marker:
    type: alert
    if: { file: { path: /run/x.flag, exists: "false" } }
    then: { action: alert, message: "flag" }
`)
	mustHave(t, issues, "file.exists must be a boolean")

	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
rules:
  marker:
    type: alert
    if: { file: { path: /run/x.flag, exists: false } }
    then: { action: alert, message: "flag" }
`)
	if hasIssue(good, "file.exists") {
		t.Fatalf("boolean exists flagged: %v", good)
	}
}
