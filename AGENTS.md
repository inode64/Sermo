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
│   ├── daemons/
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
├── catalog/
│   ├── services/   # long-running daemons (apache.yml, mysql.yml, redis.yml, ...)
│   ├── apps/       # tools/runtimes (java.yml, git.yml, ...)
│   └── libs/       # shared libraries (glibc.yml, pam.yml)
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
│   ├── daemons.md
│   └── safety.md
├── go.mod
├── go.sum
├── Makefile
└── README.md
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

## Reuse and shared behavior

Treat reuse as a project rule, not just a style preference.

1. Before adding a new helper, parser, validator, runner or UI/backend adapter,
   search for existing code that already solves the same problem and extend it
   when that keeps the ownership boundary clear.
2. Do not duplicate validation, parsing, comparison, notification, monitoring or
   action dispatch logic across `sermod`, `sermoctl`, web, watches and daemons.
   Prefer one shared implementation with narrow adapters at the edges.
3. When a new check, option, monitor flag, notification behavior or web action is
   generally useful to both host `watches:` and service daemons, implement it for
   both surfaces in the same change unless there is a documented reason not to.
4. If a feature intentionally applies only to watches or only to daemons, state
   that limitation in code comments where the dispatch/validation decision lives
   and in the user docs.
5. Keep examples and documentation in step with shared behavior: update
   `docs/configuration.md`, `docs/rules.md`, daemon docs and `configs/sermo.yml`
   whenever the YAML surface changes.

## Native Go, not external processes

**Always implement functionality with native Go — the standard library, or
`golang.org/x/sys` / `golang.org/x/net` — and avoid spawning external processes or
scripts wherever possible.** Reading `/proc`/`/sys`, syscalls (statfs, uname),
TLS/x509, SMTP, HTTP, ELF, etc. are all native; reach for `os/exec` only when
there is genuinely no native equivalent.

- **Never use a shell.** All process execution goes through an explicit argv
  (`execx.Runner` / `os/exec` with name+args); no `sh -c`, no string command
  lines, so check/hook commands can't be shell-injected. Every external command
  carries a timeout (rule 3).
- **Justified external-process exceptions** (do not add more without a clear
  reason, and document them here):
  - The **service-manager backends** (`systemctl`, `rc-service`): systemd/OpenRC
    have no native Go API in scope, and pulling in D-Bus is a heavier dependency.
  - **User-configured commands**: `command` checks, watch `hook`s, and the
    `sermoctl lock -- COMMAND` wrapper — running an external program is their
    whole purpose. Argv only.
  - The **`libraries` check's `ldd`**: it queries the dynamic loader; reimplementing
    that from `debug/elf` would be unreliable.
  - The **disk watch's `then.expand` action** (`internal/volume`): LVM and
    filesystem growth have no native Go API, so it shells out to `lvs`/`vgs`/
    `lvextend` and `resize2fs`/`xfs_growfs`/`btrfs`. The orchestration —
    resolving the path's mount (native `/proc/mounts`) and LV, checking VG free,
    capping the request, sequencing extend-then-grow — is all Go.
- When you need OS information, prefer a syscall over a tool: e.g. architecture is
  read with `unix.Uname` (not `uname -m`), filesystem usage with `syscall.Statfs`
  (not `df`), process data from `/proc` (not `ps`).

## Security and safety invariants

These rules are mandatory.

1. Never kill processes by name only.
2. Never use `SIGKILL` unless the daemon definition explicitly allows it.
3. A `SIGKILL` policy must include a restrictive `kill_only_if` clause.
4. Process matching must validate at least `exe` and `user`; prefer `pidfile` or `cgroup` as additional evidence. `exe` is the resolved `/proc/<pid>/exe` path matched exactly (never argv[0]/cmdline, never a substring); an unresolvable `exe` never matches. See `implementation-spec.md` section 21.
5. Never restart, start or stop a service when a matching guard blocks the action.
6. Never restart or start when required preflight checks fail.
7. Never perform service actions without a timeout.
8. Never enter a restart loop. Automatic remediation must honor the resolved
   per-service `policy` block; `policy.cooldown` is mandatory and positive after
   config resolution, with optional max_actions/backoff; see
   `implementation-spec.md` section 16, "Remediation policy". Cooldown is
   decided by the daemon's rule evaluation before the shared engine runs. Manual
   operator commands are exempt from cooldown but still subject to locks, guards
   and preflight.
9. Always log whether an action was executed or blocked, and why.
10. Database daemons must default to conservative stop policies.
11. Auto-remediation must use the same safe operation path as manual `sermoctl` commands.
12. Only residuals that exactly match `kill_only_if` are ever signaled; a residual
    that does not match (or has an unresolvable exe) is reported, never killed. Any
    remaining residual makes the result `orphan_processes`, and a failed stop must
    not automatically start the service unless policy explicitly allows it.
