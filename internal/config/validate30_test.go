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
		"sermo.yml":        baseGlobal,
		"services/svc.yml": serviceYAML,
	})
	cfg, err := loadConfig(t, global)
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
  libs_interval: invalid
  max_parallel_checks: 0
  max_parallel_operations: -1
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(cfg)
	for _, want := range []string{
		"engine.interval",
		"engine.default_timeout",
		"engine.operation_timeout",
		"engine.libs_interval",
		"engine.max_parallel_checks",
		"engine.max_parallel_operations",
	} {
		mustHave(t, issues, want)
	}
}

func TestValidateBackoffDurations(t *testing.T) {
	// A garbage initial must not let a valid max slip through. A previous
	// implementation left the parsed initial at 0, so any max compared >= 0
	// and passed.
	badInitial := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: nonsense, max: 10s }
`)
	mustHave(t, badInitial, "policy.backoff.initial")

	// An omitted max reports its own parse error, not the misleading ">= initial".
	missingMax := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: 5s }
`)
	mustHave(t, missingMax, "policy.backoff.max must be a valid positive duration")

	// max < initial is still rejected with the ordering message.
	maxBelow := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: 30s, max: 5s }
`)
	mustHave(t, maxBelow, "policy.backoff.max must be >= initial")

	// A valid pair produces no backoff issue.
	ok := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: 5s, max: 1m }
`)
	if hasIssue(ok, "backoff") {
		t.Fatalf("valid backoff flagged: %v", ok)
	}
}

func TestValidateEngineOperationTimeoutAcceptsPositive(t *testing.T) {
	global := writeConfig(t, map[string]string{"sermo.yml": `
engine:
  operation_timeout: 90s
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range Validate(cfg) {
		if strings.Contains(is.Msg, "operation_timeout") {
			t.Fatalf("unexpected issue: %v", is)
		}
	}
}

func TestValidateEngineLogPaths(t *testing.T) {
	global := writeConfig(t, map[string]string{"sermo.yml": `
engine:
  access: relative.log
  events: /var/log/sermo/event.log
  diagnostics_interval: 1h
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "engine.access")
	mustHave(t, issues, "engine.diagnostics_interval")
}

func TestValidateEngineUserLookup(t *testing.T) {
	global := writeConfig(t, map[string]string{"sermo.yml": `
engine:
  user_lookup: ldap
  user_lookup_timeout: 0s
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "engine.user_lookup")
	mustHave(t, issues, "engine.user_lookup_timeout")
}

func TestValidateEngineUserLookupAcceptsDocumentedModes(t *testing.T) {
	for _, mode := range []string{"auto", "native", "getent", "numeric"} {
		t.Run(mode, func(t *testing.T) {
			global := writeConfig(t, map[string]string{"sermo.yml": `
engine:
  user_lookup: ` + mode + `
  user_lookup_timeout: 250ms
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`})
			cfg, err := loadConfig(t, global)
			if err != nil {
				t.Fatal(err)
			}
			for _, is := range Validate(cfg) {
				if strings.Contains(is.Msg, "user_lookup") {
					t.Fatalf("unexpected issue for %s: %v", mode, is)
				}
			}
		})
	}
}

func TestValidateLibvirtControl(t *testing.T) {
	valid := validateService(t, `
name: svc
control:
  type: libvirt
  domain: vm01
  uuid: 2b3f3d26-bb45-4b25-b65a-1e3ef86fc1a4
  socket: /run/libvirt/libvirt-sock
`)
	if hasIssue(valid, "control") {
		t.Fatalf("valid libvirt control got issues: %v", valid)
	}

	mustHave(t, validateService(t, `
name: svc
control: { type: libvirt }
`), "control.domain is required")
	mustHave(t, validateService(t, `
name: svc
control:
  type: libvirt
  domain: vm01
  uuid: nope
`), "control.uuid")
	mustHave(t, validateService(t, `
kind:
name: svc
control:
  type: libvirt
  domain: vm01
  socket: /run/libvirt/libvirt-sock
  host: 127.0.0.1
`), "must not set both socket and host")
}

func TestValidateDockerControl(t *testing.T) {
	valid := validateService(t, `
name: svc
control:
  type: docker
  container: web
  socket: /run/docker.sock
`)
	if hasIssue(valid, "control") {
		t.Fatalf("valid docker control got issues: %v", valid)
	}

	validTCP := validateService(t, `
name: svc
control:
  type: docker
  container: web
  host: 127.0.0.1
  port: 2376
  tls: skip-verify
`)
	if hasIssue(validTCP, "control") {
		t.Fatalf("valid docker TCP control got issues: %v", validTCP)
	}

	mustHave(t, validateService(t, `
name: svc
control: { type: docker }
`), "control.container is required")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  socket: docker.sock
`), "control.socket")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  socket: /run/docker.sock
  host: 127.0.0.1
`), "must not set both socket and host")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  host: 127.0.0.1
  port: 70000
`), "control.port")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  tls: maybe
`), "control.tls")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  interface: eth0
`), "control key \"interface\"")
}

