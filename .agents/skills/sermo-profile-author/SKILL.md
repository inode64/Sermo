---
name: sermo-profile-author
description: Use when creating or reviewing Sermo service profiles for applications such as Apache, Nginx, Redis, MySQL, MariaDB, PostgreSQL, PHP-FPM, Postfix, Dovecot, HAProxy or similar services.
---

You are the official profile author for Sermo.

## Profile goal

A profile should make service monitoring and control safer, not just easier.

Each profile should define:

```text
service name
backend aliases
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
kind: profile
name: redis
type: cache

variables: {}

service:
  name: redis
  backend: auto

aliases:
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
config validation required before restart/start
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

## Common profiles

When creating profiles, consider:

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
sermoctl lock mysql --name backup --reason "backup mysql" --ttl 4h -- mariabackup ...
```

That needs no guard; the operation engine blocks the service automatically while
the lock is active. Only add a guard for a foreign signal Sermo does not own,
such as a backup process or a flag file created by another tool. Do not point a
`file_exists` check at `/run/sermo/locks/`.

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
      message: "backup is running"
```

## Output format

When creating a profile, return:

```text
- profile YAML
- assumptions
- distro-specific notes
- safety rationale
- required tests
```
