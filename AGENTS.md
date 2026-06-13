# Sermo — project conventions

## Reuse and shared behavior

Before adding a new helper, parser, validator, runner or web/backend adapter,
look for existing code that already solves the same problem and extend it when
that keeps the ownership boundary clear. Do not duplicate validation, parsing,
comparison, notification, monitoring or action-dispatch logic across `sermod`,
`sermoctl`, web, watches and daemons.

When a new check, option, monitor flag, notification behavior or web action is
generally useful to both host `watches:` and service daemons, implement it for
both surfaces in the same change unless there is a documented reason not to. If
the feature intentionally applies only to one surface, document that limitation
where the dispatch/validation decision lives and in the user docs. Keep
`docs/configuration.md`, `docs/rules.md`, daemon docs and `configs/sermo.yml` in
step with YAML behavior.

## Naming and terminology

Names are vocabulary. Use exactly the same name for a given concept across
variables, parameters, comments and struct fields.

This is the naming counterpart of "Reuse and shared behavior". Before choosing
a name, look at the structs that already model the concept (e.g. `config.Service`,
`process.Selector`, `app.Event`). When in doubt, treat the field name from the
public struct or API as the single canonical term.

## Service operations

Every start, stop, restart, reload or signal action on a service must go through
the shared `internal/operation` package (and its engine). Do not call backends
directly, do not send signals from `app/` or `cli/`, and do not bypass locks,
guards, preflight or policy. The operation path is the single source of truth
for safe service control.

## External commands

Never call `os/exec`, `exec.Command` or equivalent directly. All external
commands (systemctl, rc-service, user checks, hooks, ldd, etc.) must go through
the `execx` runner with a context and an explicit timeout. `execx` is the only
sanctioned execution surface.

## Documentation lockstep

When you change configuration, add a check type, notifier, rule action or
observable behavior, update the corresponding documentation, catalog examples
(when generally useful) and `docs/configuration.md` / `docs/rules.md` in the
same change. Keep `configs/sermo.yml` comments current. Code and docs must
evolve together.

## Central builders

New check types, watch kinds, notifiers and rule actions must be introduced
exclusively through the central builder functions (`internal/checks/build.go`,
`internal/app/watch_build.go`, `internal/notify/`, rule builders, etc.). Do not
duplicate construction logic or add ad-hoc cases across packages.

## Timeout discipline

Every blocking operation (commands, network, database, I/O, etc.) must be
bounded by a timeout taken from engine configuration (via `app.EngineDuration`
or `cfgval`) or a named, documented constant. Magic duration literals in
application logic are forbidden.

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

- **Scrollable panel** — wrap the table in `<div class="table-wrap">`, which
  adds `overflow:auto; max-height:calc(100vh - 13rem)`. Used by Services (a
  long, unbounded list).
- **Non-scrollable panel** — place the bare `<table>` directly inside the
  `<details>`, no wrapper. Used by Host watches, Events, Notifiers and
  Applications (the page scrolls as a whole instead of trapping a panel in its
  own scrollbar).

Pick the variant that matches the panel's nature and reuse it verbatim; never
hand-roll bespoke `overflow`/`max-height` rules on a single panel. When you
introduce a genuinely new pattern, document it here so the next change can
follow it.

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

The interactive wizard (`sermoctl wizard`, `internal/assist`) follows **one
canonical question flow for every assistant, present and future** — documented
in [docs/wizards.md](docs/wizards.md). Read it before adding or changing a
wizard; the invariants below must not drift per assistant.

Drive every selection through the shared `Prompt` helpers — never hand-roll a
bespoke question. Multi-selects use `Prompt.MultiChoose` (item numbers, the
keyword `all`, or an option's name); menus with reserved picks use
`Prompt.MultiChooseKeyword`. Show detected targets to pick from — **never ask
the operator to invent a name**. Yes/no questions go through `Prompt.Confirm`,
which **forces an explicit answer** (an empty line re-prompts; it does not take
a default). Monitor state and interval come from the shared
`Prompt.AskMonitoring` and are injected into every generated entry.

Reuse one consistent **all / none / default** vocabulary: `all` selects
everything; `none` opts out (monitor-only, `notify: [none]`); `default` inherits
the global notify. `none` and `default` are **always selectable, even with zero
notifiers configured** — the wizard never blocks on the notifier question. When
`default` has nothing to inherit (no global notify) it **degrades to
monitor-only** with a one-line note; it must never re-ask or abort (see
`chooseNotifiers` in `internal/assist/notify.go`). The final step previews what
will be written, confirms, and offers to delete managed files whose target is no
longer detected. Keep `docs/wizards.md`, `docs/configuration.md` and this
section in step when any of this changes.

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

## Go quality gates

Two rules, one battery:

- **Every Go file must be `gofmt`-clean after any modification.** A Claude Code
  `PostToolUse` hook (`.claude/settings.json`) runs `gofmt -w` on every edited
  `.go` file; editing outside Claude Code, run it yourself (editor
  format-on-save).
- **Every change must pass the whole battery before committing** (the tools
  analyze the full module and are too slow per-edit; binaries in `~/go/bin`):

```sh
export PATH="$HOME/go/bin:$PATH"
gofmt -l ./internal ./cmd         # must print nothing
go build ./... && go test ./...   # must pass
govulncheck ./...                 # no known vulnerabilities
staticcheck ./...                 # no findings
revive -config revive.toml ./...  # no findings
golangci-lint run                 # gosec via .golangci.yml: no findings
```

Tool notes:

- **`revive`** (`revive.toml`): default rule set minus `unused-parameter` (many
  methods implement interfaces whose `ctx` they legitimately ignore). Document
  new exported symbols — the `exported` rule is on.
- **`gosec`** runs through golangci-lint (`.golangci.yml`, **v2 format** — the
  binary must be v2). Accepted exceptions live in that config: `G115`, and in
  test fixtures `G306`/`G101`/`G703`. By-design cases (`G204`
  operator-configured commands, intentional `0644` writes, bounded `args[i]`
  reads, shutdown-context `G118`) are suppressed at the call site with
  `//nolint:gosec` plus a justifying comment — prefer that over widening the
  config.

## Security and safety invariants

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
   before the shared engine runs. Manual operator commands are exempt from
   cooldown but still subject to locks, guards and preflight.
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
