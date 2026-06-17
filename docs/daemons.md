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

The directory sets the catalog category (`service` / `app` / `library` /
`patterns`); files placed directly in a catalog root default to `service`.
`sermoctl services`, `sermoctl apps` and `sermoctl libs` list each category,
showing which are installed, the version their version command reports, and
whether they resolve without error (add `all` to include the not-installed).
`sermoctl patterns` lists the pattern sets and their rule counts (see the
`analyze:` block in [rules.md](rules.md)).

Catalog daemons can keep compatibility names with `catalog_aliases`. Aliases are
accepted by `uses:` but are not listed by `sermoctl wizard service` as separate
catalog choices, so renamed daemons such as `avahi-daemon` → `avahi` remain
loadable without creating duplicate wizard entries.

## Library daemons

A library daemon describes a shared library so services can restart when it is
upgraded. It only needs identity plus the file to watch:

```yaml
kind: daemon
name: glibc
display_name: "GNU C Library"
description: "Standard C library (libc)"
binary: "/lib64/libc.so.6"          # the file watched for changes (and its version)
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

### Native reload (`reload:`) — when the init can't, Sermo can

Some daemons reload in place (e.g. `sshd`, `snmpd`, `proftpd`, `prometheus`,
`loki` re-read their config on **`SIGHUP`**) but their **systemd** unit defines
**no `ExecReload`**, so `systemctl reload <unit>` fails — even though the daemon
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
  native reload. So the *same* daemon definition reloads correctly on a host
  whose unit exposes reload **and** on one whose unit doesn't.
- **`when: always`** always runs the native reload and never the init's — the
  signal/command equivalent of `commands.reload` (which is still accepted and
  behaves as `when: always`). **Migrating `commands.reload` → `reload.command`:**
  a bare `reload: { command: [...] }` defaults to `when: auto` (it prefers the
  init reload where one exists), so set `when: always` to keep the old
  always-run-the-command behavior.
- **Signal target.** The signal goes to systemd's `MainPID`, or — on OpenRC, or
  any unit with no MainPID — to the PID in the service's `pidfile:`. A signal
  reload with neither available fails with a clear error; give the daemon a
  `pidfile:` so the target can be resolved.

The reload that `reload:` produces is what the **`reload` action**,
`reload_on_change`, the `sermoctl reload <svc>` command and the web UI reload
button all run. It is a service-control concept: it applies to service daemons
(`kind: service`/`daemon`), not to host `watches:`, which observe host metrics
and fire hooks rather than reload a unit.

A signal reload needs a process to signal: systemd's `MainPID` (available while
the unit is active) or, on OpenRC and any backend without a MainPID, the PID in
the service's `pidfile:`. If neither exists the reload fails with a clear error
rather than silently doing nothing — declare a `pidfile:` for a daemon that must
reload by signal on OpenRC. Daemons that write no pidfile (e.g. Prometheus, Loki)
therefore reload by signal only on systemd; on OpenRC they rely on the init
script's own `reload` (`when: auto`).

## App dependencies (`apps`)

A service often runs on top of one or more **apps** — the runtimes/tools in
`catalog/apps` (java, openssl, perl, …). An app owns the **binary**, **health**
and **version** checks for that tool; it is the single source of truth, shared by
every service that uses it. A service (or daemon definition) links the apps it
needs with `apps:` — a list, since a service may depend on several:

```yaml
# catalog/services/tomcat-%v.yml — Tomcat runs on the JVM
apps: [java]
```

On resolution each linked app's preflight checks are injected into the service's
preflight under keys namespaced by the app name (`<app>-<check>`), carrying the
app's own binary path, health probe and version command. When a service links
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
app keeps ownership of the actual path. Local `variables:` entries on the daemon
or service override either form; when several apps are linked, use the prefixed
names.

Because they run in **preflight**, a missing or wrong-version runtime fails the
service's preflight, which **blocks start/restart/reload/resume** (a preflight-failed
operation never executes the action) — you do not start, restart or reload a
service whose runtime is absent.
The link is many-to-many: a service lists several apps, and one app is shared by
every service that lists it. The service keeps its own `binary`, `version` and
`config` checks (the **config** test is always service-specific, never moved to
an app). Referenced names must be `app` daemons.

## Metadata fields

A daemon or service may carry optional human-facing metadata:

```yaml
kind: daemon
name: mariadb
display_name: "MariaDB"      # pretty label; falls back to name when absent
description: "..."           # free-text note; shown verbatim, nothing when absent
category: "database"         # optional WebUI grouping/filter label
```

These fields are optional and behave differently when missing:

- **`display_name`** is the label used wherever Sermo shows the application to a
  human (e.g. `daemon list`, `service list`). When it is absent or blank, Sermo
  falls back to `name`. Set it only when it adds something over `name` — a proper
  brand (`MariaDB`, `PostgreSQL`, `OpenSSH`) or a version (`PHP-FPM 8.3`). If the
  display name would just repeat `name`, leave it out and let the fallback apply.
- **`description`** is an optional free-text note. It has **no fallback**: when it
  is absent, nothing is shown for it — Sermo never substitutes `name`. Use it for
  a real sentence, not a restatement of the name.
- **`category`** groups and filters Services and Installed applications in the
  WebUI. When absent or blank, services use `service` and apps use `app`.

All metadata fields must be strings if present; validation rejects non-string
values.

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
binary: "/usr/bin/qemu-system-${arch}"               # → /usr/bin/qemu-system-x86_64
```

