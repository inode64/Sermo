# Sermo — project conventions

Use the implementation and its tests to establish current behavior, `docs/`
and validated examples for the operator contract, and [TODO.md](TODO.md) for
planned behavior. If they disagree, identify the trigger, verify the behavior
in the owning code, and use the request and executable evidence to resolve the
mismatch. In a review, give the source location and distinguish a reproduced
defect from a hypothesis.

## Domain pointers

Load the skills that cover the decisions the task changes. Edit the sources
under `.agents/skills/`. Claude reads the same files through symlinks,
including this guide through `CLAUDE.md`. Keep public behavior in `docs/`
and update a skill when its implementation assumptions change.

| Decision | Project skill |
|---|---|
| How YAML loads, merges, expands or validates | [Config schema](.agents/skills/sermo-config-schema/SKILL.md) |
| Which checks, selectors, guards or preflight a catalog service should ship | [Profile author](.agents/skills/sermo-profile-author/SKILL.md) |
| How a network probe binds, parses a reply or reports a busy peer | [Protocol probes](.agents/skills/sermo-protocol-probes/SKILL.md) |
| When a condition fires, clears, blocks or requests an action | [Rule engine](.agents/skills/sermo-rule-engine/SKILL.md) |
| What authorizes an operation, process attribution, signal or recovery | [Safety review](.agents/skills/sermo-safety-review/SKILL.md) |
| Which fixture, fake or benchmark exercises the change | [Test engineer](.agents/skills/sermo-test-engineer/SKILL.md) |
| How to stage and clean up an authorized SSH validation run | [Remote testing](.agents/skills/sermo-remote-testing/SKILL.md) |

## Workflow

Before editing: `git status --short --branch`. Work on the current branch in
the current checkout: creating or switching a branch or worktree — a
subagent's `isolation: worktree` included — needs the user's explicit request.
Parallel subagents may read and analyze freely; parallel edits need disjoint
files. Preserve unrelated changes. Commit, push or merge only when asked.
Plans, specs and notes from process skills stay outside the repository (the
session scratchpad or a temporary directory).

Choose the simplest change that meets the request: least code, dependencies,
abstractions and future maintenance. Search for an existing function, type or
interface before adding one; add an abstraction only for a second real caller
or a test seam.

Find the owner of the concept and put the behavior there. If that owner cannot
hold it without becoming a second parser, validator, monitor, notifier or
dispatcher, extract or move — do not add a parallel owner. A generally useful
check, option or behavior belongs on both services and host watches unless an
owner-level limitation is documented in code and user docs.

The `Makefile` and CI own validation. For Go package changes, run `go test -shuffle=on ./internal/<pkg>/`
and `bin/custom-gcl run ./internal/<pkg>/...` (built by `make custom-gcl`).
Finish with:

| Change | Command |
|---|---|
| Markdown only | `make markdown-check docs-sync` |
| YAML formatting/CI files only | `make yaml-validate` |
| Concurrency or shared state | `make check` and `make race` |
| Anything else | `make check` |

Catalog, examples and docs YAML are validated by Go tests, so changing them
needs `make check`.

`make check` is the full gate; do not add a second `go build`, `make lint` or
`go test` pass after it. After editing `internal/web/src/`, run `make web` and
keep the regenerated `internal/web/index.html` and `internal/web/login.html`
in the patch.

When asked to commit, inspect the staged diff and use the repository's
[commit message format](.github/git-commit-instructions.md). Keep unrelated
changes out of the commit and explain any partial staging.

Fleet deployment uses the operator's private tooling. An operator action is
real even when daemon remediation has `dry_run: true`.

## Names, constants, config shape

Use the public model field as the vocabulary across code, comments, JSON, YAML
and docs. Reuse typed constants and enums from the owning package. Name a
value when the name carries meaning (state, kind, config key, protocol, unit,
default, timeout, threshold). The `mnd` and `goconst` analyzers decide the
rest; do not hide fixture data or a one-off error message behind a constant.

One canonical shape, in config and in code. When a change replaces a config
shape, logic or procedure, remove the old one in the same change — parsing,
validation, callers, wrappers, aliases, fallbacks, examples, docs and the
tests and fixtures that only exercised it — unless the user or an external
compatibility or safety requirement says otherwise. Catalog sugar is valid
only when resolution desugars it into the canonical runtime tree and drops
the sugar.

Volatile paths go under `/run`. Normalize host-reported `/var/run/...` to
`/run/...`. Resolve symlinks before adding a catalog or generated path.

## Execution

- Service lifecycle actions (start, stop, restart, reload, resume, pause,
  repair) and the manual closes and `kill_query` go through
  `internal/operation`. CLI, daemon and web never call backend lifecycle
  methods. Any other signal (host watch `then.kill`, the dashboard's
  unowned-process kill, `engine.reap_own_strays`, mount blockers,
  `sermoctl daemon reload`) goes through `process.Signaler`/`process.Reaper`
  (pidfd) with its own explicit authority. Primitive backend/process
  implementations and their fakes are the exception.
- Use Go code (see Dependencies) instead of external commands. Required
  external commands use the injectable `execx` runner, argv, a context and a
  timeout — no shell, no `os/exec` in production. The only product exception is
  `sermoctl lock … -- COMMAND`.
