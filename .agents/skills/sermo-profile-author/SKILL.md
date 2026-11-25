---
name: sermo-profile-author
description: Use when authoring or reviewing a catalog service profile, including daemon identity, health checks, init backends, backup guards and restart policy.
---

Catalog service author for Sermo. Schema and merge rules live in
`sermo-config-schema`, the operator contract in `docs/services.md`, safety
policy in `docs/safety.md`. Start from the closest existing profile under
`catalog/services/` (`catalog/services/redis.yml` for a cache or database);
this skill is the decisions that make a profile safe and usable.

## What a profile describes

A service check describes the service process. State of a host resource the
service observes belongs in a host watch; use `reports: state` or
`reports: value` only when observed data must stay visible without affecting
service health or availability. `smartd` is the reference: its catalog checks
are `service`, `process` and memory, while drive health lives in host `smart`
watches (`examples/watches/smart.yml`).

Flag one health watch `verify: true` for start verification. A probe that
connects to a socket systemd activates (the libvirt daemons' `libvirt` checks)
sets `skip_when_idle` to the daemon's unit; without it the probe starts the
daemon on every cycle, boot included. Guards are
top-level `rules:` of `type: guard`; remediation and alerts attach through
`watches.<name>.then`.

Link an app only when starting the daemon needs it: the daemon itself, its
runtime (java, perl, python3) or a tool its init path runs (`ovsdb-tool`). A
client the profile runs only to probe the daemon is a variable
(`ctl_binary: ${bindir}/syslog-ng-ctl`): linked, its preflight blocks every restart
while the client is broken, its failure grades as an outage of the active
service, and `ServiceBinary` may pick the client, since it takes the first
linked app's `<app>-binary` in name order. An app's `binary` is its daemon, not
the client of the same package.

A daemon written in Python, Node or Perl runs the interpreter: `exe` names the
interpreter, never the launcher script (a selector on the script matches no
process and its `kill_only_if` authorizes nothing), and `cmd` narrows it to
this daemon. `catalog/services/pm2.yml` and `catalog/services/patroni.yml` are
the references; check `/proc/<pid>/exe` and the command line of the real
process before writing either field.

A daemon whose workload must survive a restart (sessions, jobs, shims, bricks)
sets `stop_policy.keep_children: true` with a strict principal selector, never a
`delegated` selector: `delegated` is observe-only, for processes another backend
owns.

## Safety defaults by service class

For databases and caches (mysql, mariadb, postgres, mongod and influxdb stop with
120s/60s; redis, keydb and memcached with 90s/60s; etcd and patroni, whose
cluster peers take over, with 60s/30s; galera's garbd holds no data and keeps
the defaults):

```text
long graceful_timeout (90-120s) and term_timeout (60s)
explicit kill_only_if pinned to the service user and binary
force_kill inherited (defaults.stop_policy.force_kill: auto): a daemon that
  survives the grace periods gets SIGKILL, and the engine's own recovery
  (WAL, InnoDB, AOF) restores consistency
backup locks or guards blocking stop/restart while a dump runs
config validation required before start/restart/reload/resume
```

Set `force_kill: false` only where SIGKILL must never happen; a residual in the
unit's own cgroup still receives SIGTERM, and a survivor then leaves the restart
in `orphan_processes` with the service stopped until an operator acts.

For stateless web services:

```text
force_kill true only with kill_only_if, or inherited auto with strict selectors
shorter graceful timeout acceptable
configtest required before restart
```

## Alert severity

Grade every alert so a notifier's `min_severity` can route it. A resource
alert (memory, `cpu_thread`, `io`, queue depth, backlog, replication lag) is
`severity: warning` at its threshold with a `levels: { error: … }` tier at a
higher one. Where a host typically tunes the threshold (queues, connections,
backlogs, replication lag), name both as variables side by side
(`queue_limit` / `queue_error_limit`) so an instance raises them together.
Fixed ceilings shared by every profile (memory and `cpu_thread` percentages,
the 100 MB/s `io` ceiling with its 500 MB/s error tier) stay literals; an
override that raises such a literal raises its `levels.error` too, because a
tier that is not stricter than its base is ignored with a warning. Only an
alert with no meaningful second tier (a count of idle replication slots) or a
check type without `levels:` (`db_queries`) warns alone. Health checks rely on the
self-grades (`service`/`process` down is critical, `cert` expiry warning or
critical); do not declare `severity:` on them unless the profile really means
a different grade. Quote `${var}` values inside YAML flow mappings.

## Config preflight

If a validation command differs by distribution, make it override-friendly
through variables.

`preflight.config` should refuse exactly what the start refuses, on every init
backend. Read the systemd unit's ExecStartPre lines and the OpenRC `checkconfig`
before choosing the command: a looser test lets Sermo restart into a unit that
will not start, and a stricter one refuses every restart, automatic recovery
included, while the service would start fine. When the backends disagree, keep
the stricter test and say why in the profile. Gentoo's `named.service` runs
`named-checkconf -z` but OpenRC only parses named.conf, so named keeps `-z`.
Its periodic copy, the `configuration` check, is graded error so that refusal
is reported.

## Library updates

A daemon that terminates TLS or parses external data (web, PHP, mail) lists
the catalog libraries it may load under `restart_on_change.libraries`, so a
security update restarts it. List them generously: a library the host does
not have is skipped, with no rule and no blocking preflight. A library whose
soname changes with each version (ICU, ImageMagick) stays out of the list.
Add the advisory `alert-if-libraries-replaced` watch (`stale_libraries`) for
everything else the processes map. A database only gets the advisory watch:
its restart is the operator's decision. A daemon whose sessions survive a
restart (`keep_children`, OpenSSH) lists only what its main process maps and
gets no advisory watch: `stale_libraries` scans the kept sessions too, so the
alert would hold until they close.

PHP-FPM uses `reload.on_upgrade: true` because USR2 executes the installed binary and drains the old workers. A configuration-only reload cannot opt in.

## Init and reload

Verify every declared systemd/OpenRC backend and every fallback. An OpenRC
signal fallback needs a canonical pidfile plus a non-delegated `processes:`
selector with exact `exe` and `user`; otherwise use an argv `reload.command` or the backend's
native reload. Before the finish gate:

```sh
go test ./internal/config -run 'TestRealCatalog(AllServicesValidate|ReloadServicesResolve)$' -count=1
```

The operator procedure is in `docs/services.md`.

## Locks

Prefer Sermo named runtime locks when the protected job can be wrapped:

```bash
sermoctl lock mysql --name backup --reason "backup mysql" --ttl 4h -- mariadb-backup ...
```

That needs no guard; the operation engine blocks the service automatically while
the lock is active. Use `sermoctl locks SERVICE` to inspect active locks and
`sermoctl lock acquire` / `release` for persistent holds. Add a guard only for a
foreign signal Sermo does not own, such as a backup process or a flag file
created by another tool. The check a guard reads is a sensor: `reports: state`
and `optional: true`, so neither a running backup nor a sensor that cannot
connect reads the service as down (an unavailable sensor still blocks: guards
fail closed). A top-level `rules:` block accepts guards, remediation and alert
rules; service watches can attach remediation through `watches.<name>.then`.
Do not point a `file_exists` check at
`<engine.runtime>/locks/` (default `/run/sermo/locks/`).

Example:

```yaml
watches:
  backup-flag:
    check:
      type: file_exists
      path: /run/mysql-backup/in-progress
      reports: state      # an absent flag is the healthy, normal state
      optional: true

rules:
  block-restart-during-backup:
    type: guard
    blocks: ["restart", "stop"]
    if:
      active:
        check: backup-flag
    then:
      action: block
      message: "${display_name} backup is running"
```

