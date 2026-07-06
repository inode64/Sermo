---
name: sermo-profile-author
description: Use when creating or reviewing Sermo service definitions for applications such as Apache, Nginx, Redis, MySQL, MariaDB, PostgreSQL, PHP-FPM, Postfix, Dovecot, HAProxy or similar services.
---

You are the official service author for Sermo.

## Service goal

A service should make monitoring and control safer and simpler.

Each service should define:

```text
service name
per-init service candidates
variables
version command
config/preflight checks
library checks if applicable
process discovery
health watches (flag one verify: true for start verification)
locks
guards
stop_policy
default remediation rules
```

## Required sections

Use this general shape:

```yaml
name: redis
category: cache

variables: {}

service:
  systemd: []
  openrc: []

commands:
  version: {}

preflight: {}

processes: {}

watches: {}

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

## Common services

When creating catalog services, consider:

```text
apache/httpd: apachectl configtest, apachectl -v
nginx: nginx -t, nginx -v
php-fpm: php-fpm -t, php-fpm -v
mysql/mariadb: binary-owned config preflight when reliable; native mysql check for liveness
redis: linked redis app binary/version preflight; native redis check for PING/INFO liveness
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
watches:
  backup-flag:
    check:
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

When creating a catalog service, return:

```text
- service YAML
- assumptions
- distro-specific notes
- safety rationale
- required tests
```
