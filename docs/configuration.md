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

## Host watches

`watches` monitor host-level resources independently of any service and run a
**hook** (a local command) when a threshold is crossed. They are daemon
configuration; they never merge into a service.

```yaml
watches:
  disk-root:
    enabled: true          # optional, default true
    interval: 1m           # optional, default engine.interval
    check:
      type: disk
      path: /
      used_pct: { op: ">=", value: 90 }   # check fires when crossed
    for: { cycles: 3 }     # optional window; reuses the rules engine
    then:
      hook:
        command: [/usr/local/bin/alert-disk.sh, "/"]
        timeout: 10s       # optional, default engine.default_timeout
```

The `disk` check reads filesystem usage for `path` and is true when every
present predicate (`used_pct` and/or `free_pct`, each `{op, value}` with
`op ∈ >=,>,<=,<,==,!=`) holds. When the condition holds for the `for`/`within`
window, the hook command runs (argv only, never a shell) with these environment
variables: `SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_PATH`, `SERMO_VALUE`,
`SERMO_MESSAGE`.

### `net` — network interface

A `net` watch monitors one interface, grouped under a single entry that names the
interface once and lists the metrics it cares about. Each metric is independent:
it has its own condition **and its own hook**. Internally the entry expands into
one watch per metric, so the metrics never share state and fire (and remediate)
separately.

```yaml
watches:
  net-eth0:
    enabled: false
    interval: 30s
    check: { type: net, interface: eth0 }
    metrics:
      state:                       # interface up/down
        on: change                 # fire on any state change; or `expect: up|down`
        then:
          hook:
            command: [/usr/local/bin/sermo-net-state.sh, eth0]
      speed:                       # link speed (Mbps)
        on: change                 # speed only supports change detection
        then:
          hook:
            command: [/usr/local/bin/sermo-net-speed.sh, eth0]
      errors:                      # rx/tx error counters
        counters: [rx_errors, tx_errors]   # optional, this is the default
        delta: { op: ">", value: 100 }     # fire when the per-cycle delta crosses
        then:
          hook:
            command: [/usr/local/bin/sermo-net-errors.sh, eth0]
```

The three metrics and their conditions:

- **`state`** — interface up/down. Use `on: change` to fire on any transition, or
  `expect: up` / `expect: down` to fire whenever the state is *not* the expected
  value.
- **`speed`** — link speed in Mbps. Supports `on: change` only; it primes a
  baseline on the first cycle and fires when the speed differs afterwards.
- **`errors`** — sums the named `counters` (default `rx_errors`, `tx_errors`) and
  fires when the per-cycle **delta** satisfies `delta: {op, value}` (same operator
  set as disk). The first cycle primes a baseline; counter resets clamp the delta
  to zero (never fire on a reset).

Change/delta metrics are **stateful across cycles**: the first cycle establishes a
baseline and does not fire.

A net hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_INTERFACE`,
`SERMO_METRIC`, `SERMO_VALUE`, `SERMO_MESSAGE`, and — for the change metrics
(`state`, `speed`) — `SERMO_OLD` and `SERMO_NEW`.

In general, **every key a check puts in its result `Data` is exported to the hook
as `SERMO_<UPPER_KEY>`** (non-alphanumeric characters become `_`). The lists above
are simply the keys each built-in check emits.

Other resource types (file counts, …) will be added as new check `type` values
using the same watch/hook structure.

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
