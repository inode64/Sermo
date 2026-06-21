# Sermo — project conventions

## AI / agent workflow — standard git commits

AI agents, sub-agents, assistant sessions and automated coding processes use the
same normal Git workflow as a human contributor in the current repository
checkout. Keep the process simple: inspect status, make the requested edits,
run the relevant checks, then commit or merge only when the user asked for that
level of integration.

**Goals**
- Keep one visible source of truth in the repository checkout the user is using.
- Avoid hidden integration queues and extra cleanup steps.
- Make every change easy to inspect with normal `git status`, `git diff` and
  `git log`.
- Preserve user edits and unrelated local state.

**Mandatory workflow**

1. Before editing, inspect the current branch and working directory state:

   ```sh
   git status --short --branch
   ```

2. Work directly in the current checkout unless the user explicitly asks for a
   separate branch. If the current branch is not appropriate for the task, ask
   or create a normal local branch with a clear name before changing files.

3. Preserve unrelated changes. If files already have user edits, read and work
   with them instead of reverting or overwriting them. Leave unrelated untracked
   files alone.

4. Keep edits scoped to the request and the ownership boundaries in this
   document. Run targeted tests while developing and the complete battery before
   committing when the change is code-affecting.

5. Commit when the user asks for a commit, asks to merge into the main branch,
   or the task explicitly includes committing as part of the deliverable:

   ```sh
   git add <changed-files>
   git commit -m "agent: <concise description of the change>"
   ```

6. Merge only when the user explicitly asks for integration. Before merging,
   inspect the incoming commits and diff, resolve conflicts intentionally, and
   re-run the relevant checks after the merge.

**Prohibitions**
- Do not overwrite, revert, reset or discard user changes unless the user
  explicitly asks for that exact destructive action.
- Do not push to `origin` unless the user explicitly asks for a push or PR.
- Do not leave the repository in a partially staged state without explaining it.

**Relationship to the rest of AGENTS.md**
This workflow is part of the "Small-change checklist". Every implementation
should start by inspecting repository state and finish with either a clean,
tested commit or a clearly reported working-directory state.

## Reuse and shared behavior

Default to the smallest change that preserves the current design. Before adding
a helper, parser, validator, runner, builder or web/backend adapter, look for
existing code that already solves the same problem and extend it when the
ownership boundary stays clear. Do not duplicate validation, parsing,
comparison, notification, monitoring or action-dispatch logic across `sermod`,
`sermoctl`, web, watches and daemons.

Use this order of preference:

1. Reuse an existing type, helper, builder or command path unchanged.
2. Extend the existing owner when the new behavior belongs to the same concept.
3. Add a small private helper next to the owner when it removes real duplication.
4. Add a new package or abstraction only when behavior is shared across package
   boundaries and the existing owners are the wrong place for it.

Do not introduce a second way to express the same concept just because the new
call site is slightly different. If the new behavior needs a different path,
document why at the dispatch or validation point.

When a new check, option, monitor flag, notification behavior or web action is
generally useful to both host `watches:` and service daemons, implement it for
both surfaces in the same change unless there is a documented reason not to. If
the feature intentionally applies only to one surface, document that limitation
where the dispatch/validation decision lives and in the user docs (see
Documentation lockstep).

## Naming and terminology

Names are vocabulary. Use exactly the same name for a given concept across
variables, parameters, comments, struct fields and docs.

This is the naming counterpart of "Reuse and shared behavior". Before choosing
a name, look at the structs that already model the concept (e.g. `config.Service`,
`process.Selector`, `app.Event`). When in doubt, treat the field name from the
public struct or API as the single canonical term. Avoid near-synonyms such as
target/service/daemon, limit/max/cap or notify/notifier unless the code already
uses them for distinct concepts.

The one sanctioned exception is a Go builtin collision: a lowercase local or
parameter must not be named `max`, `min`, `cap`, `len`, etc. (the
`redefines-builtin-id` lint forbids it). The canonical term still names the
exported field and JSON (`Max`, `json:"max"`), and the lowercase local takes a
documented alias — `limit` for `max`. So the kernel-maximum concept is `Max` /
`"max"` in structs and on the wire, and `limit` in function locals (see
`levelCountResult` in `internal/checks/check.go` and `countMeter` in
`internal/app/webbackend.go`). Do not "fix" those `limit` locals back to `max`.

## Configuration compatibility

When Sermo's own configuration structure changes, break compatibility by
default: keep one canonical spelling in code, docs, examples and tests. Do not
preserve old aliases, migration-only validators, compatibility comments,
fixtures or tests for removed Sermo config parameters unless the old spelling is
still a current external compatibility requirement or safety invariant.

