# Sermo Codex Instructions

## Project summary

Sermo is a safe service monitoring and control system for Linux.

- Project: `Sermo`
- Daemon: `sermod`
- CLI: `sermoctl`
- Default config directory: `/etc/sermo`
- Default runtime directory: `/run/sermo`
- Default state directory: `/var/lib/sermo`
- Primary target OS: Linux
- Initial init/service backends: systemd and OpenRC

Sermo must provide a portable, safe abstraction over Linux service managers. Users and scripts should be able to call `sermoctl restart mysql` without needing to know whether the system uses `systemctl` or `rc-service`.

## Core goals

Implement Sermo as two binaries:

```text
sermod    # daemon that monitors services and applies safe remediation
sermoctl  # CLI wrapper for status, preflight, locks, config rendering and actions
```

The daemon and CLI must share the same internal engine for:

```text
service manager detection
config loading and rendering
checks
rules
guards
locks
safe start/stop/restart operations
process discovery
```

Do not create two separate implementations of service actions.

## Non-goals for the MVP

Do not implement these unless explicitly requested:

```text
web UI
distributed cluster mode
remote API authentication
plugin ABI
database persistence
complex notification integrations
multi-tenant RBAC
systemd D-Bus integration beyond optional scaffolding
```

Prefer a reliable local CLI/daemon first.

## Repository layout

The canonical repository layout is defined in `implementation-spec.md` section 5.
That document is the single source of truth; the tree below must stay in sync with
it. If you need to change the layout, change `implementation-spec.md` section 5
first and then update this section to match.

Use this structure unless there is a strong reason to change it:

```text
sermo/
├── cmd/
│   ├── sermod/
│   │   └── main.go
│   └── sermoctl/
│       └── main.go
├── internal/
│   ├── app/          # daemon, scheduler and in-memory state (sermod)
│   ├── checks/       # tcp, http, command, service, file, process, metric checks
│   ├── cli/          # sermoctl command implementations
│   ├── config/       # YAML model, loader, merge, render, variables, validate
│   ├── events/       # structured event model and logger
│   ├── execx/        # external command runner with mandatory timeouts
│   ├── locks/        # internal runtime locks and external lock checks
│   ├── metrics/      # cpu/memory collectors
│   ├── operation/    # safe start/stop/restart engine (shared by sermod + sermoctl)
│   ├── preflight/    # preflight runner reusing the check runner
│   ├── process/      # discovery, procfs, tree, signal, residual handling
│   ├── profiles/     # profile registry, resolver and sources
│   ├── rules/        # condition AST, evaluator, windows, rule state
│   └── servicemgr/   # backend detection, systemd_exec, openrc
├── profiles/
│   ├── apache.yml
│   ├── mysql.yml
│   ├── redis.yml
│   └── php-fpm.yml
├── configs/
│   └── sermo.yml
├── packaging/
│   ├── systemd/
│   │   └── sermod.service
│   └── openrc/
│       └── sermod
├── docs/
├── .agents/
│   └── skills/
└── AGENTS.md
```

Notes on package responsibilities:

- Safe operation logic (the old `safety/` idea) lives inside `operation/` and
  `process/` (residual detection, `kill_only_if` validation, signal escalation),
  not in a separate package.
- Cooldown/backoff and other action-gating policy (the old `policy/` idea) is
  tracked in `rules/` rule state and enforced by `operation/`.
- The daemon lifecycle (the old `supervisor/` idea) is `app/` (`daemon.go`,
  `scheduler.go`, `state.go`).

## Dependencies

Keep the MVP dependency set small.

Allowed initial dependencies:

```text
github.com/spf13/cobra
github.com/goccy/go-yaml
github.com/prometheus/procfs
```

Allowed later dependencies:

```text
github.com/coreos/go-systemd/v22
github.com/prometheus/client_golang
github.com/fsnotify/fsnotify
```

Use the Go standard library where it is sufficient.

## Go conventions

Follow these rules:

1. Write idiomatic, simple Go.
2. Use `context.Context` for every operation that can block.
3. Every external command must have a timeout.
4. Wrap errors with context using `fmt.Errorf("...: %w", err)`.
5. Prefer small interfaces at package boundaries.
6. Keep exported APIs minimal.
7. Use table-driven tests for config, rules and safety logic.
8. Do not panic in normal error paths.
9. Avoid global mutable state.
10. Avoid package names that conflict with standard concepts, such as `init`.

## Security and safety invariants

