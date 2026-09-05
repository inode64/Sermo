# Sermo — project conventions

This file contains repository-specific decisions for agents. It is not a second
user manual or a copy of executable configuration. Use these sources of truth:

- current behavior: code and tests;
- public behavior and configuration: `docs/` and validated examples;
- planned behavior: [TODO.md](TODO.md);
- validation commands: `Makefile` and CI workflows;
- Go analyzer policy: `.golangci.yml`, `.custom-gcl.yml` and `.semgrep/`;
- Web UI behavior: `internal/web/src/`, its tests and
  [docs/webui-representation.md](docs/webui-representation.md);
- operational safety: `internal/operation`, `internal/process`,
  `internal/locks` and [docs/safety.md](docs/safety.md).

If prose and implementation disagree, report the mismatch, establish the
intended behavior from the request and executable evidence, then update every
affected source in the same change. Do not preserve a contradiction by treating
this file as more authoritative than the code it describes.

## AI / agent workflow — standard git commits

Before editing, run:

```sh
git status --short --branch
```

Work in the current checkout unless the user asks for a branch. Preserve all
unrelated tracked and untracked changes. Search with `rg`, extend the existing
owner, add focused tests, and keep the patch scoped.

Run targeted checks while developing. Finish with the smallest complete gate:

| Change | Required finish command |
|---|---|
| Markdown only | `make markdown-check` |
| YAML only | `make yaml-validate` |
| Go, scripts, Web UI, build files or mixed changes | `make check` |

`make check` is the single full gate; the `Makefile` owns its current phases.
Do not duplicate it with a second `go build`, `make lint` or `go test` pass.
Run it once inside the project sandbox: the repository configuration grants
only the package and vulnerability registry access it needs plus exact loopback
for Playwright, and the target routes tool caches to `/tmp`. If a managed policy
overrides that configuration or exact loopback support has not already been
verified for the active sandbox implementation, request the required elevation
before starting the gate; do not use a first full run as the probe and then
repeat it elevated.
After editing `internal/web/src/`, run `make web` before the finish gate and keep
the regenerated `internal/web/index.html` in the patch.

Commit only when the user asks for a commit, merge or equivalent integration.
Use:

```text
<type>(<optional-scope>): <concise description>

Objective: <outcome>
Invariant: <behavior or safety property preserved>
Evidence: <checks and runtime validation actually run>
Limitations: <known boundary or None.>
```

Valid types are `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`,
`ci` and `perf`. Do not identify an agent as author. Do not push or merge unless
the user explicitly asks, and never leave unexplained partial staging.

For fleet work, follow the `sermo-remote-testing` skill and its staged-host
workflow. An operator action is real even when daemon remediation is configured
with `dry_run: true`; use Sermo's CLI or dashboard action path and investigate
any failure Sermo cannot safely repair.

## Reuse and shared behavior

Treat an example or failing test as evidence of an invariant, not the whole
scope. Search equivalent CLI, daemon, web, service and watch paths before adding
logic. Prefer, in order: reuse the owner unchanged, extend that owner, add a
small private helper beside it, then add a shared package only when ownership
crosses package boundaries.

Do not create a second parser, validator, monitor, notification path or action
dispatcher for the same concept. A generally useful check, option or behavior
belongs on both services and host watches unless an owner-level limitation is
documented in code and user docs.

## Constants and repeated values

Reuse typed constants and enums from the owning package. Name concept-bearing
values such as states, kinds, config keys, protocols, units, defaults, timeouts
and thresholds even when they occur only once. Create a constant for ordinary
repetition when it appears more than three times. Do not hide fixture data or a
one-off error message behind a constant without a correctness benefit.

## Naming and terminology

Use the public model field as the canonical vocabulary across code, comments,
JSON, YAML and docs. Do not introduce near-synonyms for an existing concept.
When a canonical term collides with a Go builtin, keep the public name and use
the established local alias; for example, `Max` / `"max"` uses `limit` in a
local variable.

## Configuration structure changes

For Sermo-owned configuration, prefer one canonical shape and remove the old
shape from parsing, validation, examples, docs and tests in the same change.
Compatibility requires an explicit user request or an external compatibility
or safety requirement. Do not add fixtures that keep retired field names alive.

Catalog sugar is allowed only when resolution desugars it into the canonical
runtime tree and removes the sugar. Document new sugar in
[docs/services.md](docs/services.md).

## Runtime paths

Write volatile paths under `/run`, including pidfiles, sockets and locks.
Normalize host-reported `/var/run/...` paths to `/run/...`; this Linux
compatibility is not a second Sermo config spelling. Resolve symlinks before
adding a catalog or generated path so aliases do not become duplicate targets.