func TestValidateRuleStructure(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
  guard-bad-blocks:
    type: guard
    blocks: [restart, 7]
    if: { failed: { check: http } }
    then: { action: block, message: "x" }
`)
	mustHave(t, issues, "then.action \"explode\" is not one of")
	mustHave(t, issues, "guard requires a non-empty blocks list")
	mustHave(t, issues, "only guard rules may set blocks")
	mustHave(t, issues, "action block requires a non-empty message")
	mustHave(t, issues, "blocks must be a string or list of strings")
}

func TestValidateMultiAction(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
name: svc
service: x
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  both:
    type: remediation
    if: { failed: { check: http } }
    for: { cycles: 0 }
    within: { cycles: 5, min_matches: 9 }
    then: { action: restart }
  bad-for-key:
    type: remediation
    if: { failed: { check: http } }
    for: { cycles: 3, unexpected: true }
    then: { action: restart }
  bad-within-key:
    type: remediation
    if: { failed: { check: http } }
    within: { cycles: 5, min_matches: 2, unexpected: true }
    then: { action: restart }
  both-for-lengths:
    type: remediation
    if: { failed: { check: http } }
    for: { cycles: 3, duration: 6m }
    then: { action: restart }
  bad-duration:
    type: remediation
    if: { failed: { check: http } }
    within: { duration: nope, min_matches: 2 }
    then: { action: restart }
`)
	mustHave(t, issues, "cannot define both for and within")
	mustHave(t, issues, "for.cycles must be > 0")
	mustHave(t, issues, "within.min_matches must be <= within.cycles")
	mustHave(t, issues, "rules.bad-for-key.for.unexpected is not supported")
	mustHave(t, issues, "rules.bad-within-key.within.unexpected is not supported")
	mustHave(t, issues, "rules.both-for-lengths.for cannot define both cycles and duration")
	mustHave(t, issues, "rules.bad-duration.within.duration must be a valid positive duration")
}

func TestValidateRuleDurationWindows(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  restart-after-duration:
    type: remediation
    if: { failed: { check: http } }
    for: { duration: 6m }
    then: { action: restart }
  alert-within-duration:
    type: alert
    if: { failed: { check: http } }
    within: { duration: 30m, min_matches: 3 }
    then: { action: alert, message: "http down" }
`)
	if hasIssue(issues, "duration") || hasIssue(issues, "rules.restart-after-duration") || hasIssue(issues, "rules.alert-within-duration") {
		t.Fatalf("valid duration windows flagged: %v", issues)
	}
}

func TestValidateUnknownCheckReference(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
name: svc
service: x
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
name: svc
service: x
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

func TestValidateInlineCommandConditionUser(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
rules:
  r:
    type: remediation
    if:
      command:
        command: ["can-restart"]
        user: []
        timeout: 5s
    then: { action: restart }
`)
	mustHave(t, issues, "rules.r.if.command user must be a non-empty string")
}

func TestValidateInlineProbeFields(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
rules:
  bad-http:
    type: alert
    if: { failed: { http: {} } }
    then: { action: alert, message: m }
  bad-shape:
    type: alert
    if: { active: { tcp: "127.0.0.1:80" } }
    then: { action: alert, message: m }
  ok-http:
    type: alert
    if: { failed: { http: { url: "http://127.0.0.1/" } } }
    then: { action: alert, message: m }
`)
	mustHave(t, issues, "rules.bad-http.if.failed.http.url is required for an http check")
	mustHave(t, issues, "rules.bad-shape.if.active.tcp must be a mapping")
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-http") {
			t.Fatalf("valid inline http probe wrongly flagged: %v", is)
		}
	}
}

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
		if hasIssue(check(ok), "expect_status") {
			t.Fatalf("valid expect_status %q wrongly flagged", ok)
		}
	}
	// Invalid scalar and invalid list element are caught via the scalar walk.
	mustHave(t, check(`999nope`), "expect_status")
	mustHave(t, check(`[200, bogus]`), "expect_status")
}

func TestValidateInlineProbeConnectionProtocols(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
rules:
  ok-mysql:
    type: alert
    if: { failed: { mysql: { port: 3306 } } }
    then: { action: alert, message: m }
  bad-mysql:
    type: alert
    if: { failed: { mysql: { port: 70000 } } }
    then: { action: alert, message: m }
`)
	mustHave(t, issues, `rules.bad-mysql.if.failed.mysql.port "70000" must be an integer in 1..65535`)
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-mysql") {
			t.Fatalf("valid inline mysql probe wrongly flagged: %v", is)
		}
	}
}