These rules are mandatory.

1. Never kill processes by name only.
2. Never use `SIGKILL` unless the service profile explicitly allows it.
3. A `SIGKILL` policy must include a restrictive `kill_only_if` clause.
4. Process matching must validate at least `exe` and `user`; prefer `pidfile` or `cgroup` as additional evidence.
5. Never restart, start or stop a service when a matching guard blocks the action.
6. Never restart or start when required preflight checks fail.
7. Never perform service actions without a timeout.
8. Never enter a restart loop. Automatic remediation must honor the per-service
   `policy` block (cooldown, optional max_actions/backoff); see
   `implementation-spec.md` section 16, "Remediation policy". Cooldown is decided
   by the daemon's rule evaluation before the shared engine runs. Manual operator
   commands are exempt from cooldown but still subject to locks, guards and
   preflight.
9. Always log whether an action was executed or blocked, and why.
10. Database profiles must default to conservative stop policies.
11. Auto-remediation must use the same safe operation path as manual `sermoctl` commands.
12. A failed stop with residual processes must not automatically start the service unless policy explicitly allows it.
13. Remediation must trigger on service-scoped metrics only. A system-wide metric
    (total memory, total CPU, load) must never restart, start or stop an
    individual service; it may only drive an alert.
14. Rule conditions are read-only predicates, evaluated at most once per cycle. A
    condition must never mutate system state; mutation belongs to actions.
15. Locks are acquired atomically (O_CREAT|O_EXCL) and bounded by a TTL. A lock is
    honored only while active; an expired lock, or one whose owner PID is dead
    (checked via owner_start_ticks to survive PID reuse), is stale and must be
    reclaimed through a logged path, never silently overwritten. Lock files are
    named `<service>[.<name>].lock`. See `implementation-spec.md` section 20.
16. The scheduler runs one independent worker per service; a long operation
    (a multi-minute restart) on one service must never block monitoring of
    another. Never serialize all services through a single loop. Mass restarts
    are bounded by a global operation semaphore. See section 24.

## Service manager abstraction

Implement service management behind this conceptual interface:

```go
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

Backends:

```text
auto
systemd
openrc
```

Autodetection order:

1. Explicit CLI flag.
2. Environment variable.
3. Config value.
4. Automatic detection.

For automatic detection:

```text
systemd:
  - `systemctl` exists
  - `/run/systemd/system` exists
  - tolerate `degraded` from `systemctl is-system-running`

openrc:
  - `rc-service` exists
  - `/run/openrc` exists, or `rc-status` works
```

If both appear available, prefer the active init system rather than the mere presence of a command.

## Config model

Use YAML.

Sermo supports:

```text
global config
profiles
services
clones
overrides
variables
aliases          # per-backend candidate unit names (see spec section 11)
commands         # optional informational commands, never auto-run
checks
preflight        # entries may set optional: true (best-effort, non-blocking)
processes
rules
guards
locks
stop_policy
policy
```

Prefer maps keyed by name over lists for mergeable sections:

```yaml
checks:
  http:
    type: http
    url: http://127.0.0.1/health
```

Avoid this for mergeable sections:

```yaml
checks:
  - name: http
    type: http