13. Remediation must trigger on service-scoped metrics only. A system-wide metric
    (total memory, total CPU, load) must never restart, start or stop an
    individual service; it may only drive an alert.
14. Rule conditions are read-only predicates, evaluated at most once per cycle. A
    condition must never mutate system state; mutation belongs to actions.
15. Locks are acquired atomically (O_CREAT|O_EXCL) and bounded by a TTL. A lock is
    honored only while active; an expired lock, or one whose owner PID is dead
    (checked via owner_start_ticks to survive PID reuse), is stale and must be
    reclaimed through a logged path, never silently overwritten. Named runtime
    lock files use `<service>[.<name>].lock` under `<paths.runtime>/locks`
    (default `/run/sermo/locks`); the `sermoctl lock` commands that
    create/release them are post-MVP. The internal operation lock uses the
    separate path `<paths.runtime>/ops/<service>.lock` so it cannot collide with
    a user lock named `op`. `paths.locks` and `/etc/sermo/locks.d` have no MVP
    semantics. See `implementation-spec.md` sections 18 and 20.
16. The scheduler runs one independent worker per service; a long operation
    (a multi-minute restart) on one service must never block monitoring of
    another. Never serialize all services through a single loop. Mass restarts
    are bounded by a global operation semaphore, and concurrent check execution
    across all services is bounded by `engine.max_parallel_checks` (a separate
    global pool). See sections 12 and 24.

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

`Manager.Restart` may exist as a backend primitive, but Sermo's safe operation
engine must not use it for `sermoctl restart` or automatic remediation. A safe
restart is always `Stop` -> residual process handling -> `Start`, so any
`orphan_processes` result aborts before the service is started again.

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
daemons
services
clones
overrides
variables
aliases          # per-backend candidate unit names (see spec section 11)
commands         # optional informational commands, never auto-run
checks
preflight        # entries may set optional: true (best-effort, non-blocking)
postflight       # checks after start/restart; required failures return postflight_failed
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

A service can use a daemon:

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
checks/preflight/postflight/processes/rules: keyed by name
enabled: false disables inherited item
delete: true removes inherited item
```

Resolution precedence, low to high:

```text
global defaults  <  daemon (uses) or clone source  <  service overrides
```

The global `defaults` block (stop_policy, policy, rule_window) is merged in as the
base layer of every service, so a field omitted everywhere falls back to it.
Engine-wide settings (interval, max_parallel_checks, default_timeout, backend) are
daemon config and are NOT merged into services. Variable expansion runs once,
after all merging. See `implementation-spec.md` section 8.
The effective `defaults.policy.cooldown` is required and must be positive, and a
service/daemon override may only replace it with another positive duration.
`paths.runtime` is the single runtime root (default `/run/sermo`). Named runtime
locks live under `<paths.runtime>/locks` and operation locks under
`<paths.runtime>/ops`. Do not use `paths.locks` or `/etc/sermo/locks.d` in the
MVP; active locks are runtime state, not configuration files.

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
sermoctl config diff BASE SERVICE
```

`config render` must show the final resolved service and the source files used.

`config diff` compares two resolved services line-by-line for pre-deploy review.

## Check types are unified across checks and watches

There is **one set of check types**, shared by a service's
`checks:`/`preflight:`/`postflight:` (referenced from rules) and by host
`watches:` (which fire a hook). The build path is already shared
(`internal/checks.buildCheck`, used by both `Build` and `BuildInline`).

**Standing rule — whenever you add a new check type, integrate it with the
existing checks and keep the docs in step:**

- If it is a single-shot check (`Check.Run → Result`), make it work in **both**
  places: add it to `knownCheckTypes` (service checks/rules) *and* ensure the
  watch path accepts it (`internal/app/watch_build.go`; service checks usable as
  watches go through `buildSingleWatch`). Validate its fields in **one** shared
  validator (`internal/config/validate.go`) called from both
  `validateCheckSection` and `validateWatches`.
- Decide its firing polarity: condition-style (`OK == true` means the alert
  condition, e.g. disk/load/metric/count) vs health-style (`OK == true` means
  healthy, e.g. tcp/http). `isHealthCheckType` drives whether a watch fires its
  hook on failure. Keep that list current.
- Multi-target / multi-hook checks (`net`, `icmp`, `swap` metric expansion;
  `file`, `process`) are watch-only by design — they are not single-shot. Note
  the exclusion rather than forcing them into `checks:`.
