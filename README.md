# Sermo

**Sermo is a portable, safety-first service supervisor for Linux hosts.** It sits
above **systemd** and **OpenRC**, validates a service before it ever acts on it,
understands the service's *real* processes, and applies **guarded** remediation
rules — it never restarts blindly and never kills the wrong process.

Where an init system answers "is the unit active?", Sermo answers the harder
operational questions: *is the service actually healthy, is it safe to touch
right now, and if it isn't healthy, what is the safe thing to do?* It monitors,
diagnoses, remediates under strict invariants, keeps availability (SLA) history,
watches host-level resources, sends notifications, and serves a live web
dashboard — all from a single daemon.

## Why Sermo

A plain "restart on failure" supervisor is dangerous on a real host: it restarts
during a backup, kills a process that happens to share a binary name, or acts on
a service whose config is broken and makes the outage worse. Sermo is built
around the opposite principle — **prove it is safe, then act**:

- **Guarded, never blind.** Every start/stop/restart/reload/resume/pause, plus
  the manual stale-pidfile repair path, runs through one operation engine that
  checks preflight, guards and runtime locks first. A blocked action does not
  run.
- **Knows the real processes.** It discovers a service's actual PIDs from
  `/proc`, so health, residual detection and (rare, opt-in) kills are based on
  the resolved executable and UID — never on a process name.
- **Validates before it touches anything.** Broken config, a failing required
  preflight, or an active named lock blocks remediation instead of amplifying an
  outage.
- **Busy is not down.** A server that answers "too many connections" or
  "loading" is reported with its own reason, never restarted for it.
- **Portable.** The same configuration and behaviour run over systemd *and*
  OpenRC; the init backend is auto-detected.
- **Honest availability.** SLA counts only cycles it actually observed — time
  before any evidence could exist is a gap, not counted as downtime.

## Features

**Monitoring & health**
- A **fleet of independent workers** — one per service — each running its own
  checks, evaluating rules and driving remediation on its own schedule; a panic
  in one worker never takes down the daemon.