## Configuration file granularity

Use one YAML document of one target kind per file. Kind comes from the catalog
subdirectory or configured services/watches directory, so omit redundant
`kind:` fields. A notifier fragment is the narrow exception: its top-level
`notifiers:` map contains exactly one named entry. Reference bundles such as
`docs/sermo-all.yml` may group schema examples.

Classified watch directories (`watches/`, `networks/`, `storages/`, `mounts/`)
must all be listed under `paths.watches`; each `.local` sibling is the per-host
override layer. For source-tree validation, build with
`SERMO_DATADIR=$PWD make build` and use `examples/sermo-dev.yml`.

## Catalog service scope

A service check describes the service process. State of a host resource the
service observes belongs in a host watch; use `reports: state` or
`reports: value` only when observed data must remain visible without affecting
service health or availability. `smartd` and its generated drive watches are
the reference model.

## Catalog init and reload fallback verification

For catalog changes involving init metadata or `reload.signal`, verify every
declared systemd/OpenRC backend and every fallback. An OpenRC signal fallback
requires a canonical pidfile plus an exact `exe` and `user` process selector;
otherwise use an argv `reload.command` or the backend's native reload.

Run the focused catalog contract before the finish gate:

```sh
go test ./internal/config -run 'TestRealCatalog(AllServicesValidate|ReloadServicesResolve)$' -count=1
```

The complete operator procedure lives in [docs/services.md](docs/services.md).

## Service operations

All application-level start, stop, restart, reload, resume and signal actions
go through `internal/operation`. CLI, daemon and web code must not call service
backends or signal processes directly. Primitive backend/process
implementations and their fakes are the only narrow exceptions.

## Native by default

Prefer the Go standard library or a Go module. Required external commands use
the injectable `execx` runner, argv arrays, a context and an explicit timeout;
production code does not use a shell or call `os/exec` directly.

The sole product exception is `sermoctl lock … -- COMMAND`, which intentionally
runs the operator's foreground argv with inherited standard streams. Do not
generalize that exception.

## Protocol probes: interface binding is mandatory

Every `internal/conn` protocol probe honors `cfg.Interface`. Built-ins and
aliases are registered in `internal/conn/registry.go`; registered probes enter
the shared executor once and obtain the prepared target through the existing
helpers. Do not add package-init registration or duplicate endpoint defaults.

Stream probes dial through `BindDialer`; packet listeners use
`BindListenConfig`. A library is acceptable only if it is codec-only or accepts
Sermo's dialer/connection. Reject libraries that perform unhookable internal
I/O, because default routing would violate the interface invariant.

Keep documented transport exceptions local: chronyd's Unix datagram client and
DHCP's per-datagram `IP_PKTINFO` path. A protocol may rebuild an endpoint only
when its wire format genuinely selects a different target, with the reason
documented at that call.

## Documentation lockstep

Update user docs, both language versions and useful examples whenever public
configuration, CLI, checks, rules, notifiers, safety or observable behavior
changes. `docs/configuration.md`, `docs/rules.md`, `docs/services.md` and
`examples/sermo.yml` are owners, not a mandatory list for unrelated changes.

When new behavior is planned rather than implemented, put it in `TODO.md`; do
not describe it here as current behavior.

## Documentation scope and style

Write for Linux administrators: direct explanations, realistic YAML,
copy-pasteable commands and explicit safety notes. Document public behavior,
required maintenance rationale and non-obvious safety exceptions. Link to the
owner instead of copying long implementation inventories or tool settings.

## Central builders

Add checks, watches, notifiers and rule actions through the owning central
builder or registry. Extend `internal/checks/build.go`,
`internal/app/watch_build.go`, `internal/notify` or the rule builders instead of
scattering construction switches through callers.

Notifier call sites reference configured names only; a new transport is built
and registered inside `internal/notify` plus its user documentation.

## Timeout discipline

Every blocking command, network, database or file operation has a timeout from
engine configuration or a named constant. Tests may use short literals to bound
the test itself. Never add an unbounded production wait.

## Daemon performance discipline

Treat every daemon-cycle path as hot. Reuse samples within a cycle, avoid
repeated host scans, unnecessary allocation/sorting and blocking work in
scheduler-critical sections. Make expensive work explicit and interval-bound;
add a benchmark when fleet-scale cost is not obvious.

## Small-change checklist

