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
checks:
  http:
    url: "http://${host}:${port}${health_path}"
```

The packaged catalog (`catalog/`) covers common service families such as web
servers, databases, container runtimes, NFS/libvirt helpers and hardware/system
services. They define variables, preflight, processes, checks, stop_policy,
remediation policy and rules so a configured service usually only sets a few
overrides. High-impact catalog services such as databases, caches and queues may carry stricter
local `policy` settings than the global defaults, with longer cooldowns,
rate limits and backoff to avoid restart loops.

## Categories

Catalog documents are grouped by the subdirectory they live in under a catalog root:

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
directly in a catalog root are rejected. Use one YAML file per catalog document:
one service, app, lib or pattern in each file.
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

A configured service (or catalog service definition) opts in with `restart_on_change`:

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
upgrade rather than every cycle. Referenced names must be `library` services.

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
go test ./internal/config -run 'TestRealCatalog(AllDaemonsValidate|ReloadDaemonsResolve)$' -count=1
```

The reload that `reload:` produces is what the **`reload` action**,
`reload_on_change`, the `sermoctl reload <svc>` command and the web UI reload
button all run. It is a service-control concept: it applies to services, not to
host watches, which observe host metrics and fire hooks rather than reload a
unit.

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
app's own `variables.binary` path, health probe and version command. When a
service links
several apps, each one's checks stay distinct — e.g. `backrest`'s
`apps: [backrest, restic]`
yields `backrest-binary`, `backrest-health`, `backrest-version`,
`restic-binary`, `restic-health`, `restic-version`, so a missing or unhealthy
`restic` raises its own alert separate from `backrest`:

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
service's preflight, which **blocks start/restart/reload/resume** (a preflight-failed
operation never executes the action) — you do not start, restart, reload or
resume a service whose runtime is absent.
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
- **`category`** groups and filters Services and Installed applications in the
  WebUI. When absent or blank, services use `service` and apps use `app`.
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

checks:
  vm:
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

### `control: docker` — Docker containers

A service can be controlled as one Docker container instead of a systemd/OpenRC
unit:

```yaml
name: web-container
control:
  type: docker
  container: web
  socket: /run/docker.sock

checks:
  docker:
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

### `also_service` — auxiliary init units

A service can name **auxiliary init units of its own** (a `.socket`, `.timer`,
companion unit) that are started/stopped/restarted **together with the primary**,
in the same operation. It mirrors the `service:` shape (per-init lists, resolved
for the active backend):

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
`also_service` is rejected.

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
  when the service is `active`, has a declared `reload:` block or reload
  remediation rule, and that reload path is supported; **resume** is enabled only
  while it is `paused`.

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
  executable serves several instances. The cmdline is spoofable, so `cmd` only
  narrows discovery; residual signaling is still authorized only by
  `stop_policy.kill_only_if` (`exe_any` plus `users`).
- `user` / `group` — the process real UID / GID owner.

These feed monitoring **and** the residual reaper, so a richer selector lets a
stop catch and kill more leftovers (an unkillable residual stays
`orphan_processes`). The `process` *check* still matches by `exe`/`user` only.

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
stopped service is skipped, not alarmed. A check already named `pidfile` is
respected, so a catalog service that needs a custom check can still spell it out. Public
`processes:` entries stay limited to `exe`/`cmd` selectors with optional
`user`/`group`; do not put `pidfile` under `processes:`. The shorthand path can
reference variables (e.g. `pidfile: "${pidfile}"`) and accepts a scalar path, a
candidate list, or `{path: ..., optional: true}`. Candidate lists are tried in
order and pass on the first live pidfile; if none exists, the backend PID
fallback can still satisfy the gated health check. `optional: true` keeps a
missing pidfile as a warning instead of making the service unhealthy.

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

Use `%i`/`${instance}` for named init instances discovered from bounded service
metadata. Scope backend-specific discovery to matching service candidates; for
example, a legacy OpenRC profile can expose only `service.openrc:
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
checks:
  pidfile:
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

An entry under `processes`, `checks`, `preflight` or `postflight` may carry an
`enable_if` guard that keeps it only when a key in a distro config file satisfies
a predicate; otherwise the entry is dropped during service resolution. This
models components that are optional per host — e.g. a Samba profile that links a
`winbindd` app can monitor `winbindd` only when `/etc/conf.d/samba`'s
`daemon_list` names it:

```yaml
processes:
  winbindd:
    exe: ${winbindd_binary}
    enable_if:
      file: /etc/conf.d/samba
      key: daemon_list
      contains: winbindd          # or: equals: <value> | matches: <regex>
checks:
  winbindd:
    type: process
    exe: ${winbindd_binary}
    state: running
    enable_if:
      file: /etc/conf.d/samba
      key: daemon_list
      contains: winbindd
```

A missing file or absent key prunes the entry (fail-safe). The guard is stripped
from surviving entries. `config validate` still checks disabled entries before
they are pruned, so typos in optional process/check definitions are reported.
`enable_if` is intentionally not supported under `rules`, `policy`, `guards` or
other safety-affecting sections.

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

A configured service then targets a concrete instance, e.g. `uses: nebula-vpn0`.

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
service is *not available* there. Services using `control:` (libvirt/docker) do
not use the init-unit fallback.

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
checks:
  tcp:    { type: tcp, port: "${port}" }
  config: { type: command, command: ["dbserverd", "--defaults-file=${config}", "--help"] }
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

A service may also carry its own `watches:` block — per-service watches that fire
a hook/notification scoped to the service (and can use the service-scoped
`service`/`process` check types). See
[Service watches](configuration.md#service-watches-scoped-to-a-service).

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