Before applying a new structure, state the scope of the breakage and remove the
previous structure in the same change: runtime parsing, validation, examples,
reference docs, agent guidance and tests must not keep the old form alive.
Document exceptions explicitly at the owner. Examples of valid exceptions are
Linux/init compatibility such as `/var/run` metadata normalized to `/run`, and
hard safety invariants such as deriving lock directories from `paths.runtime`.

## Runtime paths

Use `/run` for volatile runtime artifacts in catalog profiles, generated
configuration, examples and docs: pidfiles, sockets, OpenRC runtime metadata,
Sermo runtime directories and locks. Do not write new `/var/run` paths in Sermo
configuration. Modern Linux systems expose `/var/run` as a compatibility
symlink to `/run`, and older init scripts, service managers or packaged configs
may still report that spelling. Sermo must keep normalizing those host-provided
paths; this is Linux/init compatibility, not an obsolete Sermo configuration
shape to remove.

When systemd, OpenRC or a host file reports a pidfile or socket under
`/var/run`, normalize it to the equivalent `/run/...` path before writing it to a
catalog service, generated service config or documentation example.

Before adding any new runtime path, check whether the path or one of its parent
directories is a symlink (`readlink -f <path>` or `namei -l <path>`). Register
the canonical target path, not the symlink spelling, so the catalog does not grow
duplicate aliases for the same pidfile or socket.

## Configuration file granularity

Use one YAML file per configured target. Catalog profiles keep one daemon, app,
lib or pattern per file. Runtime configuration keeps one `kind: service` per
file, one `kind: mount` per file, one notifier per file and one host watch per
file (`storage`, `network`, `uplink`, `load`, etc.). Watch and notifier
fragments still use a top-level `watches:` or `notifiers:` map, but that map
must contain exactly one named entry.

Do not add new examples, wizard output, remote-test config or operational docs
that group several apps, services, mounts, storage entries, interfaces, VMs,
containers or other targets into one YAML file. The single exception is a
clearly labeled reference bundle such as `docs/sermo-all.yml`, whose purpose is
to validate and demonstrate the full schema in one file.

## Configuration structure changes

When changing the public configuration structure, prefer a clean break over
carrying old shapes forward. Backward compatibility, dual parsers, aliases,
deprecated fields and migration shims are exceptions, not the default. Keep
them only when the user explicitly asks for compatibility or when there is a
documented safety or release reason that justifies the legacy path.

Before applying a new configuration structure, report the intended scope to the
user: which YAML shape is being replaced, which structs/builders/validators are
being removed or rewritten, which docs/examples change, and what operators will
need to update. After the change is accepted or requested, remove the old
documentation, examples, schema structures, validation paths and tests that
describe the retired shape. Do not leave parallel legacy behavior in place just
to avoid touching more files; too many inherited paths make future changes less
reviewable and less safe.

## Catalog init and reload fallback verification

When adding or changing a catalog daemon that depends on init metadata or defines
`reload.signal`, verify every init backend in its `service:` map and every
fallback Sermo may use. Do not validate only the distro where the profile was
first written.

For OpenRC, inspect the real packaged `/etc/init.d/<unit>` and matching
`/etc/conf.d/<unit>` for `reload()`, `pidfile`, `command`, `command_user`,
`start-stop-daemon --pidfile`, supervisor settings and any `*_PIDFILE` variables.
For systemd, inspect the unit and `systemctl show` metadata (`CanReload`,
`MainPID`, `PIDFile`, `User`). Normalize any reported `/var/run` paths to
canonical `/run` paths before writing catalog YAML.

Any OpenRC-capable `reload.signal` must have a canonical `pidfile:` candidate
and a `processes:` selector with exact `exe` and `user`, so the
pidfile PID can be verified before Sermo signals it. If init scripts differ by
distro, encode the real candidates with a path list or `os:` branch. If a backend
has no trustworthy pidfile and exact identity selector, use `reload.command` or
rely on the backend's own reload path instead of shipping an unsafe signal
fallback.

Before finishing such a change, run the real catalog validation for both
backends:

```sh
go test ./internal/config -run 'TestRealCatalog(AllDaemonsValidate|ReloadDaemonsResolve)$' -count=1
```

## Service operations

Application-level start, stop, restart, reload, resume or signal actions on a service
must go through the shared `internal/operation` package and its engine. Do not
call backends directly, do not send signals from `app/` or `cli/`, and do not
bypass locks, guards, preflight or policy. The operation path is the single
source of truth for safe service control.

