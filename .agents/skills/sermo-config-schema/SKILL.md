---
name: sermo-config-schema
description: Use when designing, editing, validating, merging, rendering, or reviewing Sermo YAML configuration, catalog services/apps/libs/patterns, services, mounts, clones, variables, checks, guards, locks, rules, or stop policies.
---

You are the Sermo configuration schema designer.

## Core principles

1. YAML must be readable by sysadmins.
2. YAML must be deterministic and renderable to a flat final config.
3. Mergeable sections must be maps keyed by name, not lists.
4. Overrides must be explicit.
5. Dangerous behavior must require explicit configuration.
6. Validation errors must identify the file, service, field and reason.
7. The daemon should only consume resolved services.

## Document kinds

A document's kind is determined by where it is read from, so files do not carry a
`kind:` field — it is derived from the location:

- catalog subdirectory: `catalog/services/` → service, `catalog/apps/` → app,
  `catalog/libs/` → lib, `catalog/patterns/` → patterns;
- deployed config dirs: `paths.services` → service, `paths.mounts` → mount.

A `kind:` key is optional and redundant; if one is present in a deployed file it
must match the location, otherwise loading fails. Examples (one per file):

```yaml
# catalog/services/apache.yml  → service
name: apache
```

```yaml
# catalog/apps/openssl.yml  → app
name: openssl
```

```yaml
# catalog/libs/glibc.yml  → lib
name: glibc
```

```yaml
# <paths.services>/apache-main.yml  → service
name: apache-main
uses: apache
```

```yaml
# <paths.services>/redis-cache.yml  → service
name: redis-cache
clone: redis-main
```

```yaml
# <paths.mounts>/mount-backup.yml  → mount
name: mount-backup
path: /mnt/backup
```

Every document has a `name`. Optional human-facing metadata may
accompany them:

```yaml
name: mariadb
display_name: "MariaDB"   # optional pretty label
description: "..."        # optional free-text note
category: "database"      # optional WebUI grouping/filter label
```

- `display_name` is the label shown to humans in catalog inventory
  (`sermoctl services` / `apps` / `libs`) and the Web UI service/application
  lists.
  When absent or blank it falls back to `name`. Omit it when it would just repeat
  `name`.
- `description` is optional free text with NO fallback: when absent, nothing is
  shown — never substitute `name`.
- `category` groups/filters Services and Apps in the WebUI; when absent,
  services fall back to `service` and apps to `app`. All three must be strings
  if present.

## File granularity

Use one YAML file per target — a single document of one kind per file, never
several grouped together. A document's kind is derived from where it lives
(catalog subdir / `paths.services` / `paths.mounts`), so a top-level `kind:` is
optional. Watch and notifier fragment files still use a top-level `watches:` or
`notifiers:` map, but the map must contain exactly one named entry.
`docs/sermo-all.yml` is the only reference-style exception: it groups examples so
the schema can be validated in one place.

`clone` copies the source service in UNEXPANDED form (its fields and `variables`,
with `${...}` still literal), so overriding a single variable in the clone changes
what `${var}` resolves to after expansion. Same for `uses` with a catalog service. See
`docs/configuration.md`.

## Version templates

A catalog service or app whose name contains `%v` (free-form version) or `%n`
(plain integer) is a version template: it materializes into one concrete document per installed
version when several can coexist (php-fpm, postgres, tomcat, beam, db, python).
`%v` pairs with `${version}` and accepts `8.3`/`12.0.2`; `%n` pairs with `${n}`
and matches only whole integers (`python%n` → `python2`, `python3`, not
`python3.11`). The placeholder may sit anywhere in the name (`db%vsql` →
`db4.8sql`). Put the matching `${...}` in `variables.binary`, or in
`versions.from` when an app/lib discovery source is not the runtime executable.
For `catalog/services` service templates, put the tokens in `service:` and let
active systemd/OpenRC units drive service materialization; linked apps keep owning
binary discovery and validation.
`versions.from` may be a backend-neutral string/list, or a map with `systemd`
and `openrc` branches. Map branches are exclusive: Sermo selects only the active
init branch from `engine.backend` or `SERMO_BACKEND`, falling back to detected
`${init}`. On load Sermo globs those paths with `${version}` wildcarded, extracts
each installed version, and registers `name` with `%v`→version and every
`${version}` substituted in the body. For app and library templates that discover
from `versions.from` and do not declare
`variables.binary`, the materialized document binds `${binary}` to the path that
matched. Matches are de-duplicated by materialized token tuple. The template is
then dropped and yields nothing when no version is installed. Keep
exactly one descriptive file per template, but the YAML filename does not have
to match `name:`. `%v` is substituted only in the name; inside the body always
use `${version}` (`variables.binary`, `display_name`, service candidates,
commands, etc.).
When the runtime executable is version-agnostic in an app/lib, point discovery at
a version-specific path with `versions.from` (discovery-only; stripped from the
materialized document). A service template may also `uses` a base catalog service to inherit
checks/processes/rules and override only `variables.binary`. Simple `%v`/`%n`
templates may materialize an unversioned active-slot entry by default when the
marker-less path exists; composite templates with `%i`, `%s` or more than one
token do not infer that entry from `versions.from`, but can declare
`versions.current_from` to materialize the active-slot base name explicitly.
`current_from` accepts a path string or a list of path strings.
Materialized names must not collide with explicit documents in the same catalog
category; validation reports those collisions.

