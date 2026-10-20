---
name: sermo-config-schema
description: Use when designing, editing, validating, merging, rendering, or reviewing Sermo YAML configuration, catalog services/apps/libs/patterns, services, watches, mounts, clones, variables, version templates, checks, guards, locks, rules, or stop policies.
---

Configuration schema for Sermo. The public schema is in
`docs/configuration.md`, `docs/services.md` and `docs/rules.md`;
`docs/sermo-all.yml` validates every documented example. Do not restate those
here. This skill records the design decisions a change must respect.

## Design decisions

- YAML is written by sysadmins: readable, deterministic and renderable to one
  flat resolved config. The daemon consumes resolved targets only.
- A document's kind comes from its location (catalog subdirectory,
  `paths.services`, `paths.watches`); `kind:` is redundant and must match when
  present in a `paths.*` document; catalog documents ignore it. One document of one kind per file. A notifier fragment holds exactly
  one entry under `notifiers:`. `docs/sermo-all.yml` is the only bundle.
- Mergeable sections (`watches`, `checks`, `preflight`, `processes`, `rules`)
  are maps keyed by name, never lists, so an override can disable or delete
  one inherited item. Scalars override, maps merge recursively, arrays replace.
- Precedence for services: global `defaults` < catalog service (`uses`) or
  `clone` source < service overrides. Only target-safe defaults merge in
  (including generated-sensor permissions); engine settings never do.
- `clone` and `uses` copy the source unexpanded. Variables expand once, after
  all merging; a variable whose value contains `${...}` is rejected; an
  unresolved variable fails validation.
- Typed fields (`port`, `expect_status`, durations, percentages) are parsed
  after expansion through `internal/cfgval`, so a quoted or `${var}` value is
  never a YAML error.
- `${arch}`, `${os}` and the `os:` selector resolve at load time; `${name}`,
  `${display_name}`, `${service}`, `${host}` at resolution; `${date}`,
  `${event}`, `${action}` only inside rule messages at runtime.
- Version templates (`%v`, `%n`, `%s`, `%i` → `${version}`, `${n}`, `${sep}`,
  `${instance}`; `versions.from`, `versions.current_from`, `versions.require`,
  `versions.unversioned`) and `${hostname}` instance units materialize catalog
  documents and are then dropped. The operator still enables the concrete
  services. The full contract is in `docs/services.md`; keep it current when
  the resolver changes.
- `restart_on_change.libraries` and other catalog sugar desugar at resolution
  into the canonical runtime tree; the sugar never reaches the daemon.
- `engine.runtime` is the single runtime root. `paths.locks` and
  `/etc/sermo/locks.d` are not supported.
- Classified watch directories (`watches/`, `networks/`, `storages/`,
  `mounts/`) are all listed under `paths.watches`; each `.local` sibling is
  the per-host override. Source-tree validation uses
  `SERMO_DATADIR=$PWD make build` and `examples/sermo-dev.yml`.
- Dangerous behavior needs explicit configuration, and no option may disable a
  hard safety invariant (required preflight, guards, named and operation locks,
  the SIGKILL gate — `force_kill: true` plus `kill_only_if`, or kernel-proven
  unit-cgroup membership under `true`/`auto`; `false` never SIGKILLs — and the
  exact exe+user kill identity).
- Validation errors name the target (and the file for load/document errors or a
  `.local` override), the field path and the reason.
- There is no resolved-config rendering subcommand; do not document one.
- `severity` has five levels (`internal/severity` owns them). `levels:` lives
  in the check entry — a watch's `check:`, a service `checks:` entry, or a
  multi-metric type's metric block — never on a watch entry. Its shape errors
  are validation errors; an inert tier (not stricter, not above the declared
  severity) is ignored and surfaces through `config.Warnings`, not `Validate`.
  A service watch's entry `severity:` is copied into the generated check.

## Traps

- Adding a check, option or behavior to services but not host watches, or the
  reverse, without a documented owner-level limitation.
- Keeping a retired field name alive through fixtures or compatibility parsing
  without an explicit request.
- Reading a numeric field before expansion, or comparing metric values without
  honoring the `%` suffix.
- A `scope: system` metric driving anything but `alert`.
- A `file_exists` check pointing under `<engine.runtime>/locks`.
- A version template whose `%v` token also appears in the body; the body uses
  `${version}`.

When reviewing a config change, state the resolved meaning, the merge
behavior, the safety impact and the validation cases that must exist.
