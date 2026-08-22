# Configuration

Sermo configuration is split by target type: **catalog service/app/lib/pattern
definitions**, **services** as concrete monitored instances, **notifiers** as
delivery targets, and **watches** as host-level monitors. Storage capacity,
network/uplink and fstab-backed mount entries are all watch documents; operators
usually keep them in classified directories such as `storages/`, `networks/` and
`mounts/`, all listed under `paths.watches`. Watch files are single-watch
documents with `name:`; notifier files remain global fragments with a top-level
`notifiers:` map.

New configuration must use one YAML file per target. That means one catalog app,
daemon, lib or pattern per file; one service per file; one notifier per file;
and one host watch per file (`storage`, `network`, `uplink`, `load`, mount
watches and other watch documents). Notifier fragment files still have the
top-level `notifiers:` map, but that map must contain exactly one named entry.
This keeps generated configuration easy to diff, replace and clean up per
target.

A document's **kind is determined by where it lives** — its catalog subdirectory
(`services/` → service, `apps/` → app, `libs/` → lib, `patterns/` → patterns) or
the configured path it loads from (`paths.services` → service, `paths.watches`
→ watch). A catalog `services/` definition (a *catalog service*) and a
`paths.services` instance (a *configured service*) share the kind `service`;
they stay distinct by location. A top-level `kind:` key is therefore **optional
and redundant**; when one is present in a deployed file it must match the
location, which catches a file placed in the wrong directory. Shipped
configuration omits it.

> **Complete annotated example.** [`docs/sermo-all.yml`](sermo-all.yml) shows
> every configuration surface in one place — global config, watches, and one
> document of each kind (a catalog service, app, lib, pattern, a configured
> service, and storage/mount watches), plus a cloned service example — and is
> validated by the test suite, so it cannot
> drift from the schema. It is a reference bundle only; real deployments keep
> one target per file. The shipped operational config is `examples/sermo.yml`.
> From a source checkout, build with `SERMO_DATADIR=$PWD make build`, then use
> `examples/sermo-dev.yml` to validate the bundled example tree without
> rewriting installed `/etc/sermo` paths.

## Contents

