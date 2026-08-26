# Services

A catalog service is a reusable base definition for an application. A configured
service `uses` a catalog service and overrides only what differs. A service file
lives under `paths.services`, which is what marks it as a configured service — no
`kind:` is needed (see
[configuration](configuration.md): a document's kind is derived from its
location).

```yaml
name: apache-main
uses: apache
variables:
  health_path: /health
watches:
  restart-if-http-failed:
    check:
      url: "http://${host}:${port}${health_path}"
```

The packaged catalog covers common service families such as web servers,
databases, container runtimes, NFS/libvirt helpers, observability daemons such
as Rsyslog, and hardware/system services.
In the source tree this is `catalog/`; in packaged builds Sermo reads the catalog
directory compiled into the binary. Catalog profiles define variables,
preflight, processes, watches, stop_policy, remediation policy and rules so a
configured service usually only sets a few overrides. High-impact catalog
services such as databases, caches and queues may carry stricter local `policy`
settings than the global defaults, with longer cooldowns, rate limits and
backoff to avoid restart loops.

The packaged `snmpd` profile keeps its unauthenticated local SNMP protocol
probe visible but marks it optional. An agent restricted to authenticated SNMP
managers is therefore still healthy when its init-service check is active; add
an authenticated site-specific SNMP check when protocol availability itself is
required.

## Contents

