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
  max_parallel_checks: 0
paths:
  enabled: [ @ROOT@/enabled ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(cfg)
	for _, want := range []string{"engine.interval", "engine.default_timeout", "engine.max_parallel_checks"} {
		mustHave(t, issues, want)
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
	// Host-resource checks (disk/load/…) are usable in a service's checks: and
	// referenceable from rules, just like tcp/http/metric.
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  rootfs: { type: disk, path: /, used_pct: { op: ">=", value: 90 } }
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

func TestValidateResourceServiceCheckErrors(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  rootfs: { type: disk }
  sysload: { type: load }
`)
	mustHave(t, issues, "checks.rootfs.path is required for a disk check")
	mustHave(t, issues, "checks.sysload requires at least one of load1/load5/load15")
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

func TestValidateAliases(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
aliases:
  upstart: [foo]
  openrc: []
`)
	mustHave(t, issues, `aliases key "upstart" is not a valid backend`)
	mustHave(t, issues, "aliases.openrc must be a non-empty list")
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