- [Schema changes](#schema-changes)
- [Layout](#layout)
- [Storage and mount units](#storage-and-mount-units)
- [Engine settings](#engine-settings)
  - [Per-service interval](#per-service-interval)
  - [Per-check interval](#per-check-interval)
- [Web UI](#web-ui)
  - [Authentication](#authentication)
  - [Behind a reverse proxy (required to expose it)](#behind-a-reverse-proxy-required-to-expose-it)
  - [Liveness (/livez)](#liveness-livez)
  - [Readiness (/readyz)](#readiness-readyz)
  - [Latency graph](#latency-graph)
- [Availability (SLA)](#availability-sla)
  - [Availability time series](#availability-time-series)
- [Notifications](#notifications)
  - [Notification templates](#notification-templates)
  - [Default selection and precedence](#default-selection-and-precedence)
- [Telegram report bot](#telegram-report-bot)
- [Host watches](#host-watches)
  - [then.expand — volume growth (storage watch)](#thenexpand--volume-growth-storage-watch)
  - [then.makestep — forced clock correction (clock watch)](#thenmakestep--forced-clock-correction-clock-watch)
  - [Manual RAID reconstruction control](#manual-raid-reconstruction-control)
  - [Service watches (scoped to a service)](#service-watches-scoped-to-a-service)
  - [net — network interface](#net--network-interface)
  - [icmp — external host (ping)](#icmp--external-host-ping)
  - [clock — wall-clock drift](#clock--wall-clock-drift)
  - [swap — system swap](#swap--system-swap)
  - [load — system load average](#load--system-load-average)
  - [memory — system RAM](#memory--system-ram)
  - [pressure — kernel PSI stall time](#pressure--kernel-psi-stall-time)
  - [oom — kernel OOM kills](#oom--kernel-oom-kills)
  - [fds — system file descriptors](#fds--system-file-descriptors)
  - [diskio — block-device I/O rates](#diskio--block-device-io-rates)
  - [pids — kernel PID table](#pids--kernel-pid-table)
  - [conntrack — netfilter connection table](#conntrack--netfilter-connection-table)
  - [firewall_rules — loaded firewall rules](#firewall_rules--loaded-firewall-rules)
  - [entropy — kernel entropy pool](#entropy--kernel-entropy-pool)
  - [zombies — defunct processes](#zombies--defunct-processes)
  - [Check summaries](#check-summaries)
  - [file — file/directory attributes and freshness](#file--filedirectory-attributes-and-freshness)
  - [process — process by name](#process--process-by-name)
  - [process_policy — alert-only user execution policy](#process_policy--alert-only-user-execution-policy)
- [Global defaults](#global-defaults)
- [Resolution order](#resolution-order)
- [Merge rules](#merge-rules)
- [Per-host overrides (&lt;dir&gt;.local)](#per-host-overrides-dirlocal)
- [Binary resource variables](#binary-resource-variables)
  - [${bindir} search prefix](#bindir-search-prefix)
- [Variables](#variables)
  - [Global custom variables (defaults.variables)](#global-custom-variables-defaultsvariables)
  - [Secrets from the environment](#secrets-from-the-environment)
- [Validating](#validating)
- [Diagnostics](#diagnostics)

## Schema changes

The documented schema is the current contract. When a Sermo-owned configuration
field, alias or YAML shape is removed, do not keep compatibility fixtures or
tests that still spell the removed form. Tests should cover the canonical
current shape and, when strict validation needs coverage, use generic unknown
fields or types instead of retired configuration names. External compatibility
requirements, such as Linux/init metadata that still reports `/var/run` and is
normalized to `/run`, must be documented as explicit exceptions at the owner.

## Layout

```
/etc/sermo/sermo.yml              global config
/usr/share/sermo/catalog/{services,apps,libs,patterns}/*.yml   packaged catalog
/usr/share/sermo/examples/        packaged examples operators may copy/adapt
/etc/sermo/services/*.yml concrete service documents
/etc/sermo/apps/*.yml     host-specific app documents
/etc/sermo/notifiers/*.yml notifier fragments
/etc/sermo/watches/*.yml  generic host watch documents
/etc/sermo/networks/*.yml network/uplink watch documents
/etc/sermo/storages/*.yml storage watch documents
/etc/sermo/mounts/*.yml   fstab-backed storage mount watch documents
/etc/sermo/templates/*.yml notification templates
/etc/sermo/<dir>.local/*.yml per-host overrides a deployment never overwrites
```

The configurable directories Sermo reads come from `paths` in the global config:

```yaml
paths:
  services:
    - /etc/sermo/services
  apps:
    - /etc/sermo/apps
  notifiers:
    - /etc/sermo/notifiers
  watches:
    - /etc/sermo/watches
    - /etc/sermo/networks
    - /etc/sermo/storages
    - /etc/sermo/mounts
  runtime: /run/sermo
  state: /var/lib/sermo
  templates: /etc/sermo/templates
```

The packaged catalog is always loaded from the catalog directory compiled into
the binary. The project Makefile sets that to `$(SERMO_DATADIR)/catalog`, which
defaults to `/usr/share/sermo/catalog` in packaged builds. If a service is not
in the packaged catalog yet, define it as a normal local service under
`paths.services`.

Directory lists under `paths.services`, `paths.apps`, `paths.notifiers` and
`paths.watches` accept either a path string or an explicit mapping:

```yaml
paths:
  services:
    - /etc/sermo/services          # recursive: false
    - path: /etc/sermo/services.d
      recursive: true
```

When `recursive` is omitted it defaults to `false`. A non-recursive entry loads
only `.yml`/`.yaml` files directly inside that directory. `recursive: true`
descends the whole subtree, still loading files in deterministic sorted order.
Unknown keys under `paths` are rejected so typos do not silently disable a
configured source.

`paths.runtime` is the root for named runtime locks (`<runtime>/locks`, one file
per lock named `<service>[.<name>].lock`) and internal operation locks
(`<runtime>/ops/<service>.lock`). It lives on tmpfs and is wiped on reboot.
`paths.locks` is **not** supported. See [Locks](safety.md#locks) for the TTL and
stale-reclaim semantics.

Only service and app document directories have config-relative fallbacks. If
`paths.services` or `paths.apps` is omitted, Sermo falls back to `services/` or
`apps/` next to the loaded `sermo.yml` file. With the standard
`/etc/sermo/sermo.yml` this means `/etc/sermo/services` and `/etc/sermo/apps`.

Optional include directories have no implicit fallback. If `paths.notifiers`,
or `paths.watches` is omitted or empty, Sermo loads no notifiers or host watch
documents; sibling `notifiers/`, `watches/`, `networks/`, `storages/` or
`mounts/` directories next to `sermo.yml` are ignored until listed explicitly
under `paths`.

Every new service document, notifier fragment or watch document under configured
directories should be isolated in its own `.yml` file, even when several targets
are generated in the same wizard run. A storage watch may expose mount
operations with a top-level `mount:` block while keeping capacity monitoring in
the same target.

Use `/run` for runtime paths in Sermo configuration and examples. Do not write
new `/var/run` pidfiles, sockets, lockfiles or runtime directories in
Sermo-owned config.
Linux keeps `/var/run` as a compatibility path for `/run`, and older init
scripts, service managers or packaged configs may still report it; Sermo
normalizes those host-provided paths to the equivalent `/run/...` spelling.
Use `pidfile:` for one logical process with candidate pidfile paths, and
`pidfiles:` for several required process roles. `pidfiles.<role>` must have a
matching `processes.<role>` with exact `exe` and `user`.
When a pidfile is backend-specific, `pidfile: {path: /run/name.pid,
optional: true}` keeps the discovery source but downgrades the generated
health check to a warning.
Use `lockfile:` only for a regular runtime file created by the service itself;
it is a health artifact like `socket:`, not an operation lock.

Before adding a new runtime path, resolve it on the target host:

```sh
readlink -f /var/run/example.pid
namei -l /var/run/example.pid
```

If the path resolves through a symlink, configure the canonical target path
instead. This is especially common for Linux `/var/run` → `/run` compatibility,
but can also happen with app-specific runtime directories.

Catalog apps may declare `version_from: <app-name>` when a different binary from
the same package has the authoritative version probe. The app still checks its
own `variables.binary` for installation and health; `version_from` only fills
the displayed
version when the app has no local version result. Local `health`, `version` and
`version_short` commands still win. The referenced app must be another catalog
app addressed by its canonical name, and `version_from` chains must not cycle.
This is not an operational dependency and does not inject preflight checks into
services.

Catalog apps may also declare `version_match` to prove the identity of a
compatibility binary before considering the app installed. The matcher is
evaluated against the combined stdout/stderr of the app's `version` command and
supports `contains`, `excludes` and `regex` string matchers. A failed
`version_match` marks the app as not installed, even when the binary exists; this
lets MariaDB use an older `/usr/sbin/mysqld` fallback without also showing the
MySQL catalog app on MariaDB hosts. When a service links the app through
`apps:`, the matcher is copied into that app's namespaced version preflight.

Catalog and service documents may declare `aliases: [...]`, a list of alternate
simple names. Aliases are metadata: they resolve names but never merge into the
runtime service body. Catalog aliases let `uses:` accept distro spellings such
as `apache2` for the canonical `apache` profile. Service aliases let
`sermoctl` commands accept alternate names and operate on the canonical
configured service. Aliases must not duplicate another name or alias of the same
document kind.

When a catalog service or service lists apps, every app variable is also
available to that catalog service/service with a normalized app-name prefix: an
app with `variables: { binary: /usr/bin/cupsd, cups_config: /usr/bin/cups-config
}` exposes `${cupsd_binary}` and `${cupsd_cups_config}`. Command preflight
entries named `version` or `version_short` also declare `${cupsd_version}` and
`${cupsd_version_short}` with empty defaults; an explicit command `export:` may
declare additional variables. At runtime, successful command checks publish the
same exported names in the check result `data`; a `version` command also derives
`version_short` from its stdout, preferring `major.minor[.patch]` and accepting
guarded integer-only `version N` output, including date-coded releases, when no
dotted version is present.

Dashes and other non-alphanumeric characters become underscores. This lets a
service reuse binary paths owned by one or more apps without naming collisions.

When exactly one app is linked, its variables are also exposed without the
prefix as defaults, so a service can use `${binary}` while the app remains the
owner of the binary path. A local `variables:` entry with the same prefixed or
unprefixed name still wins for host-specific overrides. When several apps are
linked, use the prefixed names to avoid ambiguity.

`paths.state` (default `/var/lib/sermo`) is the root for the persistent state
database `sermo.db` (SQLite). Unlike `paths.runtime`, it survives reboots, which
is what lets a service's or watch's `monitor: previous` flag restore its last
monitoring state. It also stores automatic-remediation cooldown/backoff, rule
`for`/`within` window progress and the latest service-check and host-watch
readings, so restarting `sermod` does not reset when a rule may act again or make
the dashboard lose the last real daemon-cycle result. SLA and check measurements,
plus service and daemon process metric history shown in the web UI live there too.

Both directories are created **0700, owner root**. On systemd they come from the
shipped `tmpfiles.d/sermo.conf` (installed at `/usr/lib/tmpfiles.d/sermo.conf`),
applied at boot by `systemd-tmpfiles-setup` or immediately with
`systemd-tmpfiles --create sermo.conf` rather than from the `sermod.service`
unit's `RuntimeDirectory=`/`StateDirectory=`. On OpenRC the init script's `checkpath`
creates them at 0700. The daemon also creates either at 0700 if it has to, so the
mode holds even outside the packaging.

`paths.templates` (default `/etc/sermo/templates`) is the directory for
notification templates. `make install` creates it and installs
`default-alert.yml`.

## Storage and mount units

A storage watch defines a named filesystem target in any directory listed under
`paths.watches` (commonly `/etc/sermo/storages` or `/etc/sermo/mounts`). It uses
`check.type: storage` for capacity and mount-state monitoring, and may add a
top-level `mount:` block for `sermoctl mount`/`sermoctl umount`. Mount operations
deliberately use `/etc/fstab` as the source of truth: the YAML contains the mount
path and Sermo policy only, not `source`, `fstype`, `options` or class metadata.
When a storage watch has `mount:`, set `check.mounted: true` when the path must
be a mounted mountpoint.

```yaml
name: mount-backup
display_name: Backup mount
category: storage

monitor: previous
interval: 30s

check:
  type: storage
  path: /mnt/backup
  mounted: true
  used_pct: { op: ">=", value: "90%" }
for: { cycles: 3 }
then:
  notify: default

mount:
  refcount: true
  umount:
    term_timeout: 12s
    kill_timeout: 5s
  stop_policy:
    kill_only_if:
      users: [backup]
      exe_any: [/usr/bin/rsync]
```

The CLI accepts either the configured storage name or the absolute mount path:

```sh
sermoctl mount mount-backup
sermoctl mount /mnt/backup
sermoctl umount mount-backup
sermoctl umount mount-backup --force --lazy
sermoctl umount mount-backup --kill-blockers
sermoctl umount /mnt/backup
sermoctl mount status mount-backup
sermoctl mount list
```

The Web UI's **Mount units** panel exposes storage watches that have a `mount:`
block. It can mount/unmount, show the same busy-process blockers before
unmounting, send a native TTY alert to logged-in users who are blocking the
mount, and let the operator choose `force`, `lazy` and `kill blockers` for that
single unmount attempt. The `kill blockers` option only signals current blockers
that match `mount.stop_policy.kill_only_if`; every blocker remains visible in
the confirmation table even when no kill policy is configured.
The root filesystem (`path: /`) is read-only from mount operations: Sermo will
show it as mounted, but CLI and Web/API `umount`, blocker alerts and
blocker signalling are rejected.

With `mount.refcount: true` (the default), every successful `mount` increments
Sermo's runtime counter and `umount` decrements it. The real `umount` only runs
when the counter reaches zero; if the path is not mounted yet, the first
`mount` runs `mount <path>` and requires a matching `/etc/fstab` entry. The
counter is kept under `<paths.runtime>/mounts/state`, and each mount operation
uses a per-target lock under `<paths.runtime>/mounts/ops`.

Normal unmount is conservative: Sermo first runs `umount <path>`. If the mount
is still busy, `sermoctl umount --force` or the Web UI `force` checkbox permits
`umount -f <path>`. `--kill-blockers` or the Web UI `kill blockers` checkbox
then permits TERM/KILL only for blockers that match
`mount.stop_policy.kill_only_if`; cmdline is display data and never authorizes a
kill. `--lazy` or the Web UI `lazy` checkbox permits `umount -l <path>` as the
last fallback.

## Engine settings

The `engine` block is daemon-wide configuration consumed by `sermod`; it never
merges into a service:

```yaml
engine:
  backend: auto               # auto | systemd | openrc
  interval: 30s               # default cycle interval; per-service overridable
  max_parallel_checks: 8        # bound on concurrent checks across all services
  default_timeout: 10s        # default per-check timeout
  operation_timeout: 90s        # outer deadline for safe service actions
  artifact_interval: 5m       # cadence for apps, libraries and service config/version artifacts
  startup_delay: 0            # grace period before the first cycle (0 disables)
  user_lookup: auto           # auto | native | getent | numeric
  user_lookup_timeout: 250ms  # per-getent lookup timeout; cached in-process
  reap_own_strays: true       # SIGTERM leftovers of a previous sermod in its own cgroup at startup
  state_cache_size: 64M       # SQLite page cache for the state database
  retention_1m: 3h            # per-minute history (the 1h graphs)
  retention_5m: 30h           # 5-minute history (the 24h graphs)
  retention_1h: 216h            # hourly history (the 7d graphs)
  retention_6h: 912h           # 6-hourly history (the 30d graphs)
  retention_1d: 8784h          # daily history (the 1y graphs, rolling-year SLA)
  retention_events: 720h       # event/activity feed
  rollup_interval: 5m         # consolidation + prune cadence
  # service_restart_notice:    # optional normal alert for a newly started principal process
  #   uptime_below: 5m
  #   notify: [ops-email]      # explicit targets; does not inherit top-level notify
  #   subject: "[sermo] ${restart.service}: main process restarted"
  #   message: "${restart.service} PID ${restart.pid} started ${restart.uptime} ago"
  # Optional append-only JSONL export logs (opt-in: omit a key to disable it).
  # access: /var/log/sermo/access.log
  # events: /var/log/sermo/event.log
  # diagnostics: /var/log/sermo/diagnostics.log
  # diagnostics_interval: 1h  # scheduled diagnostics when diagnostics is set
```

Optional `engine.access`, `engine.events` and `engine.diagnostics` enable
append-only JSON Lines export under absolute paths. Each path must be absolute
when set; parent directories are created as needed (`0750` dirs, `0640` files).
Omit a key to leave that channel off.

- `engine.events` mirrors every daemon event the web UI and `sermoctl activity`
  already record (actions, alerts, hooks, suppressions, …) in addition to the
  SQLite store.
- `engine.access` records mutating operator traffic: POST actions through the web
  API and state-changing `sermoctl` commands (`monitor`, `start`, `lock`, …).
  Web records carry the parsed target and action (services, watches, mounts,
  locks, …) and the request query string when present (for example
  `umount?kill=1` or `clear?before=24h`), so action-changing flags are audited.
  Routine GET polling is not logged.
- `engine.diagnostics` runs scheduled configuration/host diagnostics in the
  background (default interval `1h`, overridable with `engine.diagnostics_interval`)
  and appends each snapshot as one JSON line to the file. Rotate and retain the
  file with your host's log tooling (for example logrotate); Sermo does not prune
  it.

`engine.interval` is the default cadence at which every service's checks are
run. Each service runs all of its checks once per cycle.

### Principal-process restart notice

`engine.service_restart_notice` is opt-in. When configured, Sermo emits one
normal `alert` event for each new identity of a service's trusted principal
process whose real uptime is below `uptime_below`. It runs in the first active
daemon cycle too, so it detects a restart that occurred while `sermod` was
stopped. This is an event and notification only: it never changes the service,
does not affect SLA, and appears through the normal Web UI and `sermoctl
activity` feed.

```yaml
engine:
  service_restart_notice:
    uptime_below: 5m       # required, strictly positive
    notify: [ops-email]    # required; `none` records the event without delivery
    subject: "[sermo] ${restart.service}: main process restarted"  # optional
    message: >-            # required
      ${restart.service} principal process ${restart.process}
      (PID ${restart.pid}) started at ${restart.started_at};
      uptime ${restart.uptime}.
```

The selection in `notify` is deliberately explicit and never inherits the
top-level `notify` default. `subject` defaults to
`[sermo] ${restart.service}: main process restarted`. The message and subject
may use `${restart.service}`, `${restart.unit}`, `${restart.process}`,
`${restart.pid}`, `${restart.uptime}`, `${restart.uptime_seconds}`,
`${restart.started_at}`, and `${restart.threshold}`, as well as the standard
runtime variables such as `${date}` and `${event}`.

Sermo trusts only a backend or an explicit service identity: systemd's `MainPID`,
Docker's init PID, or an OpenRC pidfile / named `processes.main` selector. If it
cannot identify one principal process safely, it does not guess and emits no
restart notice. The PID and start timestamp are kept in the state database, so
each identity produces at most one notice across daemon restarts. A Sermo
operation is recorded but deliberately not notified during its settling cycle,
which prevents a CLI, Web UI, or automatic Sermo restart from being reported as
external.

Outbound delivery follows normal alert safety: panic mode suppresses it and
dry-run mode uses only the enabled terminal route. A notifier template also
receives `SERMO_RESTART_SERVICE`, `SERMO_RESTART_UNIT`,
`SERMO_RESTART_PROCESS`, `SERMO_RESTART_PID`, `SERMO_RESTART_UPTIME`,
`SERMO_RESTART_UPTIME_SECONDS`, `SERMO_RESTART_STARTED_AT`, and
`SERMO_RESTART_UPTIME_BELOW` as structured fields.

`engine.artifact_interval` (default `5m`) is the cadence at which the daemon
inspects installed catalog apps, catalog libraries, and service version/config
artifacts. An app or library profile may set its top-level `interval`; a service
uses its own top-level `interval` for version/config monitors and changed paths.
A service worker may still run more often, but reads the latest shared artifact
sample instead of re-running a version command or filesystem probe each cycle.
The internal `artifact:*` samplers only refresh those shared samples: they do
not emit firing/recovered events or notifications. Service operations still run
their normal preflight checks directly.

`engine.backend: auto` detects the init system: probe systemd (`systemctl`
exists, `/run/systemd/system` exists, `is-system-running` usable — `degraded`
counts as usable) and OpenRC (`rc-service` exists, `/run/openrc` exists or
`rc-status` works). With exactly one available it is used; with both, the
**active init system wins** (PID 1 / systemd state, else a working OpenRC) —
never mere command presence; with neither, or an unresolvable tie, startup
fails asking for `--backend`, `SERMO_BACKEND` or `engine.backend`. That is
also the override order: CLI flag > environment > config > auto-detection.
For OpenRC oneshot services whose `status` command cannot report cleanly, Sermo
falls back to `rc-status -a` and trusts the init state.

Safe service actions (`start`, `stop`, `restart`, `reload`, `resume`) run
without a global concurrency cap. Each service is still serialized by its own
cross-process operation lock (`<paths.runtime>/ops/<service>.lock`), automatic
remediation is rate-limited by the mandatory per-service `policy` block
(cooldown, `max_actions`, backoff), and every action is bounded by
`engine.operation_timeout`.

`engine.operation_timeout` is the outer deadline for a safe
start/stop/restart/reload/resume/repair. The engine may raise it per service when the resolved
`stop_policy` needs longer (graceful stop plus signal escalation). The same
limit applies to automatic remediation, `sermoctl` actions and web-initiated
operations. When the web UI is enabled, `sermod` also sets the HTTP server's
initial write timeout from the longest resolved deadline and extends each long
action response from the active configuration after a reload, so an operation is
not cut off mid-request. The default is `90s`.

`engine.startup_delay` is a non-negative duration that holds the daemon before
it starts its first check cycle, giving the host time to finish booting so
services that are still coming up are not flagged or remediated prematurely. The
wait applies once, on startup, before any worker runs; a shutdown signal during
the wait aborts cleanly without starting any worker. The default `0` disables it.

`engine.reap_own_strays` is startup hygiene for sermod itself: on start, before it
has spawned anything of its own, sermod sends `SIGTERM` to every process left in
its **own** init unit control group. A unit configured `KillMode=process` or
`KillMode=none` leaves the previous incarnation's children there, and each one
keeps holding its descriptors, inotify instances and memory. It runs only when
sermod's control group is a systemd **service** unit — never from a login shell,
where it would share a scope with the operator's own shell — sends only `SIGTERM`,
and emits one event per process signalled. It defaults to on; set it to `false` to
turn it off. See [safety.md](safety.md).

`engine.user_lookup` controls how Sermo turns user/group names into UID/GID
values for runtime process identity:

- `auto` (default): if the binary was built with CGO enabled, Go's `os/user`
  uses libc/NSS, so LDAP/SSSD/NIS-backed users resolve through the host's normal
  identity stack. If the binary was built static with `CGO_ENABLED=0`, Sermo
  first uses the native passwd/group reader and then falls back to `getent
  passwd` / `getent group` so the static binary can still ask the host NSS
  setup.
- `native`: use only Go's `os/user`. With CGO disabled this normally means
  local `/etc/passwd` and `/etc/group`.
- `getent`: prefer `getent passwd|group`, then fall back to native lookup.
- `numeric`: disable name lookup. Numeric UID/GID selectors still work; named
  selectors fail closed and owner columns show numeric IDs when no name is
  available.

`engine.user_lookup_timeout` bounds each `getent` call; results, including
misses, are cached in the running process so normal monitoring does not spawn a
command for every process every cycle. If a name cannot be resolved, Sermo does
not guess: process selectors and `kill_only_if.users` using that name do not
match. For critical stop policies, numeric UIDs/GIDs are the most deterministic
form.

`engine.state_cache_size` (default `64M`) sets the SQLite page cache for the
state database (`paths.state`). The state DB accumulates SLA, measurement and
metric history whose indexes grow into the tens of MB; the cache
keeps those hot pages in memory so a per-cycle write burst does not read them back
from disk and stall an interactive `monitor`/`unmonitor` (every statement shares
one connection). Raise it on hosts with a large history and spare RAM (the value
is a byte size with a `K`/`M`/`G` suffix); it is taken from the running daemon's
config and applies the next time `sermod` opens the database (a restart, since the
handle is held open for the daemon's lifetime).

#### Stored history resolution

Sermo keeps one archive per graph window instead of per-minute history for a year.
Every chart draws a fixed number of columns — 120 for the metric charts, 90 for the
SLA strip — so a bucket finer than `window / columns` would be stored and never
displayed. Each archive holds the bucket span its window actually draws, and is
retained just past that window:

| Bucket span | `engine` key | Default | Serves |
|---|---|---|---|
| 1 minute | `retention_1m` | `3h` | the `1h` graphs, and recent-incident detail |
| 5 minutes | `retention_5m` | `30h` | the `24h` graphs |
| 1 hour | `retention_1h` | `216h` (9 days) | the `7d` graphs |
| 6 hours | `retention_6h` | `912h` (38 days) | the `30d` graphs |
| 1 day | `retention_1d` | `8784h` (366 days) | the `1y` graphs and the rolling-year SLA |

Each archive is consolidated from the one below it, not from the live samples, so a
long daemon outage cannot lose a resolution. **Consolidation is exact**:
availability sums its counters, metrics sum the sample count and total (keeping the
average weighted, never an average of averages) and carry the minimum and maximum
through. A ratio, average or extreme read from the daily archive equals the one
computed from its minute buckets.

What consolidation does give up is *when*: past `retention_1m` an incident is
located to its bucket, not to its minute. The count survives — each bucket records
how many one-minute buckets inside it saw a failure — so a window reports "3
affected minutes" however coarse it has become, and the SLA strip colours those
buckets accordingly (see [Web UI](webui-representation.md#sla-timeline-strip)).
For the exact timestamp of an old incident, use the event/activity feed, retained
separately by `engine.retention_events` (default `720h`, 30 days).

`engine.rollup_interval` (default `5m`) is how often `sermod` consolidates and
prunes. Keep it well under `retention_1m`: the per-minute archive is the source
every coarser one reads, and its prune is floored at the consolidation watermark,
so a slower cadence delays reclaiming space rather than losing history. A window
served by a consolidated archive is therefore at most one interval behind the live
samples; the `1h` window reads per-minute buckets and is immediate.

Raising a retention costs proportionally more disk at that resolution; lowering one
takes effect on the next maintenance pass. Reclaiming the freed pages needs a
`VACUUM`, which `sermoctl state compact` performs.

Archive maintenance scans the selected resolution partition instead of carrying
secondary `(res, bucket)` indexes. With the default three-hour finest retention that
scan is bounded, while removing the two index copies reduces write amplification and
disk use. Budget the archive rows plus normal SQLite/WAL headroom; do not double the
archive estimate for those removed indexes.

When `sermoctl daemon reload` asks the running daemon to reload, `sermod` reads
the configuration from the path passed to `sermod run --config` (the same file
`sermoctl` uses). `sermod` validates the new config, rebuilds its service
workers and host watches, and swaps them in without restarting the process.
Per-service runtime state is preserved across reload: monitoring cycle counters
and watched-file baselines for `changed:` conditions stay in memory, while
remediation cooldown/backoff and rule `for`/`within` windows are also persisted
in `paths.state` and survive a full `sermod` process restart. Invalid config, or
a config with no included services or watches, is rejected and the current
generation keeps running; a `reload` or `error` event is recorded. Reload does
not repeat `startup_delay` and does not mark `/readyz` as shutting down.

`paths.runtime` and `paths.state` are process-lifetime resources: they contain
the daemon singleton/operation locks and its open persistent store. Changing
either requires a full `sermod` restart; a configuration reload rejects that
change and leaves the current generation running.

The `web` block is also startup-only: its listener address/port, authentication
and guest policy are installed on the HTTP server when `sermod` starts. Change
those settings with a full restart; a configuration reload rejects them rather
than leaving the old web access policy active.

`engine.interval` remains reloadable and immediately reschedules services that
inherit the global cadence. `engine.operation_timeout` is also reloadable; web
action responses extend their deadline from the active configuration, including
a resolved per-service `stop_policy` timeout. Per-service CPU rate baselines are
reset only when a service is removed from the running config; persisted metric
and event history remains in `paths.state` until the configured retention prunes
it or an explicit `sermoctl state compact` runs.

Trigger a daemon configuration reload with:

```sh
sermoctl daemon reload
```

Installed service managers expose the same in-place reload through their native
interface:

```sh
systemctl reload sermod  # systemd
rc-service sermod reload # OpenRC
```

Both send `SIGHUP` to the init-tracked `sermod` process; they validate and swap
the configuration in place rather than restarting the daemon. As with
`sermoctl daemon reload`, an invalid configuration leaves the current generation
running and is reported in Sermo's event log and daemon log.

Only one `sermod` instance may run per `<paths.runtime>` directory (default
`/run/sermo`). At startup it takes an exclusive lock on
`<paths.runtime>/sermod.lock`; if another instance already holds it, the new
process logs a warning, exits with status **1**, and does not start a second
monitor loop.

The daemon writes `<paths.runtime>/sermod.pid` (default `/run/sermo/sermod.pid`)
at startup to make `sermoctl daemon reload` reliable. If no pidfile is present,
`sermoctl daemon reload` falls back to locating the running `sermod` process by
name — a native scan of `/proc`, no external `pidof`/`pgrep` needed.

While the dashboard requires authentication, the daemon also writes
`<paths.runtime>/web.token` (mode `0600`, removed when it stops): the admin
credential `sermoctl` uses to reach the web API, which hashed passwords cannot
supply. See [Hashed credentials](#hashed-credentials).

`sermoctl daemon reload` reloads `sermod`'s own configuration (as above).
`sermoctl reload <service>` is a different operation — it reloads *that service*
in place through the engine (preflight → reload → health). How a service reloads,
including the `reload:` block that lets Sermo signal a service when its init unit
has no reload, is documented in
[services.md](services.md#reload-on-config-change-reload_on_change).
If neither the init backend nor a valid `reload.command`/`reload.signal`
fallback can reload the service, `sermoctl reload <service>` is rejected before
execution.

### Per-service interval

`engine.interval` sets the default for every service. A service may override it
with its own top-level `interval`, so cheap services can be checked often and
expensive ones rarely without changing the global default:

```yaml
name: nginx
interval: 10s            # optional, default engine.interval; positive duration
watches:
  http:
    check: { type: http, url: "http://127.0.0.1/health", expect_status: 200 }
```

The override governs the whole worker cycle for that service (its checks, rules
and remediation), exactly like the global interval — only its cadence differs.
Window counts (`for`/`within` with `cycles`) are therefore counted in that
service's own cycles; duration windows use wall-clock elapsed time between those
observed cycles. Worker starts are still spread across one global interval so a
fleet of services does not all probe on the same tick.

### Per-check interval

An individual check may run **less often** than the worker cycle with
`interval`. The worker keeps ticking at its resolution; the check runs every
`round(interval / resolution)` cycles and **reuses its last result** between
runs, keeping check caches and rule windows complete. On startup, reload or any
config change that introduces a check, Sermo runs it once immediately when no
cached result exists, then applies the configured cadence.

```yaml
interval: 30s            # the service resolution (or engine.interval)
watches:
  http:
    check:
      type: http
      url: "http://127.0.0.1/health"   # runs every cycle (30s)
  version:
    check:
      type: command
      command: ["/usr/bin/nginx", "-v"]
      interval: 30m                     # runs every 60 cycles (30m / 30s)
```

A per-check `interval` **cannot be shorter than the resolution** and should be a
**multiple** of it. If it isn't, the daemon rounds it to the nearest multiple
(at least one cycle) and **logs a warning at startup** — it never fails to start.

For a GlusterFS service, `gluster_cluster` is a local service watch that uses
the already configured `gluster` client to check peer membership, volume and
brick availability, self-heal daemons, and optional pending-heal limits. The
remote deployment generator derives its expected peers and volumes from the
host's read-only Gluster XML inventory; an explicit configuration follows the
same shape. See [the check reference](rules.md#ports) for its full contract.

```yaml
watches:
  cluster:
    interval: 2m
    check:
      type: gluster_cluster
      peers: [node-b, node-c]
      volumes:
        images:
          bricks: 3
          self_heal: true
          max_heal_entries: 0
          max_split_brain_entries: 0
```

## Web UI

The daemon can serve a small web dashboard to view services and watches.
Admins can monitor/unmonitor both, and can start/stop/restart/reload/resume/repair services
over the same safe operation engine the CLI uses.

When an active service declares exact `processes:` identity (`exe` plus `user`),
`restart` first verifies that at least one live process still matches that
identity. If the init backend says the unit is active but Sermo cannot match the
configured executable/user, the restart is blocked before stop/start so a bad
daemon binary path cannot make Sermo act on an untrusted service identity.

A service normally resolves to a systemd/OpenRC unit. It can instead declare a
per-service `control:` target for non-init resources: `control.type: libvirt`
for QEMU/libvirt VMs or `control.type: docker` for Docker containers. Those
targets still use the same locks, guards, preflight checks and operation
timeouts; see [services](services.md#control-docker--docker-containers).

Below the services table the dashboard lists **installed applications** (catalog
app daemons whose binary is present) and **installed libraries** (catalog
library files whose `preflight.file` is present). Both inventories show name and
short version when available; an app `health` command, when configured, decides
OK/error from its exit code before the version command is considered. If no
`health` command is configured, the `version` command is the fallback probe
while fetching the displayed version.

The lists are sortable by name, category or version, and expanding a row reveals
the full version string, file location and permissions. When a version is
inherited through `version_from`, the API row includes `version_source` with the
provider app name. Services, applications and libraries can be filtered and
grouped by their top-level `category` metadata field. The same data is available
from `sermoctl apps`, `sermoctl libs`, `GET /api/applications` and `GET
/api/libraries`. The dashboard caches each inventory for up to 5 minutes, so
auto-refreshes do not rerun every version probe. Each row shows when those
version/status probes actually ran; serving a cached response does not advance
that sample time. For an editable panel-by-panel map, see
[webui-representation.md](webui-representation.md).

**The web UI is only activated when `web.port` is explicitly defined.** If the
`web:` block is omitted, or if a `web:` block is present without a `port` key
(even if other keys such as `address` are set), the HTTP server is not started.
At startup `sermod` logs a warning: "web ui disabled; no port will be opened".

```yaml
web:
  address: 127.0.0.1        # optional, default 127.0.0.1 (loopback only)
  port: 9797                # REQUIRED to activate the web UI (9797 recommended)
```

- **Activation rule:** the web UI ("servicio web") is **not started** unless
  `web.port` is present and valid. Omitting the key (or the whole `web:` block)
  leaves the dashboard disabled; `sermod` logs the exact reason at startup.
- **Recommended port: `9797`.** It is easy to remember and avoids the common
  monitoring ports (`9090` Prometheus, `9093` Alertmanager, `9100` node-exporter,
  `3000` Grafana, `8080`).
- **Authentication** is optional but recommended before exposing it. Without it,
  the UI binds to **loopback (`127.0.0.1`) by default** and is fully open.

### Authentication

Set passwords on the `web` block for HTTP Basic auth with two roles:

```yaml
web:
  port: 9797
  password: "s3cret"           # admin: read + actions (start/stop/restart/reload/resume/repair, monitor/unmonitor)
  guest_password: "lookonly"   # optional: a read-only login
  guest: true                  # optional: allow anonymous read-only access
```

- **admin** — full access. Granted by `password`.
- **guest** — **read-only**: can view everything but every action (a `POST`) is
  refused with `403`. Granted by `guest_password`, and/or to anyone when
  `guest: true` (anonymous read-only). Process **command lines are redacted to
  the executable** for guests (service process trees and mount blockers):
  arguments can carry secrets that only admins should see.

#### Passwords from a file

Either password can come from a **file** instead, through its `_file` companion
key — for a secret provisioned by another tool (systemd `LoadCredential`, a
secrets manager, an Ansible template) that must never be written into
`sermo.yml`:

```yaml
web:
  port: 9797
  password_file: /etc/sermo/secrets/web.pass          # instead of `password`
  guest_password_file: /etc/sermo/secrets/guest.pass  # instead of `guest_password`
```

- The file holds **one credential per line**. Blank lines and lines starting
  with `#` are ignored; surrounding whitespace and the trailing newline every
  editor adds are stripped. A file with no usable credential is a configuration
  error rather than an empty password.
- **Any** credential in the file grants that file's role. That is how a password
  rotates without a cut, and how each operator gets their own.
- A **relative** path resolves against the directory holding `sermo.yml`, like
  the `paths.*` directories. `${env:...}` works in the path as anywhere else.
- `password` and `password_file` are **mutually exclusive**, as are
  `guest_password` and `guest_password_file`. Setting both is a configuration
  error — which of the two wins should never be left to the reader.
- If the file cannot be read, `sermod` **refuses to start** (exit `78`, the
  standard configuration-error code) and logs the reason. It never falls back to
  an open dashboard. `sermoctl` still runs, and `config validate` reports the
  same message.
- Keep the file readable by the daemon user only (`chmod 0600`). It is a secret
  in the filesystem, not in the config, and it should be treated as one. `sermod`
  logs a warning at startup when the file is readable beyond its owner.

#### The browser password prompt

The dashboard authenticates with HTTP Basic, so the browser's own password box
is the login form. It appears when a **document** is requested without a usable
credential: the dashboard itself (`/`), and `/login`, which exists to summon the
box on demand and then send you back home — that is how an anonymous guest
escalates to admin.

The challenge uses a **per-host realm**: `Basic realm="Sermo <hostname>"`, where
`<hostname>` is the same short host identity as `${hostname}` (first DNS label,
or `SERMO_HOSTNAME` when set). On `algieba` the browser shows a realm of
`Sermo algieba`, so the password manager can tell one dashboard from another when
many tabs are open. If the host identity is unknown the realm falls back to
`Sermo`.

Everything else answers `401` **without** a `WWW-Authenticate` header: the JSON
API, and `/api/stream`, the Server-Sent Events channel the dashboard keeps open.
That distinction matters because the stream reconnects on its own, every five
seconds by the server's own `retry` hint. When those replies still carried the
challenge, a reconnect that arrived without the cached credential made the
browser raise a modal password box at an operator who was doing nothing but
reading — and several dashboards open at once multiplied the reconnections, so
the prompts arrived every few minutes. The dashboard now handles a bare `401`
itself by navigating to `/login`, which asks once, deliberately.

A client that sets neither `Sec-Fetch-Mode` nor `Accept` is treated as an API
caller, except on `/` and `/login`, which always challenge so a first login
works from anything.

#### Hashed credentials

A credential may be stored **hashed**, so the file never holds a password anyone
can read — the `/etc/shadow` model, with no decryption key to provision:

```
# /etc/sermo/secrets/web.pass
$2a$12$K3JqR7uH...                          # ana
$sha256$c2FsdA$9b74c9bd...                  # provisioning
still-cleartext-if-you-want-it
```

`sermoctl web hash-password` writes the line for you:

```console
$ sermoctl web hash-password --name ana >> /etc/sermo/secrets/web.pass
Password: ****
Repeat:   ****

$ sermoctl web hash-password --generate      # a generated secret, shown once
secret: X7KGiJqO-stXR3W1dlWLqzguYt-TJkZYCgiP2hP0XcA
$sha256$i6xhXZ6zQlQpysyEusOo4A$2PnStYnKgGXhXLNqy2a/gEBn5EuBv9HmWOZjVJigIys

$ printf '%s' "$PASS" | sermoctl web hash-password --stdin
```

- **`$2a$` / `$2b$` / `$2y$` (bcrypt)** is for a password a **person chose**: it
  is deliberately slow, which is what makes a stolen file hard to crack. `--cost`
  sets the work factor (default 12).
- **`$sha256$`** is for a **generated** secret (`--generate`). Verification costs
  microseconds instead of a quarter second; against a 256-bit random secret there
  is nothing to guess, so the hashing cost buys nothing. Do not use it for a
  password a person invented.
- A line starting with `$` is always read as a hash. An **unrecognized** `$...$`
  format is a configuration error, never a literal password — a mistyped hash
  must not silently become the password itself.
- A line that does not start with `$` is a cleartext credential and may contain
  `#` and spaces. On a hashed line, anything after the hash is a comment.
- Consequently a **cleartext password cannot start with `$`** — in a file or in
  `web.password`. `sermod` refuses to start rather than guess (exit `78`); hash
  such a password instead.
- A single source holds at most **64** credentials: every failed attempt is
  checked against all of them.
- Verification results are cached briefly, so the bcrypt cost is paid once per
  credential rather than on every request.
- Credentials are read **when the daemon starts**, not on `sermoctl daemon
  reload`: editing the file takes effect on the next `sermod` restart. Adding the
  new credential before removing the old one is still what rotates without a cut.

Because a hash cannot be turned back into a password, `sermoctl` cannot send one.
It authenticates with the **runtime token** `sermod` writes to
`<paths.runtime>/web.token` (mode `0600`, removed when the daemon stops), which
grants admin access to whoever can read it — the same trust model as the control
socket. `sermoctl` resolves its credential in this order:

1. `$SERMO_WEB_PASSWORD` — needed for a remote daemon, or when running as a user
   that cannot read the token.
2. `<paths.runtime>/web.token`.
3. A cleartext password in the configuration.

The **password**, not the username, selects the role — at the browser prompt enter
any username and the admin or guest password; passwords are compared in constant
time. With `guest: true` the dashboard loads read-only without a prompt, and a
**"log in"** link (`GET /login`) triggers the prompt to escalate to admin. The UI
hides the action buttons for guests; the API enforces it regardless. When no
password/guest is set, auth is disabled (open) and the daemon **logs a warning**
at startup. `GET /api/whoami` reports the caller's role.

In **open mode** the server additionally validates the request `Host` header
(DNS-rebinding protection): `localhost`, IP-literal Hosts and the bind address
are accepted, any other DNS name is refused with `421`. A rebound hostile page
necessarily presents its own domain in `Host`, while direct browsing by IP or
localhost is unaffected. If an auth-less deployment sits behind a proxy that
forwards another hostname, list it explicitly:

```yaml
web:
  port: 9797
  allowed_hosts: [dashboard.internal]   # extra Host names accepted in open mode
```

With auth enabled the check does not apply — a rebound origin cannot attach
Basic credentials, and proxies keep whatever `Host` they use. Plain `/livez`
and `/readyz` probes are always exempt.

### Behind a reverse proxy (required to expose it)

The web server speaks **plain HTTP only** and binds to loopback by default. To
reach it from anything but the local host, **put it behind a reverse proxy**
(nginx, Apache, …) that terminates **TLS** — do **not** widen `web.address` to a
public interface. Keep Sermo on `127.0.0.1` and let the proxy be the only client:

```yaml
web:
  address: 127.0.0.1   # leave on loopback
  port: 9797
  password: "${env:SERMO_WEB_PASSWORD}"
```

**nginx** (TLS in front, proxy to loopback):

```nginx
server {
    listen 443 ssl;
    server_name sermo.example.com;
    ssl_certificate     /etc/ssl/certs/sermo.crt;
    ssl_certificate_key /etc/ssl/private/sermo.key;

    location / {
        proxy_pass         http://127.0.0.1:9797;
        proxy_set_header   Host $host;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_set_header   X-Forwarded-For $remote_addr;
    }
}
```

**Apache** (`mod_ssl` + `mod_proxy` + `mod_proxy_http`):

```apache
<VirtualHost *:443>
    ServerName sermo.example.com
    SSLEngine on
    SSLCertificateFile    /etc/ssl/certs/sermo.crt
    SSLCertificateKeyFile /etc/ssl/private/sermo.key

    ProxyPreserveHost On
    ProxyPass        / http://127.0.0.1:9797/
    ProxyPassReverse / http://127.0.0.1:9797/
</VirtualHost>
```

Notes:

- The proxy and the dashboard share an **origin**, so the `X-Sermo-Csrf` header and
  Sermo's own admin/guest auth keep working through it — the browser forwards the
  `Authorization` header. You can rely on Sermo's roles, add the proxy's own auth
  (basic/OIDC/mTLS) on top, or both.
- Redirect HTTP→HTTPS at the proxy and let it handle certificates (Sermo has no
  native TLS). Restrict access there too (allow-lists, SSO) if needed.
- Never publish port `9797` directly; only the proxy should connect to it.

Read-only endpoints:

- `GET /` — the dashboard.
- `GET /livez` — liveness, see below.
- `GET /readyz` — readiness, see below. The dashboard polls `/readyz?verbose` to
  show a **Starting** or **Shutting down** banner while monitoring is not active
  yet.
- `GET /api/whoami` — caller role, permissions and feature visibility.
- `GET /api/dashboard?since=24h` — aggregate snapshot used by the Web UI for
  frequently refreshed service, host and daemon panels. Its `generation` field
  and the `X-Sermo-Generation` header on dashboard data responses prevent the UI from
  combining data across a concurrent daemon reload. Individual endpoints below
  remain available and are used as a browser fallback.
- `GET /api/services` — **configured runtime** service list (the service
  files under `paths.services`): name, `state` (`disabled`, `stopped`,
  `started`, `starting`, `collecting`, `monitored`, `warning`, `failed`), backend status,
  `check_health`, `checks_failing`, `observability_ready`,
  `observability_missing`, active locks, monitor state/source/timestamp,
  backend, unit, cooldown, remediation state, next eligible action and last
  event. This is not `sermoctl services`, which lists catalog service profiles — see
  [cli.md](cli.md#catalog-inventory).
- `GET /api/services/{name}` — service detail: latest checks, rolling SLA, named
  runtime locks, discovered processes, automatic remediation policy state and
  rule window progress.
- `GET /api/sessions` — current SSH, tmux and screen sessions plus the state of
  each configured session source. Rows expose idle time plus process-tree CPU,
  resident memory and IO rates when attributable; tmux and screen inventory
  still comes from published check samples rather than an HTTP-time client run.
- `GET /api/services/{name}/sla?since=24h` — availability history at the
  resolution that window is stored at (see [Stored history
  resolution](#stored-history-resolution)); `since` is a duration, default 24h,
  capped at `engine.retention_1d` (default 366 days, ~1 year).
- `GET /api/services/{name}/metrics?check=NAME&since=24h` — check latency
  history + summary. Add `metric=KEY` for a named numeric metric published by
  that check, see below.
- `GET /api/services/{name}/runtime?since=24h` — service process tree CPU,
  memory and IO history. Its current value is the latest worker-published
  sample (with its original `at` timestamp); API reads never trigger another
  process discovery pass.
- `GET /api/services/{name}/events?limit=N` — events for one service.
- `GET /api/watches` — host-level and service-scoped watches, their `scope`,
  monitor state, conditions, notifications, live readings when available and
  recent activity. Service-scoped names use `service:watch`.
- `GET /api/notifiers` — configured notifier targets.
- `POST /api/notifiers/{name}/test` — sends an explicit test message through one enabled notifier.
- `GET /api/applications` — installed catalog applications.
- `GET /api/libraries` — installed catalog libraries.
- `GET /api/daemon` — daemon/backend/runtime settings and host uptime.
- `GET /api/daemon/metrics?since=24h` — persistent sermod CPU, memory and IO
  history for the current daemon process, plus current PID, file descriptors and
  threads.
- `GET /api/host` — current host-level CPU, memory and load metrics.
- `GET /api/locks` — named runtime locks with TTL, owner status, age, blocked
  actions and release eligibility.
- `GET /api/activity` — recent activity summary used by the dashboard header.
- `GET /api/monitoring` — monitoring-enabled vs paused counts for non-disabled
  services.
- `GET /api/events?limit=N` — global event feed as `{events, next_before_id,
  has_more}`, newest first. Optional filters: `service`, `watch`, `kind`, `status`
  and `only_errors=1`. Pass `before_id` from a response to continue toward older
  rows. Cursor pages also accept a positive `since` duration such as `24h`.

State-changing endpoints are CSRF-protected for every non-GET/HEAD request and
require admin permissions when auth is enabled:

Target-scoped requests (service, watch, mount, notifier, lock and preflight)
must also send `X-Sermo-Generation` with the generation most recently returned
by the dashboard data API. The daemon returns `428` when it is missing and
`412` when a reload has made it stale; fetch a fresh dashboard snapshot before
retrying. The daemon pins that generation until the request finishes, so an
accepted operation cannot switch targets during a concurrent reload.

- `POST /api/services/{name}/preflight` — run the same preflight checks as
  `sermoctl preflight SERVICE`, without starting or stopping anything.
- `POST /api/services/{name}/{action}` — service action. `action` is `monitor`,
  `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume` or `repair`;
  start/stop/restart/reload/resume go through the safe operation engine. `repair`
  is manual-only: it accepts only a failed/inactive service, removes a regular
  pidfile under `/run` only after confirming its PID is dead, resets a failed
  init-unit marker through the backend manager, then uses the same guarded start
  path. It refuses live PIDs, symlinks, malformed pidfiles and paths outside
  `/run`.
- `POST /api/services/{name}/reap` — signal the service's stray processes, gated
  by its own `reap.kill_only_if`: with none declared the reply reports every stray
  and nothing is signalled. Equivalent to `sermoctl reap SERVICE --apply`. It changes
  no unit state, so it neither pauses monitoring nor opens a settling window.
- `POST /api/services/{name}/sessions/{pid}/close?start_ticks=TICKS&terminal=PTS`
  — close one freshly revalidated SSH terminal with `SIGTERM`.
- `POST /api/services/{name}/terminal-sessions/{check}/close?multiplexer=TYPE&session=NAME&user=USER&identity=IDENTITY`
  — close one freshly revalidated tmux or screen session through its configured
  client. The operation requires the exact published generation identity and
  shares the service lock, guards, timeout and event path.
- `POST /api/services/{name}/terminal-sessions/{check}/close-empty`
  — close one freshly revalidated empty tmux server with an explicit configured
  socket. The server must still have zero sessions; Sermo invokes tmux's exact
  `kill-server` argv, verifies the namespace has disappeared and removes only
  an unchanged stale socket left by tmux.
- `POST /api/watches/{name}/{action}` — watch action. `action` is
  `monitor`, `unmonitor`, `expand`, `probe`, `pause` or `resume`. `probe` is
  read-only and is available for diskio, LVM, RAID, SMART and hdparm watches. `pause`/`resume`
  require the RAID control block below.
- `POST /api/locks/{service}/release?name=NAME` — release an inactive
  stale/expired named runtime lock; active locks are refused.
- `POST /api/events/clear?before=TIME` — clear the persisted event/activity log;
  `before` may be a positive duration or a non-future RFC3339 timestamp. Omit it
  to clear all events.
- `POST /api/state/compact?before=TIME` — consolidate and prune stored history to
  the configured retention, then vacuum the state database. `before` optionally
  drops whatever history remains older than an explicit cutoff. Matches
  `sermoctl state compact`.
- `POST /api/reload` — request a `sermod` configuration reload, equivalent to
  `sermoctl daemon reload`.

### Liveness (`/livez`)

`GET /livez` is a liveness probe for the daemon: if its web server answers, the
process is alive, so it always returns **200**. A plain request returns
`text/plain` body `ok`; this plain probe is served **without authentication** so a
monitor, load balancer, container orchestrator or reverse proxy can probe it with
no credentials. `GET /livez?verbose` returns JSON with `status`, `uptime` (and
`uptime_seconds`), `started_at`, `now`, the number of `services`, and the Go
runtime version; when web auth is configured, the verbose form follows normal
read authentication like the dashboard:

```sh
curl -fsS http://127.0.0.1:9797/livez            # -> ok
curl -fsS -u admin:secret http://127.0.0.1:9797/livez?verbose
```

It reports process liveness only; for configuration/host/database health use
[diagnostics](#diagnostics).

### Readiness (`/readyz`)

`GET /readyz` is a readiness probe: it returns **200** only after `sermod` has
finished `engine.startup_delay` (if any) **and every monitored target —
services, host watches and installed apps — has completed its first cycle**, so
the daemon actually has data rather than merely having launched. While settling,
the verbose `message` reports progress (`starting: 3/10 monitored targets have
reported`) and the web UI header shows `status: starting` with a neutral grey
tab favicon. Each monitored service, host watch and installed app also reports
`state: starting` until its first observation cycle has completed, except a
service with fresh trusted process evidence may report `state: active` (process
confirmed, not check health). Services still waiting for an `active` init
backend complete settling on the first status probe (they surface as `failed`
while inactive); checks and remediation wait until the backend is active.
If the backend explicitly reports `failed` but Sermo has fresh evidence of an
exact live process and every functional check passes, the aggregated service
state is `warning`, not `failed`. The API still exposes `status: failed`: this
is a workload that remains available while its init unit needs operator repair,
and service operations continue to respect that backend state.

Only **installed** catalog applications with an active app-monitor participate
in that settling registry; catalog entries whose binary is not present are
omitted from `GET /api/applications` and never show `starting`. During settling,
installed apps may appear with `state: starting` before their first app-watch
cycle completes; during that window Sermo does not run service checks (while the
backend is still inactive), and it suppresses alerts, hooks, notifications and
automatic remediation on the first active observation cycle.

During the startup grace period, the first-cycle settling, or while the daemon
is shutting down, it returns **503**. To avoid a startup stampede the first
cycle of the whole fleet is staggered across one `engine.interval` (the slow
per-app cadence is used only after that first check); a **config reload does not
re-gate** `/readyz` — the daemon stays `ready` and the web header/favicon do not
return to the grey `starting` state. Newly added or changed monitored targets
can still report `state: starting` individually until their first observation
cycle completes.

A plain request returns `ok` or `starting` / `shutting_down` as `text/plain`;
`GET /readyz?verbose` returns JSON with `ready`, `status`, `backend`,
`services`, `watches` (host watches plus installed-app monitors) and an optional
`message`. Like `/livez`, only the plain probe is public; the verbose form
follows normal read authentication when web auth is configured:

```sh
curl -fsS http://127.0.0.1:9797/readyz                 # -> ok (when monitoring)
curl -fsS -u admin:secret http://127.0.0.1:9797/readyz?verbose
```

Use `/livez` to know the process is alive; use `/readyz` before sending traffic or
to gate a reverse proxy until monitoring has actually begun.

A **monitored service whose init backend stays inactive** (for example a unit you
intentionally keep stopped) completes startup observation on the first status
probe: it reports `state: failed` and no longer blocks `/readyz`. Sermo still
defers service checks and automatic remediation until that unit becomes active.
Service workers, host watches and installed-app monitors use separate settling
keys, so a service and a catalog app that share a name (for example `redis`)
both count toward readiness independently.

Service operations use the same observation-only settling after startup:
`start`, `restart`, `reload` and `resume` from automatic remediation, the web UI
or `sermoctl` suppress service alerts, notifications, automatic remediation and
SLA samples until the operation has finished and the worker has observed one
active cycle with fresh data. `stop` suppresses cycles while the operation is
running; a successful manual stop then pauses monitoring as described below.
This per-service settling does not re-gate `/readyz`.

Events are the daemon's activity — actions, alerts, suppressions, hook/notify
results and errors — kept in the persistent state store and mirrored to the
daemon log. `limit` defaults to 100 (max 1000). The dashboard shows a global
feed; a service's detail shows its own events.

The detail's check results are the **latest observed** by the worker (published
each cycle), so they cost nothing to view and reflect each check's own cadence
(see [per-check interval](#per-check-interval)); they are rehydrated from
`paths.state` after a daemon restart, and a check not run yet shows "not run
yet". A service result is used only through its normal freshness window (two
effective check intervals, with a 30-second minimum) and while it still matches
the configured check type; otherwise the detail marks it stale and waits for a
new cycle rather than showing old values.
Host-watch readings use the same persisted latest-observed path, with stale
samples hidden after their normal freshness window. The Graphs section uses one
window selector for SLA and runtime
measurements. Its SLA timeline comes from the same data as `sermoctl sla`: it
plots the per-minute samples over the selected window (1h/24h/7d/30d/1y), marks
each degraded minute as an incident at its local time, and leaves gaps where the
service was unmonitored.

### Latency graph

For each `tcp`, `ports`, `http` and `service` check, the daemon records the
check's **latency** (milliseconds) every observed cycle — the same idea as the
`icmp` latency metric — and the service detail draws a **latency graph** for the
selected check. A window selector covers the **last hour, day, week, month and
year**, and for the chosen period the panel shows the **average, minimum and
maximum** plus a line (average over time) with a min–max band. The data is at
`GET /api/services/{name}/metrics?check=NAME&since=DURATION` as `{summary:{count,
avg,min,max}, points:[{start,n,avg,min,max}], unit:"ms"}`. Add `metric=KEY` to
read a named numeric metric for checks that publish one, such as `hdparm`
`read`/`cached`, `sensors` `temp`/`fan`/`voltage`, `smart` `temperature`/`wear`
or `edac` `ce`/`ue`; in that case `unit` is the metric's unit instead of `ms`.
Measurements follow the same archive ladder as the SLA samples ([Stored history
resolution](#stored-history-resolution)), so the minimum and maximum of a spike
survive at every resolution; a check that only runs every N cycles ([per-check
interval](#per-check-interval)) records a sample only when it actually runs, so
the average is not skewed.

The `Daemon / Engine settings` process charts use the same persistent state
database for sermod's own CPU, memory and IO history, so those graphs survive a
daemon restart or host reboot. They follow the same archive ladder as every other
stored series.

The service detail's CPU, memory and IO charts use the same persistent state
database for each service process tree, so those graphs also survive a daemon
restart or host reboot. They start filling as soon as the service is monitored;
CPU and IO rates need two cycles before the first rate point exists, while
memory can render from the first process sample. Runtime metric buckets follow the
same archive ladder as every other stored series. Services that declare an
empty `processes: { }` map have no resident process tree; the dashboard omits
their process table and latency/CPU/memory/IO charts.

Web-triggered monitor changes are recorded with source `web` in the state store;
manual stops from the web UI or CLI use `web-manual-stop` / `cli-manual-stop`
until a later successful start restores the previous monitored state. A
successful storage `umount` pauses that storage watch with `web-mount-umount` or
`cli-mount-umount`; a later successful `mount` restores it only when that umount
created the pause.

The dashboard and `GET /api/services` / `GET /api/watches` expose `state`,
`monitored`, `monitor_source` and `monitor_changed_at` separately. A service can
show `started` while its backend is active but monitoring is paused,
`collecting` while monitoring is active but runtime/check/SLA indicators are
still filling, `warning` when the only indicator left is one that will not
arrive — the unit is active and its checks pass, but a completed daemon cycle
attributed no process to it, so `observability_missing` reports `service
processes` — and `monitored` only once those indicators are ready. Host
watches do not have service-manager `started` or `stopped` states; their `state`
is `disabled` when configuration or monitor state excludes them from active
checks, `starting` before the first monitored sample, `failed` for an active
failing condition, and `ok` otherwise. Their separate monitor flag is still
exposed for actions and metadata.

Operations take the per-service operation lock, so they never overlap a worker's
action on the same service. The state store also carries a short-lived
operation-settling marker so `sermoctl`-initiated actions and web actions both
hold back service alerts until the daemon has a post-operation sample.

Because the daemon runs as root, the UI is hardened: it binds to loopback by
default, supports auth (above), sets HTTP timeouts, and requires an
**`X-Sermo-Csrf`** header on every action (POST) request — the dashboard sends it;
an API client must too (e.g. `curl -H 'X-Sermo-Csrf: 1' -X POST …`). This blocks
cross-site request forgery from a browser. The header's value is not a secret:
the protection is the OWASP custom-request-header pattern — browsers refuse to
attach custom headers to cross-site requests without a CORS preflight, and the
server never answers preflights. That guarantee holds only while the server
sends no `Access-Control-Allow-Origin` header; do not add permissive CORS. See
[safety](safety.md#trust-model).

## Availability (SLA)

The daemon records one availability sample per monitoring cycle per service, so
you can see how often each service has been healthy over time. No configuration
is needed — it is on for every monitored service.

A service is **available** in a cycle when none of its **required** checks
failed. Optional checks (warnings) do not affect it, and a service with no
required checks is always available. Health-style checks (`tcp`, `http`,
`service`, `process`, `cert`, `firewall_rules`, etc.) fail when `OK=false`;
condition-style checks (`fds`, `oom`, resource thresholds, etc.) fail only when
the alerting condition fires. Samples are accumulated into per-minute buckets in
the state DB (`/var/lib/sermo/sermo.db`); the daemon prunes buckets older than a
year on startup.

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

Each point is one monitored minute; **unmonitored minutes are absent**
(gaps), so a graph can render an excluded period (Sermo down, or the service
paused/disabled) distinctly from real downtime. The web dashboard uses the same
points to place incident markers at the minute the problem was observed.

## Notifications

`notifiers` are named, typed delivery targets that a watch can send to when it
fires, as an alternative or complement to running a local hook. They are global
daemon configuration; they never merge into a service. Each notifier has a
**name** (the map key) referenced from a watch's `then.notify` list, so different
watches can notify different targets.

Notifier fragments live under directories listed in `paths.notifiers` (commonly
`/etc/sermo/notifiers`). Each file contains exactly one notifier under the
top-level `notifiers:` map:

```yaml
# /etc/sermo/notifiers/ops-email.yml
notifiers:
  ops-email:                 # the name referenced by then.notify
    enabled: true             # optional; defaults to true
    type: email
    template: default-alert    # optional; loads /etc/sermo/templates/default-alert.yml
    dsn: "smtp://user:pass@smtp.example.com:587"   # smtp:// (STARTTLS) or smtps:// (implicit TLS)
    from: "Sermo <sermo@example.com>"
    to: [ops@example.com, oncall@example.com]       # one or more recipients
```

Notifier types:

- **`email`** — sends over SMTP.
  - **`dsn`** — `smtp://[user:pass@]host[:port]` (STARTTLS when offered; default
    port 587) or `smtps://…` (implicit TLS; default port 465). Credentials, when
    present, are only sent over an encrypted connection.
  - **`from`** — the sender address (a bare `addr` or `Name <addr>`).
  - **`to`** — one or more recipient addresses.
- **`gotify`** — pushes to a **[Gotify](https://gotify.net) server**
  (self-hosted push with its own mobile app).
  - **`webhook`** — the server base URL (`https://push.example.net`).
  - **`token`** — the Gotify application token, sent as the `X-Gotify-Key`
    header so it never appears in a URL. The subject travels as the title and
    the detail (the `SERMO_*` fields) as the message.

```yaml
# /etc/sermo/notifiers/gotify.yml
notifiers:
  push:
    type: gotify
    webhook: "https://push.example.net"
    token: "A.XXXXXXXX"
```

- **`ntfy`** — publishes to an **[ntfy](https://ntfy.sh) topic** (self-hosted or
  ntfy.sh): push notifications on phone and desktop with no external
  dependency.
  - **`webhook`** — the full topic URL (`https://ntfy.example.net/sermo-alerts`).
    A reverse-proxy subpath is kept (`https://host/ntfy/alerts` publishes to
    `https://host/ntfy` with topic `alerts`). The subject travels as the
    notification title and the detail (the
    `SERMO_*` fields) as the message.
  - **`token`** — optional access token for a protected topic (sent as an
    `Authorization: Bearer` header). The dashboard shows only the server host;
    the topic name stays private.

```yaml
# /etc/sermo/notifiers/push.yml
notifiers:
  push:
    type: ntfy
    webhook: "https://ntfy.example.net/sermo-alerts"
    token: "tk_XXXXXXXX"   # optional
```

- **`slack`** — posts to a Slack **incoming webhook**.
  - **`webhook`** — the incoming-webhook URL (`https://hooks.slack.com/services/…`).
    The notification's subject is the lead line and its detail (the `SERMO_*`
    fields) follows in a code block.

```yaml
# /etc/sermo/notifiers/team-slack.yml
notifiers:
  team-slack:
    type: slack
    webhook: "https://hooks.slack.com/services/T0000/B0000/XXXXXXXX"
```

- **`teams`** — posts to a Microsoft Teams **incoming webhook** (a Teams
  Workflows / Power Automate "when a webhook request is received" URL).
  - **`webhook`** — the workflow's HTTP POST URL. The notification is sent as
    an Adaptive Card: the subject as the bold lead line, the detail (the
    `SERMO_*` fields) in a monospace block.

```yaml
# /etc/sermo/notifiers/ops-teams.yml
notifiers:
  ops-teams:
    type: teams
    webhook: "https://prod-01.westeurope.logic.azure.com:443/workflows/…"
```

- **`telegram`** — sends through a **Telegram bot** (`sendMessage`).
  - **`token`** — the bot token from `@BotFather`. It stays inside the API URL
    and never appears on the dashboard. Prefer `${env:...}`; when it resolves
    empty (the variable is unset) the notifier stays inactive instead of failing
    config load.
  - **`chat_id`** — the numeric chat/group id or an `@channel` name. The
    subject is the lead line and the detail (the `SERMO_*` fields) follows as
    plain text.
  - **`parse_mode`** *(optional)* — `MarkdownV2`, `Markdown` or `HTML` to render
    the message as formatted text (bold, code, links) instead of plain text. Omit
    for plain text.
  - **`silent`** *(optional)* — `true` delivers the message quietly, with no
    sound or vibration (Bot API `disable_notification`).
  - **`message_thread_id`** *(optional)* — an integer forum-topic id, to post
    into a specific topic within a group.

```yaml
# /etc/sermo/notifiers/telegram.yml
notifiers:
  ops-telegram:
    type: telegram
    token: "123456789:AAF...XXXX"
    chat_id: -1001234567890
    # parse_mode: MarkdownV2       # optional: format the message text
    # silent: true                 # optional: deliver without sound
    # message_thread_id: 42        # optional: post into a forum topic
```

- **`tty`** — writes directly to active Linux terminal sessions, similar to
  `write(1)` but implemented inside Sermo without invoking an external command.
  The built-in notifier named `tty` is always available and can be overridden to
  target specific users:

```yaml
notify: [tty]      # optional global default: notify logged-in terminal users
```

  To customize or disable it, define a normal notifier with the same name:

```yaml
# /etc/sermo/notifiers/tty.yml
notifiers:
  tty:
    type: tty
    users: [root, deploy]   # optional; omit to target every active terminal
```

  The `tty` notifier reads `/run/utmp` (falling back to `/var/run/utmp`) and
  writes to the corresponding `/dev/<tty>` device with non-blocking native Go
  I/O. It respects terminal permissions such as `mesg n`; if the daemon user
  cannot write a terminal, delivery to that terminal fails and Sermo records a
  `notify-failed` event.

- **`wall`** — broadcasts to every active Linux terminal session using the same
  native Go utmp/TTY implementation as `tty`, but with no user filter. The
  built-in notifier named `wall` is always available:

```yaml
notify: [wall]     # broadcast to every logged-in terminal session
```

  `wall` intentionally has no `users` option; use `tty` when you need to target
  only selected users.

The supported notifier types today are `email`, `ntfy`, `gotify`, `telegram`,
`slack`, `teams`, `tty` and `wall`.

Set **`enabled: false`** on any notifier to keep it defined but skip delivery.
Disabled notifiers may still be referenced by `notify` selections.

Use `sermoctl notifier test NAME` to send a clearly marked test message through
one enabled notifier. The Notifiers panel offers the same action to WebUI
administrators. Both paths use the configured delivery target and timeout, do
not trigger watches, hooks or remediation, and reject disabled notifiers.

`sermoctl services --notify NAME[,NAME]` sends an ad-hoc services inventory
report through configured notifiers. Email notifiers receive a multipart
plain-text/HTML message with summary cards and a service table; Slack and Teams
receive the text fallback, and terminal notifiers write the text report to
logged-in TTY sessions. `--notify all` targets every enabled notifier, including
the built-in `tty` and `wall` notifiers unless they have been explicitly
disabled. When a notify selection contains both `tty` and `wall`, Sermo sends
only `wall` because it already covers every active terminal. The CLI renders
this report directly; notifier templates are not used.

`none` is a **reserved keyword** and cannot be used as a notifier name.

### Notification templates

Any notifier can opt into a named template with `template: <name>`. Names map to
`<paths.templates>/<name>.yml`, so `template: default-alert` loads
`/etc/sermo/templates/default-alert.yml` by default. The install target ships
that template as a neutral baseline:

```yaml
subject: "{{ .Subject }}"
body: |
  {{ .Body }}
```

Templates are Go `text/template` files wrapped in YAML with optional `subject`
and `body` keys. If either key is omitted, Sermo keeps the original generated
subject or body for that part. The available data is:

- **`.Subject`** — the subject generated by Sermo.
- **`.Body`** — the body generated by Sermo.
- **`.Field "SERMO_SERVICE"`** — a structured context field by name; missing
  fields render as an empty string.
- **`.SortedFields`** — all structured fields as stable `{Name, Value}` entries,
  useful for `range`.

A principal-process restart notice supplies the structured fields
`SERMO_RESTART_SERVICE`, `SERMO_RESTART_UNIT`, `SERMO_RESTART_PROCESS`,
`SERMO_RESTART_PID`, `SERMO_RESTART_UPTIME`, `SERMO_RESTART_UPTIME_SECONDS`,
`SERMO_RESTART_STARTED_AT`, and `SERMO_RESTART_UPTIME_BELOW`; it also sets the
usual `SERMO_SERVICE`, `SERMO_RULE` and `SERMO_EVENT` fields.

Example:

```yaml
subject: '[{{ .Field "SERMO_SERVICE" }}] {{ .Subject }}'
body: |
  {{ .Body }}

  Context:
  {{ range .SortedFields }}{{ .Name }}={{ .Value }}
  {{ end }}
```

Template names may contain letters, digits, `_`, `-` and `.`, but no path
separators or `..`. Sermo validates referenced template files when the
configuration is loaded; a missing or invalid template is reported as a config
issue, and the affected notifier is skipped by `sermod`.

### Default selection and precedence

A top-level **`notify`** key sets the default notifiers that apply to every notify
site (a watch's `then.notify` and a rule's `notify`) — so you configure routing
once instead of repeating it on every watch and rule:

```yaml
notify: [ops-email]      # default for every site that declares none of its own
# notify: none           # (or omit the key) for no default
```

Each site then **overrides** the default — the per-site choice always wins:

- an explicit list (`notify: [team-slack]`) replaces the default for that site;
- `notify: none` suppresses delivery for that site — valid **anywhere a notify
  selection is**, with or without a global default configured. A watch whose
  only action is `notify: [none]` (inside an explicit `then`) is a deliberate
  *monitor-only* watch: it still runs, shows its state in the dashboard and
  records events, it just never delivers;
- omitting `notify` (inside an explicit `then`) inherits the global default.

`none` cannot be combined with notifier names in the same list. Omitting the
entire `then` key on a watch (or per-metric) is another way to get pure
alert-only behaviour (firing state + events in the UI and log, but no actions
and no inheritance of globals). See the host watches section below for the
bare `check` + `for` example.

## Telegram report bot

The optional top-level **`telegram_bot`** section runs an interactive,
**read-only** Telegram bot inside `sermod`. Where a `telegram` notifier *pushes*
alerts, the bot lets an operator *ask* for reports on demand: send it `/status`
and it replies with the current fleet summary. It can never change the host — it
only reads the same state the web dashboard serves.

It receives commands over Bot API **long polling** (`getUpdates`), so it needs
no inbound port, no public exposure and no reverse proxy — matching Sermo's
outbound-only posture. The bot token stays inside the API URL and is scrubbed
from logs and errors, exactly like the `telegram` notifier.

```yaml
# /etc/sermo/sermo.yml (or a drop-in fragment)
telegram_bot:
  token: "${env:TELEGRAM_BOT_TOKEN}"   # bot token from @BotFather
  allowed_chats:                       # required: only these chats are answered
    - 123456789
    - -1001234567890
  # poll_interval: 30s                 # optional getUpdates long-poll timeout
  # enabled: false                     # optional; omitted => enabled
```

- **`token`** — the bot token from `@BotFather` (used for both `getUpdates` and
  replies). Prefer `${env:...}` so it is not written in a file. Optional: when it
  resolves empty (the env var is unset) the bot simply stays inactive and the
  rest of the config still loads.
- **`allowed_chats`** — the list of numeric chat ids the bot answers. A message
  from any other chat is ignored, never answered. This is the access control:
  keep it to the operators/groups that may query the daemon.
- **`poll_interval`** *(optional)* — the long-poll timeout, default `30s`,
  clamped to `1s`–`10m`.
- **`enabled`** *(optional)* — set `false` to keep the section but stop polling.

Commands (all read-only):

| Command | Reply |
| --- | --- |
| `/status` | fleet summary: services ok/failing, monitored/paused, recent errors, host uptime |
| `/services [name]` | the service list, or one named service's state and health |
| `/watches` | host and service watch states |
| `/sla <service>` | availability windows (hour…year) for a service |
| `/events [count]` | the most recent events (default 10, max 50) |
| `/help` | the command list |

Reloading (`sermoctl daemon reload` / `SIGHUP`) applies changes to `token`,
`allowed_chats` and `poll_interval` without a restart. Because the goroutine is
only started at boot when the section is present, **enabling the bot for the
first time requires a restart** (the same rule the web UI follows for its port).
On startup the bot discards any commands queued while it was down, so a restart
never replays old requests.

## Host watches

`watches` monitor host-level resources independently of any service and run a
**hook** (a local command) and/or send **notifications** (to named `notifiers`)
when a threshold is crossed. They are daemon configuration; a global `watches`
document never merges into a service. (A service can also declare its own
`watches:` block scoped to that service — see
[Service watches](#service-watches-scoped-to-a-service) below.)

> **Tip — generate configuration interactively.** `sermoctl wizard` can write
> three different surfaces. The storage assistant (`volume`) prints storage
> watch documents and writes one file per target under `/etc/sermo/storages`.
> Watch assistants (`net`, `uplink`) print watch document previews and, if
> accepted, write one watch per file under a watch-type directory such as
> `/etc/sermo/networks` or `/etc/sermo/watches`; the wizard adds that directory
> to `paths.watches` (writing a `.bak` first).
> Service assistants (`service`, `docker`, `vm`) write
> one service file per target under `services/` and ensure that
> `paths.services` loads it; `docker` and `vm` add `control.type: docker` or
> `control.type: libvirt` plus matching check-only watches. The mount assistant
> (`mount`) lists `/etc/fstab` mount points and writes safe storage watch files
> with a `mount:` block under `/etc/sermo/mounts`; it does not mount or unmount
> while generating config.
>
> `sermoctl wizard volume` creates storage checks for mounted local and
> network/distributed filesystems (threshold as a percent or size, optional
> auto-expand for LVM-backed filesystems), excluding pseudo/control filesystems
> such as `rpc_pipefs`. `sermoctl wizard net` covers interface state, errors,
> speed and address; type `active` to pick currently up non-loopback
> interfaces. `sermoctl wizard uplink` generates the layered internet-uplink set
> for an interface: link state, assigned address, default route, bound ping and
> DNS resolution through the system resolver; type `default` to use the detected
> default-route interface.
> `sermoctl wizard service` detects installed catalog services and enables them
> with service files (see [services](services.md)); when several services
> are selected, port overrides are skipped unless explicitly reviewed, and known
> config files can be added as a periodic `watches.config-files` check-only entry
> with a default `60m` interval. Run with no argument to choose from the list.
>
> On finishing, the wizard offers to delete managed files whose target is no
> longer detected from the current generated output directories. New assistant
> types can be added over time. At any multi-select prompt you can
> type item numbers (`1,3`), the keyword `all`, or an option's name. When asked
> for notification targets the numbered list shows only the notifiers defined in
> the config; the reserved answers `all` / `none` / `default` are offered in the
> question itself — even when the config defines no notifiers: type `all` to
> notify every configured notifier, `default` to inherit the global default, or
> `none` to generate `notify: [none]` and suppress delivery. `none` and
> `default` are always accepted. When `default` has nothing to inherit (no
> global `notify` configured) it degrades to a monitor-only watch
> (`notify: [none]`) with a one-line note — it never re-asks or aborts. The
> wizard asks monitored entries for monitor state (`enabled`/`disabled`/
> `previous`) and an optional check interval; mount watch files generated only
> for mount operations are not monitored entries, so the mount assistant skips those questions. See
> [wizards](wizards.md) for the full flow.

A watch's `then` block (when present) declares the actions taken when it
fires — a `hook`, a `notify` list, an `expand` (storage only), a `kill`
(process only), or any combination.

**Omitting `then` entirely** is supported and means *alert-only / monitor-only*:
the `check` + `for` (or per-metric conditions) are still evaluated; when the
window is satisfied the watch emits a `firing` event (visible in the web UI
Alerts/Watches tiles, "failed" state badge, failed filter, and in the event
log under the watch expansion). When a previously firing watch clears, it emits
`recovered` and the watch returns to `ok`. No hook is executed and no
notifications are delivered (global `notify:` defaults are **not** inherited
for bare watches).

```yaml
watches:
  memory:
    monitor: disabled
    interval: 30s
    check:
      type: memory
      used_pct: { op: ">=", value: "90%" }
    for: { cycles: 3 }
    # no then: alert-only (web + events only; no notify/hook even if globals exist)
```

If you want actions, write an explicit `then:` block. Inside it, omitting the
`notify` sub-key inherits the global default (or you can list names, or use
`notify: [none]` to opt out while still declaring e.g. a hook).

Use `notify: [none]` (in an explicit `then`) to suppress notifications: alongside
another action (for example `expand`), or on its own as a monitor-only watch
(state and events without delivery). It is always valid, whether or not a
global `notify` default is configured.

**Notification cadence.** A firing watch delivers its `notify` **once**, on the
rising edge — when the alert starts. It does not re-notify every cycle while the
condition persists, and by default the `firing` event is also recorded only on
that edge. The `hook` still runs each firing cycle. When the watch clears and
later fires again, the next episode notifies afresh. To get a periodic
**reminder** while a watch stays firing, set `then.notify_interval` to a
positive duration: the notification is re-sent once that interval elapses. It
only affects delivery, so it requires `notify` targets. Both the edge-triggered
default and `notify_interval` apply to storage watches, the standard
single-shot service checks, and the `net`/`icmp`/`swap` metric watches. The
`file` and `process` watches have their own notification model — one event per
changed path or matching pid — and ignore `notify_interval`. A `process_policy`
watch records an alert for every sample whose PID lacks a readable start time,
because it cannot safely identify that PID incarnation; if configured,
`notify_interval` still paces notifier delivery for those alerts.

**Event/notification emission.** Automatic `firing`/`alert` events and their
notifications default to `on_change`: emit when a watch or rule enters a firing
episode, then emit `recovered` when it clears. Set top-level `emission` to
restore per-cycle output globally, or override only a specific rule/watch with
its own `emission:` block:

Watch episodes, `for`/`within` progress, reminder timing and automatic-action
pacing survive daemon restarts; an unchanged active condition is not emitted or
notified again merely because `sermod` restarted.

```yaml
emission:
  events: on_change    # on_change | every_cycle
  notify: on_change    # on_change | every_cycle

watches:
  storage-root:
    emission: { events: every_cycle }
    # ...

rules:
  warn-down:
    emission: { notify: every_cycle }
    # ...
```

`emission` is valid only globally, under `rules.*`, and under `watches.*`; a
service does not have a service-wide emission override. Real operation result
events remain audit events and are recorded whenever an operation is attempted.

`emission.events` also governs the `error` events a worker emits for failures it
cannot act on: a rule condition that cannot be evaluated at all (a version probe
whose binary refuses to run), a guard that cannot be evaluated while its rule
keeps firing, and a failing state store (a full disk). Under `on_change` each is
reported when it appears and again whenever its message changes or it returns
after a clean cycle, not once per cycle for as long as the host stays broken.
Store-level errors belong to no rule and follow the global policy.

```yaml
# /etc/sermo/storages/storage-root.yml
name: storage-root
monitor: previous
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" }
for: { cycles: 3 }
then:
  notify: [ops-email]
  notify_interval: 30m     # re-notify every 30m while still firing
```

Use `dry_run: true` when you want automatic actions wired for a trial run, but
you do not want non-console side effects yet. It is available in `defaults`, in
each service, and in each watch entry. A target-level
setting overrides `defaults.dry_run`.

Dry-run applies only to automatic actions driven by monitoring/rules:

- service remediation operations (`start`, `stop`, `restart`, `reload`,
  `resume`) are evaluated but not executed;
- service-owned `version.on_change` / `config.on_change` monitors inherit the
  service's `dry_run` flag, so their non-console notifications are suppressed;
- watch actions (`hook`, `expand`, `kill`, `makestep`) are evaluated but not executed;
- notifications are suppressed except `wall`, which is still delivered for local
  console visibility.

Manual operator actions are not dry-run gated: CLI/Web start, stop, restart,
reload, resume, monitor/unmonitor, mount/umount and other explicit operations
still execute normally.

A dry-run watch still runs its check and window, emits the normal `firing` event
when it would fire, then emits a `dry-run` event describing the actions it would
run. If an expansion or remediation would currently be blocked by policy, the
`dry-run` event reports the suppression, but dry-run does not advance
cooldown/backoff state.

```yaml
defaults:
  dry_run: true

name: apache-main
uses: apache
dry_run: false  # override the global default for this service
rules:
  restart-http:
    type: remediation
    if: { failed: { check: http } }
    then: { action: restart }

watches:
  load:
    monitor: previous
    dry_run: true
    check:
      type: load
      per_cpu: true
      load5: { op: ">", value: 1.5 }
    for: { cycles: 3 }
    then:
      hook: { command: [/usr/local/bin/sermo-load-alert.sh] }
      notify: [ops-email]
```

Use `dry_run` for host watches while you are proving thresholds, hook argv/env,
notifier routing or `then.expand` / `then.kill` / `then.makestep` policy gating. Remove it when
automatic actions should actually execute. If you only want a long-term
dashboard/log signal, omit `then` entirely or use `notify: [none]`; those are
monitor-only configurations, not action rehearsals.

A watch supports the same top-level `monitor` flag as a service/daemon:
`enabled` (the default) forces monitoring on at daemon start/reload, `disabled`
builds the watch but starts it paused, and `previous` restores the last persisted
runtime state. This is distinct from `enabled: false`, which disables the watch
entry structurally and no runtime watch is built. Use `monitor: disabled` when
you want the watch visible in the web UI and available for an admin to resume
with **monitor**.

Storage, network and generic host monitors all live in watch documents loaded
from `paths.watches`. The directory name is just classification for operators:
`storages/`, `networks/`, `mounts/` and `watches/` are all normal watch dirs
when listed under `paths.watches`. A watch document is a normal YAML file with
top-level `name`, optional `display_name` / `category`, and the watch fields:

```yaml
# /etc/sermo/storages/storage-root.yml
name: storage-root
category: storage
monitor: previous
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" }
then:
  notify: [ops-email]
```

```yaml
# /etc/sermo/watches/memory.yml
name: memory
category: host
monitor: previous
check: { type: memory, used_pct: { op: ">=", value: "90%" } }
then:
  notify: [ops-email]
```

Keeping wizard output in separate files makes it easy to remove or review one
target without rewriting the whole global config. Notifier fragments follow the
same one-entry rule under a top-level `notifiers:` map in `paths.notifiers`.
The compact reference examples below still use global `watches:` maps; when you
store the same watch under a directory listed in `paths.watches`, move the entry
name to top-level `name:` and keep the inner fields at the top level.

These conventions keep the per-type sections below short:

- **Hook environment.** Every watch hook receives `SERMO_WATCH` (the watch name),
  `SERMO_CHECK_TYPE`, `SERMO_VALUE` (the breaching reading) and `SERMO_MESSAGE`,
  plus **every key the check puts in its result `Data`, exported as
  `SERMO_<UPPER_KEY>`** (non-alphanumeric characters become `_`). Each type lists
  only its notable extra keys as *Hook extras*.
- **Hook outcome.** A hook can assert what its command returned. By default a
  non-zero exit makes the hook fail (a `hook-failed` event); set `expect_exit`
  to treat another code, or a list of codes such as `[0, 1]`, as success.
  `expect_stdout` / `expect_stderr` additionally check the captured output — a
  plain string requires that substring, or an `{op, value}` mapping compares the
  trimmed output with the same operators as an http check's `expect_body`
  (`== != > >= < <= contains =~`). A failed assertion is a `hook-failed` event
  with the mismatch detail.

  ```yaml
  then:
    hook:
      command: [/usr/local/bin/notify, alert]
      timeout: 10s
      expect_exit: 0                          # default; success exit code
      expect_stdout: "queued"                 # output must contain this
      expect_stderr: { op: "==", value: "" }  # …or an {op, value} comparison
  ```

  The same `expect_exit` / `expect_stdout` / `expect_stderr` fields work on a
  `command` check (see [Checks](rules.md#checks)). Command checks also support
  `user` to run the argv as a specific OS user; hook commands do not.
- **Evaluation model.** A **level check** (`storage`, `memory`, `pressure`,
  `load`, `fds`, `pids`, `conntrack`, `entropy`, `zombies`, swap `usage`) fires
  when **every present predicate holds**
  — a predicate is `{op, value}` with the operator set `>= > <= < == !=`; declare
  at least one, and add `for: { cycles: N }` or `for: { duration: 6m }` to
  require a sustained condition first.
  Predicate values share one grammar across every level check: a `*_pct` field
  accepts a number or an explicit `%` suffix in 0–100 (`90` or `"90%"`), a
  `*_bytes` field **requires** a size suffix (`K`/`M`/`G`/`T`, e.g. `10G`), and
  any other field is a plain number. A
  **stateful check** (counter deltas — net `errors`, swap `io`, `oom`; count
  growth with `delta`/`within`; and change detection — net/icmp
  `state`/`speed`/`latency`, `file`, `process`; and rate
  computation — `diskio`) compares
  against a baseline carried across cycles: the **first cycle primes the baseline
  and never fires**, and a counter reset clamps the per-cycle delta to zero.

### `then.expand` — volume growth (storage watch)

A storage watch can grow the LVM-backed filesystem under the checked path
automatically when it runs low. The expansion is native (Sermo
orchestrates it in Go, invoking only `lvs`/`vgs`/`lvextend` and the filesystem
grow tool — `resize2fs`, `xfs_growfs` or `btrfs` — which have no Go API):

```yaml
# /etc/sermo/storages/expand-backup.yml
name: expand-backup
monitor: previous
check:
  type: storage
  path: /mnt/backup
  free_pct: { op: "<", value: "10%" }
for: { cycles: 3 }                    # confirm low for 3 cycles first
policy: { cooldown: 30m }             # at most one expansion per 30m (see below)
then:
  expand: { by: 5G }                  # grow by up to 5G (capped to VG free)
  notify: [ops-email]                 # optional: report the outcome
```

`expand.by` is the amount to grow by (`K`/`M`/`G`/`T`, binary units). It is
**capped to the volume group's free space**, and when the VG has no free space
the action fails and is reported — Sermo never shrinks or reformats. Scope:
LVM logical volumes with an ext2/3/4, xfs or btrfs filesystem; a non-LVM or
otherwise unsupported volume fails cleanly rather than guessing.

Because watch actions are evaluated while the condition holds, an `expand`
action should always carry a watch-level **`policy`** block (same fields as
service remediation: `cooldown`, `backoff`, `max_actions`/`max_actions_window`)
so the volume is not extended on every tick while it stays low. The action runs
at most once per cooldown window; each attempt — success or failure — starts the
cooldown, so a failing expansion is not retried every cycle. Outcomes are
recorded as `expand` / `expand-skipped` / `expand-failed` events; cooldown skips
follow the watch's event emission policy (`on_change` by default,
`every_cycle` when configured).

When the web UI is enabled, a storage watch with `then.expand` also shows an
**expand** action. That manual action uses the same configured `check.path` and
`expand.by` values from YAML; the browser does not send a path or size.

### `then.makestep` — forced clock correction (clock watch)

A `clock` watch may carry a native `then.makestep` action: Sermo asks the local
chronyd to correct the system clock immediately. It sends chrony's `REQ_MAKESTEP`
— the command `chronyc makestep` sends — over chronyd's Unix command socket. No
external process, no operator script.

```yaml
watches:
  clock-step:
    interval: 1m
    check:
      type: clock
      source: chrony
      socket: /run/chrony/chronyd.sock
      max_offset: 5s
      timeout: 3s
    for: { cycles: 3 }
    policy:
      cooldown: 30m
    then:
      makestep:
        socket: /run/chrony/chronyd.sock
      notify: [ops-email]
```

`socket` is the only field and defaults to `/run/chrony/chronyd.sock`. There is
no host/port form and a `host:` or `port:` here is rejected, not ignored:
chronyd's UDP command port is its monitoring-only channel and refuses privileged
commands outright, so the step is only ever possible over the socket. sermod runs
as root and must be able to write to it.

**`policy.cooldown` is required and must be positive.** Unlike `then.expand`, a
watch that omits it is rejected at validation and refused by the builder: a step
is a clock *discontinuity*, not an idempotent nudge, and an ungated action runs
on every firing cycle. Each attempt — success or failure — starts the cooldown,
so a daemon that refuses is not hammered.

**The action only ever fires on an offset breach.** A clock check fails for
several reasons, and only an offset breach is one a step can fix; stepping
because the source is unsynchronized, too distant or too dispersed would jump the
clock by an unknown or zero correction. The check reports which one it was as
`clock_failure` (`offset`, `unsynchronized`, `stratum`, `root_dispersion`, and so
`SERMO_CLOCK_FAILURE` to a hook), and any value other than `offset` is skipped
with the reason recorded.

Note what `dry_run` does **not** give you: it suppresses every notifier except
`wall`, so a dry-run watch is not a way to "alert but never correct". For an
alert-only tier, configure a watch **without** the action — that is exactly what
tiers 1 and 3 are. Reserve `dry_run` for rehearsing a watch you intend to arm.

Results are recorded as `makestep` / `makestep-skipped` / `makestep-failed`
events. `dry_run: true` and panic mode both suppress it exactly like the other
actions, and there is deliberately **no** web action button: a one-click clock
step does not belong in a browser.

Pointing the check at the same `socket:` the action will command is worth doing —
a passing check is then standing evidence that the socket is reachable before the
action ever needs it.

**`then.makestep` is chrony-only, because chrony is the only one of the three
time daemons that offers a "correct the clock now" command.** ntpd exposes none,
and systemd-timesyncd's entire D-Bus surface is `SetRuntimeNTPServers` — while
`org.freedesktop.timedate1.SetTime` is refused outright whenever NTP
synchronization is enabled, which is exactly when a time daemon is running. For
those two the forced correction is a **restart of the daemon**, which is what the
shipped `ntpd` and `systemd-timesyncd` catalog services do:

```yaml
# catalog/services/ntpd.yml (and systemd-timesyncd.yml)
watches:
  restart-if-clock-drifting:
    check:
      type: clock
      servers: [time.cloudflare.com, pool.ntp.org]
      max_offset: 5s
      optional: true
    for: { duration: 15m }
    then:
      action: restart
```

That route goes through the **safe operation engine** — locks, guards, preflight
and the service's `policy.cooldown` — exactly like a manual `sermoctl restart`.
Two differences from `then.makestep` are worth knowing: it restarts the daemon
rather than stepping the clock in place, and a rule fires on *any* check failure,
so an unreachable NTP server can trigger it. Hence the two servers and the long
window; point `servers:` at your own NTP hosts.

**Never enable this on a ceph mon or osd host.** A clock jump can cost a monitor
its quorum. Use an alert-only watch there; see `examples/watches/clock-ceph.yml`.

`then.notify` lists notifier names (each must be defined under `notifiers`). For
the multi-metric watches (`net`, `icmp`, `swap`) the `notify`/`hook` live in each
metric's own `then`, so a metric can have its own targets. The notification's
subject/body carry the watch's message and the same `SERMO_*` fields a hook
receives.

For a `raid` watch, `then.notify_on` filters when its normal `then.notify`
targets receive delivery: `on_degraded`, `on_recovering`, `on_good`, or
`on_array_change`. Define as many named notifiers and templates as needed; every
selected notifier receives the structured RAID fields. It cannot be combined
with `then.notify_interval`.

### Manual RAID reconstruction control

Manual RAID control is disabled unless a single-array RAID watch opts in:

```yaml
name: raid-md0
check:
  type: raid
  array: md0
raid_control:
  pause_resume: true
then:
  notify: [ops]
```

The CLI and WebUI can run a short probe for this watch. Pausing reconstruction
uses two confirmations in the WebUI (and `--confirm md0` in the CLI), then
validates the live reconstruction state, locks the array and writes the native
md `sync_action`. Resume has the same preflight, lock and post-write check, but
can resume any paused configured array; Sermo does not require ownership of the
original pause. `dry_run: true` reports the intended operation without writing.

For an `lvm` watch, `then.notify_on: [on_change]` notifies only when its
effective health changes between `ok` and `error`, including recovery. It cannot
be combined with `then.notify_interval`.

**Checks and watches share the same check types.** Any single-shot check — the
host-resource ones below (`storage`, `memory`, `pressure`, `load`, `fds`,
`pids`, `conntrack`, `entropy`, `zombies`, `oom`, `failed_units`, `inotify`, among others) *and* the
service checks (`tcp`, `tcp_connections`, `ssh_idle`, `terminal_sessions`, `ports`, `http`, `command`, `file_exists`, `file`,
`lockfile`, `binary`, `pidfile`, `socket`, `libraries`, `config`,
`route`, `clock`, `firewall_rules`, `cert`, `sqlite`/`sqlite3`, `websocket`,
`count`, and connection-protocol checks such as `mysql`/`smtp`) — can be used as
a watch here, and the host-resource ones can equally be used in a service's
check-only `watches:` entries or explicit `checks:`/rules (see
[Checks](rules.md#checks)).

A `dbus` check without a target verifies the bus itself. To verify a named
service, set both `bus_name` and `object_path`; Sermo resolves its unique owner
with D-Bus auto-activation disabled. The default `probe: peer` checks liveness;
`probe: introspect` can require a `dbus_interface`, and `probe: property` reads
one scalar `property` from the required `dbus_interface`. These fields work
unchanged in host watches and service-embedded watches. See
[the D-Bus check](rules.md#database-protocols).

A watch fires its hook on the check's **alert** outcome: threshold crossed for
condition checks, **failure** for health checks
(`tcp`/`http`/`firewall_rules`/`cert`/…), so e.g. an `http` watch alerts when
the endpoint is down, a `firewall_rules` watch alerts when the loaded rule count
is below `min_rules`, and a `cert` watch alerts when the certificate is invalid
or expiring. The multi-metric (`net`, `icmp`, `swap`) watch shape below (a
`metrics:` map, one hook per metric) and the multi-target (`file`, `process`)
types are watch-only; the single-metric form of `net`/`icmp`/`swap` (an explicit
`metric:` field) also works as a service check-only watch or explicit `checks:`
entry (see [Checks](rules.md#checks)).

When the Web UI is enabled, `GET /api/watches` renders watch readings from the
latest daemon watch cycle; it does not start its own command, network, SQL,
firewall, count, disk I/O, `hdparm` or `smart` probes on each dashboard poll.
The Web UI and `sermoctl watch probe` can request one explicit sample for
configured `diskio`, `hdparm`, `lvm`, `raid` and `smart` host watches. `diskio`,
`hdparm`, `lvm` and `raid` are read-only samples — a `diskio` probe reads the
counters twice around a short pause, because its rates are the delta between two
readings and a single one would only be a baseline; a manual `smart` probe instead starts the device's
short self-test with `smartctl --test=short DEVICE`. Its successful command
acknowledgement means the self-test was scheduled, not that the drive is
healthy; scheduled SMART cycles continue to read the drive's identity, health,
attributes and self-test log with `smartctl -i -H -A -c -l selftest -j`.

While a self-test is in progress, the shared Web/CLI state is `testing`; later
daemon samples clear it when the device reports that the test ended. RAID and
LVM watches likewise surface device work as `testing`, `recovering`,
`rebuilding`, `repairing`, `moving` or `merging`, with their reported progress
where available. These are device-operation states, not health verdicts. The
daemon records the probe and event for the shared Web/CLI view, but does not
evaluate its watch window or run a rule, notifier, hook or remediation action.

### Service watches (scoped to a service)

A service can carry its own `watches:` block — the same entry shape as a host
watch (a `check:`, an optional `for`/`within` window, and a `then` block with a
fire-and-forget `hook`, `notify`, `expand`, `kill` or `makestep`, or a service `action`) —
declared **inside the service document**. Events are labelled
`<service>:<watch>`. Fire-and-forget entries reuse the host-watch runtime
(firing/recovered windows, hooks, notifiers, dry-run); entries with
`then.action` are desugared to `checks:` + `rules:`.

What "inside a service" adds over a host watch is the service's **check
context**, scoped to **everything discovery attributes to the service**: on an
init backend that reports them, the unit's whole control group, plus the
`processes:`/`pidfile:` selector matches and their descendants. That set is wider
than the main process's PID tree — a control-group member whose parent has exited
is still in it (see [`strays`](#strays--processes-the-service-cannot-account-for)):

- `process_count` counts only that set, so it is immune to unrelated host
  processes that share a user or exe. An optional `user`/`exe`/`exe_dir` narrows
  *within* it.
- `metric` (`cpu`, `cpu_thread`, `memory`, `io`, …) reads the **service scope**
  by default — the summed reading over that same tree — from a dedicated per-watch
  collector, so its rate deltas never collide with the engine's metric sampling.
- `service` binds to this service's unit.

Host-global checks (`fds`, `storage`, `count`, `load`, `http`,
`tcp_connections`, `ssh_idle`, `terminal_sessions`, …) read the same host resource on either surface — a service
watch does not per-service-clamp them. `tcp_connections` is scoped to a local
listening port, not a service PID tree, so use it only where that port uniquely
identifies the service.

Use fire-and-forget entries for a **hook/notification** tied to a service-local
signal: a growing spool directory, a CPU-hot worker thread, a backlog of files.
Use `then.action` for the compact operation/guard/alert form described below.
The check kinds **not** available here are `net`/`icmp`/`swap` (host/network
multi-metric watches — use the global `watches:` section) and the **`process`
watch** (it matches processes host-wide and can `kill`, which is unsafe from a
service scope — use `process_count`/`metric` for service-scoped process
monitoring, or a host watch). A system-scope metric, `scope: system`, is allowed
but only ever alerts — it must never drive a service operation action.

The watch name must not be `version` or `config` (reserved for the service's
version/config monitors). A service watch is visible and pausable like a global
watch: it appears in the web UI Watches panel and responds to
`sermoctl watch monitor|unmonitor <service>:<watch>`. Unmonitoring the **service**
does not touch its watches — their monitor state is independent.

#### Unified `then.action` (operation / guard / alert)

A service watch's `then` may declare an **`action`** instead of the fire-and-forget
`hook`/`expand`/`kill`, so one `watches:` entry expresses a check **and** its
remediation/guard/alert together:

- `action: restart | start | stop | reload | resume` — a **remediation** that runs
  through the operation engine (service lock, guards, cooldown/backoff/rate-limit,
  post-operation settling, panic mode) exactly like a `rules:` remediation.
- `action: block` with `blocks: [restart, start, …]` and `message` — a **guard**
  evaluated *during* an operation that refuses the listed actions while the check
  fails. Guards do not notify.
- `action: alert` with `message` and optional `notify` — an **alert**.

Such an entry is **desugared** to the equivalent `checks:` + `rules:` entry, so it
is exactly equivalent to writing that check + rule by hand and inherits every
safety gate (including the rule that a `scope: system` metric can never drive a
service action). Its `message` supports the rule runtime placeholders documented
in [rules](rules.md), including `${rule.duration}`, `${check.threshold}` and
`${check.value}` for single-check conditions and `${change.path}` /
`${change.old_version}` for `changed:` conditions. Because the result is a rule,
not a watch-runtime notifier, `then.notify_interval` is not supported with
`then.action`. The `check:` is always
**embedded** (`check: { type: http, … }`) and is generated as a check named after
the watch. Two watches embedding the same endpoint probe it twice. If a
remediation must reuse an existing shared health/`verify: true` check without a
second probe, write the classic `checks:` + `rules:` form explicitly.

The condition polarity follows the check: a **health** check (tcp/http/service/
command/cert/…) fires on **failure**; a **condition** check (metric/storage/load/…)
fires when its **threshold** is met (mark an embedded condition check
`optional: true` so it does not affect the service's availability/SLA).

A service watch with no `then` is a check-only entry: on resolution it becomes
`checks.<watch>` and participates in service health/SLA/post-operation
verification exactly like a hand-written check. A watch with `then` is **either**
an operation/alert (`then.action`) **or** a fire-and-forget side effect
(`hook`/`expand`/`kill`) — not both. The classic `checks:` + `rules:` sections
remain fully valid for hand-written sharing, but catalog service profiles use
`watches:` with embedded checks for standalone checks and compact actions.

```yaml
watches:
  restart-if-tcp-failed:       # desugars to checks.restart-if-tcp-failed + a remediation rule
    check: { type: tcp, host: "${host}", port: "${port}" }
    for: { cycles: 3 }
    then: { action: restart }
  block-restart-during-backup: # a guard: refuse restart while the backup process runs
    check: { type: process_count, exe: "${backup_binary}", count: { op: ">", value: 0 } }
    then: { action: block, blocks: [restart], message: "backup running" }
```

```yaml
# services/mail-queue.yml — a watch scoped to this service
name: mail-queue
uses: postfix

watches:
  deferred-backlog:            # emitted as "mail-queue:deferred-backlog"
    interval: 1m
    check:
      type: count
      path: /var/spool/postfix/deferred
      of: file
      recursive: true
      count: { op: ">=", value: 5000 }
    for: { cycles: 3 }
    then:
      hook: { command: [/usr/local/bin/drain-queue.sh] }
      notify: [ops-email]
  worker-runaway:              # process_count over the service's PID tree
    check:
      type: process_count      # no user/exe needed — counts only this service's tree
      count: { op: ">", value: 40 }
    then:
      notify: [ops-email]
  thread-hot:                  # cpu_thread of the service tree, from a dedicated collector
    interval: 30s
    check: { type: metric, scope: service, name: cpu_thread, op: ">", value: "90%" }
    for: { duration: 6m }
    then:
      notify: [ops-email]
```

```yaml
# /etc/sermo/storages/storage-root.yml
name: storage-root
monitor: enabled       # optional, default enabled
interval: 1m           # optional, default engine.interval
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" } # check fires when crossed
for: { cycles: 3 }     # optional window; reuses the rules engine
clear: { cycles: 3 }   # recovery hysteresis; default 5m via defaults.clear_window (Rules → Windows)
then:
  hook:
    command: [/usr/local/bin/alert-storage.sh, "/"]
    timeout: 10s       # optional, default engine.default_timeout
```

The generated `storage` check reads filesystem usage for `path` and is true when
every present predicate holds (`op ∈ >=,>,<=,<,==,!=`). Predicates cover **block space** —
`used_pct`, `free_pct`, `used_bytes`, `free_bytes` — and **inodes** —
`inodes_used_pct`, `inodes_free_pct`, `inodes_free` (absolute count).
`*_pct.value` accepts a number or an explicit `%` suffix in 0–100, e.g. `90` or `90%`.
`*_bytes.value` must include a size suffix (`K`/`M`/`G`/`T`, with optional
`B`/`iB`), e.g. `10G`; unitless byte values such as `10` are rejected.
Inode predicates catch the "disk full" that `df` hides: a filesystem out of
inodes (millions of tiny files) rejects new files while bytes are still free.
```yaml
# /etc/sermo/storages/storage-root.yml
name: storage-root
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" }       # block space
  free_bytes: { op: "<", value: 10G }        # absolute free space
  inodes_used_pct: { op: ">=", value: "90%" } # inode table
then:
  hook: { command: [/usr/local/bin/alert-storage.sh, "/"] }
```

A filesystem that does not report inodes (`inodes_total == 0`, e.g. btrfs) never
fires an inode predicate, so it cannot misread `0/0`.

#### Mount conditions

The `storage` check also verifies the **mount** of its `path`, so a filesystem's
mount and its space are configured in one entry (no duplicated `path`). This also
makes a space check trustworthy: a path that should be a mount but isn't would
otherwise make `statfs` silently report the *parent* filesystem. Add `mounted`
when you want to assert the path's mount state:

```yaml
# /etc/sermo/storages/data.yml
name: data
check:
  type: storage
  path: /data
  mounted: true            # require it to be a mount point (set false to require NOT mounted)
  used_pct: { op: ">=", value: "90%" } # space predicate(s), optional alongside mount
then:
  hook: { command: [/usr/local/bin/alert-storage.sh, "/data"] }
```

A storage check needs **at least one** of a space/inode predicate or a mount
condition (mount-only is fine). The mount is checked first from `/proc/mounts`: if
it is missing when `mounted: true` (or present when `mounted: false`), the check
alerts on that and the space predicates are skipped (their numbers would be
meaningless). `fstype`, `device` and `options` are not configurable predicates;
they are reported as result data and, while fresh, shown in the Web UI from the
daemon-cycle snapshot. This is the safe filesystem check for ext2/3/4, XFS, btrfs, vfat
and other mounted filesystems: it checks the mounted path, capacity and inode
data where the filesystem exposes it, and never runs a repair/`fsck` command on
a live filesystem.

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
    monitor: disabled
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
      address:                     # assigned IP addresses (non-link-local)
        on: change                 # fire on renumbering; or `expect: present|absent`
        then:
          hook:
            command: [/usr/local/bin/sermo-ddns-update.sh, eth0]
```

The four metrics and their conditions:

- **`state`** — interface up/down. Use `on: change` to fire on any transition, or
  `expect: up` / `expect: down` to fire whenever the state **is** the expected
  value.
- **`speed`** — link speed in Mbps. Supports `on: change` only (fires when the
  speed differs from the baseline).
- **`errors`** — sums the named `counters` (default `rx_errors`, `tx_errors`) and
  fires when the per-cycle **delta** satisfies `delta: {op, value}`.
- **`address`** — the interface's assigned addresses (IPv4 + global IPv6;
  link-local is excluded). Use `on: change` to fire when the set changes — a
  provider-forced renumbering or reconnect, the natural trigger for a dynamic-DNS
  hook — or `expect: present` / `expect: absent` to fire whenever addresses
  **are** in the expected state (a PPP session can be up with IPCP failed and no
  address assigned; the `pppd` catalog service uses `expect: present`).

Hook extras: `SERMO_INTERFACE`, `SERMO_METRIC`, and — for the change metrics
(`state`/`speed`/`address`) — `SERMO_OLD`/`SERMO_NEW`.

`errors` is the metric worth grading `severity: warning` (see
[Severity](rules.md#severity-severity)): a climbing error counter is degradation
to look at, while the link going down is the outage. Declared per metric, the
link keeps its red and the counter reads amber:

```yaml
metrics:
  state:
    expect: down
  errors:
    severity: warning
    delta: { op: ">", value: 100 }
```

### `icmp` — external host (ping)

An `icmp` watch monitors an **external host** by ICMP echo (ping): reachability
and round-trip latency. The host is named once and each metric is independent,
with its own condition **and its own hook**. The entry expands into one watch
per metric, so metrics do not share state.

```yaml
watches:
  ping-gw:
    monitor: disabled
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
  `threshold: {op, value}` (same operator set as storage) to fire when the RTT
  crosses a fixed bound, **or** `change: {delta}` to fire on an abrupt jump
  (`|rtt − rtt_prev| > delta`); set exactly one. Latency conditions only apply
  while the host is reachable; an unreachable cycle never fires latency and never
  updates the change baseline (so the baseline is the last *reachable* RTT).

Hook extras: `SERMO_HOST`, `SERMO_METRIC`, and — for the change metrics —
`SERMO_OLD`/`SERMO_NEW`.

`latency` is the metric worth grading `severity: warning` (see
[Severity](rules.md#severity-severity)): a slow gateway is degradation, while an
unreachable one is the outage its `state` metric already reports.

ICMP requires elevated privileges: the daemon needs the `CAP_NET_RAW` capability
(or the host's `net.ipv4.ping_group_range` sysctl must include the daemon's gid)
to open a raw ICMP socket. This iteration is **IPv4-only**.

### `clock` — wall-clock drift

A `clock` watch checks this host's wall-clock offset against external NTP servers.
It is meant for hosts that may not run a local NTP daemon: Sermo sends client NTP
queries itself, raises the alert when drift is outside policy, and leaves the correction to a
watch action ([`then.makestep`](#thenmakestep--forced-clock-correction-clock-watch)
on a chrony host) or to your hook script.

```yaml
watches:
  clock-drift:
    monitor: disabled
    interval: 5m
    check:
      type: clock
      servers:
        - time.cloudflare.com
        - pool.ntp.org
      max_offset: 2s
      max_stratum: 4              # optional, default 15
      max_root_dispersion: 250ms  # optional
      timeout: 3s
    for: { cycles: 2 }
    then:
      notify: [ops-email]
      hook:
        command: [/usr/local/sbin/sermo-sync-clock.sh]
        timeout: 2m
        expect_exit: 0
```

`servers` and `max_offset` are required. Optional `interface` /
`interface_match` bind the NTP request through specific links, matching the other
network checks. Hooks receive `SERMO_SERVER`, `SERMO_OFFSET_SECONDS`,
`SERMO_OFFSET_ABS_SECONDS`, `SERMO_STRATUM`, `SERMO_ROOT_DISPERSION_MS` and the
other returned NTP fields, so the script can decide what to run — though on a
chrony host `then.makestep` corrects the clock natively, with no script.

On a host that already runs chrony, set `source: chrony` instead: Sermo reads the
local chronyd over its command protocol rather than sending its own NTP queries,
which is the only option when chrony runs as a client and therefore serves no NTP
on port 123 to probe.

```yaml
watches:
  chrony-drift:
    interval: 5m
    check:
      type: clock
      source: chrony
      host: 127.0.0.1       # optional, the default
      port: 323             # optional, chronyd's command port
      # socket: /run/chrony/chronyd.sock   # instead of host/port
      max_offset: 100ms
      max_stratum: 4
      max_root_dispersion: 200ms
      unit: s
      timeout: 3s
```

`servers` is rejected with `source: chrony`, and `host`/`port`/`socket` name the
local daemon instead. With `socket:` the result reports `socket` in place of
`server` and `port`, so a hook receives `SERMO_SOCKET` and no
`SERMO_SERVER`/`SERMO_PORT`. Hooks then also receive `SERMO_SYNCHRONIZED`,
`SERMO_SKEW_PPM`, `SERMO_FREQUENCY_PPM`, `SERMO_RMS_OFFSET_SECONDS`,
`SERMO_REFERENCE_AGE_SECONDS` and `SERMO_SOURCES_ONLINE`, so a script can tell a
clock that is merely drifting from one whose sources have gone away. An
`interface` pin does not apply when `socket:` is used — a Unix socket has no
egress link. To assert individual fields rather than a drift threshold, use the
[`chrony` check type](rules.md) with `expect:`.

### `swap` — system swap

A `swap` watch monitors system swap as two independent metrics, grouped like
`net`/`icmp` (each with its own condition **and its own hook**). `usage` catches
swap filling up (a level check); `io` catches swap thrashing (a counter delta —
heavy paging in/out, a classic sign of memory pressure).

```yaml
watches:
  swap:
    monitor: disabled
    interval: 30s
    check: { type: swap }
    metrics:
      usage:                                 # how full swap is (level check)
        used_pct: { op: ">=", value: 80 }    # any of used_pct / free_pct / free_bytes
        then: { hook: { command: [/usr/local/bin/sermo-swap-usage.sh] } }
      io:                                    # paging activity (counter delta)
        delta: { op: ">", value: 1000 }      # pages swapped in+out per cycle
        then: { hook: { command: [/usr/local/bin/sermo-swap-io.sh] } }
```

- **`usage`** predicates: `used_pct`, `free_pct` (of total swap) and `free_bytes`
  (a size with a `K`/`M`/`G`/`T` suffix, e.g. `1G` — same grammar as the storage
  check). A host with **no swap configured** never fires (so a `free_bytes`
  predicate does not misfire on a swapless box).
- **`io`** sums the pages swapped **in and out** (`pswpin`+`pswpout` from
  `/proc/vmstat`); the `delta` threshold is pages per interval, so it scales with
  `interval`.
- Hook extras: `SERMO_METRIC` (`usage`|`io`), `SERMO_TOTAL_BYTES`,
  `SERMO_FREE_BYTES`.

### `load` — system load average

A `load` watch checks the 1/5/15-minute load averages against thresholds. With
`per_cpu: true` the loads are divided by the CPU count first, so a threshold means
**load per core** (≈1.0 is fully utilized) and the same config works on any
machine size.

```yaml
watches:
  load:
    monitor: disabled
    interval: 30s
    check:
      type: load
      per_cpu: true                  # optional, default false: divide by NumCPU
      load5: { op: ">", value: 1.5 }    # any of load1 / load5 / load15
      load15: { op: ">", value: 1.0 }
    for: { cycles: 3 }
    then: { hook: { command: [/usr/local/bin/sermo-load-alert.sh] } }
```

Predicates: `load1`, `load5`, `load15`. Prefer `load5`/`load15` for sustained
saturation (`load1` is spiky). Hook extras: `SERMO_LOAD1`/`SERMO_LOAD5`/
`SERMO_LOAD15` (raw) and `SERMO_NUM_CPU`.

### `memory` — system RAM

A `memory` watch checks system RAM against thresholds. It is built on the
kernel's **MemAvailable** estimate (from `/proc/meminfo`) — the memory new
allocations can claim without swapping — so page cache and reclaimable buffers
never read as "used". Catches the slow leak or over-packed host before the OOM
killer does.

```yaml
check:                                   # in a watch body like `load` above
  type: memory
  used_pct: { op: ">=", value: "90%" }   # (total - available) / total
  # available_bytes: { op: "<", value: 1G }   # absolute headroom, alternatively
```

Predicates: `used_pct`, `available_pct` (of total RAM) and `available_bytes`
(size suffix required, e.g. `1G` — the shared size grammar). A host whose
`/proc/meminfo` reports no total never fires. Pair with `for: { cycles: 3 }` so
a momentary spike does not alert. Hook extras: `SERMO_TOTAL_BYTES`,
`SERMO_AVAILABLE_BYTES`, `SERMO_USED_PCT`, `SERMO_AVAILABLE_PCT`.

### `pressure` — kernel PSI stall time

A `pressure` watch checks a kernel **PSI** resource (`/proc/pressure/cpu`,
`memory` or `io`) against stall-percentage thresholds. PSI reports the share of
wall time tasks spent **stalled** waiting on the resource — the kernel's own
"this host is struggling" signal. It complements `load` (queue depth) and
`memory` (headroom) with actual experienced stall: a host can look fine on both
and still be thrashing.

```yaml
check:                                   # in a watch body like `load` above
  type: pressure
  resource: memory                       # required: cpu | memory | io
  some_avg10: { op: ">", value: 10 }     # % of time SOME tasks stalled (10s avg)
  # full_avg60: { op: ">", value: 5 }    # % of time ALL tasks stalled (60s avg)
```

Predicates (each a stall percentage, 10s/60s/300s rolling windows):
`some_avg10`/`some_avg60`/`some_avg300` and `full_avg10`/`full_avg60`/
`full_avg300`. `some` means at least one task stalled; `full` means every
non-idle task stalled (the severe form; for `cpu` it is 0 or absent on older
kernels). Prefer `some_avg60`/`full_avg60` with a `for` window for sustained
pressure. A kernel built without PSI (`CONFIG_PSI=n`) never fires. Hook extras:
`SERMO_RESOURCE` and all six `SERMO_SOME_*`/`SERMO_FULL_*` averages.

### `oom` — kernel OOM kills

An `oom` watch fires when the kernel out-of-memory killer has reaped processes
since the last cycle — a counter delta on the cumulative `oom_kill` counter from
`/proc/vmstat`.

```yaml
watches:
  oom:
    check: { type: oom }            # delta optional; default fires on any kill (> 0)
    then: { hook: { command: [/usr/local/bin/sermo-oom-alert.sh] } }
```

The common case is "alert on any OOM kill", so `delta` may be omitted (defaults to
`> 0`); set a higher threshold to alert only on a burst. A host whose kernel does
not expose `oom_kill` never fires. Hook extras: `SERMO_TOTAL` (cumulative kills).

### `fds` — system file descriptors

An `fds` watch checks the system-wide open file descriptors against the kernel
maximum (`fs.file-max`, from `/proc/sys/fs/file-nr`). Fd exhaustion makes every
`open()`/`socket()`/`accept()` fail with `EMFILE`/`ENFILE`, so it is worth
catching early.

```yaml
check:                                   # in a watch body like `load` above
  type: fds
  used_pct: { op: ">=", value: 85 }      # allocated / file-max
  # free: { op: "<", value: 10000 }      # absolute headroom, alternatively
```

Predicates: `used_pct` (percent of the limit), `free` (`file-max − allocated`) and
`allocated` (absolute). Hook extras: `SERMO_ALLOCATED`, `SERMO_MAX`,
`SERMO_USED_PCT`, `SERMO_FREE`.

### `diskio` — block-device I/O rates

A `diskio` watch monitors one block device's I/O, computed from per-cycle
`/proc/diskstats` deltas: **utilization** (share of wall time the device was
busy), **throughput** and **average request latency**. Use it for saturated or
degraded disks that storage-space checks cannot see. It is **stateful**: the
first cycle only baselines (never fires), and a counter reset clamps the delta to
zero.

```yaml
watches:
  sda-busy:
    interval: 30s
    check:
      type: diskio
      device: sda                          # required: a /proc/diskstats name
      util_pct: { op: ">=", value: 90 }    # % of the cycle the device was busy
      await_ms: { op: ">", value: 50 }     # avg ms per completed request
      # read_bytes:  { op: ">", value: 100M }  # bytes/second, size suffix
      # write_bytes: { op: ">", value: 50M }
    for: { cycles: 3 }
    then: { hook: { command: [/usr/local/bin/sermo-diskio-alert.sh, sda] } }
```

Predicates: `util_pct` (0–100), `await_ms` (plain ms), and `read_bytes`/
`write_bytes` — **bytes per second**, written with the shared size grammar
(`50M` = 50 MiB/s = 52,428,800 B/s; note rates are *displayed* in SI decimal
units, so that threshold reads back as `52.43 MB/s` in events and the web UI).
All present predicates must hold (AND), so `util_pct` +
`await_ms` together distinguish "busy and slow" from merely busy. A device
missing from `/proc/diskstats` never fires (the check reports the error). Hook
extras: `SERMO_DEVICE`, `SERMO_UTIL_PCT`, `SERMO_READ_BYTES`,
`SERMO_WRITE_BYTES`, `SERMO_AWAIT_MS`.

### `pids` — kernel PID table

A `pids` watch checks the kernel PID table — the total scheduling entities
alive (threads; each consumes a PID, from the fourth `/proc/loadavg` field)
against `kernel.pid_max`. A full table makes every `fork()`/`clone()` fail with
`EAGAIN` host-wide: the end state a runaway fork loop or a leaking thread pool
reaches, and where the [`zombies`](#zombies--defunct-processes) growth warning
ultimately lands.

```yaml
check:                                   # in a watch body like `load` above
  type: pids
  used_pct: { op: ">=", value: 90 }      # threads / kernel.pid_max
  # free: { op: "<", value: 5000 }       # absolute headroom, alternatively
```

Predicates: `used_pct` (percent of the limit), `free` (`pid_max − threads`) and
`count` (absolute threads). An unreadable `pid_max` leaves `used_pct`/`free`
unknown (they never fire); `count` still works. Hook extras: `SERMO_COUNT`,
`SERMO_MAX`, `SERMO_USED_PCT`, `SERMO_FREE`.

### `conntrack` — netfilter connection table

A `conntrack` watch checks the netfilter connection-tracking table against its
maximum (`nf_conntrack_max`, from `/proc/sys/net/netfilter`). A full table
silently **drops new connections** (and logs `nf_conntrack: table full`), so it is
worth catching on busy gateways, proxies and NAT boxes before it saturates.

```yaml
check:                                   # in a watch body like `load` above
  type: conntrack
  used_pct: { op: ">=", value: 90 }      # count / nf_conntrack_max
  # free: { op: "<", value: 20000 }      # absolute headroom, alternatively
```

Predicates: `used_pct` (percent of the max), `free` (`nf_conntrack_max − count`)
and `count` (absolute). Needs the `nf_conntrack` module loaded; without it the
check never fires. Hook extras: `SERMO_COUNT`, `SERMO_MAX`, `SERMO_USED_PCT`,
`SERMO_FREE`.

### `firewall_rules` — loaded firewall rules

Use `firewall_rules` for firewall loaders such as FireHOL that exit after
installing rules. It is a health check: as a watch it fires when the loaded
nftables/iptables rule count drops below `min_rules` (default `1`).

```yaml
watches:
  firewall:
    check: { type: firewall_rules, backend: auto, min_rules: 1 }
    then: { hook: { command: [/usr/local/bin/firewall-missing.sh] } }
```

`backend` is `auto`, `nftables` or `iptables`. Hook extras:
`SERMO_BACKEND`, `SERMO_RULES`, `SERMO_MIN_RULES`.

### `entropy` — kernel entropy pool

An `entropy` watch checks the available kernel entropy (bits) from
`/proc/sys/kernel/random/entropy_avail` against a threshold. Low entropy makes
`/dev/random` reads block and slows crypto and TLS handshakes — most visible on
VMs and headless/embedded hosts without a hardware RNG.

```yaml
check:                                   # in a watch body like `load` above
  type: entropy
  avail: { op: "<", value: 200 }         # fire when available entropy drops below 200 bits
```

The single `avail: {op, value}` predicate is required; the usual form is
`avail < N`. Hook extras: `SERMO_AVAIL` (the same value as `SERMO_VALUE`, bits
available).

### `zombies` — defunct processes

A `zombies` watch counts processes in the zombie (defunct) run state — those that
have exited but whose parent has not reaped them — against a threshold. A few are
transient and normal; a growing count means a parent is leaking child slots and
will eventually exhaust the PID table.

```yaml
check:                                   # in a watch body like `load` above
  type: zombies
  count: { op: ">", value: 20 }          # fire when more than 20 zombies persist
```

The single `count: {op, value}` predicate is required; pair it with a `for` window
so a momentary burst of exiting children does not fire. Hook extras:
`SERMO_ZOMBIES` (the same value as `SERMO_VALUE`, the count).

### Check summaries

Every check accepts an optional `summary` string. When present, it replaces the
check's normal message in the dashboard, emitted events, notifications and
`SERMO_MESSAGE` for hooks. Without it, Sermo keeps the existing checker-specific
message and display.

`summary` is rendered after the check runs. `${value}` is the observed value used
by the comparison, `${trigger}` is the active trigger when the checker exposes
one, and every resolved check field can be referenced directly or through
`${check.<field>}`. Result data is also available as `${result.<field>}`. Numbers
follow the canonical convention described in [rules.md](rules.md) — comma as the
thousands separator and dot as the decimal mark (`12,345.68`); durations and
timestamps use the normal readable format. Unknown references remain visible,
which makes a mistaken field name easy to identify.

A check that could not observe at all keeps its own message: an unavailable
result — an unreadable source, a timeout, a database that will not open — has no
reading to summarise, so the probe's diagnostic is what reaches the dashboard
instead of the template. Rendering the summary over it would both hide the cause
and, since the observation produced no data, leave `${value}` on screen.

Unavailable observations are unhealthy, but they never satisfy a watch
condition and never run watch actions. The watch emits one `error` event when
observation becomes unavailable and one `recovered` event when a valid sample
returns; this edge state survives daemon restarts and configuration reloads.

```yaml
check:
  type: file
  paths: [/usr/share/GeoIP]
  recursive: true
  older_than: 480h
  summary: "GeoIP ${value} is older than ${older_than} in ${number_files} files"
```

For a non-recursive regular file, `${number_files}` is `1`; with a recursive
directory it is the number of regular files scanned. Normal configuration
variables are resolved before the summary runs, so service checks can combine
runtime values with service variables:

Metric summaries preserve the metric unit: byte values render in IEC binary
units (`B`, `KiB`, `MiB`, `GiB` or `TiB`, matching how size suffixes are parsed
in configuration), and byte rates add `/s`.

```yaml
check:
  type: sql
  engine: sqlite
  path: ${db_dir}/retry
  query: "SELECT count(*) FROM tblblob"
  op: ">"
  value: ${db_cleanup_record_limit}
  summary: "Retry DB has ${value} records (limit ${check.value}); cleanup age ${db_cleanup_age}"
```

### `file` — file/directory attributes and freshness

A `file` watch monitors one or more files/directories for attribute changes —
size, permissions, owner, deletion and modification age — and runs the entry's
hook **once per path change or freshness breach**. `paths:` is a required non-empty
list. With `recursive: true` it watches every subtree, so a hook fires per changed
or stale entry.

```yaml
watches:
  app-data:
    monitor: disabled
    interval: 30s
    check:
      type: file
      paths:                          # one or more files/directories
        - /var/lib/myapp
        - /srv/myapp/incoming
      recursive: true                 # optional, default false (whole subtree)
      include_hidden: true            # optional, default false (include .files/.dirs)
      absent_ok: true                 # optional, default false (a missing path is healthy)
      older_than: 24h                 # optional: mtime age; any stale path fires
      summary: "${path} age ${value}, limit ${older_than}, files ${number_files}"
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
  the last cycle) or a threshold `{op, value}` (same operator set as storage). The
  threshold is **edge-triggered**: it fires once when the size crosses into the
  condition and re-arms only after it drops back out — not every cycle while
  breached. A path first seen *already* satisfying the threshold counts as a
  crossing and fires, the same way `older_than` reports a path that is already
  stale, so a file whose very appearance is the event (a `dead.letter`, a crash
  dump) is caught — on the first real cycle, since the startup observation cycle
  reports nothing. The baseline lives with the watch, so a reload re-arms it and
  a still-breaching path is reported once more, exactly as `older_than` behaves.
- **`permissions`** — `on: change`; fires when the permission bits change.
- **`owner`** — `on: change`; fires when the owning uid or gid changes.
- **`existence`** — `on: delete`; fires when a path that existed stops existing
  (re-creation is then adopted silently). Deletion is the only transition reported.
- **`older_than`** — a positive duration such as `24h`; fires when the elapsed
  time since a path's modification time (`mtime`) becomes greater than that
  duration. A path already stale on the first cycle fires immediately; it re-arms
  once the path is modified or removed.

The watch's own state answers **"do the configured paths exist"** — the conditions
above signal through events, not through it. So a watch none of whose paths exist
reads as failed by default, which is what a config file or a database directory
needs. Set **`absent_ok: true`** to invert that for a watch whose job is to report
a file *appearing* — `/root/dead.letter`, a crash dump, `/forcefsck`. There the
absence is the healthy state, and without it the watch sits red on every host that
has nothing wrong with it. The conditions are unaffected: the file showing up over
its `size` threshold still fires.

When `recursive: true` and a selected path is a directory, every entry in that
subtree is tracked independently (symlinks are watched as links, never followed).
By default, descendants whose name starts with `.` are skipped, including their
subtrees. Set `include_hidden: true` to track them. A hidden path named directly
in `path` or `paths` is always tracked.
New entries are adopted silently unless already stale or already over a `size`
threshold — the two conditions judgeable from a single observation. The others
(`size: {on: change}`, `permissions`, `owner`, `existence`) name a transition, so
they have no prior to compare against and stay silent on adoption. Deleted entries
fire `existence` if configured. Each detected change is **one event and one hook
run**, so a cycle that finds several changed paths fires several times — except
`older_than`: when several paths cross the age threshold in the same cycle, the
hook still runs once per path (with its own `SERMO_PATH`), but they produce
**one aggregated event and notification** naming the count and up to 5 paths
(`12 files older than 2w6d: a, b, … (+7 more)`), so a directory of stale files
cannot burst identical events with the same timestamp.

Hook extras: `SERMO_PATH` (the changed path), `SERMO_CHANGE`
(`size`|`size_threshold`|`permissions`|`owner`|`deleted`|`older_than`),
`SERMO_OLD`/`SERMO_NEW` (old/new value), and `SERMO_SIZE`/`SERMO_OP` for size
conditions. An `older_than` event also sets `SERMO_MODIFIED_AT`,
`SERMO_AGE_SECONDS` and `SERMO_VALUE` (the configured duration).

### `stale_binary` — service running a replaced binary

Service-scoped. Reports the service's processes whose executable was replaced or
removed on disk — the normal result of upgrading a package without restarting
the service, which keeps serving the previous version indefinitely.

You do not write this check: Sermo injects it, named `stale-binary`, into every
service that declares `processes:` or `pidfile:`, together with a rule that
alerts and then restarts. It takes no fields; the selectors it inspects are the
service's own.

When the init backend supplies a live process set (for example, a systemd
cgroup), that attribution is authoritative: deleted processes elsewhere on the
host are not assigned to the service merely because their former path and user
match a selector. The selector fallback remains available when the backend
cannot name the service's processes, as on many OpenRC services.

It exists because the condition is otherwise invisible or misleading. A process
identified through the init backend keeps running with an unusable executable
and nothing is reported at all; a process matched only by an `exe:` selector
stops matching entirely, so the service merely looks like it has no processes
and reads as `warning` with no stated cause.

`stale_binary` is diagnostic rather than an availability failure: it does not
reduce service health or SLA. When it finds a replaced executable, the service
is shown as `warning` without a separate restart hint in the State field, while
its generated rule continues to alert and, where allowed, restart safely.

Set `restart_on_stale_binary: false` on a service to keep the alert and the
notification but drop the restart:

```yaml
name: ovs-vswitchd
restart_on_stale_binary: false   # restarting the dataplane cuts the host off
```

The flag governs this trigger only. A manual `sermoctl restart`, and remediation
that restarts the service after a real failure, are unaffected. It inherits from
`defaults:` like `dry_run`, so a whole host can opt out at once.

A process whose executable was replaced still resolves no exe, so it matches no
`exe` selector and is never signalled — see [safety.md](safety.md). This check
reports the condition; it does not relax that rule.

### `strays` — processes the service cannot account for

Service-scoped. Reports the members of the service's init unit control group that
no configured selector claims and that no longer hang off the unit's principal
process — a probe that daemonized, a child the daemon never reaped, a survivor of
an earlier incarnation. See [safety.md](safety.md) for the full definition.

Sermo injects it, named `strays`, into every init-managed service that declares
`processes:` or `pidfile:`, with no fields: what counts as a stray follows from the
service's own selectors and its control group, and the expected count is zero.

Two optional bounds are accepted when you declare your own instance:

| Field | Meaning |
| --- | --- |
| `max` | fail above this many strays (default `0`) |
| `max_increase` + `within` | fail when the count grew by more than this over that wall-clock span |

Both are **bounds above which the check fails**, so `OK` always means healthy and a
rule keeps using `failed:`. That is why they are not `{op, value}` predicates: in a
level check like `process_count`, OK means "the predicate holds", which would invert
`failed:` for a configured instance while the injected one kept it. `op` and `value`
are rejected at validation time for that reason.

`max_increase` measures growth against the oldest sample still inside `within`, not
against the previous cycle. A per-cycle delta is true for exactly one cycle, so a
rule's `for:` window would silence it permanently; growth stays true for the whole
span and composes with rule windows. A count that falls never reports negative
growth.

It reports **state**: the count and the executables appear in the dashboard and in
`sermoctl status`, and never reduce service health or SLA. The daemon is serving;
something merely accumulated beside it.

Unlike `stale_binary`, **no rule is injected with it**. On real hosts the raw
condition is dominated by workloads a profile has legitimately marked
`delegated: true` (container shims, Gluster bricks, libvirt's `dnsmasq` pairs), so
a fleet-wide alert would mostly report incomplete catalog coverage — and the remedy
for that is a selector, not a reap. Alerting is therefore the operator's call.

The injected check is enough for "tell me when anything is unaccounted for":

```yaml
rules:
  alert-on-strays:
    if: { failed: { check: strays } }
    for: { cycles: 3 }        # ignore a leftover that clears itself
    then:
      action: alert
      message: "${display_name} accumulated ${check.value} unaccounted-for process(es)"
```

For a threshold or a sudden jump, declare your own instances — one check per
condition, since a check ANDs its bounds — and OR them in one rule:

```yaml
checks:
  strays-high:
    type: strays
    max: 20                   # a busy host parks a few; twenty is a leak
  strays-growing:
    type: strays
    max_increase: 5
    within: 30m               # five new ones in half an hour is a leak too

rules:
  alert-on-stray-leak:
    type: alert
    notify: [ops-email]
    if:
      or:
        - failed: { check: strays-high }
        - failed: { check: strays-growing }
    then:
      action: alert
      message: "${display_name}: stray processes need attention"
```

With a single check leaf the message can also name the numbers —
`${check.value}` (the count, or the growth for a `max_increase` instance) and
`${check.threshold}` (the bound). An `or:` of two checks leaves both empty, because
Sermo cannot tell which leaf to describe; keep the numbers in per-check rules if you
want them in the text.

`notify:` follows the normal two-layer selection (rule-level, else the global
`notify:` default), and `emission:` the normal repeat suppression — by default one
alert per episode, on the rising edge.

Services with an external `control:` backend (docker, libvirt) get no injected
check: there the whole container or domain PID set is attributed to the service,
so a process no selector names is ordinary rather than a leftover.

Clearing a stray is a separate, manual step — `sermoctl reap SERVICE --apply`,
gated by the service's own `reap.kill_only_if` (see [services.md](services.md)).
No rule action can reap.

#### Why this is not a process count

A stray is, by construction, **the opposite of a child**: classification builds the
principal's live tree and labels only what is *not* in it. A process reachable by
walking PPID down from the main process is a worker; a control-group member whose
PPID chain does not reach it was reparented to PID 1, which is what makes it a
leftover. The two sets are disjoint.

Every other instrument counts a superset and cannot separate them:

| Instrument | Counts | Includes strays |
| --- | --- | --- |
| `process_count` in a service's `checks:` | host PIDs passing `user`/`exe`/`exe_dir` | only incidentally — it is host-wide |
| `process_count` in a service's `watches:` | everything discovery attributes to the service | yes |
| `process_count` metric (`scope: service`) | the same set, sampled per cycle | yes |
| Dashboard process totals (count, memory, FDs, threads) | the same set | yes |
| `strays` | that set minus the principal, its live tree, pidfile and selector matches, and delegated processes | it is only that |

Two consequences worth knowing:

- A leak **does** inflate the service's `process_count`, memory, FD and thread
  figures, and that is correct — the resources really are consumed. What none of
  those numbers can say is that the growth is unexplained, which is why they need a
  per-service threshold and `strays` does not (its expected value is zero
  everywhere).
- Counting only true descendants would make the leak **invisible**: the leftover
  left the tree the moment its parent exited. Sermo therefore has no
  descendants-only counter at all.

Both counts are graphed, so an accumulation shows up as a rising `strays` series
beside a rising process count — the shape that distinguishes a leak from load.

### `process` — process by name

A `process` watch tracks the processes whose **name** matches (the resolved exe
basename or its full path), optionally filtered by owning `user`, and fires the
hook **once per matching PID** when that process has been alive at least `for`
and/or its CPU/memory/IO crosses a threshold. It is distinct from the
per-service `process` check, which reports running/zombie/absent state.

```yaml
watches:
  hot-workers:
    monitor: disabled
    interval: 30s
    check:
      type: process
      name: myworker                  # exe basename (e.g. myworker) or full path
      user: www-data                  # optional: also match the owning user
      for: 5m                         # optional: alive at least this long (process start time)
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
`io` are rates, so they need two samples: a new PID never fires on them in its
first cycle. Each matching PID is tracked independently — **one event and one
hook per PID** — so a worker pool produces one hook per offending worker.

`gone: true` is the inverse — it fires once when a previously-seen matching PID
**disappears** (and re-arms if it returns), so it never fires merely because the
process is present. Set it alone for a pure liveness alert ("nginx is gone"), or
alongside the presence conditions. With multiple matching PIDs it fires per exited
PID. A PID the kernel recycled between two cycles counts as a disappearance too:
the number is still in the sample, but the start time proves another process is
behind it, so the one that ended is reported gone.

Hook extras: `SERMO_PID` (the matching pid), `SERMO_PROCESS` (the configured
name), `SERMO_CHANGE` (`threshold` for a presence fire, `gone` for a
disappearance), `SERMO_USER` (if set), `SERMO_AGE_SECONDS`, `SERMO_MEMORY` (RSS
bytes), and — once a rate is available — `SERMO_CPU` (percent) and `SERMO_IO`
(bytes/sec).

`for` is measured from the **process's own start time** (`/proc/<pid>/stat` field
22 against the boot time), not from when the daemon first saw it: a daemon
restart does not reset it, and a process already older than `for` when it is
first sampled fires as soon as the watch is evaluated — the settling cycle after
startup only observes, so on the cycle after that, `then.kill` included. When the
start time is unreadable the watch falls back to its first observation of that
PID.

`io` reads `/proc/<pid>/io`, which requires the daemon to have permission to read
it (typically running as root); when it is unreadable the IO condition never fires.
The optional `user:` filter is resolved through `engine.user_lookup`; numeric
UIDs are accepted and avoid host identity-service ambiguity. The WebUI shows
current matches, PIDs and aggregate RSS/IO counters.

### `process_policy` — alert-only user execution policy

A `process_policy` host watch audits **every process whose real UID belongs to
one configured user**. It is for service accounts that must execute a small,
reviewed set of binaries: a process must match one `allow` entry with an exact,
absolute `exe` path and, when configured, an anchored RE2 `cmd` expression.
An unresolved executable or a `/proc/<pid>/exe` target marked `(deleted)` is
always a violation, even when its previous path appears in the allowlist.

```yaml
name: security-user-postgres
display_name: PostgreSQL execution policy
category: security
monitor: enabled
interval: 30s
check:
  type: process_policy
  user: postgres
  allow:
    postgres-18:
      exe: /usr/lib/postgresql/18/bin/postgres
    postgres-17:
      exe: /usr/lib/postgresql/17/bin/postgres
      cmd: '^/usr/lib/postgresql/17/bin/postgres(?: |$)'
then:                         # optional; omit for dashboard/event-only alerting
  notify: [security]
  notify_interval: 15m
```

`allow` is non-empty. Each named entry accepts only `exe` and optional `cmd`:
`exe` must be a clean absolute path, and `cmd` must compile and start with `^`
and end with `$`. A basename, glob or partial command is rejected. The command
line is used only to make an allow entry narrower; it is never copied into the
WebUI, events, notification environment or generated inventory.

This is intentionally an **alert-only** check. Its `then:` block may contain
only `notify` and `notify_interval`; `hook`, `kill`, `expand`, `makestep` and a
`policy:` block are invalid. It cannot restart, signal, repair or otherwise
change the watched account. Violations fire once per PID incarnation (PID plus
start time), re-arming for a new process that reuses the same PID. When the
start time cannot be read, Sermo records a firing event for every current
violating sample rather than risk suppressing a reused PID; `notify_interval`,
when configured, still paces notifier delivery. The WebUI shows the account,
active-match and violation counts, PIDs and a safe reason, but no command
arguments.

#### `then.kill` — terminate the matched process

A process watch can **terminate the matched PID natively**, without an external
hook script, with a `then.kill` action. It reuses the same guarded process
reaper as service stop and mount blocker-signalling policies. Because it signals real
processes, `then.kill` requires `check.name` to be an absolute resolved
`/proc/<pid>/exe` path and `check.user` to be set; basename-only process watches
may still monitor and notify, but cannot kill.

```yaml
watches:
  kill-stale-sudo:
    monitor: enabled
    interval: 1m
    check:
      type: process
      name: /usr/bin/sudo
      user: root
      for: 120m            # alive at least 120 minutes (process start time)
    then:
      kill:
        signal: TERM       # optional, default TERM; TERM or KILL
        # escalate: true     # optional: follow the signal with SIGKILL for a survivor
        # term_timeout: 10s  # optional (escalate only): grace before SIGKILL
        # kill_timeout: 5s   # optional (escalate only): grace after SIGKILL
```

- **`signal`** is the signal to send, `TERM` (default) or `KILL`. It is validated
  by the same parser the daemon uses, so a typo or an inappropriate signal fails
  `config validate`.
- The kill target is gated by the same `kill_only_if` model used elsewhere:
  the matched PID's resolved exe must exactly equal `check.name`, and its real
  UID must resolve from `check.user`. An unresolvable exe is never killed.
- **`escalate: true`** turns the single signal into the stop-policy TERM→KILL
  model: send the signal, wait `term_timeout`, and — after **re-verifying the PID
  still matches this watch** (defending against PID reuse over the grace period) —
  send `SIGKILL` to a survivor.
- It fires with the same **edge-triggered, once-per-PID** semantics as the hook,
  and only on a **presence** fire (`for`/`cpu`/`memory`/`io`) — never on a `gone`
  fire, which has nothing to signal. Each signal delivery is recorded as a `kill`
  (or `kill-failed`) event visible in the watch's activity.
- `dry_run: true` and panic mode **suppress** the kill (a `dry-run` /
  `panic-suppressed` event is emitted instead), like hooks and non-console
  notifications.
- `kill` can stand alone (a pure kill watch) or accompany a `hook` and/or
  `notify`. It is **only valid on a `process` watch** (like `then.expand` is
  storage-only). Because it signals real processes, the daemon must have
  permission to do so (typically running as root). The absolute `name` plus
  `user` pair scopes which PIDs can be killed; every matching PID that crosses the
  condition.

Other resource types will be added as new check `type` values using the same
watch/hook structure.

## Global defaults

Only target-safe parts of `defaults` merge into configured targets:
`dry_run` applies to services and watches; `stop_policy`, `policy` and
`rule_window` apply to services; `restart_on_change` applies to services only
for the inherited `config`/`version` permission flags. Engine-wide settings (`interval`,
`max_parallel_checks`, `default_timeout`,
`operation_timeout`, `artifact_interval`, `startup_delay`, `backend`, `user_lookup`,
`user_lookup_timeout`, `reap_own_strays`, `state_cache_size`, the `retention_*`
windows and `rollup_interval`, and `service_restart_notice`) are daemon
configuration and never merge into a service.

A service's `reap:` block is deliberately **not** a `defaults` key either: one
global `kill_only_if` would hand every service the same kill authority over
processes none of them can name.

For service residual cleanup, `defaults.stop_policy.force_kill` accepts `false`,
`true` or `auto`. `true` requires an explicit `kill_only_if` selector. `auto`
reuses only strict named `processes:` identities that declare both `exe` and
`user`; it does not authorize a process by name or cmdline and leaves services
without a verified identity as `orphan_processes`.

`defaults.dry_run` is optional and defaults to `false`; a service or watch may
override it with its own top-level `dry_run`.

`restart_policy` is intentionally per catalog/configured service and does not
inherit from `defaults`: selecting an atomic init-backend restart requires an
explicit service-level decision. See
[restart strategy](services.md#restart_policy--restart-strategy).

`defaults.policy.cooldown` is **required and positive**: every resolved service
inherits a loop-prevention cooldown unless it overrides it.

`defaults.restart_on_change` controls only the inherited permission flags for
automatic restart-on-change sugar. It may not declare global `paths`, `apps`,
`libraries` or `messages`; those sources stay local to a catalog service or
configured service.

```yaml
defaults:
  restart_on_change:
    config: false   # block generated restart_on_change.paths rules by default
    version: true   # allow generated restart_on_change.apps/libraries rules
```

A service can override either flag in its own `restart_on_change` block. Missing
flags default to allowed, so existing service-local `restart_on_change` entries
keep their behavior.

`defaults.rule_window` is the **fallback firing window** for any rule that
declares neither its own `for` nor `within` (see the rules section). It accepts:

```yaml
defaults:
  rule_window:
    cycles: 1            # choose cycles or duration, not both
    # duration: 6m
    mode: consecutive    # consecutive (a `for` window) | within (a sliding window)
    # min_matches: 1     # mode: within only — optional, defaults to 1 (true at least once)
```

`cycles: 1` + `mode: consecutive` is also the built-in default (fire the moment a
rule's condition is true), so the shipped `sermo.yml` carries this block only as
a commented reference.
Raise `cycles` (e.g. `3`) or set `duration` (e.g. `6m`) to require a longer
consecutive window before every window-less rule fires, or use `mode: within`
with `min_matches` for a sliding window. A rule's own `for`/`within` always
overrides the fallback, and like the other per-service defaults it can be
overridden per catalog service or service.

## Resolution order

A service is resolved into a flat definition, lowest precedence first:

1. The effective global `defaults` (target-safe parts).
2. The `uses` daemon, or the `clone` chain, merged on top.
3. The service's own fields (highest precedence).
4. `${var}` expansion, once, over the merged result.
5. Validation of the flattened service.

```
global defaults  <  daemon (uses) or clone source  <  service overrides
```

`uses` and `clone` are taken **unexpanded**, so a clone can override a single
variable and have every `${var}` reference resolve to the new value.

## Merge rules

- Scalars and lists overwrite.
- Maps merge recursively.
- Named sections (`checks`, `preflight`, `processes`, `rules`)
  are maps keyed by name, so a child can override one field of one entry.
- Disable an inherited entry with `enabled: false`; delete it with
  `delete: true`.

## Per-host overrides (`<dir>.local`)

Every directory in `paths.services`, `paths.apps`, `paths.notifiers` and
`paths.watches` may have a sibling `<dir>.local`. Sermo loads it when it exists —
`services.local`, `apps.local`, `notifiers.local`, `watches.local`,
`networks.local`, `storages.local`, `mounts.local` — and folds each document onto
the one of the same `name:`:

```yaml
# /etc/sermo/services.local/prometheus.yml — this host moves a lot of data
name: prometheus
watches:
  alert-if-io-high:
    check: { value: 838860800 }
```

The layer exists because a fleet's servers do not share one workload. Packaged
thresholds are chosen to be true everywhere, which on any individual host makes
some of them wrong; `<dir>.local` is where that host says so, once, without
editing a generated file.

It is discovered from the layout rather than registered in `paths`, and that is
deliberate: `sermo.yml` is regenerated wholesale by the deployment scripts, so an
override registered there would be de-registered by exactly the event this layer
exists to survive. Listing a `.local` directory in `paths` is rejected.

- **A document merges onto its base when one exists, otherwise it is loaded as an
  ordinary document.** One rule: `watches.local` can retune a generated watch and
  can add a host-only one.
- **The [merge rules](#merge-rules) above apply unchanged** — maps merge
  recursively, scalars and lists overwrite, `enabled: false` disables an
  inherited entry and `delete: true` removes it.
- **The override sits above the host document**, so the order is
  `defaults < catalog < host document < host override`.
- **It is taken unexpanded**, like `uses`/`clone`. Redefining one variable
  therefore reaches every `${var}` that reads it in the base document.
- **The merged result is fully validated.** An override cannot bypass an
  invariant, and `sermoctl config validate` names the `.local` file in its
  diagnostics.
- **One override per name.** The four classified watch directories share a single
  namespace, so a watch may be overridden from any of their `.local` siblings —
  but only from one.
- **`templates.local` is the exception**: a template is a whole file with no named
  entries to merge, so `templates.local/<name>.yml` shadows the packaged template
  entirely rather than merging into it.


Worked examples (cloning, disabling, multiple instances) live in
[services](services.md#cloning). Catalog templates for installed
versions/instances use `%v`, `%n` and `%i`; see [versioned
services](services.md#versioned-services).

When simple `%v` or `%n` templates also have an active-slot binary without a
suffix, such as `php` next to `php8.4` or `python` next to `python3`, Sermo
materializes that unversioned entry automatically. Composite templates with
extra tokens do not infer an active slot from `versions.from`; declare
`versions.current_from` for compatibility entries such as `/usr/bin/java`
alongside Java version discovery. `current_from` may be a path or a list of
paths. Set `versions.unversioned: false` only when the marker-less or
`current_from` active slot should be ignored. A materialized name must not
collide with an explicit document in the same category; validation reports that
as a configuration error. When a template uses `${current}`, inventory listings
also mark a versioned entry as current when the active-slot wrapper and that
entry report the same `version_short`.

`versions.from` may be a backend-neutral path/list, or a map with `systemd` and
`openrc` branches. Map branches are exclusive: Sermo selects only the active
init backend from `engine.backend` or `SERMO_BACKEND`, falling back to detected
`${init}`. Catalog service templates should put tokens in `service:` instead;
active systemd/OpenRC units materialize their daemon instances for discovery.
When a configured service explicitly names a materialized instance in `uses:`,
that instance remains available while its unit is stopped or failed so
validation and monitoring can report its state. Linked apps own binary discovery
and validation.

## Binary resource variables

Declare executable candidates as a normal variable and select them through
`preflight.binary`:

```yaml
variables:
  binary:
    - /usr/bin/php-fpm${version}
    - /usr/sbin/php-fpm${version}
preflight:
  binary: { type: binary, path: "${binary}" }
```

The resource preflight entry narrows `${binary}` to the first candidate matching
the declared type. `binary` requires a regular executable file; `file` requires
a regular file; `lockfile` requires a regular file; `pidfile` requires a
regular file; `socket` requires a Unix socket. If none currently matches, Sermo
keeps the first non-empty candidate so the runtime preflight reports the bad
path explicitly instead of expanding to an empty string. Paths must be absolute
after templating.

### `${bindir}` search prefix

When the only difference between the candidates is the standard binary
directory, use the `${bindir}` prefix instead of listing them by hand. It
expands at load time into one candidate per standard search directory, in order:

```
/usr/bin → /usr/sbin → /usr/local/bin → /usr/local/sbin
```

So `binary: ${bindir}/mysqld` is shorthand for:

```yaml
variables:
  binary:
    - /usr/bin/mysqld
    - /usr/sbin/mysqld
    - /usr/local/bin/mysqld
    - /usr/local/sbin/mysqld
```

`${bindir}` is a prefix, not a standalone value: always write `${bindir}/<name>`.
It composes with `${version}` templates (`${bindir}/php-fpm${version}`) and may
be mixed with explicit paths inside a list when a binary also lives outside the
standard directories. It expands in `variables` and in the `versions.from` /
`versions.current_from` discovery globs (including their per-backend branches),
which name binaries in the same directories; it is not substituted anywhere
else. Because candidates resolve to the first one that exists,
the selected path is the installed one regardless of search order. For binaries
outside these four directories (e.g. `/opt/...`, `/usr/lib/...`), keep an
explicit path.

Use `variables.binary` plus an explicit
preflight entry for apps, daemons and services. Libraries use the same pattern
with `type: file`:

```yaml
name: glibc
variables:
  binary: /lib64/libc.so.6
preflight:
  file: { type: file, path: "${binary}" }
```

In this single-path form the check passes when `path` exists and is a regular
file. Add `non_empty: true` when a zero-byte file must fail too — useful for a
generated config or a certificate bundle that a failed build can leave empty:

```yaml
preflight:
  bundle: { type: file, path: /etc/ssl/certs/ca-bundle.crt, non_empty: true }
```

Command checks can declare variables too. `from: stdout` and `trim: true` are the
defaults; `default` is optional and otherwise empty. When the command succeeds,
those values are also attached to the result `data`. The built-in `version` and
`version_short` command names already export `version` and `version_short`; a
`version` command also derives `version_short` from stdout, so only special
values need an explicit `export:`:

```yaml
preflight:
  api:
    type: command
    command: ["/usr/bin/tool", "api-version"]
    export:
      api: { regex: "API ([0-9]+)", default: "" }
```

## Variables

```yaml
variables:
  host: 127.0.0.1
  port: 8080
watches:
  http:
    check:
      type: http
      url: "http://${host}:${port}/health"
```

- Variables are flat literal strings; a value must not itself contain another
  `${var}` (but `${env:...}` is allowed — see below). Catalog version/instance
  templates may use their template placeholders such as `${version}` or `${n}`
  in path variables before materialization.
- Expansion is a single pass: any `${...}` left afterward is an undefined
  variable and a validation error.
- Numeric fields (`port`, `expect_status`) accept an int, a quoted string, or a
  `${var}`, and are parsed after expansion.

### Global custom variables (`defaults.variables`)

Declare variables once under `defaults.variables` and use them as `${name}`
**anywhere** values are expanded — every service, daemon and host `watches:`
entry:

```yaml
defaults:
  policy: { cooldown: 5m }
  # dry_run simulates automatic service, storage and watch actions without
  # executing service operations, hook/expand/kill actions, or non-console
  # notifications. Manual operator actions are unaffected. A target-level
  # dry_run setting overrides the default.
  dry_run: false

  variables:
    custom_var1: /opt/myapp
    custom_var2: 8443
    libdir: [/usr/lib64, /usr/lib]   # list = first existing path
```

- **Precedence:** a service's own `variables.X` wins over `defaults.variables.X`,
  which wins over the builtins (`${host}`, `${port}`, `${hostname}`, …). So a
  custom `host`/`port` overrides the builtin for every service that does not set
  its own.
- **Names:** must be unique (a duplicated YAML key is a load error) and must not
  be a **reserved name** — the selection keywords `all`/`none`/`default` and the
  runtime tokens `date`/`event`/`action` are rejected. `binary` is allowed and is
  resolved through `preflight.binary` when it carries path candidates. Builtin
  names (`host`, `port`, …) are allowed and override the builtin (see precedence).
- Values support `${env:...}` and list-first-existing exactly like per-service
  variables. They cannot contain another `${var}` (no nesting), like any variable.
- An undefined `${custom_x}` is a validation error in services **and** watches.

### Secrets from the environment

`${env:NAME}` resolves to the environment variable `NAME` **anywhere** in the
config — service fields *and* the global blocks (notifier DSNs/webhooks, the web
password, …) — so secrets are never written in the file:

```yaml
watches:
  api:
    check:
      type: http
      url: "https://api.example.com/health"
      headers:
        Authorization: "Bearer ${env:API_TOKEN}"   # read from the daemon's env

notifiers:
  ops:
    type: email
    dsn: "${env:SMTP_DSN}"
```

- A shell-style default is supported: `${env:NAME:-fallback}` uses `fallback` when
  `NAME` is unset or empty.
- An unset variable expands to its default (or empty) and is **never** a
  validation error — but if it feeds a required field (a notifier `dsn`, the web
  `password`), that field then reads as missing. Run `config validate` with the
  same environment as the daemon (e.g. systemd's `EnvironmentFile`) to check the
  secrets resolve.
- Unlike `${var}`, `${env:...}` is resolved separately, so it also works in the
  global config (which has no `variables` section) and inside a variable's value.

## Validating

```sh
sermoctl config validate          # whole Sermo configuration
```

`config validate` exits `78` on a configuration error. See
[rules](rules.md) for what each section may contain.

## Diagnostics

`config validate` checks that the configuration is *well-formed*. When
`engine.diagnostics` is set, `sermod` also runs scheduled checks against the
**live host** and appends each snapshot to the log file.

Each JSON line includes `time` (RFC3339), `errors`, `warnings` and a `findings`
array. Every finding has `level` (`error` / `warning` / `info`), `scope` and
`message`. The checks cover:

- **Configuration** — every `config validate` issue (errors).
- **Interval alignment** — per-check `interval`s that are **not a multiple of the
  global resolution** (`engine.interval`) or below it, so they will be rounded
  (see [per-check interval](#per-check-interval)).
- **Host resources** — referenced things that **do not exist on this host**:
  network interfaces (`net` watches), files/directories (`storage`/`count` checks,
  `file` watches), **mount points** (a `storage` check with mount conditions whose
  path is not currently mounted), **block devices** (`diskio` names without a
  `/sys/class/block` entry; `hdparm`/`smart` device paths) and **kernel PSI**
  (a `pressure` check on a kernel without `/proc/pressure` — `CONFIG_PSI=n` —
  which would otherwise silently never fire).
- **Locks** — malformed lock files under `<paths.runtime>/locks`.

Rotate and retain `engine.diagnostics` with your host's log tooling; Sermo does
not prune that file.

To reclaim old state-database history intentionally, use:

```sh
sermoctl state compact                  # configured retention, then VACUUM
sermoctl state compact --before 720h    # also drop history older than 30 days
sermoctl state compact --before 2026-01-01T00:00:00Z
```

`state compact` runs the same consolidation and prune pass `sermod` runs on its
`engine.rollup_interval`, then checkpoints and vacuums the SQLite state database so
freed pages can return to the filesystem. `--before` additionally drops whatever
history remains older than an explicit cutoff, at every resolution; it must be a
positive duration or a non-future RFC3339 timestamp. A bucket that straddles the
cutoff is kept, so the cutoff never deletes samples newer than itself.