- Add checks, watches, notifiers and rule actions through
  `internal/checks/build.go`, `internal/app/watch_build.go`, `internal/notify`
  or the rule builders. Notifier call sites use configured names only.
- Files Sermo locates on the host (procfs, sysfs, catalog, pidfiles, runtime
  locks and logs) are read through `internal/hostfs`, which accepts only
  absolute, clean paths. Operator-supplied paths are resolved by the CLI first.
  A path the operator, the catalog or a verified process names (a check
  target, a variable's candidate) uses the `hostfs` `*Target` functions, read
  as written; forbidigo rejects a direct `os` read.
- Every blocking command, network, database or file operation has a timeout
  from engine configuration or a named constant. Tests may use short literals
  to bound the test.
- Treat every daemon-cycle path as hot: reuse samples within a cycle, avoid
  repeated host scans, and make expensive work interval-bound. Fix the
  algorithm before micro-optimizing. A performance change keeps only what
  `benchstat` shows as a significant gain in before/after benchmarks at
  worst-case sizes (process count, services, concurrency) on hermetic
  fixtures.

## Dependencies

Reuse repository code; otherwise use the standard library when it suffices.
For complex functionality (a protocol, a file format, a database client)
search for a module before writing one. Compare the two or three best
candidates briefly with current, dated data — releases and commits,
maintainers, adoption, advisories, transitive modules, CGO need, license
compatible with Apache-2.0 — pick the maintained, widely adopted one, and
record the comparison in the commit's Evidence. Write it yourself only when
every candidate is abandoned, has the wrong semantics or brings far more than
the feature needs. A new direct dependency needs its `gomodguard_v2` entry in
`.golangci.yml`.

## Documentation

Update user docs and useful examples when public configuration, CLI, checks,
rules, notifiers, safety or observable behavior changes.
`docs/configuration.md`, `docs/rules.md`, `docs/services.md` and
`examples/sermo.yml` are owners, not a mandatory list for unrelated changes.
`README.es.md` is the only translation; update it when `README.md` changes.
Planned behavior goes in `TODO.md`.

Write for Linux administrators: direct explanations, realistic YAML,
copy-pasteable commands, explicit safety notes. Link to the owner instead of
copying inventories. Code comments say why — a constraint, an invariant, a
non-obvious reason — and never restate the code.

## Quality gates

Idiomatic Go that passes the configured gate without new suppressions. An
error says what was attempted, for a sysadmin reader. No new package-level
mutable state: registration goes in the existing registry, and Linux-specific
behavior stays behind an interface with a fake.
`.golangci.yml` is the linter source of truth. A necessary `//nolint` names
the exact analyzer and the design reason. Custom analyzers are configured in
`.custom-gcl.yml`. Never weaken a gate or broaden an
exclusion to land a change. How to add a `.semgrep/` call-boundary rule is in
[`.semgrep/README.md`](.semgrep/README.md).

Match the owning package's test style; its hermetic seams are listed in
`sermo-test-engineer`. Command execution is scripted with
`internal/execx/execxtest`; add another `execx.Runner` fake only for stateful
or timing behavior. Tests must not operate real services,
signal host processes, or depend on ambient `/etc`, `/proc`, network or init
state. Cover the success, invalid/unsafe, blocked and timeout/error paths the
change actually has.

## Safety

Read [the operator safety policy](docs/safety.md) and the safety review
skill before changing operation authority. Keep these invariants across CLI,
daemon and web entry points.

- Route service operations through the operation engine with a timeout,
  locks, guards and required preflight. Record one outcome per executed or
  blocked action. Named and operation locks are separate, atomic, TTL-bounded
  and auditable on reclaim.
- Automatic remediation requires a positive resolved cooldown. System-scoped
  metrics cannot trigger it. Conditions only observe. Bound shared check
  concurrency and keep slow services from blocking other workers.
- Revalidate signal authority through the pidfd at delivery. Name, basename,
  argv and cmdline alone never establish it. Use the safety skill for exact
  exe and user, unit-cgroup proof, deleted executables, manual session closes
  and the separate requirements for SIGKILL.
- Delegated selectors only observe. Respect `stop_policy.keep_children` and
  block a subsequent start on surviving or unattributed residuals. Strays are
  diagnostic except for `engine.reap_own_strays` (SIGTERM in sermod's own unit
  cgroup at startup).

Database profiles stay conservative. No configuration may disable a hard
invariant.

## Web UI and wizards

- Web UI: sources in `internal/web/src/` (design tokens in `tokens.css`,
  shared by the dashboard and the login page); generated
  `internal/web/index.html` and `internal/web/login.html`. A list panel is a
  descriptor in `app.js` (`listPanels`: columns, status chips, row renderer)
  over one shared toolbar, sort, grouping and saved state, plus its shell in
  `index.html` (`data-panel`, and `data-view` for the tile that leads to it); the Watches shell
  comes from `watch-panels.json`. lit-html, delegated `data-*` clicks (not inline or
  lit handlers), design tokens, no literal CSS colors, existing CSP.
  String-built SVG only in its container. Desktop/mobile and WCAG 2.2 AA
  checks.
- Wizards: `internal/assist` owns prompts, detected targets and the
  `all` / `none` / `default` notifier vocabulary. Never ask for a name
  detection provides; `none` and `default` stay selectable with no notifier
  configured (an empty inherited default is monitor-only); preview and
  confirm generated files; offer cleanup only for targets detection proves
  absent