func TestValidateSystemMetricOnlyInAlert(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
rules:
  bad:
    type: remediation
    if: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } }
    then: { action: restart }
  bad-inline:
    type: remediation
    if: { failed: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } } }
    then: { action: restart }
  ok-alert:
    type: alert
    if: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } }
    then: { action: alert, message: "machine hot" }
  ok-alert-inline:
    type: alert
    if: { failed: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } } }
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
name: svc
service: x
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
name: svc
service: x
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
name: svc
service: x
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
name: svc
service: x
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

func TestValidateSystemTotalSwapMetric(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
rules:
  swap-alert:
    type: alert
    if: { metric: { scope: system, name: total_swap, op: ">", value: 80% } }
    then: { action: alert, message: "swap high" }
`)
	if len(good) != 0 {
		t.Fatalf("total_swap should be in the system metric catalog, got %v", good)
	}
}

func TestValidateStopPolicyKillSelector(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
stop_policy:
  force_kill: true
  kill_only_if:
    users: [mysql]
`)
	mustHave(t, issues, "kill_only_if must define both users and exe_any")

	invalidList := validateService(t, `
name: svc
service: x
stop_policy:
  kill_only_if:
    users: [mysql, 7]
    exe_any: [/usr/sbin/mysqld]
`)
	mustHave(t, invalidList, "kill_only_if must define both users and exe_any")
}

func TestValidateForceKillRequiresSelector(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
stop_policy:
  force_kill: true
`)
	mustHave(t, issues, "force_kill=true requires kill_only_if")
}

func TestValidateStopPolicyDurations(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
stop_policy:
  graceful_timeout: nope
  term_timeout: 0s
  kill_timeout: -1s
`)
	mustHave(t, issues, `stop_policy.graceful_timeout "nope" must be a valid positive duration`)
	mustHave(t, issues, `stop_policy.term_timeout "0s" must be a valid positive duration`)
	mustHave(t, issues, `stop_policy.kill_timeout "-1s" must be a valid positive duration`)
}

func TestValidateStopPolicyFilesAbsentStrictList(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
stop_policy:
  files_absent: [/run/svc.sock, 7]
`)
	mustHave(t, issues, "stop_policy.files_absent must be a non-empty list of paths/globs")
}

func TestValidateCheckEntrySchemas(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
checks:
  cmd: { type: command, command: "echo hi" }
  svc-missing: { type: service }
  svc-state: { type: service, expect: bogus }
  proc-user-only: { type: process, user: mysql }
  proc-ambiguous: { type: process, exe: /x, exe_any: [/y] }
  proc-bad-exe-any: { type: process, exe_any: [/y, 7] }
  proc: { type: process, exe: /x, state: weird }
  opt: { type: binary, path: /x, optional: "yes" }
  timeout: { type: binary, path: /x, timeout: slow }
preflight:
  config-path: { type: config, path: [7] }
  config-change: { type: config, path: /etc/app.conf, on_change: "yes" }
  lockfile: { type: file_exists, path: /run/sermo/locks/x.lock }
  owned-lockfile: { type: lockfile, path: /run/sermo/locks/service.lock }
`)
	mustHave(t, issues, "command must be an array, not a shell string")
	mustHave(t, issues, "expect is required for a service check")
	mustHave(t, issues, `expect "bogus" is not one of`)
	mustHave(t, issues, "exe or exe_any is required for a process check")
	mustHave(t, issues, "must define only one of exe or exe_any")
	mustHave(t, issues, "exe_any must be a string or non-empty list of strings")
	mustHave(t, issues, `state "weird" is not one of`)
	mustHave(t, issues, "optional must be a boolean")
	mustHave(t, issues, `checks.timeout.timeout "slow" must be a valid positive duration`)
	mustHave(t, issues, "config-path.path must be a string or non-empty list of strings")
	mustHave(t, issues, "config-change.on_change must be a boolean")
	mustHave(t, issues, "must not point under the runtime lock dir")
	mustHave(t, issues, "owned-lockfile lockfile must not point under the runtime lock dir")
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
	if hasIssue(good, "count") {
		t.Fatalf("valid count check flagged: %v", good)
	}
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

