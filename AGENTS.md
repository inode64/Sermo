# Sermo Agent Instructions

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

## Code formatting (Go)

**Every Go file must be `gofmt`-clean after any modification.** Run `gofmt` on a
file whenever you change it, so the tree always conforms to the standard Go
formatting. This keeps diffs minimal and consistent with the rest of the
codebase.

```sh
gofmt -w <file.go>        # format one file
gofmt -l ./internal ./cmd # list any non-conforming files (should be empty)
```

This is enforced automatically in Claude Code: a `PostToolUse` hook in
`.claude/settings.json` runs `gofmt -w` on every `.go` file written or edited, so
formatting never drifts. If you edit Go outside Claude Code (another editor, a
script), run `gofmt -w` yourself before committing; configure your editor to
"format on save" with gofmt to make this automatic.

## Static analysis and linting (Go)

**Every modification must pass the project's static-analysis tools before it is
committed**, to comply with the programming standards. These complement `gofmt`
and catch bugs, vulnerabilities, security issues, and style drift. The binaries
live in `~/go/bin` (add it to `PATH`).

Run all of them against the whole module after any change:

```sh
export PATH="$HOME/go/bin:$PATH"

govulncheck ./...                 # known vulnerabilities (deps + std lib)
staticcheck ./...                 # correctness / bug static analysis
revive -config revive.toml ./...  # style/lint (config tuned for this repo)
golangci-lint run                 # runs gosec (security); uses .golangci.yml
```

All four must report **no findings** before committing. Notes:

- **`revive`** is driven by `revive.toml` at the repo root. It mirrors revive's
  default rule set but disables `unused-parameter`, because many methods
  implement interfaces (e.g. `web.Backend`) whose signature includes a `ctx`
  the implementation legitimately ignores; renaming those to `_` hurts
  readability. Document new exported symbols (the `exported` rule is on).
- **`gosec`** is not installed standalone in `~/go/bin`, so it is run through
  golangci-lint (which bundles it) via `.golangci.yml` at the repo root; that
  config enables only gosec. Accepted exceptions are documented there: `G115`
  (noisy integer-overflow rule on already-bounded values) and `G306` in test
  fixtures. By-design cases, such as `G204` (executing operator-configured
  commands) and intentional `0644` writes (pidfile, generated YAML), are
  suppressed at the call site with `//nolint:gosec` plus a justifying comment.
  Keep that pattern: prefer a justified inline `//nolint:gosec` over widening
  the config.
- Unlike `gofmt` (auto-applied per file by the Claude Code hook), these tools
  analyze the whole module and are too slow to run on every edit. Run them once
  before committing, or wire them into CI / a pre-commit hook.

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
   `docs/configuration.md`, `docs/rules.md`, `docs/sermo-all.yml`, daemon docs
   and `configs/sermo.yml` whenever the YAML surface changes; `docs/safety.md`
   for engine/lock/process semantics and `docs/cli.md` for commands and exit
   codes.

## Web UI cohesion

The web UI is a single embedded document, `internal/web/index.html` (HTML, CSS
and JS in one file). **Before adding or changing any UI element, find the
existing element that already solves the same problem and copy its structure,
classes and styling exactly** — do not invent a parallel way to do the same
thing. Cohesion across panels is a hard requirement, not a preference.

Concretely, every data panel is a `<details id="{name}-section">` with a
`<summary>`, an optional flex `#{name}-controls` row (search + filters + count)
and a `<table class="{name}-table">` with a sticky header. The one deliberate
distinction:

1. **Scrollable panel** — wrap the table in `<div class="table-wrap">`, which
   adds `overflow:auto; max-height:calc(100vh - 13rem)`. Used by Services (a
   long, unbounded list).
2. **Non-scrollable panel** — place the bare `<table>` directly inside the
   `<details>`, no wrapper. Used by Host watches, Events, Notifiers and
   Applications (the page scrolls as a whole instead of trapping a panel in
   its own scrollbar).

Pick the variant that matches the panel's nature and reuse it verbatim; never
hand-roll bespoke `overflow`/`max-height` rules on a single panel. When you
introduce a genuinely new pattern, document it here and in `CLAUDE.md` so the
next change can follow it.

The visual layer is a token-driven design system (June 2026 redesign):