- Always update `docs/rules.md` (the check-type table + the "shared types"
  note) and `docs/configuration.md` (host watches), plus a `configs/sermo.yml`
  example, in the same change.

## Notifications are pluggable

Notifications go to named, typed **notifiers** under the global `notifiers`
section (`internal/notify`), referenced by name from a watch's `then.notify`
list. A watch's `then` block may have a `hook`, a `notify` list, or both (at least
one). Implemented transports: `email` (SMTP) and `slack` (incoming webhook).

**Standing rule — keep notifiers extensible; adding a transport (teams, …) must
not require changes outside `internal/notify` and the docs:**

- Register the new type's constructor in `internal/notify` (the `builders` map)
  and implement the `Notifier` interface (`Name`/`Type`/`Send`). Use only the Go
  standard library where feasible (the project avoids new dependencies).
- Add its config validation to `validateNotifiers` (internal/config/validate.go)
  and keep `notify.SupportedTypes()` in step.
- The watch/dispatch side is transport-agnostic (it addresses every notifier
  through the interface) — do not special-case a transport there.
- Update `docs/configuration.md` (Notifications) and a `configs/sermo.yml`
  example in the same change.

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

Rate metrics (cpu, total_cpu) are a delta between two samples, so the
`internal/metrics` collector is stateful and sampled once per cycle; on the first
cycle a rate is not-ready and its condition evaluates to false (no remediation on
a warm-up value). Instantaneous metrics (memory, process_count, load) need no
history. A metric `Check` stays single-shot — it reads the collector, which holds
the state. See section 12.

Guards must be evaluated before remediation rules.

A rule's `then:` is a single `Action { action, message, ... }` in the MVP (the
`RuleType` and `Action` Go types are defined in `implementation-spec.md` section
16). `block` and `alert` actions require a `message` — it is the reason shown to
the operator and recorded in the event. Only guard rules use `action: block`, and
a guard must list the actions it blocks under `blocks:`.

## Operation flow

Safe restart flow:

```text
1. Load resolved service definition.
2. Detect backend.
3. defer: emit exactly one event from the final result (registered first).
4. Acquire internal operation lock at `<paths.runtime>/ops/<service>.lock`
   (default `/run/sermo/ops/<service>.lock`; atomic; fail fast if held by a live
   owner).
5. defer: release the lock (registered only after a successful acquire).
6. Evaluate blocking locks.        # any of 6-13 may return early
7. Run required preflight checks.
8. Evaluate guard rules.
9. For restart, execute Stop through servicemgr.
10. Wait for graceful stop where applicable.
11. Discover residual processes and apply stop_policy.
12. If any residual remains, return `orphan_processes` and do not start.
13. After a clean stop, reconcile init state (`ResetState`: systemd
    `reset-failed`, OpenRC `zap`) so a stuck/failed marker can't disagree with
    the processes. Best effort — never fails an already-successful stop.
14. Execute Start through servicemgr only after the stop phase is clean.
15. Run postflight checks and return the result.
```

The two deferred steps mean the event always fires and the lock is always
released when held, so a blocked, failed or panicking operation never leaks the
lock and always emits exactly one event. Use Go `defer` in that order; never
repeat release/record at each early return. See `implementation-spec.md`
section 18.

Step 6 ("evaluate blocking locks") and step 8 ("evaluate guard rules") are two
distinct, complementary mechanisms — not two ways to do the same thing. Step 6
blocks automatically on Sermo's own named runtime lock files; step 8 blocks on
guard rules over checks of foreign signals Sermo does not own (a backup process,
a foreign flag file). A `file_exists`/`process` check must point at a foreign
signal, never at a file under `<paths.runtime>/locks/` (default
`/run/sermo/locks/`; that would duplicate step 6).
The `sermoctl lock` creation/release commands are post-MVP; MVP CLI scope only
requires `sermoctl locks SERVICE` for reporting. See `implementation-spec.md`
section 20.

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

For the MVP, `sermoctl locks SERVICE` is a reporting command. Commands that
create or release named runtime locks (`sermoctl lock ...`, `sermoctl lock
acquire`, `sermoctl lock release`) are post-MVP.

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
daemon uses resolution
service clone resolution
cycle detection
variable expansion
backend detection with mocked commands
systemd degraded detection
both-present backend detection prefers active init
systemd service name normalization
OpenRC status parsing
rule engine and/or/not
for cycles
within cycles
guard blocking
preflight blocking
postflight failure reporting
lock blocking
operation lock path does not collide with named runtime locks
remediation cooldown and rate limiting
positive resolved policy.cooldown validation
paths.runtime lock directory derivation and paths.locks rejection
safe restart sequencing
restart never starts after orphan_processes
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
