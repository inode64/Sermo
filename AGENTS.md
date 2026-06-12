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
   `docs/configuration.md`, `docs/rules.md`, daemon docs and `configs/sermo.yml`
   whenever the YAML surface changes.

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
4. Process matching must validate at least `exe` and `user`; prefer `pidfile` or `cgroup` as additional evidence. `exe` is the resolved `/proc/<pid>/exe` path matched exactly (never argv[0]/cmdline, never a substring); an unresolvable `exe` never matches. See spec section 21.
5. Never restart, start or stop a service when a matching guard blocks the action.
6. Never restart or start when required preflight checks fail.
7. Never perform service actions without a timeout.
8. Never enter a restart loop. Automatic remediation must honor the resolved
   per-service `policy` block; `policy.cooldown` is mandatory and positive after
   config resolution, with optional max_actions/backoff; see
   spec section 16, "Remediation policy". Cooldown is decided by the daemon's
   rule evaluation before the shared engine runs. Manual
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
    a user lock named `op`. `paths.locks` and `/etc/sermo/locks.d` have no MVP
    semantics. See spec sections 18 and 20.
16. The scheduler runs one independent worker per service; a long operation
    (a multi-minute restart) on one service must never block monitoring of
    another. Never serialize all services through a single loop. Mass restarts
    are bounded by a global operation semaphore, and concurrent check execution
    across all services is bounded by `engine.max_parallel_checks` (a separate
    global pool). See spec sections 12 and 24.

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

---

# Implementation specification

The original implementation spec, merged here so the project keeps a single
instructions file; "spec section N" references (here and in `.agents/skills/`)
point into this part. Section numbering is preserved, so there are gaps where
obsolete sections were removed. It is the reference for the core semantics:
config model and merge rules, variables, checks, rules, operations, locks,
process discovery, stop policy, CLI exit codes. Its scope statements are
historical — the web UI, notifiers, host watches and the wizard shipped long
ago. On conflict, the instructions above and the code win. The example daemons
it once carried live for real in `catalog/`.


## 8. Configuration model

Sermo has two document kinds:

```yaml
kind: daemon
```

and:

```yaml
kind: service
```

A daemon is a reusable base definition.
A service is a concrete monitored instance.

A service may use a daemon:

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
1. Load packaged daemons from /usr/share/sermo/daemons.
2. Load user daemons from /etc/sermo/daemons-available.
3. Load global configuration and conf.d files, producing one effective `defaults`
   block (sermo.yml layered with conf.d).
4. Load included service documents and watch fragments from `paths.includes`.
5. Resolve each service into a flat definition, lowest precedence first:
   a. Base layer: the effective global `defaults` (its stop_policy, policy and
      rule_window). This is the lowest precedence.
   b. Apply the `uses` daemon, or the `clone` chain, merged on top of the base.
   c. Merge the service's own fields (overrides) on top — highest precedence.
   d. Expand ${var} variables (section 10), once, after all merging.
   e. Validate the final flattened service.
```

Precedence, low to high:

```text
global defaults  <  daemon (uses) or clone source  <  service overrides
```

Only the per-service parts of `defaults` merge into a service: `stop_policy`,
`policy`, and `rule_window` (the fallback window for a rule that declares neither
`for` nor `within`, see section 13). Engine-wide settings (`interval`,
`max_parallel_checks`, `default_timeout`, `backend`) are daemon configuration and
are NOT merged into individual services.

The effective global `defaults.policy.cooldown` is required and must be positive
so every resolved service inherits a loop-prevention cooldown unless it overrides
that value with another positive duration.

Because variable expansion is step 5d — after every merge — a default, daemon or
override may be written with `${var}` and is resolved using the variables visible
on the final flattened service.

`uses` and `clone` both form the middle precedence layer (step 5b), and both are
taken in UNEXPANDED form:

```text
- uses copies the named daemon's definition (fields and its variables) with
  `${...}` still literal.
- clone copies the source service's merged-but-unexpanded definition: the result
  of resolving that source through its own defaults/uses/clone/overrides, but
  BEFORE its variable expansion, including its `variables` map.
- The cloning (or using) service then merges its own fields and variable
  overrides on top, and expansion (step 5d) runs once on the combined result.
