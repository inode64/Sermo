# Profiles

A profile is a reusable base definition for an application. A service `uses` a
profile and overrides only what differs.

```yaml
kind: service
name: apache-main
uses: apache
variables:
  health_path: /health
checks:
  http:
    url: "http://${host}:${port}${health_path}"
```

The packaged profiles (`profiles/`) cover apache, mysql, mariadb, redis and
php-fpm. They define variables, preflight, processes, checks, stop_policy and
rules so a service usually only sets a few overrides.

## Metadata fields

A profile or service may carry two optional human-facing strings:

```yaml
kind: profile
name: mariadb
display_name: "MariaDB"      # pretty label; falls back to name when absent
description: "..."           # free-text note; shown verbatim, nothing when absent
```

Both are optional and behave differently when missing:

- **`display_name`** is the label used wherever Sermo shows the application to a
  human (e.g. `profile list`, `service list`). When it is absent or blank, Sermo
  falls back to `name`. Set it only when it adds something over `name` — a proper
  brand (`MariaDB`, `PostgreSQL`, `OpenSSH`) or a version (`PHP-FPM 8.3`). If the
  display name would just repeat `name`, leave it out and let the fallback apply.
- **`description`** is an optional free-text note. It has **no fallback**: when it
  is absent, nothing is shown for it — Sermo never substitutes `name`. Use it for
  a real sentence, not a restatement of the name.

Both must be strings if present; validation rejects non-string values.

### Using name and display_name in expansion

`${name}` and `${display_name}` are always available as variables during
resolution, without being declared under `variables`. `${name}` expands to the
resolved service name and `${display_name}` to its display name (falling back to
`name`). This lets a profile parameterize human-facing strings instead of
hardcoding a brand:

```yaml
rules:
  block-restart-during-backup:
    type: guard
    blocks: [restart, stop]
    then:
      action: block
      message: "${display_name} backup is running"
```

When a service `uses` the profile, `${display_name}` resolves to whatever that
service's display name is (the profile's, unless the service overrides it). An
explicit `variables.name` or `variables.display_name` takes precedence over the
built-in.

## Unit aliases

The unit name differs across distributions. A profile lists per-backend
candidates; Sermo resolves the first one the active backend actually knows
(systemd via `systemctl cat`, OpenRC via the init script):

```yaml
service:
  name: apache2
aliases:
  systemd: [apache2.service, httpd.service]
  openrc:  [apache2, apache]
```

The candidate list is `service.name` first, then the aliases for the active
backend, deduplicated. All later operations use the resolved name.

## Cloning

A service may `clone` another service to make a second instance:

```yaml
kind: service
name: redis-cache
clone: redis-main
variables:
  port: 6380
  pidfile: /run/redis-cache/redis.pid
```

Clone copies the source **before** variable expansion, so overriding the `port`
variable alone is enough — every check that references `${port}` resolves to the
new value. Clone chains resolve transitively; cycles are rejected.

## Disabling and deleting inherited entries

```yaml
checks:
  http:
    enabled: false   # keep but disable
  ping:
    delete: true     # remove the inherited entry
```

## Auxiliary commands

`commands` is informational metadata (e.g. a version command). Sermo loads and
validates it but never runs it as part of monitoring or remediation.

```yaml
commands:
  version:
    command: ["apachectl", "-v"]
    timeout: 5s
```
