# Configuration

Sermo has two document kinds: **profiles** (reusable base definitions) and
**services** (concrete monitored instances). Both are YAML and share the same
sections.

## Layout

```
/etc/sermo/sermo.yml              global config
/usr/share/sermo/profiles/*.yml   packaged profiles
/etc/sermo/apps-available/*.yml   user profiles
/etc/sermo/apps-enabled/*.yml     enabled services
```

The directories Sermo reads come from `paths` in the global config:

```yaml
paths:
  profiles:
    - /usr/share/sermo/profiles
    - /etc/sermo/apps-available
  enabled:
    - /etc/sermo/apps-enabled
  runtime: /run/sermo
  state: /var/lib/sermo
```

`paths.runtime` is the root for named runtime locks (`<runtime>/locks`) and
internal operation locks (`<runtime>/ops`). It lives on tmpfs and is wiped on
reboot. `paths.locks` is **not** supported.

`paths.state` (default `/var/lib/sermo`) is the root for the persistent state
database `sermo.db` (SQLite). Unlike `paths.runtime`, it survives reboots, which
is what lets a service's `monitor: previous` flag restore its last monitoring
state. The schema is versioned and migrated forward automatically, so future
features can add tables without a manual upgrade.

## Engine settings

The `engine` block is daemon-wide configuration consumed by `sermod`; it never
merges into a service:

```yaml
engine:
  backend: auto          # auto | systemd | openrc
  interval: 30s          # base cycle interval per worker
  max_parallel_checks: 8 # bound on concurrent checks
  default_timeout: 10s   # default per-check timeout
  startup_delay: 0       # grace period before the first cycle (0 disables)
```

`engine.startup_delay` is a non-negative duration that holds the daemon before
it starts its first check cycle, giving the host time to finish booting so
services that are still coming up are not flagged or remediated prematurely. The
wait applies once, on startup, before any worker runs; a shutdown signal during
the wait aborts cleanly without starting any worker. The default `0` disables it.

## Global defaults

Only the per-service parts of `defaults` merge into a service: `stop_policy`,
`policy`, and `rule_window`. Engine-wide settings (`interval`,
`max_parallel_checks`, `default_timeout`, `startup_delay`, `backend`) are daemon
configuration and never merge into a service.

`defaults.policy.cooldown` is **required and positive**: every resolved service
inherits a loop-prevention cooldown unless it overrides it.

## Resolution order

A service is resolved into a flat definition, lowest precedence first:

1. The effective global `defaults` (per-service parts).
2. The `uses` profile, or the `clone` chain, merged on top.
3. The service's own fields (highest precedence).
4. `${var}` expansion, once, over the merged result.
5. Validation of the flattened service.

```
global defaults  <  profile (uses) or clone source  <  service overrides
```

`uses` and `clone` are taken **unexpanded**, so a clone can override a single
variable and have every `${var}` reference resolve to the new value.

## Merge rules

- Scalars and lists overwrite.
- Maps merge recursively.
- Named sections (`checks`, `preflight`, `postflight`, `processes`, `rules`)
  are maps keyed by name, so a child can override one field of one entry.
- Disable an inherited entry with `enabled: false`; delete it with
  `delete: true`.

## Variables

```yaml
variables:
  host: 127.0.0.1
  port: 8080
checks:
  http:
    type: http
    url: "http://${host}:${port}/health"
```

- Variables are flat literal strings; a value must not itself contain `${...}`.
- Expansion is a single pass: any `${...}` left afterward is an undefined
  variable and a validation error.
- Numeric fields (`port`, `expect_status`) accept an int, a quoted string, or a
  `${var}`, and are parsed after expansion.

## Validating

```sh
sermoctl --config /etc/sermo/sermo.yml config validate          # all services
sermoctl --config /etc/sermo/sermo.yml config validate mysql    # one service
sermoctl --config /etc/sermo/sermo.yml config render mysql-main # resolved form
```

`config validate` exits `78` on a configuration error. See
[rules](rules.md) for what each section may contain.
