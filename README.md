# Sermo

Sermo is a portable service supervisor over **systemd** and **OpenRC**. It
validates services before acting, detects blocking operational state (named
runtime locks, backups, invalid config), discovers a service's real processes,
and applies **guarded** remediation rules — never restarting blindly.

It ships two binaries:

- **`sermoctl`** — the operator CLI (status, safe start/stop/restart, config
  validate/render, locks, processes, preflight, per-service availability/SLA,
  `diagnose` for config/host/database consistency).
- **`sermod`** — the daemon: one independent worker per service that runs
  checks, evaluates rules, and drives remediation through the same safe
  operation engine `sermoctl` uses. It also runs **host watches** (disk space,
  inodes and mount, load average, swap — usage and paging IO — kernel OOM kills,
  file descriptors, the netfilter conntrack table, kernel entropy, zombie
  processes, TLS certificates — expiry, validity and algorithm/issuer changes —
  network interfaces — state, speed and rx/tx
  errors — external hosts via ICMP — reachability and latency — files/directories
  — size, permissions, owner and deletion — and processes by name — age,
  CPU/memory/IO and disappearance) that fire a hook command and/or send
  **notifications** (email and Slack, pluggable for Teams/…) when a threshold is
  crossed or an attribute changes. It can also serve a small **web dashboard**
  (enable by setting `web.port`, recommended `9797`) to view services and
  monitor/start/stop/restart them, with a per-service detail showing its checks,
  SLA history and a latency graph (tcp/ports/http/service checks, avg/min/max
  over hour..year), and a global and per-service event feed. Optional Basic auth
  with an admin role and a
  read-only guest mode. It serves plain HTTP on loopback — put it behind a
  **TLS-terminating reverse proxy** (nginx/Apache) to expose it
  ([how](docs/configuration.md#behind-a-reverse-proxy-required-to-expose-it)).

## Build

```sh
make build      # produces bin/sermoctl and bin/sermod
make test       # run the test suite
```

Requires Go 1.26+. Runtime dependencies: `systemctl` or `rc-service` on the host.

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
staging, and installs the binaries, every profile (keeping the
`services/apps/libs` layout), a sample `sermo.yml`, the tmpfiles.d config, and
both the systemd unit and the OpenRC init script (with their binary/config paths
rewritten to match):

```sh
sudo make install PREFIX=/usr                 # /usr/bin, /usr/sbin, /etc/sermo, ...
make install DESTDIR=/tmp/stage PREFIX=/usr    # stage for packaging
```

Key variables (override on the command line): `DESTDIR`, `PREFIX`/`prefix`,
`bindir`, `sbindir`, `datadir`, `sysconfdir`, `TMPFILESDIR`,
`SYSTEMD_UNITDIR`, `OPENRC_INITDIR`. Granular targets are available too:
`install-bin`, `install-profiles`, `install-config`, `install-tmpfiles`,
`install-systemd`, `install-openrc` (and `uninstall`). An existing `sermo.yml`
is never overwritten. `make install` does not create `/var/lib/sermo`; the
installed tmpfiles.d config owns that directory creation.

## Quick start

```sh
# Inspect a unit (no config needed)
sermoctl backend
sermoctl status nginx
sermoctl is-active nginx

# List installed services, applications and libraries, their version and health
sermoctl --config /etc/sermo/sermo.yml services      # service software (nginx, mariadb, ...)
sermoctl --config /etc/sermo/sermo.yml apps          # tools/runtimes (only installed)
sermoctl --config /etc/sermo/sermo.yml apps all      # include not-installed
sermoctl --config /etc/sermo/sermo.yml libs          # shared libraries (restart triggers)

# Validate and render the resolved configuration
sermoctl --config /etc/sermo/sermo.yml config validate
sermoctl --config /etc/sermo/sermo.yml config render apache-main
sermoctl --config /etc/sermo/sermo.yml config diff redis-main redis-cache

# Operate a configured service through the safe engine
sermoctl --config /etc/sermo/sermo.yml restart apache-main

# Pause / resume monitoring of a service (e.g. for maintenance)
sermoctl --config /etc/sermo/sermo.yml unmonitor apache-main   # daemon stops checking it
sermoctl --config /etc/sermo/sermo.yml monitor apache-main     # resume
sermoctl --config /etc/sermo/sermo.yml reload                  # ask daemon to re-read its config (SIGHUP)

# Availability (SLA) per service over rolling windows (hour..year)
sermoctl --config /etc/sermo/sermo.yml sla                     # all services
sermoctl --config /etc/sermo/sermo.yml sla apache-main         # one service
sermoctl --config /etc/sermo/sermo.yml sla --series apache-main --since 168h  # per-minute series (graph data)

# Run the daemon
sermod run --config /etc/sermo/sermo.yml
```

## Layout

```
/etc/sermo/sermo.yml              global config
/usr/share/sermo/profiles/*.yml   packaged profiles (apache, mysql, redis, ...)
/etc/sermo/apps-available/*.yml   user profiles
/etc/sermo/apps-enabled/*.yml     included service documents
/run/sermo/locks/*.lock           named runtime locks (tmpfs, wiped on reboot)
/run/sermo/ops/*.lock             internal operation locks
/var/lib/sermo/sermo.db           persistent state DB (monitoring state, SLA samples; survives reboot)
```

Example profiles and configs are under [`profiles/`](profiles/) and
[`configs/`](configs/). Packaging units are under [`packaging/`](packaging/).

## Exit codes (`sermoctl`)

| code | meaning                                            |
|------|----------------------------------------------------|
| 0    | success / active / allowed                         |
| 1    | service inactive, check failed, or rule false      |
| 2    | runtime error / backend not detected               |
| 64   | usage error                                        |
| 75   | temporarily blocked by a lock or guard             |
| 78   | configuration invalid                              |

## Documentation

- [Configuration](docs/configuration.md) — global config, profiles, services,
  merge and variables.
- [Rules](docs/rules.md) — checks, conditions, windows, guards, remediation
  policy.
- [Profiles](docs/profiles.md) — writing and overriding profiles.
- [Safety](docs/safety.md) — the safety invariants that cannot be disabled.

## Safety in one paragraph

Sermo never restarts or starts a service if a required preflight fails or a
guard blocks it, never SIGKILLs by default, and never kills a process by name —
a kill requires an exact match on the resolved `/proc/<pid>/exe` path **and** the
real UID against an explicit `kill_only_if` selector. See [safety](docs/safety.md).