```

This is why a clone can override a single variable and have it take effect: in
a clone like `redis-cache` setting `port: 6380` works only because the clone
copies `redis-main` before expansion, so the override changes the value that
`${port}` resolves to. If clone copied an already-expanded source, the override
would not reach the already-substituted checks. Clone chains resolve
transitively; cycles are rejected (section 30).

The daemon must only work with resolved, flat service definitions.

### Document sections

A daemon or service may contain these top-level sections, all maps keyed by
name where applicable:

```text
description   optional human-readable label for the service or daemon
service       backend target name and backend selector
aliases       per-backend candidate unit names (section 11)
variables     string variables for ${...} expansion (section 10)
commands      optional named auxiliary commands (below)
preflight     checks run before dangerous actions (section 19)
postflight    checks run after start/restart actions (section 19)
processes     process discovery selectors (section 21)
checks        monitoring checks (section 12)
stop_policy   stop/kill behaviour (section 22)
policy        remediation cooldown/rate limit (section 16)
rules         guard/remediation/alert rules (section 13)
```

`description` is an optional free-text label shown by `sermoctl service show` and
included in `config render`. It is informational: the engine never acts on it. It
is a top-level scalar (not a map), inherited and overridable like any other field.

`commands` is optional, informational metadata: named commands an operator may
want to keep with the daemon (for example a version command). The MVP loads and
validates them (array form, optional timeout) and `sermoctl service show` may
display them, but the engine never runs them automatically as part of monitoring
or remediation.

```yaml
commands:
  version:
    command: ["apachectl", "-v"]
    timeout: 5s
```

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

Named sections must be maps keyed by name, not arrays. This applies to `checks`,
`preflight`, `postflight`, `processes` and `rules`:

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

Daemons may define variables:

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

- Variables are flat literal strings.
- Expansion is simple `${var}` substitution.
- Missing variables are validation errors.
- No expressions or template language in MVP.

Expansion is a single pass and not recursive:

```text
- Field values are expanded once against the flat variable map.
- A variable VALUE must not itself contain `${...}`; a variable that references
  another variable is a validation error (see TODO.md for nested references).
- Because variable values are literal and expansion is one pass, no `${...}` can
  legitimately survive expansion. Any `${...}` left after the pass means an
  undefined variable and is a validation error.
- There is no escape syntax in the MVP: `${` always begins a variable reference.
```

Expansion runs after all merging (section 8, step 5d), so a value inherited from
a default, daemon or override is expanded with the variables visible on the
final flattened service.

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

`Manager.Restart` may wrap a backend's native restart command for backend-level
capability, but the safe operation engine must not use it for Sermo restart
actions. A Sermo restart is always `Stop` -> residual process handling -> `Start`
so `orphan_processes` can abort the operation before the service is started
again.

Backend detection priority:

```text
1. CLI flag --backend
2. Environment variable SERMO_BACKEND
3. Global config engine.backend
4. Automatic detection
```

Automatic detection:

```text
1. Probe systemd availability:
   - systemctl exists
   - /run/systemd/system exists
   - systemctl is-system-running is usable; treat `running` and `degraded` as
     usable states. `degraded` must not make detection fail.
2. Probe OpenRC availability:
   - rc-service exists
   - /run/openrc exists, or rc-status works
3. If exactly one backend is available, use it.
4. If both backends are available, prefer the active init system:
   - if PID 1 or systemctl state shows systemd is active, use systemd.
   - else if /run/openrc exists and rc-status works, use OpenRC.
   - else fail with a clear ambiguous-backend error and ask for --backend,
     SERMO_BACKEND or engine.backend.
5. If neither backend is available, fail with a clear error.
```

Do not detect a backend by command presence alone. On hosts where both command
sets are installed, the active init system wins over the mere presence of
`systemctl` or `rc-service`.

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

### Unit aliases

The unit name differs across distributions (Apache is `apache2` on Debian,
`httpd` on RHEL). A daemon may list per-backend candidate names with `aliases`:

```yaml
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
```

Resolution, once the backend is known:

```text
1. Build the candidate list: service.name first, then aliases for the active
   backend, in order, deduplicated.
2. systemd: normalize each candidate (append `.service` if it has no unit
   suffix). openrc: use the name as-is.
3. Pick the first candidate the backend actually knows (systemd:
   `systemctl cat`/`list-unit-files`; openrc: the init script exists). Cache it.
4. If none resolve, fail with a clear error listing the candidates tried.
```

All later operations on the service use the resolved name. If `aliases` is
absent, the candidate list is just `service.name`.

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

### Check execution and concurrency

Within a cycle, a service's distinct probes (declared checks and inline
conditions, deduplicated per section 14) are run concurrently and their results
collected before rule evaluation. Order does not matter: results are keyed by
name, and rules read them from the per-cycle cache.

`engine.max_parallel_checks` bounds how many checks run at once across the WHOLE
daemon, not per service:

```text
- A single global semaphore of size max_parallel_checks gates every check
  execution from every service worker. With many services this caps total
  concurrent probes (sockets, subprocesses) instead of spawning one goroutine
  per check unbounded.
- Each check still runs under its own timeout (the check's `timeout`, else
  engine.default_timeout), so a slow check holds a slot only until its timeout.
- This pool is independent from the operation semaphore (section 24): checks and
  start/stop/restart operations are bounded separately.
- A check that cannot acquire a slot waits; it does not skip. If the whole cycle
  cannot finish before the next tick, that tick is skipped (section 24).
