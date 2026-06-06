# Sermo

Sermo is a portable service supervisor over **systemd** and **OpenRC**. It
validates services before acting, detects blocking operational state (named
runtime locks, backups, invalid config), discovers a service's real processes,
and applies **guarded** remediation rules — never restarting blindly.

It ships two binaries:

- **`sermoctl`** — the operator CLI (status, safe start/stop/restart, config
  validate/render, locks, processes, preflight).
- **`sermod`** — the daemon: one independent worker per service that runs
  checks, evaluates rules, and drives remediation through the same safe
  operation engine `sermoctl` uses.

## Build

```sh
make build      # produces bin/sermoctl and bin/sermod
make test       # run the test suite
```

Requires Go 1.26+. Runtime dependencies: `systemctl` or `rc-service` on the
host; no root needed for read-only commands.

## Quick start

```sh
# Inspect a unit (no config needed)
sermoctl backend
sermoctl status nginx
sermoctl is-active nginx

# List installed applications and libraries, their version and health
sermoctl --config /etc/sermo/sermo.yml apps         # only installed
sermoctl --config /etc/sermo/sermo.yml apps all      # include not-installed
sermoctl --config /etc/sermo/sermo.yml libs          # shared libraries (restart triggers)

# Validate and render the resolved configuration
sermoctl --config /etc/sermo/sermo.yml config validate
sermoctl --config /etc/sermo/sermo.yml config render apache-main

# Operate a configured service through the safe engine
sermoctl --config /etc/sermo/sermo.yml restart apache-main

# Pause / resume monitoring of a service (e.g. for maintenance)
sermoctl --config /etc/sermo/sermo.yml unmonitor apache-main   # daemon stops checking it
sermoctl --config /etc/sermo/sermo.yml monitor apache-main     # resume

# Run the daemon
sermod run --config /etc/sermo/sermo.yml
```

## Layout

```
/etc/sermo/sermo.yml              global config
/usr/share/sermo/profiles/*.yml   packaged profiles (apache, mysql, redis, ...)
/etc/sermo/apps-available/*.yml   user profiles
/etc/sermo/apps-enabled/*.yml     enabled services
/run/sermo/locks/*.lock           named runtime locks
/run/sermo/ops/*.lock             internal operation locks
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