The narrow exceptions are the backend/process implementations that provide the
primitive operation APIs, and tests/fakes that prove those primitives work. Keep
those primitives small, injectable and covered by tests.

## Native by default

Avoid external commands whenever practical; prefer the Go standard library or a
Go-module alternative, unless the entry explicitly requires a third-party library
or command. When an external command is genuinely required (`systemctl`,
`rc-service`, user `command` checks, hooks, …), production code must not
call `os/exec` directly: it goes through an injectable `execx` runner with a
context and an explicit timeout, invoking an argv directly — never a shell.
`execx` and tests/fakes are the only exceptions.

## Protocol probes: interface binding is mandatory

Every `internal/conn` protocol probe must honor `cfg.Interface` — the egress
network interface (Linux `SO_BINDTODEVICE`), set on multi-homed hosts so a probe
leaves through a specific link. The shared `BindDialer(cfg.Interface)` (and
`BindListenConfig` for packet sockets) is the single mechanism; every probe dials
through it, directly or via `probeBanner`/`dialDeadline`/`dialConn`. A probe that
silently uses default routing is a bug.

This constrains adopting a Go module to "simplify" a protocol. Decide by where the
library does its I/O:

1. **Codec-only library (no I/O)** — preferred. Keep dialing through `BindDialer`
   and hand the bytes to the library to build/parse. Interface binding is
   untouched. Example: DNS uses `golang.org/x/net/dns/dnsmessage` purely as a wire
   codec over the existing UDP dial.
2. **Library that does its own I/O but accepts a custom dialer or connection** —
   acceptable. Route its dialing through `BindDialer` via the library's hook so
   binding is preserved. Example: NTP uses `github.com/beevik/ntp` through its
   `QueryOptions.Dialer` callback, which dials with `BindDialer(cfg.Interface)`.
3. **Library that dials internally and cannot accept a custom dialer/connection**
   — do NOT adopt it: it would bypass `SO_BINDTODEVICE` and break interface
   binding. Keep the hand-rolled probe (and its transport) instead. This is why
   the DHCP probe keeps its own raw-socket transport (`dhcp_linux.go`) rather than
   switching to a full DHCP client library, even though a module exists.

In short: interface binding wins over code reduction. A module is only worth
adopting when binding survives — otherwise our own code stays. Record the reason
at the probe when a migration is intentionally not done.

## Documentation lockstep

When you change configuration, add a check type, notifier, rule action or
observable behavior, update the corresponding documentation, catalog examples
(when generally useful) and `docs/configuration.md`, `docs/rules.md` and the
daemon docs in the same change. Keep `examples/sermo.yml` comments current. Code
and docs must evolve together.

When a user request, implementation finding or runtime behavior contradicts the
current documentation, call out the mismatch explicitly before treating either
side as authoritative. If the user accepts the requested behavior or the change
is implemented, update the conflicting documentation in the same patch; do not
leave docs describing the old behavior.

## Documentation scope and style

Document only what is required for one of these reasons:

- User-facing behavior, configuration, CLI, safety policy or operational
  workflow changed and must stay in lockstep with the code.
- A lint rule or analyzer requires the documentation or justification, such as
  exported Go symbols, `//nolint` rationale or a security exception.
- The requirement, invariant or exception is necessary to use, maintain or
  review the code safely.

Keep documentation direct. Prefer the shortest accurate explanation, link to an
existing source of truth instead of repeating it, and remove redundant prose when
editing nearby text. Do not document obvious implementation steps just to narrate
the code.

## Central builders

New check types, watch kinds, notifiers and rule actions start in the central
builder functions (`internal/checks/build.go`, `internal/app/watch_build.go`,
`internal/notify/`, rule builders, etc.). Do not duplicate construction logic
or add ad-hoc cases across packages. If no central builder exists yet, create
one at the owning package instead of scattering switch cases through callers.

## Timeout discipline

Every blocking operation (commands, network, database, I/O, etc.) must be
bounded by a timeout taken from engine configuration (via `app.EngineDuration`
or `cfgval`) or a named, documented constant. Magic duration literals in
application logic are forbidden. Short literals are acceptable in tests when
they bound the test itself rather than production behavior.

## Daemon performance discipline

Treat every code path that runs inside `sermod` as performance-sensitive:
workers, checks, watches, rule evaluation, process discovery, metrics sampling,
state persistence, web-backend refreshes and reload/rebuild paths all affect the
long-running daemon. Optimize these paths for speed and bounded resource use
before adding convenience work. Prefer cached or shared samples over repeated
host scans in the same cycle, avoid avoidable allocations and sorting in hot
loops, keep blocking work out of scheduler-critical sections, and make expensive
operations explicit, rate-limited or interval-bound.

