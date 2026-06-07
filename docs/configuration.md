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
  interval: 30s          # default cycle interval; per-service overridable
  max_parallel_checks: 8 # bound on concurrent checks
  default_timeout: 10s   # default per-check timeout
  startup_delay: 0       # grace period before the first cycle (0 disables)
```

`engine.interval` is the default cadence at which every service's checks are
run. Each service runs all of its checks once per cycle.

`engine.startup_delay` is a non-negative duration that holds the daemon before
it starts its first check cycle, giving the host time to finish booting so
services that are still coming up are not flagged or remediated prematurely. The
wait applies once, on startup, before any worker runs; a shutdown signal during
the wait aborts cleanly without starting any worker. The default `0` disables it.

### Per-service interval

`engine.interval` sets the default for every service. A service may override it
with its own top-level `interval`, so cheap services can be checked often and
expensive ones rarely without changing the global default:

```yaml
kind: service
name: nginx
interval: 10s            # optional, default engine.interval; positive duration
checks:
  http: { type: http, url: "http://127.0.0.1/health", expect_status: 200 }
```

The override governs the whole worker cycle for that service (its checks, rules
and remediation), exactly like the global interval — only its cadence differs.
Window counts (`for`/`within`, measured in cycles) are therefore counted in that
service's own cycles. Worker starts are still spread across one global interval
so a fleet of services does not all probe on the same tick.

## Availability (SLA)

The daemon records one availability sample per monitoring cycle per service, so
you can see how often each service has been healthy over time. No configuration
is needed — it is on for every monitored service.

A service is **available** in a cycle when none of its **required** checks
failed. Optional checks (warnings) do not affect it, and a service with no
required checks is always available. Samples are accumulated into per-minute
buckets in the state DB (`/var/lib/sermo/sermo.db`); the daemon prunes buckets
older than a year on startup.

Only **observed** cycles count, so these periods are **excluded** from the SLA
rather than counted as downtime:

- **Sermo itself is stopped** — no cycles run, so those minutes have no samples.
- **The service is paused** (`unmonitor`, or `monitor: disabled`) — the cycle
  returns before any check, recording nothing.
- **The service is disabled** (`enabled: false`) — no worker is built for it.
- **A check is disabled/removed** — it is absent from the cycle, so it neither
  passes nor fails; availability reflects only the checks that actually ran.

So maintenance windows and outages of Sermo itself never depress a service's SLA.

Report availability over rolling windows (the last hour, day, week, month and
year) with `sermoctl sla`:

```sh
sermoctl sla                 # every configured service
sermoctl sla apache-main     # one service
sermoctl --json sla          # machine-readable: up/total/ratio per window
```

A window with no samples reads `n/a` (availability unknown), not `0%`.

### Availability time series

Samples are kept as per-minute buckets, which is also the raw **time series** a
graph is built from. Export one service's series (oldest first) with `--series`:

```sh
sermoctl sla --series apache-main                  # last 24h (default)
sermoctl sla --series apache-main --since 168h     # last 7 days
sermoctl --json sla --series apache-main           # points: start, up, total, ratio
```

Each point is one monitored minute; **unmonitored minutes are simply absent**
(gaps), so a graph can render an excluded period (Sermo down, or the service
paused/disabled) distinctly from real downtime.

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
  `expect: up` / `expect: down` to fire whenever the state **is** the expected
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

### `icmp` — external host (ping)

An `icmp` watch monitors an **external host** by ICMP echo (ping): its
reachability and its round-trip latency. Like `net` it is grouped per host — the
host is named once and each metric is independent, with its own condition **and
its own hook**. The entry expands into one watch per metric, so the metrics never
share state and fire separately.

```yaml
watches:
  ping-gw:
    enabled: false
    interval: 30s
    check: { type: icmp, host: 8.8.8.8, count: 3 }   # count optional, default 3
    metrics:
      state:                       # reachable / unreachable
        on: change                 # fire on any transition; or `expect: up|down`
        then:
          hook:
            command: [/usr/local/bin/sermo-host-state.sh, "8.8.8.8"]
      latency:                     # round-trip time (ms)
        threshold: { op: ">", value: 100 }   # fire when rtt crosses the threshold
        then:
          hook:
            command: [/usr/local/bin/sermo-host-latency.sh, "8.8.8.8"]
```

The two metrics and their conditions:

- **`state`** — host reachable (`up`) or unreachable (`down`). Use `on: change`
  to fire on any transition, or `expect: up` / `expect: down` to fire whenever the
  state **is** the expected value.
- **`latency`** — round-trip time in milliseconds. Use either
  `threshold: {op, value}` (same operator set as disk) to fire when the RTT
  crosses a fixed bound, **or** `change: {delta}` to fire on an abrupt jump
  (`|rtt − rtt_prev| > delta`); set exactly one. Latency conditions only apply
  while the host is reachable; an unreachable cycle never fires latency and never
  updates the change baseline (so the baseline is the last *reachable* RTT).

The change-based metrics (`state` with `on: change`, `latency` with `change`) are
**stateful across cycles**: the first cycle establishes a baseline and does not
fire.

An icmp hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_HOST`,
`SERMO_METRIC`, `SERMO_VALUE`, `SERMO_MESSAGE`, and — for the change metrics —
`SERMO_OLD` and `SERMO_NEW`.