```yaml
# catalog/apps/postgres.yml  → app
name: postgres-%v
display_name: "PostgreSQL ${version}"
variables:
  binary: "/usr/lib64/postgresql-${version}/bin/postgres"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }
```

## Categories and library restarts

Catalog documents are categorized by the subdirectory under a catalog root:
`services/`, `apps/`, `libs/`, `patterns/`. Files at the catalog root are
rejected. Loading recurses; the directory sets both the document's kind (so
`services/` → service) and `Document.Category`. `apps` and
`libs` are minimal catalog documents (name, display_name, description,
`variables.binary` and preflight/version entries) surfaced by `sermoctl apps` /
`libs`.

A `library` catalog document names a shared library and the file to watch
(`variables.binary`, e.g. `/lib64/libc.so.6`) and verifies it with
`preflight.file`. Unlike app/service executable variables, library paths are
watched files. A service opts into library-change restarts with:

```yaml
restart_on_change:
  libraries: [glibc, pam]
```

This desugars at resolution into one remediation rule per library:
`if: { changed: { library: X, path: <lib file> } } then: { action: restart }`.
The `changed` condition (true when the file's size/mtime differs from the
across-cycle baseline; first cycle adopts, successful restart re-baselines) is the
primitive; referenced names must be `library` catalog documents.

## Merge rules

Use these rules:

```text
scalars: override
maps: recursive merge
arrays: replace unless documented otherwise
checks/preflight/postflight/processes/rules: maps keyed by name
enabled: false disables inherited item
delete: true removes inherited item
```

Precedence, low to high: `global defaults < catalog service (uses)/clone source < service
overrides`. The global `defaults` (stop_policy, policy, rule_window) is the base
layer of every service; engine settings (interval, max_parallel_checks,
default_timeout, backend) are NOT merged into services. Variables expand once,
after all merging. See `docs/configuration.md`.
The effective `defaults.policy.cooldown` is required and must be positive. A
catalog service or service may omit `policy.cooldown` only when it inherits that value;
any explicit override must also be positive.
`paths.runtime` is the single runtime root. Named runtime locks are derived from
`<paths.runtime>/locks`, and operation locks from `<paths.runtime>/ops`.
`paths.locks` and `/etc/sermo/locks.d` are not supported.

Prefer this:

```yaml
checks:
  http:
    type: http
    url: http://127.0.0.1/health
```

Avoid this for mergeable items:

```yaml
checks:
  - name: http
    type: http
```

## Variables

Support simple variable expansion. The TCP check takes separate `host` and
`port` fields (there is no `address` field):

```yaml
variables:
  host: 127.0.0.1
  port: 6379

checks:
  tcp:
    type: tcp
    host: "${host}"
    port: "${port}"
```

Do not introduce complex templating unless requested.

`${name}` and `${display_name}` are built-in variables always available during
resolution (no `variables` entry needed): `${name}` is the resolved service name,
`${display_name}` is the display name (falling back to `name`). Use them to
parameterize human-facing strings, e.g. `message: "${display_name} backup is
running"`. An explicit `variables` entry of the same name overrides the built-in.

`${current}` is only a version-template materialization marker for catalog
templates. It is replaced before normal resolution, not exposed as a runtime
variable: `current` for the versioned entry whose binary is the same filesystem
entry as the active-slot binary, whether that slot was inferred from the
marker-less path or declared with `versions.current_from`, or empty otherwise.
Use it in metadata such as
`display_name: "PHP ${version} ${current}"`; metadata is trimmed after
substitution.

