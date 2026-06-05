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
8. Never enter a restart loop; use cooldown/backoff.
9. Always log whether an action was executed or blocked, and why.
10. Database profiles must default to conservative stop policies.
11. Auto-remediation must use the same safe operation path as manual `sermoctl` commands.
12. A failed stop with residual processes must not automatically start the service unless policy explicitly allows it.

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
checks
rules
guards
locks
stop_policy
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

The daemon should consume a fully resolved, flat configuration. Do not make the daemon reason about inheritance at runtime.

## Required config commands

Implement these early:

```text
sermoctl config validate
sermoctl config render SERVICE
sermoctl config diff BASE SERVICE
```

`config render` must show the final resolved service and the source files used.

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

```yaml
rules:
  - name: restart-if-port-failed
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

Guards must be evaluated before remediation rules.

## Operation flow

Safe restart flow:

```text
1. Load resolved service profile.
2. Detect backend.
3. Acquire internal operation lock.
4. Evaluate blocking locks.
5. Run required preflight checks.
6. Evaluate guard rules.
7. Execute stop/restart/start through servicemgr.
8. Wait for graceful stop where applicable.
9. Discover residual processes.
10. Apply stop_policy.
11. Run postflight checks.
12. Record event.
13. Release lock.
```

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

Exit codes:

```text
0   success / active
1   expected false condition, such as inactive
2   internal error or invalid config
75  temporarily blocked action, such as active backup lock
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