func TestValidateStorageMountIntegration(t *testing.T) {
	// A storage check carries space/inode predicates and/or a mounted condition in one
	// entry (no separate mount type) — including a mount-only storage check.
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  data: { type: storage, path: /data, used_pct: { op: ">=", value: 90 }, mounted: true }
  mountonly: { type: storage, path: /srv, mounted: true }
`)
	if hasIssue(good, "checks.data") || hasIssue(good, "checks.mountonly") {
		t.Fatalf("valid storage+mount checks flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  empty: { type: storage, path: /data }
  bad-mounted: { type: storage, path: /data, mounted: "yes" }
  unsupported-mount-controls: { type: storage, path: /data, mounted: true, fstype: ext4, device: /dev/sdb1, options: [rw] }
`)
	mustHave(t, bad, "checks.empty requires a space/inode predicate")
	mustHave(t, bad, "checks.bad-mounted.mounted must be a boolean")
	mustHave(t, bad, "checks.unsupported-mount-controls.fstype is not supported for a storage check")
	mustHave(t, bad, "checks.unsupported-mount-controls.device is not supported for a storage check")
	mustHave(t, bad, "checks.unsupported-mount-controls.options is not supported for a storage check")
}

func TestValidateResourceServiceCheckErrors(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
name: svc
service: x
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/health", interval: 30m }
`)
	if hasIssue(good, "interval") {
		t.Fatalf("a valid per-check interval was flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/health", interval: soon }
`)
	mustHave(t, bad, `checks.http.interval "soon" must be a valid positive duration`)
}

func TestValidateCertCheck(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api: { type: cert, host: api.example.com, port: 443, expires_in_days: 14, on_algorithm_change: true, cert_verify: true }
`)
	if hasIssue(good, "checks.api") {
		t.Fatalf("a valid cert check was flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-host: { type: cert }
  bad-days: { type: cert, host: x, expires_in_days: 0 }
  bad-port: { type: cert, host: x, port: 70000 }
  bad-bool: { type: cert, host: x, cert_verify: "yes" }
`)
	mustHave(t, bad, "checks.no-host requires a host or a path")
	mustHave(t, bad, "checks.bad-days.expires_in_days must be a positive integer")
	mustHave(t, bad, "checks.bad-port.port must be an integer in 1..65535")
	mustHave(t, bad, "checks.bad-bool.cert_verify must be a boolean")
}

func TestValidateVerifyFlag(t *testing.T) {
	// verify: true on a health check (http) is valid.
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/", verify: true }
`)
	if hasIssue(good, "checks.http") {
		t.Fatalf("verify:true on a health check was flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  cond: { type: memory, used_pct: { op: ">", value: 90 }, verify: true }
  notbool: { type: http, url: "http://x/", verify: "yes" }
`)
	mustHave(t, bad, "checks.cond.verify is only valid on a health check")
	mustHave(t, bad, "checks.notbool.verify must be a boolean")
}

