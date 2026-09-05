package config

import (
	"testing"
)

func TestValidatePidfilesRequireMatchingProcessIdentity(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfile: /run/legacy.pid
pidfiles:
  missing: /run/missing.pid
  relative: run/relative.pid
  no-exe: /run/no-exe.pid
  no-user: /run/no-user.pid
processes:
  no-exe:
    user: svc
    cmd: svc --no-exe
  no-user:
    exe: /usr/sbin/no-user
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	for _, want := range []string{
		"pidfile and pidfiles are mutually exclusive",
		"pidfiles.missing requires matching processes.missing",
		"pidfiles.no-exe requires processes.no-exe.exe",
		"pidfiles.no-user requires processes.no-user.user",
		`pidfiles.relative path "run/relative.pid" must be absolute`,
	} {
		if !hasIssue(issues, want) {
			t.Errorf("missing issue containing %q in %v", want, issues)
		}
	}
}

func TestValidateAlsoServiceErrors(t *testing.T) {
	mustHave(t, validateService(t, `
name: s
service: { systemd: [docker] }
also_service: { systemd: [docker] }
`), "primary service unit")

	mustHave(t, validateService(t, `
name: s
service: { systemd: [docker] }
also_service: { foo: [x] }
`), "not one of systemd, openrc")

	mustHave(t, validateService(t, `
name: s
service: { systemd: [docker] }
also_service: { systemd: [docker.socket, 7] }
`), "also_service.systemd must be a non-empty list")
}

// Service-document validation — rules, conditions, windows, inline probes and metrics.
func TestServiceValidationRules(t *testing.T) {
	runServiceIssueCases(t, []serviceIssueCase{
		{
			name: "rule structure",
			service: `
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
`,
			want: []string{
				"then.action \"explode\" is not one of",
				"guard requires a non-empty blocks list",
				"only guard rules may set blocks",
				"action block requires a non-empty message",
				"blocks must be a string or list of strings",
			},
		},
		{
			name: "rule windows",
			service: `
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
`,
			want: []string{
				"cannot define both for and within",
				"for.cycles must be > 0",
				"within.min_matches must be <= within.cycles",
				"rules.bad-for-key.for.unexpected is not supported",
				"rules.bad-within-key.within.unexpected is not supported",
				"rules.both-for-lengths.for cannot define both cycles and duration",
				"rules.bad-duration.within.duration must be a valid positive duration",
			},
		},
		{
			name: "rule clear windows",
			service: `
name: svc
service: x
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  ok-clear:
    type: alert
    if: { failed: { check: http } }
    for: { cycles: 3 }
    clear: { duration: 4m }
    then: { action: alert, message: "http down" }
  bad-clear-scalar:
    type: alert
    if: { failed: { check: http } }
    clear: 3
    then: { action: alert, message: "http down" }
  bad-clear-key:
    type: alert
    if: { failed: { check: http } }
    clear: { cycles: 3, min_matches: 2 }
    then: { action: alert, message: "http down" }
  bad-clear-lengths:
    type: alert
    if: { failed: { check: http } }
    clear: { cycles: 3, duration: 4m }
    then: { action: alert, message: "http down" }
  clear-on-remediation:
    type: remediation
    if: { failed: { check: http } }
    clear: { cycles: 3 }
    then: { action: restart }
`,
			want: []string{
				"rules.bad-clear-scalar.clear must be a mapping",
				"rules.bad-clear-key.clear.min_matches is not supported",
				"rules.bad-clear-lengths.clear cannot define both cycles and duration",
				"rules.clear-on-remediation.clear is only supported on alert rules",
			},
			absent: []string{"ok-clear"},
		},
		{
			name: "rule duration windows",
			service: `
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
`,
			absent: []string{
				"duration",
				"rules.restart-after-duration",
				"rules.alert-within-duration",
			},
		},
		{
			name: "unknown check reference",
			service: `
name: svc
service: x
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  r:
    type: remediation
    if: { failed: { check: nonexistent } }
    then: { action: restart }
`,
			want: []string{`references unknown check "nonexistent"`},
		},
		{
			name: "condition exactly one operator",
			service: `
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
`,
			want: []string{"must contain exactly one condition/operator"},
		},
		{
			name: "scalar window rejected",
			service: `
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
`,
			want: []string{"for must be a mapping"},
		},
		{
			name: "inline command condition needs timeout",
			service: `
name: svc
service: x
rules:
  r:
    type: remediation
    if:
      command:
        command: ["can-restart"]
    then: { action: restart }
`,
			want: []string{"command condition must declare a timeout"},
		},
		{
			name: "inline command condition user",
			service: `
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
`,
			want: []string{"rules.r.if.command user must be a non-empty string"},
		},
		{
			name: "inline probe fields",
			service: `
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
`,
			want: []string{
				"rules.bad-http.if.failed.http.url is required for an http check",
				"rules.bad-shape.if.active.tcp must be a mapping",
			},
			absent: []string{"ok-http"},
		},
		{
			name: "inline probe connection protocols",
			service: `
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
`,
			want:   []string{`rules.bad-mysql.if.failed.mysql.port "70000" must be an integer in 1..65535`},
			absent: []string{"ok-mysql"},
		},
		{
			name: "metric form mismatch",
			service: `
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
`,
			want: []string{
				`% threshold but metric "process_count" has no percentage form`,
				`absolute threshold but metric "cpu" has no absolute form`,
			},
		},
		{
			name: "metric catalog and value",
			service: `
name: svc
service: x
rules:
  r:
    type: alert
    if: { metric: { scope: service, name: not_a_metric, op: "~", value: abc } }
    then: { action: alert, message: "m" }
`,
			want: []string{
				`metric "not_a_metric" is not in the service catalog`,
				"op \"~\" is not one of",
				"value \"abc\" must be a number",
			},
		},
	})
}