- Inspect Git state and preserve unrelated changes.
- Search for the existing owner and equivalent surfaces.
- Keep public names stable unless the task is an explicit migration.
- Add or update focused tests and user docs.
- Review timeout, daemon cost and safety impact.
- Run the required finish gate and report the working-tree state.

## Web UI cohesion

Sources live in `internal/web/src/`; `internal/web/index.html` is generated and
committed. Repetitive watch-panel metadata belongs in
`internal/web/src/watch-panels.json`; executable behavior stays in the existing
JavaScript owners. Run `make web` after any source edit.

Rendering uses lit-html templates and full-list reconciliation. Compose nested
templates, let bindings escape values, and keep interaction in the delegated
`data-*` click path rather than inline or lit event handlers. String-built SVG
may write only to its dedicated container. Preserve the server's existing CSP
contract.

Before adding a visual, loader, formatter or control, find the existing
presentation of that concept and reuse it whole. Use the established panel,
responsive-table and design-token patterns; do not introduce literal CSS colors
or horizontal page overflow. Web changes must pass the existing desktop/mobile
and WCAG 2.2 AA checks. Detailed UI contracts live in
[docs/webui-representation.md](docs/webui-representation.md).

## Wizard option selection

All assistants follow [docs/wizards.md](docs/wizards.md). Use shared `Prompt`
helpers, detected targets and the canonical monitor/interval flow; never invent
a separate prompt parser or ask for a target name that detection can provide.

Preserve the shared `all` / `none` / `default` notifier vocabulary. `none` and
`default` remain selectable with no configured notifier, and an empty inherited
default degrades to monitor-only. Preview and confirm generated files, and offer
cleanup only for targets proven absent by detection.

## Catalog: instanced systemd services

Reuse version/instance materialization instead of asking operators for ad-hoc
variables. Use `${hostname}` for a host-keyed instance and `%n` / `${n}` for
discovered numeric instances. Catalog templates materialize definitions; the
operator still enables concrete services. Keep the built-in variable and
template rules in [docs/services.md](docs/services.md) current.

## Go quality gates

Write idiomatic Go that passes the configured gate without new suppressions.
`make check` is the finish command, `make lint` is the focused analyzer command,
and `.golangci.yml` is the complete linter and exclusion source of truth. Do not
copy its roll-call or current thresholds into prose.

Use `.custom-gcl.yml` for the custom golangci-lint build and `.semgrep/` for
repository call-boundary rules. How to add a rule, including the required
positive and negative fixtures, is in [.semgrep/README.md](.semgrep/README.md).
A necessary `//nolint` names the exact analyzer and explains the
design reason; never weaken a gate or broaden an exclusion to land a change.

The `Makefile` owns YAML, Markdown, script, dependency, web, vulnerability and
coverage phases. Fix findings at their source; do not replace the gate with a
handwritten subset.

## Testing

Match the owning package's existing test style. Prefer table-driven subtests,
existing fakes, injectable seams and temporary directories. Tests must not
operate real services, signal host processes or depend on ambient `/etc`,
`/proc`, network or init state.

Cover the success, invalid/unsafe input, blocked and timeout/error paths relevant
to the change. Preserve distinctions such as nil versus empty values when they
are part of the contract. Use targeted tests during development and the workflow
finish gate before reporting completion.

## Security and safety invariants

The detailed policy and operator behavior live in
[docs/safety.md](docs/safety.md). Every change must preserve these hard
boundaries:

- service actions use `internal/operation`, an explicit timeout, operation
  locks, guards and required preflight;
- automatic remediation uses the same path, requires a positive resolved
  cooldown and never triggers from a system-scoped metric;
- process authorization never uses name, basename, argv or cmdline alone;
  signalable processes require exact resolved `exe` and `user` identity;
- `SIGKILL` requires explicit `force_kill` plus restrictive `kill_only_if`;
- unmatched residuals are reported as orphans and block a following start;
- conditions are read-only and mutation belongs to actions;
- named locks and operation locks remain separate, atomically acquired,
  TTL-bounded and auditable when reclaimed;
- each executed or blocked action records one auditable outcome;
- one slow service must not block monitoring of another, while shared check
  concurrency remains bounded.

Database catalog profiles stay conservative. No config option may disable a
hard invariant.

## graphify

`graphify-out/` is a local, gitignored knowledge graph. For codebase questions,
query an existing `graphify-out/graph.json` before broad source traversal. Use
`graphify query`, `graphify path` or `graphify explain` as appropriate and verify
important findings in the owning files.

Update or rebuild the graph only when it is absent or stale for the task. Never
stage generated graph output, and do not run an update after a change unless
subsequent work needs a current graph.
