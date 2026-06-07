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

### Per-check interval

An individual check may run **less often** than the worker cycle by setting its
own `interval`. The worker keeps ticking at its resolution; a check with an
`interval` simply runs every `round(interval / resolution)` cycles and **reuses
its last result** on the cycles in between — so the check cache and rule windows
stay complete, just with a value that is refreshed less often. This is ideal for
expensive checks (a version probe, `ldd`, a slow command) next to cheap ones.

```yaml
interval: 30s            # the service resolution (or engine.interval)
checks:
  http:
    type: http
    url: "http://127.0.0.1/health"   # runs every cycle (30s)
  version:
    type: command
    command: ["/usr/sbin/nginx", "-v"]
    interval: 30m                     # runs every 60 cycles (30m / 30s)
```

A per-check `interval` **cannot be shorter than the resolution** and should be a
**multiple** of it. If it isn't, the daemon rounds it to the nearest multiple
(at least one cycle) and **logs a warning at startup** — it never fails to start.

## Web UI

The daemon can serve a small web dashboard to view services and act on them —
monitor/unmonitor and start/stop/restart — over the same safe operation engine
the CLI uses. It is enabled by setting a `port`:

```yaml
web:
  address: 127.0.0.1     # optional, default 127.0.0.1 (loopback only)
  port: 9797             # enables the UI; omit the whole block to disable
```

- **Recommended port: `9797`.** It is easy to remember and avoids the common
  monitoring ports (`9090` Prometheus, `9093` Alertmanager, `9100` node-exporter,
  `3000` Grafana, `8080`).
- **No authentication.** The UI can stop/restart services, so it binds to
  **loopback (`127.0.0.1`) by default**. Only change `address` if you front it
  with an authenticating reverse proxy (or a private network you trust).

Endpoints: `GET /` (the dashboard), `GET /api/services` (JSON: name, status,
monitored, backend, unit), `GET /api/services/{name}` (a service's detail: its
checks with the latest result, and its SLA over the rolling windows), and
`POST /api/services/{name}/{action}` where action is `monitor`, `unmonitor`,
`start`, `stop` or `restart`. Clicking a service in the dashboard opens its
detail. The dashboard auto-refreshes every 5s.