```

`sermoctl` one-shot commands (`status`, `preflight`) run their checks directly
under `default_timeout`; the global pool is a `sermod` concern.

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

`expect_status` accepts a single code (`200`), a class (`2xx`), a list of
either, or an `{op, value}` comparison (the shared compare operators).

### Command

```yaml
checks:
  config:
    type: command
    command: ["apachectl", "configtest"]
    expect_exit: 0
    timeout: 10s
```

`command` is array form only (never a shell string). `expect_exit` is the exit
code that means the command succeeded — the value the application must return for
correct operation; it defaults to `0`. The check is OK when the command's actual
exit code equals `expect_exit`.

### Service state

```yaml
checks:
  service:
    type: service
    expect: active
```

`expect` is one of the servicemgr statuses (section 11): `active`, `inactive`,
`failed` or `unknown`. The check is OK when the resolved status equals `expect`.

### File exists

Use this to detect a foreign flag/lock file written by another tool. Do not point
it at Sermo's own lock files under `<paths.runtime>/locks/` (default
`/run/sermo/locks/`) — the engine already checks those (section 20).

```yaml
checks:
  backup-flag:
    type: file_exists
    path: /run/mysql-backup/in-progress
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

`state` is one of:

```text
running  a process matching the selector exists and is not a zombie. Default.
zombie   a matching process exists but is defunct (/proc/<pid>/stat state Z).
absent   no process matches the selector.
```

The check is OK when the observed state equals `state`. Matching uses the exact
resolved-exe and real-UID rules of section 21.

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

### Metric collection

Some metrics are instantaneous and some are rates, and this changes how they are
collected:

```text
instantaneous  memory, total_memory, process_count, load*  — one read per cycle.
rate           cpu, total_cpu  — a delta between two samples over elapsed time.
```

A rate cannot be computed from a single read. CPU% is
`Δ(cpu_time) / (Δ(wall_time) * ncpu) * 100`, so the collector must remember the
previous sample. This is why metric collection lives in a stateful, long-lived
`internal/metrics` collector owned by the daemon, NOT in the per-call check:

```text
- The collector is sampled once per cycle (a metric check or condition reads the
  already-sampled value, so several CPU rules in a cycle share one sample — this
  matches the once-per-cycle probe rule of section 14).
- Each cycle it reads a fresh sample, computes rates against the stored previous
  sample, then stores the new sample as the next baseline.
- Sources: service cpu = sum of utime+stime deltas (/proc/<pid>/stat fields
  14-15) over the discovered process set; system total_cpu = /proc/stat aggregate
  delta; memory = RSS sum or /proc/meminfo; both normalized as section 14
  describes (absolute or %).
```

First-cycle behaviour:

```text
- A rate metric has no previous sample on the first cycle for a service, so its
  value is NOT READY.
- A metric condition over a not-ready value evaluates to false; it must never
  fire a remediation on a value the collector could not compute yet.
- The first real comparison happens on the second cycle. Document this small
  warm-up so operators do not expect a cpu rule to fire on the very first tick.
```

The `Check.Run` interface stays single-shot and stateless: a metric check is a
thin reader over the collector. The state lives in the collector, keyed by
service (and one system collector), and is reset when a service is reloaded.

---

## 13. Rule model

Package: `internal/rules`

Rules are declarative logical trees.

`rules` is a map keyed by the rule name, like `checks`, `preflight` and
`processes` (see section 9). The key is the rule name; there is no separate
`name` field inside the entry. A rule has:

```yaml
rules:
  RULE_NAME:
    type: remediation | guard | alert
    if: {}
    for: {}
    within: {}
    then: {}
```

Keying rules by name lets a service override or disable a single inherited rule
(for example change `for.cycles`, or set `enabled: false`) exactly the way check
overrides work. Only `if` and `then` are mandatory.

Rule evaluation does not depend on map order. Guards are always evaluated before
remediation (by `type`), and at most one remediation action runs per service per
cycle; when several remediation rules are satisfied at once, they are considered
in sorted key order and the first non-blocked action wins.

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
    check: backup-flag
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

`state` uses the servicemgr statuses: `active`, `inactive`, `failed`, `unknown`
(same set as the service check `expect`, section 12).

### Process condition

```yaml
if:
  process:
    exe: /usr/sbin/mysqld
    user: mysql
    state: running
```