func TestValidateHTTPFields(t *testing.T) {
	good := validateService(t, `
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
`)
	if hasIssue(good, "checks.api") {
		t.Fatalf("a valid http check was flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
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
name: svc
service: x
policy: { cooldown: 5m }
checks:
  scan: { type: ports, host: 127.0.0.1, ports: "80,443,1024-4000", expect: open, match: any, on_change: true, connect_timeout: 500ms }
`)
	if hasIssue(good, "checks.scan") {
		t.Fatalf("a valid ports check was flagged: %v", good)
	}

	bad := validateService(t, `
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
`)
	mustHave(t, bad, "checks.no-ports.ports is required")
	mustHave(t, bad, `checks.bad-range.ports range "100-50" is out of 1..65535`)
	mustHave(t, bad, `checks.bad-port.ports range "70000" is out of 1..65535`)
	mustHave(t, bad, "checks.too-many.ports too many ports")
	mustHave(t, bad, "checks.bad-expect.expect must be open, closed or any")
	mustHave(t, bad, "checks.bad-match.match must be all, any or none")
	mustHave(t, bad, "checks.bad-timeout.connect_timeout must be a valid positive duration")
}

func TestValidateCheckGate(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  tcp:   { type: tcp, host: 127.0.0.1, port: 3306 }
  query: { type: command, command: ["/bin/true"], requires: [tcp], skip_when_changed: ["/etc/my.cnf"] }
`)
	if hasIssue(good, "checks.query") {
		t.Fatalf("a valid gated check was flagged: %v", good)
	}

	bad := validateService(t, `
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
`)
	mustHave(t, bad, "checks.self.requires cannot reference itself")
	mustHave(t, bad, `checks.ghost.requires references unknown check "missing"`)
	mustHave(t, bad, "checks.badsk.skip_when_changed must be a file path or a list")
	mustHave(t, bad, "checks.badreqlist.requires must be a check name or a list of check names")
	mustHave(t, bad, "checks.badsklist.skip_when_changed must be a file path or a list")
}

func TestValidatePolicyMaxActions(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy:
  cooldown: 5m
  max_actions: 0
`)
	mustHave(t, issues, "max_actions must be an integer > 0")
}

func TestValidateDescriptionMustBeString(t *testing.T) {
	issues := validateService(t, `
name: svc
description: [not, a, string]
service: x
`)
	mustHave(t, issues, "description must be a string")
}

func TestValidateDescriptionStringPasses(t *testing.T) {
	issues := validateService(t, `
name: svc
description: "A friendly label"
service: x
`)
	for _, is := range issues {
		if strings.Contains(is.Msg, "description") {
			t.Fatalf("valid description wrongly flagged: %v", is)
		}
	}
}

func TestValidateCategoryMustBeString(t *testing.T) {
	issues := validateService(t, `
name: svc
category: [not, a, string]
service: x
`)
	mustHave(t, issues, "category must be a string")
}

func TestValidateCategoryStringPasses(t *testing.T) {
	issues := validateService(t, `
name: svc
category: database
service: x
`)
	for _, is := range issues {
		if strings.Contains(is.Msg, "category") {
			t.Fatalf("valid category wrongly flagged: %v", is)
		}
	}
}

func TestValidateAppVersionFrom(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/consumer.yml": `
name: consumer
variables:
  binary: /usr/bin/consumer
version_from: provider
`,
		"catalog/apps/provider.yml": `
name: provider
variables:
  binary: /usr/bin/provider
preflight:
  version: { type: command, command: ["/usr/bin/provider", "--version"] }
`,
		"catalog/apps/missing.yml": `
name: missing
variables:
  binary: /usr/bin/missing
version_from: ghost
`,
		"catalog/apps/self.yml": `
name: self
variables:
  binary: /usr/bin/self
version_from: self
`,
		"catalog/apps/a.yml": `
name: a
variables:
  binary: /usr/bin/a
version_from: b
`,
		"catalog/apps/b.yml": `
name: b
variables:
  binary: /usr/bin/b
version_from: a
`,
		"catalog/apps/bad-name.yml": `
name: bad-name
variables:
  binary: /usr/bin/bad-name
version_from: ../provider
`,
		"catalog/apps/bad-type.yml": `
name: bad-type
variables:
  binary: /usr/bin/bad-type
version_from: [provider]
`,
		"catalog/services/not-app.yml": `
name: not-app
version_from: provider
service: not-app
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	for _, issue := range issues {
		if issue.Scope == "app consumer" {
			t.Fatalf("valid version_from flagged: %v", issues)
		}
	}
	mustHave(t, issues, `version_from references unknown app "ghost"`)
	mustHave(t, issues, "version_from must not reference itself")
	mustHave(t, issues, "version_from cycle detected")
	mustHave(t, issues, `version_from "../provider" must be a simple name`)
	mustHave(t, issues, "version_from must be a non-empty app name")
	mustHave(t, issues, "version_from is only supported on app catalog documents")
}

func TestValidateAppVersionMatch(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/mysql.yml": `
name: mysql
variables:
  binary: /usr/sbin/mysqld
version_match: { excludes: MariaDB }
preflight:
  version: { type: command, command: ["/usr/sbin/mysqld", "--version"] }
`,
		"catalog/apps/bad-shape.yml": `
name: bad-shape
variables:
  binary: /usr/bin/bad-shape
version_match: MariaDB
preflight:
  version: { type: command, command: ["/usr/bin/bad-shape", "--version"] }
`,
		"catalog/apps/bad-key.yml": `
name: bad-key
variables:
  binary: /usr/bin/bad-key
version_match: { rejects: MariaDB }
preflight:
  version: { type: command, command: ["/usr/bin/bad-key", "--version"] }
`,
		"catalog/apps/bad-regex.yml": `
name: bad-regex
variables:
  binary: /usr/bin/bad-regex
version_match: { regex: "[" }
preflight:
  version: { type: command, command: ["/usr/bin/bad-regex", "--version"] }
`,
		"catalog/apps/no-version.yml": `
name: no-version
variables:
  binary: /usr/bin/no-version
version_match: { contains: Demo }
`,
		"catalog/services/not-app.yml": `
name: not-app
version_match: { contains: Demo }
service: not-app
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	for _, issue := range issues {
		if issue.Scope == "app mysql" {
			t.Fatalf("valid version_match flagged: %v", issues)
		}
	}
	mustHave(t, issues, "version_match must be a mapping")
	mustHave(t, issues, `version_match unknown key "rejects"`)
	mustHave(t, issues, "version_match regex")
	mustHave(t, issues, "version_match requires a version command")
	mustHave(t, issues, "version_match is only supported on app catalog documents")
}

func TestValidateCommandExpectExit(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
preflight:
  cfg: { type: command, command: ["check"], expect_exit: notanint }
  ok: { type: command, command: ["check"], expect_exit: 1 }
  ok-list: { type: command, command: ["check"], expect_exit: [0, 1] }
  bad-list: { type: command, command: ["check"], expect_exit: [0, nope] }
`)
	mustHave(t, issues, "expect_exit must be an integer")
	for _, is := range issues {
		if strings.Contains(is.Msg, "preflight.ok ") || strings.Contains(is.Msg, "preflight.ok-list") {
			t.Fatalf("valid expect_exit wrongly flagged: %v", is)
		}
	}
}

func TestValidateCommands(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
name: svc
service:
  upstart: [foo]
  systemd: []
  openrc: [svc, 7]
`)
	mustHave(t, issues, `service key "upstart" is not one of systemd, openrc`)
	mustHave(t, issues, "service.systemd must be a non-empty list")
	mustHave(t, issues, "service.openrc must be a non-empty list")

}