// Service-document validation — check schemas, commands, process selectors and stop/remediation policy.
func TestServiceValidationChecksAndPolicy(t *testing.T) {
	runServiceIssueCases(t, []serviceIssueCase{
		{
			name: "force kill requires selector",
			service: `
name: svc
service: x
stop_policy:
  force_kill: true
`,
			want: []string{"force_kill=true requires kill_only_if"},
		},
		{
			name: "automatic force kill mode is valid",
			service: `
name: svc
service: x
stop_policy:
  force_kill: auto
processes:
  main: { exe: /usr/sbin/svc, user: svc }
`,
		},
		{
			name: "force kill rejects unknown mode",
			service: `
name: svc
service: x
stop_policy:
  force_kill: eventually
`,
			want: []string{`stop_policy.force_kill must be a boolean or "auto"`},
		},
		{
			name: "stop policy durations",
			service: `
name: svc
service: x
stop_policy:
  graceful_timeout: nope
  term_timeout: 0s
  kill_timeout: -1s
`,
			want: []string{
				`stop_policy.graceful_timeout "nope" must be a valid positive duration`,
				`stop_policy.term_timeout "0s" must be a valid positive duration`,
				`stop_policy.kill_timeout "-1s" must be a valid positive duration`,
			},
		},
		{
			name: "stop policy files absent strict list",
			service: `
name: svc
service: x
stop_policy:
  files_absent: [/run/svc.sock, 7]
`,
			want: []string{"stop_policy.files_absent must be a non-empty list of paths/globs"},
		},
		{
			name: "check entry schemas",
			service: `
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
`,
			want: []string{
				"command must be an array, not a shell string",
				"expect is required for a service check",
				`expect "bogus" is not one of`,
				"exe or exe_any is required for a process check",
				"must define only one of exe or exe_any",
				"exe_any must be a string or non-empty list of strings",
				`state "weird" is not one of`,
				"optional must be a boolean",
				`checks.timeout.timeout "slow" must be a valid positive duration`,
				"config-path.path must be a string or non-empty list of strings",
				"config-change.on_change must be a boolean",
				"must not point under the runtime lock dir",
				"owned-lockfile lockfile must not point under the runtime lock dir",
			},
		},
		{
			name: "resource service check errors",
			service: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  rootfs: { type: storage }
  sysload: { type: load }
`,
			want: []string{
				"checks.rootfs.path is required for a storage check",
				"checks.sysload requires at least one of load1/load5/load15",
			},
		},
		{
			name: "policy max actions",
			service: `
name: svc
service: x
policy:
  cooldown: 5m
  max_actions: 0
`,
			want: []string{"max_actions must be a positive integer"},
		},
		{
			name: "command expect exit",
			service: `
name: svc
service: x
preflight:
  cfg: { type: command, command: ["check"], expect_exit: notanint }
  ok: { type: command, command: ["check"], expect_exit: 1 }
  ok-list: { type: command, command: ["check"], expect_exit: [0, 1] }
  bad-list: { type: command, command: ["check"], expect_exit: [0, nope] }
`,
			want: []string{"expect_exit must be an integer"},
			absent: []string{
				"preflight.ok ",
				"preflight.ok-list",
			},
		},
		{
			name: "commands",
			service: `
name: svc
service: x
commands:
  version: { command: "apachectl -v" }
  slow: { command: ["x"], timeout: nope }
  ok: { command: ["apachectl", "-v"], timeout: 5s }
`,
			want: []string{
				"commands.version command must be an array, not a shell string",
				"commands.slow timeout",
			},
			absent: []string{"commands.ok"},
		},
		{
			name: "process selectors reject unknown keys",
			service: `
name: svc
service: x
processes:
  main: { exe: /usr/sbin/mysqld, extra: value }
  worker: { cmd: "worker", unexpected: true }
`,
			want: []string{
				"processes.main.extra is not supported",
				"processes.worker.unexpected is not supported",
			},
		},
		{
			name: "percent bound",
			service: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  rootfs: { type: storage, path: /, used_pct: { op: ">=", value: "150%" } }
`,
			want: []string{`used_pct value "150%" must be a percentage in 0..100`},
		},
		{
			name: "contains op",
			service: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  q: { type: sql, engine: sqlite, path: /var/db/x.db, query: "select status from t", op: contains, value: ok }
  redis: { type: redis, expect: { role: { op: contains, value: master } } }
`,
			absent: []string{"op"},
		},
	})
}

// Service-document validation — description, category, enable_if and variable specs.
func TestServiceValidationMetadataAndVariables(t *testing.T) {
	runServiceIssueCases(t, []serviceIssueCase{
		{
			name: "description must be string",
			service: `
name: svc
description: [not, a, string]
service: x
`,
			want: []string{"description must be a string"},
		},
		{
			name: "description string passes",
			service: `
name: svc
description: "A friendly label"
service: x
`,
			absent: []string{"description"},
		},
		{
			name: "category must be string",
			service: `
name: svc
category: [not, a, string]
service: x
`,
			want: []string{"category must be a string"},
		},
		{
			name: "enable if is limited and disabled branches still validate",
			service: `
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
`,
			want: []string{
				"processes.optional.cmd is not a valid regex",
				"rules.guarded.enable_if is only supported",
			},
		},
		{
			name: "enable if spec",
			service: `
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
`,
			want: []string{
				`checks.bad.enable_if.file "relative.conf" must be absolute`,
				"checks.bad.enable_if.contains must be non-empty",
				"checks.bad.enable_if.matches is not a valid regex",
				"checks.bad.enable_if must define exactly one",
				"checks.bad.enable_if.extra is not supported",
			},
		},
		{
			name: "from file variable specs",
			service: `
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
`,
			want: []string{
				"variables.no-default.default is required",
				"variables.no-reader must define exactly one of directive or pattern",
				"variables.both must define exactly one of directive or pattern",
				"variables.bad-pattern.pattern is not a valid regex",
				"variables.no-capture.pattern must define at least one capture group",
				"variables.extra.unexpected is not supported",
				"variables.empty-path.from_file is required",
			},
		},
		{
			name: "from file variable path references",
			service: `
name: svc
service: x
variables:
  port: { from_file: "${missing_config}", directive: port, default: 1194 }
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: "${port}" }
`,
			want: []string{"variable ${missing_config} used in variables.port.from_file but not defined"},
		},
		{
			name: "binary variable strict list",
			service: `
name: svc
service: x
variables:
  binary: [/usr/sbin/svc, 7]
`,
			want: []string{"variables.binary must be a non-empty path string or list"},
		},
	})
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

// Paired good/bad validation: good must produce no issue mentioning goodToken,
// bad must produce every substring in want. Rows go here instead of one function
// each; assertServiceValidation does the work.
func TestServiceValidationGoodAndBad(t *testing.T) {
	tests := []struct {
		name      string
		good      string
		goodToken string
		bad       string
		want      []string
	}{
		{
			name: "check interval",
			good: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/health", interval: 30m }
`,
			goodToken: "interval",
			bad: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  http: { type: http, url: "http://x/health", interval: soon }
`,
			want: []string{`checks.http.interval "soon" must be a valid positive duration`},
		},
		{
			name: "service interval",
			good: `
name: svc
service: x
interval: 10s
policy: { cooldown: 5m }
`,
			goodToken: "interval",
			bad: `
name: svc
service: x
interval: notaduration
policy: { cooldown: 5m }
`,
			want: []string{"interval"},
		},
		{
			name: "count check nested threshold",
			good: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  backlog: { type: count, path: /var/spool, count: { op: ">", value: 1000 } }
`,
			goodToken: "count",
			bad: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  backlog: { type: count, path: /var/spool, op: ">", value: 5, count: { op: ">", value: 1000 } }
`,
			want: []string{"count check must not mix a nested count {op, value} with top-level op/value"},
		},
		{
			name: "rule window min_matches optional",
			good: `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { cycles: 5, mode: within }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`,
			goodToken: "rule_window",
			bad: `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { cycles: 5, mode: within, min_matches: 0 }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`,
			want: []string{"rule_window.min_matches must be > 0"},
		},
		{
			name: "rule window duration",
			good: `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { duration: 6m, mode: consecutive }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`,
			goodToken: "rule_window",
			bad: `
name: svc
service: x
policy: { cooldown: 5m }
rule_window: { cycles: 3, duration: 6m }
checks:
  http: { type: http, url: "http://127.0.0.1/" }
`,
			want: []string{"rule_window cannot define both cycles and duration"},
		},
		{
			name: "file condition exists boolean",
			good: `
name: svc
service: x
policy: { cooldown: 5m }
rules:
  marker:
    type: alert
    if: { file: { path: /run/x.flag, exists: false } }
    then: { action: alert, message: "flag" }
`,
			goodToken: "file.exists",
			bad: `
name: svc
service: x
policy: { cooldown: 5m }
rules:
  marker:
    type: alert
    if: { file: { path: /run/x.flag, exists: "false" } }
    then: { action: alert, message: "flag" }
`,
			want: []string{"file.exists must be a boolean"},
		},
		{
			name: "pids check",
			good: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  pid-table: { type: pids, used_pct: { op: ">=", value: "90%" } }
`,
			goodToken: "pid-table",
			bad: `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-pred: { type: pids }
`,
			want: []string{"checks.no-pred requires at least one of used_pct/free/count"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertServiceValidation(t, tt.good, tt.goodToken, tt.bad, tt.want...)
		})
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
	// exe-only and cmd-only selectors are valid (user/group optional); a selector
	// with neither exe nor cmd, or an invalid cmd regex, is rejected.
	assertServiceValidationTokens(t, `
name: svc
service: x
processes:
  worker: { exe: /usr/sbin/mysqld }
  unifi: { cmd: "java .*unifi", group: unifi }
`, []string{"processes.worker", "processes.unifi"}, `
name: svc
service: x
processes:
  main: { user: mysql }
  badcmd: { cmd: "(" }
`,
		"processes.main requires exe or cmd",
		"processes.badcmd.cmd is not a valid regex")
}

// delegated bars a process from every kill decision, so it must be a real
// boolean: read leniently, a stray string would come back false and silently
// re-expose the workload the flag exists to protect.
func TestValidateProcessSelectorDelegatedMustBeBoolean(t *testing.T) {
	assertServiceValidationTokens(t, `
name: svc
service: x
processes:
  shim: { exe: /usr/bin/containerd-shim, user: root, delegated: true }
  main: { exe: /usr/bin/containerd, user: root, delegated: false }
`, []string{"processes.shim", "processes.main"}, `
name: svc
service: x
processes:
  shim: { exe: /usr/bin/containerd-shim, user: root, delegated: "true" }
`,
		"processes.shim.delegated must be a boolean")
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