`state` is `running` (default), `zombie` or `absent`, as defined for the process
check in section 12.

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
  port-783-down:
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
  high-cpu:
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
  high-memory:
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
// Rules are loaded as `Rules map[string]Rule`, keyed by rule name, like checks.
// The map key is the canonical name; Rule.Name is filled from the key during
// resolution and is not read from a yaml `name` field.
type Rule struct {
    Name   string    `yaml:"-"`
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

A rule's `then:` takes a single action:

```yaml
then:
  action: restart
```

or a list:

```yaml
then:
  actions:
    - type: alert
      message: Redis memory high
    - type: restart
```

Implemented actions: `restart`, `start`, `stop`, `block`, `alert`.
`exec` is reserved but not implemented (see TODO.md).

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

// Action is one resolved action of a rule's `then:` block (single `action`
// or an `actions` list).
type Action struct {
    Type    ActionType `yaml:"action"`
    Message string     `yaml:"message,omitempty"`

    // exec action only — reserved, not implemented (TODO.md). Array form only.
    Command []string      `yaml:"command,omitempty"`
    Timeout time.Duration `yaml:"timeout,omitempty"`
}
```

`message` is mandatory in practice for `block` and `alert`, where it is the
reason shown to the operator and recorded in the event; it is optional for
`restart`/`start`/`stop`. The guard examples in sections 17 and 25-27 rely on it.

### Remediation policy: cooldown and rate limiting

Automatic remediation must never enter a restart loop. Every service has a
resolved remediation policy that gates how often actions may actually run:

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
                    same service. Required and positive for MVP.
max_actions         maximum number of executed remediation actions allowed
                    inside max_actions_window. Optional for MVP.
max_actions_window  sliding window for max_actions. Optional for MVP.
backoff             optional exponential growth of the effective cooldown after
                    each consecutive remediation.
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
`5m` in the reference config), merged in as the base layer during resolution
(section 8, step 5a).

Validation is performed on the resolved service, after defaults, daemons,
clones, overrides and variable expansion are applied. Therefore a daemon or
service document may omit `policy.cooldown` if it inherits one, but the rendered
service must contain a positive `policy.cooldown`. A value of `0s` is invalid:
it disables the loop-prevention mechanism and must not be used as a way to opt
out of cooldown.

---

## 17. Guard rules

Guard rules block unsafe actions.

Example: block restart if config is invalid.

```yaml
rules:
  block-restart-if-config-invalid:
    type: guard
    blocks:
      - restart
      - start
    if:
      failed:
        check: config
    then:
      action: block
      message: "Configuration invalid, restart blocked"
```

Example: block stop/restart during backup.

```yaml
rules:
  block-restart-during-backup:
    type: guard
    blocks:
      - restart
      - stop
    if:
      or:
        - active:
            check: backup-flag
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

Before `Start`, `Stop` or `Restart` executes any backend action, the engine
acquires the internal operation lock, checks named runtime locks, and evaluates
guards for the requested action. Required preflight checks run before `Start`
and `Restart`; a failed required preflight blocks the action with
`preflight_failed`. `Stop` does not require preflight by default, but still
honors locks and guards.

Restart flow:

```text
1. Load resolved service.
2. defer: emit exactly one event from the final result. Registered FIRST, so it
   fires on every exit path below, including a failed lock acquisition.
3. Acquire the internal operation lock for the service (see below). On failure,
   set the result to blocked and return (step 2 still emits the event).
4. defer: release the internal operation lock. Registered ONLY after a successful
   acquire, so the engine never releases a lock it does not hold.
5. Check Sermo runtime locks: if any active named lock exists for this service
   (section 20, category 1), return blocked. This is automatic and needs no rule.
6. Run preflight checks required for restart. If preflight fails, return
   preflight_failed.
7. If any guard blocks restart, return blocked.
8. Execute backend Stop.
9. Wait graceful_timeout, then discover residual processes.
10. If residual processes remain:
    - if force_kill=false, return orphan_processes.
    - if force_kill=true, apply the signal escalation policy.
11. Rediscover residual processes after any signal escalation. If any remain,
    return orphan_processes and do NOT start the service.
12. After a clean stop, reconcile init state (`ResetState`: systemd
    `reset-failed`, OpenRC `zap`) so a stuck/failed marker cannot disagree with
    the processes. Best effort — never fails an already-successful stop.
13. Execute backend Start.
14. Verify final service status.
15. Run postflight checks.
16. Return the result (ok or the relevant failure status).
```

Every numbered step from 5 onward is a possible early return. The two deferred
steps mean cleanup lives in exactly two places: the event always fires (step 2)
and the lock is always released when held (step 4), no matter which step returns
or whether the function panics. Implement this with Go `defer`, ordered exactly
as above; do not repeat release/emit at each return.

The internal operation lock:

```text
- It serializes start/stop/restart for one service so two operations never run
  concurrently (a manual sermoctl action and an automatic sermod remediation, or
  two manual actions). Path: `<paths.runtime>/ops/<service>.lock` (default
  `/run/sermo/ops/<service>.lock`).
- Acquire it atomically with O_CREAT|O_EXCL, following the lock lifecycle in
  section 20.
- If it is already held by a LIVE owner, fail fast: return a blocked result with
  exit code 75 and message "operation in progress". The engine never waits or
  queues.
- If the existing lock is STALE (expired TTL, or a dead owner PID), reclaim it
  through the logged reclaim path of section 20, then acquire and proceed.
- It is distinct from named runtime locks under `<paths.runtime>/locks`
  (section 20): those guard against external work like backups; this one guards
  against overlapping operations.
- It lives outside `<paths.runtime>/locks` on purpose, so it cannot collide with
  a valid named runtime lock such as `mysql.op.lock`, and the named runtime lock
  scanner must never report the operation lock as a user-held lock.
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
    ResultOK               ResultStatus = "ok"
    ResultBlocked          ResultStatus = "blocked"
    ResultPreflightFailed  ResultStatus = "preflight_failed"
    ResultPostflightFailed ResultStatus = "postflight_failed"
    ResultFailed           ResultStatus = "failed"
    ResultOrphanProcesses  ResultStatus = "orphan_processes"
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

## 19. Preflight and postflight

Preflight checks run before dangerous actions.

Example:

```yaml
preflight:
  config:
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

For MVP, implement preflight and postflight by reusing the check runner.

Postflight checks run after a successful backend `Start`, and after the `Start`
phase of a safe restart. They use the same check schema and runner as
`preflight` and `checks`, and are maps keyed by check name:

```yaml
postflight:
  http:
    type: http
    url: http://127.0.0.1/health
    expect_status: 200
    timeout: 5s
```

Required postflight entries (the default, `optional:false`) are assertions that
the action completed but the service did not become healthy. A required
postflight failure returns `postflight_failed`, records the failed checks in the
result and event, and exits like a failed check. It does NOT automatically roll
back or stop the service. Optional postflight entries behave like optional
preflight entries: failures are warnings recorded in the result and event.

`Stop` does not run postflight checks. Use service status/residual process
handling to validate stop operations.

### Optional preflight entries

A preflight entry may set `optional: true`:

```yaml
preflight:
  libraries:
    type: libraries
    binary: /usr/sbin/apache2
    optional: true
```

```text
- A required preflight entry (the default, optional:false) that fails blocks the
  action and returns preflight_failed.
- An optional preflight entry that fails is recorded as a warning in the result
  and event, but does NOT block the action.
- An optional entry that cannot run at all (tool missing) is treated the same as
  a failed optional entry: a warning, not a block.
```

Use `optional` for best-effort validations such as `libraries` (ldd), which can
be unreliable; never for the authoritative config test.

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

Important: do not run `ldd` on untrusted arbitrary user-uploaded binaries. This
is an admin tool that reads root-managed configuration, so it is acceptable with
a warning in documentation (native ELF parsing is in TODO.md).

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

1. Named runtime lock files under `<paths.runtime>/locks`
   (default `/run/sermo/locks`).
2. External lock checks defined in daemon definitions.

Scope:

```text
- The operation engine reads active named runtime locks and blocks service
  actions while they are active.
- `sermoctl locks SERVICE` reports active, expired and stale named runtime locks.
- `sermoctl lock` (wrap a command), `sermoctl lock acquire` and `sermoctl lock
  release` create and release named runtime locks.
```

The internal operation lock from section 18 is deliberately separate from this
namespace. It uses `<paths.runtime>/ops/<service>.lock` (default
`/run/sermo/ops/<service>.lock`), follows the same atomic lifecycle, and
serializes overlapping operations. It is not created by
`sermoctl lock`, is not listed as a named runtime lock, and cannot be released by
`sermoctl lock release`.

CLI lock command:

```bash
sermoctl lock mysql --name backup --reason "backup mysql" --ttl 4h -- mysqldump --single-transaction --all-databases
```

Lock file naming:

```text
<paths.runtime>/locks/<service>[.<name>].lock

default runtime:
sermoctl lock mysql               -> /run/sermo/locks/mysql.lock
sermoctl lock mysql --name backup -> /run/sermo/locks/mysql.backup.lock
```

A service may hold several named locks at once (for example `backup` and
`migration`). The example above creates `/run/sermo/locks/mysql.backup.lock`.
That path is checked automatically by the operation engine; no guard or
`file_exists` check should point at it.

Lock file JSON format:

```json
{
  "service": "mysql",
  "name": "backup",
  "reason": "backup mysql",
  "owner_pid": 12345,
  "owner_start_ticks": 884512,
  "created_at": "2026-06-05T12:00:00Z",
  "expires_at": "2026-06-05T16:00:00Z"
}
```

`owner_start_ticks` is the owner process start time (field 22 of
`/proc/<pid>/stat`). It is recorded so a stale lock left by a crashed owner can be
told apart from a live one even after PID reuse.

### Lock lifecycle

Acquisition is atomic:

```text
1. Create the lock file with O_CREAT|O_EXCL under the lock namespace directory
   (`<paths.runtime>/locks` for named runtime locks, `<paths.runtime>/ops` for
   operation locks).
2. If it already exists, the existing lock is held UNLESS it is stale (below).
   A new holder must never silently overwrite a live lock.
3. Write the JSON payload and fsync the file, then fsync the directory so a lock
   that exists is always complete and readable after a crash.
```

A lock is **not active** (it is ignored, and may be reclaimed) when any of:

```text
- expires_at is in the past (TTL elapsed); or
- owner_pid is set and no process with that PID is alive (kill(pid, 0) fails); or
- a process with owner_pid is alive but its start time does not match
  owner_start_ticks (the PID was reused by an unrelated process).
```

Otherwise, the lock is **active** and blocks the actions its guards cover.

Reclaiming a stale lock:

```text
- Only sermod or sermoctl reclaim a stale lock, and only after emitting an event
  that says why it was stale (expired vs dead owner vs PID reuse).
- Reclaim is: read, confirm still stale, unlink, then acquire fresh. If the lock
  turned active between the check and the unlink, abort and treat it as held.
```

Release:

```text
- `sermoctl lock SERVICE -- COMMAND` holds the lock for the lifetime
  of COMMAND and unlinks it when COMMAND exits, on any path including a signal,
  via deferred cleanup. If COMMAND is killed, the TTL still bounds the lock's
  lifetime.
- `sermoctl lock acquire` / `sermoctl lock release` manage a lock
  explicitly; release unlinks the owner's lock.
- An owner only removes its own lock; stale removal goes through the reclaim path.
```

TTL guidance:

```text
- --ttl is the maximum lifetime even if the owner never releases (e.g. crash),
  so a lock can never wedge remediation forever.
- Choose a ttl safely above the real duration of the protected work. A ttl that
  expires mid-backup would wrongly unblock restarts; this trade-off belongs in
  docs/safety.md.
```

Go model:

```go
// ActiveLock is a lock currently considered active. Referenced by operation
// results (section 18) and by `sermoctl locks SERVICE`.
type ActiveLock struct {
    Service   string    `json:"service"`
    Name      string    `json:"name,omitempty"`
    Reason    string    `json:"reason,omitempty"`
    OwnerPID  int       `json:"owner_pid"`
    CreatedAt time.Time `json:"created_at"`
    ExpiresAt time.Time `json:"expires_at"`
    Path      string    `json:"path"`
}
```

### Two blocking mechanisms, and when to use each

The two lock categories are complementary, not two ways to do the same thing:

```text
Category 1 — Sermo named runtime locks (the preferred path when you can wrap
the work).
  The operation engine blocks automatically on any active named lock for the
  service (section 18, step 5). No rule is needed. `sermoctl lock ... -- COMMAND`
  creates them.

Category 2 — external lock CHECKS gated by a guard.
  A check (file_exists, process, ...) over a signal Sermo does NOT own: a backup
  process not represented by a Sermo named runtime lock, or a foreign lock/flag
  file written by another tool. Gate it with a guard rule.
  Use it when the protecting job exposes a foreign signal that Sermo can
  check safely.
```

They compose: an action is blocked if a Sermo named runtime lock is active OR a
guard blocks it. Both run on every operation.

Do not model the same signal both ways. A `file_exists` check pointing at a path
under `<paths.runtime>/locks/` duplicates the engine's category-1 check and is a
sign the guard should be removed (use the runtime lock) or the check should point
at a foreign signal instead. Category-2 checks should reference foreign processes
or foreign files, never Sermo's own lock files.

External lock check example (category 2 — a backup tool represented by a
foreign process signal):

```yaml
checks:
  mariabackup:
    type: process
    exe: /usr/bin/mariabackup
    user: mysql
    state: running

rules:
  block-restart-during-backup:
    type: guard
    blocks: [restart, stop]
    if:
      active:
        check: mariabackup
    then:
      action: block
      message: "MySQL backup is running"
```

A backup wrapped with `sermoctl lock mysql --name backup -- ...` needs no such
guard: the named runtime lock blocks the restart on its own.

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
    Exe      string    // resolved /proc/<pid>/exe, NOT argv[0]
    Cmdline  []string  // informational only, never used for matching
    Role     string
    Source   string
}
```

### Process identity

How each field is read is security-critical, because kill decisions depend on it:

```text
Exe   the resolved target of the /proc/<pid>/exe symlink: the absolute, real path
      of the running binary. It is NEVER argv[0]/cmdline[0], which a process can
      set to any string and is therefore unsafe to trust.