`${arch}` is the machine architecture (uname -m: `x86_64`, `aarch64`, ...),
substituted everywhere on load — including inside variable values and
version-discovery paths — so it works in `binary`, library paths and
`versions.from`, e.g. `binary: /usr/bin/qemu-system-${arch}`. `${os}` is the
os-release ID (`gentoo`, `debian`, ...), substituted the same way. Both honor
`SERMO_ARCH` / `SERMO_OS` env overrides.

Other built-ins: `${service}` (backend unit name) and `${host}` (hostname,
`SERMO_HOST` override; only applies when no `host` variable is defined) resolve at
resolution time. `${date}` (RFC3339 timestamp), `${event}` (firing rule name) and
`${action}` (restart/start/stop/reload/resume) are RUNTIME values substituted by
the worker when it emits a rule message — use them in `message:` strings;
elsewhere they stay literal.

An `os:` key anywhere (value = map of os-id -> block) is an OS SELECTOR: at load,
the branch for the detected OS (or a `default` branch) is merged into the parent
and the rest discarded. It works at any depth — service candidates, checks,
processes, policy, variables — and is the structural counterpart to the `${os}`
string.

Validation must fail on unresolved variables.

## Typed fields and variable interaction

Variables are always strings, but some fields are logically numeric or have a
small grammar. Use a tolerant scalar (`FlexInt`) for these so YAML never fails
just because a value was quoted or carried a `${var}`; parse after expansion. See
`docs/configuration.md`.

```text
port           int, quoted string or ${var}; resolves to an int in 1..65535.
expect_status  int, quoted string or ${var}; resolves to an int (HTTP status).
timeout        duration string such as "3s".
metric value   string with an optional trailing "%" (see Metrics below).
```

These are all valid and equivalent: `port: 783`, `port: "783"`, `port: "${port}"`.

## Metrics

Metric checks and metric conditions carry a `scope`:

```yaml
checks:
  memory:
    type: metric
    scope: service        # service (default) | system
    name: memory
    op: ">"
    value: 40%
```

```text
scope: service  measures only this service (its process set / cgroup). Default.
scope: system   measures the whole machine (total_memory, total_cpu, load*).
value           "%" suffix = percentage 0..100; otherwise an absolute number.
```

Safety: a remediation rule may only trigger on a `scope: service` metric. A
`scope: system` metric may drive `alert` only — never an operation action
(`restart`/`start`/`stop`/`reload`/`resume`) for a single service. See
`docs/rules.md`.

## Required validation

Validate:

```text
duplicate service names
materialized version-template names that collide with explicit documents
missing catalog services in uses
apps entries that reference unknown app catalog documents
missing service targets in clone
clone cycles
unknown check types
unknown rule condition types (and/or/not/failed/active/metric/service/process/file/command/changed)
changed condition requires a path; restart_on_change references must be library catalog documents
unknown actions
missing variables
nested variable (a variable value containing ${...}) — rejected
invalid durations
invalid percentages
invalid units
security toggles that disable hard safety invariants (preflight, locks, SIGKILL default, kill selector) — rejected
force_kill without kill_only_if
kill_only_if without both exe_any and users
SIGKILL without explicit permission
rules with both for and within if unsupported
guards without blocks
display_name, description and category, if present, are strings (all optional; display_name falls back to name, description has no fallback, category groups WebUI services/apps)
service must be scalar `service: <unit>` or a per-init map
service expect/state in {active, inactive, failed, unknown}; process state in {running, zombie, absent}
metric scope is service or system, and name exists in that scope's catalog
scope: system metric used in a remediation rule (must be alert only)
typed fields (port, expect_status) parse to their target type after expansion
paths.runtime, if set, is an absolute directory
paths.locks rejected; locks/ops directories derive from paths.runtime
/etc/sermo/locks.d not scanned for active locks
defaults.policy.cooldown present and positive
resolved service policy.cooldown present and positive; catalog service/service omissions are allowed only when inherited
policy.max_actions requires max_actions_window
block/alert actions require a message
postflight entries use the same schema as preflight/checks; optional is boolean
file_exists checks do not point under <paths.runtime>/locks; Sermo named runtime locks are checked by the engine
```

## Resolved config

There is no CLI command that renders the final resolved service config today.
Use `sermoctl status SERVICE`, `sermoctl config validate`, the Web UI/API, or
YAML examples that match the current public commands. Do not document or
reintroduce a resolved-config rendering subcommand under `sermoctl config`.

## Output format

When reviewing schema or config, return:

```text
- Valid / invalid
- Merge behavior
- Final resolved meaning
- Safety concerns
- Suggested changes
- Tests to add
```