func TestValidateProcessSelectorsRequireExeOrCmd(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
processes:
  main: { user: mysql }
  badcmd: { cmd: "(" }
`)
	mustHave(t, issues, "processes.main requires exe or cmd")
	mustHave(t, issues, "processes.badcmd.cmd is not a valid regex")

	// exe-only and cmd-only selectors are now valid (user/group optional).
	ok := validateService(t, `
name: svc
service: x
processes:
  worker: { exe: /usr/sbin/mysqld }
  unifi: { cmd: "java .*unifi", group: unifi }
`)
	for _, is := range ok {
		if hasIssue([]Issue{is}, "processes.worker") || hasIssue([]Issue{is}, "processes.unifi") {
			t.Fatalf("exe-only / cmd-only selectors must be valid: %v", ok)
		}
	}
}

func TestValidateProcessSelectorsRejectUnknownKeys(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
processes:
  main: { exe: /usr/sbin/mysqld, extra: value }
  worker: { cmd: "worker", unexpected: true }
`)
	mustHave(t, issues, "processes.main.extra is not supported")
	mustHave(t, issues, "processes.worker.unexpected is not supported")
}

func TestValidateEnableIfIsLimitedAndDisabledBranchesStillValidate(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
checks:
  service: { type: service, expect: active }
processes:
  optional:
    cmd: "("
    enable_if: { file: /no/such/conf, key: daemon_list, contains: optional }
rules:
  guarded:
    type: guard
    enable_if: { file: /etc/conf.d/svc, key: daemon_list, contains: guarded }
    blocks: [restart]
    if: { failed: { check: service } }
    then: { action: block, message: "blocked" }
`)
	mustHave(t, issues, "processes.optional.cmd is not a valid regex")
	mustHave(t, issues, "rules.guarded.enable_if is only supported")
}

func TestValidateEnableIfSpec(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
checks:
  bad:
    type: binary
    path: /bin/true
    enable_if:
      file: relative.conf
      key: daemon_list
      contains: ""
      matches: "["
      extra: true
`)
	mustHave(t, issues, `checks.bad.enable_if.file "relative.conf" must be absolute`)
	mustHave(t, issues, "checks.bad.enable_if.contains must be non-empty")
	mustHave(t, issues, "checks.bad.enable_if.matches is not a valid regex")
	mustHave(t, issues, "checks.bad.enable_if must define exactly one")
	mustHave(t, issues, "checks.bad.enable_if.extra is not supported")
}

func TestValidateFromFileVariableSpecs(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
variables:
  no-default: { from_file: /etc/svc.conf, directive: port }
  no-reader: { from_file: /etc/svc.conf, default: 1194 }
  both: { from_file: /etc/svc.conf, directive: port, pattern: 'port (\d+)', default: 1194 }
  bad-pattern: { from_file: /etc/svc.conf, pattern: '(', default: 1194 }
  no-capture: { from_file: /etc/svc.conf, pattern: 'port \d+', default: 1194 }
  extra: { from_file: /etc/svc.conf, directive: port, default: 1194, unexpected: true }
  empty-path: { from_file: "", directive: port, default: 1194 }
checks:
  service: { type: service, expect: active }
`)
	mustHave(t, issues, "variables.no-default.default is required")
	mustHave(t, issues, "variables.no-reader must define exactly one of directive or pattern")
	mustHave(t, issues, "variables.both must define exactly one of directive or pattern")
	mustHave(t, issues, "variables.bad-pattern.pattern is not a valid regex")
	mustHave(t, issues, "variables.no-capture.pattern must define at least one capture group")
	mustHave(t, issues, "variables.extra.unexpected is not supported")
	mustHave(t, issues, "variables.empty-path.from_file is required")
}

func TestValidateVersionsCurrentFromSpecs(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/java.yml": `
name: java-%i-%v
versions:
  current_from:
    - ""
    - { path: /usr/lib/jvm/static/bin/java }
    - 42
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "versions.current_from[0] must be a non-empty path string")
	mustHave(t, issues, "versions.current_from[1] must be a path string or list of path strings")
	mustHave(t, issues, "versions.current_from[2] must be a path string or list of path strings")
}