UID   the real UID from /proc/<pid>/status (Uid: line, first value); User is that
      UID resolved via the passwd database.
Cmdline  read from /proc/<pid>/cmdline for display and logging only; never used
      for matching or kill decisions.
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

### Matching rules

Selectors (`command_match`, `kill_only_if`) match on identity, never on a name:

```text
- An exe selector (command_match.exe, kill_only_if.exe_any) matches only by EXACT
  equality against the resolved /proc/<pid>/exe path, after canonicalizing both
  sides (symlink resolution + path clean). No basename, prefix or substring
  match — "mysqld" must never match by appearing somewhere in a string.
- A user selector matches the process real UID exactly.
- command_match requires ALL of its declared fields to match (exe AND user, when
  both are given).
```

Unresolvable exe — fail-safe:

```text
- If /proc/<pid>/exe cannot be read (permission), or resolves to a "(deleted)"
  path (the binary was replaced, e.g. after a package upgrade), the process does
  NOT match any exe selector. It is reported as a residual with exe unknown, and
  any kill that depends on exe matching will NOT touch it.
- Leaving an unidentifiable process alive is safer than killing the wrong one;
  log it so an operator can investigate.
```

Safety rule:

```text
Never kill a process based only on a partial name match, and never on cmdline.
A kill requires an exact resolved-exe and real-UID match against kill_only_if.
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

Any `stop_policy` field omitted by a daemon or service inherits from
`defaults.stop_policy` in the global config, which is merged in as the base layer
during resolution (section 8, step 5a). Daemons should still state the timeouts
that matter for that application explicitly, so the behavior is readable without
cross-referencing the defaults.

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
4. if no residuals, success (ok)
5. if residuals and force_kill=false, fail with orphan_processes
6. if residuals and force_kill=true:
   - classify each residual. A residual is KILLABLE only if every field matches
     kill_only_if: exact resolved /proc/<pid>/exe AND real UID (section 21). A
     residual with an unresolvable exe is never killable.
   - SIGTERM the killable set; wait term_timeout; rediscover.
   - SIGKILL any of the killable set still present (policy already allows it);
     wait kill_timeout; rediscover.
   - a residual that never matched kill_only_if is NEVER signalled.
7. final result:
   - ok only if no residuals remain at all.
   - otherwise orphan_processes, whether the remaining process was deliberately
     left alone (did not match kill_only_if) or survived SIGKILL. The result
     lists every remaining process so an operator can act.
```