- A broad **check catalog** ([full list](docs/rules.md#checks)): service
  state/version/config, TCP ports, HTTP(S) and WebSocket endpoints, TLS
  certificate expiry, native protocol probes (PostgreSQL, MySQL/MariaDB, Redis,
  MongoDB, SMTP/IMAP, DNS, LDAP, SNMP, AMQP, …), SQL queries, replication and
  long-running statements, SQLite integrity, storage health (SMART, RAID, LVM,
  `hdparm` throughput), hardware sensors, egress interface, default route,
  firewall rules, clock drift, file/dir size growth, and process/count metrics.
- **Replaced code detection** — services still running a binary or shared
  library replaced on disk by an update, and `restart_on_change` to restart a
  service when one of its apps or configuration files changes.
- **Check interdependencies** (`requires` / `skip_when_changed`) so a probe is
  only run when its prerequisites hold.
- **Host watches** — monitor resources that aren't services (mounts, RAID
  arrays, network uplinks, certificates, …), firing hook commands and/or
  notifications, and repairing storage when asked (`then.expand` grows a
  volume, `then.remount` recovers a hung mount).
- **Security watches** — `process_policy` alerts when a user runs a program its
  policy does not allow, and `unowned_processes` lists processes no init unit or
  installed package accounts for. Being listed authorizes nothing: only the
  watch's own `kill_only_if` or an administrator's verified choice signals one.

**Safe remediation**
- A single **operation engine** shared by the CLI and the daemon: operation
  lock → named runtime locks → required preflight → guards →
  stop → verify processes and reconcile init → start → verify +
  postflight.
- **Named runtime locks** to fence maintenance windows (backups, migrations):
  `sermoctl lock … -- COMMAND` holds a TTL'd lock for the duration of a command.
- **Guards, windows and remediation policy** to express *when* and *how often* an
  action may run, with escalation.
- **Hard safety invariants** that YAML cannot disable (see below).

**Availability & history**
- **SLA over rolling windows** (hour → year) for every service and availability
  watch, stored as one archive per graph window so a year of history stays
  bounded without losing an incident.
- An **event/activity log** with retention and compaction of the state store.

**Operate**
- A focused operator **CLI** (`sermoctl`) for status, safe lifecycle actions,
  config validation, locks, processes, preflight, inventory, SLA and events.
- **Notifications** to email, Slack, Teams, Telegram, ntfy, Gotify and the
  logged-in terminals (`tty`, `wall`) with a templated default message, graded
  on five severity levels
  (debug → critical): each notifier sets the lowest level it receives, a
  threshold can escalate from warning to error to critical, and an incident is
  announced once per level instead of once per watch.
- An optional **interactive Telegram report bot** (read-only): ask it `/status`,
  `/services`, `/sla` and it replies with live reports — long polling only, no
  inbound port, answering allow-listed chats.
- A **daemon-wide panic switch** to pause all automatic remediation instantly.
- **Guided wizards** for common setups (service, docker, vm, mount, volume, net,
  uplink).

**Web dashboard** (optional)
- A live, self-contained dashboard: per-service checks, SLA history, latency
  graphs, an event feed, and the full inventory (services / apps / libraries)
  and host watches.
- A **Sessions** panel: interactive SSH, tmux and screen sessions, and the
  statements running on MySQL, MariaDB and PostgreSQL; an administrator can
  close a verified session or cancel a re-verified statement.
- **Push-driven** via server-sent events with a polling fallback, and built to
  **WCAG 2.2 AA** accessibility.
- Loopback HTTP by default, with admin and read-only guest logins from hashed
  credential files — expose it only behind a TLS reverse proxy.

## How it works

A single daemon (`sermod`) loads the configuration and the packaged catalog,
resolves them into a service tree, and builds a **fleet**: one *Worker* per
service and one *Watch* per host resource or app. A scheduler runs them in a
loop. The CLI and the web UI talk to the daemon over HTTP and signals. Every
action on a service — manual or automatic — goes through `operation.Engine`,
which coordinates locks, preflight, guards and the init backend, so the CLI and
the daemon can never diverge in how they act.

```
clients ── sermoctl (CLI) ─┐                    ┌── operation.Engine ── systemd / OpenRC
           browser (Web UI) ┤                    ├── named locks (oplock + scanner)
                            │   sermod (daemon)  │
      signals ── SIGHUP ────┼── Monitor ─ Scheduler ─ Fleet ┤── state store (SLA · events · metrics)
                SIGTERM ────┘        │   (Worker per service │── notifiers (email/slack/teams/telegram/…)
                                     │    Watch per resource)│
      config + packaged catalog ────┘                       └── web.Server (dashboard + /api)
```

See [docs/architecture.md](docs/architecture.md) for the faithful,
code-anchored diagrams (operation pipeline, lock states, monitoring cycle).

## The two binaries

- **`sermoctl`** — the operator CLI: status, safe start/stop/restart/reload/
  resume/repair (and pause for libvirt VMs and Docker containers), config
  validate, locks, processes, preflight, availability/SLA, inventory and events.
  Read-only commands do not need root.
- **`sermod`** — the daemon: one independent worker per service runs checks,
  evaluates rules and drives remediation through the same safe operation engine
  `sermoctl` uses. It also runs **host watches** that fire hook commands and/or
  **notifications** (email, Slack, Teams, Telegram, ntfy, Gotify, terminals),
  and can serve the **web
  dashboard** (set `web.port`, recommended `9797`) — loopback HTTP with optional
  auth; expose it only behind a TLS reverse proxy
  ([how](docs/configuration.md#behind-a-reverse-proxy-required-to-expose-it)).

## Safety invariants

These cannot be turned off in YAML — validation rejects any `security:` toggle
that tries. In full in [docs/safety.md](docs/safety.md):

1. **No action on a failed required preflight** — blocked with `preflight_failed`.
2. **No action a guard blocks** — guards are evaluated before remediation, and
   what an action performs decides which guards apply (a guard on `start` also
   blocks `restart` and `repair`).
3. **Active named runtime locks always block service actions** — checked
   automatically, no rule needed.
4. **Never signal an unverified residual** — a residual is signalled only when
   an explicit `kill_only_if` selector or a strict `processes:` identity names
   it, or the kernel places it in the service's own control group. SIGKILL also
   needs `force_kill`: the engine default is `false`; the shipped `sermo.yml`
   sets `auto`.
5. **Never kill by process name** — a kill requires an exact match on the
   resolved `/proc/<pid>/exe` path **and** the real UID; a command line only
   narrows a match, and anything Sermo cannot positively identify is *reported,
   not killed*.
6. **Never send terminating signals to PID 1 or kernel threads** — blocked
   centrally, not configurable.
7. **`force_kill: true` requires `kill_only_if`** with non-empty `users` and
   `exe_any` selectors.
8. **Restart always verifies stop and start** under the same locks, preflight,
   guards and deadline; surviving processes block the new start.
9. **A stray process is a diagnostic, never a target** — a control-group member
   no selector claims is reported, and nothing signals it for being a stray.
10. **`process_policy` only alerts, and `unowned_processes` authorizes
    nothing** — being listed never makes a process a signal target.
11. **A libvirt network with live guest interfaces is never destroyed.**
12. **A database statement is cancelled only after it is re-verified** — a
    changed or finished statement is never killed.
13. **A busy server is never restarted for being busy** — "too many
    connections", "loading" or "starting up" means it is alive.

## Build

```sh
make build      # produces bin/sermoctl and bin/sermod
make test       # run the test suite
```

Requires Go 1.27.3+. Runtime dependencies: `systemctl` or `rc-service` on the host.

**`sermod` runs as root.** It manages services owned by different users and
accesses privileged areas (service control, signalling other users' processes,
cross-user `/proc` inspection including per-process IO, raw ICMP sockets), so the
packaged units run it as root; it warns at startup if it is not. The config is
therefore trusted, root-owned input — `command` checks and `hook`s run as root
(never via a shell), so keep `/etc/sermo` root-only and put secrets in the
environment (`${env:NAME}`). See [safety](docs/safety.md#privileges-the-daemon-runs-as-root).
Read-only `sermoctl` commands (status, config, etc.) do not need root.

## Install

`make install` honors the standard GNU directory variables and `DESTDIR`
staging, and installs the binaries, the full catalog (keeping the
`services/apps/libs/patterns` layout), the examples, a sample `sermo.yml`, the
default notification template, the tmpfiles.d config, and both the systemd unit and the
OpenRC init script (with their binary/config paths rewritten to match). The
OpenRC script logs to `/var/log/sermod.log`, and `install-openrc` adds the
logrotate rule that rotates it in place (`copytruncate`):

```sh
sudo make install PREFIX=/usr                 # /usr/bin, /usr/sbin or merged-/usr /usr/bin, /etc/sermo, ...
make install DESTDIR=/tmp/stage PREFIX=/usr    # stage for packaging
```

Key variables (override on the command line): `DESTDIR`, `PREFIX`/`prefix`,
`bindir`, `sbindir`, `datadir`, `sysconfdir`, `TMPFILESDIR`,
`SYSTEMD_UNITDIR`, `OPENRC_INITDIR`, `LOGROTATEDIR`. Granular targets are available too:
`install-bin`, `install-catalog`, `install-examples`, `install-config`,
`install-templates`, `install-tmpfiles`, `install-systemd`, `install-openrc`
(and `uninstall`). An
existing `sermo.yml` is never overwritten. `make install` does not create
`/var/lib/sermo`; the installed tmpfiles.d config owns that directory creation.

On merged-/usr hosts where `/usr/sbin` is a symlink to `/usr/bin`, the default
`sbindir` collapses to `$(bindir)` so `DESTDIR` packages do not materialize a
real `usr/sbin` directory and replace the host symlink when extracted. Pass an
explicit `sbindir=...` only when the target really has a distinct sbin directory.

Do not deploy a `DESTDIR` tree by extracting a tar archive directly into `/`
with preserved directory metadata. A staged tree contains directory entries such
as `./`, `etc/` and `usr/`; plain `tar -xpf` can apply those modes to existing
system directories. Use the package manager, copy the installed files directly,
or extract ad-hoc test archives with:

```sh
sudo tar --no-overwrite-dir -C / -xpf sermo-stage.tar
```

## Quick start

```sh
# Inspect a unit (no config needed)
sermoctl backend
sermoctl status nginx
sermoctl is-active nginx

# List configured services (init units, Docker containers, VMs)
sermoctl services                     # SERVICE / TYPE / STATE / MONITORED
sermoctl services --notify ops-email  # email a configured services health report
sermoctl patterns                     # pattern sets in use

# List catalog inventory
sermoctl services catalog      # packaged catalog service profiles (nginx, mariadb, ...)
sermoctl services catalog all  # include profiles not installed on this host
sermoctl apps          # tools/runtimes (only installed)
sermoctl apps all      # include not-installed
sermoctl libs          # shared libraries (restart triggers)

# Validate configuration
sermoctl config validate

# Operate a configured service through the safe engine
sermoctl restart apache-main
sermoctl pause vm-web            # suspend a libvirt VM or pause a Docker container

# Pause / resume monitoring of a service (e.g. for maintenance)
sermoctl unmonitor apache-main   # daemon stops checking it
sermoctl monitor apache-main     # resume
sermoctl daemon reload           # ask sermod to re-read its config

# Fence a maintenance window with a named runtime lock
sermoctl lock apache-main --reason backup --ttl 1h -- /usr/local/bin/backup.sh

# Availability (SLA) over rolling windows (hour..year)
sermoctl sla                     # every service and availability watch
sermoctl sla apache-main         # one service
sermoctl sla --series apache-main --since 168h  # stored series; buckets follow the window

# Run the daemon
sermod run --config /etc/sermo/sermo.yml
```

Packaged definitions live under [`catalog/`](catalog/), sample configs under
[`examples/`](examples/), packaging units under [`packaging/`](packaging/). The
on-host file layout is in
[configuration → layout](docs/configuration.md#layout).
Daemon flags (`--verbose`) are in
[CLI → sermod daemon flags](docs/cli.md#sermod-daemon-flags).

## Documentation

- [Configuration](docs/configuration.md) — global config, catalog services, services,
  merge and variables; [`docs/sermo-all.yml`](docs/sermo-all.yml) is the
  complete annotated example.
- [Rules](docs/rules.md) — checks, conditions, windows, guards, remediation
  policy.
- [Services](docs/services.md) — writing and overriding services.
- [CLI](docs/cli.md) — commands, flags and exit codes.
- [Safety](docs/safety.md) — the invariants that cannot be disabled: no
  unguarded actions, no SIGKILL by default, never kill by name (exact
  resolved-exe + UID match only).
- [Web dashboard](docs/configuration.md#web-ui) — enabling the dashboard, its
  access model and what it shows.
- [Architecture](docs/architecture.md) — end-to-end diagrams of the daemon,
  the operation pipeline, lock states and the monitoring cycle.
