# Sermo implementation specification

## 1. Project identity

Project name: **Sermo**

Binaries:

- `sermod`: daemon responsible for monitoring, evaluating rules and applying safe remediation actions.
- `sermoctl`: command-line tool used by operators and scripts.

Default paths:

```text
/etc/sermo/                 # configuration
/usr/share/sermo/profiles/  # packaged base profiles
/run/sermo/                 # runtime state, locks, sockets
/var/lib/sermo/             # persistent state, optional later
```

Short description:

```text
Sermo is a safe service monitoring and control system for Linux.
It provides a portable service wrapper over systemd and OpenRC, validates services before actions, detects blocking operational states, discovers service processes and applies guarded remediation rules.
```

Primary design rule:

```text
sermod and sermoctl must use the same operation engine.
If sermoctl restart mysql is protected by preflight checks and locks, the automatic restart performed by sermod must be protected in exactly the same way.
```

---

## 2. Goals

Sermo should provide:

1. Automatic service manager detection: systemd or OpenRC.
2. A portable CLI wrapper for service control:
   - `sermoctl status SERVICE`
   - `sermoctl start SERVICE`
   - `sermoctl stop SERVICE`
   - `sermoctl restart SERVICE`
3. Safe restart workflow:
   - check locks
   - run preflight validation
   - stop/restart via detected backend
   - verify residual processes
   - optionally escalate to SIGTERM/SIGKILL only when explicitly allowed
   - run postflight checks
4. Declarative YAML configuration.
5. Packaged base profiles for applications such as Apache, Redis, MySQL/MariaDB and PHP-FPM.
6. User configuration by layering, overrides and clones.
7. Monitoring rules with logical conditions: `and`, `or`, `not`.
8. Rule windows:
   - `for: cycles: N` for consecutive failures
   - `within: cycles: N, min_matches: M` for sliding windows
9. Guard rules that block unsafe actions.
10. Process discovery using pidfiles, cgroups where available and `/proc`.
11. Simple event logging.
12. Good output for scripts, including stable exit codes and optional JSON output.

---

## 3. Non-goals for the first implementation

Do not implement these in the MVP:

- Web UI.
- Distributed/cluster mode.
- Database-backed event storage.
- Plugin system.
- Full Monit-compatible language parser.
- Remote agents.
- PolicyKit integration.
- Full systemd D-Bus implementation unless the command-based backend is already working.

The MVP should work with `systemctl` and `rc-service` first.

---

## 4. External dependencies

Use a small dependency set.

Required for MVP:

```bash
go get github.com/spf13/cobra
go get github.com/goccy/go-yaml
go get github.com/prometheus/procfs
```

Recommended later:

```bash
go get github.com/coreos/go-systemd/v22
go get github.com/fsnotify/fsnotify
```

Dependency rationale:

- `github.com/spf13/cobra`: CLI framework for `sermoctl` and `sermod` subcommands.
- `github.com/goccy/go-yaml`: YAML parsing and rendering.
- `github.com/prometheus/procfs`: process and system metrics from `/proc` and `/sys`.
- `github.com/coreos/go-systemd/v22/dbus`: optional future native systemd backend.
- `github.com/fsnotify/fsnotify`: optional future config reload watcher.

Use Go standard library where possible:

- `context`
- `os/exec`
- `net/http`
- `net`
- `time`
- `log/slog`
- `encoding/json`
- `os/signal`
- `syscall`

---

## 5. Repository layout