Sermo only ever signals processes that exactly match `kill_only_if`, so a partial
cleanup never touches an unauthorized process. A residual it is not allowed to
identify and kill is reported, not killed: a clean `orphan_processes` failure is
safer than killing the wrong thing or falsely reporting success.

After a stop or the stop phase of a restart returns `orphan_processes`, the
service must NOT be started (a restart aborts without starting). Auto-start after
a failed stop is only allowed if policy explicitly enables it.

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
sermoctl config diff BASE SERVICE

sermoctl daemon list
sermoctl daemon show DAEMON

sermoctl service list
sermoctl service show SERVICE
sermoctl service clone SOURCE TARGET

sermoctl lock SERVICE --reason REASON --ttl DURATION -- COMMAND...
sermoctl lock acquire SERVICE --reason REASON --ttl DURATION
sermoctl lock release SERVICE
```

The CLI has since grown beyond this core surface (`apps`, `libs`, `patterns`,
`services`, `monitor`/`unmonitor`, `sla`, `diagnose`, `reload`, `wizard`);
`sermoctl` with no arguments prints the authoritative usage.

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

`sermod` startup:

```text
1. Load global config.
2. Load and resolve daemons/services into flat definitions.
3. Detect the service backend.
4. Start the scheduler: one independent worker per enabled service.
5. Block until SIGTERM/SIGINT, then shut down cleanly.
```

### Scheduler concurrency

Each enabled service is monitored by its own worker (goroutine) with an
independent ticker at `engine.interval`. Workers do not share a cycle, so a long
operation on one service never blocks monitoring of another. This is the core
rule: the daemon must never serialize all services through a single loop, because
a restart can take minutes (`graceful_timeout` + `term_timeout`) and would freeze
every other service's monitoring.

A service worker cycle is:

```text
- run this service's checks and cache results (section 14)
- evaluate guards, then remediation/alert rules
- if a remediation rule fires and is not blocked, consult the service policy
  (cooldown, max_actions); if allowed, run the operation through the shared engine