- **Design tokens.** All colors/radii/shadows come from CSS custom properties on
  `:root` (`--bg`, `--panel`, `--text`, `--line`, `--ok`, `--warn`, `--crit`,
  `--info`, …) with a `prefers-color-scheme: dark` override block. Never
  hardcode a color in new CSS — use the tokens, deriving tints with
  `color-mix(in srgb, var(--x) N%, transparent)`. (JS-emitted inline SVG fills
  keep the GitHub-ish literal palette, which reads on both schemes.)
- **Panel cards.** Every `<details>` section (plus `#locks-section` and
  `#detail`) is styled as a card automatically — rounded border, shadow, the
  `<summary>` as header. A new section needs no extra classes.
- **Overview tiles.** The `#overview` band under the topbar is the at-a-glance
  layer: `renderOverview` (called from `renderStatus`, no extra requests)
  emits `<button class="tile" data-panel-target=…>` per vital sign, with
  `t-ok`/`t-warn`/`t-crit` accents and optional `usageBar` gauges.
- **Status pills.** `.target-state` renders states as tinted pills with a
  colored dot (`::before`, `currentColor`); `state-failed` pulses. New states
  only need a `state-<name>` color class.
- **Heartbeat strip.** `beatStrip(points)` renders the 24h availability strip
  (48 half-hour segments, `beat-ok`/`beat-warn`/`beat-bad`, hollow when
  unobserved) used in the service expansion; reuse it anywhere a compact
  availability history is needed.

**CSP and inline styles:** `style-src` deliberately carries `'unsafe-inline'`
**without** a nonce — per CSP2, a nonce in the list makes browsers ignore
`'unsafe-inline'` and silently strip every generated `style="…"` attribute
(section hiding, gauge widths). Do not "harden" style-src back to a nonce;
script-src remains nonce-strict (see `securityHeaders` in
`internal/web/server.go`).

## Wizard option selection

The wizard (`sermoctl wizard`, `internal/assist`) drives every selection through
the shared `Prompt` helpers — never hand-roll a bespoke question.

- Multi-selects use `Prompt.MultiChoose`: item numbers, the keyword `all`, or an
  option's name.
- Menus with reserved picks use `Prompt.MultiChooseKeyword`: the numbered list
  shows **only the real options**; reserved answers ride in the question hint.
- One vocabulary everywhere: `all` selects everything, `none` opts out,
  `default` inherits the global setting.
- The notifier menu lists only the notifiers defined in the config. `none` and
  `default` are accepted even when none are defined.
- `none` is **always valid** — with or without a global notify default — and
  produces a monitor-only watch (`notify: [none]`: state and events, no
  delivery). It is also rejected as a notifier name.
- Only an inert `default` (no global notify configured, no other action like
  auto-expand) makes the wizard explain why and re-ask, via the shared
  `ensureNotifyAction`. Never abort with a hard error; never hand-roll that
  validation per assistant.
- Update `docs/configuration.md` and this section when adding assistants or
  selection steps.

## Catalog: instanced systemd daemons

When a catalog daemon targets a systemd **instance** unit (`unit@instance`), do
not invent a hand-typed `${id}` variable the operator must remember to set —
derive the instance from code, reusing existing machinery:

- **Single instance keyed by host** (e.g. `ceph-mon@radon`, `ceph-mds@radon`):
  use the built-in `${hostname}` (the short hostname) — `service:
  "ceph-mon@${hostname}"`. It resolves with zero per-service config; an explicit
  `hostname` variable or `SERMO_HOSTNAME` overrides it. `${hostname}` is the short
  form, distinct from `${host}` (the bind-address fallback) — see `docs/daemons.md`.
- **Numeric multi-instance** (e.g. one OSD per device, `ceph-osd@0..N`): make the
  daemon a `%n` version template (`name: ceph-osd%n`) with `versions: { from:
  "/var/lib/ceph/osd/ceph-${n}" }`. `internal/config/versions.go` globs that path
  on the host and materializes one concrete daemon per discovered id, with `${n}`
  baked into `service: "ceph-osd@${n}"`. Honest limitation: this auto-discovers
  daemon *definitions*; the operator still enables one `kind: service` per
  instance (Sermo monitors services, not catalog daemons).