func TestValidateVersionsFromInitBranches(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc%v
versions:
  from:
    default: /etc/svc-${version}
    systemd:
      - /usr/lib/systemd/system/svc@${version}.service
      - ""
    openrc: 42
    launchd: /Library/LaunchDaemons/svc-${version}.plist
checks:
  service: { type: service, expect: active }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "versions.from.default is not supported; use systemd or openrc")
	mustHave(t, issues, "versions.from.systemd[1] must be a non-empty path string")
	mustHave(t, issues, "versions.from.openrc must be a path string or list of path strings")
	mustHave(t, issues, "versions.from.launchd is not supported; use systemd or openrc")
}

func TestValidateFromFileVariablePathReferences(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
variables:
  port: { from_file: "${missing_config}", directive: port, default: 1194 }
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: "${port}" }
`)
	mustHave(t, issues, "variable ${missing_config} used in variables.port.from_file but not defined")
}

func TestValidateBinaryVariableStrictList(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
variables:
  binary: [/usr/sbin/svc, 7]
`)
	mustHave(t, issues, "variables.binary must be a non-empty path string or list")
}

func TestValidateCleanServicePasses(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
name: svc
service: x
interval: notaduration
policy: { cooldown: 5m }
`)
	mustHave(t, bad, "interval")

	good := validateService(t, `
name: svc
service: x
interval: 10s
policy: { cooldown: 5m }
`)
	if hasIssue(good, "interval") {
		t.Fatalf("valid service interval flagged: %v", good)
	}
}

func TestValidateCountCheckNestedThreshold(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  backlog: { type: count, path: /var/spool, count: { op: ">", value: 1000 } }
`)
	if hasIssue(good, "count") {
		t.Fatalf("nested count threshold flagged: %v", good)
	}

	mixed := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  backlog: { type: count, path: /var/spool, op: ">", value: 5, count: { op: ">", value: 1000 } }
`)
	mustHave(t, mixed, "count check must not mix a nested count {op, value} with top-level op/value")
}

func TestValidatePidfileCheckRequiresPath(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  pid: { type: pidfile }
`)
	mustHave(t, issues, "path is required for a pidfile check")

	invalidList := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  pid: { type: pidfile, path: [/run/svc.pid, 7] }
`)
	mustHave(t, invalidList, "path is required for a pidfile check")
}

func TestValidateSocketCheckRequiresPath(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  sock: { type: socket }
`)
	mustHave(t, issues, "path is required for a socket check")

	invalidList := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  sock: { type: socket, path: [/run/svc.sock, 7] }
`)
	mustHave(t, invalidList, "path is required for a socket check")
}

func TestValidateLockfileCheckRequiresPath(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  lock: { type: lockfile }
`)
	mustHave(t, issues, "path is required for a lockfile check")

	invalidList := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  lock: { type: lockfile, path: [/run/svc.lock, 7] }
`)
	mustHave(t, invalidList, "path is required for a lockfile check")
}

func TestValidatePercentBound(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  rootfs: { type: storage, path: /, used_pct: { op: ">=", value: "150%" } }
`)
	mustHave(t, issues, `used_pct value "150%" must be a percentage in 0..100`)
}

func TestValidateRuleWindowMinMatchesOptional(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { cycles: 5, mode: within }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`)
	if hasIssue(issues, "rule_window") {
		t.Fatalf("rule_window within without min_matches should default to 1, got %v", issues)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { cycles: 5, mode: within, min_matches: 0 }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`)
	mustHave(t, bad, "rule_window.min_matches must be > 0")
}

func TestValidateRuleWindowDuration(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { duration: 6m, mode: consecutive }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`)
	if hasIssue(issues, "rule_window") {
		t.Fatalf("valid duration rule_window flagged: %v", issues)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { cycles: 3, duration: 6m }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`)
	mustHave(t, bad, "rule_window cannot define both cycles and duration")
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
	if hasIssue(good, "cert") || hasIssue(good, "server_name") {
		t.Fatalf("valid cert checks flagged: %v", good)
	}
}