When a new feature adds daemon-cycle work, review its cost at normal fleet scale
and add tests or benchmarks when the cost is non-obvious. A small inefficiency in
one service/watch can be multiplied by every configured target and degrade
monitoring latency, web responsiveness and remediation timing.

## Small-change checklist

Before finishing any code change:

- **Git discipline (AI agents):** Inspect `git status --short --branch` before
  editing, preserve unrelated user changes, commit only when requested or when
  the task includes committing, and never push unless explicitly asked.
- Search for the existing owner with `rg` before adding a new helper or switch.
- Keep the patch close to that owner; avoid unrelated refactors.
- Preserve public YAML, JSON, CLI and web field names unless the change is
  explicitly a migration.
- Add or move tests when a bug or ambiguous behavior is found.
- For daemon-facing changes, check the runtime cost in the steady-state cycle
  and avoid repeated scans, blocking calls or avoidable allocations on hot paths.
- Update docs and examples in the same change when behavior changes.

## Web UI cohesion

The web UI is a single embedded document, `internal/web/index.html` (HTML, CSS
and JS in one file). **Before adding or changing any UI element, find the
existing element that already solves the same problem and copy its structure,
classes and styling exactly** — do not invent a parallel way to do the same
thing. Cohesion across panels is a hard requirement, not a preference.

Concretely, every data panel is a `<details id="{name}-section">` with a
`<summary>`, an optional flex `#{name}-controls` row (search + filters + count)
and a bare `<table class="{name}-table">` placed directly inside the `<details>`.
Do not wrap data tables in scroll containers; the page scrolls as a whole
instead of trapping a panel in its own scrollbar. When you introduce a genuinely
new pattern, document it here so the next change can follow it.

When adding a host watch/check with useful runtime data, wire its Web UI
`Watch.Meter` or `Watch.Readings` path and add a `webbackend` regression test;
do not leave it visible only as static config.

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
- **SLA timeline strip.** `renderSLATimeline(segments, window)` renders a
  contiguous status-page availability band — one `.sla-seg` cell per equal
  sub-span (oldest left), colored by `slaColor`, hatched `.sla-gap` where no
  cycle was observed. `renderSLAWindows` uses it for every rolling SLA window;
  the per-segment ratios come from the backend (`SLAWindow.Segments`). Reuse it
  anywhere a compact availability history is needed.
- **Value formatting (one type → one formatter).** A given kind of value must
  render identically everywhere; never hand-format with bare `toFixed`, string
  concatenation or a raw `${value}`. Each type has a single canonical helper —
  route every user-facing reading through it (this is what keeps "2.1%" from
  appearing elsewhere as "2.14%" or "234.5678 B/s"):
  - **Numbers** → `fmtNum(n, max=2)` (the base formatter; ≤`max` decimals,
    trailing zeros stripped, `—` when non-finite). Every other helper builds on it.
  - **Percentages** → `fmtPct(n)` (`fmtNum(n,2)+"%"`). Includes CPU%, memory %,
    saturation, SLA % — tiles, bars and detail readings all use it.
  - **Bytes / byte-rates** → `fmtBytes(n)` (and `fmtBytes(n)+"/s"`); via
    `fmtMetricValue(v, unit)` for unit-tagged time series (`bytes`, `B/s`, `%`,
    `ms`, default).
  - **Durations** → `fmtUptime`/`fmtSeconds`/`shortDur`; **relative time** →
    `fmtRemain`/`fmtUntilShort`/`fmtAge`/`fmtSince`; **absolute timestamps** →
    `fmtTime`.
  - **Gauges** → `usageBar` (full-width host gauge), `usageBarMini` (dense table
    cells), `cpuBarMini` (single-core-normalized CPU). Clamp with `pctClamp`.
  Bare `toFixed` is reserved for **geometry only** — SVG path coordinates and CSS
  bar widths (`--usage-pct`, `--sla-pct`) keep their own fixed precision. When a
  value needs a representation no helper covers, add or extend a helper next to
  the others rather than formatting inline at the call site. See the `fmtNum`
  banner comment in `internal/web/index.html`.

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
  app a `%n` template (`name: ceph-osd%n`) with `versions: { from:
  "/var/lib/ceph/osd/ceph-${n}" }`, then make the daemon a matching `%n` template
  that links `apps: ["ceph-osd${n}"]`. `internal/config/versions.go` globs the
  app discovery path on the host and materializes one concrete daemon per
  discovered id, with `${n}` baked into `service: "ceph-osd@${n}"`. Honest
  limitation: this auto-discovers daemon *definitions*; the operator still
  enables one `kind: service` per instance (Sermo monitors services, not catalog
  daemons).