```

A service can use a profile:

```yaml
kind: service
name: apache-main
uses: apache
```

A service can clone another service:

```yaml
kind: service
name: redis-cache
clone: redis-main
```

Merge rules:

```text
scalars: override
maps: recursive merge
checks/preflight/processes/rules: keyed by name
enabled: false disables inherited item
delete: true removes inherited item
```

Numeric fields that may also be written as a string or carry a `${var}` (for
example `port` and `expect_status`) use a tolerant scalar type that accepts an
int or a string and is parsed to its target type after variable expansion. The
metric `value` is a string with an optional trailing `%`. See
`implementation-spec.md` section 10, "Typed fields and variable interaction".

The daemon should consume a fully resolved, flat configuration. Do not make the daemon reason about inheritance at runtime.

## Required config commands

Implement these early:

```text
sermoctl config validate
sermoctl config render SERVICE
```

`config render` must show the final resolved service and the source files used.

`sermoctl config diff BASE SERVICE` is planned but post-MVP; see
`implementation-spec.md` section 23.

## Rule engine

Rules use a structured YAML condition tree.

Support:

```text
and
or
not
failed
active
metric
service
process
file
command
```

Support windows:

```text
for cycles: consecutive matches
within cycles: rolling window with min_matches
```

Example:

`rules` is a map keyed by rule name (like `checks`/`preflight`/`processes`), not
a list; the key is the rule name and there is no inner `name` field. This lets a
service override or disable a single inherited rule.

```yaml
rules:
  restart-if-port-failed:
    type: remediation
    if:
      failed:
        check: tcp-783
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart
```

Conditions are read-only predicates. The evaluator runs all declared checks and
any inline condition probes once per cycle and caches the results, so a probe
shared by several rules executes at most once per cycle. Inline `command`
conditions must be side-effect-free, array form, with a timeout; prefer declaring
anything expensive as a named check and referencing it with `failed`/`active`.

Metric conditions carry a `scope` (`service`, the default, or `system`). Service
metrics measure only the monitored service (its discovered process set or
cgroup); system metrics measure the whole machine. Remediation rules must use
service-scoped metrics only — a system-wide metric may drive an `alert` but must
never restart, start or stop an individual service. See `implementation-spec.md`
sections 12 and 14.

Guards must be evaluated before remediation rules.

A rule's `then:` is a single `Action { action, message, ... }` in the MVP (the
`RuleType` and `Action` Go types are defined in `implementation-spec.md` section
16). `block` and `alert` actions require a `message` — it is the reason shown to
the operator and recorded in the event. Only guard rules use `action: block`, and
a guard must list the actions it blocks under `blocks:`.

## Operation flow

Safe restart flow:

```text
1. Load resolved service profile.
2. Detect backend.
3. defer: emit exactly one event from the final result (registered first).
4. Acquire internal operation lock (atomic; fail fast if held by a live owner).
5. defer: release the lock (registered only after a successful acquire).
6. Evaluate blocking locks.        # any of 6-11 may return early
7. Run required preflight checks.
8. Evaluate guard rules.
9. Execute stop/restart/start through servicemgr.
10. Wait for graceful stop where applicable.
11. Discover residual processes; apply stop_policy.
12. Run postflight checks and return the result.
```

The two deferred steps mean the event always fires and the lock is always
released when held, so a blocked, failed or panicking operation never leaks the
lock and always emits exactly one event. Use Go `defer` in that order; never
repeat release/record at each early return. See `implementation-spec.md`
section 18.

Step 6 ("evaluate blocking locks") and step 8 ("evaluate guard rules") are two
distinct, complementary mechanisms — not two ways to do the same thing. Step 6
blocks automatically on Sermo's own runtime locks (created by `sermoctl lock`);
step 8 blocks on guard rules over checks of foreign signals Sermo does not own (a
backup process, a foreign flag file). A `file_exists`/`process` check must point
at a foreign signal, never at a file under `/run/sermo/locks/` (that would
duplicate step 6). See `implementation-spec.md` section 20.

## CLI expectations

Minimum CLI commands:

```text
sermoctl backend
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl start SERVICE
sermoctl stop SERVICE
sermoctl restart SERVICE
sermoctl preflight SERVICE
sermoctl processes SERVICE
sermoctl locks SERVICE
sermoctl config validate
sermoctl config render SERVICE
```

Exit codes (canonical list and the `2` vs `78` distinction are defined in
`implementation-spec.md` section 23; keep this in sync):

```text
0   success / active / allowed
1   expected false condition, such as inactive or a failed check
2   internal or runtime error / backend not detected
64  usage error (bad flags or arguments)
75  temporarily blocked action, such as an active backup lock or guard
78  configuration invalid (syntax, schema or `config validate` failure)
```

## Testing requirements

Any change touching safety-sensitive behavior must include tests.

Required test areas:

```text
config merge
profile uses resolution
service clone resolution
cycle detection
variable expansion
backend detection with mocked commands
systemd service name normalization
OpenRC status parsing
rule engine and/or/not
for cycles
within cycles
guard blocking
preflight blocking
lock blocking
remediation cooldown and rate limiting
safe restart sequencing
process matching safety
SIGKILL policy validation
```

Use fake command runners instead of running real `systemctl`, `rc-service`, `kill` or service commands in unit tests.

## Verification before finishing a task

Run:

```bash
go test ./...
go vet ./...
```

If available, also run:

```bash
gofmt -w .
go test -race ./...
```

If a command cannot be run, state why.

## Definition of done

A task is not done unless:

```text
- code compiles
- tests were added or updated where appropriate
- safety invariants are preserved
- config examples remain valid
- CLI behavior is documented when changed
- error messages are useful to a sysadmin
```
