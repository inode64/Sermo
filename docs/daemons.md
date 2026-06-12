# Daemons

A daemon is a reusable base definition for an application. A service `uses` a
daemon and overrides only what differs.

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

The packaged catalog (`catalog/`) covers apache, mysql, mariadb, redis and
php-fpm. They define variables, preflight, processes, checks, stop_policy and
rules so a service usually only sets a few overrides.

## Categories

Daemons are grouped by the subdirectory they live in under a catalog root:

```
catalog/
  services/   # daemon-managed long-running services (apache, nginx, mariadb, ...)
  apps/       # installed tools/runtimes (java, perl, sqlite, go, git, ...)
  libs/       # shared libraries used as restart triggers (glibc, pam)
  patterns/   # output-analysis rule sets referenced by a check's analyze: block
```

The directory sets the daemon's category (`service` / `app` / `library` /
`patterns`); files placed directly in a daemons root default to `service`.
`sermoctl services`, `sermoctl apps` and `sermoctl libs` list each category,
showing which are installed, the version their version command reports, and
whether they resolve without error (add `all` to include the not-installed).
`sermoctl patterns` lists the pattern sets and their rule counts (see the
`analyze:` block in [rules.md](rules.md)).

## Library daemons

A library daemon describes a shared library so services can restart when it is
upgraded. It only needs identity plus the file to watch:

```yaml
kind: daemon
name: glibc
display_name: "GNU C Library"
description: "Standard C library (libc)"
variables:
  binary: "/lib64/libc.so.6"        # the file watched for changes (and its version)
```

A service (or daemon definition) opts in with `restart_on_change`:

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
upgrade rather than every cycle. Referenced names must be `library` daemons.

## Reload on config change (`reload_on_change`)

Many daemons re-read their configuration **without a restart** — systemd
(`systemctl daemon-reload`), nginx (`nginx -s reload`), named (`rndc reload`),
rsyslog, … `reload_on_change` watches config files/directories and, when one
changes, runs the **reload** action instead of a disruptive restart:

```yaml
# catalog/services/systemd.yml
preflight:
  config: { type: command, command: ["systemd-analyze", "verify"] }   # checked first
commands:
  reload: { command: ["systemctl", "daemon-reload"] }   # see commands.reload below
reload_on_change:
  paths: [/etc/systemd/system, /lib/systemd/system]
```

On resolution this desugars into one remediation rule per path:

```yaml
rules:
  reload-on-change-1:
    type: remediation
    if: { changed: { path: /etc/systemd/system } }
    then: { action: reload }
```