```text
sermo/
├── cmd/
│   ├── sermod/
│   │   └── main.go
│   └── sermoctl/
│       └── main.go
├── internal/
│   ├── app/
│   │   ├── daemon.go
│   │   ├── scheduler.go
│   │   └── state.go
│   ├── cli/
│   │   ├── root.go
│   │   ├── backend.go
│   │   ├── service.go
│   │   ├── config.go
│   │   ├── locks.go
│   │   ├── preflight.go
│   │   └── processes.go
│   ├── config/
│   │   ├── model.go
│   │   ├── loader.go
│   │   ├── merge.go
│   │   ├── render.go
│   │   ├── variables.go
│   │   └── validate.go
│   ├── profiles/
│   │   ├── registry.go
│   │   ├── resolver.go
│   │   └── source.go
│   ├── servicemgr/
│   │   ├── manager.go
│   │   ├── detector.go
│   │   ├── systemd_exec.go
│   │   ├── openrc.go
│   │   └── errors.go
│   ├── checks/
│   │   ├── check.go
│   │   ├── runner.go
│   │   ├── tcp.go
│   │   ├── http.go
│   │   ├── command.go
│   │   ├── service.go
│   │   ├── file.go
│   │   ├── process.go
│   │   └── metric.go
│   ├── rules/
│   │   ├── condition.go
│   │   ├── evaluator.go
│   │   ├── window.go
│   │   └── state.go
│   ├── operation/
│   │   ├── engine.go
│   │   ├── start.go
│   │   ├── stop.go
│   │   ├── restart.go
│   │   └── result.go
│   ├── preflight/
│   │   ├── runner.go
│   │   └── result.go
│   ├── locks/
│   │   ├── manager.go
│   │   ├── runtime.go
│   │   ├── file.go
│   │   └── external.go
│   ├── process/
│   │   ├── model.go
│   │   ├── discover.go
│   │   ├── procfs.go
│   │   ├── tree.go
│   │   ├── signal.go
│   │   └── residual.go
│   ├── metrics/
│   │   ├── collector.go
│   │   ├── cpu.go
│   │   └── memory.go
│   ├── events/
│   │   ├── event.go
│   │   └── logger.go
│   └── execx/
│       └── runner.go
├── profiles/
│   ├── apache.yml
│   ├── mysql.yml
│   ├── mariadb.yml
│   ├── redis.yml
│   └── php-fpm.yml
├── configs/
│   ├── sermo.yml
│   └── apps-enabled/
│       ├── apache-main.yml
│       ├── mysql-main.yml
│       └── redis-main.yml
├── packaging/
│   ├── systemd/
│   │   └── sermod.service
│   └── openrc/
│       └── sermod
├── docs/
│   ├── configuration.md
│   ├── rules.md
│   ├── profiles.md
│   └── safety.md
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## 6. Configuration layout

Global configuration:

```text
/etc/sermo/sermo.yml
```

Packaged profiles:

```text
/usr/share/sermo/profiles/*.yml
```

User profiles:

```text
/etc/sermo/apps-available/*.yml
```

Enabled services:

```text
/etc/sermo/apps-enabled/*.yml
```

Runtime locks:

```text
/run/sermo/locks/*.lock
```

Recommended complete layout:

```text
/etc/sermo/
├── sermo.yml
├── conf.d/
│   ├── 10-defaults.yml
│   ├── 20-company.yml
│   └── 90-local.yml
├── apps-available/
│   ├── apache-custom.yml
│   └── mysql-custom.yml
├── apps-enabled/
│   ├── apache-main.yml
│   ├── mysql-main.yml
│   └── redis-cache.yml
└── locks.d/
```

---

## 7. Global configuration example

```yaml
engine:
  backend: auto
  interval: 30s
  max_parallel_checks: 8
  default_timeout: 10s

paths:
  profiles:
    - /usr/share/sermo/profiles
    - /etc/sermo/apps-available
  enabled:
    - /etc/sermo/apps-enabled
  locks:
    - /run/sermo/locks
    - /etc/sermo/locks.d

defaults:
  rule_window:
    cycles: 1
    mode: consecutive

  stop_policy:
    graceful_timeout: 30s
    term_timeout: 15s
    kill_timeout: 5s
    force_kill: false

  policy:
    cooldown: 5m

security:
  require_preflight_before_restart: true
  block_restart_on_active_lock: true
  allow_sigkill_by_default: false
  require_kill_selector: true

logging:
  level: info
  format: text
```

---

## 8. Configuration model

Sermo has two document kinds:

```yaml
kind: profile
```

and:

```yaml
kind: service
```

A profile is a reusable base definition.
A service is a concrete monitored instance.

A service may use a profile:

```yaml
kind: service
name: apache-main
uses: apache
```

A service may clone another service:

```yaml
kind: service
name: redis-cache
clone: redis-main
```

Resolution order:

```text
1. Load packaged profiles from /usr/share/sermo/profiles.
2. Load user profiles from /etc/sermo/apps-available.
3. Load global configuration and conf.d files.
4. Load enabled services from /etc/sermo/apps-enabled.
5. Resolve services:
   - apply uses profile
   - apply clone chain
   - merge overrides
   - expand variables
   - validate final flattened service
```

The daemon must only work with resolved, flat service definitions.

---

## 9. Merge rules

Use predictable merge rules.

Scalars overwrite:

```yaml
cooldown: 2m
```

merged with:

```yaml
cooldown: 5m
```

becomes:

```yaml
cooldown: 5m
```

Maps merge recursively:

```yaml
policy:
  max_actions: 3
  cooldown: 2m
```

merged with:

```yaml
policy:
  cooldown: 5m
```

becomes:

```yaml
policy:
  max_actions: 3
  cooldown: 5m
```

Named sections must be maps, not arrays:

```yaml
checks:
  http:
    type: http
    url: http://127.0.0.1/
```

This allows a child document to override only one check field:

```yaml
checks:
  http:
    url: http://127.0.0.1/health
```

Disable inherited entries with:

```yaml
checks:
  http:
    enabled: false
```

Optionally delete inherited entries with:

```yaml
checks:
  http:
    delete: true
```

For MVP, `enabled: false` is required; `delete: true` is optional.

---

## 10. Variables

Profiles may define variables:

```yaml
variables:
  host: 127.0.0.1
  port: 8080
  user: www-data
  binary: /usr/sbin/apache2
```

Use variables with `${name}`:

```yaml
checks:
  http:
    type: http
    url: "http://${host}:${port}/health"
```

MVP variable rules:

- Variables are strings.
- Expansion is simple `${var}` substitution.
- Missing variables are validation errors.
- No expressions or template language in MVP.

### Typed fields and variable interaction

Variables are always strings, but several configuration fields are logically
numeric (for example `port`, `expect_status`) or have a small grammar (for
example a metric `value` such as `40%`). To let these fields be written both
directly and through `${var}` substitution, Sermo accepts more than one YAML
form for them and normalizes after expansion. These are all valid and resolve to
the same value:

```yaml
port: 783
port: "783"
port: "${port}"     # where variables.port = "783"
```

Loading and resolution order for such fields:

```text
1. Load the field as a raw scalar, tolerating either an int or a string.
2. Expand ${var} references. Because variables are strings, any field that
   contains a variable reference is a string at this step.
3. Parse the expanded value into the field's target type (int, percentage, ...).
4. A value that cannot be parsed, or is out of range, is a config validation
   error (exit code 78).
```

Implement this with a small tolerant scalar type instead of plain `int`, so YAML
unmarshalling never fails just because a numeric field was quoted or carried a
variable. The signature below is illustrative; adapt it to the YAML library:

```go
// FlexInt accepts a YAML integer or a string scalar (which may contain ${var}).
// Raw holds the pre-expansion text; Val is filled in during resolution.
type FlexInt struct {
    Raw string
    Val int
}

func (f *FlexInt) UnmarshalYAML(unmarshal func(any) error) error {
    var s string
    if err := unmarshal(&s); err == nil {
        f.Raw = s
        return nil
    }
    var i int
    if err := unmarshal(&i); err != nil {
        return err
    }
    f.Raw = strconv.Itoa(i)
    return nil
}
```

Target types for MVP fields:

```text
port            FlexInt, resolved to an int in range 1..65535.
expect_status   FlexInt, resolved to an int (a single status code in MVP).
timeout         duration string such as "3s" (already a string, no FlexInt).
metric value    string with optional trailing "%"; see section 14.
```

Resolution (steps 2-4) happens once, when a service is flattened, so the daemon
only ever sees parsed values. The raw form is kept only for `config render` and
error messages.

---

## 11. Service manager abstraction

Package: `internal/servicemgr`

Interface:

```go
package servicemgr

import "context"

type Backend string

const (
    BackendAuto    Backend = "auto"
    BackendSystemd Backend = "systemd"
    BackendOpenRC  Backend = "openrc"
)

type Status string

const (
    StatusActive   Status = "active"
    StatusInactive Status = "inactive"
    StatusFailed   Status = "failed"
    StatusUnknown  Status = "unknown"
)

type Manager interface {
    Backend() Backend
    IsAvailable(ctx context.Context) bool

    Status(ctx context.Context, service string) (Status, error)
    IsActive(ctx context.Context, service string) (bool, error)

    Start(ctx context.Context, service string) error
    Stop(ctx context.Context, service string) error
    Restart(ctx context.Context, service string) error
}
```

Backend detection priority:

```text
1. CLI flag --backend
2. Environment variable SERMO_BACKEND
3. Global config engine.backend
4. Automatic detection
```

Automatic detection:

```text
1. If systemctl exists and /run/systemd/system exists, use systemd.
2. Else if rc-service exists and /run/openrc exists, use OpenRC.
3. Else try rc-status as fallback.
4. Else fail with a clear error.
```

Do not detect systemd by the presence of `systemctl` alone.

Systemd initial implementation:

```text
systemctl is-active SERVICE.service
systemctl start SERVICE.service
systemctl stop SERVICE.service
systemctl restart SERVICE.service
```

Normalize systemd unit names:

```text
nginx      -> nginx.service
nginx.service -> nginx.service
```

OpenRC implementation:

```text
rc-service SERVICE status
rc-service SERVICE start
rc-service SERVICE stop
rc-service SERVICE restart
```

---

## 12. Checks

Package: `internal/checks`

Common check interface:

```go
type Result struct {
    Service string
    Check   string
    OK      bool
    Message string
    Latency time.Duration
    Data    map[string]any
}

type Check interface {
    Name() string
    Run(ctx context.Context) Result
}
```

Field typing: `port` and `expect_status` are `FlexInt` (accept an int or a
string, possibly a `${var}`), `timeout` is a duration string, and the metric
`value` follows the grammar in section 14. See section 10, "Typed fields and
variable interaction". Both `port: 783` and `port: "${port}"` are valid.

MVP check types:

### TCP

```yaml
checks:
  port-783:
    type: tcp
    host: 127.0.0.1
    port: 783
    timeout: 3s
```

### HTTP

```yaml
checks:
  http:
    type: http
    url: http://127.0.0.1/health
    method: GET
    expect_status: 200
    timeout: 5s
```

Field defaults:

```text
method         GET when omitted.
expect_status  200 when omitted.
timeout        engine.default_timeout when omitted.
```

`expect_status` is a single status code in the MVP (a `FlexInt`, see section 10).
A status class such as `2xx` or a list of accepted codes is a documented post-MVP
extension; the loader should reject a non-numeric `expect_status` until then.

### Command

```yaml
checks:
  configtest:
    type: command
    command: ["apachectl", "configtest"]
    expect_exit: 0
    timeout: 10s
```

### Service state

```yaml
checks:
  service:
    type: service
    expect: active
```

### File exists

```yaml
checks:
  backup-lock:
    type: file_exists
    path: /run/sermo/locks/mysql.backup.lock
```

### Process exists

```yaml
checks:
  mariabackup:
    type: process
    exe: /usr/bin/mariabackup
    user: mysql
    state: running
```

### Metric

```yaml
checks:
  memory:
    type: metric
    scope: service        # service | system; default service
    name: memory
    op: ">"
    value: 40%
```

Every metric has a **scope** that decides what it measures:

```text
service  (default)  measures only the monitored service: its discovered process
                    set, summed across the process tree, or the service cgroup
                    when the backend exposes one (systemd MainPID/cgroup).
system              measures the whole machine, regardless of any service.
```

MVP metric catalog:

```text
scope: service
  memory          resident memory of the service, as bytes or % of total RAM
  cpu             CPU used by the service, as % of total CPU capacity
                  (100% = all cores saturated)
  process_count   number of discovered processes in the service set

scope: system
  total_memory    used memory of the whole machine, as bytes or % of total RAM
  total_cpu       CPU used by the whole machine, as % of total CPU capacity
  load1, load5, load15   load averages
```

Why scope matters for safety:

```text
A service metric answers "is THIS service unhealthy?" and is a sound trigger for
remediation. A system metric answers "is the machine under pressure?" and is NOT
a sound reason to restart one particular service: the pressure usually comes from
something else, and restarting the wrong service can make an incident worse.
```

For this reason, remediation rules may only use `scope: service` metrics. System
metrics are allowed only for `alert` actions in the MVP (see section 14 and the
validation rules in section 30).

The reference names use a `total_` prefix for system metrics and unprefixed names
for service metrics, so a misplaced scope is easy to spot in review.

---

## 13. Rule model

Package: `internal/rules`

Rules are declarative logical trees.

A rule has:

```yaml
rules:
  - name: string
    type: remediation | guard | alert
    if: {}
    for: {}
    within: {}
    then: {}
```

Only `if` and `then` are mandatory.

If no `for` or `within` is defined, default is equivalent to:

```yaml
for:
  cycles: 1
  mode: consecutive
```

For MVP, reject rules that use both `for` and `within` at the same time.

---

## 14. Rule conditions

Supported logical operators:

```yaml
if:
  and:
    - condition
    - condition
```

```yaml
if:
  or:
    - condition
    - condition
```

```yaml
if:
  not:
    condition
```

Supported leaf conditions:

### Failed check

```yaml
if:
  failed:
    check: http
```

### Active check

```yaml
if:
  active:
    check: backup-lock
```

### Inline TCP failure

```yaml
if:
  failed:
    tcp:
      host: 127.0.0.1
      port: 783
      timeout: 3s
```

### Metric condition

```yaml
if:
  metric:
    scope: service        # service | system; default service
    name: cpu
    op: ">"
    value: 30%
```

`scope` and `name` follow the metric catalog in section 12. A condition that
omits `scope` defaults to `scope: service`.

`op` is one of `>`, `>=`, `<`, `<=`, `==`, `!=`. `value` is loaded as a string
(so it may carry a `${var}`) and parsed after expansion:

```text
- A trailing "%" marks a percentage value in 0..100, compared against the
  metric's percentage form (for example memory as a percentage of RAM).
- Otherwise the value is an absolute number, compared against the metric's
  absolute form, in the metric's native unit.
- A value that is neither a valid number nor number+"%" is a validation error.
- Mixing forms (a "%" threshold against an absolute-only metric, or vice versa)
  is a validation error.
```

Safety: a `scope: system` metric may appear only in rules whose action is
`alert`. Using a system metric in a remediation rule (restart/start/stop) is a
validation error. See sections 12 and 30.

### Service condition

```yaml
if:
  service:
    state: active
```

### Process condition

```yaml
if:
  process:
    exe: /usr/sbin/mysqld
    user: mysql
    state: running
```

### File condition

```yaml
if:
  file:
    path: /run/backup/mysql.lock
    exists: true
```

### Command condition

```yaml
if:
  command:
    command: ["/usr/local/sbin/can-restart-mysql"]
    expect_exit: 0
    timeout: 10s
```

Go model:

```go
type Condition struct {
    And []Condition `yaml:"and,omitempty"`
    Or  []Condition `yaml:"or,omitempty"`
    Not *Condition  `yaml:"not,omitempty"`

    Failed *FailedCondition `yaml:"failed,omitempty"`
    Active *ActiveCondition `yaml:"active,omitempty"`

    Metric  *MetricCondition  `yaml:"metric,omitempty"`
    Service *ServiceCondition `yaml:"service,omitempty"`
    Process *ProcessCondition `yaml:"process,omitempty"`
    File    *FileCondition    `yaml:"file,omitempty"`
    Command *CommandCondition `yaml:"command,omitempty"`
}
```

Validation rule:

```text
Each condition node must contain exactly one of:
and, or, not, failed, active, metric, service, process, file, command.
```

### Condition evaluation semantics

Conditions must be cheap and repeatable, because rules are evaluated on every
scheduler cycle. To keep cost predictable and avoid side effects, the evaluator
treats every leaf condition as a probe that runs at most once per cycle:

```text
1. At the start of a cycle, run all declared checks once and cache their results.
2. Collect every inline leaf condition used across all rules (inline tcp, file,
   process, metric, command) and deduplicate them by their normalized parameters.
3. Run each distinct inline probe at most once and cache the result for the cycle.
4. Evaluate all rule trees (guards first, then remediation) against the cached
   results. `failed`/`active` references and inline conditions never cause a
   second execution within the same cycle.
```

So the number of probe executions per cycle equals the number of distinct probes,
independent of how many rules reference them.

Inline `command` conditions:

```text
- Must be a read-only predicate: they decide true/false and must not change
  system state. If you need to gate or mutate, use a guard or an action, not a
  condition.
- Use array form with a timeout, exactly like command checks.
- Run once per cycle like any other probe.
- Recommended: declare anything expensive or with external effects as a named
  check under `checks:` and reference it with `failed`/`active`. That makes its
  cost and intent explicit and includes it in `config render`.
```

---

## 15. Rule windows

### Consecutive cycles

Equivalent to:

```text
if failed host 127.0.0.1 port 783 for 3 cycles then restart
```

YAML:

```yaml
rules:
  - name: port-783-down
    type: remediation
    if:
      failed:
        tcp:
          host: 127.0.0.1
          port: 783
          timeout: 3s
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart
```

### Single cycle CPU rule

Equivalent to:

```text
if service cpu > 30% for 1 cycles then restart
```

YAML:

```yaml
rules:
  - name: high-cpu
    type: remediation
    if:
      metric:
        scope: service
        name: cpu
        op: ">"
        value: 30%
    for:
      cycles: 1
      mode: consecutive
    then:
      action: restart
```

### Sliding window

Equivalent to:

```text
if service memory > 40% within 15 cycles then restart
```

YAML:

```yaml
rules:
  - name: high-memory
    type: remediation
    if:
      metric:
        scope: service
        name: memory
        op: ">"
        value: 40%
    within:
      cycles: 15
      min_matches: 1
    then:
      action: restart
```

More useful variant:

```yaml
within:
  cycles: 15
  min_matches: 5
```

This means the condition must be true at least 5 times in the last 15 cycles.

Go model:

```go
type Rule struct {
    Name   string    `yaml:"name"`
    Type   RuleType  `yaml:"type"`
    If     Condition `yaml:"if"`
    For    *ForWindow `yaml:"for,omitempty"`
    Within *WithinWindow `yaml:"within,omitempty"`
    Then   Action    `yaml:"then"`
    Blocks []string  `yaml:"blocks,omitempty"`
}

type ForWindow struct {
    Cycles int    `yaml:"cycles"`
    Mode   string `yaml:"mode,omitempty"`
}

type WithinWindow struct {
    Cycles     int `yaml:"cycles"`
    MinMatches int `yaml:"min_matches"`
}
```

`RuleType` and `Action` are defined in section 16.

---

## 16. Actions

MVP actions:

```yaml
then:
  action: restart
```

Supported actions:

```text
restart
start
stop
alert
block
exec
```

For MVP, implement:

- `restart`
- `start`
- `stop`
- `block`
- `alert` as log-only

Optional later:

```yaml
then:
  actions:
    - type: alert
      message: Redis memory high
    - type: restart
```

For MVP, implement only single action.

Go model:

```go
type RuleType string

const (
    RuleRemediation RuleType = "remediation"
    RuleGuard       RuleType = "guard"
    RuleAlert       RuleType = "alert"
)

type ActionType string

const (
    ActionRestart ActionType = "restart"
    ActionStart   ActionType = "start"
    ActionStop    ActionType = "stop"
    ActionAlert   ActionType = "alert"
    ActionBlock   ActionType = "block"
    ActionExec    ActionType = "exec"
)

// Action is the resolved `then:` block of a rule. The MVP supports a single
// action per rule; the optional multi-action form is post-MVP.
type Action struct {
    Type    ActionType `yaml:"action"`
    Message string     `yaml:"message,omitempty"`

    // exec action only, post-MVP. Array form, never a shell string.
    Command []string      `yaml:"command,omitempty"`
    Timeout time.Duration `yaml:"timeout,omitempty"`
}
```

`message` is mandatory in practice for `block` and `alert`, where it is the
reason shown to the operator and recorded in the event; it is optional for
`restart`/`start`/`stop`. The guard examples in sections 17 and 25-27 rely on it.

### Remediation policy: cooldown and rate limiting

Automatic remediation must never enter a restart loop. Every service has a
remediation policy that gates how often actions may actually run:

```yaml
policy:
  cooldown: 5m
  max_actions: 5
  max_actions_window: 1h
  backoff:
    initial: 1m
    factor: 2
    max: 30m
```

Field meaning:

```text
cooldown            minimum time that must pass after an executed remediation
                    action before another automatic action may run for the
                    same service. Required mechanism for MVP.
max_actions         maximum number of executed remediation actions allowed
                    inside max_actions_window. Optional for MVP.
max_actions_window  sliding window for max_actions. Optional for MVP.
backoff             optional exponential growth of the effective cooldown after
                    each consecutive remediation. Optional, post-MVP.
```

Relationship to rule windows:

```text
for / within  decide WHEN a rule fires (how many failed cycles are needed).
policy        decides whether the action is allowed to run RIGHT NOW, given how
              recently the service was already acted upon.
```

These are independent. A rule may keep firing every cycle while the cooldown
suppresses repeated execution. `for`/`within` must not be abused as a cooldown.

Scope:

```text
- Cooldown and rate limiting apply to AUTOMATIC remediation performed by sermod.
- Manual operator actions (sermoctl restart, etc.) are NOT subject to cooldown;
  the operator is acting deliberately. They remain subject to locks, guards and
  preflight.
- The shared operation engine performs the action. The cooldown decision is made
  by the daemon's rule evaluation BEFORE it calls the engine, so manual and
  automatic paths still share the same engine while only automatic remediation is
  rate limited.
```

Remediation state lives in `internal/rules/state.go`, keyed by service:

```go
type RemediationState struct {
    LastActionAt   time.Time
    RecentActions  []time.Time   // timestamps still inside max_actions_window
    CurrentBackoff time.Duration // 0 when backoff is disabled
}
```

Decision rule for an automatic action on service S at time now:

```text
1. If now - LastActionAt < effective cooldown -> suppress (log, do not act).
   effective cooldown = max(policy.cooldown, CurrentBackoff).
2. Else if max_actions is set and len(RecentActions within window) >= max_actions
   -> suppress (log, do not act).
3. Else allow. After the engine runs:
   - set LastActionAt = now
   - append now to RecentActions and trim entries outside the window
   - if backoff enabled, grow CurrentBackoff (capped at backoff.max)
   - on a healthy interval with no firing rule, decay/reset CurrentBackoff.
```

When a service defines no policy, the global `defaults.policy` applies (cooldown
`5m` in the reference config).

---

## 17. Guard rules

Guard rules block unsafe actions.

Example: block restart if config is invalid.

```yaml
rules:
  - name: block-restart-if-config-invalid
    type: guard
    blocks:
      - restart
      - start
    if:
      failed:
        check: configtest
    then:
      action: block
      message: "Configuration invalid, restart blocked"
```

Example: block stop/restart during backup.

```yaml
rules:
  - name: block-restart-during-backup
    type: guard
    blocks:
      - restart
      - stop
    if:
      or:
        - active:
            check: backup-lock
        - active:
            check: mariabackup
    then:
      action: block
      message: "Backup is running"
```

Evaluation order:

```text
1. Run all declared checks and any inline rule probes once, and cache the results
   for this cycle (see section 14, condition evaluation semantics).
2. Evaluate guard rules.
3. Evaluate remediation rules.
4. If remediation wants an action, check whether any guard blocks that action.
5. If not blocked, run operation engine.
```

A remediation rule must never bypass guard rules.

---

## 18. Operation engine

Package: `internal/operation`

The operation engine performs safe actions.

Actions:

- Start
- Stop
- Restart

Restart flow:

```text
1. Load resolved service.
2. Acquire internal operation lock for the service.
3. Check external locks.
4. Run preflight checks required for restart.
5. If any guard blocks restart, stop and return blocked result.
6. Execute backend restart or stop/start.
7. Verify final service status.
8. Discover residual processes.
9. If residual processes remain:
   - if force_kill=false, return orphan_processes error.
   - if force_kill=true, apply signal escalation policy.
10. Run postflight checks.
11. Release internal operation lock.
12. Emit event.
```

For databases, default `force_kill` must be false.

The operation engine does not implement cooldown or rate limiting itself: those
gate the *decision* to act and are enforced by the daemon's rule evaluation
before the engine is called (see section 16, "Remediation policy"). This keeps
manual `sermoctl` actions and automatic `sermod` remediation on the same engine
while only automatic remediation is rate limited.

Operation result model:

```go
type ResultStatus string

const (
    ResultOK              ResultStatus = "ok"
    ResultBlocked         ResultStatus = "blocked"
    ResultPreflightFailed ResultStatus = "preflight_failed"
    ResultFailed          ResultStatus = "failed"
    ResultOrphanProcesses ResultStatus = "orphan_processes"
)

type Result struct {
    Service string
    Action  string
    Status  ResultStatus
    Message string
    Backend string
    Checks  []checks.Result
    Locks   []locks.ActiveLock
    Processes []process.Process
}
```

---

## 19. Preflight

Preflight checks run before dangerous actions.

Example:

```yaml
preflight:
  configtest:
    type: command
    command: ["apachectl", "configtest"]
    timeout: 10s

  binary:
    type: binary
    path: /usr/sbin/apache2

  libraries:
    type: libraries
    binary: /usr/sbin/apache2
```

For MVP, implement preflight by reusing the check runner.

Special check types to implement:

### binary

Verifies that a path exists and is executable.

```yaml
preflight:
  binary:
    type: binary
    path: /usr/sbin/mysqld
```

### libraries

Verifies that a dynamically linked binary has no missing shared libraries.

Initial implementation may run:

```text
ldd /path/to/binary
```

and fail if output contains:

```text
not found
```

Important: do not run `ldd` on untrusted arbitrary user-uploaded binaries. In the MVP, this is an admin tool that reads root-managed configuration, so it is acceptable with a warning in documentation. Later, replace with safer ELF parsing.

### command

Runs a validation command with timeout.

For MySQL:

```yaml
preflight:
  config:
    type: command
    command: ["mysqld", "--validate-config"]
    timeout: 15s
```

For Apache:

```yaml
preflight:
  config:
    type: command
    command: ["apachectl", "configtest"]
    timeout: 10s
```

For PHP-FPM:

```yaml
preflight:
  config:
    type: command
    command: ["php-fpm", "-t"]
    timeout: 10s
```

---

## 20. Locks

Package: `internal/locks`

Support two categories:

1. Internal runtime locks created by Sermo.
2. External lock checks defined in service profiles.

CLI lock command:

```bash
sermoctl lock mysql --reason "backup mysql" --ttl 4h -- mysqldump --single-transaction --all-databases
```

While the command runs, Sermo should create a lock file in:

```text
/run/sermo/locks/mysql.lock
```

Suggested JSON format:

```json
{
  "service": "mysql",
  "reason": "backup mysql",
  "owner_pid": 12345,
  "created_at": "2026-06-05T12:00:00Z",
  "expires_at": "2026-06-05T16:00:00Z"
}
```

External lock checks example:

```yaml
checks:
  backup-lock:
    type: file_exists
    path: /run/sermo/locks/mysql.backup.lock

  mariabackup:
    type: process
    exe: /usr/bin/mariabackup
    user: mysql
    state: running

rules:
  - name: block-restart-during-backup
    type: guard
    blocks: [restart, stop]
    if:
      or:
        - active:
            check: backup-lock
        - active:
            check: mariabackup
    then:
      action: block
      message: "MySQL backup is running"
```

---

## 21. Process discovery

Package: `internal/process`

Process discovery methods:

```yaml
processes:
  pidfile:
    type: pidfile
    path: /run/mysqld/mysqld.pid

  command:
    type: command_match
    exe: /usr/sbin/mysqld
    user: mysql
```

Process model:

```go
type Process struct {
    PID      int
    PPID     int
    User     string
    UID      uint32
    Exe      string
    Cmdline  []string
    Role     string
    Source   string
}
```

Discovery strategy:

```text
1. Try backend-specific information.
   - systemd: MainPID and cgroup, later.
   - OpenRC: service status and pidfile, where available.
2. Try configured pidfiles.
3. Try command_match selectors.
4. Build child process tree from /proc.
5. Deduplicate by PID.
```

Safety rule:

```text
Never kill a process based only on a partial name match.
```

Required safe selector for kill:

```yaml
stop_policy:
  kill_only_if:
    users: [mysql]
    exe_any:
      - /usr/sbin/mysqld
```

---

## 22. Stop and kill policy

Any `stop_policy` field omitted by a profile or service inherits from
`defaults.stop_policy` in the global config (section 7). Profiles should still
state the timeouts that matter for that application explicitly, so the behaviour
is readable without cross-referencing the defaults.

Example:

```yaml
stop_policy:
  graceful_timeout: 120s
  term_timeout: 60s
  kill_timeout: 5s
  force_kill: false
  kill_only_if:
    users: [mysql]
    exe_any:
      - /usr/sbin/mysqld
```

For stateless web services:

```yaml
stop_policy:
  graceful_timeout: 30s
  term_timeout: 10s
  kill_timeout: 5s
  force_kill: true
  kill_only_if:
    users: [www-data, apache]
    exe_any:
      - /usr/sbin/apache2
      - /usr/sbin/httpd
```

Signal escalation:

```text
1. backend.Stop(service)
2. wait graceful_timeout
3. discover residual processes
4. if no residuals, success
5. if residuals and force_kill=false, fail with orphan_processes
6. if residuals and force_kill=true:
   - validate every process against kill_only_if
   - send SIGTERM
   - wait term_timeout
   - discover again
   - send SIGKILL only if still present and policy allows it
```

Default:

```text
force_kill: false
```

---

## 23. CLI design

Root flags:

```text
--config /etc/sermo/sermo.yml
--backend auto|systemd|openrc
--json
--quiet
--timeout duration
```

Commands:

```bash
sermoctl backend
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl start SERVICE
sermoctl stop SERVICE
sermoctl restart SERVICE

sermoctl preflight SERVICE
sermoctl processes SERVICE
sermoctl locks SERVICE

sermoctl config validate [SERVICE]
sermoctl config render SERVICE
sermoctl config diff BASE SERVICE      # post-MVP

sermoctl profile list                 # post-MVP
sermoctl profile show PROFILE         # post-MVP

sermoctl service list                 # post-MVP
sermoctl service show SERVICE         # post-MVP
sermoctl service clone SOURCE TARGET  # post-MVP

sermoctl lock SERVICE --reason REASON --ttl DURATION -- COMMAND...
sermoctl lock acquire SERVICE --reason REASON --ttl DURATION
sermoctl lock release SERVICE
```

This is the full planned command surface. Commands marked `# post-MVP` are not
required for the first implementation; the authoritative MVP subset is listed
just below. `config diff` in particular is planned but out of scope for the MVP
and is not covered by the section 30 validation requirements.

MVP commands:

```bash
sermoctl backend
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl start SERVICE
sermoctl stop SERVICE
sermoctl restart SERVICE
sermoctl preflight SERVICE
sermoctl processes SERVICE
sermoctl config validate [SERVICE]
sermoctl config render SERVICE
```

Exit codes:

```text
0   success / active / allowed
1   service inactive, check failed or rule false
2   internal or runtime error / backend not detected
64  usage error (bad flags or arguments)
75  temporarily blocked by lock or guard
78  configuration invalid (syntax, schema or validation failure)
```

Distinction between `2` and `78`:

```text
78  the configuration itself is wrong: YAML syntax error, missing kind/name,
    unknown variable, unresolved uses/clone, failed `config validate`.
    Use 78 whenever the problem is in the config files the operator can fix.
2   everything else that is not a clean false (1), a usage error (64),
    a temporary block (75) or a config problem (78): I/O errors, backend
    not detected, an exec that could not be launched, an unexpected panic
    recovered at the top level.
```

`is-active` behavior:

```text
0 -> active
1 -> not active
2 -> error
```

---

## 24. Daemon design

`sermod` should:

```text
1. Load global config.
2. Load and resolve profiles/services.
3. Detect service backend.
4. Start scheduler.
5. For each service every interval:
   - run checks
   - evaluate guards
   - evaluate remediation rules
   - if a remediation rule fires, consult the service remediation policy
     (cooldown, max_actions); if suppressed, log and skip the action
   - execute safe operation through the shared engine only if allowed
   - update remediation state (LastActionAt, RecentActions, backoff)
   - persist in-memory rule state
   - log event, recording whether the action was executed or suppressed and why
6. Handle SIGTERM cleanly.
7. Handle SIGHUP by reloading config later; MVP may log unsupported.
```

Initial `sermod` command:

```bash
sermod run --config /etc/sermo/sermo.yml
```

Optional foreground mode only for MVP. Packaging can run it as a normal daemon under systemd/OpenRC later.

---

## 25. Example profile: Apache

```yaml
kind: profile
name: apache
type: webserver

service:
  name: apache2
  backend: auto

aliases:
  systemd:
    - apache2.service
    - httpd.service
  openrc:
    - apache2
    - apache

variables:
  binary: /usr/sbin/apache2
  user: www-data
  host: 127.0.0.1
  port: 80
  health_path: /

commands:
  version:
    command: ["apachectl", "-v"]

preflight:
  config:
    type: command
    command: ["apachectl", "configtest"]
    timeout: 10s

  libraries:
    type: libraries
    binary: "${binary}"
    optional: true

processes:
  main:
    type: command_match
    exe: "${binary}"
    user: root

  workers:
    type: command_match
    exe: "${binary}"
    user: "${user}"

checks:
  service:
    type: service
    expect: active

  http:
    type: http
    url: "http://${host}:${port}${health_path}"
    expect_status: 200
    timeout: 5s

stop_policy:
  graceful_timeout: 30s
  term_timeout: 10s
  kill_timeout: 5s
  force_kill: true
  kill_only_if:
    users: ["${user}", root]
    exe_any:
      - "${binary}"

rules:
  - name: block-restart-if-config-invalid
    type: guard
    blocks: [restart, start]
    if:
      failed:
        check: config
    then:
      action: block
      message: "Apache configuration is invalid"

  - name: restart-if-http-failed
    type: remediation
    if:
      failed:
        check: http
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart
```

---

## 26. Example service: Apache main

```yaml
kind: service
name: apache-main
uses: apache

variables:
  health_path: /health

checks:
  http:
    url: http://127.0.0.1/health
    expect_status: 200

rules:
  - name: restart-if-http-failed
    type: remediation
    if:
      failed:
        check: http
    for:
      cycles: 5
      mode: consecutive
    then:
      action: restart
```

---

## 27. Example profile: MySQL

```yaml
kind: profile
name: mysql
type: database

service:
  name: mysql
  backend: auto

variables:
  binary: /usr/sbin/mysqld
  clientadmin: /usr/bin/mysqladmin
  user: mysql
  host: 127.0.0.1
  port: 3306
  pidfile: /run/mysqld/mysqld.pid

commands:
  version:
    command: ["${binary}", "--version"]

preflight:
  binary:
    type: binary
    path: "${binary}"

  config:
    type: command
    command: ["${binary}", "--validate-config"]
    timeout: 15s

  libraries:
    type: libraries
    binary: "${binary}"

processes:
  pidfile:
    type: pidfile
    path: "${pidfile}"

  mysqld:
    type: command_match
    exe: "${binary}"
    user: "${user}"

checks:
  service:
    type: service
    expect: active

  tcp:
    type: tcp
    host: "${host}"
    port: "${port}"
    timeout: 3s

  ping:
    type: command
    command: ["${clientadmin}", "ping"]
    timeout: 5s

  backup-lock:
    type: file_exists
    path: /run/sermo/locks/mysql.backup.lock

stop_policy:
  graceful_timeout: 120s
  term_timeout: 60s
  force_kill: false
  kill_only_if:
    users: ["${user}"]
    exe_any:
      - "${binary}"

policy:
  cooldown: 15m
  max_actions: 2
  max_actions_window: 1h

rules:
  - name: block-restart-if-config-invalid
    type: guard
    blocks: [restart, start]
    if:
      failed:
        check: config
    then:
      action: block
      message: "MySQL configuration is invalid"

  - name: block-restart-during-backup
    type: guard
    blocks: [restart, stop]
    if:
      active:
        check: backup-lock
    then:
      action: block
      message: "MySQL backup lock is active"

  - name: restart-if-tcp-failed
    type: remediation
    if:
      failed:
        check: tcp
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart

  - name: restart-if-ping-failed
    type: remediation
    if:
      failed:
        check: ping
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart

  - name: restart-if-memory-high
    type: remediation
    if:
      metric:
        scope: service
        name: memory
        op: ">"
        value: 40%
    within:
      cycles: 15
      min_matches: 5
    then:
      action: restart
```

---

## 28. Example profile: Redis

```yaml
kind: profile
name: redis
type: cache

service:
  name: redis
  backend: auto

variables:
  binary: /usr/bin/redis-server
  cli: /usr/bin/redis-cli
  user: redis
  host: 127.0.0.1
  port: 6379
  pidfile: /run/redis/redis.pid

commands:
  version:
    command: ["${binary}", "--version"]

preflight:
  binary:
    type: binary
    path: "${binary}"

  libraries:
    type: libraries
    binary: "${binary}"

processes:
  pidfile:
    type: pidfile
    path: "${pidfile}"

  redis:
    type: command_match
    exe: "${binary}"
    user: "${user}"

checks:
  service:
    type: service
    expect: active

  tcp:
    type: tcp
    host: "${host}"
    port: "${port}"
    timeout: 2s

  ping:
    type: command
    command: ["${cli}", "-h", "${host}", "-p", "${port}", "ping"]
    timeout: 3s

stop_policy:
  graceful_timeout: 30s
  term_timeout: 15s
  force_kill: false

rules:
  - name: restart-if-tcp-failed
    type: remediation
    if:
      failed:
        check: tcp
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart

  - name: restart-if-ping-failed
    type: remediation
    if:
      failed:
        check: ping
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart
```

---

## 29. Example clone

```yaml
kind: service
name: redis-cache
clone: redis-main

service:
  name: redis-cache

variables:
  port: 6380
  pidfile: /run/redis-cache/redis.pid

checks:
  tcp:
    host: 127.0.0.1
    port: 6380

  ping:
    command: ["/usr/bin/redis-cli", "-p", "6380", "ping"]
```

---

## 30. Config validation requirements

`sermoctl config validate` must check:

```text
- YAML syntax is valid.
- Each document has kind and name.
- Service names are unique.
- Profile names are unique.
- uses points to an existing profile.
- clone points to an existing service.
- Clone cycles are rejected.
- Variables referenced with ${...} exist.
- Each rule has name, if and then.
- Each condition node has exactly one condition/operator.
- for.cycles > 0.
- within.cycles > 0.
- within.min_matches > 0 and <= within.cycles.
- A rule cannot define both for and within in MVP.
- All check references point to existing checks or preflight checks.
- backend is one of auto, systemd, openrc.
- stop_policy.force_kill=true requires kill_only_if.
- kill_only_if must define at least users or exe_any.
- command checks and inline command conditions use array form, not shell string.
- inline command conditions must declare a timeout.
- then.action is one of restart, start, stop, alert, block (exec is post-MVP).
- guard rules must use action block; only guard rules may use block.
- block and alert actions require a non-empty message.
- type: guard requires a non-empty blocks list; non-guard rules must not set blocks.
- policy.cooldown, if set, must be a valid non-negative duration.
- policy.max_actions, if set, must be > 0 and requires policy.max_actions_window.
- policy.max_actions_window, if set, must be a valid positive duration.
- policy.backoff, if set, requires initial > 0 and max >= initial.
- After variable expansion, port fields resolve to an integer in 1..65535.
- After variable expansion, expect_status resolves to a valid HTTP status integer.
- metric value parses as a number with an optional trailing "%".
- any field carrying ${var} must parse to its declared target type after expansion.
- metric scope is one of service or system; default is service.
- metric name exists in the catalog for its scope (section 12).
- a scope: system metric must not appear in a remediation rule; it is allowed
  only in rules whose action is alert.
```

Example error output:

```text
ERROR mysql-main:
  variable ${pidfile} used in processes.pidfile.path but not defined

ERROR apache-main:
  rule restart-if-http-failed references unknown check http-health

ERROR redis-cache:
  clone cycle detected: redis-cache -> redis-main -> redis-cache
```

---

## 31. Output examples

### Backend

```bash
sermoctl backend
```

```text
systemd
```

JSON:

```json
{
  "backend": "systemd"
}
```

### Status

```bash
sermoctl status mysql
```

```text
mysql active backend=systemd service=mysql.service
```

JSON:

```json
{
  "service": "mysql",
  "backend": "systemd",
  "status": "active",
  "unit": "mysql.service"
}
```

### Blocked restart

```bash
sermoctl restart mysql
```

```text
BLOCKED mysql restart
reason: MySQL backup lock is active
```

JSON:

```json
{
  "service": "mysql",
  "action": "restart",
  "status": "blocked",
  "reason": "MySQL backup lock is active"
}
```

---

## 32. Testing strategy

Unit tests:

```text
internal/config:
  - merge maps recursively
  - scalar override
  - enabled:false handling
  - variable expansion
  - missing variable detection
  - clone cycle detection
  - flexible scalar parsing (port/expect_status as int, quoted string or ${var})
  - metric value parsing (percentage vs absolute, invalid value rejected)
  - reject scope: system metric used in a remediation rule
  - unknown metric name for the declared scope is rejected
  - block/alert action requires a message; guard requires a blocks list

internal/rules:
  - and/or/not evaluation
  - failed check evaluation
  - metric comparison
  - for consecutive windows
  - within sliding windows
  - cooldown suppression of repeated remediation
  - max_actions rate limiting within window
  - a probe shared by several rules runs at most once per cycle (memoization)

internal/servicemgr:
  - systemd unit normalization
  - backend detection with fake paths/commands
  - openrc status parsing

internal/process:
  - pidfile parsing
  - process selector matching
  - kill safety selector validation

internal/operation:
  - restart blocked by guard
  - restart blocked by preflight failure
  - restart blocked by active lock
  - residual process handling with force_kill=false
```

Integration tests:

```text
- Fake service manager commands using temporary PATH.
- Fake systemctl and rc-service scripts.
- Temporary config tree.
- sermoctl config validate.
- sermoctl config render.
- sermoctl restart with guard blocking.
```

Do not require real root or real systemd/OpenRC in unit tests.

---

## 33. Implementation phases for Codex

### Phase 1: Skeleton and CLI

Implement:

```text
- go.mod
- cmd/sermoctl
- cmd/sermod
- cobra root command
- version command
- backend command
- basic logging
```

Acceptance:

```bash
go test ./...
go run ./cmd/sermoctl backend --backend auto
```

### Phase 2: Service manager wrapper

Implement:

```text
- servicemgr.Manager
- systemd exec backend
- OpenRC backend
- autodetection
- status/start/stop/restart commands
```

Acceptance:

```bash
sermoctl --backend systemd status nginx
sermoctl --backend openrc status nginx
```

Tests must use fake commands instead of real init systems.

### Phase 3: Config loader and renderer

Implement:

```text
- YAML models
- load global config
- load profiles
- load enabled services
- merge rules
- variable expansion
- config validate
- config render
```

Acceptance:

```bash
sermoctl config validate --config ./configs/sermo.yml
sermoctl config render apache-main --config ./configs/sermo.yml
```

### Phase 4: Check runner

Implement:

```text
- tcp check
- http check
- command check
- service check
- file_exists check
- binary check
- libraries check
```

Acceptance:

```bash
sermoctl preflight apache-main --config ./configs/sermo.yml
```

### Phase 5: Rule evaluator

Implement:

```text
- Condition AST
- and/or/not
- failed/active check references
- inline tcp condition
- metric condition with scope (service default) and the system-in-remediation
  validation rule; full service metric collection can land with phase 7
- for consecutive window
- within sliding window
```

Acceptance:

```text
Unit tests prove the three example Monit-like rules work.
Config validation rejects a scope: system metric used in a remediation rule.
```

Note: service-scoped metric collection builds on process discovery (phase 7) and
the `internal/metrics` collectors. In phase 5 the evaluator can read metric
values from an injected collector interface so rules and validation are testable
before the real collector exists.

### Phase 6: Operation engine

Implement:

```text
- restart flow
- preflight before restart
- guard blocking
- internal operation lock
- result output
```

Acceptance:

```bash
sermoctl restart mysql-main --config ./configs/sermo.yml
```

must block if the config preflight fails or backup lock exists.

### Phase 7: Process discovery and safe kill policy

Implement:

```text
- pidfile discovery
- procfs command_match discovery
- residual process detection
- force_kill=false behavior
- kill_only_if validation
```

Acceptance:

```bash
sermoctl processes mysql-main --config ./configs/sermo.yml
```

### Phase 8: Daemon scheduler

Implement:

```text
- sermod run
- periodic check execution
- rule evaluation
- remediation using operation engine
- graceful shutdown
```

Acceptance:

```bash
sermod run --config ./configs/sermo.yml
```

### Phase 9: Packaging examples

Implement:

```text
- packaging/systemd/sermod.service
- packaging/openrc/sermod
- README install section
```

---

## 34. Security rules

Hard rules:

```text
1. Never restart if preflight fails and security.require_preflight_before_restart=true.
2. Never restart or stop if a guard blocks the action.
3. Never SIGKILL by default.
4. Never kill by process name only.
5. force_kill=true requires kill_only_if.
6. Commands must be array form, not shell string.
7. Avoid invoking shell unless explicitly configured later.
8. Every action must produce a structured event.
9. sermod and sermoctl must share the same operation code path.
10. Automatic remediation must respect the service cooldown/rate-limit policy and
    must never enter a restart loop. Manual operator actions are exempt from
    cooldown but still subject to locks, guards and preflight.
11. Remediation rules must trigger on service-scoped metrics only. A system-wide
    metric must never restart, start or stop an individual service; it may only
    drive an alert.
12. Rule conditions are read-only predicates evaluated at most once per cycle.
    A condition must never change system state; mutation belongs to actions, not
    to conditions.
```

---

## 35. Event model

Package: `internal/events`

Event structure:

```go
type Event struct {
    Time     time.Time      `json:"time"`
    Service  string         `json:"service"`
    Action   string         `json:"action,omitempty"`
    Type     string         `json:"type"`
    Status   string         `json:"status"`
    Message  string         `json:"message,omitempty"`
    Backend  string         `json:"backend,omitempty"`
    Rule     string         `json:"rule,omitempty"`
    Data     map[string]any `json:"data,omitempty"`
}
```

Initial sink:

```text
log/slog to stdout/stderr
```

Later sinks:

```text
JSON file
syslog
Prometheus metrics
webhook
```

---

## 36. References

- Cobra: https://github.com/spf13/cobra
- goccy/go-yaml: https://github.com/goccy/go-yaml
- go-systemd: https://github.com/coreos/go-systemd
- prometheus/procfs: https://github.com/prometheus/procfs
- fsnotify: https://github.com/fsnotify/fsnotify
- systemctl manual: https://www.freedesktop.org/software/systemd/man/systemctl.html
- OpenRC quickstart examples: [https://wiki.alpinelinux.org/wiki/OpenRC](https://github.com/OpenRC/openrc/blob/master/user-guide.md)
