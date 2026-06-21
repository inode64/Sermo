# Configuration

Sermo configuration is split by target type: **catalog daemon/app/lib/pattern
definitions**, **services** as concrete monitored instances, **notifiers** as
delivery targets, **watches** as host-level monitors, and **mounts** as
fstab-backed mount units. Watch and notifier files are global fragments with a
top-level `watches:` or `notifiers:` map; those fragments do not use `kind:`.

New configuration must use one YAML file per target. That means one catalog app,
daemon, lib or pattern per file; one `kind: service` per file; one `kind: mount`
per file; one notifier per file; and one host watch per file (`storage`,
`network`, `uplink`, `load`, and other watch fragments). Global fragment files
still have the top-level `watches:` or `notifiers:` map, but that map must
contain exactly one named entry. This keeps generated configuration easy to
diff, replace and clean up per target.

> **Complete annotated example.** [`docs/sermo-all.yml`](sermo-all.yml) shows
> every configuration surface in one place — global config, watches, and one
> document of each kind (daemon, app, lib, patterns, service, mount), plus a
> cloned service example — and is validated by the test suite, so it cannot
> drift from the schema. It is a reference bundle only; real deployments keep
> one target per file. The shipped operational config is `examples/sermo.yml`.

## Layout

```
/etc/sermo/sermo.yml              global config
/usr/share/sermo/catalog/{services,apps,libs,patterns}/*.yml   packaged catalog
/usr/share/sermo/examples/        packaged examples operators may copy/adapt
/etc/sermo/catalog-available/{services,apps,libs,patterns}/*.yml   user catalog definitions
/etc/sermo/services/*.yml concrete service documents
/etc/sermo/apps/*.yml     host-specific app documents
/etc/sermo/mounts/*.yml   fstab-backed mount documents
/etc/sermo/notifiers/*.yml notifier fragments
/etc/sermo/storages/*.yml storage watch fragments
/etc/sermo/networks/*.yml network watch fragments
/etc/sermo/watches/*.yml  generic host watch fragments
/etc/sermo/templates/*.yml notification templates
```

The directories Sermo reads come from `paths` in the global config:

```yaml
paths:
  catalog:
    - /usr/share/sermo/catalog
    - /etc/sermo/catalog-available
  services:
    - /etc/sermo/services
  apps:
    - /etc/sermo/apps
  notifiers:
    - /etc/sermo/notifiers
  storages:
    - /etc/sermo/storages
  networks:
    - /etc/sermo/networks
  watches:
    - /etc/sermo/watches
  mounts:
    - /etc/sermo/mounts
  runtime: /run/sermo
  state: /var/lib/sermo
  templates: /etc/sermo/templates
```

Directory lists under `paths.catalog`, `paths.services`, `paths.apps`,
`paths.notifiers`, `paths.storages`, `paths.networks`, `paths.watches` and
`paths.mounts` accept either a path string or an explicit mapping:

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
For `paths.catalog`, catalog documents must live under the immediate
`services/`, `apps/`, `libs/` or `patterns/` category directories. Those
category directories are part of the catalog layout and are read even when
`recursive` is false; `recursive: true` only controls directories below those
category directories.

