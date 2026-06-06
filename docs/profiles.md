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