- [Categories](#categories)
- [Library services](#library-services)
- [Reload on config change (reload_on_change)](#reload-on-config-change-reload_on_change)
  - [Native reload (reload:) — when the init can't, Sermo can](#native-reload-reload--when-the-init-cant-sermo-can)
- [App dependencies (apps)](#app-dependencies-apps)
- [Metadata fields](#metadata-fields)
  - [Built-in variables](#built-in-variables)
  - [OS-specific blocks (os:)](#os-specific-blocks-os)
  - [control: libvirt — QEMU/libvirt virtual machines](#control-libvirt--qemulibvirt-virtual-machines)
  - [control: docker — Docker containers](#control-docker--docker-containers)
  - [restart_policy — restart strategy](#restart_policy--restart-strategy)
  - [also_service — auxiliary init units](#also_service--auxiliary-init-units)
  - [also_apply — cascade to other services](#also_apply--cascade-to-other-services)
  - [processes: by executable or cmdline](#processes-by-executable-or-cmdline)
  - [Stopped-state invariants (stop_policy)](#stopped-state-invariants-stop_policy)
  - [Unclaimed control-group members (reap)](#unclaimed-control-group-members-reap)
  - [pidfile: and pidfiles: shorthand (selectors + health checks)](#pidfile-and-pidfiles-shorthand-selectors--health-checks)
  - [socket: shorthand (gated health check)](#socket-shorthand-gated-health-check)
  - [lockfile: shorthand (gated health check)](#lockfile-shorthand-gated-health-check)
- [Versioned services](#versioned-services)
  - [Integer and instance placeholders](#integer-and-instance-placeholders)
  - [Composite names with a separator (%s)](#composite-names-with-a-separator-s)
  - [Service-owned discovery](#service-owned-discovery)
  - [Optional components (enable_if)](#optional-components-enable_if)
  - [Variables read from a config file (from_file)](#variables-read-from-a-config-file-from_file)
  - [Listing installed applications](#listing-installed-applications)
- [Service unit](#service-unit)
- [Cloning](#cloning)
- [Multiple instances of one application](#multiple-instances-of-one-application)
- [Disabling and deleting inherited entries](#disabling-and-deleting-inherited-entries)
- [Monitoring flag](#monitoring-flag)
- [Blocking operations while clients are connected](#blocking-operations-while-clients-are-connected)
- [PostgreSQL replication watches](#postgresql-replication-watches)
- [Exim hints database maintenance](#exim-hints-database-maintenance)
- [Auxiliary commands](#auxiliary-commands)

## Categories

Catalog documents are grouped by the subdirectory they live in under the
packaged catalog root:

```
catalog/
  services/   # long-running services (apache, nginx, mariadb, ...)
  apps/       # installed tools/runtimes (java, perl, sqlite, go, git, ...)
  libs/       # shared libraries used as restart triggers (glibc, pam)
  patterns/   # output-analysis rule sets referenced by a check's analyze: block
```

The directory sets the catalog category (`service` / `app` / `library` /
`patterns`) and therefore the document's kind (`service` / `app` / `lib` /
`patterns`), so a top-level `kind:` is redundant and omitted; files placed
directly in the packaged catalog root are rejected. Use one YAML file per catalog
document: one service, app, lib or pattern in each file.
`sermoctl services`, `sermoctl apps` and `sermoctl libs` list each category,
showing which are installed, the version their version command reports, and
whether they resolve without error (add `all` to include the not-installed).
Configured service instances (under `paths.services`) are listed
by the web UI and `GET /api/services`, not by `sermoctl services` — see
[cli.md](cli.md#catalog-inventory).
`sermoctl patterns` lists the pattern sets and their rule counts (see the
`analyze:` block in [rules.md](rules.md)).

Catalog documents may declare `aliases: [...]` for distro or package names that
operators naturally type. For example, the canonical catalog service
`name: apache` can carry aliases such as `apache2` and `httpd`, so a configured
service may write `uses: apache2` while resolving to the same catalog profile. A
configured service may also declare aliases; `sermoctl` normalizes those aliases to
the canonical configured service name before status, start, stop, restart,
reload, monitor, SLA and process/lock commands. Catalog aliases are also usable
as service names only in the conservative one-service case where a configured
service has the same name as the catalog service, such as `name: smb`,
`uses: smb`, with catalog alias `samba`.

## Library services

A library service describes a shared library so configured services can restart
when it is upgraded. It only needs identity plus the file to watch:

```yaml
name: glibc
display_name: "GNU C Library"
description: "Standard C library (libc)"
variables:
  binary: "/lib64/libc.so.6"        # the file watched for changes (and its version)
preflight:
  file: { type: file, path: "${binary}" }
```

Set a top-level `interval` on an app or library profile to override the global
`engine.artifact_interval` (default `5m`) used for artifact inspection.
When a service subscribes through `restart_on_change.libraries`, Sermo also
adds that library file as a required preflight check for start, restart, reload
and resume; a missing, non-regular or empty library file blocks the operation.

A configured service (or catalog service definition) opts in with
`restart_on_change`. Packaged catalog services that link versioned apps declare
the app form by default; custom services can use the same shape. `paths` is for
configuration files that require a full restart rather than a reload:

```yaml
restart_on_change:
  config: true
  version: true
  paths:
    - ${config}
  libraries: [glibc, pam]
  apps:
    containerd:
      level: minor
  messages:
    path: "${display_name} will restart after config change: ${change.path}"
    app: "${display_name} will restart after version change of ${change.app}: ${change.old_version} -> ${change.new_version}"
```

On resolution this desugars into one remediation rule per path that restarts the
service when that config path changes, one rule per library that restarts the
service when that library's file changes, and one rule per app that restarts the
service when the linked app's version changes at the selected level. Each
generated rule alerts first, inheriting the normal rule/global notifiers, then
runs the restart action through the safe operation engine:

```yaml
rules:
  restart-on-change-config-1:
    type: remediation
    if: { changed: { path: /etc/containerd/config.toml } }
    then:
      actions:
        - type: alert
          message: "containerd will restart after config change: ${change.path}"
        - type: restart
  restart-on-change-glibc:
    type: remediation
    if: { changed: { library: glibc, path: /lib64/libc.so.6 } }
    then:
      actions:
        - type: alert
          message: "containerd will restart after library change: ${change.library} (${change.path})"
        - type: restart
  restart-on-change-containerd-version:
    type: remediation
    if: { changed: { app: containerd, level: minor } }
    then:
      actions:
        - type: alert
          message: "containerd will restart after version change of ${change.app}: ${change.old_version} -> ${change.new_version}"
        - type: restart
```

The daemon samples these paths and linked app versions at `engine.artifact_interval`
(or the applicable local `interval`). A service may evaluate rules more often,
but it reuses that sample; detection is therefore delayed by at most one artifact
cadence plus one scheduler tick.

The optional `config` and `version` booleans are inherited permissions. When
absent they default to allowed, preserving the current service behavior.
`config: false` suppresses generated `paths` restart rules. `version: false`
suppresses generated `apps` and `libraries` restart rules. Global defaults may
set only these two booleans:

```yaml
defaults:
  restart_on_change:
    config: false
    version: true
```

A catalog service or configured service may override either flag in its local
`restart_on_change` block.

### Turning restart-on-update off per host

Three mechanisms restart or reload a service when something it depends on
changes. Each has a permission gate, and every gate is settable per host:

| Mechanism | Trigger | Gate | Notifies |
|---|---|---|---|
| `restart_on_change` | app version, library, config path | `config:` / `version:` | yes — alert, then restart |
| `reload_on_change` | config path | `config:` | **no** — reload only, no alert action |
| [`restart_on_stale_binary`](configuration.md#stale_binary--service-running-a-replaced-binary) | binary replaced on disk | the flag itself | yes — alert, then restart |

Two levels of granularity, both on the host, neither requiring a catalog edit:

```yaml
# /etc/sermo/sermo.yml — the whole host
defaults:
  restart_on_change: { version: false }   # no version-driven restarts here
  reload_on_change:  { config: false }    # no config-driven reloads either
  restart_on_stale_binary: false
```

```yaml
# /etc/sermo/services/nginx.yml — just this service, on this host
name: nginx
uses: nginx
restart_on_change: { version: true }      # …except nginx
```

The merge is **deep**, which is what makes the host level usable: setting only
the gate in `defaults:` folds into the catalog's block instead of replacing it,
so the `paths`, `apps` and `messages` the catalog ships survive. Precedence is
`defaults:` < catalog < the host's per-service file — see
[Resolution order](configuration.md#resolution-order). A scalar the catalog sets
explicitly (as the OVS services do for `restart_on_stale_binary`) therefore beats
a host-wide default; override it in the per-service file instead.

Global `defaults:` accepts **only the gates**, never `paths`/`apps`/`libraries`/
`messages`: a host decides *whether* these restarts happen, the catalog decides
*what* triggers them.

`messages` is optional and local to the service or catalog service. It accepts
`path`, `app` and `library` templates. The templates are expanded like normal
service strings first (`${display_name}`, `${config}`, …), then rule runtime
placeholders such as `${change.path}`, `${change.app}`, `${change.old_version}`
and `${change.new_version}` are filled when the alert is emitted.

The restart runs through the normal safe engine (guards, cooldown, max_actions),
and the change is acknowledged once the restart succeeds, so it fires once per
upgrade rather than every cycle. Referenced library names must be `library`
services. Referenced app names must also appear in the service's `apps:` list,
and the app must provide a `version` or `version_short` command. App levels are
`major`, `minor` and `patch` (default for the short form `apps: [containerd]`).
If the app binary or version command is broken, Sermo treats the version sample
as invalid, does not update the version baseline, and does not restart the
service.

## Reload on config change (`reload_on_change`)

Many services re-read their configuration **without a restart** — systemd
(`systemctl daemon-reload`), nginx (`nginx -s reload`), named (`rndc reload`),
rsyslog, … `reload_on_change` watches config files/directories and, when one
changes, runs the **reload** action instead of a disruptive restart:

```yaml
# catalog/services/systemd.yml
reload:
  command: ["systemctl", "daemon-reload"]
  when: always
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

Note the single action. Unlike `restart_on_change`, the generated rule carries
**no `alert` action, so a reload sends no notification** — it is recorded as an
operation-result event and nothing more. That is deliberate for a non-disruptive
reload; if you want to be told, add your own alert rule on the same `changed:`
condition. Set `reload_on_change: { config: false }` to suppress the generated
rules entirely, per service or per host.

The **`reload`** action runs through the same safe engine as restart but in
place: it runs **preflight first** (so an invalid config — caught by the
service's `config` check — blocks the reload), reloads, then verifies health.
`reload` is also a valid rule action on its own (`then: { action: reload }`) and
is blocked by guards that list `reload`, like any other service action.

**What "reload" runs.** By default it is the backend per-unit reload —
`systemctl reload <unit>` (which runs the unit's `ExecReload`, e.g. `nginx -s
reload`) or OpenRC's init-script `reload`. A catalog service can override this with
**`reload.command`** when the reload is not a per-unit operation — systemd
itself reloads with `systemctl daemon-reload`, not `systemctl reload systemd`:

```yaml
reload:
  command: ["systemctl", "daemon-reload"]
  when: always
```

If the init backend reports no reload support and the service has no valid
`reload.command` or `reload.signal` fallback, Sermo rejects the `reload` action
before execution. The CLI reports the unsupported reload and the web UI disables
the reload button through `can_reload=false`.

### Native reload (`reload:`) — when the init can't, Sermo can

Some services reload in place (e.g. `sshd`, `snmpd`, `proftpd`, `prometheus`,
`loki` re-read their config on **`SIGHUP`**) but their **systemd** unit defines
**no `ExecReload`**, so `systemctl reload <unit>` fails — even though the service
itself supports it (the same service under OpenRC usually does reload, via an
init-script `reload()` that sends the signal). The `reload:` block closes that
gap: it declares a **native reload** Sermo performs itself, by signalling the
service's main process or running a command.

```yaml
reload:
  signal: HUP        # send this signal to the main process (HUP, USR1, USR2, …)
  when: auto         # auto (default): use the init's reload if the unit/script
                     #   has one, otherwise do this; always: never use the init,
                     #   always do this
# or, instead of a signal, a command:
reload:
  command: ["nginx", "-s", "reload"]
  when: auto
```

- **`when: auto`** (default) asks the backend whether it can reload — systemd's
  `CanReload` (the unit has an `ExecReload`), or an OpenRC init script that
  defines `reload`. If it can, the init reload runs; if it can't, Sermo runs the
  native reload. So the *same* catalog service definition reloads correctly on a host
  whose unit exposes reload **and** on one whose unit doesn't.
- **`when: always`** always runs the native reload and never the init's — the
  right choice for reloads that are not per-unit operations. A bare
  `reload: { command: [...] }` defaults to `when: auto`, so set `when: always`
  when the command must always run.
- **Signal target.** The signal goes to systemd's `MainPID`, or — on OpenRC, or
  any unit with no MainPID — to the PID in the service's `pidfile:`. The pidfile
  fallback is only used when that PID also matches a `processes:` selector with
  exact `exe` and `user`; a stale pidfile must not signal an unrelated process.
  A signal reload with neither target available fails. Services without pidfile
  metadata reload by signal only on systemd; on OpenRC they rely on the init
  script's own `reload` (`when: auto`).

#### Catalog author checklist: init scripts and fallbacks

Before shipping or changing a catalog service with `reload.signal`, verify every
init backend listed in `service:` and every fallback Sermo may use. Do not check
only the platform where the profile was first written.

1. Inspect the real packaged init definitions. For OpenRC, read
   `/etc/init.d/<unit>` and the matching `/etc/conf.d/<unit>`; for systemd, read
   the unit and its reported reload/PID metadata.
2. Record whether the init backend can reload by itself. With `when: auto`, Sermo
   prefers the backend reload when systemd reports `CanReload=yes` or the OpenRC
   script defines `reload()`. If a host lacks that path, Sermo's native fallback
   must still be safe.
3. For any OpenRC-capable `reload.signal`, declare a canonical `/run/...`
   `pidfile:` candidate and a `processes:` selector with exact `exe` and `user`.
   The executable must be the resolved `/proc/<pid>/exe` path (usually through
   the linked app's binary variable), and the user should be a service variable
   so local packaging differences can override it.
4. If OpenRC scripts differ by distribution, encode the real pidfile candidates
   as a list or an `os:` branch. Do not ship a single path that was verified on
   only one distro.
5. If a backend has no pidfile or no trustworthy `exe` plus `user` identity, do
   not rely on `reload.signal` for that backend. Use an argv `reload.command`, or
   rely only on the init backend's reload when every configured backend validates.
6. Run the catalog validation tests for both init backends before release.

Useful host checks:

```bash
sermoctl backend
systemctl cat <unit>
systemctl show -p CanReload -p MainPID -p PIDFile -p User <unit>
sed -n '/^reload()/,/^}/p' /etc/init.d/<unit>
grep -E '^(command|command_user|pidfile|.*PIDFILE)=' /etc/init.d/<unit> /etc/conf.d/<unit>
readlink -f /usr/sbin/<service>
namei -l /run/<service>.pid
```

Useful catalog audit while developing:

```bash
go test ./internal/config -run 'TestRealCatalog(AllServicesValidate|ReloadServicesResolve)$' -count=1
```

The reload path chosen by the backend or by `reload:` is what the **`reload`
action**, `reload_on_change`, the `sermoctl reload <svc>` command and the web UI
reload button all run. It is a service-control concept: it applies to services,
not to host watches, which observe host metrics and fire hooks rather than
reload a unit.

## App dependencies (`apps`)

A service can link one or more **apps** from `catalog/apps` (java, openssl,
perl, …). An app owns the tool's **binary**, **health** and **version** checks.
Link them with `apps:`:

```yaml
# catalog/services/tomcat.yml — Tomcat runs on the JVM
apps: [java, "tomcat-${version}"]
```

On resolution each linked app's preflight checks are injected into the service's
preflight under keys namespaced by the app name (`<app>-<check>`), carrying the
app's own `variables.binary` path, health probe and version command. Link an app
only when the service action itself requires it. For example, Backrest can be
monitored and restarted without `restic`; `restic` is required by a backup
operation, which reports its own error if the binary is absent. Likewise,
Samba's `winbindd` belongs in an `enable_if`-guarded process/watch, not in
`apps`, because it depends on the host's Samba configuration.

When a service links several required apps, each one's checks stay distinct:

```yaml
preflight:
  java-binary:  { type: binary, path: /usr/bin/java }
  java-health:  { type: command, command: ["/usr/bin/java", "-help"] }
  java-version: { type: command, command: ["/usr/bin/java", "-version"] }
```

App variables are also available to the service. They are always exposed with a
normalized app-name prefix (`${java_binary}`, `${php_fpm_binary}`, ...). If the
service links exactly one app, those variables are additionally available without
the prefix as defaults, so service-specific checks can use `${binary}` while the
app keeps ownership of the actual path. Local `variables:` entries on the catalog
service or configured service override either form; when several apps are linked,
use the prefixed names.

Because they run in **preflight**, a missing or wrong-version runtime fails the
service's preflight, which **blocks start/restart/reload/resume** (a
preflight-failed operation never executes the action) — you do not start,
restart, reload or resume a service whose runtime is absent.
The link is many-to-many: a service lists several apps, and one app is shared by
every service that lists it. Validation reports an `apps:` entry that does not
resolve to a catalog app, so dangling runtime links are caught before deployment.
The service keeps its own `variables.binary`,
`version` and `config` checks (the **config** test is always service-specific,
never moved to an app). Referenced names must be `app` services.

## Metadata fields

A catalog service or configured service may carry optional human-facing metadata:

```yaml
name: mariadb
display_name: "MariaDB"      # pretty label; falls back to name when absent
description: "..."           # free-text note; shown verbatim, nothing when absent
category: "database"         # optional WebUI grouping/filter label
type: "database"             # optional free-form classification; recorded, not acted on
```

These fields are optional and behave differently when missing:

- **`display_name`** is the label used wherever Sermo shows the catalog entry to
  a human (e.g. `sermoctl services`, `sermoctl apps` and the Web UI). When it is
  absent or blank, Sermo falls back to `name`. Set it only when it adds something
  over `name` — a proper brand (`MariaDB`, `PostgreSQL`, `OpenSSH`) or a version
  (`PHP-FPM 8.3`). If the display name would just repeat `name`, leave it out and
  let the fallback apply.
- **`description`** is an optional free-text note. It has **no fallback**: when it
  is absent, nothing is shown for it — Sermo never substitutes `name`. Use it for
  a real sentence, not a restatement of the name.
- **`category`** groups and filters Services, Installed applications and
  Installed libraries in the WebUI. When absent or blank, services use
  `service`, apps use `app` and libraries use `library`.
- **`type`** is an optional free-form classification label (e.g. `database`,
  `cache`, `queue`, `webserver`, `appserver`, `tunnel`) used in the catalog to
  organize entries. It is recorded but **not currently consumed** by the engine
  and has no effect on monitoring, grouping or remediation.

`display_name`, `description` and `category` must be strings if present;
validation rejects non-string values.

### Built-in variables

The variables in the table below are always available during resolution
**without being declared** under `variables` — so a catalog service can parameterize
human-facing strings (and paths) instead of hardcoding them:

```yaml
rules:
  block-restart-during-maintenance:
    type: guard
    blocks: [restart, stop]
    then:
      action: block
      message: "${display_name} maintenance is active" # → "MariaDB maintenance is active"
variables:
  binary: "/usr/bin/qemu-system-${arch}"             # → /usr/bin/qemu-system-x86_64
preflight:
  binary: { type: binary, path: "${binary}" }
```

An explicit `variables` entry of the same name always takes precedence over a
built-in. `${arch}`/`${os}` are baked **on load** (everywhere — variable values
and app discovery paths included); the rest resolve per service, and
the runtime ones (`${date}`/`${event}`/`${action}`) only in rule `message:`
strings. The `SERMO_ARCH` / `SERMO_OS` / `SERMO_HOST` / `SERMO_HOSTNAME` /
`SERMO_INIT` / `SERMO_USER` environment variables override the matching built-in
(handy for testing or building config off-host).

`${user}` is a config-load built-in. It uses `SERMO_USER` when set, otherwise
the user running Sermo. It is intentionally separate from the runtime
`engine.user_lookup` resolver used for process selectors and `kill_only_if`; set
`SERMO_USER` when you need `${user}` to be deterministic while generating or
validating config off-host.

| Variable          | Value                                          | Resolved        |
|-------------------|------------------------------------------------|-----------------|
| `${name}`         | the resolved service name                     | resolution      |
| `${display_name}` | the display name (falls back to name)          | resolution      |
| `${service}`      | the service's primary unit name                | resolution      |
| `${host}`         | hostname (`SERMO_HOST` override)               | resolution¹     |
| `${hostname}`     | short hostname (`SERMO_HOSTNAME`)              | resolution⁵     |
| `${init}`         | detected init system (`SERMO_INIT`)            | resolution      |
| `${user}`         | Sermo's user (`SERMO_USER` override)           | resolution⁴     |
| `${pidfile}`      | conventional `/run/<unit>.pid`                 | resolution⁴     |
| `${port}`         | the top-level `port:` field (when set)         | resolution³     |
| `${arch}`         | machine architecture (`SERMO_ARCH`)            | load (baked)    |
| `${os}`           | os-release id (`SERMO_OS`)                     | load (baked)    |
| `${date}`         | event timestamp (RFC3339)                      | runtime²        |
| `${event}`        | the firing rule's name                         | runtime²        |
| `${action}`       | the action taken (restart/start/stop/reload/resume) | runtime²        |

¹ `${host}` only applies when the service does not define a `host` variable (a
bind address like `127.0.0.1`); an explicit `host` always wins.

⁵ `${hostname}` is the **short** hostname — the first label before the first dot
(`node1` on `node1.example.com`) — distinct from `${host}` (which keeps the full
detected hostname / bind-address fallback). Use it for systemd instance units
keyed by host identity, e.g. `service: "ceph-mon@${hostname}"` → `ceph-mon@node1`.
For numeric multi-instance services (e.g. one OSD per device) use a `%n` service
template whose `service:` carries `${n}`. Sermo materializes `ceph-osd0…N` from
active units such as `ceph-osd@0.service`, then links the generic `ceph-osd` app
for binary validation. An explicit `hostname` variable (or `SERMO_HOSTNAME`)
wins.

⁴ `${user}` and `${pidfile}` are fallbacks: a service's own `user` (a service
account such as `www-data`) or `pidfile` variable always wins. Put the pidfile
variable in the service-level `pidfile: "${pidfile}"`, and use `user: "${user}"`
inside any `processes:` selector that should be tied to the service account.

Runtime paths in Sermo config use the canonical `/run` spelling. Do not write
new `/var/run` pidfiles, sockets or lockfiles in catalog services, generated
services or examples. Linux keeps `/var/run` as compatibility for `/run`, and
older init scripts, service managers or packaged configs may still report that
spelling; detected paths should be normalized to `/run/...` before they are
committed to config.
Before adding a new runtime path, check whether it or a parent directory is a
symlink (`readlink -f <path>` or `namei -l <path>`), then record the canonical
target path rather than the alias.

² `${date}`/`${event}`/`${action}` are substituted when the worker emits a rule
message, so they belong in `message:` strings — e.g.
`message: "[${host}] ${service}: ${event} → ${action} at ${date}"`. Elsewhere they
stay literal.

³ `${port}` mirrors a top-level `port:` field on the configured service (or catalog
service), so an instance can set its listen port once and have every `${port}`
reference resolve to it:

```yaml
name: db-inst2
uses: dbserver
port: 3307          # → ${port} everywhere in the catalog service
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

watches:
  http:
    check:
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
pidfile:                        # the resolved value becomes the OS's list
  os:
    fedora: [/run/postgres.pid]
    gentoo: [/run/postgres${port}.pid, /run/postgres.pid]
    default: [/run/postgres.pid]
```

The service-level `pidfile:` accepts a single path or a **list of candidates**.
Discovery tries them in order and uses the first that points at a running
process, so per-OS or versioned pidfile locations all resolve without personal
config. Use `pidfiles:` instead when one service intentionally owns several
resident processes that each have their own pidfile.

For oneshot loaders that do not keep a resident process (for example firewall
loaders), set `processes: {}` explicitly. That prevents Sermo from deriving a
process selector from init metadata and keeps the WebUI from showing CPU/memory
process totals for a service that cannot have them.

### `control: libvirt` — QEMU/libvirt virtual machines

A service can be controlled as a libvirt/QEMU virtual machine instead of a
systemd/OpenRC unit:

```yaml
name: vm-web01
control:
  type: libvirt
  uri: qemu:///system
  domain: web01
  socket: /run/libvirt/libvirt-sock     # or /run/libvirt/virtqemud-sock on modular libvirt

watches:
  vm:
    check:
      type: libvirt
      socket: /run/libvirt/libvirt-sock
      query: qemu:///system
      params: { domain: web01 }

processes:
  qemu:
    exe: /usr/bin/qemu-system-x86_64
    cmd: "web01|2b3f3d26-bb45-4b25-b65a-1e3ef86fc1a4"
    user: qemu
```

`control.domain` is the libvirt domain Sermo operates. `uri` defaults to
`qemu:///system`; `socket` defaults to `/run/libvirt/libvirt-sock` unless `host`
is set for a remote libvirt TCP connection. Modular libvirt deployments often
expose QEMU domains through `/run/libvirt/virtqemud-sock`; set `socket` to that
path when the monolithic socket is absent. `uuid` is optional and, when set,
Sermo looks up the domain by UUID instead of name.

The safe operation engine is unchanged: locks, guards, preflight, postflight,
operation timeouts and remediation policy still apply. The primitive actions are
libvirt operations:

- `start` creates/boots the defined domain (`DomainCreate`).
- `stop` requests a graceful guest shutdown (`DomainShutdown`); it does not
  destroy the VM.
- `restart` is still Sermo's safe stop+start flow.
- `resume` resumes a paused domain (`DomainResume`).
- `reload` is unsupported for VM domains unless a future service-specific
  mechanism is added.

Libvirt status maps to Sermo status as follows: running/blocked → `active`,
paused/pmsuspended → `paused`, shutoff/shutdown/nostate → `inactive`, crashed →
`failed`. The CLI and web UI still expose backend `status=paused`; the aggregated
service state is `failed` while monitoring is active, or `stopped` when Sermo
monitoring is paused.

Process discovery is intentionally explicit in this first VM integration. If you
want process metrics or residual-process reporting for the QEMU process, add a
restrictive `processes:` selector as above: exact `exe` and `user` plus a `cmd`
regex that narrows the shared QEMU binary to the intended domain or UUID. The
cmdline selector narrows discovery; residual signaling is still
authorized only by `stop_policy.kill_only_if`.

`sermoctl wizard vm` can generate this service shape from domains
detected through the local libvirt socket. It probes both
`/run/libvirt/libvirt-sock` and `/run/libvirt/virtqemud-sock` and writes the
socket it actually used into the generated service and check.

### `control: libvirt-network` — libvirt virtual networks

A service can control one libvirt **virtual network** (`virsh net-start` /
`net-destroy` territory) instead of a domain:

```yaml
name: libvirt-net-default
category: virtual-network
control:
  type: libvirt-network
  uri: network:///system
  network: default
  socket: /run/libvirt/virtnetworkd-sock
  guard_socket: /run/libvirt/virtqemud-sock

processes:
  dnsmasq-root:
    exe: /usr/bin/dnsmasq
    cmd: '--conf-file=/var/lib/libvirt/dnsmasq/default\.conf([[:space:]]|$)'
    user: root
    delegated: true
```

`control.network` is the libvirt network name. Network RPC runs over
`socket`/`uri` (defaults `/run/libvirt/libvirt-sock` and `network:///system`,
which monolithic libvirtd accepts too); the guest-attachment guard below needs
**domain** APIs, which on modular libvirt live on a different daemon, so it
dials `guard_socket`/`guard_uri` (defaults: the network socket and
`qemu:///system`). `host`/`port` select a TCP endpoint exactly like
`control: libvirt`, shared by both sessions.

The safe operation engine is unchanged: locks, guards, preflight, operation
timeouts and remediation policy still apply. The primitive actions are libvirt
network operations:

- `start` starts the defined network (`NetworkCreate`).
- `stop` destroys the network (`NetworkDestroy`) — but **hard-refuses while any
  live guest has an interface on the network** (matched by source network name
  or by the network's bridge, and counting paused guests: their taps stay
  attached). Destroying such a network cuts guest connectivity and the taps do
  not reattach on the next start. No configuration option relaxes this guard,
  and an unverifiable guest blocks the destroy rather than being skipped.
- `restart` is still Sermo's safe stop+start flow, so it inherits the guard.
- `reload` and `resume` are unsupported for virtual networks.

Network state maps active → `active` and inactive → `stopped`/`failed`
following the usual monitoring semantics.

Why manage networks at all: libvirt spawns one dnsmasq pair per NAT network,
and that pair deliberately survives daemon restarts (see the packaged
`virtnetworkd` profile, where it is `delegated`). After a dnsmasq package
upgrade those processes keep running the replaced binary and only a
**network** restart renews them — so the generated network service is the one
target whose `stale-binary` finding a restart genuinely fixes. Attribute the
pair with `delegated: true` selectors like the example above: Sermo observes
it, libvirt owns its lifecycle.

The fleet installer generates one such service per **active network with a
libvirt-owned IP** (the shape that spawns dnsmasq); bridge-mode networks are
recorded as skipped because a restart renews nothing on them, and a host with
no domain-API socket is skipped too — without one the attachment guard could
not verify guests, and an unverifiable destroy target must not exist.

### `control: docker` — Docker containers

A service can be controlled as one Docker container instead of a systemd/OpenRC
unit:

```yaml
name: web-container
control:
  type: docker
  container: web
  socket: /run/docker.sock

watches:
  docker:
    check:
      type: docker
      socket: /run/docker.sock
      container: web
      on_change: true
      expect:
        container.status: { op: "==", value: running }
        container.health: { op: "==", value: healthy }
```

`control.container` is the Docker container name or id Sermo operates. With no
`socket` or `host`, control uses `/run/docker.sock`; set `socket` for another
local socket, or set `host` and optional `port`/`tls` for a TCP Docker API
endpoint. `control.interface` is not supported for control; interface-bound
egress remains available on Docker checks.

The safe operation engine is unchanged: locks, guards, preflight, postflight,
operation timeouts and remediation policy still apply. The primitive actions are
Docker Engine API operations:

- `start` calls the container start endpoint.
- `stop` calls the container stop endpoint with no Docker-side kill escalation;
  Sermo's operation timeout is the outer bound, and residual handling remains in
  Sermo's stop policy.
- `restart` is still Sermo's safe stop+start flow.
- `resume` unpauses a paused container.
- `reload` is unsupported for Docker containers unless a future
  service-specific mechanism is added.

Docker status maps to Sermo status as follows: running -> `active`, paused ->
`paused`, created/exited -> `inactive`, restarting/dead/removing -> `failed`.
The CLI and web UI still expose backend `status=paused`; the aggregated service
state is `failed` while monitoring is active, or `stopped` when Sermo monitoring
is paused.

For process metrics and residual-process reporting, Sermo reads the container's
`State.Pid` from Docker inspect and discovers that process tree. You normally do
not need a `processes:` selector for a controlled container. Residual signaling
is still authorized only by `stop_policy.kill_only_if`.

`sermoctl wizard docker` can generate this service shape from containers
detected through the local Docker socket.

### `restart_policy` — restart strategy

Every restart keeps the common operation-engine gates: one operation lock, named
runtime locks, required preflight, guards, the active-service identity check when
available, the operation timeout, backend status verification, postflight and
exactly one result event. The service chooses only how the restart action itself
is performed:

```yaml
restart_policy:
  mode: native
```

- `staged` (default) runs Sermo's `Stop` → residual discovery/reaping → init
  state reconciliation → `Start` flow. `stop_policy`, stopped-state cleanup and
  residual reporting apply in full.
- `native` invokes one atomic `Restart` on the selected systemd/OpenRC backend.
  Beyond the stale-init reconciliation described below, it runs no stop phase, no
  residual reaper and no stopped-state cleanup. A backend error fails the
  operation; Sermo never falls back silently to staged restart.

Both modes first **reconcile a stale init state**. When the backend reports the
unit as stably `inactive` or `failed` while the service's own processes are still
running, the init has lost track of a live daemon — a systemd unit that dropped
its `MainPID` is the usual way in. `unknown` and transitional states do not prove
that divergence and never enter the reaper. Neither mode recovers from real drift
alone: a native restart asks the init to signal a PID it no longer knows, and the
replacement daemon then collides with the survivor over its port, socket or lock.
Sermo therefore clears those survivors first, under the service's own
`stop_policy` and with `delegated` processes excluded, reconciles the init state,
and only then runs the restart; the result message reports it. Nothing is
signalled that a stop would not have signalled. Status-query, discovery or
init-reset errors return `failed`, and any survivor returns
`orphan_processes`; both outcomes stop before the backend restart, so Sermo
never launches a second daemon.

`native` is valid only for init-managed services; a service with `control:`
(Docker container or libvirt domain) must use `staged`. Use native mode when the
init unit deliberately owns a delegated process tree whose workload descendants
may survive a daemon restart and therefore must not be classified as failed-stop
residuals. The packaged `containerd` and Docker Engine profiles use it for shims,
proxies and container workloads, and the `glusterd` profile uses it because its
`KillMode=process` unit deliberately keeps the brick and self-heal processes
serving across a restart. The systemd-only `polkit` profile also uses it because
D-Bus activation can cancel an isolated stop while immediately starting a new
daemon generation. Ordinary multi-process daemons keep `staged`: native
mode must not be used merely to hide a service that fails to stop cleanly.

With `also_service`, native restart leaves auxiliary units active and restarts
only the primary atomically; explicit `start`/`stop` and staged restart retain
the wrap ordering below. `also_apply` still sends the restart action through
each referenced service's own engine and policy.

### `also_service` — auxiliary init units

A service can name **auxiliary init units of its own** (a `.socket`, `.timer`,
companion unit) that are started/stopped **together with the primary**, in the
same operation. A staged restart composes those two operations. It mirrors the
`service:` shape (per-init lists, resolved for the active backend):

```yaml
service:
  systemd: [docker]
  openrc:  [docker]
also_service:
  systemd: [docker.socket]
```

These are plain init units driven directly by the service manager (not separate
monitored services — that is `also_apply`). They are acted on in **wrap /
socket-activation order**: started **before** the primary (strict — a failure
aborts the operation before the primary starts), and stopped **after** it
(best-effort — a stop failure is reported in the result message but does not fail
an already-successful stop). `reload` touches the primary only. The primary's
guards, locks and preflight wrap the whole operation. Listing the primary unit in
`also_service` is rejected. A native restart also touches the primary only and
leaves these auxiliary units active, as described above.

### `also_apply` — cascade to other services

Where `also_service` acts on *init units of this service*, `also_apply` acts on
**other Sermo services**: when this service is started/stopped/restarted (by a
remediation rule or a manual `sermoctl`), the same action runs on each listed
service through **its own** guarded operation.

```yaml
also_apply: [nginx, varnish]
```

- **Dependency-aware order**: on `start`/`restart` the primary acts first, then
  the additionals (a dependent comes up after what it depends on); on `stop` the
  additionals act first, then the primary.
- **Each target keeps its own guards/locks/preflight** (it runs its real
  operation). A target's remediation cooldown and paused/`unmonitor` state are
  *not* consulted — `also_apply` is an explicit relationship.
- **Best-effort & loop-safe**: a failing/blocked target is reported (a `cascade`
  event; a blocked target is retried once) but does not fail the primary; cycles
  are cut by a visited set.
- Entries must be configured services and must not include the service itself.
- `sermoctl start|stop|restart <svc> --no-cascade` acts on exactly one service.
- `sermoctl reload <svc>` and `sermoctl resume <svc>` act on the primary only
  (no cascade). Use `sermoctl daemon reload` to reload the running `sermod`
  configuration. In the web UI the per-service **reload** button is enabled only
  when the service is `active` and Sermo reports `can_reload=true` from either
  the init backend (`ExecReload`/OpenRC `reload`) or a valid `reload:` fallback;
  **resume** is enabled only while it is `paused`.

`also_apply` (other services) and `also_service` (this service's init units) are
complementary; a service may use both.

### `processes:` by executable or cmdline

A `processes:` selector matches a process by the **AND** of the fields you set;
at least one of `exe`/`cmd` is required. The map key is the selector's role name
in status, metrics and alerts:

```yaml
processes:
  unifi: { cmd: "java .*unifi", user: unifi, group: unifi }
  mongo: { exe: "${mongod_binary}", user: unifi }
```

- `exe` — exact resolved `/proc/<pid>/exe` (fail-safe; never cmdline).
- `cmd` — a Go RE2 regex matched against the process **cmdline** (argv joined).
  Use it for shared binaries (`java .*unifi`, `openvpn .*tun1\.conf`) when one
  executable serves several instances. The cmdline is spoofable, so `cmd` never
  authorizes signaling by itself — a kill still demands the exact resolved exe
  **and** the real UID — but it does narrow the identity `force_kill: auto`
  derives, so a daemon that shares its executable with its own workload keeps
  that distinction when residuals are signalled.
- `delegated` — `true` marks processes the service owns but Sermo must never
  signal: a workload tree the init unit deliberately keeps alive across a daemon
  restart (GlusterFS bricks and self-heal, container shims). They stay visible in
  monitoring, are never counted as residuals of a stop, and contribute no kill
  authority. **Delegation flows down the process tree**, because a workload
  process owns whatever it spawns: marking an SSH session covers the user's shell
  and everything that shell runs, none of which would match a selector of its own.
  Use it when the unit stops only its main process — systemd `KillMode=process` —
  so that stopping the daemon does not take its workload down with it.
- `user` / `group` — the process real UID / GID owner.

Do not use a generic helper executable shared by several units as a service
selector. On systemd, cgroup attribution identifies the unit's processes; where
there is no unique exact identity, leave the helper unselected. A broad helper
selector can cross-attribute another unit's live process as a residual and safely
block the restart.

These feed monitoring **and** the residual reaper, so a richer selector lets a
stop catch and kill more leftovers (an unkillable residual stays
`orphan_processes`). The `process` *check* still matches by `exe`/`user` only.

Set `stop_policy.force_kill: auto` to make every named selector that has both
an exact `exe` and `user` authorize cleanup of that same residual identity.
Sermo keeps each executable/user pair together — plus that selector's `cmd`, when
it declares one — sends TERM, rediscovers, then sends KILL only to the same
verified survivor. A selector with only `cmd`, an unresolved executable, a
`delegated` selector, or a process outside the configured identities remains
an `orphan_processes` failure and the restart never starts a second daemon.
`force_kill: true` still requires the explicit `kill_only_if` selector and is
the appropriate override when the configured process identities are not the
desired kill set; `force_kill: false` disables escalation.

### Dependency isolation (`allow_dependencies`)

Every start and stop Sermo issues is isolated from the init system's dependency
graph, so restarting one service can never restart others. A restart is composed
as stop then start, and both halves carry the flag — on systemd it is the stop
that would otherwise drag down units bound to this one.

For a socket-activated systemd unit, leaving its socket untouched can make
systemd start that **same unit** again immediately after the isolated stop. When
the stop succeeded, every live residual is attributed by the backend to that
unit, and its state is again `active`, Sermo treats that as the completed restart:
it runs postflight but does not issue a second start or touch the activating
socket. This is not an exception for arbitrary leftovers: a selector-only
residual, an inactive/unknown unit, or any non-systemd backend still returns
`orphan_processes` and never starts the service.

| Backend | Command |
|---|---|
| systemd | `systemctl <verb> --job-mode=ignore-dependencies -- <unit>` |
| OpenRC | `rc-service --nodeps <service> <verb>` |

Only state-changing verbs are isolated. Status queries, `reload` and
`reset-failed`/`zap` never propagate, so they are issued unchanged.

**This cuts both ways, deliberately.** Isolation also means a start does *not*
pull up what the service requires: if a dependency is down, the start proceeds
and the service may fail on its own. systemd's documentation warns that
`ignore-dependencies` can leave the system inconsistent — that is the trade
accepted here, in exchange for never taking down a service nobody asked to
touch.

Measured on real hosts, `nfs-server` is the catalog exception that needs normal
dependency propagation. It `ConsistsOf` `nfs-mountd` and `nfs-idmapd`; an
isolated staged restart stops those companions but cannot pull the required
mount daemon up again, leaving NFSv3 mount requests unavailable while the kernel
NFS port remains healthy. The packaged `nfs` profile therefore sets
`allow_dependencies: true`. The NFS profile itself is a no-resident-process
service because its server runs in the kernel; the separate `rpc-mountd` profile
owns process discovery and stale-binary reporting for the userspace daemon.

Set the flag only on a service that is useless without the units it requires,
and where you would rather it pull them up than fail:

```yaml
name: some-service
allow_dependencies: true
```

Only the packaged `nfs` service ships with it because the init graph is part of
that coordinating service's lifecycle. For ordinary companion units,
`also_service:` is the explicit form and remains preferable to relying on the
init system's graph.

It inherits from global `defaults:` like `dry_run`, so a whole host can opt back
in at once:

```yaml
defaults:
  allow_dependencies: true
```

Docker and libvirt services are unaffected: their backends have no dependency
graph of this kind.

### Stopped-state invariants (`stop_policy`)

After a **clean** stop, the engine can verify the service left nothing behind:

```yaml
stop_policy:
  graceful_timeout: 30s
  pidfile_absent: true                      # the declared pidfile must be gone
  files_absent: [/run/postgresql/.s.PGSQL*] # stale sockets/locks (globs)
  clean_after_stop: false                   # master opt-in: delete on stop
```

- A lingering pidfile or `files_absent` match is a **warning** (the stop still
  succeeds, `ResultOK`) folded into the result message and surfaced in CLI/web —
  it means the service crashed or left junk. Residual *processes* keep their
  stronger `orphan_processes` (red) handling via the reaper.
- **`clean_after_stop`** is the single master switch for *all* active deletion
  after a clean stop. It is **opt-in (default `false`)**: with it off the engine
  only **verifies and warns** — it never deletes. Set it to `true` to enable
  cleanup, which then does two things:
  1. **deletes** any lingering `pidfile_absent`/`files_absent` artifact (the old
     `rm`-on-stop behavior), re-warning only if the delete fails; and
  2. **deletes** the `clean_on_stop` list below.

`clean_on_stop` lists files and directories to **delete** on a clean stop (a
maintenance cleanup, distinct from the `files_absent` invariant). It only deletes
when `clean_after_stop: true`; listed without the master flag it is inert (so you
can stage the list and enable it later):

```yaml
stop_policy:
  clean_after_stop: true                        # required to actually delete
  clean_on_stop:
    - /run/svc/foo.tmp                          # a file
    - /tmp/svc-*.lock                           # a glob (files)
    - { path: /var/cache/svc, recursive: true } # a directory tree
```

- A plain entry (string or glob) is deleted with `Remove` (file or empty dir);
  `{ path, recursive: true }` deletes a directory tree (`RemoveAll`).
- **Safety (strict):** every path must be absolute; a `recursive` entry must be a
  concrete (non-glob) path at least two levels deep and not the filesystem root or
  a shallow system directory (`/`, `/etc`, `/usr`, `/var`, `/var/lib`, …) — those
  are refused at validation time. A delete failure is a warning, not a failure.

### Unclaimed control-group members (`reap`)

The init backend attributes a whole control group to the service, so Sermo sees
processes there that no `processes:` selector claims. When such a process is also
outside the unit's principal process tree — reparented to PID 1 while still
counted against the unit — it is a **stray**: a probe that daemonized, a child the
daemon never reaped, a survivor of an earlier incarnation.

Strays are always reported (`sermoctl processes` shows `stray=true`). The optional
`reap:` block is what lets an operator clear them:

```yaml
reap:
  kill_only_if:
    users: [root]
    exe_any: [/usr/bin/dbus-daemon]
```

- Without the block, `sermoctl reap SERVICE --apply` lists every stray and signals
  none. Authorization is opt-in per service and is never inherited from
  `defaults:`: one global selector would hand every service the same kill authority
  over processes none of them can name.
- `kill_only_if` is the same paired selector `stop_policy` uses and passes the same
  gate — exact resolved `exe` **and** real UID; a delegated process never, an
  unresolvable exe never, PID 1 and kernel threads never.
- Unknown keys under `reap:` are rejected at validation time. A stray is reached by
  control-group membership rather than by a selector that named it, so a typo must
  not leave the action authorized by something you did not write.
- No rule action can reap, and a stop never consults this block — a restart reports
  a stray it cannot clear rather than killing it. See [safety.md](safety.md) for the
  full contract and [cli.md](cli.md) for the command.

Detection is separate from clearing: Sermo injects a `strays` check into every
init-managed service that declares selectors, reporting the count and the
executables without alerting. See
[configuration.md](configuration.md#strays--processes-the-service-cannot-account-for).

### `pidfile:` and `pidfiles:` shorthand (selectors + health checks)

A catalog service can declare a top-level `pidfile: <path>` to wire **both** uses of a
pidfile from one line:

```yaml
pidfile: /run/named/named.pid
```

When a catalog service legitimately uses different pidfile names across distributions,
declare candidates in preference order:

```yaml
pidfile:
  - /run/mysqld/mariadb.pid
  - /run/mysqld/mysqld.pid
```

When the pidfile is useful on one backend but legitimately absent on another
(for example OpenRC writes one while a systemd unit runs the daemon in the
foreground), keep the pidfile source for discovery but make the generated health
check auxiliary:

```yaml
pidfile: { path: /run/rngd.pid, optional: true }
```

Use `/run` here, not `/var/run`. If a distro init script or service manager
reports `/var/run/...`, write the equivalent `/run/...` path in the catalog
service definition while preserving Linux/init compatibility. Before committing a new
pidfile or socket path, resolve it with `readlink -f` or inspect it with
`namei -l`; if any component is a symlink, use the resolved canonical target.

On resolution this creates (a) an internal pidfile discovery selector — so the
parent process **and its descendants** are discovered and monitored without
adding a public `processes:` entry — and (b) a `pidfile` health check gated by
`requires: [service]`. Because of the gate, a missing or stale pidfile is
reported as an **error only while the service is active** (it means the service
died or lost its pidfile without the service manager noticing); a legitimately
stopped service is skipped, not alarmed.

A check already named `pidfile` is respected, so a catalog service that needs a
custom check can still spell it out. Public `processes:` entries stay limited to
`exe`/`cmd` selectors with optional `user`/`group`; do not put `pidfile` under
`processes:`. The shorthand path can reference variables (e.g. `pidfile:
"${pidfile}"`) and accepts a scalar path, a candidate list, or `{path: ...,
optional: true}`. Candidate lists are tried in order and pass on the first live
pidfile; if none exists, the backend PID fallback can still satisfy the gated
health check. `optional: true` keeps a missing pidfile as a warning instead of
making the service unhealthy.

When a single service owns several independent resident processes, use
`pidfiles:` as a map keyed by process role. Each role must also exist under
`processes:` with exact `exe` and `user`, so the pidfile PID can be tied back to
the process identity Sermo is allowed to observe:

```yaml
pidfiles:
  smbd: /run/samba/smbd.pid
  nmbd: /run/samba/nmbd.pid

processes:
  smbd:
    exe: "${smbd_binary}"
    user: root
  nmbd:
    exe: "${nmbd_binary}"
    user: root
```

Each `pidfiles.<role>` creates its own internal pidfile selector and its own
gated health check (`pidfile-smbd`, `pidfile-nmbd`, ...). A value may still be a
candidate list for that specific role. Do not combine `pidfile:` and
`pidfiles:` in the same service: `pidfile:` means "one logical PID with
candidate paths"; `pidfiles:` means "all of these roles must have a live
pidfile."

### `socket:` shorthand (gated health check)

A catalog service can declare a top-level Unix socket path when the active service should
leave a socket behind:

```yaml
variables:
  socket: /run/cups/cups.sock
socket: { path: "${socket}", optional: true }
```

On resolution this creates a `socket` health check gated by `requires: [service]`
and removes the top-level key. Like `pidfile:`, `socket:` accepts a scalar path,
a candidate list, or `{path: ..., optional: true}`. Use it for runtime sockets
owned by the service; protocol checks such as `redis`, `dbus` or `libvirt` still
use their own `socket` field inside the check body.

### `lockfile:` shorthand (gated health check)

A catalog service can declare one regular lockfile created by the active service:

```yaml
lockfile: /run/lock/subsys/smb
```

On resolution this creates a `lockfile` health check gated by
`requires: [service]` and removes the top-level key. Like `socket:`, `lockfile:`
accepts a scalar path, a candidate list, or `{path: ..., optional: true}`. It is
only evidence that the service left its own runtime lock artifact; it does not
block start/stop/restart/reload/resume and must not point under
`<paths.runtime>/locks`, which is reserved for Sermo operation locks.

### D-Bus service health

Use an embedded `type: dbus` watch when a daemon owns a stable system-bus name.
The catalog profiles for systemd managers, NetworkManager, firewalld, TuneD,
GDM and several desktop/hardware daemons use the same check path as host
watches. Prefer the default `peer` probe when the object implements it; use
`introspect` to require a public interface, or `property` to read one stable
scalar property. Set `require_owner: true` for these resident daemons so an
active unit with a lost D-Bus registration fails instead of passing as merely
activatable. These probes disable D-Bus auto-activation and do not permit
arbitrary method calls. Adding a check-only watch does not add remediation;
attach a `then:` action only when that action has been reviewed independently.
See [the D-Bus check reference](rules.md#database-protocols).

### Proxmox VE and supporting host daemons

The packaged catalog includes the resident Proxmox VE control-plane daemons,
LXC/LXCFS, the Proxmox firewall variants, HA managers, API/SPICE proxies,
QEMU event handling and ZFS ZED. It also includes commonly adjacent host
daemons discovered on Proxmox nodes: Amazon SSM Agent, Cron, KSM tuning, the
pNFS block mapper, and Prometheus IPMI/NUT exporters.

`pve-container-%n` is an instanced systemd profile. An active unit such as
`pve-container@101.service` materializes `pve-container-101`; inactive container
units are not inferred. Proxmox Perl daemons match the exact resolved Perl
executable and configured user before their narrow process title is considered.
The command line never authorizes signaling by itself. Exporter profiles verify
their local `/metrics` endpoint, while `pvedaemon`, `pveproxy` and `spiceproxy`
use TCP reachability because their HTTP endpoints intentionally require a
protocol-specific request or authentication.

These profiles add monitoring and normal init-backed operator controls only.
They do not ship automatic restart rules or enable `SIGKILL`. Boot setup units,
systemd infrastructure, filesystem mount helpers and hardware state belong to
their existing host watches rather than duplicate catalog services. Debian's
`dm-event` and `smartmontools` unit names are aliases of the existing `dmeventd`
and `smartd` profiles.

## Versioned services

Some applications ship one binary per version and several can be installed at
once (php-fpm, postgres, tomcat, erlang/beam, berkeley db). Instead of one file
per version, write a single **app version template** whose `name:` contains
`%v`, with `${version}` in the discovery path. A service template with the same
token links that app.

```yaml
name: postgres-%v
display_name: "PostgreSQL ${version}"
variables:
  binary: "/usr/lib64/postgresql-${version}/bin/postgres"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }

---
name: postgres-%v
display_name: "PostgreSQL ${version}"
service:
  systemd: ["postgresql-${version}", "postgres-${version}"]
  openrc: ["postgresql-${version}", "postgres-${version}"]
apps: ["postgres-${version}"]
variables:
  data_dir: /var/lib/postgresql/${version}/data
pidfile: "${data_dir}/postmaster.pid"
```

On load, Sermo discovers app versions by globbing the linked app's
`variables.binary` path with `${version}` wildcarded (here
`/usr/lib64/postgresql-*/bin/postgres`) and extracting what filled it. Service
templates in `catalog/services` prefer the active init service as source of
truth: token-bearing `service:` candidates are matched against active
systemd/OpenRC units, and only matching services materialize. Each match becomes a
concrete app or service with `%v` and `${version}` substituted everywhere (name,
display_name, service, app links, ...) — `postgres-14`, `postgres-16`, ... — and
the templates themselves are dropped. If nothing is installed or no matching
service is active, the template yields nothing. The YAML filename does not have
to match `name:`; keep one descriptive file for the template and treat `name:`
as the catalog identifier. `%v` may sit anywhere in the name (`db%vsql` →
`db4.8sql`). Note: `%v` is substituted only in the name; inside the body always
use `${version}` (e.g. in `service` or `apps`).

Prefer application discovery in `catalog/apps` when the installed binary path
identifies the version or instance. A versioned or instanced service that links a
matching app, such as `apps: ["postgres-${version}"]` or
`apps: ["php-fpm${version}"]`, uses that app for runtime binary validation. For
catalog services, put the same tokens in `service:` so the service materializes
from the unit that is actually active on the selected init backend.

`variables.binary` may be a string or a candidate list. Use it when the
versioned path is also the runtime executable that preflight and version checks
should probe. For app and library templates that discover from `versions.from`
and do not declare `variables.binary`, the materialized document binds
`${binary}` to the path that matched; keep `versions.from` for discovery sources
that are not the runtime executable.

When an app or library cannot discover from its runtime executable, use
`versions.from` there and link the generic or versioned app that owns the binary:

```yaml
name: myservice-%i
versions:
  from: "/etc/myservice/${instance}.conf"
variables:
  binary: /usr/sbin/myservice
preflight:
  binary: { type: binary, path: "${binary}" }
```

`versions.from` is discovery-only metadata; it never appears in materialized apps
or services. Matches are de-duplicated by their materialized token tuple.

A discovered version must start with a digit, so siblings of an unbounded
trailing placeholder (a bare `php-fpm` symlink, a `php-fpm.conf`) are not mistaken
for versions. Even so, a placeholder bounded on both sides (e.g.
`/usr/lib64/php${version}/bin/php-fpm`, in the app `variables.binary` path) discovers most
precisely.

### Trailing subcommand suffixes

Some packages ship one binary per subcommand and no plain versioned entry point:
Berkeley DB installs `db5.3_archive`, `db5.3_dump`, `db5.3_stat`, … and no
`db5.3`. A trailing `${version}` captures everything after the name, so each
subcommand would materialize its own app (`Berkeley DB 5.3_archive`,
`Berkeley DB 5.3_dump`, …).

`versions.suffix` names the part of the captured value that is not the version.
It takes one glob or a list of them, anchored at the end of the value; the
longest match is trimmed, and every trimmed value then de-duplicates into a
single instance:

```yaml
name: db%v
display_name: "Berkeley DB ${version}"
versions:
  from: ${bindir}/db${version}
  suffix: "_*"
variables:
  binary:
    - ${bindir}/db${version}_dump
    - ${bindir}/db${version}_stat
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-V"], timeout: 10s }
```

`db5.3_archive`, `db5.3_dump` and `db5.3_stat` all trim to `5.3`, so one
`db5.3` app is registered per installed release. A value the suffix does not
match, or would consume entirely, is kept whole — a bare `db6.2` still
registers as `6.2`. A suffix must begin with a literal separator; a leading `*`
or `?` is rejected because it would swallow the version itself.

Pin `variables.binary` when the family is discovered this way. Discovery from
`versions.from` leaves the declared binary alone, so the candidate list decides
which subcommand preflight probes instead of whichever one globbed first — it
matters when some of them behave differently (`db5.3_tuner` rejects `-V`).

### Integer and instance placeholders

`%v`/`${version}` accepts a digit-leading version (`8.3`, `12.0.2`); use
`%n`/`${n}` when the value is a **plain integer** — it matches only whole
numbers, otherwise working exactly like `%v`:

```yaml
name: python%n
display_name: "Python ${n}"
variables:
  binary: "/usr/bin/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
```

`/usr/bin/python*` then materializes `python2`/`python3`, but not `python3.11` or
`python-config`.

When a simple `%v` or `%n` template also has an unversioned active-slot binary,
Sermo materializes it automatically. If `/usr/bin/python` exists, this registers
`python` in addition to `python2`/`python3`; when it is absent, only the numbered
binaries are registered. The empty token is substituted before `name`,
`display_name` and `description` are trimmed, so `display_name: "Python ${n}"`
becomes `Python` for the active slot. Composite templates (`%i` plus `%v`, a
separator token, etc.) do not infer that entry from `versions.from`; declare
`versions.current_from` when they have a concrete active-slot executable such as
`/usr/bin/java`. That path materializes the unversioned base name before the
first token (`java-%i-%v` -> `java`) and becomes its `${binary}` when the
template does not declare one. `current_from` may also be a list of direct paths:

```yaml
versions:
  current_from: /usr/bin/java
```

Set `versions.unversioned: false` to ignore the marker-less or `current_from`
active slot; a map form can still override fields for the unversioned instance
when a template needs a custom label:

```yaml
name: python%n
display_name: "Python ${n}"
versions:
  unversioned:
    description: "Active Python interpreter"
variables:
  binary: "/usr/bin/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
```

If a template would materialize a `name:` that already exists as an explicit
document in the same catalog category, validation reports a collision. Remove
one definition or adjust the template discovery; Sermo does not silently choose
between an explicit document and a generated one.

Templates may also use `${current}` in `display_name` or `description`. During
materialization it becomes `current` only for the versioned entry whose binary is
the same filesystem entry as the active-slot binary, whether discovered from the
marker-less path or declared with `versions.current_from` (for example
`/usr/bin/php -> /usr/bin/php8.2` or `/usr/bin/java` pointing at the active JVM);
otherwise it becomes empty before metadata is trimmed. This lets
`display_name: "PHP ${version} ${current}"` render as `PHP 8.2 current` for the
active version and `PHP 8.3` for the others without running version commands
during config load. Symlinks are resolved before comparison. App/service
inventory commands may still add the `current` label at inspection time when an
active-slot wrapper reports the same `version_short` as one materialized
version, which keeps wrappers such as Gentoo Java generic without `from_file`
catalog metadata.

Use `%i`/`${instance}` for named init instances discovered from bounded service
metadata. Scope backend-specific discovery to matching service candidates; for
example, an OpenRC-specific profile can expose only `service.openrc:
["openvpn.${instance}"]`, while a systemd template can expose
`service.systemd: ["openvpn-client@${instance}"]`.

### Composite names with a separator (`%s`)

Some services encode **both** a version and an environment/pool in one name, joined
by `-` or `_` — `tomcat-8.5-main`, `tomcat-9-guacamole`, `php-fpm8.4_airbnb`. Use
`%s`/`${sep}` for that joining separator, which matches an empty string, `-` or
`_`. A name may carry several tokens (`tomcat-%v%s%i`); for service templates they
are discovered together from active service units whose `service:` candidates
contain the same markers, and bound everywhere at once. A non-final `%v` is
bounded so it stops at the separator (`8.5`), and the instance may be empty —
when it is, the separator collapses too, so a bare `tomcat@8.5.service`
materializes `tomcat-8.5` with no trailing `-`:

```yaml
name: tomcat-%v%s%i
service:
  openrc: ["tomcat-${version}${sep}${instance}"]
  systemd: ["tomcat@${version}${sep}${instance}"]
```

### Service-owned discovery

A service template in `catalog/services` normally discovers from active init
units. Put every supported service spelling in `service:` and split it by backend
when systemd/OpenRC names differ. The linked app (generic like `openvpn`, or
versioned like `php-fpm${version}`) still supplies `${binary}` for preflight and
process identity. A service never discovers from its own *binary*.

When a generic unit spelling could also name a different daemon, set
`versions.require` to a path containing the same template variables. Sermo
materializes a single-token or composite instance only if at least one required
path exists; this keeps overlapping unit names out of the wrong catalog profile.

When discovery comes from init service metadata, let the linked app own runtime
binary validation when it is versioned. For example, PHP-FPM links
`php-fpm${version}`; that app already validates `/usr/sbin/php-fpm${version}` or
`/usr/bin/php-fpm${version}`, so the service does not repeat the same candidates
in `versions.require`:

```yaml
service:
  systemd:
    - "php-fpm@${version}${sep}${instance}"
    - "php-fpm@php${version}${sep}${instance}"
    - "php-fpm-php${version}${sep}${instance}"
    - "php${version}${sep}${instance}-fpm"
    - "php-fpm${version}"
  openrc:
    - "php-fpm-php${version}${sep}${instance}"
    - "php${version}${sep}${instance}"
    - "php-fpm${version}${sep}${instance}"
    - "php-fpm${version}"
apps: ["php-fpm${version}"]
pidfile:
  - "/run/php-fpm/php-fpm-${version}${sep}${instance}.pid"
  - "/run/php-fpm/php-fpm-php${version}${sep}${instance}.pid"
  - "/run/php-fpm-php${version}${sep}${instance}.pid"
watches:
  pidfile:
    check:
      type: pidfile
      optional: true
      path:
        - "/run/php-fpm/php-fpm-${version}${sep}${instance}.pid"
        - "/run/php-fpm/php-fpm-php${version}${sep}${instance}.pid"
        - "/run/php-fpm-php${version}${sep}${instance}.pid"
      requires: [service]
```

Put the exact systemd instance first in `service.systemd`, e.g.
`php-fpm@${version}${sep}${instance}` for `php-fpm@8.2.service`. Avoid a generic
`php-fpm` systemd fallback in versioned templates: it can make several
discovered PHP-FPM versions operate on the same unit. The pidfile check is
optional because some systemd units publish `MainPID` even when the declared
`PIDFile=` is not written.

### Optional components (`enable_if`)

An entry under `processes`, `watches` or `preflight` may carry an
`enable_if` guard that keeps it only when a key in a distro config file satisfies
a predicate; otherwise the entry is dropped during service resolution. This
models components that are optional per host — e.g. a Samba profile monitors
`winbindd` only when `/etc/conf.d/samba`'s `daemon_list` names it. Do not link
such a component under `apps`, because linked apps are mandatory preflight
dependencies for service operations:

```yaml
processes:
  winbindd:
    exe: ${winbindd_binary}
    enable_if:
      file: /etc/conf.d/samba
      key: daemon_list
      contains: winbindd          # or: equals: <value> | matches: <regex>
watches:
  winbindd:
    enable_if:
      file: /etc/conf.d/samba
      key: daemon_list
      contains: winbindd
    check:
      type: process
      exe: ${winbindd_binary}
      state: running
```

A missing file or absent key prunes the entry (fail-safe). The guard is stripped
from surviving entries. `config validate` still checks disabled entries before
they are pruned, so typos in optional process/check definitions are reported.
`enable_if` is intentionally not supported under `rules`, `policy`, `guards` or
other safety-affecting sections.

Instead of a config-file predicate, `enable_if` may name the init backend the
entry belongs to: `enable_if: { init: openrc }` (or `systemd`) keeps the entry
only when that backend is active, and excludes the `file`/`key` predicate form.
Use it for components that exist under one init system only. The packaged
`salt-minion` profile gates its Gentoo `supervise-daemon` selector this way:
under OpenRC the supervisor is the one strict identity Sermo may signal, while
on Gentoo running systemd no supervisor exists, and an exact selector that can
never match a live process would make the restart identity guard block every
operation on the unit:

```yaml
processes:
  supervisor:
    exe: /usr/bin/supervise-daemon
    user: root
    enable_if: { init: openrc }
```

The config reader accepts `key=value` assignments, YAML `key: value` block
mappings and bare `key` feature flags.
Use `equals: ""` to match a bare flag. The packaged `dnsmasq` profile uses this
to add its DHCP check only when `/etc/dnsmasq.conf` has `dhcp-range=...`, and its
TFTP check only when that file has `enable-tftp`. If those directives live in a
file under `/etc/dnsmasq.d`, override the corresponding
`watches.dhcp.enable_if.file` or `watches.tftp.enable_if.file` in the configured
service. Override `dhcp_host`/`dhcp_port` or
`tftp_host`/`tftp_port`/`tftp_query` when the local endpoint differs.

The packaged `cloudflared` profile is the YAML case: `cloudflared tunnel ingress
validate` only validates locally declared ingress rules, so it fails on a
remotely-managed tunnel whose `config.yml` carries just a `token:` — and a failed
preflight blocks every operation, restart included. Its config preflight is
therefore gated on `ingress` being present in the file.

### Variables read from a config file (`from_file`)

A variable may take its value from a config file instead of a literal, useful when
a port or path is defined in the service's own config. `directive:` reads the token
after a `key value` line (OpenVPN/sshd style); `pattern:` reads capture group 1 of
a regex; `default:` applies when the file or key is absent:

```yaml
variables:
  config: "/etc/openvpn/${instance}.conf"
  port:
    from_file: "${config}"
    directive: port              # "port 1194" -> 1194
    default: 1194               # required fallback when file/key is absent
  # tomcat: pattern: '<Connector[^>]*?\bport="(\d+)"'
```

It is evaluated during resolution (so it can reference other variables such as
`${config}`) and re-evaluated on every config reload. `pattern` may also
reference variables such as `${instance}`; those values are escaped as regex
literals before the file is read. The variable spec must define `from_file`,
`default`, and exactly one of `directive` or `pattern`. `pattern` must compile
and include a capture group. A missing file or unmatched key uses `default`;
malformed specs or unknown variables in `from_file` / `pattern` are validation
errors.

### Listing installed applications

`sermoctl apps` reports the applications described by catalog apps: which are
installed (their binary is present and executable), whether their `health`
command succeeds when configured, and the version their `version` command
reports. The VERSION column shows the short version by default; add `--long` to
show the full raw string.

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
its own row (e.g. `PHP-FPM 8.3`, `PHP-FPM 7.4`). For `sermoctl services`, version
commands are best-effort inventory data: a failed distro-specific version probe
leaves the version unknown instead of marking the installed service as an error.
`--json` is unaffected by `--long` — it always emits both, with the structured
`name`, `display_name`, `binary`, `version`, `version_short`,
`version_source`, `installed`, `ok` and `status`.

When an app declares `health`, Sermo uses it as the preferred health probe for
`sermoctl apps`/`libs`/`services` and the WebUI application list. Only the exit
code is evaluated (`expect_exit`, default `0`, or a list such as `[0, 1]`);
stdout/stderr matchers and the printed output are ignored for health. The
`version` command is only used as a fallback health probe when no `health`
command exists; when `health` exists, `version` reports display data and a
version failure does not override health.
Do not mark an app `version` probe optional unless the app also has a `health`
probe; otherwise Sermo can only prove that the binary exists, not that it can run.
For catalog apps that are separate binaries from the same package, `version_from`
can point at another catalog app whose version probe supplies the displayed
version. The app still checks its own `variables.binary` and health;
`version_from` only
sets `version`/`version_short` when the app has no local version result.

Catalog apps can use `version_match` when a binary name is shared by compatible
implementations. It runs against the combined stdout/stderr of the local
`version` command and supports `contains`, `excludes` and `regex`. If it fails,
the app is treated as not installed rather than as an installed app with a bad
version. For example, MariaDB accepts `mysqld` only when the output contains
`MariaDB`, while MySQL excludes that token so MariaDB's compatibility `mysqld`
does not appear as MySQL.

`version` is the raw first line the version command prints (e.g. `nginx version:
nginx/1.30.2`); `version_short` reduces it to just the numeric version and at
most the patchlevel (`1.30.2`), taking the first `major.minor[.patch]` token and
dropping any further build components and suffixes (so `2.8.4.1-0+g…` becomes
`2.8.4` and `4.2.8p18` becomes `4.2.8`). If there is no dotted token, a guarded
integer-only `version N` token is accepted for projects such as polkit and
date-coded numad releases. It is empty when the version line carries no
recognizable number.

A catalog service may instead declare a dedicated `version_short` command (under
`preflight` or `commands`, alongside `version`) that prints the bare version
itself, sidestepping the regex when a tool can report it directly. Its first
non-empty output line is then used verbatim. The packaged interpreter apps do
this with their resolved binary — e.g. PHP runs `php -r 'echo PHP_VERSION;'`,
Python runs `python -c 'import platform;print(platform.python_version())'`, Node
`node -p process.versions.node` — so their short version never depends on
parsing. When no such command is configured (or it errors or prints nothing),
`version_short` falls back to parsing the `version` line as above.

```yaml
preflight:
  health:        { type: command, command: ["${binary}","-h"], timeout: 10s }
  version:       { type: command, command: ["${binary}","-v"], timeout: 10s }
  version_short: { type: command, command: ["${binary}","-r","echo PHP_VERSION;"], timeout: 10s }
```

A service template may `uses` a base service to inherit its checks, processes and
rules, while a linked app supplies the instance- or version-specific binary. The
packaged `nebula-%i` service builds on the base `nebula` service and links the
`nebula-${instance}` app:

```yaml
name: nebula-%i
uses: nebula
display_name: "Nebula ${instance}"
apps: ["nebula-${instance}"]
```

A configured service then targets a concrete instance, e.g. `uses: nebula-nebula0`.
Active systemd/OpenRC units normally materialize catalog instances for discovery.
An explicitly configured `uses:` instance also materializes when its unit is
stopped or failed, so `sermod` can report that service state instead of rejecting
the whole configuration.

Nebula Mesh's accompanying components are cataloged separately as
`nebula-agent` and `nebula-mgmt`. Both resolve their native systemd or OpenRC
unit and exact process identity. The management-server profile reads `listen:`
from `/etc/nebula-mgmt/server.yml` (falling back to `127.0.0.1:8080`) and checks
its unauthenticated `/readyz` endpoint. These profiles are monitor-only: they
do not add automatic restart rules for certificate or control-plane services.

## Service unit

The service's identity is its `name`; `service` declares the init-unit
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
it (e.g. sysv-generated units). A **per-init list** first requires a backend
match; if the probe cannot surface one, Sermo logs or prints a warning and falls
back to the configured seed unit so `sermod`, the web UI and `sermoctl` behave
the same on historic init-service setups. An init system with no entry means the
service is *not available* there: `sermod` skips it and reports the skip as an
informational notice (not a warning), because the map itself declares the
backend unsupported. Services using `control:` (libvirt/docker) do not use the
init-unit fallback.

An enabled instance can override the unit with a scalar (e.g.
`service: redis-cache`) to run as its own unit, or omit `service` entirely to
inherit the catalog service's candidates.

## Cloning

A service may `clone` another service to make a second instance:

```yaml
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
rules, different listen port, pidfile and config file — let each instance `uses`
the catalog service and override only its unique variables.

The catalog service parametrizes everything that varies with `${...}` placeholders and
threads each one into the commands and checks that consume it. In particular the
config-file path should be a variable wired into every command that reads it, so
two instances never pick up each other's configuration:

```yaml
name: dbserver
variables:
  port:    3306
  pidfile: /run/dbserver/main.pid
  config:  /etc/dbserver/main.cnf
pidfile: "${pidfile}"
watches:
  tcp:
    check: { type: tcp, port: "${port}" }
  config:
    check: { type: command, command: ["dbserverd", "--defaults-file=${config}", "--help"] }
```

Each instance overrides the three variables and gives itself an init unit (a
systemd template instance or a distinct unit name) with a scalar `service`:

```yaml
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
*catalog service* and only overrides variables. Reach for `clone` only when one instance
should copy *another concrete service* almost verbatim. See [`docs/sermo-all.yml`](sermo-all.yml)
for a complete worked configuration.

## Disabling and deleting inherited entries

```yaml
watches:
  http:
    enabled: false   # keep but disable
  ping:
    delete: true     # remove the inherited entry
```

## Monitoring flag

The top-level `monitor` flag sets a service's monitoring behavior when the
daemon starts:

```yaml
name: web
uses: nginx
monitor: enabled    # enabled (default) | disabled | previous
```

- **`enabled`** (the default when the flag is absent): always monitor on startup.
- **`disabled`**: never monitor — the worker exists but every cycle is skipped.
- **`previous`**: restore the runtime state the service had before the daemon
  last stopped. On the very first run (no recorded state) it defaults to
  monitored.

Top-level `enabled: false` disables the service entirely; no worker is built.
With `monitor`, the worker exists and only check/rule execution changes.

The live state is toggled at runtime with `sermoctl monitor <svc>` /
`sermoctl unmonitor <svc>` and persisted in the state database under
`paths.state` (see [configuration](configuration.md)). Because that database
survives reboots, a `previous` service comes back up in whatever state an
operator last left it.

Host watch documents use the same top-level
`monitor: enabled | disabled | previous` values; see
[configuration](configuration.md#host-watches).

A service may also carry its own `watches:` block — per-service watches that can
fire a hook/notification or compact `then.action`, and can use the service-scoped
`service`/`metric`/`process_count` check types. See
[Service watches](configuration.md#service-watches-scoped-to-a-service).
Host-global checks such as `terminal_sessions` also work there: place a
`tmux` or `screen` check under the SSH service to show its
configured users' terminal sessions in the Web UI Sessions panel. It does
not create a systemd/OpenRC service. Administrators may close one exact
displayed session; Sermo freshly revalidates its multiplexer-owned generation
and uses the configured client as that user, without signalling a guessed PID.
The remote deployment generator creates these checks for active multiplexer
namespaces that its read-only inventory can attribute to a local user; named
`tmux` sockets are kept separate, while inactive or unattributable namespaces
are omitted.

The packaged `fcron` profile treats more than one process in the service tree as
an active scheduled job. While that condition is active, its guard blocks
`restart` and `stop`, allowing the job to finish before an operator retries the
operation.

## Blocking operations while clients are connected

Restarting a database, cache or FTP server with active clients is a site policy,
not a catalog default. Add one of the opt-in examples to the concrete service
you enable: [MySQL](../examples/services/mysql-active-connections-guard.yml),
[PostgreSQL](../examples/services/postgres-active-connections-guard.yml),
[Redis](../examples/services/redis-active-connections-guard.yml), or
[ProFTPD](../examples/services/proftpd-active-connections-guard.yml). The
database examples count application sessions with read-only SQL; Redis uses its
native `connected_clients` metric; FTP uses `tcp_connections`, which counts
control-channel TCP sockets rather than authenticated users. See
[connection guards](rules.md#connection-guards) for the safety behavior.

## PostgreSQL replication watches

The `postgres` catalog service ships five replication sensors built on the `sql`
check: `alert-if-replication-slot-backlog`, `alert-if-logical-slot-unconfirmed`,
`alert-if-replication-slot-inactive`, `alert-if-replication-replay-lag` and
`alert-if-standby-replay-delay`. They are tuned by these variables:

| variable | default | meaning |
|---|---|---|
| `monitor_user` | `postgres` | role the replication queries connect as |
| `database` | `postgres` | database the queries connect to |
| `slot_backlog_mib` | `1024` | WAL retained by the most lagging slot, in MiB |
| `logical_unconfirmed_mib` | `512` | data a logical consumer has not confirmed, in MiB |
| `replay_lag_mib` | `256` | sent but not replayed by the most lagging replica, in MiB |
| `standby_delay_seconds` | `300` | how far behind a standby may fall, in seconds |

The thresholds are plain numbers because a `sql` check compares numerically and
does not take size suffixes, so the queries return MiB and seconds directly.
Pick them well above the idle baseline: a healthy primary already retains around
16 MiB (one WAL segment) because a logical slot's `restart_lsn` trails by design.

**`monitor_user` must hold `pg_monitor`** (the default `postgres` superuser
does). A role without `pg_monitor` or `pg_read_all_stats` still sees rows in
`pg_stat_replication`, but with `sent_lsn`/`replay_lsn` as NULL for other
backends — the aggregate then collapses to `0` and the lag watches never fire,
silently. `pg_replication_slots` is readable by any role, so the slot watches
are unaffected.

These watches only make sense where replication actually happens. Enable only
the watches that match the PostgreSQL role and replication features on that
host.

## Exim hints database maintenance

The `exim` catalog service exposes separate confirmed operator buttons for
`exim_tidydb` on the `callout` and `retry` hints databases. Its matching
service watches count the records, publish `records` metric series and can run
the same bounded cleanup command when the configured limit is exceeded.

Exim 4.99 and newer name the SQLite record table `tblblob`; older supported
releases use `tbl`. The catalog defaults `callout_db_table` and
`retry_db_table` to `tblblob`. Fleet installation discovers each database's
schema read-only and overrides either variable with `tbl` when required. A
non-SQLite database, absent file or unsupported schema disables only the
affected record watch; `exim_tidydb` buttons remain available because that
utility supports Exim's native hints backend independently of the graph query.

## Auxiliary commands

`commands` declares named auxiliary commands. Sermo never runs them as generic
checks, but the **reserved names** are consumed by features:

- **`health`** — run by the `sermoctl apps`/`libs`/`services` listings and the
  WebUI application list to decide whether an installed application is healthy.
  It uses the same `preflight.<name>` then `commands.<name>` lookup as
  `version`, but only checks the exit code. When present, it takes precedence
  over `version` for app health; `version` remains display-only.
- **`version`** (and `version_short`) — run by the `sermoctl apps`/`libs`/
  `services` listings to report a service's version, and **each cycle** by the
  `version.on_change` monitor (see [Service health conditions](rules.md#service-health-conditions-version--state--config)).
  That monitor compares the numeric `version_short`, and an optional
  `version.on_change.level` (`major`/`minor`/`patch`, default `patch`) selects at
  which `a.b.c` granularity a change should alert.
  The monitor inherits the service's `dry_run` flag, so non-console notification
  delivery is suppressed while the service is in dry-run mode.
  When both exist, `preflight.version` takes precedence over `commands.version`.
  They also declare `version` and `version_short` variables with empty defaults
  for expansion; linked apps expose them to services as `${app_version}` and
  `${app_version_short}`. Other command-derived values can be declared with
  `export:`, whose default source is trimmed stdout and whose default value is
  empty.

Any other entry is informational only. A run can assert its outcome, the same
way a watch hook or `command` check does: `expect_exit` (default 0, or a list
such as `[0, 1]`) and optional `expect_stdout`/`expect_stderr` matchers — a
substring or an `{op, value}` comparison (`== != > >= < <= contains =~`).
Reserved commands may also set `user` (username or numeric UID) to execute the
argv as that OS user when Sermo has permission to switch users.

```yaml
commands:
  version:
    user: www-data
    command: ["apachectl", "-v"]
    timeout: 5s
    expect_exit: 0                                   # optional, default 0
    expect_stdout: { op: "=~", value: "Apache/2" }   # optional: match the output
```