Keep `docs/daemons.md` (built-in variable table) in step when adding a built-in.

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
4. Process matching must validate at least `exe` and `user`; prefer `pidfile` or `cgroup` as additional evidence. `exe` is the resolved `/proc/<pid>/exe` path matched exactly (never argv[0]/cmdline, never a substring); an unresolvable `exe` never matches. See `docs/safety.md` (process identity).
5. Never restart, start or stop a service when a matching guard blocks the action.
6. Never restart or start when required preflight checks fail.
7. Never perform service actions without a timeout.
8. Never enter a restart loop. Automatic remediation must honor the resolved
   per-service `policy` block; `policy.cooldown` is mandatory and positive after
   config resolution, with optional max_actions/backoff; see `docs/rules.md`
   (remediation policy). Cooldown is decided by the daemon's rule evaluation
   before the shared engine runs. Manual
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
    (default `/run/sermo/locks`), managed by the `sermoctl lock` commands
    (wrap / acquire / release). The internal operation lock uses the
    separate path `<paths.runtime>/ops/<service>.lock` so it cannot collide with
    a user lock named `op`. `paths.locks` and `/etc/sermo/locks.d` have no
    semantics. See `docs/safety.md` (locks).
16. The scheduler runs one independent worker per service; a long operation
    (a multi-minute restart) on one service must never block monitoring of
    another. Never serialize all services through a single loop. Mass restarts
    are bounded by a global operation semaphore, and concurrent check execution
    across all services is bounded by `engine.max_parallel_checks` (a separate
    global pool). See `docs/safety.md` (scheduler and concurrency).

## Check types are unified across checks and watches

There is **one set of check types**, shared by a service's
`checks:`/`preflight:`/`postflight:` (referenced from rules) and by host
`watches:` (which fire a hook). The build path is shared
(`internal/checks.buildCheck`, used by both `Build` and `BuildInline`).

**Standing rule — a new check type must land on both surfaces in one change:**

- Add it to `checks.SingleShotCheckTypes` (config validation trusts that list)
  and validate its fields in shared validators (`internal/config/`) used by both
  the service-check and watch paths, so the grammars cannot drift.
- Decide its firing polarity: condition-style (`OK == true` is the alert, e.g.
  disk/load/count) vs health-style (`OK == true` is healthy, e.g. tcp/http).
  `checks.IsHealthType` drives whether a watch fires on failure — keep it
  current.
- The multi-metric `metrics:` map shape of `net`/`icmp`/`swap` and the
  multi-target `file`/`process` watches are watch-only; the single-metric form
  of `net`/`icmp`/`swap` (an explicit `metric:` field) works in `checks:` too.
- Update `docs/rules.md` (check-type table), `docs/configuration.md` (host
  watches) and a `configs/sermo.yml` example in the same change.

## Notifications are pluggable

Notifications go to named, typed **notifiers** under the global `notifiers`
section (`internal/notify`), referenced by name from a watch's `then.notify`
list. A watch's `then` block may have a `hook`, a `notify` list, or both (at least
one). Implemented transports: `email` (SMTP), `slack` (incoming webhook) and
`teams` (Workflows incoming webhook, Adaptive Card payload).

**Standing rule — keep notifiers extensible; adding a transport (discord, …) must
not require changes outside `internal/notify` and the docs:**

- Register the new type's constructor in `internal/notify` (the `builders` map)
  and implement the `Notifier` interface (`Name`/`Type`/`Send`). Use only the Go
  standard library where feasible (the project avoids new dependencies).
- Add its config validation to `validateNotifiers` (`internal/config/validate_global.go`)
  and keep `notify.SupportedTypes()` in step.
- The watch/dispatch side is transport-agnostic (it addresses every notifier
  through the interface) — do not special-case a transport there.
- Update `docs/configuration.md` (Notifications) and a `configs/sermo.yml`
  example in the same change.

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

## Before committing checklist

```sh
export PATH="$HOME/go/bin:$PATH"
gofmt -l ./internal ./cmd         # must print nothing
go build ./... && go test ./...   # must pass
govulncheck ./...                 # no vulnerabilities
staticcheck ./...                 # no findings
revive -config revive.toml ./...  # no findings
golangci-lint run                 # gosec: no findings (.golangci.yml)
```

When useful, also run `go vet ./...` and `go test -race ./...`. If a command
cannot be run, state why.

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