func TestValidateContainsOp(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
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
name: svc
service: x
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
name: svc
service: x
policy: { cooldown: 5m }
rules:
  marker:
    type: alert
    if: { file: { path: /run/x.flag, exists: "false" } }
    then: { action: alert, message: "flag" }
`)
	mustHave(t, issues, "file.exists must be a boolean")

	good := validateService(t, `
name: svc
service: x
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

func TestValidateMemoryCheckBothSurfaces(t *testing.T) {
	// In a service's checks: (unified check types — same validator as watches).
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  ram: { type: memory, used_pct: { op: ">=", value: "90%" } }
`)
	if hasIssue(good, "memory") || hasIssue(good, "ram") {
		t.Fatalf("valid memory check flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-pred:  { type: memory }
  bad-size: { type: memory, available_bytes: { op: "<", value: 1024 } }
`)
	mustHave(t, bad, "checks.no-pred requires at least one of used_pct/available_pct/available_bytes")
	mustHave(t, bad, `available_bytes value "1024" must include a size suffix`)
}

func TestValidatePressureCheck(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  mem-stall: { type: pressure, resource: memory, some_avg10: { op: ">", value: 10 } }
`)
	if hasIssue(good, "pressure") || hasIssue(good, "mem-stall") {
		t.Fatalf("valid pressure check flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  bad-res: { type: pressure, resource: disk, some_avg10: { op: ">", value: 10 } }
  no-pred: { type: pressure, resource: cpu }
`)
	mustHave(t, bad, "checks.bad-res.resource must be cpu, memory or io")
	mustHave(t, bad, "checks.no-pred requires at least one of some_avg10/some_avg60/some_avg300/full_avg10/full_avg60/full_avg300")
}

func TestValidatePidsCheck(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  pid-table: { type: pids, used_pct: { op: ">=", value: "90%" } }
`)
	if hasIssue(good, "pid-table") {
		t.Fatalf("valid pids check flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-pred: { type: pids }
`)
	mustHave(t, bad, "checks.no-pred requires at least one of used_pct/free/count")
}

func TestValidateDiskIOCheck(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  db-disk: { type: diskio, device: nvme0n1, util_pct: { op: ">=", value: "90%" }, write_bytes: { op: ">", value: 50M } }
`)
	if hasIssue(good, "db-disk") {
		t.Fatalf("valid diskio check flagged: %v", good)
	}

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-dev:  { type: diskio, util_pct: { op: ">=", value: 90 } }
  no-pred: { type: diskio, device: sda }
  raw-bps: { type: diskio, device: sda, read_bytes: { op: ">", value: 1048576 } }
`)
	mustHave(t, bad, "checks.no-dev.device is required for a diskio check")
	mustHave(t, bad, "checks.no-pred requires at least one of util_pct/read_bytes/write_bytes/await_ms")
	mustHave(t, bad, `read_bytes value "1048576" must include a size suffix`)
}

func TestValidateCleanOnStopDotDotEscape(t *testing.T) {
	// ".." segments must not sidestep the protected-dir check.
	issues := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
stop_policy:
  clean_on_stop:
    - { path: /var/cache/myapp/../.., recursive: true }
`)
	mustHave(t, issues, "refuses to recursively delete")
}

// TestValidateRuleTypeActionCoupling sharpens the rule-type distinction:
// operation actions belong to remediation rules only, and a remediation rule
// must carry one — an alert-only remediation (or an alert rule with a restart)
// would otherwise validate and then silently not do what it reads like.
func TestValidateRuleTypeActionCoupling(t *testing.T) {
	rule := func(rtype, then string) string {
		return "name: svc\nservice: x\nchecks:\n  c: { type: tcp, host: 127.0.0.1, port: 80 }\nrules:\n  r:\n    type: " + rtype + "\n    if: { failed: { check: c } }\n" + then
	}
	mustHave(t, validateService(t, rule("remediation", "    then: { action: alert, message: m }\n")),
		"remediation requires an operation action")
	mustHave(t, validateService(t, rule("alert", "    then: { action: restart }\n")),
		"only remediation rules may use action restart")
	mustHave(t, validateService(t, rule("guard", "    blocks: [restart]\n    then:\n      actions: [ { type: block, message: m }, { type: stop } ]\n")),
		"only remediation rules may use action stop")
	if issues := validateService(t, rule("remediation", "    then: { action: reload }\n")); hasIssue(issues, "rules.r") {
		t.Fatalf("a reload remediation must be valid, got %v", issues)
	}
	if issues := validateService(t, rule("remediation", "    then: { action: resume }\n")); hasIssue(issues, "rules.r") {
		t.Fatalf("a resume remediation must be valid, got %v", issues)
	}
}
