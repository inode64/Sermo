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

## Categories

Profiles are grouped by the subdirectory they live in under a profiles root:

```
profiles/
  services/   # daemon-managed long-running services (apache, nginx, mariadb, ...)
  apps/       # installed tools/runtimes (java, perl, sqlite, go, git, ...)
  libs/       # shared libraries used as restart triggers (glibc, pam)
```

The directory sets the profile's category (`service` / `app` / `library`); files
placed directly in a profiles root default to `service`. `sermoctl apps` lists
the `app` profiles and `sermoctl libs` the `library` profiles (see below).

## Library profiles

A library profile describes a shared library so services can restart when it is
upgraded. It only needs identity plus the file to watch:

```yaml
kind: profile
name: glibc
display_name: "GNU C Library"
description: "Standard C library (libc)"
variables:
  binary: "/lib64/libc.so.6"        # the file watched for changes (and its version)
```

A service (or service profile) opts in with `restart_on_change`:

```yaml
restart_on_change:
  libraries: [glibc, pam]
```

On resolution this desugars into one remediation rule per library that restarts
the service when that library's file changes:

```yaml
rules:
  restart-on-change-glibc:
    type: remediation
    if: { changed: { library: glibc, path: /lib64/libc.so.6 } }
    then: { action: restart }
```

The restart runs through the normal safe engine (guards, cooldown, max_actions),
and the change is acknowledged once the restart succeeds, so it fires once per
upgrade rather than every cycle. Referenced names must be `library` profiles.

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

`${arch}` is a third built-in: the machine architecture in `uname -m` form
(`x86_64`, `aarch64`, ...). Use it to locate an arch-specific binary or library:

```yaml
variables:
  binary: "/usr/bin/qemu-system-${arch}"   # → /usr/bin/qemu-system-x86_64
```

`${arch}` is substituted everywhere on load (including inside variable values and
version-template discovery paths), so it works in `binary`, library paths, and
`versions.from` alike.

`${os}` is the OS id from `/etc/os-release` (`gentoo`, `debian`, `ubuntu`, ...),
substituted the same way — e.g. `confdir: "/etc/${os}-apache"`.

`${arch}` and `${os}` can be overridden with the `SERMO_ARCH` / `SERMO_OS`
environment variables (useful for testing or building config off-host).

### OS-specific blocks (os:)

Beyond the `${os}` string, an `os:` key anywhere in a document selects a whole
sub-block by OS. The block for the detected OS (or a `default` block) is merged
into its parent and the rest discarded — at load, before resolution. It is not
limited to init/aliases; use it in checks, processes, policy, variables, anywhere:

```yaml
aliases:
  os:
    gentoo: { systemd: [apache.service],  openrc: [apache]  }
    debian: { systemd: [apache2.service], openrc: [apache2] }

checks:
  http:
    type: http
    timeout: 5s          # kept for every OS
    os:
      gentoo: { url: "http://localhost/gentoo-health" }
      debian: { url: "http://localhost/debian-health" }

policy:
  os:
    debian:  { cooldown: 1m }
    default: { cooldown: 9m }   # used when the OS has no branch
```

Siblings of `os:` are preserved and the selected branch merges over them. `os` is
reserved as a selector key wherever its value is a map.

## Versioned profiles

Some applications ship one binary per version and several can be installed at
once (php-fpm, postgres, tomcat/catalina, erlang/beam, berkeley db). Instead of one file per
version, write a single **version template**: a profile whose name (and filename)
contains `%v`, with `${version}` in the binary path.

```yaml
kind: profile
name: postgres-%v
display_name: "PostgreSQL ${version}"
service: { name: postgres }
variables:
  binary: "/usr/lib64/postgresql-${version}/bin/postgres"
preflight:
  binary: { type: binary, path: "${binary}" }
```

On load, Sermo discovers installed versions by globbing the `binary` path with
`${version}` wildcarded (here `/usr/lib64/postgresql-*/bin/postgres`) and
extracting what filled it. Each match becomes a concrete profile with `%v` and
`${version}` substituted everywhere (name, binary, display_name, aliases, ...) —
`postgres-14`, `postgres-16`, ... — and the template itself is dropped. If nothing
is installed the template yields nothing. The filename mirrors the name
(`postgres-%v.yml`); only that one file is needed. `%v` may sit anywhere in the
name (`db%vsql` → `db4.8sql`). Note: `%v` is substituted only in the name; inside
the body always use `${version}` (e.g. in `aliases`).

When the monitored `binary` is generic (no version in its path), point discovery
at a version-specific path with `versions.from`:

```yaml
kind: profile
name: php-fpm%v
service: { name: php-fpm }
versions:
  from: "/usr/lib64/php${version}/bin/php-fpm"   # globbed to find versions
aliases:
  systemd: [ "php${version}-fpm.service" ]
variables:
  binary: /usr/sbin/php-fpm                       # the actual binary, version-agnostic
```

`versions.from` is discovery-only metadata; it never appears in the materialized
profile. When omitted, discovery falls back to the `binary` path.

A discovered version must start with a digit, so siblings of an unbounded
trailing placeholder (a bare `php-fpm` symlink, a `php-fpm.conf`) are not mistaken
for versions. Even so, a placeholder bounded on both sides (e.g.
`/usr/lib64/php${version}/bin/php-fpm`, via `versions.from`) discovers most
precisely.

### Integer placeholder (%n)

`%v`/`${version}` accepts a free-form version (`8.3`, `12.0.2`). When the value is
a plain integer, use `%n`/`${n}` instead: it matches only whole numbers, so it
rejects siblings like `python3.11`. Otherwise it works exactly like `%v`.

```yaml
kind: profile
name: python%n
display_name: "Python ${n}"
service: { name: python${n} }
variables:
  binary: "/usr/bin/python${n}"
```

`/usr/bin/python*` then materializes `python2` and `python3`, but not
`python3.11` or `python-config`.

### Listing installed applications

`sermoctl apps` reports the applications described by profiles: which are
installed (their binary is present and executable), the version their version
command reports, and whether they resolve without error.

```text
APPLICATION   VERSION                      STATUS
Nginx         nginx version: nginx/1.24.0  ok
Python 3      Python 3.11.2                ok
Redis         -                            error: /usr/sbin/redis-server is not executable
```

Only installed applications are shown; `sermoctl apps all` also lists the rest as
`not installed`. With version templates this lists each installed version as its
own row (e.g. `PHP-FPM 8.3`, `PHP-FPM 7.4`). `--json` emits the structured
`name`, `display_name`, `binary`, `version`, `installed`, `ok` and `status`.

A template may `uses` a base profile to inherit its checks, processes and rules,
overriding only the version-specific binary. The packaged `php-fpm-%v` builds on
`php-fpm`:

```yaml
kind: profile
name: php-fpm-%v
uses: php-fpm
display_name: "PHP-FPM ${version}"
variables:
  binary: "/usr/lib64/php${version}/bin/php-fpm"
```

A service then targets a concrete version, e.g. `uses: php-fpm-8.3`.

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
