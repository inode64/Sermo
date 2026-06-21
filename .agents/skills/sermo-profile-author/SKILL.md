---
name: sermo-profile-author
description: Use when creating or reviewing Sermo catalog daemon definitions for applications such as Apache, Nginx, Redis, MySQL, MariaDB, PostgreSQL, PHP-FPM, Postfix, Dovecot, HAProxy or similar services.
---

You are the official catalog daemon author for Sermo.

## Daemon goal

A catalog daemon should make service monitoring and control safer, not just easier.

Each daemon should define:

```text
service name
per-init service candidates
variables
version command
config/preflight checks
postflight health checks
library checks if applicable
process discovery
health checks
locks
guards
stop_policy
default remediation rules
```

## Required sections

Use this general shape:

```yaml
kind: daemon
name: redis
category: cache

variables: {}

service:
  systemd: []
  openrc: []

commands:
  version: {}

preflight: {}

postflight: {}

processes: {}

checks: {}

rules: {}

stop_policy: {}
```

## Safety defaults by service class

For databases:

```text
force_kill: false
long graceful timeout
backup locks enabled
config validation required before start/restart/reload/resume
```

For stateless web services:

```text
force_kill may be true only with kill_only_if
shorter graceful timeout acceptable
configtest required before restart
```

For caches:

```text
conservative by default
avoid SIGKILL unless explicitly allowed
```

## Common daemons

When creating catalog daemons, consider:

```text
apache/httpd: apachectl configtest, apachectl -v
nginx: nginx -t, nginx -v
php-fpm: php-fpm -t, php-fpm -v
mysql/mariadb: mysqld --validate-config when supported, mysqladmin ping
redis: redis-server --version, redis-cli ping
postgresql: pg_isready, postgres version, pg_ctl where applicable
```

If a config validation command differs by distribution, make it override-friendly through variables.

## Locks

Prefer Sermo named runtime locks when the protected job can be wrapped:

```bash
sermoctl lock mysql --name backup --reason "backup mysql" --ttl 4h -- mariadb-backup ...
```

That needs no guard; the operation engine blocks the service automatically while
the lock is active. Use `sermoctl locks SERVICE` to inspect active locks and
`sermoctl lock acquire` / `release` for persistent holds. Add a guard only for a
foreign signal Sermo does not own, such as a backup process or a flag file
created by another tool. Do not point a `file_exists` check at
`<paths.runtime>/locks/` (default `/run/sermo/locks/`).

Example:

```yaml
checks:
  backup-flag:
    type: file_exists
    path: /run/mysql-backup/in-progress

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

## Output format

When creating a catalog daemon, return:

```text
- daemon YAML
- assumptions
- distro-specific notes
- safety rationale
- required tests
```