The detail's check results are the **latest observed** by the worker (published
each cycle), so they cost nothing to view and reflect each check's own cadence
(see [per-check interval](#per-check-interval)); a check not run yet shows "not
run yet". The SLA windows come from the same data as `sermoctl sla`.

Web-triggered monitor changes are recorded with source `web` in the state store,
and operations take the per-service operation lock, so they never overlap a
worker's action on the same service.

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

## Notifications

`notifiers` are named, typed delivery targets that a watch can send to when it
fires, as an alternative or complement to running a local hook. They are global
daemon configuration; they never merge into a service. Each notifier has a
**name** (the map key) referenced from a watch's `then.notify` list, so different
watches can notify different targets.

```yaml
notifiers:
  ops-email:                 # the name referenced by then.notify
    type: email
    dsn: "smtp://user:pass@smtp.example.com:587"   # smtp:// (STARTTLS) or smtps:// (implicit TLS)
    from: "Sermo <sermo@example.com>"
    to: [ops@example.com, oncall@example.com]       # one or more recipients
```

Notifier types (both use only the Go standard library — no external dependency):

- **`email`** — sends over SMTP.
  - **`dsn`** — `smtp://[user:pass@]host[:port]` (STARTTLS when offered; default
    port 587) or `smtps://…` (implicit TLS; default port 465). Credentials, when
    present, are only sent over an encrypted connection.
  - **`from`** — the sender address (a bare `addr` or `Name <addr>`).
  - **`to`** — one or more recipient addresses.
- **`slack`** — posts to a Slack **incoming webhook**.
  - **`webhook`** — the incoming-webhook URL (`https://hooks.slack.com/services/…`).
    The notification's subject is the lead line and its detail (the `SERMO_*`
    fields) follows in a code block.

```yaml
notifiers:
  team-slack:
    type: slack
    webhook: "https://hooks.slack.com/services/T0000/B0000/XXXXXXXX"
```

The set of notifier **types is pluggable** — new transports (`teams`, …) are
added without touching watches or rules (each registers a builder in
`internal/notify`). A new transport looks the same: a `type` plus its own
fields, addressed by name.

## Host watches

`watches` monitor host-level resources independently of any service and run a
**hook** (a local command) and/or send **notifications** (to named `notifiers`)
when a threshold is crossed. They are daemon configuration; they never merge into
a service.

A watch's `then` block declares the actions taken when it fires — a `hook`, a
`notify` list, or both (at least one is required):

```yaml
watches:
  disk-root:
    check: { type: disk, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      notify: [ops-email]                # send to these notifiers
      hook: { command: [/usr/local/bin/alert-disk.sh, "/"] }   # optional
```

`then.notify` lists notifier names (each must be defined under `notifiers`). For
the multi-metric watches (`net`, `icmp`, `swap`) the `notify`/`hook` live in each
metric's own `then`, so a metric can have its own targets. The notification's
subject/body carry the watch's message and the same `SERMO_*` fields a hook
receives.

**Checks and watches share the same check types.** Any single-shot check — the
host-resource ones below (`disk`, `load`, `fds`, `conntrack`, `entropy`,
`zombies`, `oom`) *and* the service checks (`tcp`, `http`, `command`,
`file_exists`, `binary`, `libraries`, `count`) — can be used as a watch here, and
the host-resource ones can equally be used in a service's `checks:`/rules (see
[Checks](rules.md#checks)). A watch fires its hook on the check's **alert**
outcome: threshold crossed for condition checks, **failure** for health checks
(`tcp`/`http`/…), so e.g. an `http` watch alerts when the endpoint is down. The
multi-metric (`net`, `icmp`, `swap`) and multi-target (`file`, `process`) watch
types below are watch-only because they fire one hook per metric/target.

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

The `disk` check reads filesystem usage for `path` and is true when every present
predicate holds (`op ∈ >=,>,<=,<,==,!=`). Predicates cover **block space** —
`used_pct`, `free_pct` — and **inodes** — `inodes_used_pct`, `inodes_free_pct`,
`inodes_free` (absolute count). Inode predicates catch the "disk full" that `df`
hides: a filesystem out of inodes (millions of tiny files) rejects new files while
bytes are still free.

```yaml
watches:
  disk-root:
    check:
      type: disk
      path: /
      used_pct: { op: ">=", value: 90 }        # block space
      inodes_used_pct: { op: ">=", value: 90 }  # inode table
    then:
      hook: { command: [/usr/local/bin/alert-disk.sh, "/"] }
```

A filesystem that does not report inodes (`inodes_total == 0`, e.g. btrfs) never
fires an inode predicate, so it cannot misread `0/0`.

#### Mount conditions

The `disk` check also verifies the **mount** of its `path`, so a filesystem's
mount and its space are configured in one entry (no duplicated `path`). This also
makes a space check trustworthy: a path that should be a mount but isn't would
otherwise make `statfs` silently report the *parent* filesystem. Add any of:

```yaml
watches:
  data:
    check:
      type: disk
      path: /data
      mounted: true            # require it to be a mount point (set false to require NOT mounted)
      fstype: ext4             # optional: expected filesystem type
      options: [rw, noatime]   # optional: these mount options must all be present
      device: /dev/sdb1        # optional: expected source device
      used_pct: { op: ">=", value: 90 }   # space predicate(s), optional alongside mount
    then:
      hook: { command: [/usr/local/bin/alert-disk.sh, "/data"] }
```

A disk check needs **at least one** of a space/inode predicate or a mount
condition (mount-only is fine). The mount is checked first from `/proc/mounts`: if
it is missing or doesn't match, the check alerts on that and the space predicates
are skipped (their numbers would be meaningless). The result data adds `mounted`,
`fstype`, `device` and `options`.

When the condition holds for the `for`/`within` window, the hook runs (argv only,
never a shell) and/or the notifiers fire, with these environment variables:
`SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_PATH`, `SERMO_VALUE` (the first
predicate's reading), `SERMO_MESSAGE`, plus the rest of the check's data
(`SERMO_USED_PCT`, `SERMO_INODES_USED_PCT`, `SERMO_MOUNTED`, `SERMO_FSTYPE`, …).

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

### `swap` — system swap

A `swap` watch monitors system swap, grouped like `net`/`icmp`: two independent
metrics, each with its own condition **and its own hook**. `usage` catches swap
filling up; `io` catches swap thrashing (heavy paging in/out, a classic sign of
memory pressure).

```yaml
watches:
  swap:
    enabled: false
    interval: 30s
    check: { type: swap }
    metrics:
      usage:                                   # how full swap is (a level check)
        used_pct: { op: ">=", value: 80 }      # any of used_pct / free_pct / free_bytes
        then:
          hook:
            command: [/usr/local/bin/sermo-swap-usage.sh]
      io:                                      # paging activity (thrashing)
        delta: { op: ">", value: 1000 }        # pages swapped in+out per cycle
        then:
          hook:
            command: [/usr/local/bin/sermo-swap-io.sh]
```

- **`usage`** — like `disk`, fires when every present predicate holds. Predicates
  are `used_pct`, `free_pct` (percent of total swap) and `free_bytes`, each
  `{op, value}` with the disk operator set. A host with **no swap configured**
  never fires (so a `free_bytes` predicate does not misfire on a swapless box).
- **`io`** — sums the pages swapped **in and out** (`pswpin`+`pswpout` from
  `/proc/vmstat`) and fires when the **per-cycle delta** satisfies `delta: {op,
  value}` (like `net` errors). The threshold is pages per interval, so it scales
  with `interval`. The first cycle primes a baseline and never fires; counter
  resets clamp the delta to zero.

A swap hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE` (`swap`), `SERMO_METRIC`
(`usage`|`io`), `SERMO_VALUE` (the breaching reading: a percent/bytes for usage,
the page delta for io), `SERMO_MESSAGE`, plus `SERMO_TOTAL_BYTES`/`SERMO_FREE_BYTES`.

### `load` — system load average

A `load` watch checks the system load averages (1/5/15-minute) against thresholds.
Like `disk` it is a single level check with one hook: it fires when every present
predicate holds. With `per_cpu: true` the loads are divided by the CPU count
first, so a threshold means **load per core** (≈1.0 is fully utilized) and the
same config works on any machine size.

```yaml
watches:
  load:
    enabled: false
    interval: 30s
    check:
      type: load
      per_cpu: true                  # optional, default false: divide by NumCPU
      load5: { op: ">", value: 1.5 }    # any of load1 / load5 / load15
      load15: { op: ">", value: 1.0 }
    for: { cycles: 3 }
    then:
      hook:
        command: [/usr/local/bin/sermo-load-alert.sh]
```

Predicates are `load1`, `load5`, `load15`, each `{op, value}` with the disk
operator set; declare at least one. Prefer `load5`/`load15` for sustained
saturation (`load1` is spiky). A load hook receives `SERMO_WATCH`,
`SERMO_CHECK_TYPE` (`load`), `SERMO_VALUE` (the first predicate's reading,
per-core when `per_cpu`), `SERMO_MESSAGE`, plus `SERMO_LOAD1`/`SERMO_LOAD5`/
`SERMO_LOAD15` (raw) and `SERMO_NUM_CPU`.

### `oom` — kernel OOM kills

An `oom` watch fires when the kernel out-of-memory killer has reaped processes
since the last cycle. It tracks the cumulative `oom_kill` counter from
`/proc/vmstat` and compares the **per-cycle delta** to a threshold — the same
stateful pattern as swap `io` / net `errors`.

```yaml
watches:
  oom:
    enabled: false
    interval: 30s
    check:
      type: oom
      # delta is optional; the default fires on any kill (> 0).
      delta: { op: ">", value: 0 }
    then:
      hook:
        command: [/usr/local/bin/sermo-oom-alert.sh]
```

The common case is "alert on any OOM kill", so `delta` may be omitted entirely
(`check: { type: oom }` defaults to `> 0`); set a higher threshold to alert only
on a burst. The first cycle primes the baseline and never fires; a host whose
kernel does not expose the `oom_kill` counter never fires. An oom hook receives
`SERMO_WATCH`, `SERMO_CHECK_TYPE` (`oom`), `SERMO_VALUE` (kills this cycle),
`SERMO_TOTAL` (cumulative), and `SERMO_MESSAGE`.

### `fds` — system file descriptors

An `fds` watch checks the system-wide open file descriptors against the kernel
maximum (`fs.file-max`), read from `/proc/sys/fs/file-nr`. Like `disk` it is a
level check with one hook. Fd exhaustion makes every `open()`/`socket()`/
`accept()` on the host fail with `EMFILE`/`ENFILE`, so it is worth catching early.

```yaml
watches:
  fds:
    enabled: false
    interval: 30s
    check:
      type: fds
      used_pct: { op: ">=", value: 85 }    # allocated / file-max
      # free: { op: "<", value: 10000 }    # absolute headroom, alternatively
    for: { cycles: 3 }
    then:
      hook:
        command: [/usr/local/bin/sermo-fds-alert.sh]
```

Predicates are `used_pct` (allocated as a percent of the limit), `free`
(`file-max − allocated`) and `allocated` (absolute), each `{op, value}` with the
disk operator set; declare at least one. An fds hook receives `SERMO_WATCH`,
`SERMO_CHECK_TYPE` (`fds`), `SERMO_VALUE` (the first predicate's reading),
`SERMO_MESSAGE`, plus `SERMO_ALLOCATED`, `SERMO_MAX`, `SERMO_USED_PCT` and
`SERMO_FREE`.

### `conntrack` — netfilter connection table

A `conntrack` watch checks the netfilter connection-tracking table against its
maximum (`nf_conntrack_max`), read from `/proc/sys/net/netfilter`. Like `disk` it
is a level check with one hook. A full table silently **drops new connections**
(and logs `nf_conntrack: table full, dropping packet`), so it is worth catching
on busy gateways, proxies and NAT boxes before it saturates.

```yaml
watches:
  conntrack:
    enabled: false
    interval: 30s
    check:
      type: conntrack
      used_pct: { op: ">=", value: 90 }    # count / nf_conntrack_max
      # free: { op: "<", value: 20000 }    # absolute headroom, alternatively
    for: { cycles: 3 }
    then:
      hook:
        command: [/usr/local/bin/sermo-conntrack-alert.sh]
```

Predicates are `used_pct` (count as a percent of the max), `free`
(`nf_conntrack_max − count`) and `count` (absolute), each `{op, value}` with the
disk operator set; declare at least one. It needs the `nf_conntrack` module
loaded; on a host without it the check simply never fires. A conntrack hook
receives `SERMO_WATCH`, `SERMO_CHECK_TYPE` (`conntrack`), `SERMO_VALUE` (the first
predicate's reading), `SERMO_MESSAGE`, plus `SERMO_COUNT`, `SERMO_MAX`,
`SERMO_USED_PCT` and `SERMO_FREE`.

### `entropy` — kernel entropy pool

An `entropy` watch checks the available kernel entropy (bits) from
`/proc/sys/kernel/random/entropy_avail` against a threshold. Low entropy makes
reads from `/dev/random` block and slows crypto and TLS handshakes — most visible
on VMs and headless/embedded hosts without a hardware RNG. Like `disk` it is a
level check with one hook.

```yaml
watches:
  entropy:
    enabled: false
    interval: 1m
    check:
      type: entropy
      avail: { op: "<", value: 200 }    # fire when available entropy drops below 200 bits
    for: { cycles: 3 }
    then:
      hook:
        command: [/usr/local/bin/sermo-entropy-alert.sh]
```

The single `avail: {op, value}` predicate (disk operator set) is required; the
usual form is `avail < N`. An entropy hook receives `SERMO_WATCH`,
`SERMO_CHECK_TYPE` (`entropy`), `SERMO_AVAIL`/`SERMO_VALUE` (bits available) and
`SERMO_MESSAGE`.

### `zombies` — defunct processes

A `zombies` watch counts processes in the zombie (defunct) run state — those that
have exited but whose parent has not reaped them — against a threshold. A few are
transient and normal; a growing count means a parent is leaking child slots and
will eventually exhaust the PID table. Like `disk` it is a level check with one
hook.

```yaml
watches:
  zombies:
    enabled: false
    interval: 1m
    check:
      type: zombies
      count: { op: ">", value: 20 }    # fire when more than 20 zombies persist
    for: { cycles: 3 }                 # for a few cycles, to ignore brief spikes
    then:
      hook:
        command: [/usr/local/bin/sermo-zombies-alert.sh]
```

The single `count: {op, value}` predicate (disk operator set) is required; pair it
with a `for` window so a momentary burst of exiting children does not fire. A
zombies hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE` (`zombies`),
`SERMO_ZOMBIES`/`SERMO_VALUE` (the count) and `SERMO_MESSAGE`.

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
      gone: true                      # optional: fire when a tracked PID disappears
    then:
      hook:
        command: [/usr/local/bin/sermo-proc-alert.sh]
        timeout: 10s
```

Declare at least one of `for`, `cpu`, `memory`, `io`, `gone`. The presence
conditions (`for`/`cpu`/`memory`/`io`) **all** must hold for a PID to fire (AND),
and firing is **edge-triggered per PID**: the hook runs once when the conditions
become true and re-arms only after they stop holding — not every cycle. `cpu` and
`io` are rates, so they need two samples: a just-discovered PID never fires on them
in its first cycle. Each matching PID is tracked and fired independently — **one
event and one hook per PID** — so a worker pool produces one hook per offending
worker.

`gone: true` is the inverse — it fires once when a previously-seen matching PID
**disappears** (and re-arms if it returns), so it never fires merely because the
process is present. Set it alone for a pure liveness alert ("nginx is gone"), or
alongside the presence conditions. With multiple matching PIDs it fires per exited
PID, mirroring the per-PID model.

A process hook receives `SERMO_WATCH`, `SERMO_CHECK_TYPE` (`process`), `SERMO_PID`
(the matching pid), `SERMO_PROCESS` (the configured name), `SERMO_CHANGE`
(`threshold` for a presence fire, `gone` for a disappearance), `SERMO_USER` (if
set), `SERMO_AGE_SECONDS`, `SERMO_MEMORY` (RSS bytes), and — once a rate is
available — `SERMO_CPU` (percent) and `SERMO_IO` (bytes/sec).

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
