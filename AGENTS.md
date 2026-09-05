# Sermo — project conventions

Repository decisions for agents, not a second user manual. Sources of truth:

- current behavior: code and tests
- public behavior and configuration: `docs/` and validated examples
- planned behavior: [TODO.md](TODO.md)
- validation: `Makefile` and CI
- analyzers: `.golangci.yml`, `.custom-gcl.yml`, `.semgrep/`
- Web UI: `internal/web/src/` and its tests
- safety: `internal/operation`, `internal/process`, `internal/locks`,
  [docs/safety.md](docs/safety.md)

If prose and implementation disagree, report the mismatch, establish the
intended behavior from the request and executable evidence, then update every
affected source in the same change.

## Skills

Project skills live in `.agents/skills/<name>/SKILL.md` and are linked from
`.claude/skills/`. A skill records design decisions and traps of one domain.
Public behavior stays in `docs/`. Do not copy this file into a skill. When a
skill and the code disagree, fix the skill in the same change.

## Workflow

Before editing: `git status --short --branch`. Work in the current checkout
unless asked for a branch. Preserve unrelated changes. Commit, push or merge
only when asked.

Find the owner of the concept and put the behavior there. If that owner cannot
hold it without becoming a second parser, validator, monitor, notifier or
dispatcher, extract or move — do not add a parallel owner. A generally useful
check, option or behavior belongs on both services and host watches unless an
owner-level limitation is documented in code and user docs.

Run targeted checks while developing. Finish with:

| Change | Command |
|---|---|
| Markdown only | `make markdown-check` |
| YAML only | `make yaml-validate` |
| Anything else | `make check` |

`make check` is the full gate; do not add a second `go build`, `make lint` or
`go test` pass after it. After editing `internal/web/src/`, run `make web` and
keep the regenerated `internal/web/index.html` in the patch.

When asked to commit:

```text
<type>(<optional-scope>): <concise description>

Objective: <outcome>
Invariant: <behavior or safety property preserved>
Evidence: <checks and runtime validation actually run>
Limitations: <known boundary or None.>
```

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `ci`,
`perf`. Do not identify an agent as author. Never leave unexplained partial
staging.

Fleet work uses the `sermo-remote-testing` skill. An operator action is real
even when daemon remediation has `dry_run: true`.

## Names, constants, config shape

Use the public model field as the vocabulary across code, comments, JSON, YAML
and docs. Reuse typed constants and enums from the owning package. Name a
value when the name carries meaning (state, kind, config key, protocol, unit,
default, timeout, threshold). The `mnd` and `goconst` analyzers decide the
rest; do not hide fixture data or a one-off error message behind a constant.

One canonical config shape. Remove the old shape from parsing, validation,
examples, docs and tests in the same change unless the user or an external
compatibility or safety requirement says otherwise. Catalog sugar is valid
only when resolution desugars it into the canonical runtime tree and drops
the sugar.

Volatile paths go under `/run`. Normalize host-reported `/var/run/...` to
`/run/...`. Resolve symlinks before adding a catalog or generated path.

## Execution

- Start, stop, restart, reload, resume and signal go through
  `internal/operation`. CLI, daemon and web do not call backends or signal
  processes; primitive backend/process implementations and their fakes are
  the exception.
- Prefer the Go standard library or a module. Required external commands use
  the injectable `execx` runner, argv, a context and a timeout — no shell, no
  `os/exec` in production. The only product exception is
  `sermoctl lock … -- COMMAND`.
- Add checks, watches, notifiers and rule actions through
  `internal/checks/build.go`, `internal/app/watch_build.go`, `internal/notify`
  or the rule builders. Notifier call sites use configured names only.
- Every blocking command, network, database or file operation has a timeout
  from engine configuration or a named constant. Tests may use short literals
  to bound the test.
- Treat every daemon-cycle path as hot: reuse samples within a cycle, avoid
  repeated host scans, and make expensive work interval-bound.

## Documentation

Update user docs and useful examples when public configuration, CLI, checks,
rules, notifiers, safety or observable behavior changes.
`docs/configuration.md`, `docs/rules.md`, `docs/services.md` and
`examples/sermo.yml` are owners, not a mandatory list for unrelated changes.
`README.es.md` is the only translation; update it when `README.md` changes.
Planned behavior goes in `TODO.md`.

Write for Linux administrators: direct explanations, realistic YAML,
copy-pasteable commands, explicit safety notes. Link to the owner instead of
copying inventories.

## Quality gates

Idiomatic Go that passes the configured gate without new suppressions.
`.golangci.yml` is the linter source of truth. A necessary `//nolint` names
the exact analyzer and the design reason. Never weaken a gate or broaden an
exclusion to land a change. How to add a `.semgrep/` call-boundary rule is in
[`.semgrep/README.md`](.semgrep/README.md).

Match the owning package's test style. Tests must not operate real services,
signal host processes, or depend on ambient `/etc`, `/proc`, network or init
state. Cover the success, invalid/unsafe, blocked and timeout/error paths the
change actually has.

## Safety

Operator policy: [docs/safety.md](docs/safety.md). Review checklist:
`sermo-safety-review`. Hard boundaries:

- service actions use `internal/operation`, a timeout, operation locks,
  guards and required preflight
- automatic remediation uses the same path, needs a positive resolved
  cooldown, and never triggers from a system-scoped metric
- process authorization never uses name, basename, argv or cmdline alone;
  signalable processes require exact resolved `exe` and `user`
- `SIGKILL` requires `force_kill` plus restrictive `kill_only_if`
- unmatched residuals are orphans and block a following start
- conditions are read-only; mutation belongs to actions
- named locks and operation locks stay separate, atomically acquired,
  TTL-bounded and auditable when reclaimed
- each executed or blocked action records one auditable outcome
- one slow service must not block monitoring of another; shared check
  concurrency stays bounded

Database catalog profiles stay conservative. No config option may disable a
hard invariant.

## Domain pointers

Load the matching skill when the task is in that domain. Do not copy these
into a change that does not touch them.

- Config, catalog, merge, variables, templates: `sermo-config-schema`;
  [docs/configuration.md](docs/configuration.md),
  [docs/services.md](docs/services.md)
- Rules, windows, guards, policy: `sermo-rule-engine`;
  [docs/rules.md](docs/rules.md)
- Catalog service definitions: `sermo-profile-author`
- Protocol probes: honor `cfg.Interface`; register in
  `internal/conn/registry.go`; dial with `BindDialer`, listen with
  `BindListenConfig`
- Web UI: sources in `internal/web/src/`; generated
  `internal/web/index.html`; repetitive watch-panel metadata in
  `watch-panels.json`. lit-html, delegated `data-*` clicks (not inline or
  lit handlers), design tokens, no literal CSS colors, existing CSP.
  String-built SVG only in its container. Desktop/mobile and WCAG 2.2 AA
  checks.
- Wizards: `internal/assist` owns prompts, detected targets and the
  `all` / `none` / `default` notifier vocabulary
- Instanced catalog services: `${hostname}`, `%n` / `${n}`; templates
  materialize, the operator enables; [docs/services.md](docs/services.md)

`graphify-out/` is local and gitignored. Never stage it. Query it only when it
exists and is useful; verify in the owning files.
