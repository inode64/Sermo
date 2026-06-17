# Sermo

Sermo is a portable service supervisor over **systemd** and **OpenRC**. It
validates services before acting, detects blocking operational state (named
runtime locks, backups, invalid config), discovers a service's real processes,
and applies **guarded** remediation rules — never restarting blindly.

It ships two binaries:

- **`sermoctl`** — the operator CLI (status, safe start/stop/restart/reload/resume, config
  validate, locks, processes, preflight, per-service availability/SLA,
  `diagnose` for config/host/database consistency).
- **`sermod`** — the daemon: one independent worker per service runs checks,
  evaluates rules and drives remediation through the same safe operation engine
  `sermoctl` uses. It also runs **host watches** (storage, load, memory, swap,
  network, ICMP, DNS, routes, files, processes, certificates and more — see
  [host watches](docs/configuration.md#host-watches)) that fire hook commands
  and/or **notifications** (email, Slack, Teams), and can serve a **web
  dashboard** (set `web.port`, recommended `9797`) with per-service checks, SLA
  history, latency graphs and an event feed — loopback HTTP with optional auth;
  expose it only behind a TLS reverse proxy
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
staging, and installs the binaries, the full catalog (keeping the
`services/apps/libs/patterns` layout), a sample `sermo.yml`, the default
notification template, the tmpfiles.d config, and both the systemd unit and the
OpenRC init script (with their binary/config paths rewritten to match):

```sh
sudo make install PREFIX=/usr                 # /usr/bin, /usr/sbin or merged-/usr /usr/bin, /etc/sermo, ...
make install DESTDIR=/tmp/stage PREFIX=/usr    # stage for packaging
```

Key variables (override on the command line): `DESTDIR`, `PREFIX`/`prefix`,
`bindir`, `sbindir`, `datadir`, `sysconfdir`, `TMPFILESDIR`,
`SYSTEMD_UNITDIR`, `OPENRC_INITDIR`. Granular targets are available too:
`install-bin`, `install-catalog`, `install-config`, `install-templates`,
`install-tmpfiles`, `install-systemd`, `install-openrc` (and `uninstall`). An
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

# List installed services, applications and libraries, their version and health
sermoctl services      # service software (nginx, mariadb, ...)
sermoctl apps          # tools/runtimes (only installed)
sermoctl apps all      # include not-installed
sermoctl libs          # shared libraries (restart triggers)

# Validate configuration
sermoctl config validate

# Operate a configured service through the safe engine
sermoctl restart apache-main

# Pause / resume monitoring of a service (e.g. for maintenance)
sermoctl unmonitor apache-main   # daemon stops checking it
sermoctl monitor apache-main     # resume
sermoctl daemon reload           # ask sermod to re-read its config (SIGHUP)

# Availability (SLA) per service over rolling windows (hour..year)
sermoctl sla                     # all services
sermoctl sla apache-main         # one service
sermoctl sla --series apache-main --since 168h  # per-minute series (graph data)

# Run the daemon
sermod run --config /etc/sermo/sermo.yml
```

Packaged definitions live under [`catalog/`](catalog/), sample configs under
[`configs/`](configs/), packaging units under [`packaging/`](packaging/). The
on-host file layout is in
[configuration → layout](docs/configuration.md#layout).

## Documentation

- [Configuration](docs/configuration.md) — global config, daemons, services,
  merge and variables; [`docs/sermo-all.yml`](docs/sermo-all.yml) is the
  complete annotated example.
- [Rules](docs/rules.md) — checks, conditions, windows, guards, remediation
  policy.
- [Daemons](docs/daemons.md) — writing and overriding daemons.
- [CLI](docs/cli.md) — commands, flags and exit codes.
- [Safety](docs/safety.md) — the invariants that cannot be disabled: no
  unguarded actions, no SIGKILL by default, never kill by name (exact
  resolved-exe + UID match only).