Keep `docs/daemons.md` (built-in variable table) in step when adding a built-in.

## Go quality gates

Two rules, one battery:

- **Every Go file must be `gofmt`-clean after any modification.** A Claude Code
  `PostToolUse` hook (`.claude/settings.json`) runs `gofmt -w` on every edited
  `.go` file; editing outside Claude Code, run it yourself (editor
  format-on-save).
- **Every change must pass the whole battery before committing** (the tools
  analyze the full module and are too slow per-edit). Use `make lint` for the
  analyzer set; the Makefile already finds Go-installed tools in `~/go/bin` and
  gives static-analysis caches a writable fallback for non-interactive agents:

```sh
gofmt -l ./internal ./cmd         # must print nothing
go build ./... && go test ./...   # must pass
make lint                         # govulncheck/staticcheck/revive/golangci-lint
```

Tool notes:

- **`make lint`** is the canonical analyzer entrypoint. Do not hand-prefix
  `PATH` or call the analyzer binaries one by one unless you are debugging the
  lint target itself. `govulncheck` may need network access to refresh the
  vulnerability DB; a network/DNS failure there is an environment issue, not a
  code finding.
- **`revive`** (`revive.toml`): default rule set minus `unused-parameter` (many
  methods implement interfaces whose `ctx` they legitimately ignore). Document
  new exported symbols — the `exported` rule is on.
- **`golangci-lint`** uses `.golangci.yml` (**v2 format** — the binary must be
  v2) for `gosec`, `bodyclose`, `copyloopvar`, `ineffassign`, `nilerr` and
  `wastedassign`.
  Accepted gosec exceptions live in that config: `G115`, and in test fixtures
  `G306`/`G101`/`G703`. By-design cases (`G204` operator-configured commands,
  intentional `0644` writes, bounded `args[i]` reads, shutdown-context `G118`)
  are suppressed at the call site with `//nolint:gosec` plus a justifying
  comment — prefer that over widening the config.

## Testing

Tests are part of the change, not an afterthought (see the small-change
checklist). Match the suite's existing style instead of inventing one.

- **Inject the seam; never touch the host from logic under test.** Every probe
  that reads the system takes an injectable function or interface, so tests run
  without real `/proc`, sockets or services: the `*SamplerFunc` fields and the
  `Deps` samplers on checks (`FdsSamplerFunc`, `MemorySamplerFunc`, …), the
  `metrics.Reader` interface, `execx.Runner`, `process.Signaler`, and the web
  `Backend` interface. Add a seam in the same shape when you add a probe.
- **Reuse the existing fakes** — `fakeReader` (metrics), `fakeRunner`/
  `scriptRunner` (servicemgr), `fakeFds`/`fakeConntrack` (checks), `fakeBackend`
  (web). Copy their shape; do not add a mocking framework.
- **Table-driven subtests.** Express variants as a slice of cases driven by
  `t.Run(tc.name, …)`, the dominant pattern across the suite.
- **Split pure logic out of I/O so it is testable directly** (e.g.
  `parseMeminfoKB`, `parseOSReleasePrettyName`, `levelCountResult`). This serves
  the reuse rule too.
- **Prompt-driven flows** (`internal/assist`) abort on truncated input via
  `assist.Recover(&err)`; drive them with a scripted `strings.NewReader` and
  assert the result, as the wizard tests do.
- Short magic durations are fine in tests when they bound the test itself, not
  production behavior (see Timeout discipline).

## Security and safety invariants

1. Never kill processes by name only.
2. Never use `SIGKILL` unless the daemon definition explicitly allows it.
3. A `SIGKILL` policy must include a restrictive `kill_only_if` clause.
4. Process matching must validate at least `exe` and `user`; prefer `pidfile` or `cgroup` as additional evidence. `exe` is the resolved `/proc/<pid>/exe` path matched exactly (never argv[0]/cmdline, never a substring); an unresolvable `exe` never matches. See `docs/safety.md` (process identity).
5. Never start, stop, restart, reload or resume a service when a matching guard
   blocks the action.
6. Never start, restart, reload or resume when required preflight checks fail.
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
    (total memory, total CPU, load) must never drive start, stop, restart, reload
    or resume for an individual service; it may only drive an alert.
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

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

When the user types `/graphify`, invoke the `skill` tool with `skill: "graphify"` before doing anything else.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- Dirty graphify-out/ files are expected after hooks or incremental updates; dirty graph files are not a reason to skip graphify. Only skip graphify if the task is about stale or incorrect graph output, or the user explicitly says not to use it.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
