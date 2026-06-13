# Sermo — project conventions

## AI / agent workspaces — git worktrees mandatory for code modifications

All AI agents, sub-agents, assistant sessions and automated coding processes that will modify source files **must** operate inside a dedicated `git worktree`. Direct edits from the primary checkout are reserved exclusively for the human operator.

**Goals**
- Multiple agent instances can execute in parallel (separate directories → no file conflicts).
- The human's primary tree on the local `main` branch remains permanently clean and is the single integration point.
- Agent work is trivially inspectable, diffable, discardable or mergeable as an atomic unit.
- Matches the isolation facilities provided by the environment (e.g. `spawn_subagent` with `isolation: "worktree"`).

**Mandatory workflow**

1. The primary (human-facing) session keeps its working tree on a clean local `main` at all times.
2. When an agent needs to change code, it (or its spawner) first creates a dedicated worktree + branch from the primary checkout:

   ```sh
   # Execute from the primary Sermo directory (must be on main, tree clean)
   git worktree add -b agent/<task-slug> ../sermo-agent-<task-slug>
   cd ../sermo-agent-<task-slug>
   ```

   Choose a short descriptive slug (`disk-metrics-refactor`, `add-foo-check-20250613`, ...). Sibling directories keep navigation simple.

3. **All** development work happens inside the worktree:
   - File creation/edits (editors, `search_replace`, `write`, ...).
   - Builds, tests, `make lint`, gofmt, etc.
   - Any locally built `bin/sermoctl` / `bin/sermod` used for manual verification.
   - The agent must still obey every rule in this document (reuse, safety invariants, documentation lockstep, full quality gates, etc.).

4. When the unit of work is finished and has passed the complete battery:
   - Commit inside the worktree (the agent may stage but the human usually reviews before the final merge):

     ```sh
     git add -A
     git commit -m "agent: <concise description of the change>"
     ```

5. Integration is always performed **from the primary checkout** (still on local `main`):

   ```sh
   # From primary Sermo checkout on main
   git merge --no-ff ../sermo-agent-<task-slug> -m "Merge agent/<task-slug>: <summary>"

   # Clean up
   git worktree remove ../sermo-agent-<task-slug>
   git branch -d agent/<task-slug>
   ```

   You can also use the fetch form if preferred:

   ```sh
   git fetch ../sermo-agent-<task-slug> agent/<task-slug>
   git merge --no-ff FETCH_HEAD -m "..."
   ```

   The human reviews the diff and is the gate for the merge. Agent sessions must never push their branches to `origin`.

6. Tool-spawned sub-agents that will write files **must** request worktree isolation when the launcher supports it:

   When using `spawn_subagent` (or any equivalent sub-task primitive), pass `isolation: "worktree"`. The child receives its own isolated checkout; the framework returns the worktree path. The child agent follows the same "commit inside worktree → parent merges from primary" discipline. After the child finishes, the parent is responsible for the merge + `worktree remove`.

**Prohibitions**
- Never modify files while your current working directory is the primary checkout (except the human operator for trivial fixes).
- Never reuse or nest worktrees for unrelated tasks.
- Never leave a worktree behind after the corresponding branch has been merged or abandoned (`git worktree prune` helps).
- Never `git push` agent branches unless the human explicitly asks for a PR.

**Relationship to the rest of AGENTS.md**
This workflow is part of the "Small-change checklist". Every implementation plan, skill, or prompt that drives agents on Sermo must start by establishing (or declaring use of) a worktree and must end by merging the result back into local `main`.

Update this section when the coding environment changes its sub-agent isolation primitives.

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

## Service operations

Application-level start, stop, restart, reload or signal actions on a service
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
`rc-service`, user `command` checks, hooks, `ldd`, …), production code must not
call `os/exec` directly: it goes through an injectable `execx` runner with a
context and an explicit timeout, invoking an argv directly — never a shell.
`execx` and tests/fakes are the only exceptions.

## Documentation lockstep

When you change configuration, add a check type, notifier, rule action or
observable behavior, update the corresponding documentation, catalog examples
(when generally useful) and `docs/configuration.md`, `docs/rules.md` and the
daemon docs in the same change. Keep `configs/sermo.yml` comments current. Code
and docs must evolve together.

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

## Small-change checklist

Before finishing any code change:

- **Worktree discipline (AI agents):** If you are an agent that edits code, you must have performed the work inside a dedicated `git worktree` created from a clean primary `main`, and the final merge back to local `main` (plus `worktree remove`) must be part of completing the change. See "AI / agent workspaces".
- Search for the existing owner with `rg` before adding a new helper or switch.
- Keep the patch close to that owner; avoid unrelated refactors.
- Preserve public YAML, JSON, CLI and web field names unless the change is
  explicitly a migration.
- Add or move tests when a bug or ambiguous behavior is found.
- Update docs and examples in the same change when behavior changes.

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