An explicit `variables` entry of the same name always takes precedence over a
built-in. `${arch}`/`${os}` are baked **on load** (everywhere — variable values
and app discovery paths included); the rest resolve per service, and
the runtime ones (`${date}`/`${event}`/`${action}`) only in rule `message:`
strings. The `SERMO_ARCH` / `SERMO_OS` / `SERMO_HOST` / `SERMO_HOSTNAME` /
`SERMO_INIT` / `SERMO_USER` environment variables override the matching built-in
(handy for testing or building config off-host).

| Variable          | Value                                          | Resolved        |
|-------------------|------------------------------------------------|-----------------|
| `${name}`         | the resolved service/daemon name              | resolution      |
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

¹ `${host}` only applies when the daemon does not define a `host` variable (a
bind address like `127.0.0.1`); an explicit `host` always wins.

⁵ `${hostname}` is the **short** hostname — the first label before the first dot
(`radon` on `radon.srvdr.com`) — distinct from `${host}` (which keeps the full
detected hostname / bind-address fallback). Use it for systemd instance units
keyed by host identity, e.g. `service: "ceph-mon@${hostname}"` → `ceph-mon@radon`.
For numeric multi-instance daemons (e.g. one OSD per device) use a `%n` daemon
template linked to a matching `%n` app template. The app owns discovery, for
example `versions: { from: "/var/lib/ceph/osd/ceph-${n}" }`; the daemon links
`apps: ["ceph-osd${n}"]` and materializes `ceph-osd0…N` with `service:
"ceph-osd@${n}"`. An explicit `hostname` variable (or `SERMO_HOSTNAME`) wins.

⁴ `${user}` and `${pidfile}` are fallbacks: a daemon's own `user` (a service
account such as `www-data`) or `pidfile` variable always wins. They pair with the
pidfile selector — e.g. `processes.main: { type: pidfile, path: "${pidfile}" }` —
and the `command_match` user — `user: "${user}"`.

Runtime paths in Sermo config use the canonical `/run` spelling. Do not write
new `/var/run` pidfiles or sockets in catalog daemons, generated services or
examples; `/var/run` is the legacy compatibility alias for `/run`, and detected
paths should be normalized to `/run/...` before they are committed to config.
Before adding a new runtime path, check whether it or a parent directory is a
symlink (`readlink -f <path>` or `namei -l <path>`), then record the canonical
target path rather than the alias.

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

For oneshot loaders that do not keep a resident process (for example firewall
loaders), set `processes: {}` explicitly. That prevents Sermo from deriving a
process selector from init metadata and keeps the WebUI from showing CPU/memory
process totals for a service that cannot have them.

### `control: libvirt` — QEMU/libvirt virtual machines

A service can be controlled as a libvirt/QEMU virtual machine instead of a
systemd/OpenRC unit:

```yaml
kind: service
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
    type: command_match
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
`failed`. The CLI and web UI show a VM paused by libvirt as `paused`, distinct
from Sermo's monitor pause (`unmonitor`).

Process discovery is intentionally explicit in this first VM integration. If you
want process metrics or residual-process reporting for the QEMU process, add a
restrictive `processes.command_match` selector as above: exact `exe` and `user`
plus a `cmd` regex that narrows the shared QEMU binary to the intended domain or
UUID. The cmdline selector narrows discovery; residual signaling is still
authorized only by `stop_policy.kill_only_if`.

`sermoctl wizard vm` can generate this `kind: service` shape from domains
detected through the local libvirt socket. It probes both
`/run/libvirt/libvirt-sock` and `/run/libvirt/virtqemud-sock` and writes the
socket it actually used into the generated service and check.

### `control: docker` — Docker containers

A service can be controlled as one Docker container instead of a systemd/OpenRC
unit:

```yaml
kind: service
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
The CLI and web UI show a Docker-paused container as `paused`, distinct from
Sermo's monitor pause (`unmonitor`).