The **`reload`** action runs through the same safe engine as restart but in
place: it runs **preflight first** (so an invalid config — caught by the
service's `config` check — blocks the reload), reloads, then verifies health.
`reload` is also a valid rule action on its own (`then: { action: reload }`) and
is blocked by the same guards as restart/start.

**What "reload" runs.** By default it is the backend per-unit reload —
`systemctl reload <unit>` (which runs the unit's `ExecReload`, e.g. `nginx -s
reload`) or OpenRC's init-script `reload`. A daemon can override this with its
own **`commands.reload`** when the reload is not a per-unit operation — systemd
itself reloads with `systemctl daemon-reload`, not `systemctl reload systemd`:

```yaml
commands:
  reload: { command: ["systemctl", "daemon-reload"] }
```

## App dependencies (`apps`)

A service often runs on top of one or more **apps** — the runtimes/tools in
`catalog/apps` (java, openssl, perl, …). An app owns the **binary** and
**version** checks for that tool; it is the single source of truth, shared by
every service that uses it. A service (or daemon definition) links the apps it
needs with `apps:` — a list, since a service may depend on several:

```yaml
# catalog/services/tomcat-%v.yml — Tomcat runs on the JVM
apps: [java]
```

On resolution each linked app's preflight checks are injected into the service's
preflight under keys namespaced by the app name (`<app>-<check>`), carrying the
app's own binary path and version command. When a service links several apps,
each one's checks stay distinct — e.g. `backrest`'s `apps: [backrest, restic]`
yields `backrest-binary`, `backrest-version`, `restic-binary`, `restic-version`,
so a missing `restic` raises its own alert separate from `backrest`:

```yaml
preflight:
  java-binary:  { type: binary, path: /usr/bin/java }
  java-version: { type: command, command: ["/usr/bin/java", "-version"] }
```

Because they run in **preflight**, a missing or wrong-version runtime fails the
service's preflight, which **blocks start/restart** (a preflight-failed operation
never executes the action) — you do not start a service whose runtime is absent.
The link is many-to-many: a service lists several apps, and one app is shared by
every service that lists it. The service keeps its own `binary`, `version` and
`config` checks (the **config** test is always service-specific, never moved to
an app). Referenced names must be `app` daemons.

## Metadata fields

A daemon or service may carry two optional human-facing strings:

```yaml
kind: daemon
name: mariadb
display_name: "MariaDB"      # pretty label; falls back to name when absent
description: "..."           # free-text note; shown verbatim, nothing when absent
```

Both are optional and behave differently when missing:

- **`display_name`** is the label used wherever Sermo shows the application to a
  human (e.g. `daemon list`, `service list`). When it is absent or blank, Sermo
  falls back to `name`. Set it only when it adds something over `name` — a proper
  brand (`MariaDB`, `PostgreSQL`, `OpenSSH`) or a version (`PHP-FPM 8.3`). If the
  display name would just repeat `name`, leave it out and let the fallback apply.
- **`description`** is an optional free-text note. It has **no fallback**: when it
  is absent, nothing is shown for it — Sermo never substitutes `name`. Use it for
  a real sentence, not a restatement of the name.

Both must be strings if present; validation rejects non-string values.

### Built-in variables

The variables in the table below are always available during resolution
**without being declared** under `variables` — so a daemon can parameterize
human-facing strings (and paths) instead of hardcoding them:

```yaml
rules:
  block-restart-during-backup:
    type: guard
    blocks: [restart, stop]
    then:
      action: block
      message: "${display_name} backup is running"   # → "MariaDB backup is running"
variables:
  binary: "/usr/bin/qemu-system-${arch}"             # → /usr/bin/qemu-system-x86_64
```

An explicit `variables` entry of the same name always takes precedence over a
built-in. `${arch}`/`${os}` are baked **on load** (everywhere — variable values
and `versions.from` discovery paths included); the rest resolve per service, and
the runtime ones (`${date}`/`${event}`/`${action}`) only in rule `message:`
strings. The `SERMO_ARCH` / `SERMO_OS` / `SERMO_HOST` / `SERMO_HOSTNAME` /
`SERMO_INIT` / `SERMO_USER` environment variables override the matching built-in
(handy for testing or building config off-host).

| Variable          | Value                                   | Resolved        |
|-------------------|-----------------------------------------|-----------------|
| `${name}`         | the resolved service/daemon name       | resolution      |
| `${display_name}` | the display name (falls back to name)   | resolution      |
| `${service}`      | the service's primary unit name         | resolution      |
| `${host}`         | hostname (`SERMO_HOST` override)        | resolution¹     |
| `${hostname}`     | short hostname (`SERMO_HOSTNAME`)       | resolution⁵     |
| `${init}`         | detected init system (`SERMO_INIT`)     | resolution      |
| `${user}`         | Sermo's user (`SERMO_USER` override)    | resolution⁴     |
| `${pidfile}`      | conventional `/run/<unit>.pid`          | resolution⁴     |
| `${port}`         | the top-level `port:` field (when set)  | resolution³     |
| `${arch}`         | machine architecture (`SERMO_ARCH`)     | load (baked)    |
| `${os}`           | os-release id (`SERMO_OS`)              | load (baked)    |
| `${date}`         | event timestamp (RFC3339)               | runtime²        |
| `${event}`        | the firing rule's name                  | runtime²        |
| `${action}`       | the action taken (restart/start/stop)   | runtime²        |

¹ `${host}` only applies when the daemon does not define a `host` variable (a
bind address like `127.0.0.1`); an explicit `host` always wins.

⁵ `${hostname}` is the **short** hostname — the first label before the first dot
(`radon` on `radon.srvdr.com`) — distinct from `${host}` (which keeps the full
detected hostname / bind-address fallback). Use it for systemd instance units
keyed by host identity, e.g. `service: "ceph-mon@${hostname}"` → `ceph-mon@radon`.
For numeric multi-instance daemons (e.g. one OSD per device) use a `%n` version
template instead, discovering ids from a path — `versions: { from:
"/var/lib/ceph/osd/ceph-${n}" }` materializes `ceph-osd0…N` with `service:
"ceph-osd@${n}"`. An explicit `hostname` variable (or `SERMO_HOSTNAME`) wins.

⁴ `${user}` and `${pidfile}` are fallbacks: a daemon's own `user` (a service
account such as `www-data`) or `pidfile` variable always wins. They pair with the
pidfile selector — e.g. `processes.main: { type: pidfile, path: "${pidfile}" }` —
and the `command_match` user — `user: "${user}"`.

² `${date}`/`${event}`/`${action}` are substituted when the worker emits a rule
message, so they belong in `message:` strings — e.g.
`message: "[${host}] ${service}: ${event} → ${action} at ${date}"`. Elsewhere they
stay literal.

³ `${port}` mirrors a top-level `port:` field on the service (or daemon), so an
instance can set its listen port once and have every `${port}` reference resolve
to it:

```yaml
kind: service
name: db-inst2
uses: dbserver
port: 3307          # → ${port} everywhere in the daemon
```

Unlike the other built-ins it has **no fallback**: declare `port:` (or a
`variables.port`, which wins) wherever `${port}` is used, or resolution reports
`${port}` as undefined. This is the first-class equivalent of putting `port`
under `variables:` (as the multi-instance example below still shows).

### OS-specific blocks (os:)

Beyond the `${os}` string, an `os:` key anywhere in a document selects a whole
sub-block by OS. The block for the detected OS (or a `default` block) is merged
into its parent and the rest discarded — at load, before resolution. It is not
limited to the service block; use it in checks, processes, policy, variables, anywhere:

```yaml
service:
  os:
    gentoo: { systemd: [apache],  openrc: [apache]  }
    debian: { systemd: [apache2], openrc: [apache2] }

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

A branch may also be a **list or scalar** instead of a map. When `os:` is the only
key in its parent, the selected branch *replaces* the value (rather than merging),
which is handy for OS-specific candidate lists such as pidfile paths:

```yaml
processes:
  main:
    type: pidfile
    path:                       # the resolved value becomes the OS's list
      os:
        fedora: [/run/postgres.pid]
        gentoo: [/run/postgres${port}.pid, /run/postgres.pid]
        default: [/run/postgres.pid]
```

A **pidfile** selector's `path` accepts a single path or a **list of candidates**;
discovery tries them in order and uses the first that points at a running process
(so per-OS or versioned pidfile locations all resolve without personal config).

## Versioned daemons

Some applications ship one binary per version and several can be installed at
once (php-fpm, postgres, tomcat, erlang/beam, berkeley db). Instead of one file per
version, write a single **version template**: a daemon whose name (and filename)
contains `%v`, with `${version}` in the binary path.

```yaml
kind: daemon
name: postgres-%v
display_name: "PostgreSQL ${version}"
service: postgres
variables:
  binary: "/usr/lib64/postgresql-${version}/bin/postgres"
preflight:
  binary: { type: binary, path: "${binary}" }
```

On load, Sermo discovers installed versions by globbing the `binary` path with
`${version}` wildcarded (here `/usr/lib64/postgresql-*/bin/postgres`) and
extracting what filled it. Each match becomes a concrete daemon with `%v` and
`${version}` substituted everywhere (name, binary, display_name, service, ...) —
`postgres-14`, `postgres-16`, ... — and the template itself is dropped. If nothing
is installed the template yields nothing. The filename mirrors the name
(`postgres-%v.yml`); only that one file is needed. `%v` may sit anywhere in the
name (`db%vsql` → `db4.8sql`). Note: `%v` is substituted only in the name; inside
the body always use `${version}` (e.g. in `service`).

When the monitored `binary` is generic (no version in its path), point discovery
at a version-specific path with `versions.from`:

```yaml
kind: daemon
name: php-fpm%v
service:
  systemd: [ "php${version}-fpm" ]
versions:
  from: "/usr/lib64/php${version}/bin/php-fpm"   # globbed to find versions
variables:
  binary: /usr/bin/php-fpm                        # the actual binary, version-agnostic
```

`versions.from` is discovery-only metadata; it never appears in the materialized
daemon. When omitted, discovery falls back to the `binary` path.

A discovered version must start with a digit, so siblings of an unbounded
trailing placeholder (a bare `php-fpm` symlink, a `php-fpm.conf`) are not mistaken
for versions. Even so, a placeholder bounded on both sides (e.g.
`/usr/lib64/php${version}/bin/php-fpm`, via `versions.from`) discovers most
precisely.

### Integer placeholder (%n)

`%v`/`${version}` accepts a free-form version (`8.3`, `12.0.2`); use `%n`/`${n}`
when the value is a **plain integer** — it matches only whole numbers, otherwise
working exactly like `%v`:

```yaml
kind: daemon
name: python%n
display_name: "Python ${n}"
service: python${n}
variables: { binary: "/usr/bin/python${n}" }
```

`/usr/bin/python*` then materializes `python2`/`python3`, but not `python3.11` or
`python-config`.

### Listing installed applications

`sermoctl apps` reports the applications described by daemons: which are
installed (their binary is present and executable), the version their version
command reports, and whether they resolve without error. The VERSION column
shows the short version by default; add `--long` to show the full raw string.

```text
APPLICATION   VERSION  STATUS
Nginx         1.24.0   ok
Python 3      3.11.2   ok
Redis         -        error: /usr/bin/redis-server is not executable
```

```text
$ sermoctl apps --long
APPLICATION   VERSION                      STATUS
Nginx         nginx version: nginx/1.24.0  ok
Python 3      Python 3.11.2                ok
```

Only installed applications are shown; `sermoctl apps all` also lists the rest as
`not installed`. The same `--long` and `all` apply to `sermoctl libs` and
`sermoctl services`. With version templates this lists each installed version as
its own row (e.g. `PHP-FPM 8.3`, `PHP-FPM 7.4`). `--json` is unaffected by
`--long` — it always emits both, with the structured `name`, `display_name`,
`binary`, `version`, `version_short`, `installed`, `ok` and `status`.

`version` is the raw first line the version command prints (e.g. `nginx version:
nginx/1.30.2`); `version_short` reduces it to just the numeric version and at
most the patchlevel (`1.30.2`), taking the first `major.minor[.patch]` token and
dropping any further build components and suffixes (so `2.8.4.1-0+g…` becomes
`2.8.4` and `4.2.8p18` becomes `4.2.8`). It is empty when the version line
carries no recognizable number.

A daemon may instead declare a dedicated `version_short` command (under
`preflight` or `commands`, alongside `version`) that prints the bare version
itself, sidestepping the regex when a tool can report it directly. Its first
non-empty output line is then used verbatim. The packaged interpreter apps do
this — e.g. PHP runs `php -r 'echo PHP_VERSION;'`, Python
`python3 -c 'import platform;print(platform.python_version())'`, Node
`node -p process.versions.node` — so their short version never depends on
parsing. When no such command is configured (or it errors or prints nothing),
`version_short` falls back to parsing the `version` line as above.

```yaml
preflight:
  version:       { type: command, command: ["${binary}","-v"], timeout: 10s }
  version_short: { type: command, command: ["${binary}","-r","echo PHP_VERSION;"], timeout: 10s }
```

A template may `uses` a base daemon to inherit its checks, processes and rules,
overriding only the version-specific binary. The packaged `php-fpm-%v` builds on
`php-fpm`:

```yaml
kind: daemon
name: php-fpm-%v
uses: php-fpm
display_name: "PHP-FPM ${version}"
variables:
  binary: "/usr/lib64/php${version}/bin/php-fpm"
```

A service then targets a concrete version, e.g. `uses: php-fpm-8.3`.

## Service unit

The service's identity is the daemon `name`; `service` declares the init-unit
name(s) to operate on. The simplest form is a single name that works on both
init systems:

```yaml
service: apache2
```

When the unit name differs across init systems, list per-init candidates; Sermo
resolves the first one the active backend actually knows (systemd via
`systemctl cat`, OpenRC via the init script):

```yaml
service:
  systemd: [apache2, httpd]
  openrc:  [apache2, apache]
```

Candidates are bare names — systemd appends `.service` automatically. They are
tried in order and deduplicated, and the resolved name is used for all later
operations. A **scalar** `service` is trusted even when the probe cannot surface
it (e.g. sysv-generated units); a **per-init list** requires a match, and an
init system with no entry means the service is *not available* there.

An enabled instance can override the unit with a scalar (e.g.
`service: redis-cache`) to run as its own unit, or omit `service` entirely to
inherit the daemon's candidates.

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

## Multiple instances of one application

To run several instances of the same application — same binary, same checks and
rules, differing only in listen port, pidfile and config file — let each instance
`uses` the daemon and override just the variables that make it unique. No
special "instance" mechanism is involved: it is the ordinary `uses` + `variables`
inheritance.

The daemon parametrizes everything that varies with `${...}` placeholders and
threads each one into the commands and checks that consume it. In particular the
config-file path should be a variable wired into every command that reads it, so
two instances never pick up each other's configuration:

```yaml
kind: daemon
name: dbserver
variables:
  port:    3306
  pidfile: /run/dbserver/main.pid
  config:  /etc/dbserver/main.cnf
processes:
  pidfile: { type: pidfile, path: "${pidfile}" }
checks:
  tcp:    { type: tcp, port: "${port}" }
  config: { type: command, command: ["dbserverd", "--defaults-file=${config}", "--help"] }
```

Each instance overrides the three variables and gives itself an init unit (a
systemd template instance or a distinct unit name) with a scalar `service`:

```yaml
kind: service
name: db-inst1
uses: dbserver
service: db-inst1
variables:
  port:    3306
  pidfile: /run/dbserver/inst1.pid
  config:  /etc/dbserver/inst1.cnf
```

A second instance is the same file with its own name/unit and variables (e.g.
`name: db-inst2`, `service: db-inst2`, `port: 3307`, the `inst2.*` paths).

Prefer `uses` over [`clone`](#cloning) here: every instance derives from the
*daemon* and only overrides variables. Reach for `clone` only when one instance
should copy *another concrete service* almost verbatim. A runnable version of
this example lives under `configs/examples/multi-instance/`.

## Disabling and deleting inherited entries

```yaml
checks:
  http:
    enabled: false   # keep but disable
  ping:
    delete: true     # remove the inherited entry
```

## Monitoring flag

The top-level `monitor` flag sets a service's monitoring behavior when the
daemon starts:

```yaml
kind: service
name: web
uses: nginx
monitor: enabled    # enabled (default) | disabled | previous
```

- **`enabled`** (the default when the flag is absent): always monitor on startup.
- **`disabled`**: never monitor — the worker exists but every cycle is skipped.
- **`previous`**: restore the runtime state the service had before the daemon
  last stopped. On the very first run (no recorded state) it defaults to
  monitored.

This is distinct from the top-level `enabled: false`, which disables the service
entirely (no worker is built for it at all). With `monitor`, the worker is always
present; only whether it runs its checks/rules each cycle changes.

The live state is toggled at runtime with `sermoctl monitor <svc>` /
`sermoctl unmonitor <svc>` and persisted in the state database under
`paths.state` (see [configuration](configuration.md)). Because that database
survives reboots, a `previous` service comes back up in whatever state an
operator last left it.

Host watches use the same `monitor: enabled | disabled | previous` values under
the global `watches:` section; see [configuration](configuration.md#host-watches).

## Auxiliary commands

`commands` is informational metadata (e.g. a version command). Sermo never runs
it as part of monitoring or remediation; the `sermoctl apps`/`libs`/`services`
listings run the `version` command to report a daemon's version and confirm it
runs. That run can assert its outcome, the same way a watch hook or `command`
check does: `expect_exit` (default 0) and optional `expect_stdout`/`expect_stderr`
matchers — a substring or an `{op, value}` comparison (`== != > >= < <= =~`).

```yaml
commands:
  version:
    command: ["apachectl", "-v"]
    timeout: 5s
    expect_exit: 0                                   # optional, default 0
    expect_stdout: { op: "=~", value: "Apache/2" }   # optional: match the output
```
