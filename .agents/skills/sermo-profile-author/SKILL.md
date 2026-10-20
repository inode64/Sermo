---
name: sermo-profile-author
description: Use when creating or reviewing Sermo service definitions for applications such as Apache, Nginx, Redis, MySQL, MariaDB, PostgreSQL, PHP-FPM, Postfix, Dovecot, HAProxy or similar services.
---

Catalog service author for Sermo. Schema and merge rules live in
`sermo-config-schema`; safety policy in `docs/safety.md`. This skill is the
shape of a catalog service and the traps that produce an unsafe or unusable
one.

## Service goal

A service should make monitoring and control safer and simpler. A service
check describes the service process. State of a host resource the service
observes belongs in a host watch; use `reports: state` or `reports: value`
only when observed data must remain visible without affecting service health
or availability. `smartd` is the reference: its catalog checks are `service`,
`process` and memory, while drive health lives in host `smart` watches
(`examples/watches/smart.yml`).

Each service should define:

```text
service name and display_name
per-init service candidates
linked apps: (binary, version and library preflight are the app's)
variables
pidfile when the daemon writes one
config/preflight checks
process discovery
health watches (flag one verify: true for start verification)
locks
guards (top-level rules:, type: guard)
stop_policy (keep_children when the daemon's workload must outlive a restart)
policy and restart_on_change
remediation and alerts in watches.<name>.then
```

A daemon whose workload must survive a restart (sessions, jobs, shims, bricks)
sets `stop_policy.keep_children: true` with a strict principal selector, never a
`delegated` selector: `delegated` is observe-only, for processes another backend
owns.

## Required sections

Use this general shape:

```yaml
name: redis
display_name: Redis
category: database
type: cache
service: { systemd: [redis, redis-server], openrc: [redis, redis-server] }
apps: [redis]
variables: { user: redis }
pidfile: /run/redis/redis.pid
preflight: {}
processes: { redis: { exe: "${redis_binary}", user: "${user}" } }
watches: { service: { check: { type: service, expect: active, verify: true } } }
rules: {}          # guards
stop_policy: {}
policy: {}
restart_on_change: {}
```

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

## Common services

When creating catalog services, consider:

```text
apache/httpd: apache2ctl configtest per OS, ${apache_binary} -t by default; version from the apache app (httpd|apache2 -v)
nginx: nginx -t; version nginx -V from the nginx app
php-fpm: php-fpm --test --fpm-config <config>; version php-fpm -v from the php-fpm app
mysql/mariadb: binary-owned config preflight when reliable; native mysql check for liveness
redis: linked redis app binary/version preflight; native redis check for PING/INFO liveness
postgresql: linked postgres app (versioned binaries); native postgres check for liveness
```

If a config validation command differs by distribution, make it override-friendly through variables.

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
fail closed). A top-level `rules:` block holds guards only; remediation lives in
`watches.<name>.then`. Do not point a `file_exists` check at
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