`paths.runtime` is the root for named runtime locks (`<runtime>/locks`, one file
per lock named `<service>[.<name>].lock`) and internal operation locks
(`<runtime>/ops/<service>.lock`). It lives on tmpfs and is wiped on reboot.
`paths.locks` is **not** supported. See [Locks](safety.md#locks) for the TTL and
stale-reclaim semantics.

If `paths.services` is omitted, Sermo falls back to `services/` next to the
loaded `sermo.yml` file. If `paths.apps` is omitted, Sermo falls back to `apps/`
next to that same file. With the standard `/etc/sermo/sermo.yml` this means
`/etc/sermo/services` and `/etc/sermo/apps`.

Every new service, notifier or watch fragment under these directories should be
isolated in its own `.yml` file, even when several targets are generated in the
same wizard run.

If `paths.mounts` is omitted, Sermo falls back to `mounts/` next to the loaded
`sermo.yml` file. With the standard `/etc/sermo/sermo.yml` this means
`/etc/sermo/mounts`. Mount documents are intentionally separate from service
documents and watch fragments because they are operator actions, not monitored
services.

Use `/run` for runtime paths in Sermo configuration and examples. `/var/run` is
the historical compatibility alias for `/run`; do not write new `/var/run`
pidfiles, sockets or runtime directories. If an init system reports `/var/run`,
normalize the configured path to the equivalent `/run/...` spelling.

Before adding a new runtime path, resolve it on the target host:

```sh
readlink -f /var/run/example.pid
namei -l /var/run/example.pid
```

If the path resolves through a symlink, configure the canonical target path
instead. This is especially common for `/var/run` → `/run`, but can also happen
with app-specific runtime directories.

Catalog apps may declare `version_from: <app-name>` when a different binary from
the same package has the authoritative version probe. The app still checks its
own `variables.binary` for installation and health; `version_from` only fills
the displayed
version when the app has no local version result. Local `health`, `version` and
`version_short` commands still win. The referenced app must be another catalog
app addressed by its canonical name, and `version_from` chains must not cycle.
This is not an operational dependency and does not inject preflight checks into
services.

When a daemon or service lists apps, every app variable is also available to that
daemon/service with a normalized app-name prefix: an app with
`variables: { binary: /usr/bin/cupsd, cups_config: /usr/bin/cups-config }`
exposes `${cupsd_binary}` and `${cupsd_cups_config}`. Command preflight entries
named `version` or `version_short` also declare `${cupsd_version}` and
`${cupsd_version_short}` with empty defaults; an explicit command `export:` may
declare additional variables. At runtime, successful command checks publish the
same exported names in the check result `data`; a `version` command also derives
`version_short` from its stdout, preferring `major.minor[.patch]` and accepting
guarded integer-only `version N` output, including date-coded releases, when no
dotted version is present. Dashes and other non-alphanumeric characters become
underscores. This lets a service reuse binary paths owned by one or more apps
without naming collisions. When exactly one app is linked, its variables are also
exposed without the prefix as defaults, so a service can use
`${binary}` while the app remains the owner of the binary path. A local
`variables:` entry with the same prefixed or unprefixed name still wins for
host-specific overrides. When several apps are linked, use the prefixed names to
avoid ambiguity.

`paths.state` (default `/var/lib/sermo`) is the root for the persistent state
database `sermo.db` (SQLite). Unlike `paths.runtime`, it survives reboots, which
is what lets a service's or watch's `monitor: previous` flag restore its last
monitoring state. It also stores automatic-remediation cooldown/backoff and rule
`for`/`within` window progress, so restarting `sermod` does not reset when a rule
may act again. SLA and check measurements plus service and daemon process metric
history shown in the web UI live there too. The schema is versioned and migrated
forward automatically, so future features can add tables without a manual
upgrade.

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

## Mount units

`kind: mount` defines a named mount target controlled by `sermoctl mount` and
`sermoctl umount`. Mount units live under `paths.mounts` (default
`/etc/sermo/mounts`) and deliberately use `/etc/fstab` as the source of truth:
the YAML contains the mount path and Sermo policy only, not `source`, `fstype`,
`options` or class metadata.

```yaml
kind: mount
name: mount-backup
display_name: Backup mount
category: storage

path: /mnt/backup
refcount: true

umount:
  term_timeout: 12s
  kill_timeout: 5s
  allow_sigkill: false
  allow_lazy: false
```

The CLI accepts either the configured name or the absolute mount path:

```sh
sermoctl mount mount-backup
sermoctl mount /mnt/backup
sermoctl umount mount-backup
sermoctl umount /mnt/backup
sermoctl mount status mount-backup
sermoctl mount list
```

With `refcount: true` (the default), every successful `mount` increments
Sermo's runtime counter and `umount` decrements it. The real `umount` only runs
when the counter reaches zero; if the path is not mounted yet, the first
`mount` runs `mount <path>` and requires a matching `/etc/fstab` entry. The
counter is kept under `<paths.runtime>/mounts/state`, and each mount operation
uses a per-target lock under `<paths.runtime>/mounts/ops`.

Normal unmount is conservative: Sermo first runs `umount <path>`. If the mount
is busy, it reports the processes using the path. It only signals blockers when
`umount.allow_sigkill: true` or `stop_policy.force_kill: true` is explicitly
set, and validation then requires a restrictive `stop_policy.kill_only_if`
selector. Lazy unmount (`umount -l`) is also off by default and only used when
`umount.allow_lazy: true`.

## Engine settings

The `engine` block is daemon-wide configuration consumed by `sermod`; it never
merges into a service:

```yaml
engine:
  backend: auto               # auto | systemd | openrc
  interval: 30s               # default cycle interval; per-service overridable
  max_parallel_checks: 8        # bound on concurrent checks across all services
  max_parallel_operations: 2  # bound on concurrent start/stop/restart/reload/resume operations
  default_timeout: 10s        # default per-check timeout
  operation_timeout: 90s        # outer deadline for safe service actions
  startup_delay: 0            # grace period before the first cycle (0 disables)
  user_lookup: auto           # auto | native | getent | numeric
  user_lookup_timeout: 250ms  # per-getent lookup timeout; cached in-process
```

`engine.interval` is the default cadence at which every service's checks are
run. Each service runs all of its checks once per cycle.

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

`engine.max_parallel_operations` limits how many safe service actions
(`start`, `stop`, `restart`, `reload`, `resume`) may run at the same time across automatic
remediation, the web UI and `sermoctl`. It is separate from
`max_parallel_checks`: many checks can run while only a few service operations proceed.
Slots are shared across processes under `<paths.runtime>/op-slots` (default
`/run/sermo/op-slots`); when all slots are busy, another action waits until one
is free. The default is `2`.

`engine.operation_timeout` is the outer deadline for a safe
start/stop/restart/reload/resume. The engine may raise it per service when the resolved
`stop_policy` needs longer (graceful stop plus signal escalation). The same
limit applies to automatic remediation, `sermoctl` actions and web-initiated
operations. When the web UI is enabled, `sermod` also sets the HTTP server's
write timeout from the longest resolved deadline so a long operation is not cut
off mid-request. The default is `90s`.

`engine.startup_delay` is a non-negative duration that holds the daemon before
it starts its first check cycle, giving the host time to finish booting so
services that are still coming up are not flagged or remediated prematurely. The
wait applies once, on startup, before any worker runs; a shutdown signal during
the wait aborts cleanly without starting any worker. The default `0` disables it.

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

When `sermoctl daemon reload` asks the running daemon to reload, `sermod` reads
the configuration from the path passed to `sermod run --config` (the same file
`sermoctl` uses). `sermod` validates the new config, rebuilds its service workers
and host watches, and swaps them in without restarting the process. Per-service
runtime state is preserved across reload:
monitoring cycle counters and watched-file baselines for `changed:` conditions
stay in memory, while remediation cooldown/backoff and rule `for`/`within`
windows are also persisted in `paths.state` and survive a full `sermod` process
restart. Invalid config, or
a config with no included services or watches, is rejected and the current
generation keeps running; a `reload` or `error` event is recorded. Reload does
not repeat `startup_delay` and does not mark `/readyz` as shutting down.
Per-service CPU rate baselines are reset only when a service is removed from the
running config; persisted metric and event history remains in `paths.state`
until normal retention or an explicit `sermoctl state compact`.

Trigger a daemon configuration reload with:

```sh
sermoctl daemon reload
```

Only one `sermod` instance may run per `<paths.runtime>` directory (default
`/run/sermo`). At startup it takes an exclusive lock on
`<paths.runtime>/sermod.lock`; if another instance already holds it, the new
process logs a warning, exits with status **1**, and does not start a second
monitor loop.

The daemon writes `<paths.runtime>/sermod.pid` (default `/run/sermo/sermod.pid`)
at startup to make `sermoctl daemon reload` reliable. If no pidfile is present,
`sermoctl daemon reload` falls back to locating the running `sermod` process by
name — a native scan of `/proc`, no external `pidof`/`pgrep` needed.

`sermoctl daemon reload` reloads `sermod`'s own configuration (as above).
`sermoctl reload <service>` is a different operation — it reloads *that service*
in place through the engine (preflight → reload → health). How a service reloads,
including the `reload:` block that lets Sermo signal a daemon when its init unit
has no reload, is documented in
[daemons.md](daemons.md#reload-on-config-change-reload_on_change).

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

An individual check may run **less often** than the worker cycle with
`interval`. The worker keeps ticking at its resolution; the check runs every
`round(interval / resolution)` cycles and **reuses its last result** between
runs, keeping check caches and rule windows complete.

```yaml
interval: 30s            # the service resolution (or engine.interval)
checks:
  http:
    type: http
    url: "http://127.0.0.1/health"   # runs every cycle (30s)
  version:
    type: command
    command: ["/usr/bin/nginx", "-v"]
    interval: 30m                     # runs every 60 cycles (30m / 30s)
```

A per-check `interval` **cannot be shorter than the resolution** and should be a
**multiple** of it. If it isn't, the daemon rounds it to the nearest multiple
(at least one cycle) and **logs a warning at startup** — it never fails to start.

## Web UI

The daemon can serve a small web dashboard to view services and host watches.
Admins can monitor/unmonitor both, and can start/stop/restart/reload/resume services
over the same safe operation engine the CLI uses.

A service normally resolves to a systemd/OpenRC unit. It can instead declare a
per-service `control:` target for non-init resources: `control.type: libvirt`
for QEMU/libvirt VMs or `control.type: docker` for Docker containers. Those
targets still use the same locks, guards, preflight checks and operation
timeouts; see [daemons](daemons.md#control-docker--docker-containers).

Below the services table the dashboard lists the **installed applications** (the
catalog app daemons whose binary is present), showing each application's name and
short version; an app `health` command, when configured, decides OK/error from
its exit code before the version command is considered. If no `health` command
is configured, the `version` command is the fallback probe while fetching the
displayed version. The list is sortable by name, category or version, and
expanding a row reveals the full version string, the binary's file location and
its permissions. When a version is inherited through `version_from`, the API row
includes `version_source` with the provider app name. Services and applications
can be filtered and grouped by their top-level `category` metadata field.
The same data is available from `sermoctl apps` and `GET /api/applications`.
The dashboard caches the list for up to 30 seconds, so auto-refreshes do not
rerun every app version probe.
For an editable panel-by-panel map, see
[webui-representation.md](webui-representation.md).

**The web UI is only activated when `web.port` is explicitly defined.** If the
`web:` block is omitted, or if a `web:` block is present without a `port` key
(even if other keys such as `address` are set), the HTTP server is not started.
At startup `sermod` logs a warning: "web ui disabled; no port will be opened".

```yaml
web:
  address: 127.0.0.1        # optional, default 127.0.0.1 (loopback only)
  port: 9797                # REQUIRED to activate the web UI (9797 recommended)
  disable_diagnostics: true # optional, default false — hide the Diagnostics panel
```

- **Activation rule:** the web UI ("servicio web") is **not started** unless
  `web.port` is present and valid. Omitting the key (or the whole `web:` block)
  leaves the dashboard disabled; `sermod` logs the exact reason at startup.
- **Recommended port: `9797`.** It is easy to remember and avoids the common
  monitoring ports (`9090` Prometheus, `9093` Alertmanager, `9100` node-exporter,
  `3000` Grafana, `8080`).
- **Authentication** is optional but recommended before exposing it. Without it,
  the UI binds to **loopback (`127.0.0.1`) by default** and is fully open.
- **`disable_diagnostics`** (optional, default `false`) — when `true`, the
  dashboard hides the **Diagnostics** panel and `GET /api/diagnostics` returns an
  empty result; `POST /api/diagnostics/clean` returns `404` with `diagnostics are
  disabled`. The underlying `sermoctl diagnose` command is unaffected.

### Authentication

Set passwords on the `web` block for HTTP Basic auth with two roles:

```yaml
web:
  port: 9797
  password: "s3cret"           # admin: read + actions (start/stop/restart/reload/resume, monitor)
  guest_password: "lookonly"   # optional: a read-only login
  guest: true                  # optional: allow anonymous read-only access
```

- **admin** — full access. Granted by `password`.
- **guest** — **read-only**: can view everything but every action (a `POST`) is
  refused with `403`. Granted by `guest_password`, and/or to anyone when
  `guest: true` (anonymous read-only).

The **password**, not the username, selects the role — at the browser prompt enter
any username and the admin or guest password; passwords are compared in constant
time. With `guest: true` the dashboard loads read-only without a prompt, and a
**"log in"** link (`GET /login`) triggers the prompt to escalate to admin. The UI
hides the action buttons for guests; the API enforces it regardless. When no
password/guest is set, auth is disabled (open) and the daemon **logs a warning**
at startup. `GET /api/whoami` reports the caller's role.

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

- The proxy and the dashboard share an **origin**, so the `X-Sermo-CSRF` header and
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
- `GET /api/services` — **configured runtime** service list (the `kind: service`
  files under `paths.services`): name, `state` (`disabled`, `running`,
  `paused`, `stopped`, `monitorized`, `failed`), backend status, `check_health`,
  `checks_failing`, active locks, monitor state/source/timestamp, backend, unit,
  cooldown, remediation state, next eligible action and last event. This is not
  `sermoctl services`, which lists catalog daemon profiles — see
  [cli.md](cli.md#catalog-inventory).
- `GET /api/services/{name}` — service detail: latest checks, rolling SLA, named
  runtime locks, discovered processes, automatic remediation policy state and
  rule window progress.
- `GET /api/services/{name}/sla?since=24h` — per-minute availability history;
  `since` is a duration, default 24h, capped at the 366-day (~1 year) retention.
- `GET /api/services/{name}/metrics?check=NAME&since=24h` — check latency
  history + summary. Add `metric=KEY` for a named numeric metric published by
  that check, see below.
- `GET /api/services/{name}/runtime?since=24h` — service process tree CPU,
  memory and IO history.
- `GET /api/services/{name}/events?limit=N` — events for one service.
- `GET /api/watches` — host watches, monitor state, conditions, notifications,
  live readings when available and recent activity.
- `GET /api/notifiers` — configured notifier targets.
- `GET /api/applications` — installed catalog applications.
- `GET /api/daemon` — daemon/backend/runtime settings and host uptime.
- `GET /api/daemon/metrics?since=24h` — persistent sermod CPU, memory and IO
  history for the current daemon process, plus current PID, file descriptors and
  threads.
- `GET /api/host` — current host-level CPU, memory and load metrics.
- `GET /api/locks` — named runtime locks with TTL, owner status, age, blocked
  actions and release eligibility.
- `GET /api/activity` — recent activity summary used by the dashboard header.
- `GET /api/monitoring` — monitored vs paused service counts.
- `GET /api/events?limit=N` — global event feed, newest first. Optional filters:
  `service`, `watch`, `kind`, `status` and `only_errors=1`.
- `GET /api/diagnostics` — [diagnostics](#diagnostics) findings with `time`
  (RFC3339), `level`, `scope` and `message`; includes malformed lock files under
  `<paths.runtime>/locks`.
- `GET /api/ops` — global operation slot usage: `{in_use, total}` for
  `engine.max_parallel_operations`.

State-changing endpoints are CSRF-protected and require admin permissions when
auth is enabled:

- `POST /api/services/{name}/preflight` — run the same preflight checks as
  `sermoctl preflight SERVICE`, without starting or stopping anything.
- `POST /api/services/{name}/{action}` — service action. `action` is `monitor`,
  `unmonitor`, `start`, `stop`, `restart`, `reload` or `resume`;
  start/stop/restart/reload/resume go through the safe operation engine.
- `POST /api/watches/{name}/{action}` — host watch action. `action` is
  `monitor`, `unmonitor` or `expand`.
- `POST /api/locks/{service}/release?name=NAME` — release an inactive
  stale/expired named runtime lock; active locks are refused.
- `POST /api/events/clear?before=TIME` — clear the persisted event/activity log;
  `before` may be RFC3339 or a duration. Omit it to clear all events.
- `POST /api/diagnostics/clean` — when diagnostics are enabled, remove stale
  control state for services/watches no longer configured; metric, SLA and event
  history is kept. Returns `404` while `web.disable_diagnostics` is `true`.
- `POST /api/reload` — request a `sermod` configuration reload, equivalent to
  `sermoctl daemon reload`.

### Liveness (`/livez`)

`GET /livez` is a liveness probe for the daemon: if its web server answers, the
process is alive, so it always returns **200**. A plain request returns
`text/plain` body `ok`; `GET /livez?verbose` returns JSON with `status`, `uptime`
(and `uptime_seconds`), `started_at`, `now`, the number of `services`, and the Go
runtime version. Unlike every other endpoint it is served **without
authentication** (and is exempt from CSRF), so a monitor, load balancer, container
orchestrator or the reverse proxy can probe it with no credentials:

```sh
curl -fsS http://127.0.0.1:9797/livez            # -> ok
curl -fsS http://127.0.0.1:9797/livez?verbose    # -> {"status":"ok","uptime":"3h12m0s",...}
```

It reports process liveness only; for configuration/host/database health use
[diagnostics](#diagnostics).

### Readiness (`/readyz`)

`GET /readyz` is a readiness probe: it returns **200** only after `sermod` has
finished `engine.startup_delay` (if any) and started its service workers and host
watches. During the startup grace period, or while the daemon is shutting down, it
returns **503**. A plain request returns `ok` or `starting` / `shutting_down` as
`text/plain`; `GET /readyz?verbose` returns JSON with `ready`, `status`, `backend`,
`services`, `watches` and an optional `message`. Like `/livez`, it is served
**without authentication**:

```sh
curl -fsS http://127.0.0.1:9797/readyz                 # -> ok (when monitoring)
curl -fsS http://127.0.0.1:9797/readyz?verbose         # -> {"ready":true,"status":"ok",...}
```

Use `/livez` to know the process is alive; use `/readyz` before sending traffic or
to gate a reverse proxy until monitoring has actually begun.

Events are the daemon's activity — actions, alerts, suppressions, hook/notify
results and errors — kept in an in-memory ring (the last 1000); they also go to
the daemon log. `limit` defaults to 100 (max 1000). The dashboard shows a global
feed; a service's detail shows its own events.

The detail's check results are the **latest observed** by the worker (published
each cycle), so they cost nothing to view and reflect each check's own cadence
(see [per-check interval](#per-check-interval)); a check not run yet shows "not
run yet". The Graphs section uses one window selector for SLA and runtime
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
`read`/`cached`, `sensors` `temp`/`fan`, `smart` `temperature`/`wear` or `edac`
`ce`/`ue`; in that case `unit` is the metric's unit instead of `ms`.
Measurements are kept per minute for roughly a year (pruned like the SLA
samples); a check that only runs every N cycles ([per-check
interval](#per-check-interval)) records a sample only when it actually runs, so
the average is not skewed.

The `Daemon / Engine settings` process charts use the same persistent state
database for sermod's own CPU, memory and IO history, so those graphs survive a
daemon restart or host reboot. They are pruned to the same 366-day (~1 year)
retention window.

The service detail's CPU, memory and IO charts use the same persistent state
database for each service process tree, so those graphs also survive a daemon
restart or host reboot. They start filling as soon as the service is monitored;
CPU and IO rates need two cycles before the first rate point exists, while
memory can render from the first process sample. Runtime metric buckets are
pruned to the same 366-day (~1 year) retention window.

Web-triggered monitor changes are recorded with source `web` in the state store
(`cli`, `config` and `daemon` are the other values). The dashboard and
`GET /api/services` / `GET /api/watches` expose `state`, `monitor_source` and
`monitor_changed_at` so a running/paused/stopped unmonitored service or an
unmonitorized watch shows who paused it and when. Host watches do not have
service-manager `running` or `stopped` states; the dashboard filters them as
`ok`, `failed`, `unmonitorized` or `disabled`.
Operations take the per-service operation lock, so they never overlap a worker's
action on the same service.

Because the daemon runs as root, the UI is hardened: it binds to loopback by
default, supports auth (above), sets HTTP timeouts, and requires an
**`X-Sermo-CSRF`** header on every action (POST) request — the dashboard sends it;
an API client must too (e.g. `curl -H 'X-Sermo-CSRF: 1' -X POST …`). This blocks
cross-site request forgery from a browser. See
[safety](safety.md#trust-model).

## Availability (SLA)

The daemon records one availability sample per monitoring cycle per service, so
you can see how often each service has been healthy over time. No configuration
is needed — it is on for every monitored service.

A service is **available** in a cycle when none of its **required** checks
failed. Optional checks (warnings) do not affect it, and a service with no
required checks is always available. Health-style checks (`tcp`, `http`,
`service`, `process`, etc.) fail when `OK=false`; condition-style checks
(`cert`, `fds`, `oom`, resource thresholds, etc.) fail only when the alerting
condition fires. Samples are accumulated into per-minute buckets in the state DB
(`/var/lib/sermo/sermo.db`); the daemon prunes buckets older than a year on
startup.

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

- **`teams`** — posts to a Microsoft Teams **incoming webhook** (a Teams
  Workflows / Power Automate "when a webhook request is received" URL).
  - **`webhook`** — the workflow's HTTP POST URL. The notification is sent as
    an Adaptive Card: the subject as the bold lead line, the detail (the
    `SERMO_*` fields) in a monospace block.

```yaml
notifiers:
  ops-teams:
    type: teams
    webhook: "https://prod-01.westeurope.logic.azure.com:443/workflows/…"
```

The set of notifier **types is pluggable** — new transports (`discord`, …) are
added without touching watches or rules (each registers a builder in
`internal/notify`). A new transport looks the same: a `type` plus its own
fields, addressed by name.

Set **`enabled: false`** on any notifier to keep it defined but skip delivery.
Disabled notifiers may still be referenced by `notify` selections.

`sermoctl services --notify NAME[,NAME]` sends an ad-hoc services inventory
report through configured notifiers. Email notifiers receive a multipart
plain-text/HTML message with summary cards and a service table; Slack and Teams
receive the text fallback. `--notify all` targets every enabled notifier. The
CLI renders this report directly; notifier templates are not used.

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

## Host watches

`watches` monitor host-level resources independently of any service and run a
**hook** (a local command) and/or send **notifications** (to named `notifiers`)
when a threshold is crossed. They are daemon configuration; they never merge into
a service.

> **Tip — generate configuration interactively.** `sermoctl wizard` can write
> three different surfaces. Watch assistants (`volume`, `net`, `uplink`) print a
> `watches:` preview and, if accepted, write one watch per file under a
> watch-type directory such as `/etc/sermo/storages` or
> `/etc/sermo/networks`; the wizard adds that directory to the matching `paths.*`
> (writing a `.bak` first). Service assistants (`service`, `docker`, `vm`) write
> one `kind: service` file per target under `services/` and ensure that
> `paths.services` loads it; `docker` and `vm` add `control.type: docker` or
> `control.type: libvirt` plus matching read-only checks. The mount assistant
> (`mount`) lists `/etc/fstab` mount points and writes safe `kind: mount` files
> under `paths.mounts`; it does not mount or unmount while generating config.
>
> `sermoctl wizard volume` creates storage checks for mounted local and
> network/distributed filesystems (threshold as a percent or size, optional
> auto-expand for LVM-backed filesystems). `sermoctl wizard net` covers interface state,
> errors, speed and address; type `active` to pick currently up non-loopback
> interfaces. `sermoctl wizard uplink` generates the layered internet-uplink set
> for an interface: link state, assigned address, default route, bound ping and
> DNS resolution through the system resolver; type `default` to use the detected
> default-route interface.
> `sermoctl wizard service` detects installed catalog daemons and enables them
> with `kind: service` files (see [daemons](daemons.md)); when several services
> are selected, port overrides are skipped unless explicitly reviewed, and known
> config files can be added as a periodic `checks.config` entry with a default
> `60m` interval. Run with no argument to choose from the list.
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
> `previous`) and an optional check interval; `kind: mount` files are not
> monitored entries, so the mount assistant skips those questions. See
> [wizards](wizards.md) for the full flow.

A watch's `then` block (when present) declares the actions taken when it
fires — a `hook`, a `notify` list, an `expand` (storage only), or any
combination.

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
condition persists (the `firing` event is still recorded each cycle for the web
UI, and the `hook` still runs each cycle). When the watch clears and later fires
again, the next episode notifies afresh. To get a periodic **reminder** while a
watch stays firing, set `then.notify_interval` to a positive duration: the
notification is re-sent once that interval elapses. It only affects delivery, so
it requires `notify` targets. Both the edge-triggered default and
`notify_interval` apply to the standard watch types (`storage`, the single-shot
service checks, and the `net`/`icmp`/`swap` metric watches). The
`file` and `process` watches have their own notification model — one event per
changed path or matching pid — and ignore `notify_interval`.

```yaml
watches:
  storage-root:
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

Use `then.dry_run: true` when you want to keep the watch and its actions wired
for a trial run, but you do not want any side effects yet. The watch still runs
its check and window, emits the normal `firing` event when it would fire, then
emits a `dry-run` event describing the actions it would run. It does **not**
execute `hook`, send `notify`, or run `expand`. `dry_run` is a modifier on an
explicit action block, so it must be paired with a real `hook`, `notify`, or
`expand` action; by itself it is not an action.

```yaml
watches:
  load:
    monitor: previous
    check:
      type: load
      per_cpu: true
      load5: { op: ">", value: 1.5 }
    for: { cycles: 3 }
    then:
      dry_run: true
      hook: { command: [/usr/local/bin/sermo-load-alert.sh] }
      notify: [ops-email]
```

Use `dry_run` for host watches while you are proving thresholds, hook argv/env,
notifier routing or `then.expand` policy gating. If an expansion would currently
be blocked by policy, the `dry-run` event reports the suppression, but dry-run
does not advance cooldown/backoff state. Remove it when the action should
actually execute. If you only want a long-term dashboard/log signal, omit
`then` entirely or use `notify: [none]`; those are monitor-only configurations,
not action rehearsals.

`dry_run` and remediation `shadow` are intentionally separate:

- `then.dry_run` belongs to a host watch under `watches:`. It skips watch
  side effects: `hook`, `notify` and `expand`.
- `remediation.shadow` belongs to service remediation. It evaluates service
  remediation rules, `for`/`within` windows, guards and policy, then emits
  `shadow` events without running service operations such as `restart`, `start`,
  `stop` or `reload`. It does not suppress host watch hooks.

```yaml
kind: service
service: apache
remediation:
  shadow: true
rules:
  restart-http:
    type: remediation
    if: { failed: { check: http } }
    then: { action: restart }
```

A watch supports the same top-level `monitor` flag as a service/daemon:
`enabled` (the default) forces monitoring on at daemon start/reload, `disabled`
builds the watch but starts it paused, and `previous` restores the last persisted
runtime state. This is distinct from `enabled: false`, which disables the watch
entry structurally and no runtime watch is built. Use `monitor: disabled` when
you want the watch visible in the web UI and available for an admin to resume
with **monitor**.

Watch directories (`paths.storages`, `paths.networks` and `paths.watches`) contain
watch fragments. A watch fragment is a normal YAML file with a top-level
`watches:` map and exactly one watch:

```yaml
# /etc/sermo/storages/storage-root.yml
watches:
  storage-root:
    monitor: previous
    check: { type: storage, path: /, used_pct: { op: ">=", value: "90%" } }
    then:
      notify: [ops-email]
```

Keeping wizard output in separate files makes it easy to remove or review one
watch without rewriting the whole global config. Notifier fragments follow the
same one-entry rule under a top-level `notifiers:` map in `paths.notifiers`.

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
  at least one, and add `for: { cycles: N }` to require N consecutive cycles.
  Predicate values share one grammar across every level check: a `*_pct` field
  accepts a number or an explicit `%` suffix in 0–100 (`90` or `"90%"`), a
  `*_bytes` field **requires** a size suffix (`K`/`M`/`G`/`T`, e.g. `10G`), and
  any other field is a plain number. A
  **stateful check** (counter deltas — net `errors`, swap `io`, `oom`; and change
  detection — net/icmp `state`/`speed`/`latency`, `file`, `process`; and rate
  computation — `diskio`) compares
  against a baseline carried across cycles: the **first cycle primes the baseline
  and never fires**, and a counter reset clamps the per-cycle delta to zero.

### `then.expand` — volume growth (storage watches)

A `storage` watch can grow the LVM-backed filesystem under the checked path
automatically when it runs low. The expansion is native (Sermo orchestrates it
in Go, invoking only `lvs`/`vgs`/`lvextend` and the filesystem grow tool —
`resize2fs`, `xfs_growfs` or `btrfs` — which have no Go API):

```yaml
watches:
  expand-backup:
    check: { type: storage, path: /mnt/backup, free_pct: { op: "<", value: "10%" } }
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

Because a watch fires **every cycle** the condition holds, an `expand` action
should always carry a watch-level **`policy`** block (same fields as service
remediation: `cooldown`, `backoff`, `max_actions`/`max_actions_window`) so the
volume is not extended on every tick while it stays low. The action runs at most
once per cooldown window; each attempt — success or failure — starts the
cooldown, so a failing expansion is not retried every cycle. Outcomes are
recorded as `expand` / `expand-skipped` / `expand-failed` events.

When the web UI is enabled, a storage watch with `then.expand` also shows an
**expand** action. That manual action uses the same configured `check.path` and
`expand.by` values from YAML; the browser does not send a path or size.

`then.notify` lists notifier names (each must be defined under `notifiers`). For
the multi-metric watches (`net`, `icmp`, `swap`) the `notify`/`hook` live in each
metric's own `then`, so a metric can have its own targets. The notification's
subject/body carry the watch's message and the same `SERMO_*` fields a hook
receives.

**Checks and watches share the same check types.** Any single-shot check — the
host-resource ones below (`storage`, `memory`, `pressure`, `load`, `fds`,
`pids`, `conntrack`, `firewall_rules`, `entropy`, `zombies`, `oom`, `cert`) *and* the service
checks (`tcp`, `ports`, `http`,
`command`, `file_exists`, `file`, `binary`, `pidfile`, `socket`, `libraries`,
`config`, `autofs`, `route`, `sqlite`/`sqlite3`, `websocket`/`ws`, `count`,
and connection-protocol checks
such as `mysql`/`smtp`) — can be used as a watch here, and
the host-resource ones can equally be used in a service's `checks:`/rules (see
[Checks](rules.md#checks)). A watch fires its hook on the check's **alert**
outcome: threshold crossed for condition checks, **failure** for health checks
(`tcp`/`http`/…), so e.g. an `http` watch alerts when the endpoint is down. The
multi-metric (`net`, `icmp`, `swap`) watch shape below (a `metrics:` map, one
hook per metric) and the multi-target (`file`, `process`) types are watch-only;
the single-metric form of `net`/`icmp`/`swap` (an explicit `metric:` field) also
works in a service's `checks:` (see [Checks](rules.md#checks)).
The WebUI shows live readings only for cheap local probes; command/network-heavy
checks rely on their normal watch events.

```yaml
watches:
  storage-root:
    enabled: true          # optional, default true
    interval: 1m           # optional, default engine.interval
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "90%" } # check fires when crossed
    for: { cycles: 3 }     # optional window; reuses the rules engine
    then:
      hook:
        command: [/usr/local/bin/alert-storage.sh, "/"]
        timeout: 10s       # optional, default engine.default_timeout
```

The `storage` check reads filesystem usage for `path` and is true when every present
predicate holds (`op ∈ >=,>,<=,<,==,!=`). Predicates cover **block space** —
`used_pct`, `free_pct`, `used_bytes`, `free_bytes` — and **inodes** —
`inodes_used_pct`, `inodes_free_pct`, `inodes_free` (absolute count).
`*_pct.value` accepts a number or an explicit `%` suffix in 0–100, e.g. `90` or `90%`.
`*_bytes.value` must include a size suffix (`K`/`M`/`G`/`T`, with optional
`B`/`iB`), e.g. `10G`; unitless byte values such as `10` are rejected.
Inode predicates catch the "disk full" that `df` hides: a filesystem out of
inodes (millions of tiny files) rejects new files while bytes are still free.
```yaml
watches:
  storage-root:
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "90%" }     # block space
      free_bytes: { op: "<", value: 10G }       # absolute free space
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
watches:
  data:
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
they are reported as result data and shown in the Web UI as live filesystem
information.

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
  address assigned; the `pppd` catalog daemon uses `expect: present`).

Hook extras: `SERMO_INTERFACE`, `SERMO_METRIC`, and — for the change metrics
(`state`/`speed`/`address`) — `SERMO_OLD`/`SERMO_NEW`.

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

ICMP requires elevated privileges: the daemon needs the `CAP_NET_RAW` capability
(or the host's `net.ipv4.ping_group_range` sysctl must include the daemon's gid)
to open a raw ICMP socket. This iteration is **IPv4-only**.

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
check:                                   # in a watches: entry like `load` above
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
check:                                   # in a watches: entry like `load` above
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
check:                                   # in a watches: entry like `load` above
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
(`50M` = 50 MiB/s). All present predicates must hold (AND), so `util_pct` +
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
check:                                   # in a watches: entry like `load` above
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
check:                                   # in a watches: entry like `load` above
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
check:                                   # in a watches: entry like `load` above
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
check:                                   # in a watches: entry like `load` above
  type: zombies
  count: { op: ">", value: 20 }          # fire when more than 20 zombies persist
```

The single `count: {op, value}` predicate is required; pair it with a `for` window
so a momentary burst of exiting children does not fire. Hook extras:
`SERMO_ZOMBIES` (the same value as `SERMO_VALUE`, the count).

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
    monitor: disabled
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
  the last cycle) or a threshold `{op, value}` (same operator set as storage). The
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

Hook extras: `SERMO_PATH` (the changed path), `SERMO_CHANGE`
(`size`|`size_threshold`|`permissions`|`owner`|`deleted`), `SERMO_OLD`/`SERMO_NEW`
(old/new value), and `SERMO_SIZE`/`SERMO_OP` for size conditions.

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
`io` are rates, so they need two samples: a new PID never fires on them in its
first cycle. Each matching PID is tracked independently — **one event and one
hook per PID** — so a worker pool produces one hook per offending worker.

`gone: true` is the inverse — it fires once when a previously-seen matching PID
**disappears** (and re-arms if it returns), so it never fires merely because the
process is present. Set it alone for a pure liveness alert ("nginx is gone"), or
alongside the presence conditions. With multiple matching PIDs it fires per exited
PID.

Hook extras: `SERMO_PID` (the matching pid), `SERMO_PROCESS` (the configured
name), `SERMO_CHANGE` (`threshold` for a presence fire, `gone` for a
disappearance), `SERMO_USER` (if set), `SERMO_AGE_SECONDS`, `SERMO_MEMORY` (RSS
bytes), and — once a rate is available — `SERMO_CPU` (percent) and `SERMO_IO`
(bytes/sec).

`for` is measured from when the daemon **first observed** the process, so a daemon
restart resets it (the real elapsed-since-start is not tracked across restarts).
`io` reads `/proc/<pid>/io`, which requires the daemon to have permission to read
it (typically running as root); when it is unreadable the IO condition never fires.
The optional `user:` filter is resolved through `engine.user_lookup`; numeric
UIDs are accepted and avoid host identity-service ambiguity. The WebUI shows
current matches, PIDs and aggregate RSS/IO counters.

Other resource types will be added as new check `type` values using the same
watch/hook structure.

## Global defaults

Only the per-service parts of `defaults` merge into a service: `stop_policy`,
`policy`, and `rule_window`. Engine-wide settings (`interval`,
`max_parallel_checks`, `max_parallel_operations`, `default_timeout`,
`operation_timeout`, `startup_delay`, `backend`, `user_lookup`,
`user_lookup_timeout`) are daemon configuration and never merge into a service.

`defaults.policy.cooldown` is **required and positive**: every resolved service
inherits a loop-prevention cooldown unless it overrides it.

`defaults.rule_window` is the **fallback firing window** for any rule that
declares neither its own `for` nor `within` (see the rules section). It accepts:

```yaml
defaults:
  rule_window:
    cycles: 1            # how many cycles the window spans
    mode: consecutive    # consecutive (a `for` window) | within (a sliding window)
    # min_matches: 1     # mode: within only — optional, defaults to 1 (true at least once)
```

`cycles: 1` + `mode: consecutive` is also the built-in default (fire the moment a
rule's condition is true), so the shipped `sermo.yml` carries this block only as
a commented reference.
Raise `cycles` (e.g. `3`) to require N consecutive true cycles before every
window-less rule fires, or use `mode: within` with `min_matches` for a sliding
window. A rule's own `for`/`within` always overrides the fallback, and like the
other per-service defaults it can be overridden per daemon or service.

## Resolution order

A service is resolved into a flat definition, lowest precedence first:

1. The effective global `defaults` (per-service parts).
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
- Named sections (`checks`, `preflight`, `postflight`, `processes`, `rules`)
  are maps keyed by name, so a child can override one field of one entry.
- Disable an inherited entry with `enabled: false`; delete it with
  `delete: true`.

Worked examples (cloning, disabling, multiple instances) live in
[daemons](daemons.md#cloning).
Catalog templates for installed versions/instances use `%v`, `%n` and `%i`; see
[versioned daemons](daemons.md#versioned-daemons).
When `%v` or `%n` templates also have an active-slot binary without a suffix,
such as `php` next to `php8.4` or `python` next to `python3`, Sermo materializes
that unversioned entry automatically. Set `versions.unversioned: false` on the
app template only when the marker-less binary should be ignored.

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
a regular file; `pidfile` requires a regular file; `socket` requires a Unix
socket. If none currently matches, Sermo keeps the first non-empty candidate so
the runtime preflight reports the bad path explicitly instead of expanding to an
empty string. Paths must be absolute after templating.

Use `variables.binary` plus an explicit
preflight entry for apps, daemons and services. Libraries use the same pattern
with `type: file`:

```yaml
kind: lib
name: glibc
variables:
  binary: /lib64/libc.so.6
preflight:
  file: { type: file, path: "${binary}" }
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
checks:
  http:
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
  # remediation.shadow (or mode: "shadow") puts the service's remediation rules
  # into observation-only mode: full condition evaluation, window tracking
  # (for/within), guard evaluation and policy checks (cooldown, max_actions,
  # backoff) still occur and produce "shadow" events with rich detail about
  # what Sermo would have done and why (including suppressions). No operations
  # are executed and the live RemediationState is not advanced. Perfect for
  # safely tuning rules before going live. This does not affect host watches;
  # put dry_run: true inside a watch's then block to rehearse hooks/notifies/
  # expand without executing them. A per-service setting overrides the default.
  #   remediation: { shadow: true }
  #   # remediation: { mode: shadow }  # alternative spelling
  remediation: { shadow: false }

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
checks:
  api:
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

`config validate` checks that the configuration is *well-formed*. `diagnose` goes
further and checks it against the **live host and state database**:

```sh
sermoctl diagnose          # text report
sermoctl diagnose --json   # machine-readable
sermoctl diagnose clean    # remove stale control state for unconfigured services/watches
sermoctl state compact     # prune old history and vacuum the state database
```

It reports, as `error` / `warning` / `info` findings:

- **Configuration** — every `config validate` issue (errors).
- **State database** — that the SQLite store passes `PRAGMA integrity_check`, and
  flags **stored control state for services/watches no longer in the config**:
  monitor/unmonitor state, automatic-remediation cooldown/backoff and rule
  window progress. Use `sermoctl diagnose clean` to prune those orphaned control
  rows.
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

`diagnose` exits `78` when any **error** finding is present; warnings alone exit
`0`. The same report is available in the web UI's **Diagnostics** panel and at
`GET /api/diagnostics`, where each finding is timestamped with the time the web
endpoint generated the diagnostics response. When the web UI is enabled, that
feed also includes **operation slot** usage from the running daemon (`info` when
some slots are in use, `warning` when saturated); see also `GET /api/ops`. When
the panel finds stale control-state rows, admins can use **clean stale data**, which
calls `POST /api/diagnostics/clean` and performs the same bounded cleanup as
`sermoctl diagnose clean`: it removes only persisted control state for targets
that are no longer configured. SLA, check
measurements, events and runtime CPU, memory and IO history are kept.

To reclaim old history intentionally, use:

```sh
sermoctl state compact                  # normal 366-day retention, then VACUUM
sermoctl state compact --before 720h    # prune history older than 30 days
sermoctl state compact --before 2026-01-01T00:00:00Z
```

`state compact` deletes old bucketed SLA, measurement, daemon metric, service
runtime metric and event rows, then checkpoints and vacuums the SQLite state
database so freed pages can return to the filesystem. Without `--before`, it
applies the same 366-day (~1 year) retention window that `sermod` applies at
startup.