- update remediation state (LastActionAt, RecentActions, backoff)
- emit events, recording whether the action ran or was suppressed and why
```

The cycle is synchronous WITHIN a service: checks, evaluation and any operation
run one after another for that service. Pausing one service's monitoring while
its own operation runs is fine — monitoring a service mid-restart is meaningless,
and the internal operation lock (section 18) already forbids a second concurrent
operation on it.

Cycle overlap. A cycle (checks + evaluation + any operation) can exceed
`interval`, mainly when an operation is in progress. The rules:

```text
- If a worker's cycle is still running when its next tick fires, that tick is
  SKIPPED, not queued. Ticks never accumulate: a cycle that overruns by several
  intervals causes several skips, not a backlog of catch-up cycles afterwards.
- Skipping is per service: one service overrunning never delays another worker.
- A multi-interval operation (a 120s restart with a 30s interval) is expected to
  skip ~4 ticks; the next normal cycle resumes only after the operation returns.
- This is not an unbounded stall: every operation runs under its own timeouts
  (graceful/term/kill) and the internal operation lock is TTL-bounded (section
  20), so a worker cannot be wedged in one cycle forever.
- Log a skipped tick at debug, and the count of consecutive skips so a service
  stuck mid-operation is observable.
```

Implement this by computing the next tick from cycle COMPLETION (or by draining a
ticker that drops ticks), never by a fixed queue of pending ticks.

Bounded concurrency, to avoid a correlated failure triggering a restart storm:

```text
- Workers start with a small per-service offset (jitter) so ticks spread across
  the interval instead of all firing at once.