For process metrics and residual-process reporting, Sermo reads the container's
`State.Pid` from Docker inspect and discovers that process tree. You normally do
not need a `processes:` selector for a controlled container. Residual signaling
is still authorized only by `stop_policy.kill_only_if`.

`sermoctl wizard docker` can generate this `kind: service` shape from containers
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
  configuration (`SIGHUP`). In the web UI the per-service **reload** button is
  enabled only while the service is `active`, and **resume** only while it is
  `paused`.

`also_apply` (other services) and `also_service` (this service's init units) are
complementary; a service may use both.

### `command_match` by cmdline / group

A `command_match` selector matches a process by the **AND** of the fields you
set; at least one of `exe`/`cmd` is required:

```yaml
processes:
  unifi: { type: command_match, cmd: "java .*unifi", user: unifi, group: unifi }
  mongo: { type: command_match, exe: "${mongod_binary}", user: unifi }
```

- `exe` — exact resolved `/proc/<pid>/exe` (fail-safe; never cmdline).
- `cmd` — a Go RE2 regex matched against the process **cmdline** (argv joined).
  Use it for shared binaries (`java .*unifi`, `openvpn .*tun1\.conf`) the way the
  legacy per-service kill lists did. The cmdline is spoofable, so `cmd` only
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
  it means the daemon crashed or left junk. Residual *processes* keep their
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

### `pidfile:` shorthand (selector + health check)

A daemon can declare a top-level `pidfile: <path>` to wire **both** uses of a
pidfile from one line:

```yaml
pidfile: /run/named/named.pid
```

When a daemon legitimately uses different pidfile names across distributions,
declare candidates in preference order:

```yaml
pidfile:
  - /run/mysqld/mariadb.pid
  - /run/mysqld/mysqld.pid
```

Use `/run` here, not `/var/run`. If a distro init script or service manager
reports the legacy `/var/run/...` spelling, write the equivalent `/run/...` path
in the daemon definition. Before committing a new pidfile or socket path, resolve
it with `readlink -f` or inspect it with `namei -l`; if any component is a
symlink, use the resolved canonical target.

On resolution this desugars into (a) a `processes` pidfile selector — so the
parent process **and its descendants** are discovered and monitored — and (b) a
`pidfile` health check gated by `requires: [service]`. Because of the gate, a
missing or stale pidfile is reported as an **error only while the service is
active** (it means the daemon died or lost its pidfile without the service
manager noticing); a legitimately stopped service is skipped, not alarmed. An
existing pidfile selector or a check already named `pidfile` is respected, so a
daemon that needs a custom check can still spell it out. The shorthand path can
reference variables (e.g. `pidfile: "${pidfile}"`). Candidate lists are tried in
order and pass on the first live pidfile; if none exists, the backend PID
fallback can still satisfy the gated health check.

## Versioned daemons

Some applications ship one binary per version and several can be installed at
once (php-fpm, postgres, tomcat, erlang/beam, berkeley db). Instead of one file per
version, write a single **app version template** whose name (and filename)
contains `%v`, with `${version}` in the discovery path. A daemon template with
the same token links that app.

```yaml
kind: app
name: postgres-%v
display_name: "PostgreSQL ${version}"
binary: "/usr/lib64/postgresql-${version}/bin/postgres"
preflight:
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }

---
kind: daemon
name: postgres-%v
display_name: "PostgreSQL ${version}"
service: postgres
apps: ["postgres-${version}"]
```

On load, Sermo discovers installed versions by globbing the linked app's
`binary` path with `${version}` wildcarded (here
`/usr/lib64/postgresql-*/bin/postgres`) and extracting what filled it. A
candidate list is checked as a list, so distro-specific locations can stay in
one app template. Each match becomes a concrete app and concrete daemon with
`%v` and `${version}` substituted everywhere (name, display_name, service, app
links, ...) — `postgres-14`, `postgres-16`, ... — and the templates themselves
are dropped. If nothing is installed the template yields nothing. The filename
mirrors the name (`postgres-%v.yml`); only that one file is needed. `%v` may sit
anywhere in the name (`db%vsql` → `db4.8sql`). Note: `%v` is substituted only in
the name; inside the body always use `${version}` (e.g. in `service` or `apps`).

Keep application discovery in `catalog/apps`. A versioned or instanced daemon
that links a matching app, such as `apps: ["postgres-${version}"]`,
`apps: ["php-fpm${version}"]` or `apps: ["openvpn-${instance}"]`, must not
declare its own `versions:` block. If discovery cannot come from a versioned
binary path, put `versions.from` on the app template.

For example, an init instance template discovers instances from init files in the
app, then the daemon links the materialized app:

```yaml
kind: app
name: openvpn-%i
versions:
  from: "/etc/init.d/openvpn.${instance}"
binary: /usr/bin/openvpn

---
kind: daemon
name: openvpn-%i
apps: ["openvpn-${instance}"]
service: "openvpn.${instance}"
```

`versions.from` is discovery-only app metadata; it never appears in materialized
apps or daemons.

A discovered version must start with a digit, so siblings of an unbounded
trailing placeholder (a bare `php-fpm` symlink, a `php-fpm.conf`) are not mistaken
for versions. Even so, a placeholder bounded on both sides (e.g.
`/usr/lib64/php${version}/bin/php-fpm`, in the app binary path) discovers most
precisely.

### Integer and instance placeholders

`%v`/`${version}` accepts a digit-leading version (`8.3`, `12.0.2`); use
`%n`/`${n}` when the value is a **plain integer** — it matches only whole
numbers, otherwise working exactly like `%v`:

```yaml
kind: app
name: python%n
display_name: "Python ${n}"
binary: "/usr/bin/python${n}"
```

`/usr/bin/python*` then materializes `python2`/`python3`, but not `python3.11` or
`python-config`.

When a `%v` or `%n` template also has an unversioned active-slot binary, Sermo
materializes it automatically. If `/usr/bin/python` exists, this registers
`python` in addition to `python2`/`python3`; when it is absent, only the numbered
binaries are registered. The empty token is substituted before `name`,
`display_name` and `description` are trimmed, so `display_name: "Python ${n}"`
becomes `Python` for the active slot. Set `versions.unversioned: false` to ignore
the marker-less binary; a map form can still override fields for the unversioned
instance when a template needs a custom label:

```yaml
kind: app
name: python%n
display_name: "Python ${n}"
versions:
  unversioned:
    description: "Active Python interpreter"
binary: "/usr/bin/python${n}"
```

Use `%i`/`${instance}` for named init instances discovered from a bounded app
path, for example `versions: { from: "/etc/init.d/openvpn.${instance}" }` on
`kind: app`.

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
its own row (e.g. `PHP-FPM 8.3`, `PHP-FPM 7.4`). `--json` is unaffected by
`--long` — it always emits both, with the structured `name`, `display_name`,
`binary`, `version`, `version_short`, `installed`, `ok` and `status`.

When an app declares `health`, Sermo uses it as the preferred health probe for
`sermoctl apps`/`libs`/`services` and the WebUI application list. Only the exit
code is evaluated (`expect_exit`, default `0`); stdout/stderr matchers and the
printed output are ignored for health. The `version` command is only used as a
fallback health probe when no `health` command exists; when `health` exists,
`version` reports display data and a version failure does not override health.

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

A daemon template may `uses` a base daemon to inherit its checks, processes and
rules, while the linked app supplies the version-specific binary. The packaged
`php-fpm%v` daemon builds on `php-fpm` and links `php-fpm${version}`:

```yaml
kind: daemon
name: php-fpm%v
uses: php-fpm
display_name: "PHP-FPM ${version}"
apps: ["php-fpm${version}"]
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
see `docs/sermo-all.yml` for a complete worked configuration.

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

`commands` declares named auxiliary commands. Sermo never runs them as generic
checks, but the **reserved names** are consumed by features:

- **`health`** — run by the `sermoctl apps`/`libs`/`services` listings and the
  WebUI application list to decide whether an installed application is healthy.
  It uses the same `preflight.<name>` then `commands.<name>` lookup as
  `version`, but only checks the exit code. When present, it takes precedence
  over `version` for app health; `version` remains display-only.
- **`version`** (and `version_short`) — run by the `sermoctl apps`/`libs`/
  `services` listings to report a daemon's version, and **each cycle** by the
  `version.on_change` monitor (see [Service health conditions](rules.md#service-health-conditions-version--state--config)).
  When both exist, `preflight.version` takes precedence over `commands.version`.
- **`reload`** — run by the safe reload operation (`sermoctl reload <service>`
  and `reload_on_change` rules) when the daemon reloads via a command rather
  than its init unit.

Any other entry is informational only. A run can assert its outcome, the same
way a watch hook or `command` check does: `expect_exit` (default 0) and optional
`expect_stdout`/`expect_stderr` matchers — a substring or an `{op, value}`
comparison (`== != > >= < <= contains =~`).

```yaml
commands:
  version:
    command: ["apachectl", "-v"]
    timeout: 5s
    expect_exit: 0                                   # optional, default 0
    expect_stdout: { op: "=~", value: "Apache/2" }   # optional: match the output
```