ICMP requires elevated privileges: the daemon needs the `CAP_NET_RAW` capability
(or the host's `net.ipv4.ping_group_range` sysctl must include the daemon's gid)
to open a raw ICMP socket. This iteration is **IPv4-only**.

### `file` — file/directory attributes

A `file` watch monitors a file or directory for attribute changes — size,
permissions, owner, and deletion — and runs the entry's hook **once per change**.
It is stateful: it remembers each path's attributes across cycles and reports only
transitions, adopting the baseline silently on the first cycle (a daemon start
never fires). With `recursive: true` it watches the whole subtree, so a hook fires
per changed entry.

```yaml
watches:
  app-data:
    enabled: false
    interval: 30s
    check:
      type: file
      path: /var/lib/myapp            # file or directory
      recursive: true                 # optional, default false (whole subtree)
      size: { op: ">", value: 1048576 }   # edge threshold; or `size: { on: change }`
      permissions: { on: change }     # mode bits (perm + setuid/setgid/sticky)
      owner: { on: change }           # owning uid/gid
      existence: { on: delete }       # a previously-seen path is gone
    then:
      hook:
        command: [/usr/local/bin/sermo-file-change.sh]
        timeout: 10s
```

The conditions (declare at least one):

- **`size`** — either `{ on: change }` (fire whenever the byte size differs from
  the last cycle) or a threshold `{op, value}` (same operator set as disk). The
  threshold is **edge-triggered**: it fires once when the size crosses into the
  condition and re-arms only after it drops back out — not every cycle while
  breached.
- **`permissions`** — `on: change`; fires when the permission bits change.
- **`owner`** — `on: change`; fires when the owning uid or gid changes.
- **`existence`** — `on: delete`; fires when a path that existed stops existing
  (re-creation is then adopted silently). Deletion is the only transition reported.

When `recursive: true` and the path is a directory, every entry in the subtree is
tracked independently (symlinks are watched as links, never followed). New entries
are adopted silently; deleted entries fire `existence` if configured. Each detected
change is **one event and one hook run**, so a cycle that finds several changes
fires several times.

A file hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE` (`file`), `SERMO_PATH` (the
changed path), `SERMO_CHANGE` (`size` | `size_threshold` | `permissions` | `owner`
| `deleted`), `SERMO_MESSAGE`, and, depending on the change, `SERMO_OLD`/`SERMO_NEW`
(old/new value) plus `SERMO_SIZE`, `SERMO_OP`, `SERMO_VALUE` for size conditions.

### `process` — process by name

A `process` watch tracks the processes whose **name** matches (the resolved exe
basename or its full path), optionally filtered by owning `user`, and fires the
hook **once per matching PID** when that process has been alive at least `for`
and/or its CPU/memory/IO crosses a threshold. (This is the daemon's host watch; it
is distinct from the per-service `process` check, which reports running/zombie/
absent state.)

```yaml
watches:
  hot-workers:
    enabled: false
    interval: 30s
    check:
      type: process
      name: myworker                  # exe basename (e.g. myworker) or full path
      user: www-data                  # optional: also match the owning user
      for: 5m                         # optional: observed alive at least this long
      cpu: { op: ">", value: 80 }     # optional: CPU % (rate)
      memory: { op: ">", value: 524288000 }   # optional: RSS bytes
      io: { op: ">", value: 10485760 }         # optional: read+write bytes/sec
    then:
      hook:
        command: [/usr/local/bin/sermo-proc-alert.sh]
        timeout: 10s
```

Declare at least one of `for`, `cpu`, `memory`, `io`. **All** present conditions
must hold for a PID to fire (AND), and firing is **edge-triggered per PID**: the
hook runs once when the conditions become true and re-arms only after they stop
holding — not every cycle. `cpu` and `io` are rates, so they need two samples: a
just-discovered PID never fires on them in its first cycle. Each matching PID is
tracked and fired independently — **one event and one hook per PID** — so a worker
pool produces one hook per offending worker.

A process hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE` (`process`), `SERMO_PID`
(the matching pid), `SERMO_PROCESS` (the configured name), `SERMO_USER` (if set),
`SERMO_AGE_SECONDS`, `SERMO_MEMORY` (RSS bytes), and — once a rate is available —
`SERMO_CPU` (percent) and `SERMO_IO` (bytes/sec).

`for` is measured from when the daemon **first observed** the process, so a daemon
restart resets it (the real elapsed-since-start is not tracked across restarts).
`io` reads `/proc/<pid>/io`, which requires the daemon to have permission to read
it (typically running as root); when it is unreadable the IO condition never fires.

Other resource types will be added as new check `type` values using the same
watch/hook structure.

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