- Operations across all services share a global operation semaphore (small
  default). A worker that wants to operate waits for a slot; only that service's
  monitoring pauses while it waits, so mass restarts serialize safely.
- Check execution is bounded separately by engine.max_parallel_checks.
```

Shutdown:

```text
- On SIGTERM/SIGINT, stop starting new cycles and cancel each worker's context.
- An in-flight operation observes the cancelled context, stops waiting on its
  timeouts and returns; its deferred cleanup (section 18) releases the internal
  lock and emits the event. A partially stopped service is left as-is, never
  force-killed because of shutdown.
- Wait for workers to return, up to a bounded shutdown grace, then exit.
- Never start a new operation during shutdown.
```

SIGHUP: reload config from disk (validate, then swap workers/watches while
preserving per-service runtime state). Invalid config is rejected and the running
generation is left unchanged.

Initial `sermod` command:

```bash
sermod run --config /etc/sermo/sermo.yml
```

Optional foreground mode only for MVP. Packaging can run it as a normal daemon under systemd/OpenRC later.

---

## 30. Config validation requirements

`sermoctl config validate` must check:

```text
- YAML syntax is valid.
- Each document has kind and name.
- Service names are unique.
- Daemon names are unique.
- uses points to an existing daemon.
- clone points to an existing service.
- Clone cycles are rejected.
- Variables referenced with ${...} exist.
- A variable value must not itself contain ${...} (no nested variables in MVP).
- No ${...} remains after a single expansion pass.
- rules is a map keyed by name; rule names are unique within a service.
- Each rule has if and then (the name is the map key).
- Each condition node has exactly one condition/operator.
- for.cycles > 0.
- within.cycles > 0.
- within.min_matches > 0 and <= within.cycles.
- A rule cannot define both for and within in MVP.
- All check references point to existing checks or preflight checks.
- service check expect and service condition state are one of active, inactive,
  failed, unknown.
- process check/condition state is one of running, zombie, absent.
- backend is one of auto, systemd, openrc.
- engine.interval and engine.default_timeout are valid positive durations.
- engine.max_parallel_checks, if set, is an integer > 0.
- paths.runtime, if set, must be an absolute directory. The default is
  `/run/sermo`.
- paths.locks is rejected in the MVP. Named runtime locks are derived from
  `<paths.runtime>/locks`; operation locks are derived from `<paths.runtime>/ops`.
- `/etc/sermo/locks.d` has no MVP semantics and must not be scanned for active
  locks.
- security toggles that try to disable hard safety invariants are rejected. In
  the MVP, reject `security.require_preflight_before_restart`,
  `security.block_restart_on_active_lock`, `security.allow_sigkill_by_default`
  and `security.require_kill_selector`; these are not configurable policy.
- stop_policy.force_kill=true requires kill_only_if.
- kill_only_if must define both users and exe_any, each non-empty. A kill selector
  with only a user or only an executable is invalid because residual signaling
  requires both an exact resolved /proc/<pid>/exe match and a real-UID match.
- command checks and inline command conditions use array form, not shell string.
- inline command conditions must declare a timeout.
- then.action is one of restart, start, stop, alert, block (exec is reserved;
  see TODO.md).
- guard rules must use action block; only guard rules may use block.
- block and alert actions require a non-empty message.
- type: guard requires a non-empty blocks list; non-guard rules must not set blocks.
- aliases keys are valid backends (systemd, openrc); each value is a non-empty list.
- commands entries use array form with an optional valid duration timeout.
- postflight uses the same entry schema and check types as preflight/checks.
- optional, where present on a preflight, postflight or check entry, is a boolean.
- description, where present on a service or daemon, is a string scalar.
- command check expect_exit, where set, is an integer.
- file_exists checks must not point under Sermo's named runtime lock directory
  (`<paths.runtime>/locks`, default `/run/sermo/locks`); the operation engine
  already checks named runtime locks. Point guard checks at a foreign lock/flag
  file Sermo does not own.
- defaults.policy.cooldown must be present and a valid positive duration.
- policy.cooldown, where set in a daemon or service override, must be a valid
  positive duration.
- each resolved service must have policy.cooldown > 0 after defaults, daemon or
  clone data, overrides and variables are applied.
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

Future sinks (TODO.md): JSON file, syslog, Prometheus metrics, webhook.

---
