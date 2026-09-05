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
  present. One document of one kind per file. A notifier fragment holds exactly
  one entry under `notifiers:`. `docs/sermo-all.yml` is the only bundle.
- Mergeable sections (`watches`, `checks`, `preflight`, `processes`, `rules`)
  are maps keyed by name, never lists, so an override can disable or delete
  one inherited item. Scalars override, maps merge recursively, arrays replace.
- Precedence for services: global `defaults` < catalog service (`uses`) or
  `clone` source < service overrides. Only target-safe defaults merge in
  (`dry_run`, `stop_policy`, `policy`, `rule_window`); engine settings never do.
- `clone` and `uses` copy the source unexpanded. Variables expand once, after
  all merging; a variable whose value contains `${...}` is rejected; an
  unresolved variable fails validation.
- Typed fields (`port`, `expect_status`, durations, percentages) are parsed
  after expansion through `internal/cfgval`, so a quoted or `${var}` value is
  never a YAML error.
- `${arch}`, `${os}` and the `os:` selector resolve at load time; `${name}`,
  `${display_name}`, `${service}`, `${host}` at resolution; `${date}`,
  `${event}`, `${action}` only inside rule messages at runtime.
- Version templates (`%v`, `%n`, `versions.from`, `versions.current_from`)
  and instance tokens (`${hostname}`, `%n` / `${n}`) materialize catalog
  documents and are then dropped. The operator still enables the concrete
  services. The full contract is in `docs/services.md`; keep it current when
  the resolver changes.
- `restart_on_change.libraries` and other catalog sugar desugar at resolution
  into the canonical runtime tree; the sugar never reaches the daemon.
- `paths.runtime` is the single runtime root. `paths.locks` and
  `/etc/sermo/locks.d` are not supported.
- Classified watch directories (`watches/`, `networks/`, `storages/`,
  `mounts/`) are all listed under `paths.watches`; each `.local` sibling is
  the per-host override. Source-tree validation uses
  `SERMO_DATADIR=$PWD make build` and `examples/sermo-dev.yml`.
- Dangerous behavior needs explicit configuration, and no option may disable a
  hard safety invariant (preflight, locks, SIGKILL default, kill selector).
- Validation errors name the file, target, field and reason.
- There is no resolved-config rendering subcommand; do not document one.

## Traps

- Adding a check, option or behavior to services but not host watches, or the
  reverse, without a documented owner-level limitation.
- Keeping a retired field name alive through fixtures or compatibility parsing
  without an explicit request.
- Reading a numeric field before expansion, or comparing metric values without
  honoring the `%` suffix.
- A `scope: system` metric driving anything but `alert`.
- A `file_exists` check pointing under `<paths.runtime>/locks`.
- A version template whose `%v` token also appears in the body; the body uses
  `${version}`.

When reviewing a config change, state the resolved meaning, the merge
behavior, the safety impact and the validation cases that must exist.
