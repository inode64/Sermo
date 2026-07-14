# Checks, conditions and rules

## Checks

Checks are single-shot probes under `checks` (and `preflight`, which reuses
the same schema). A `checks` entry flagged `verify: true` also runs as the
post-operation start verification. Supported types:

The complete set of single-shot check types is defined centrally. Tests lock that list against the builder dispatch and configuration validation, and lock the connection-protocol list below against the `conn` registry, so advertised check types cannot drift from the code. (Multi-metric watch forms such as `net`/`icmp`/`swap` and `file`/`process` watches expand on these primitives.)

Connection-protocol checks (MySQL, PostgreSQL, Redis, Docker, libvirt, etc.) are registered by their protocol name with common aliases (e.g. `mysql`/`mariadb`, `fpm`/`php-fpm`).

| type          | passes when                                                        |
|---------------|--------------------------------------------------------------------|
| `tcp`         | a TCP connection to `host:port` succeeds                           |
| `ports`       | a set of `host` ports satisfy an open/closed expectation (see Ports)|
| `http`        | the response matches `expect_status` (and optional headers/body/JSON, see HTTP)|
| `command`     | the command exits with `expect_exit` (default 0) and its output matches optional `expect_stdout`/`expect_stderr`; optional `user` runs it as a specific OS user; `on_change` alerts when its output changes (e.g. a version), array form only |
| `config`      | a config-test command (`apachectl configtest`, `nginx -t`, …) passes, and (with `on_change`) the config `path` is unchanged (see Service health conditions)|
| `service`     | the backend status equals `expect` (active/inactive/paused/failed/unknown)|
| `file_exists` | a foreign flag/lock file exists (never under `<runtime>/locks`)     |
| `file`        | a path exists and is a regular file                                |
| `lockfile`    | one service-created regular lockfile candidate exists — gate with `requires: [service]`; it does not block operations |
| `binary`      | a path exists and is executable                                    |
| `pidfile`     | a pidfile exists and references a running process — gate with `requires: [service]` so a missing/stale pidfile is an error only while the service is active |
| `socket`      | one Unix socket candidate exists — gate with `requires: [service]` for sockets created by the service |
| `libraries`   | all DT_NEEDED shared libraries of the binary can be resolved (native debug/elf, no ldd) |
| `process`     | a process matching `exe`/`user` is in `state` (running/zombie/absent)|
| `metric`      | a sampled metric satisfies `op value` (see Metrics)                |
| `count`       | the number of entries in a directory satisfies `op value` (see Count)|
| `storage`     | a filesystem's space/inode predicates hold (`*_pct` accepts `%`; `*_bytes` requires K/M/G/T) |
| `autofs`      | the autofs automounter is active (autofs mountpoints present — `path`/`count`) (see Autofs)|
| `load`        | a load-average threshold holds (load1/load5/load15, optional per_cpu)|
| `users`       | the count of logged-in users (from utmp) satisfies `count {op, value}`|
| `process_count` | the number of processes (host-wide, or filtered by `user`/`exe`/`exe_dir`) satisfies `count {op, value}`|
| `hdparm`      | a disk's `hdparm` read throughput crosses a threshold (`read`/`cached` MB/s) (see Disk throughput)|
| `sensors`     | hwmon hardware sensors cross a threshold (`temp` °C / `fan` RPM / `voltage` V) (see Hardware sensors)|
| `smart`       | a drive's SMART health/attributes (failed verdict, `reallocated`, `wear`, `temperature`) (see Hardware sensors)|
| `raid`        | a Linux md software-RAID array is degraded/recovering (`degraded`/`recovering`/`arrays`) (see Hardware sensors)|
| `edac`        | ECC memory errors from EDAC (`ce` correctable / `ue` uncorrectable) (see Hardware sensors)|
| `memory`      | system RAM vs the kernel's MemAvailable (used_pct/available_pct/available_bytes) |
| `pressure`    | kernel PSI stall time for cpu/memory/io (`some_*`/`full_*` avg10/60/300) |
| `fds`         | system file descriptors vs `fs.file-max` (used_pct/free/allocated)  |
| `pids`        | the kernel PID table vs `kernel.pid_max` (used_pct/free/count)      |
| `diskio`      | a block device's per-cycle I/O rates (util_pct/read_bytes/write_bytes/await_ms) |
| `conntrack`   | the netfilter conntrack table vs its max (used_pct/free/count)      |
| `firewall_rules` | nftables/iptables has at least `min_rules` loaded rules (see Firewall rules) |
| `route`       | an up default route exists, optionally egressing a given `interface` (see Default route)|
| `clock`       | local wall-clock offset stays within `max_offset` against one of the configured NTP `servers` |
| `net`         | one interface metric (`metric: state\|speed\|errors\|address`) holds — single-metric form of the net watch |
| `icmp`        | one ping metric (`metric: state\|latency`) against `host`, optionally bound to an `interface` |
| `swap`        | one swap metric (`metric: usage\|io`) holds — single-metric form of the swap watch |
| `entropy`     | available kernel entropy satisfies `avail {op, value}`              |
| `zombies`     | the count of zombie processes satisfies `count {op, value}`         |
| `oom`         | the kernel OOM-kill count rose by `delta {op, value}` since last cycle|
| `cert`        | a TLS certificate is expiring/invalid, or its algorithm/issuer changed (see Cert)|
| `mysql` / `mariadb` | a MySQL/MariaDB server answers: with no credentials it reads the handshake greeting (liveness + version); with a user/password it authenticates and pings (see Database) |
| `mongodb` / `mongo` | a connection to a MongoDB server authenticates, pings and reports its version and replica-set `role` for `expect`/`on_change` (see Database) |
| `postgres` / `postgresql` | a connection to a PostgreSQL server authenticates and responds (see Database) |
| `redis` / `valkey` | a connection to a Redis/Valkey server authenticates and answers PING; exposes role, replication, persistence and memory from INFO for `expect` (see Database) |
| `memcached` / `memcache` | a memcached server answers `stats`; exposes version, connections, hits/misses, items, bytes and evictions for `expect` (see Database) |
| `imap`        | an IMAP server greets OK (anonymous) and, with credentials, LOGIN succeeds (see Database) |
| `pop` / `pop3` | a POP3 server greets +OK (anonymous) and, with credentials, USER/PASS succeeds (see Database) |
| `smtp`        | an SMTP server greets 220 + EHLO (anonymous) and, with credentials, AUTH PLAIN succeeds (see Database) |
| `nntp` / `nntps` | an NNTP server greets 200/201 (anonymous) and, with credentials, AUTHINFO USER/PASS succeeds (see Database) |
| `ftp`         | an FTP server greets 220 (anonymous) and, with credentials, USER/PASS login succeeds (see Database) |
| `ssh`         | an SSH server completes key exchange (anonymous: host key + banner); with credentials, login succeeds; `on_change` alerts on host-key change (see Database) |
| `fpm` / `php-fpm` | a PHP-FPM pool answers a FastCGI `/ping` with `pong`; an optional `status_path` exposes pool metrics for `expect` (Unix socket or TCP, see Database) |
| `dns`         | a DNS server answers a query (NOERROR/NXDOMAIN) for `query` (see Database) |
| `ntp`         | an NTP server answers with a synchronized time (server mode, stratum 1–15); exposes leap, precision, root delay/dispersion and reference id for `expect` (see Database) |
| `snmp`        | an SNMP agent answers a system GET (v2c community or v3 user/password); exposes sys name/contact/location/uptime for `expect`; `on_change` alerts on device-identity change (see Database) |
| `tftp`        | a TFTP server answers an RRQ with a valid packet (DATA or ERROR) (see Database) |
| `ldap`        | an LDAP directory accepts an anonymous bind, or a simple bind with credentials (see Database) |
| `ajp`         | an AJP13 connector (e.g. Tomcat's 8009) answers a CPing with CPong (see Database) |
| `ipp` / `cups` | an IPP server (CUPS/cupsd) answers an IPP request with a valid response (see Database) |
| `rsync` / `rsyncd` | an rsync daemon sends its `@RSYNCD:` greeting (see Database) |
| `dhcp` / `dhcpd` | a DHCP server answers a DHCPDISCOVER with a DHCPOFFER (see Database) |
| `dhclient` / `dhcp-client` | a local DHCP client has UDP/68 bound in `/proc/net/udp` (see Database) |
| `rspamd`      | an rspamd worker answers `GET /ping` with `pong` (see Database) |
| `libvirt` / `libvirtd` | a libvirt daemon answers RPC; exposes VM counts (`domains.active`…), node capacity and a VM's state for `expect`/`on_change` (see Database) |
| `dbus`        | a D-Bus daemon completes the auth/Hello handshake and answers `GetId` (see Database) |
| `udisks2`     | UDisks2 is registered on the system bus and answers `Peer.Ping` on its Manager object (see Database) |
| `avahi` / `avahi-daemon` | the Avahi daemon answers `GetVersionString` over its D-Bus API (see Database) |
| `syncthing`   | a Syncthing instance answers `/rest/noauth/health` with `{"status":"OK"}` (see Database) |
| `docker`      | the Docker Engine answers `/info`, exposing container counts (running/paused/stopped), images and a container's state/health for `expect`/`on_change` (see Database) |
| `unifi` / `unifi-controller` / `unifi-network` | a UniFi Network controller answers `GET /status` with `meta.rc == "ok"` on 8443 (see Database) |
| `influxdb` / `influx` | an InfluxDB server answers `/health` (or `/ping`) and reports its version on 8086 (see Database) |
| `prometheus` / `prom` | a Prometheus server answers `/api/v1/status/buildinfo` (or `/-/healthy`) on 9090 (see Database) |
| `cloudflared` / `cloudflare-tunnel` | a Cloudflare Tunnel daemon answers `/metrics` on 60123 with `cloudflared_` metrics (see Database) |
| `clamd` / `clamav` | a ClamAV daemon answers `VERSION` with its engine version (see Database) |
| `spamd` / `spamassassin` | the SpamAssassin daemon answers `PING` with `PONG` (see Database) |
| `nut` / `ups` / `upsd` | NUT's upsd answers `VER`; a UPS exposes its variables (status, battery charge/runtime, load, voltages) for `expect`/`on_change` (see Database) |
| `smb` / `samba` / `cifs` | an SMB/CIFS server negotiates (and, with credentials, authenticates) (see Database) |
| `acpid`       | the ACPI event daemon accepts a connection on its Unix socket (see Database) |
| `fail2ban`    | fail2ban-server accepts a connection on its control socket (see Database) |
| `lvmpolld`    | LVM's poll daemon answers a `hello` request with `OK` over its socket (see Database) |
| `rpcbind` / `portmap` / `portmapper` | the RPC portmapper answers an RPC NULL call (see Database) |
| `nfs` / `nfs-server` / `nfsd` | an NFS server answers an RPC NULL call on 2049 (see Database) |
| `mountd` / `rpc.mountd` / `nfs-mountd` | the NFS mount daemon answers an RPC NULL call to MOUNT (100005) (see Database) |
| `statd` / `rpc.statd` / `nsm` / `nfs-statd` | the NFS status monitor answers an RPC NULL call to NSM (100024) (see Database) |
| `nebula` / `nebula-vpn` | a Nebula mesh-VPN node answers an unknown-tunnel packet with a `recv_error` on 4242/udp (see Database) |
| `openvpn` / `ovpn` | an OpenVPN server answers a hard-reset-client with a hard-reset-server on 1194 (see Database) |
| `rdp` / `ms-wbt-server` | a Remote Desktop server answers the X.224 connection negotiation (see Database) |
| `guacd` / `guacamole` | the Guacamole proxy daemon answers a `select` with a Guacamole instruction (see Database) |
| `asterisk` / `ami` | an Asterisk PBX sends its AMI `Asterisk Call Manager/<version>` greeting (see Database) |
| `sieve` / `managesieve` | a ManageSieve server sends its capability greeting ending in `OK` (see Database) |
| `mqtt`        | an MQTT broker accepts a CONNECT (CONNACK return code 0) (see Database) |
| `amqp` / `rabbitmq` | an AMQP 0-9-1 broker sends a valid Connection.Start greeting (see Database) |
| `kafka`       | a Kafka broker/controller answers an unauthenticated `ApiVersions` request; exposes the listener `role` (broker/controller) and `produce_api`/`vote_api` flags for `expect` (see Database) |
| `varnish` / `varnishadm` | the Varnish management CLI answers with its banner/auth challenge (see Database) |
| `ceph` / `ceph-mon` | a Ceph monitor sends its messenger `ceph v…` banner (see Database) |
| `glusterfs` / `glusterd` / `gluster` | a GlusterFS node's glusterd answers an RPC NULL on 24007 (see Database) |
| `openvswitch` / `ovs` / `ovsdb` / `ovsdb-server` | ovsdb-server answers an OVSDB `list_dbs` JSON-RPC request (see Database) |
| `sqlite` / `sqlite3` | a SQLite database file passes `PRAGMA integrity_check` (see SQLite) |
| `sql`         | a SQL query's scalar result compares (`== != > >= < <= contains =~`) against a value (see SQL query) |
| `mongodb-query` | a MongoDB document count / aggregation / command result compares against a value (see MongoDB query) |
| `influxdb-query` | an InfluxQL (1.x) or Flux (2.x) query's scalar result compares against a value (see InfluxDB query) |
| `size`        | a file/directory grows by at least `grow_by` within `within` (runaway growth) (see Size growth) |
| `websocket` | a WebSocket endpoint completes the RFC 6455 opening handshake (see WebSocket) |

The `storage` check also verifies the **mount** of its `path` — see
[storage and mount units](configuration.md#storage-and-mount-units).

`process` checks and process condition leaves match real UID/GID values read
from `/proc/<pid>/status`. A configured `user:` or `group:` name is resolved
through `engine.user_lookup`; if the name cannot be resolved it fails closed and
matches no process. Numeric UID/GID values avoid host identity-service ambiguity.

The `command` check asserts the command's outcome: `expect_exit` (default 0,
or a list such as `[0, 1]`) and optional `expect_stdout` / `expect_stderr`
matchers — a plain string requires that substring, or an `{op, value}` mapping
compares the trimmed output (`== != > >= < <= contains =~`):

```yaml
checks:
  queue-drained:
    type: command
    user: appqueue             # optional: username or numeric UID on the host
    command: [/usr/local/bin/queue-depth]
    expect_exit: 0
    expect_stdout: { op: "<", value: 100 }   # fewer than 100 items queued
    expect_stderr: ""                         # nothing written to stderr
```

`user` runs the command as that OS user (Linux only). Sermo still executes the
argv directly, never through a shell; the daemon/CLI process must have permission
to switch user (normally by running as root), and an unresolved user or unsupported
runner fails the check closed.

The same `expect_exit` / `expect_stdout` / `expect_stderr` fields are available
on a watch hook (`then.hook`) to validate the hook command's result, but
`then.hook` does not use `user`.

#### Grading output with `analyze:` (pattern sets)

`expect_*` is a single pass/fail assertion. To grade an *otherwise-passing*
command's output into **warning** or **error**, add an `analyze:` block. It references reusable rule sets from `catalog/patterns/`
(category `patterns`, `sermoctl patterns`) and can add or silence rules per
check:

```yaml
checks:
  config:
    type: command
    command: ["/usr/bin/named-checkconf"]
    analyze:
      use: [common, named]     # inherit catalog/patterns sets, in order
      silence: [deprecated]     # drop inherited rules by id
      rules:                    # service-local rules, evaluated FIRST (precedence)
        - { id: zone-ok, match: "(?i)loaded serial", severity: ok }
```

A pattern set is a `patterns` document (under `catalog/patterns/`) with an ordered, id'd rule list:

```yaml
name: common
rules:
  - { id: backup-now, match: "BACK UP DATA NOW",   severity: error }
  - { id: deprecated, match: "(?i)deprecated",      severity: warning }
```

- `match` is a Go RE2 regex (`(?i)` for case-insensitive); `severity` is
  `error` | `warning` | `ok`; optional `stream` is `stdout` | `stderr` | `both`
  (default `both`).
- **Evaluation:** the resolved rule list is the check's local `rules` first (so a
  service `ok` whitelist or stricter rule overrides an inherited one), then the
  `use` sets in order (minus `silence`d ids). Per output line the first matching
  rule wins (an `ok` match whitelists that line); the check's severity is the
  maximum over all lines.
- **Result:** `error` → the check fails as required; `warning` → the check
  fails as *optional* (does not block start/restart/reload/resume
  or drive remediation by itself); no match → the check passes. The matched `pattern_id`
  and line are in the result data.
- **Precedence:** exit-code → `expect_*` → `analyze`. The analyzer only grades a
  command that already passed its exit-code and `expect_*` checks.

### Service health conditions (version / state / config)

A service can enable three standard health monitors with two short
declarative blocks — **`version:`** and **`config:`** — that **reuse the version
and config commands the catalog service already defines** (`commands.version` and
`preflight.config`). Sermo synthesizes a per-service monitor (a watch, built once
so change detection persists) from each:

```yaml
# catalog service (e.g. apache.yml) — already defines these, unchanged:
commands:
  version: { command: [apachectl, -v] }
preflight:
  config: { type: command, command: [apachectl, configtest] }

# service (services/apache.yml) — opt into the monitors:
uses: apache
version:
  on_change: { notify: [ops-email] }      # alert when the version changes
  # on_change: { notify: [ops-email], level: minor }   # …only on major/minor bumps
config:
  on_change: { notify: [ops-email] }      # alert when the config is invalid…
  path: [/etc/apache2/apache2.conf]       # …or (optional) when this file changes
```

- **Version changed** — `version.on_change` runs the catalog service's version command and
  alerts (notifying the listed notifiers) when its version changes — an unexpected
  upgrade/downgrade. Needs `commands.version` (or `preflight.version`) in the
  catalog service. The comparison is on the numeric **`version_short`** (`a.b.c`),
  so noise in the version banner (build dates, suffixes) does not trigger it. An
  optional **`level`** chooses the significant granularity:
  - `major` — only `a` changes fire (`1.4.2 → 1.9.0` is ignored; `1.x → 2.x` fires).
  - `minor` — `a` or `b` changes fire (patch releases ignored).
  - `patch` *(default)* — any `a.b.c` change fires.

  When the version output carries no parseable number, the monitor falls back to
  comparing the raw line so a change is never missed.
- **Config invalid / changed** — `config.on_change` runs the catalog service's
  `preflight.config` test and alerts when it **fails** (invalid config); with a
  `path` it also alerts when a config file changes. A **custom `preflight:`** on
  the service replaces the catalog service's `preflight.config`, and the monitor then uses
  that command, including its `user` field when present.
- **State not errored** — the existing `service` check covers this: it alerts when
  the unit is not in the expected state (`failed`/`unknown`) or the backend cannot
  be queried.
  ```yaml
  checks:
    state: { type: service, expect: active }
  ```

`on_change.notify` follows the usual notify precedence (omit to inherit the global
`notify` default, or `none` to suppress). A service-level `dry_run: true`
suppresses non-console notification delivery for these service-owned monitors;
`wall` still delivers. The underlying `command` (`on_change`) and `config` check
types can also be used as host watch documents when you want a hook or a
standalone command.

### Egress interface (`interface`)

On a **multi-homed host** (several NICs) a network check can be pinned to leave
through a specific interface with the optional **`interface`** field. The value
may be an **interface name**, an **IP** the interface carries, or its **MAC**, and
may be a **single value or a list**:

```yaml
checks:
  gw-via-wan:
    type: icmp
    host: 8.8.8.8
    metric: state
    expect: up
    interface: eth1                 # by name
  db-on-mgmt:
    type: tcp
    host: 10.0.0.5
    port: 5432
    interface: 192.168.1.2          # by IP it carries
  api-by-mac:
    type: http
    url: https://10.0.0.9/health
    interface: "00:11:22:33:44:55"  # by MAC
  gw-redundant:
    type: icmp
    host: 8.8.8.8
    metric: state
    expect: up
    interface: [eth0, eth1]         # two uplinks
    interface_match: any            # any (default) | all
```

- **Optional.** Omit `interface` (the default) and the probe uses normal routing
  across **all** interfaces, exactly as before — nothing changes unless you set it.
- **Value forms.** `eth0` (name), `192.168.1.2` (an address on the interface), or
  `00:11:22:33:44:55` (MAC) — all resolve to the same interface. A **list** pins
  the check to several interfaces.
- **`interface_match`** (only meaningful with a list): **`any`** (default) — the
  check passes if the probe succeeds through **at least one** interface (failover/
  redundant-link monitoring); **`all`** — it passes only if the probe succeeds
  through **every** listed interface (verify each path independently). The
  per-interface outcome is in the result data under `interfaces`.
- **Mechanism.** For TCP/UDP it binds the socket with `SO_BINDTODEVICE`, forcing
  egress through that interface regardless of the routing table; for `icmp` it
  binds the probe to the interface's IPv4 (the `ping -I <addr>` mechanism).
  **Linux only**, and `SO_BINDTODEVICE` needs `CAP_NET_RAW` (root) — if the
  interface does not exist or the daemon lacks privilege the check **fails** rather
  than silently using the wrong link.
- **Where it applies.** `tcp`, `ports`, `icmp`, `websocket`, and every
  connection-protocol check that dials TCP/UDP — native probes and driver-backed
  probes with a custom dialer such as `mysql`, `postgres`, `mongodb`, `ldap`,
  `libvirt`, `redis`, `smtp`, `dns`, `ntp`, `nfs`, `dhcp`, `openvpn`, `nebula`,
  `tftp`, …, plus HTTP-based protocol probes such as
  `influxdb`/`prometheus`/`cloudflared`/`syncthing`/`unifi`/`rspamd`/`ipp` —
  honors the **full list + `interface_match`**. The standalone `http` check
  honors a **single** interface (the first listed).

### Check interdependencies (`requires` / `skip_when_changed`)

Any check may declare interdependencies so it is **skipped** (not counted, no
alert, shown as `skipped`) on a cycle where it should not apply:

```yaml
checks:
  port:
    type: tcp
    host: 127.0.0.1
    port: 3306
  query:
    type: command
    command: ["/usr/bin/mysqladmin", "ping"]
    requires: [port]                              # skip while `port` is failing
    skip_when_changed: ["/etc/my.cnf", "/etc/pam.d/mysql"]  # skip while these changed
```

- **`requires: [check, …]`** — skip this check while any listed check **failed**
  this cycle. This avoids cascading alerts: if MySQL's `port` is down, the deeper
  `query` check is skipped rather than also reported as failing.
- **`skip_when_changed: [path, …]`** — skip this check while any listed file
  differs from its acknowledged baseline (e.g. a config file or library was just
  updated). The baseline is re-acknowledged after a successful (re)start, so the
  check resumes once the service is reconciled.

Both accept a single value or a list. Gates are evaluated **after** the cycle's
checks run, so the probe still executes but its result is suppressed; use a check's
`interval` or remove it to avoid running it at all.

To **restart** a service when a library, file or app version is updated (the
other half of the example — "if the pam library was updated, restart"), use a
remediation rule with a [`changed:`](#rules) condition (or
`restart_on_change: {paths: […], libraries: […], apps: […]}`):

```yaml
rules:
  restart-on-pam:
    type: remediation
    if: { changed: { library: pam } }   # or { path: /lib64/security/pam_unix.so }
    then:
      actions:
        - type: alert
          message: "${service} will restart after library change: ${change.library}"
        - type: restart
```

### Ports

A `ports` check probes several TCP ports on a host at once and evaluates a
quantified open/closed expectation. It is health-style (`OK == true` means the
expectation holds), so a watch over it fires its hook when the expectation breaks.

```yaml
checks:
  web-ports:
    type: ports
    host: 10.0.0.5             # default 127.0.0.1
    ports: "80,443,1024-4000"  # comma-separated single ports and inclusive ranges
    expect: open               # per-port desired state: open | closed | any (default open)
    match: all                 # quantifier: all (AND) | any (OR) | none (NOT) (default all)
    on_change: false           # also fail when any port flips open<->closed between cycles
    connect_timeout: 1s        # per-port dial timeout (default 1s)
```

`expect` is each port's desired state and `match` the quantifier over the ports in
that state: **`all`** = every port (AND), **`any`** = at least one (OR), **`none`**
= no port (NOT). So `expect: open, match: all` passes when **every** port is open;
`expect: closed, match: any` passes when **at least one** is closed. A port is
*open* when it accepts a TCP connection within `connect_timeout`, else *closed*.

`expect: any` skips the state expectation entirely — combine it with
`on_change: true` to alert purely on **state transitions** (a port that was open
becoming closed, or vice versa). Result data exposes `open`, `closed`, `total` and
`changed`. Ports are de-duplicated; a scan is capped at 16384 ports and runs
concurrently, but a large range of *filtered* ports (no response) is bounded only
by `connect_timeout`, so prefer tight ranges and a short timeout.

Like `cert`, the `on_change` detection is **stateful** (it remembers the previous
states across cycles). It works in service checks and host watches while the same
check instance is alive; the baseline is reset when the service worker or watch
is rebuilt, for example after a config reload.

### HTTP

Beyond the status code, an `http` check can send a method, headers and a body
(raw or JSON) and assert the response:

```yaml
checks:
  api:
    type: http
    url: "https://api.example.com/v1/health"
    method: POST                       # any HTTP verb (default GET) — see below
    headers:
      Authorization: "Bearer ${token}" # any request headers
    json:                              # request body as JSON (sets Content-Type
      probe: true                      # automatically; or use `body:` for raw text)
    expect_status: 200                 # code, class (2xx), list, or { op, value }
    follow_redirects: true             # optional; false evaluates a 3xx as-is
    expect_body: { op: contains, value: "ready" } # body comparison (see below)
    expect_latency: { op: "<", value: 800 }   # optional: response time in ms
    proxy: "http://user:pass@squid:3128"   # optional: route the request through a proxy (Squid)
    expect_json:                       # optional: response JSON must match (dotted paths)
      status: ok                       # equality (scalar)
      data.replicas: { op: ">=", value: 2 }   # operator: >, >=, <, <=, ==, !=, contains, =~
      data.message: { op: contains, value: "healthy" }
      data.version: { op: "=~", value: "^v[0-9]+" }   # regex (Go/RE2)
```

It passes (health-style, `OK == true`) when the status matches **and** every
assertion holds. **`method`** accepts any standard HTTP verb — `GET` (default),
`HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`, `TRACE`, `CONNECT` — written
in any case (it is normalized to upper-case); an unknown verb is rejected at
config validation. A request `body`/`json` is sent for any method that carries
one (`POST`/`PUT`/`PATCH`/…). **`http3: true`** sends the request over **HTTP/3
(QUIC)** instead of TCP — see below. **`proxy`** routes the request through a forward proxy such as
**Squid** (`http://[user:pass@]host:port`; `http`, `https`, `socks5` or `socks5h` schemes —
credentials, when present, go in the URL). This both monitors that the proxy
forwards correctly and that the target is reachable through it; for an `https://`
target the proxy is used via `CONNECT`, and certificate inspection (below) still
applies to the target's certificate. `json:` marshals the value and sets `Content-Type:
application/json` (override it via `headers`); `body:` sends a raw string. The
response is only read when `expect_body`/`expect_json` is set (capped at 1 MiB).
`expect_json` looks up **dotted paths** into nested objects. A scalar value is an
equality check (compared as a string); a `{op, value}` mapping uses an operator —
`>`, `>=`, `<`, `<=` (numeric), `==`, `!=`, `contains` (string), or `=~` (regex).
By default the check follows HTTP redirects using Go's standard client policy.
Set `follow_redirects: false` when the redirect itself is the health signal, for
example a local HTTP listener that intentionally redirects every request to
HTTPS.

**Response comparisons.** `expect_body` and `expect_latency` use an `{op, value}`
mapping. `expect_status` accepts either a code/class/list form or the same
`{op, value}` mapping. Operators are `== != > >= < <=` (numeric, or string for
`==`/`!=`), `contains` (substring) and `=~` (Go/RE2 regular expression) — the
same operators as the [`sql`](#sql-query-sql) check:

- `expect_status: { op: "<", value: 500 }` — compare the status code numerically
  (in addition to the code/class/list forms).
- `expect_body: { op: "=~", value: "^OK" }` — compare the **trimmed** response
  body: numeric when both sides parse as numbers (`>`, `<`, …), otherwise string
  equality, `contains` substring matching, or a regex with `=~`.
- `expect_latency: { op: "<", value: 800 }` — fail when the response time in
  milliseconds does not satisfy the comparison.

Result data carries `status` and `latency_ms` for use in rules/hooks.

On an `https://` URL the same check can also inspect the **server certificate**
presented on the request connection, so one check covers reachability *and* TLS
health. Add any of these optional keys (they reuse the `cert` check's logic):

```yaml
checks:
  api:
    type: http
    url: "https://api.example.com/v1/health"
    expect_status: 200
    cert_expires_in_days: 14         # warn this many days before expiry
    cert_verify: true                # verify chain + hostname (default true here)
    cert_on_change: false            # alert on any rotation (leaf fingerprint)
    cert_on_issuer_change: false     # alert when the issuer changes
    cert_on_algorithm_change: false  # alert when the signature algorithm changes
```

Certificate inspection activates when **any** `cert_*` key is present, and
requires an `https` URL — setting one on an `http://` URL is a configuration
error. A certificate problem (expired/not-yet-valid, inside the
`cert_expires_in_days` window, failing verification, or a change between cycles)
**fails** the `http` check, keeping its health-style semantics (`OK == true`
means healthy), the same polarity as the standalone `cert` check. When
inspection runs, the result data carries the same certificate fields the `cert`
check exposes (`issuer`, `subject`, `dns_names`, `not_after`, `days_left`,
`fingerprint`, …). To read the certificate even when it is expired or otherwise
invalid, the request skips transport-level verification and verifies the chain
manually; `cert_verify: false` disables that verification. The change conditions
are **stateful** (they remember the previous cycle). They work in service checks
and host watches while the same check instance is alive, and reset when the
service worker or watch is rebuilt. For raw TLS endpoints or local certificate
files, use the standalone [`cert`](#cert) check.

**HTTP/3 (QUIC).** Set `http3: true` to send the request over **HTTP/3** (QUIC,
UDP) instead of TCP:

```yaml
checks:
  api-h3:
    type: http
    url: "https://api.example.com/health"   # https only (QUIC is always TLS 1.3)
    http3: true
    expect_status: 200
    expect_latency: { op: "<", value: 300 }
```

All the assertions above (status, body, JSON, latency, methods, and certificate
inspection) work the same over HTTP/3. The QUIC transport **never falls back to
TCP**, so a server that does not speak HTTP/3 — or a blocked UDP/443 — makes the
request fail and **fires the check's alert/hook**, which is how you monitor that
HTTP/3 stays available. The negotiated protocol is reported in result data as
`protocol` (e.g. `HTTP/3.0`; for normal checks it is `HTTP/2.0` or `HTTP/1.1`).
HTTP/3 requires an `https` URL and cannot be combined with `proxy` (both rejected
at config validation). Uses `github.com/quic-go/quic-go` (pure Go).

### Cert

A `cert` check inspects TLS material — either a **live TLS endpoint** (`host`) or
a **local file** (`path`). It is health-style: `OK == true` means the certificate
or key material is acceptable, and any configured certificate problem makes the
check fail (`OK == false`). In rules, alert on certificate problems with
`failed: {check: api-cert}`. As a watch, the hook/notify fires when the check
fails.

```yaml
checks:
  api-cert:                    # live endpoint
    type: cert
    host: api.example.com      # host XOR path (exactly one required)
    port: 443                  # optional, default 443
    server_name: api.example.com   # optional SNI + hostname to verify (default = host)
    expires_in_days: 14        # optional: warn this many days before expiry
    cert_verify: true          # optional, default true: chain + hostname + validity
    on_algorithm_change: true  # optional: alert when the signature algorithm changes
    on_issuer_change: true     # optional: alert when the issuer (CA) changes / re-issue
    on_change: false           # optional: alert on any certificate rotation (fingerprint)

  tls-keypair:                 # local file
    type: cert
    path: /etc/ssl/private/api.key   # host XOR path
    on_change: true            # optional: alert if the file's fingerprint changes

rules:
  alert-api-cert:
    if:
      failed: { check: api-cert }
    then:
      action: alert
      message: "api.example.com certificate is invalid, expiring soon or changed"
```

**Host source.** It fails when the certificate is **expired or not yet valid**,
**expires within `expires_in_days`**, fails chain/hostname **verification**
(`cert_verify`, on by default — catches self-signed, wrong host, expired chains), or —
between cycles — its **signature algorithm**, **issuer** or **fingerprint** changes.
A network/TLS error fetching the cert is **not** a `cert` failure (use a
`tcp`/`http` check for reachability).

**File source (`path`).** Reads and parses a local file, recognising natively (no
external tools): PEM **certificate**, **certificate request** (CSR), PKCS#1 / EC /
PKCS#8 **private keys**, PKIX **public key**, **OpenSSH** private key, and **OpenSSH**
public key (`authorized_keys` line). Certificates are checked for expiry/validity as
above; material that does not expire (keys, CSRs) fails only on
`on_change`/`on_algorithm_change`. A **missing, unreadable or unparseable file makes
the check fail** (a local configuration problem, unlike a transient network
error). `cert_verify`, `port` and `server_name` do not apply to files.

**Result data** exposes `kind` (certificate / certificate_request / private_key /
public_key / openssh_private_key / openssh_public_key / …), `source`,
`signature_algorithm`, `public_key_algorithm`, `key_bits`, `subject` and
`fingerprint`. Certificates additionally expose `days_left`, `not_before`,
`not_after`, `issuer`, `serial_number` (hex) and `dns_names` (SANs).

The change conditions are **stateful** (they remember the previous value across
cycles). They work in service checks and host watches while the same check
instance is alive; the baseline is reset when the service worker or watch is
rebuilt, for example after a config reload.

Each check has an optional `timeout` (else `engine.default_timeout`) and an
optional `interval` to run it less often than the worker cycle — every
`round(interval / resolution)` cycles, reusing its last result in between (see
[per-check interval](configuration.md#per-check-interval)).

A health check (`tcp`/`http`/`service`/`command`/`cert`/…) may also set
`verify: true` to double as the **post-operation start verification**: after a
successful start/restart/reload/resume the engine runs every `verify: true`
check once and fails the operation (`postflight_failed`) if a required one is
not OK — its `for`/`within` window and any remediation are ignored, only the
immediate result counts, and `optional: true` makes a failure a warning. This
replaces the retired `postflight:` section, so the health probe is defined once
and serves both periodic monitoring and start verification. `verify: true` is
rejected on condition checks (`metric`/`storage`/`load`/`fds`/…) whose OK does
not confirm a successful start.

### Database connection (`mysql` / `mariadb`)

A connection-protocol check connects to a server over its wire protocol and
verifies it responds; the check type **is** the protocol name. A few conventions
keep the per-protocol entries short:

- **`tls`** (where listed) accepts `false` (plaintext, the default), `true`
  (verified TLS) or `skip-verify` (TLS without certificate verification). Entries
  add only protocol-specific notes — the implicit-TLS port (e.g. IMAPS 993) or
  extra modes (e.g. PostgreSQL sslmodes).
- **Auth** is noted per entry; many protocols are anonymous.
- **`socket`** (a Unix socket path) dials the socket instead of `host`/`port`;
  **`query`** is the per-protocol lookup target (e.g. the DNS name for `dns`).

Protocols, in the order of the table above:

- `mysql` (alias `mariadb`) — default port 3306; `tls` supported. `user` is
  **optional**: with no user/password it reads the server's initial handshake
  packet (sent before auth) to prove liveness and report the version — no
  credentials, like the smtp/amqp greeting probes. With a user/password it
  authenticates and pings via `github.com/go-sql-driver/mysql` (the deeper
  check). An ERR handshake (host blocked, too many connections) fails the probe.
- `mongodb` (alias `mongo`) — default port 27017; `tls` supported. `user` is
  **optional** (MongoDB may run without auth); with credentials it authenticates
  against `auth_source` (defaults to `database`, then `admin`). Connects, verifies
  a `ping`, and reads the version via `buildInfo`. A `hello` (with the legacy
  `isMaster` as fallback) reports the replica-set `role`
  (`primary`/`secondary`/`arbiter`/`standalone`), `set_name` and `read_only`, so
  an `expect:` rule can assert e.g. `role == primary`. To run a query and compare
  a result, see the **MongoDB query** check. Uses `go.mongodb.org/mongo-driver`.
- `postgres` (alias `postgresql`) — default port 5432; `tls` supported, plus the
  PostgreSQL sslmodes (`disable`/`require`/`prefer`/`verify-ca`/`verify-full`).
  Uses `github.com/lib/pq`.
- `redis` (alias `valkey`) — default port 6379; `tls` supported. `user` is
  **optional** (legacy `requirepass` uses a password only, or no auth at all); a
  password-only check sends `AUTH <password>`. Verifies `PING` → `PONG` over RESP
  (no driver). A single `INFO` then reports the server `version` (pair with
  `on_version_change`) plus health fields exposed for `expect:`: `role`,
  `master_link_status` (replicas), `rdb_last_bgsave_status`,
  `aof_last_write_status`, `loading`, `used_memory`, `maxmemory`,
  `mem_fragmentation_ratio`, `connected_clients` and `uptime_seconds`.
- `memcached` (alias `memcache`) — default port 11211; `socket` supported (Unix
  socket), `tls` supported. No auth (the ASCII text protocol). Sends a single
  `stats` command and verifies the server answers `STAT` lines terminated by
  `END` — proof the daemon is up. Reports the server `version` (pair with
  `on_version_change`) plus counters exposed for `expect:`: `uptime`,
  `curr_connections`, `total_connections`, `rejected_connections`, `cmd_get`,
  `cmd_set`, `get_hits`, `get_misses`, `curr_items`, `total_items`, `bytes`,
  `evictions`, `limit_maxbytes` and `threads` (all numeric, so `>`/`<`/`==` work).
- `imap` — default port 143; `tls` supported (implicit TLS / IMAPS — use port
  993). `user` is **optional**: with no credentials it verifies the server greets
  `* OK`; with a user/password it performs an IMAP `LOGIN`. RFC 3501.
- `pop` (alias `pop3`) — default port 110; `tls` supported (POP3S — use port 995).
  `user` is **optional**: anonymous verifies the `+OK` greeting; with a
  user/password it performs `USER`/`PASS`. RFC 1939.
- `smtp` — default port 25; `tls` supported (SMTPS — use port 465; submission
  587). `user` is **optional**: anonymous checks the `220` greeting + `EHLO`; with
  a user/password it performs `AUTH PLAIN`. RFC 5321.
- `nntp` (alias `nntps`) — default port 119; `tls` supported (NNTPS — use port
  563). `user` is **optional**: anonymous checks the greeting (`200` posting
  allowed / `201` prohibited — reported as `posting_allowed`); with a user/password
  it performs `AUTHINFO USER`/`PASS`. RFC 3977/4643.
- `ftp` — default port 21; `tls` supported (FTPS — use port 990). `user` is
  **optional**: anonymous checks the `220` greeting; with a user/password it
  performs `USER`/`PASS` (a password with no user logs in as `anonymous`). RFC 959.
- `ssh` — default port 22 (no `tls`: SSH has its own transport crypto). `user` is
  **optional**: anonymous completes the key exchange to capture the server's host
  key (authentication then fails, which is expected); with a user/password login
  must succeed. Result data: `fingerprint` (SHA256 of the host key),
  `host_key_algo`, `server_version`, `protocol`. Set **`on_change: true`** to
  alert when the host-key fingerprint changes — a possible re-key or
  man-in-the-middle. Uses `golang.org/x/crypto/ssh`.
- `fpm` (alias `php-fpm`) — PHP-FPM over FastCGI. Set `socket` to the pool's Unix
  socket (e.g. `/run/php/php8.2-fpm.sock`), or use `host`/`port` (default 9000) for
  a TCP pool. No auth. Performs a FastCGI request to `/ping` and expects `pong`, so
  the pool must have **`ping.path = /ping`** enabled. Set **`status_path`** (the
  pool's `pm.status_path`) to additionally fetch the status page and expose pool
  metrics for `expect:`: `pool`, `process_manager`, `active_processes`,
  `idle_processes`, `total_processes`, `listen_queue`, `max_listen_queue`,
  `max_active_processes`, `max_children_reached`, `slow_requests`,
  `accepted_conn` and `uptime_seconds`.
- `dns` — default port 53 (UDP). No auth. Sends an `A` query for `query` (default
  `localhost`) and verifies the answer: `NOERROR`/`NXDOMAIN` pass (the server is up
  and speaking DNS); `SERVFAIL`, `REFUSED`, a timeout or a transport error fail.
  Result data: the `rcode`, answer count and the resolved `addresses` (the
  answer's A/AAAA records, sorted and comma-joined) — so `expect` can require an
  actual resolution (`rcode: NOERROR`, `answers: {op: ">", value: 0}`) or a
  specific address (`addresses: {op: "=~", value: "93\\.184\\..*"}`). Set
  `query` to a name the server should answer (e.g. a zone it is authoritative
  for). With `resolvconf: true` (instead of `host`, mutually exclusive) the
  probe asks the first `nameserver` of `/etc/resolv.conf` — the server the
  system would ask first; with pppd's `usepeerdns`, the provider's
  resolver, which is how the `pppd` catalog service verifies resolution through
  the uplink. If that resolver is local to the host (loopback such as
  `127.0.0.0/8`/`::1`, or any address assigned to a local interface), an
  `interface` pin is ignored for the DNS packet because the resolver must be
  reached locally. RFC 1035.
- `ntp` — default port 123 (UDP). No auth. Sends a client request and verifies the
  server answers in **server mode** with a synchronized **stratum (1–15)**; a
  kiss-o'-death (stratum 0) or unsynchronized (stratum 16) reply fails. Result
  data: `stratum`, the clock `offset_seconds`, the `leap` indicator
  (`none`/`add-second`/`del-second`/`unsynchronized`), `precision_seconds`,
  `root_delay_ms`, `root_dispersion_ms` and the `reference_id` (a stratum-1
  refclock label such as `GPS`, or the upstream server's IP). So an `expect:`
  rule can assert e.g. `leap == none` or a `root_dispersion_ms` ceiling. RFC 5905.
- `snmp` — default port 161 (UDP). With **no `user`** it uses **SNMPv2c** with a
  community string (`password`, default `public` — the anonymous/shared-secret
  model). With a **`user`** it uses **SNMPv3 USM**: a `password` adds SHA
  authentication (authNoPriv), otherwise noAuthNoPriv. It reads the system group;
  result data carries `sys_object_id`, `snmp_version`, the description (as the
  version banner) and — when the agent exposes them — `sys_name`, `sys_contact`,
  `sys_location` and `sys_uptime_seconds` (assertable via `expect:`). Set
  **`on_change: true`** to alert when `sysObjectID` (the device identity —
  model/firmware) changes. Uses `github.com/gosnmp/gosnmp`.
- `tftp` — default port 69 (UDP). No auth. Sends a read request (RRQ) for `query`
  (default `sermo-tftp-check`) and verifies a valid TFTP packet: a `DATA` reply
  (the file is served) or an `ERROR` reply (e.g. file not found) both pass. Result
  data: the reply kind and, for an error, the TFTP error code/message. RFC 1350.
- `ldap` — default port 389; `tls` supported (implicit TLS / LDAPS — use port
  636). `user` is **optional**: with no credentials it does an **anonymous bind** (a
  successful bind, or an LDAP-level rejection, both prove the directory is up —
  only a transport error fails); with a user/password it does a **simple bind**
  where `user` is the bind DN and must succeed. Result data: the bind mode and
  result. Uses `github.com/go-ldap/ldap/v3`.
- `ajp` — default port 8009 (TCP). No auth. Sends an **AJP13 CPing** and expects a
  **CPong** — the same liveness probe Apache/nginx use against Tomcat's AJP
  connector.
- `ipp` (alias `cups`) — default port 631; `tls` supported (IPPS). No auth. POSTs
  an IPP `CUPS-Get-Default` request over HTTP and verifies a valid IPP response —
  any parseable reply proves cupsd is up and speaking IPP. Result data: the IPP
  version and status. RFC 8010/8011.
- `rsync` (alias `rsyncd`) — default port 873 (TCP). No auth. Reads the rsync
  daemon's `@RSYNCD: <version>` greeting; receiving it proves the daemon is up.
  Result data carries the protocol version.
- `dhcp` (alias `dhcpd`) — default port 67 (UDP). **Linux only.** No auth. Sends a
  `DHCPDISCOVER` and verifies the server replies with a `DHCPOFFER` — proof it is
  up and handing out leases. It never sends a `DHCPREQUEST`, so **no real lease is
  consumed**. Two modes: set `interface` to **broadcast** the DISCOVER out that
  link and discover any server (`255.255.255.255`); omit it to **unicast** to
  `host` (a known server or relay). The client hardware address is a random,
  anonymous locally-administered MAC by default; set `mac` to use a fixed address
  (e.g. a server that only answers reserved clients). Result data: the offered IP,
  server id, subnet mask and lease time. **Requires elevated privileges** to bind
  the DHCP client port 68 (and `CAP_NET_RAW` for the per-interface bind), like the
  `icmp` check; the host should not run a competing DHCP client on that interface.
  RFC 2131.

  ```yaml
  checks:
    dhcp-broadcast:
      type: dhcp
      interface: eth0            # broadcast on this link (discovers any server)
      mac: "02:00:00:ab:cd:ef"   # optional; default is a random anonymous MAC
    dhcp-unicast:
      type: dhcp
      host: 10.0.0.1             # unicast to a known server/relay (no interface)
  ```
- `dhclient` (alias `dhcp-client`) — default port 68 (UDP). **Linux only.** This
  is a local DHCP client check: `dhclient` receives offers on UDP/68 and does not
  provide a request/response server protocol. The check reads `/proc/net/udp` and
  passes when it finds a local UDP socket bound to `host:port` (`0.0.0.0:68` by
  default in the packaged catalog service). It does not send packets and does not consume
  a lease. Set `lease_file` (the packaged catalog service defaults to
  `/var/lib/dhcp/dhclient.leases`; override it when your distribution stores ISC
  dhclient leases elsewhere) to also require an unexpired lease. If `interface`
  is set, the lease must belong to that interface.
- `rspamd` — default port 11334 (the controller worker); `tls` supported (HTTPS).
  No auth. Sends `GET /ping` and expects `200` with a `pong` body — the
  unauthenticated liveness endpoint every rspamd worker exposes (point `port` at
  11333 for the normal scanning worker or 11332 for the proxy). Result data: the
  rspamd version, read from the `Server` header.
- `libvirt` (alias `libvirtd`) — opens an RPC connection to a libvirt daemon and
  reads its version; both succeeding prove libvirtd is up. It runs no write
  operation. **Transport:** with no `socket` and no `host` it dials the local Unix
  socket `/run/libvirt/libvirt-sock`; set `socket` for a different path such as
  `/run/libvirt/virtqemud-sock` on modular libvirt hosts, or set `host` to use
  plain **TCP** (default port 16509). TLS/SASL is not supported.
  **Connect URI:** `query` selects the driver, default `qemu:///system` (e.g.
  `lxc:///`, `xen://`). No auth — local socket access is governed by the socket's
  permissions/polkit. Uses `github.com/digitalocean/go-libvirt`.

  Beyond liveness it exposes variables for conditions (best-effort — a driver that
  rejects them still reports up): **`domains.active`** (running VMs),
  `domains.inactive`, `domains` (total), and node capacity `node.cpus`,
  `node.memory_mb`. Set **`domain`** to a VM name to also read its `domain.state`
  (`running`/`paused`/`shutoff`/`crashed`/…) and `domain.running`; `on_change` then
  alerts on that VM's state transitions, and an unknown domain fails the check.
  Result data also carries the libvirt version, connect URI, transport and hostname.

  ```yaml
  checks:
    libvirt-local:
      type: libvirt              # dials /run/libvirt/libvirt-sock
      expect:
        domains.active: { op: ">=", value: 3 }   # alert if fewer than 3 VMs are running
    libvirt-modular:
      type: libvirt
      socket: /run/libvirt/virtqemud-sock
      query: "qemu:///system"
    libvirt-tcp:
      type: libvirt
      host: 10.0.0.4             # plain TCP on 16509
      query: "qemu:///system"    # optional connect URI (default qemu:///system)
    db-vm:
      type: libvirt
      domain: db01               # watch a single VM
      on_change: true            # alert on its state transitions
      expect:
        domain.state: { op: "==", value: running }
  ```
- `dbus` — connects to a D-Bus daemon and completes its SASL auth +
  `org.freedesktop.DBus.Hello` handshake — which alone proves the bus is up — then
  calls `org.freedesktop.DBus.GetId` to read the bus UUID. It runs no write
  operation. **Target:** defaults to the system bus
  (`unix:path=/run/dbus/system_bus_socket`); set `socket` for a different
  socket path, or `query` for a full D-Bus address (`unix:abstract=…`,
  `tcp:host=…,port=…`). Socket-based, so there is no TCP port. No auth — access is
  governed by the socket's permissions. Result data: the bus id, address and the
  connection's unique name. Uses `github.com/godbus/dbus/v5`.

  ```yaml
  checks:
    dbus-system:                 # dials unix:path=/run/dbus/system_bus_socket
      type: dbus
    dbus-custom:
      type: dbus
      socket: /run/dbus/system_bus_socket   # or use `query` for a full address
  ```
- `udisks2` — the UDisks2 disk-management daemon on the system D-Bus bus. Connects
  to the bus (SASL auth + Hello), verifies `org.freedesktop.UDisks2` has a name
  owner, and calls `org.freedesktop.DBus.Peer.Ping` on
  `/org/freedesktop/UDisks2/Manager` — proof the service is registered and
  answering, not merely that `dbus-daemon` is up. **Target:** like `dbus`, defaults
  to the system bus; set `socket` for a different bus socket or `query` for a full
  D-Bus address. Socket-based, no TCP port, no auth. Result data: the D-Bus unique
  name owning `org.freedesktop.UDisks2`. Uses `github.com/godbus/dbus/v5`.

  ```yaml
  checks:
    udisks2:
      type: udisks2
      timeout: 5s
  ```
- `avahi` (alias `avahi-daemon`) — the Avahi mDNS/DNS-SD (zeroconf) daemon, probed
  over its D-Bus API (`org.freedesktop.Avahi`). Connects to the system bus (SASL
  auth + Hello) and calls `org.freedesktop.Avahi.Server.GetVersionString` — a reply
  proves avahi-daemon is up and registered on the bus — reporting the `version`
  (pair with `on_version_change`) and, best-effort, the `hostname` and server
  `state` (`running` when AVAHI_SERVER_RUNNING). **Target:** like `dbus`, defaults
  to the system bus; set `socket` for a different bus socket or `query` for a full
  D-Bus address. Socket-based, no TCP port, no auth. Uses
  `github.com/godbus/dbus/v5`.
- `syncthing` — default port 8384; `tls` supported (`skip-verify` covers
  Syncthing's default self-signed GUI certificate). Sends `GET /rest/noauth/health`
  and expects `200` with `{"status":"OK"}` — the unauthenticated liveness endpoint.
  With an **API key** in `password` (sent as `X-API-Key`) it also reads
  `/rest/system/version` and reports the Syncthing version (`os`/`arch` too); a
  rejected key fails the check. No user.

  ```yaml
  checks:
    syncthing:
      type: syncthing
      host: 127.0.0.1
      # tls: skip-verify            # if the GUI is on HTTPS
      # password: "${env:ST_KEY}"   # optional API key -> also reports version
  ```
- `unifi` (aliases `unifi-controller`, `unifi-network`) — a UniFi Network
  controller (Ubiquiti). Default port 8443, **HTTPS-only** with a self-signed
  certificate, so `tls` here selects only verification: it is **skipped by
  default**; set `tls: true` to require a valid certificate. No user. Sends `GET
  /status` (the unauthenticated liveness endpoint) and expects `200` with JSON
  `meta.rc == "ok"`, reporting `server_version` (pair with `on_version_change`) and
  `uuid`. Targets the self-hosted UniFi Network application; on a UniFi OS console
  (UDM/Cloud Key) the controller is proxied under `/proxy/network/`, which this
  check does not follow.
- `influxdb` (alias `influx`) — an InfluxDB server. Default port 8086; `tls`
  supported (`true`/`skip-verify` → https; plain HTTP by default). No auth. GETs
  `/health` (InfluxDB 2.x / 1.8+) and verifies a JSON `status` of `pass`, reporting
  the server `version` (pair with `on_version_change`); on older servers without
  `/health` it falls back to `/ping`, which answers `204` with the version in the
  `X-Influxdb-Version` header. A liveness/version check; to run an InfluxQL query
  and compare a result, see the **InfluxDB query** check.
- `prometheus` (alias `prom`) — a Prometheus server. Default port 9090; `tls`
  supported (https). GETs `/api/v1/status/buildinfo` and verifies a `success`
  status, reporting the server `version` (pair with `on_version_change`); on older
  servers it falls back to `/-/healthy` (liveness only). An optional `user`/
  `password` is sent as HTTP Basic auth (for a reverse proxy fronting the API).
- `cloudflared` (alias `cloudflare-tunnel`) — Cloudflare Tunnel's local metrics
  endpoint. Default port 60123; `tls` supported (https, plaintext by default).
  GETs `/metrics`, requires HTTP 200, and verifies that the Prometheus text
  contains `cloudflared_` metric names. This confirms the cloudflared daemon's own
  endpoint is responding instead of only checking that TCP accepts connections.
- `clamd` (alias `clamav`) — default port 3310 (TCP), or a Unix socket via `socket`
  (e.g. `/run/clamav/clamd.ctl`). No auth, no TLS. Sends the clamd `VERSION` command
  and verifies a `ClamAV <version>/…` reply. Result data: the engine `version` (the
  daily signature-database part is dropped, so `on_version_change` stays quiet
  across routine DB updates) and the full `version_string`.
- `spamd` (alias `spamassassin`) — default port 783 (TCP), or a Unix socket via
  `socket`. No auth. Sends a SPAMC/SPAMD `PING` and verifies spamd answers
  `SPAMD/<v> 0 PONG`. Result data: the SPAMD protocol version.
- `nut` (aliases `ups`, `upsd`) — NUT (Network UPS Tools) upsd; default port 3493
  (TCP), `tls` supported (implicit TLS — upsd's `STARTTLS` upgrade is not used).
  `user`/`password` are **optional**: anonymously it sends `VER` and reports the
  upsd `version` (pair with `on_version_change`). With credentials it `LOGIN`-s to
  the UPS to verify access (`USERNAME`/`PASSWORD` alone are not checked by upsd).

  Set **`ups`** to the device name (or omit it when the server has a single UPS —
  it is auto-detected) to read its variables into the result, where you alert on
  them with `expect` or on state changes with `on_change`. Exposed variables
  (when present): `ups.status` (the power/battery state — `OL` online, `OB` on
  battery, `LB` low battery, `RB` replace battery, `CHRG`/`DISCHRG` …), `ups.load`,
  `ups.temperature`, `ups.power`/`ups.realpower`, `battery.charge`,
  `battery.charge.low`, `battery.runtime`/`battery.runtime.low`, `battery.voltage`,
  `input.voltage`, `input.frequency`, `output.voltage`, `ups.mfr`, `ups.model`. An
  unknown `ups` fails the check.

  ```yaml
  checks:
    ups:
      type: nut
      host: 192.168.1.10
      ups: myups                              # omit to auto-detect a single UPS
      user: monuser                           # optional (verifies access via LOGIN)
      password: ${env:NUT_PASS}
      on_change: true                         # alert on any ups.status transition
      expect:
        ups.status: { op: "=~", value: "OL" }  # alert when not online (use =~: status is "OL CHRG")
        battery.charge: { op: ">", value: 30 } # alert when charge drops to 30%
  ```

  `on_change` tracks `ups.status`; for the upsd software version use
  `on_version_change`. Because `ups.status` is a space-separated flag list (e.g.
  `OL CHRG`), match it with `=~` rather than `==`.
- `docker` — the Docker Engine API. By default it talks to the local Unix socket
  `/run/docker.sock`; set `host` (and `port`, default 2375 / 2376 with `tls`)
  for a TCP daemon, or `socket` for a non-default path. No `user`. It GETs `/info`
  (proving the daemon is up), reports the engine `version` (pair with
  `on_version_change`), and exposes counts: **`containers`**,
  **`containers.running`**, `containers.paused`, `containers.stopped`, `images`,
  and `warnings` (number of daemon warnings). Set **`container`** (name or id) to
  also read that container's `container.status` (`running`/`exited`/`restarting`/…),
  `container.health` (`healthy`/`unhealthy`/`starting`/`none`), `container.running`,
  `container.restartcount` and `container.exitcode`; `on_change` then alerts on its
  state/health transitions. An unknown container fails the check.

  ```yaml
  checks:
    docker:
      type: docker                            # local socket by default
      expect:
        containers.running: { op: ">=", value: 4 }  # alert if fewer than 4 are up
        containers.stopped: { op: "==", value: 0 }  # alert on any stopped container
        warnings: { op: "==", value: 0 }            # alert on daemon warnings
    web-container:
      type: docker
      container: web                          # watch one container
      on_change: true                         # alert on status/health transitions
      expect:
        container.health: { op: "==", value: healthy }
        container.restartcount: { op: "<", value: 5 } # alert on a restart loop
  ```

  Most interesting conditions: `containers.running` (expected services up),
  `containers.stopped` (crashed/exited containers), per-`container` `status`/`health`
  and `restartcount`. The Docker check is read-only. To let Sermo start, stop,
  restart or resume that same container through the safe operation engine, add a
  service-level `control: { type: docker, container: ... }` block.
  and `restartcount` (restart loops), `warnings`, and `on_version_change` for engine
  upgrades.
- `smb` (aliases `samba`, `cifs`) — default port 445 (TCP). `user` is **optional**.
  It first runs an SMB2 `NEGOTIATE` (proving the server is up) and reports the
  negotiated **dialect** as the `version` (`2.0.2`/`2.1`/`3.0`/`3.0.2`/`3.1.1` —
  pair with `on_version_change`), the `protocol` family (`SMB2`/`SMB3`) and whether
  **signing is required**. With a `user` it then authenticates over **NTLM** (a
  failed login fails the check), counts the shares (`shares`), and — if a share is
  named in `query` — verifies it can be **mounted** (`share_access`). The domain
  may be embedded in `user` (`DOMAIN\user` or `user@domain`). The NEGOTIATE is
  native; the authenticated session uses `github.com/cloudsoda/go-smb2`.

  ```yaml
  checks:
    fileserver:
      type: smb
      host: 10.0.0.9
      user: "WORKGROUP\\monitor"     # optional; enables NTLM auth + share checks
      password: "${env:SMB_PASS}"
      query: "data"                   # optional: verify this share mounts
  ```
- `acpid` — the ACPI event daemon. **Socket-only** (no TCP port; defaults to
  `/run/acpid.socket`, override with `socket`). It is an event broadcaster with
  no request/response protocol, so the check is the **connect itself**: a
  successful connection proves acpid is listening (a stale socket left by a dead
  daemon refuses the connection). It reads nothing — reading would block until an
  ACPI event — and there is no version. No auth.
- `fail2ban` — fail2ban-server. **Socket-only** (defaults to
  `/run/fail2ban/fail2ban.sock`, override with `socket`). Its Python pickle
  command protocol is not worth reimplementing for a liveness check, so — like
  `acpid` — the check is the **connect itself**; it exchanges no commands. No auth.
- `lvmpolld` — LVM's poll daemon. **Socket-only** (defaults to
  `/run/lvm/lvmpolld.socket`, override with `socket`). Unlike acpid/fail2ban it is
  probed by protocol: it speaks LVM's generic daemon framework, so the check sends
  a `hello` request and verifies the daemon replies `OK`, also guarding against a
  different LVM daemon (lvmetad, dmeventd) by the reported protocol name. Result
  data: the `protocol` and `protocol_version` (the handshake exposes no lvm2
  software version). No auth.
- `rpcbind` (aliases `portmap`, `portmapper`) — default port 111 (UDP). No auth.
  Sends an **ONC RPC NULL** call (RFC 5531/1833) to the portmapper program (100000
  v2) and verifies a well-formed RPC reply — any reply (accepted or denied) proves
  the daemon is up and speaking RPC; result data carries the `rpc_status`. The same
  NULL-call probe backs the `nfs`/`mountd`/`statd`/`glusterfs` checks below.
- `nfs` (aliases `nfs-server`, `nfsd`) — an ONC RPC NULL to the NFS program
  (100003) over TCP (record marking), like `rpcbind`; default port 2049. A
  version-mismatch reply (e.g. an NFSv4-only server answering a v3 NULL) still
  passes.
- `mountd` (aliases `rpc.mountd`, `nfs-mountd`) — the NFS mount daemon: an ONC RPC
  NULL to the MOUNT program (100005) over TCP, like `nfs`. **No fixed well-known
  port** — mountd registers a (often random) port with rpcbind; default 20048,
  override `port` (find it with `rpcinfo -p <host>`).
- `statd` (aliases `rpc.statd`, `nsm`, `nfs-statd`) — the NFS status-monitor (NSM,
  used for lock recovery): an ONC RPC NULL to the NSM program (100024), like
  `mountd`. Default port 662; same no-fixed-port caveat — override `port`
  (`rpcinfo -p <host>`).
- `nebula` (alias `nebula-vpn`) — a [Nebula](https://github.com/slackhq/nebula)
  mesh-VPN node. Default port 4242 (**UDP**). No auth. A real tunnel needs a
  CA-signed certificate, but a node answers a data packet for a tunnel index it
  does not know with a plaintext **recv_error** (telling the sender to
  re-handshake), so the check sends a Nebula `message` packet carrying a random
  index and verifies the node replies with a `recv_error` echoing it — proof the
  node is up, with no credentials. The reply is governed by the node's
  `listen.send_recv_error` setting (default `always`); a node set to `never` — or to
  `private` when probed from a public address — stays silent and reads as down, so
  probe lighthouses/nodes from an address their config answers.
- `openvpn` (alias `ovpn`) — an OpenVPN server. Default port 1194; `transport`
  selects the transport (`udp`, the default, or `tcp` — match the server's
  `proto`). No auth. The first step of the OpenVPN handshake is unauthenticated
  (TLS comes after): the check sends a `P_CONTROL_HARD_RESET_CLIENT_V2` carrying a
  random session id and verifies the server answers with a
  `P_CONTROL_HARD_RESET_SERVER_V2` acknowledging it. Result data: the `transport`.
  **Caveat:** the reset only gets a reply from a server without `tls-auth`/
  `tls-crypt`; those HMAC-wrap (or encrypt) control packets, so a bare reset is
  dropped — silence is then expected and is not proof it is down.
- `rdp` (alias `ms-wbt-server`) — default port 3389 (TCP). No auth. Sends an X.224
  **Connection Request** with an RDP Negotiation Request and verifies the server
  answers with an X.224 **Connection Confirm**; a negotiation failure still counts
  as up (the server answered). Result data: the negotiated `security` protocol
  (`rdp` = standard RDP security, `tls`, `hybrid` = CredSSP/NLA, `hybrid-ex`).
  MS-RDPBCGR; the negotiation precedes authentication, so no credentials.
- `guacd` (alias `guacamole`) — default port 4822 (TCP). No auth. Opens the
  Guacamole handshake by sending a `select` instruction for a protocol (`query`,
  default `vnc`) and verifies guacd replies with a well-formed Guacamole
  instruction — an `args` reply (protocol available) or an `error` (e.g. plugin
  missing) both prove guacd is up. Result data: the selected protocol and the reply
  `opcode`.
- `asterisk` (alias `ami`) — default port 5038 (TCP); `tls` supported (AMI over
  TLS). No auth. On connect, Asterisk's Manager Interface sends an `Asterisk Call
  Manager/<version>` greeting before any login; reading it yields the manager
  `version` (result data also carries the full `banner`). Pair with
  `on_version_change` to alert on an Asterisk upgrade.
- `sieve` (alias `managesieve`) — default port 4190 (TCP); `tls` supported
  (implicit TLS). No auth. On connect the server sends a greeting of capability
  lines terminated by an `OK` response (RFC 5804); reading it and seeing the `OK`
  proves the server is up. The `IMPLEMENTATION` capability is reported as the server
  `version` (a `NO`/`BYE` greeting, e.g. a connection-limit refusal, fails the
  check).
- `mqtt` — default port 1883 (TCP); `tls` supported (MQTTS, port 8883). Performs an
  MQTT 3.1.1 `CONNECT` handshake and verifies the broker answers `CONNACK`
  accepting the connection (return code 0). With no credentials it is an anonymous
  connect; `user`/`password` authenticate. A refused CONNACK (e.g. `not-authorized`,
  `bad-username-or-password`) fails the check with the reason; result data: the
  `connack` status.
- `amqp` (alias `rabbitmq`) — default port 5672 (TCP); no auth. Sends the AMQP
  0-9-1 protocol header and verifies the broker's unprompted Connection.Start
  method. Reports the broker `version` plus best-effort `product`, `platform`
  and `cluster_name` fields for `expect`/`on_version_change`.
- `kafka` — default port 9092 (TCP); `tls` supported. No auth. Sends an
  `ApiVersions` request (API key 18, v0), which a broker or a KRaft controller
  answers before authentication, and verifies the reply's correlation id matches —
  proof the peer speaks the Kafka wire protocol. From the advertised API set it
  derives `role` (`broker` when the data-plane Produce API is present, `controller`
  when the Raft `Vote` quorum API is, and Produce is not) and the `produce_api` /
  `vote_api` (`yes`/`no`) flags, plus `api_count` and `error_code` — all assertable
  via `expect`. Used by the `kafka-broker` (9092, `expect role=broker`) and
  `kafka-controller` (9093, `expect role=controller`) catalog services.
- `varnish` (alias `varnishadm`) — default port 6082 (TCP, the Varnish `-T`
  management CLI). No auth. On connect varnishd sends a CLI response (a `<status>
  <length>` line and a body); status **200** carries the banner (with the version)
  and **107** is an authentication challenge (a CLI secret is set) — either proves
  the management CLI is up. Result data: the `cli_status` and, for a banner, the
  Varnish `version`. The CLI secret authentication is not performed (liveness only).
- `ceph` (alias `ceph-mon`) — default port 3300 (TCP, the Ceph monitor's messenger
  v2; use port 6789 for the legacy v1). No auth. On connect a Ceph daemon sends a
  messenger banner (`ceph v2\n` for v2, `ceph v027` for v1); reading a `ceph v`
  banner proves it is a Ceph endpoint. Result data: the `messenger` version
  (`v1`/`v2`). The banner precedes the authenticated handshake, so no credentials.
- `glusterfs` (aliases `glusterd`, `gluster`) — default port 24007 (TCP, the
  glusterd management daemon). No auth. An ONC RPC NULL to the GlusterFS handshake
  program over TCP (record marking), like `rpcbind`; result data carries the
  `rpc_status`. **This checks one node.** To alert when **any node** in a cluster
  is down, configure one check per node (one `host` each) — the failing node's
  check fires:

  ```yaml
  checks:
    gluster-n1: { type: glusterfs, host: 10.0.0.1 }
    gluster-n2: { type: glusterfs, host: 10.0.0.2 }
    gluster-n3: { type: glusterfs, host: 10.0.0.3 }
  ```

  Cluster-wide peer status is not gathered in-protocol (it would need authenticated
  GlusterD management RPC).
- `openvswitch` (aliases `ovs`, `ovsdb`, `ovsdb-server`) — default port 6640 (TCP,
  the Open vSwitch configuration database server `ovsdb-server`), or a Unix socket
  via `socket` (commonly `/run/openvswitch/db.sock`); `tls` supported (SSL). No
  auth. Issues an OVSDB (RFC 7047) `list_dbs` JSON-RPC request and verifies a result
  listing the served databases — result data carries the `databases` list. When the
  `Open_vSwitch` database is present it follows up with a `transact` select reading
  `ovs_version`, reported as the `version`.

### SQLite integrity (`sqlite` / `sqlite3`)

A `sqlite` check verifies a local SQLite database file is healthy by running
SQLite's integrity check. It is a **local file** check (not a network protocol).

```yaml
checks:
  app-db:
    type: sqlite
    path: /var/lib/app/app.db   # required
    quick: false                # optional: true runs the faster PRAGMA quick_check
```

It passes (health-style, `OK == true`) when `PRAGMA integrity_check` reports
`ok`. A missing/unreadable file, a file that is not a SQLite database, or
reported corruption fails the check with the detail. The file is opened
**read-only**, so the check never modifies it. `quick: true` runs
`PRAGMA quick_check` (faster, skips some per-row checks) for large databases.

### SQL query (`sql`)

A `sql` check runs a query against a database and compares its **scalar result**
(the first column of the first row) against a `value`. It is **condition-style**
(`OK == true` means the comparison holds), so in rules `active: {check: …}`
fires on it. It uses the same connection fields as the MySQL/PostgreSQL checks
and opens SQLite databases read-only.

```yaml
checks:
  jobs-backlog:
    type: sql
    engine: postgres            # mysql | mariadb | postgres | postgresql | sqlite | sqlite3
    host: 127.0.0.1             # mysql/postgres: host/port/user/password/database/tls
    user: monitor
    password: "${env:PGPASS}"
    database: app
    query: "SELECT count(*) FROM jobs WHERE state = 'queued'"
    op: ">"                     # == | != | > | >= | < | <= | contains | =~
    value: "100"
  schema-version:
    type: sql
    engine: sqlite
    path: /var/lib/app/app.db   # sqlite: a path, opened read-only
    query: "SELECT value FROM meta WHERE key = 'schema'"
    op: "=~"                    # regular expression (Go/RE2)
    value: "^v[0-9]+$"
```

- **Operators:** `>`, `>=`, `<`, `<=` compare numerically (result and `value`
  must parse as numbers); `==` / `!=` compare numerically when both are numbers,
  otherwise as strings (equal/different); `=~` matches the result against `value`
  as a Go (RE2) regular expression.
- **Engines:** `mysql`/`mariadb` and `postgres`/`postgresql` use the same
  connection fields as their protocol checks (`host`/`port`/`user`/`password`/
  `database`/`tls`) and **require a `user`**; `sqlite`/`sqlite3` take a `path`
  and open it **read-only**.
- Result data carries `engine`, `query`, `op`, `threshold`, the raw `result`
  string and, when numeric, a `value` for hooks/rules. A query error, a missing
  database or a `NULL` result fails the check. The check only reads — point it at
  a read-only user.

### MongoDB query (`mongodb-query`)

A `mongodb-query` check runs a MongoDB query, compares a **scalar result** with
`value`, and is **condition-style** (`OK == true` means the comparison holds).
It uses the same connection variables as the `mongodb` connection check
(`host`/`port`/`user`/`password`/`database`/`tls`, plus `auth_source`) and the
official MongoDB driver. Three query shapes are supported:

```yaml
checks:
  failed-jobs:                    # 1) document count
    type: mongodb-query
    host: 127.0.0.1               # host/port/user/password/database/tls/auth_source
    user: monitor
    password: "${env:MGPASS}"
    database: app
    collection: jobs
    filter: '{"status":"failed"}' # optional JSON filter; default {} (count all)
    op: "<"                       # == | != | > | >= | < | <= | contains | =~
    value: "10"
  queued-jobs:                    # 2) aggregation pipeline (scalar at `result`)
    type: mongodb-query
    database: app
    collection: jobs
    pipeline: '[{"$match":{"state":"queued"}},{"$count":"n"}]'
    result: "n"                   # dotted path into the first result document
    op: ">"
    value: "100"
  connections:                    # 3) database command (scalar at `result`)
    type: mongodb-query
    database: app                 # command runs here; defaults to admin
    command: '{"serverStatus":1}'
    result: "connections.current" # dotted path into the reply
    op: "<"
    value: "5000"
```

- **Query shapes** (exactly one): a **`collection`** (+ optional JSON `filter`)
  compares the matching **document count**; a `collection` + JSON **`pipeline`**
  runs an aggregation; a **`command`** runs a database command. `pipeline` and
  `command` extract a scalar at the dotted **`result`** path (a collection count
  needs no `result`). `filter`/`pipeline`/`command` accept relaxed **extended
  JSON** (so `$oid`, `$date`, etc. work). A collection query requires a
  `database`; `command` defaults to `admin`.
- **Operators** behave exactly as the `sql` check's (`>` `>=` `<` `<=` numeric;
  `==`/`!=` numeric-or-string; `contains` substring; `=~` RE2 regexp).
- **Auth:** with a `user`, credentials are checked against `auth_source` (default
  `database`, then `admin`). The check only reads — point it at a read-only user.
- Result data carries `mode`, `op`, `threshold`, the raw `result` and, when
  numeric, a `value` for hooks/rules.

### InfluxDB query (`influxdb-query`)

An `influxdb-query` check runs an InfluxDB query, compares a **scalar result**
with `value`, and is **condition-style** (`OK == true` means the comparison
holds). It uses the `influxdb` connection variables (`host`/`port`/`user`/
`password`/`tls`). The **`language`** selects the query API:

- **`influxql`** (default) — InfluxDB **1.x** `GET /query` against a `database`.
- **`flux`** — InfluxDB **2.x** `POST /api/v2/query` against an `org` with a
  `token`.

```yaml
checks:
  cpu-load:                     # InfluxQL (1.x)
    type: influxdb-query
    host: 127.0.0.1             # host/port/user/password/tls (https when tls set)
    user: monitor               # optional: sent as HTTP Basic auth
    password: "${env:INFLUXPW}"
    database: telegraf          # required for influxql
    query: "SELECT mean(usage_user) FROM cpu WHERE time > now() - 5m"
    op: "<"                     # == | != | > | >= | < | <= | contains | =~
    value: "80"
  disk-flux:                    # Flux (2.x)
    type: influxdb-query
    language: flux
    host: 127.0.0.1
    tls: true                   # InfluxDB 2.x is usually https
    org: my-org                 # required for flux
    token: "${env:INFLUX_TOKEN}" # required for flux (Authorization: Token …)
    query: >
      from(bucket: "telegraf")
        |> range(start: -5m)
        |> filter(fn: (r) => r._measurement == "disk" and r._field == "used_percent")
        |> mean()
    op: "<"
    value: "90"
```

- **Scalar selection.** *InfluxQL* returns rows of `[time, …]`; by default the
  result is the **last column** of the first row of the first series (the
  aggregate value, since `time` is first). *Flux* returns annotated CSV; by
  default the result is the **`_value`** column of the first data row. Set
  **`column`** to read a named column in either mode. A query that matches nothing
  fails the check ("no value").
- **Operators** behave exactly as the `sql` check's (`>` `>=` `<` `<=` numeric;
  `==`/`!=` numeric-or-string; `contains` substring; `=~` RE2 regexp).
- **Auth.** *InfluxQL:* a `user`/`password` is sent as HTTP Basic auth; an
  optional `token` (1.8+/2.x compatibility) is sent as `Authorization: Token …`
  and takes precedence. *Flux:* the `token` is required. The check only reads —
  point it at a read-only user/token.
- Result data carries `language`, `query`, `op`, `threshold`, the `database`/`org`
  in use, the raw `result` and, when numeric, a `value` for hooks/rules. A query
  error (e.g. unknown database, bad token) fails the check.

### Size growth (`size`)

A `size` check watches a file or directory and **alerts when it grows** by at
least `grow_by` within the `within` window — useful to catch a runaway log, a
disk-filling spool or a leaking cache. Only **increases** trip it: a steady or
shrinking path passes. It is **condition-style** (`OK == true` means "grew too
fast", so `active: {check: …}` fires) and **stateful**. Growth history persists
while the service worker or watch is alive and resets when that worker/watch is
rebuilt, for example after a config reload.

```yaml
watches:
  log-runaway:
    check:
      type: size
      path: /var/log/app.log   # a file, or a directory (recursive sum of file sizes)
      grow_by: 1GB             # alert if it grows at least this much…
      within: 1h               # …within this sliding window
    then:
      notify: [ops-email]      # and/or a hook
```

(The examples in this file use compact global `watches:` maps. In a file under
`paths.watches`, write the same watch as a `name: log-runaway` document and keep
the inner fields at the top level.)

(Note `within` here is the size check's **own field** — the duration of its
growth window — not the watch-level `within: {cycles|duration, min_matches}`
firing window, which a size watch normally does not need.)

Each cycle it samples the path's size (a file's bytes, or the recursive sum of
regular-file sizes under a directory), keeps the samples seen in the last
`within`, and compares the current size against the oldest one still in the
window. It fails when `current − baseline ≥ grow_by`. The first cycle only
baselines (no alert). `grow_by` uses the same size grammar as every other size
field (`free_bytes`, `expand.by`): an explicit `K`/`M`/`G`/`T` suffix (optional
`B`/`iB`), binary units (`1G` = 2³⁰), with plain byte counts rejected. Result
data carries `current_bytes`, `baseline_bytes`,
`growth_bytes`, the `window` and `value` (the growth) for hooks/rules. A
directory walk skips hidden descendants by default; set `include_hidden: true` to
include them. A hidden path named directly is always sampled. Point it at a bounded
path.

### WebSocket (`websocket`)

A `websocket` check verifies a WebSocket endpoint completes the RFC 6455 opening
handshake: it sends the HTTP `Upgrade` request and checks the server answers
`101 Switching Protocols` with a `Sec-WebSocket-Accept` matching the sent key
(so it confirms a real WebSocket server, not just any HTTP 101).

```yaml
checks:
  realtime:
    type: websocket
    url: "wss://example.com/socket"   # ws:// | wss:// | http:// | https://
    # tls: skip-verify                # accept a self-signed cert (wss/https)
    # origin: "https://example.com"   # optional Origin header
    # subprotocol: "chat"             # optional Sec-WebSocket-Protocol
    # headers: { Authorization: "Bearer ${token}" }   # optional extra headers
```

It passes (health-style, `OK == true`) when the handshake completes. `ws`/`http`
connect in plaintext; `wss`/`https` use TLS (`tls: skip-verify` accepts a
self-signed certificate). The default port follows the scheme (80 / 443) unless
the URL gives one. Result data carries the negotiated `subprotocol`. Probed
natively (no external library).

```yaml
checks:
  db:
    type: mysql                 # any protocol from the supported-types table above
    # Auth depends on protocol: postgres requires user; mysql/mongodb can probe without it;
    # redis/imap/pop/smtp may be anonymous; fpm/dns/amqp use no auth.
    host: 127.0.0.1             # default 127.0.0.1
    port: 3306                  # default: the protocol's port (mysql 3306, postgres 5432)
    user: monitor               # optional/required by protocol
    password: "${env:DB_PASS}"  # resolved from the environment at load (never store secrets in plaintext)
    database: ""                # optional
    tls: false                  # optional (see per-protocol values above)
    timeout: 5s                 # optional (engine.default_timeout)
```

It passes (health-style, `OK == true`) when it connects, authenticates as
`user`, and the server answers a ping. Result data exposes `protocol`, `host`,
`port` and the server `version`. A network/auth failure fails the check with the
error. In service/catalog profiles, add it as a check-only `watches:` entry; use
explicit `checks:` when a hand-written rule must share the same probe.

**Response comparisons (`expect`).** Any protocol check can assert the values
its probe returns — the server `version` or any field the protocol puts in its
result data (e.g. `answers`/`rcode` for `dns`, `stratum`/`offset_seconds` for
`ntp`, `sys_object_id` for `snmp`, `offered_ip`/`lease_seconds` for `dhcp`,
`ipp_version` for `ipp`, …). `expect` is a mapping of field → value (equality) or
field → `{op, value}` using the shared operators `== != > >= < <=` (numeric, or
string for `==`/`!=`), `contains` (substring) and `=~` (Go/RE2 regex). All
assertions must hold, **in
addition** to the probe succeeding:

```yaml
checks:
  resolver:
    type: dns
    host: 1.1.1.1
    query: example.com
    expect:
      rcode: NOERROR                 # equality (scalar)
      answers: { op: ">", value: 0 } # operator comparison
  clock:
    type: ntp
    host: pool.ntp.org
    expect:
      stratum: { op: "<=", value: 3 }
```

A referenced field the probe did not return fails the check with a clear
message. The same comparison operators work for every registered protocol field.

**Response latency (`expect_latency`).** Any protocol check also accepts
`expect_latency: { op, value }` (milliseconds), like the `http` check — it fails
when the probe's response time does not satisfy the comparison. Result data
always carries `latency_ms`:

```yaml
checks:
  cache:
    type: redis
    password: "${env:REDIS_PASS}"
    expect_latency: { op: "<", value: 50 }   # alert when Redis answers slowly
```

**Version-change detection (`on_version_change`).** Set `on_version_change: true`
on a service check or host watch to alert when the server's version changes
between cycles — e.g. after a package upgrade. The tracked identity is the
protocol's reported `version` — for connection-protocol checks such as `mysql`,
`postgres`, `redis`, `ssh`, `snmp`, `rspamd`, `libvirt` or `syncthing` — or, for
protocols that only return a greeting banner (such as `smtp`, `imap`, `pop`,
`ftp`), that banner. Any registered connection protocol that reports a version or
banner participates; these names are examples, not the full set. The first cycle baselines silently;
a later change **fails** the check and the result data carries
`version`/`version_old`. The baseline lives in the check instance, so it persists
while the service worker or watch is alive and resets when that worker/watch is
rebuilt, for example after a config reload. It composes with `on_change` (the
SSH/SNMP fingerprint identity) — both can be enabled at once.

```yaml
watches:
  mail-version:
    monitor: disabled
    check:
      type: smtp
      host: mail.example.com
      on_version_change: true   # alert when the SMTP banner/version changes
      expect_latency: { op: "<", value: 500 }
    then:
      notify: [ops-email]
```

More protocols are added the same way — the check type, dispatch and validation
are protocol-agnostic, so a new protocol only registers itself.

### Clock drift (`clock`)

The `clock` check measures this host's wall-clock offset by querying one or more
remote NTP servers as a client. It does **not** require a local NTP daemon and it
does **not** set the system clock itself; use the alert/hook path to notify or to
run an operator-owned sync script.

```yaml
watches:
  clock-drift:
    monitor: disabled
    interval: 5m
    check:
      type: clock
      servers:
        - time.cloudflare.com
        - pool.ntp.org
      max_offset: 2s
      max_stratum: 4              # optional, default 15
      max_root_dispersion: 250ms  # optional
      timeout: 3s
    for: { cycles: 2 }
    then:
      notify: [ops-email]
      hook:
        command: [/usr/local/sbin/sermo-sync-clock.sh]
        timeout: 2m
        expect_exit: 0
```

`servers` and `max_offset` are required. The check tries the servers in order and
passes when one server answers with synchronized NTP data whose absolute
`offset_seconds` is within `max_offset`, whose `stratum` is at most
`max_stratum`, and whose `root_dispersion_ms` is within
`max_root_dispersion` when that ceiling is configured. Result data carries the
selected `server`, `port`, `offset_seconds`, `offset_abs_seconds`, `stratum`,
`leap`, `precision_seconds`, `root_delay_ms`, `root_dispersion_ms` and
`reference_id`; hooks receive the same values as `SERMO_*` environment fields.

Every type above is a **single-shot check** (`Check.Run → Result`) and is usable in
**both** places:

- a service's check-only `watches:` entries, or explicit `checks:`/`preflight:` referenced from rules,
- a host **watch** document (or global `watches:` entry, firing a hook) — see [configuration](configuration.md#host-watches), and
- a service's own embedded `watches:` block (hook/notification entries scoped to the service, or compact `then.action`, including the service-scoped `service`/`metric` types and the PID-tree-scoped `process_count`) — see [Service watches](configuration.md#service-watches-scoped-to-a-service).

The host-resource checks (`storage`, `load`, `memory`, `pressure`, `fds`, `pids`,
`diskio`, `hdparm`, `sensors`, `smart`, `raid`, `edac`, `conntrack`, `entropy`,
`zombies`, `oom`, among others) are
condition-style — `OK == true` means there is a problem — so in rules
`active: {check: x}` fires on it, and as a watch the hook fires on it.
The health checks (`tcp`, `ports`, `http`, `command`, `service`, `file_exists`,
`file`, `lockfile`, `binary`, `pidfile`, `socket`, `process`, `libraries`, `config`,
`autofs`, `route`, `clock`, `firewall_rules`, `cert`, `sqlite`/`sqlite3`,
`websocket`, and connection-protocol checks such as `mysql`/`smtp`) are the
opposite (`OK == true` is healthy), so as a watch they fire the hook on
**failure**.

The multi-metric watches (`net`, `icmp`, `swap`) keep their `metrics:` map shape
(one hook per metric) watch-only, but their **single-metric form** — an explicit
`metric:` field producing one result, e.g. `{type: net, interface: ppp0, metric:
state, expect: up}` or `{type: icmp, host: 1.1.1.1, metric: state, expect: up}` —
works as a service check-only watch or explicit `checks:` entry (used by the
`pppd` catalog daemon to watch its uplink). The multi-target watches (`file`, `process`, one
event/hook per changed path or matching pid) stay watch-only.
`service`/`metric`/`process` checks need per-service context (backend status, a
metric sampler, process discovery) and so are not available as standalone
watches.

### Default route (`route`)

The `route` check verifies the kernel has an **up default route** — read
natively from `/proc/net/route` (IPv4, the default) or `/proc/net/ipv6_route`
(`family: ipv6`). With `interface`, a default route must egress through that
interface. It is a health check (OK means the route is there); as a watch it
fires when the route disappears.

It closes the uplink gap the link and ping layers leave: after a failed PPP
renegotiation the interface can stay `up` with the default route gone, and a
ping bound to the interface cannot tell "no route" from "provider down". The
`pppd` catalog service layers all three (`net` state, `route`, `icmp`).

```yaml
checks:
  route:
    type: route          # IPv4 by default; family: ipv6 for the v6 table
    interface: ppp0      # optional: the default route must leave through ppp0
```

The result reports the matched egress interface and gateway (when the route
has one — point-to-point links have none) in its data, and `value` carries the
number of matching default routes.

### Firewall rules (`firewall_rules`)

The `firewall_rules` check verifies that nftables or iptables rules are loaded.
It is health-style: a service or watch fails when the rule count is below
`min_rules` (default `1`). `backend: auto` tries nftables first (read via
netlink, no `nft` binary required) and falls back to iptables/ip6tables.

```yaml
checks:
  service: { type: service, expect: active }
  firewall:
    type: firewall_rules
    backend: auto        # auto | nftables | iptables
    min_rules: 1
    requires: [service]  # useful for oneshot firewall loaders
```

As a watch, it fires the hook when the firewall rules disappear. Hook extras:
`SERMO_BACKEND`, `SERMO_RULES`, `SERMO_MIN_RULES`.

### Disk throughput (`hdparm`)

The `hdparm` check times a disk's read throughput and alerts when it crosses a
threshold — useful to catch a **gradually degrading** drive. It runs `hdparm` on
`device` and exposes two MB/s values: **`read`** (buffered disk reads, `hdparm -t`
— the real device speed) and **`cached`** (cached reads, `hdparm -T` — memory/cache
throughput). Predicates are `{op, value}` in **MB/s**; at least one of `read`/
`cached` is required, and **only the timings a predicate needs are run** (a
`cached`-only check skips the slow buffered pass).

```yaml
watches:
  disk-speed:
    interval: 24h                       # hdparm -t reads ~3s and adds I/O — run rarely
    check:
      type: hdparm
      device: /dev/sda
      timeout: 30s                      # give the benchmark room
      read:   { op: "<", value: 100 }   # alert when buffered reads drop below 100 MB/s
      cached: { op: "<", value: 3000 }  # optional: cache/memory throughput
    then:
      notify: [ops-email]
```

`hdparm` is **condition-style**: a predicate expresses the *alerting* condition
(e.g. `read < 100`), so the watch hook/notify fires when it holds.
**`hdparm` needs root** (raw device access); without it the
check fails with hdparm's error. Because `-t` reads from the platter for a few
seconds and adds real I/O load, schedule it on a **long `interval`** (e.g. `24h`)
with a generous `timeout`. The measured `read`/`cached` are placed in the result
data (and the `SERMO_READ`/`SERMO_CACHED` hook variables), and are **recorded as a
time series and graphed** in the service detail (web UI) so you can spot **gradual
degradation** of a drive over time. (This per-check named-metric graphing is
generic: any check that publishes numeric `Result.Data` fields can opt in.)

### Hardware sensors

Physical-health checks are **condition-style**: predicates are alerting
conditions. Numeric values are **recorded over time and graphed** in the service
detail so gradual degradation is visible.

- **`sensors`** — lm-sensors-style **hwmon** inputs (no external tool; reads
  `/sys/class/hwmon`). Aggregates: `temp` (the hottest matching temperature, °C),
  `fan` (the slowest matching fan, RPM — catches a stalled fan) and `voltage` (the
  lowest matching rail, V). At least one predicate is required; optional `chip` and
  `label` substrings narrow which inputs count.

  ```yaml
  checks:
    cpu-temp:
      type: sensors
      chip: coretemp                 # optional: only this chip
      temp: { op: ">", value: 85 }   # alert when the hottest core exceeds 85 °C
      fan: { op: "<", value: 400 }   # optional: alert on a stalled fan
  ```

- **`smart`** — a drive's **SMART** health via `smartctl -j` (needs smartmontools
  and root). With no predicate it alerts when the overall SMART verdict is
  **FAILED**; predicates add `temperature` (°C), `reallocated` (sector count, an
  HDD failure sign), `wear` (SSD/NVMe percentage used) and `power_on_hours`.
  Complements `hdparm` (throughput) with failure prediction.

  ```yaml
  checks:
    ssd-health:
      type: smart
      device: /dev/nvme0
      interval: 1h
      reallocated: { op: ">", value: 0 }   # any reallocated sector
      wear: { op: ">", value: 90 }         # SSD/NVMe nearly worn out
  ```

- **`raid`** — Linux **md software-RAID** from `/proc/mdstat` and read-only
  `/sys/block/md*/md` data (native). With no predicate it alerts when any
  array is **degraded**; predicates add `degraded`, `recovering` and `arrays`
  counts. `array: md0` scopes the check to one array. With `sysfs_changes: true`,
  Sermo tracks `mismatch_cnt` and each member's `state`, `errors` and
  `bad_blocks` between cycles. A host with no md arrays never alerts.

  ```yaml
  checks:
    raid: { type: raid }                   # alert if any md array is degraded
  ```

  A RAID host watch can filter its `then.notify` targets to lifecycle transitions
  with `then.notify_on`: `on_degraded`, `on_recovering`, `on_good` (repair
  complete) and `on_array_change`. These notifications receive `SERMO_RAID_EVENT`,
  `SERMO_RAID_ARRAY`, operation/progress and, for sysfs changes, member, field,
  old and new values; notifier templates can use those fields to render a
  different message.

  A host watch can additionally opt into manual reconstruction pause/resume
  with `raid_control: { pause_resume: true }`; see
  [Manual RAID reconstruction control](configuration.md#manual-raid-reconstruction-control).

- **`lvm`** — Linux LVM health and capacity from read-only `lvs` JSON. It is a
  health check: `ok` means the selected VG/LV is usable; `error` covers an absent,
  partial or suspended LV, or a configured capacity threshold. Select a target
  with `volume_group` and optional `logical_volume`; `free_pct`, `thin_data_pct`
  and `thin_metadata_pct` are ordinary numeric predicates. Result readings
  include `health`, `volume_group`, `logical_volume`, `lvm_reasons`,
  `vg_free_bytes`, `vg_size_bytes`, `vg_used_bytes` and the configured
  percentage fields.

  ```yaml
  watches:
    lvm-vg0-root:
      check:
        type: lvm
        volume_group: vg0
        logical_volume: root
        thin_data_pct: { op: ">=", value: "80%" }
      then:
        notify: [ops]
        notify_on: [on_change]
  ```

  `on_change` is LVM-only and sends one notification when the effective health
  changes `ok → error` or `error → ok`; it does not notify repeatedly while an
  error persists. Templates receive VG/LV, current and previous states, current
  reasons and recovered reasons.

- **`edac`** — **ECC memory errors** from the kernel EDAC subsystem (native,
  `/sys/devices/system/edac`). `ce` is the cumulative correctable count and `ue`
  the uncorrectable count; with no predicate it alerts on `ue > 0`. The check fails
  when the platform exposes no EDAC controllers (so you notice ECC isn't reported).

  ```yaml
  checks:
    ecc:
      type: edac
      ce: { op: ">", value: 100 }          # also alert on many correctable errors
  ```

### Autofs

The `autofs` check verifies the autofs **automounter** (`automount`) is active.
autofs has no socket or port — the daemon talks to the kernel over an internal
pipe — so the liveness signal is the **mount table**: while `automount` runs it
maintains its configured map roots as `autofs`-type mountpoints in
`/proc/mounts` (they disappear when the daemon stops). Unlike `storage`/`count`,
this is a **health** check: it passes (OK) when the automounter is active as
configured, and fails when it is not.

```yaml
checks:
  automounter:
    type: autofs                  # with no path/count: require >= 1 autofs mountpoint
  home-automount:
    type: autofs
    path: /home                   # require this exact autofs mountpoint to be active
  all-maps:
    type: autofs
    count: { op: ">=", value: 3 } # require at least 3 autofs mountpoints
```

- With **no `path` and no `count`**, the check passes when at least one autofs
  mountpoint is present (the automounter is running with maps).
- **`path`** requires that exact mountpoint to be an active autofs mount.
- **`count` `{op, value}`** compares the number of autofs mountpoints (`op` is one
  of `>=, >, <=, <, ==, !=`). `path` and `count` are mutually exclusive.
- Result data carries the `count` of autofs mountpoints and the comma-joined
  `mountpoints`. On-demand mounts triggered by access appear under these roots as
  their real filesystem (e.g. `nfs`), not as `autofs`, so they are not counted —
  the check tracks the map roots, i.e. that the automounter itself is up.

### Count

A `count` check tallies the entries in a directory and either compares the total
to a threshold, or alerts when the total grows by a `delta` within a time
window. It is **condition-style** (`OK == true` means the comparison holds), so
in rules `active: {check: …}` fires when the comparison holds and
`failed: {check: …}` fires when it does not.

```yaml
checks:
  spool-backlog:
    type: count
    path: /var/spool/myapp        # required: directory to scan
    of: file                      # any (default) | file | dir | symlink
    recursive: false              # optional, default false
    include_hidden: false         # optional, default false for recursive scans
    op: ">"                       # >=, >, <=, <, ==, !=
    value: 1000                   # numeric threshold
```

```yaml
checks:
  spool-growth:
    type: count
    path: /var/spool/myapp
    of: file
    delta: { op: ">", value: 200 } # alert if the count grows by >200…
    within: 2m                     # …within this sliding window
```

- **`of`** selects which entries are counted. Entries are classified by their own
  type without following symlinks, so a symlink counts as `symlink` (never as the
  file or directory it points to); `any` counts every entry.
- **`recursive: true`** descends the whole subtree (the directory itself is never
  counted); unreadable subdirectories are skipped. Hidden descendants (names starting
  with `.`) and their subtrees are skipped by default; set `include_hidden: true` to
  count them. Default counts only the immediate entries.
- A missing or unreadable `path` makes the check fail. The observed total is
  exposed in the check's result data as `count`.
- The threshold may also be written as a nested predicate —
  `count: { op: ">", value: 1000 }` — matching the `{op, value}` form the other
  checks use. Use one form or the other, not both.
- **`delta` + `within`** is stateful. Each cycle samples the count, keeps samples
  in the last `within`, and compares the current count against the oldest sample
  still in the window. The first cycle only baselines (no alert), and only
  increases can trip the check; steady or shrinking directories pass. Result data
  carries `count`, `baseline_count`, `growth_count`, `window` and `value` (the
  growth). Use either `count`/`op`/`value` or `delta`/`within`, not both.

## Metrics

Service metrics measure the discovered process set; system metrics measure the
machine. `value` is a number with an optional trailing `%`.

```
scope: service   memory, swap, cpu, cpu_thread, process_count, io, io_read, io_write, fds, threads
scope: system    total_memory, total_swap, total_cpu, load1, load5, load15
```

**A `scope: system` metric may only drive `alert` rules, never remediation.** It
describes the whole machine, not one service, so a `restart`/`start`/`stop` rule
that reads a system metric — directly, or through a `failed`/`active` reference
to a `type: metric, scope: system` check — is dropped at config load with a
warning. This is a safety invariant: a system-wide signal must never act on an
individual service (see [docs/safety.md](safety.md)).

**Service metrics sum across the whole discovered process tree** — the matched
processes *and* their child/descendant processes — so a service's `cpu`,
`memory`, `io`, `fds`, etc. account for its workers and helpers, not just the
main process. `io`/`io_read`/`io_write` are byte/second rates over actual
block-layer I/O (`io` is read+write); `fds` is the open file-descriptor count and
`threads` the thread count.

`memory` is the summed **RSS** (resident memory) of the process tree, as bytes
and as a percentage of total RAM. `swap` is the summed **swapped-out** memory
(`VmSwap`) of the tree, as bytes and — when a swap device exists — as a
percentage of total swap; it is reported only on hosts where swap accounting is
readable.

The `cpu` percentage is the service's summed CPU time (parent + children) over
the elapsed wall-clock, **normalized by the server's total logical CPUs** (the
hardware threads, counted from `/proc/stat` so the figure reflects the whole
machine even if Sermo is pinned to a CPU subset). So `100%` means the service's
processes are saturating every CPU thread of the server, and a single fully-busy
core on an 8-thread host reads `~12.5%`. `total_cpu` uses the same whole-machine
basis.

`cpu_thread` complements `cpu` for the **single-thread** case: it is the **busiest
single process** in the tree (parent or any child) measured against **one** CPU
thread, so `100%` means one process is saturating a full core. Because the
whole-machine `cpu` dilutes a single hot process across all cores (a core-bound
process on an 8-thread host shows only `~12.5%` there), `cpu_thread` is what you
alert on to catch a process — especially a single-threaded one — pegging its
thread: `metric` `scope: service`, `metric: cpu_thread`, `op: ">"`, `value:
"90%"`. A multi-threaded process spanning several cores can read above `100%`.
`cpu_thread` is a rate, so it is not ready on the first cycle.

`cpu`/`cpu_thread`/`total_cpu` and the `io*` metrics are rates: they are **not
ready** on the first cycle and a condition over a not-ready value is false. A `%`
threshold needs a metric with a percentage form (`memory`, `swap`, `cpu`,
`cpu_thread`, `total_memory`, `total_swap`, `total_cpu`;
`swap`/`memory`/`total_memory`/`total_swap` also have an absolute byte form); a bare number needs an absolute form (everything else, including
`io*`/`fds`/`threads`, which are absolute only). Reading another process's I/O or fd count
requires privilege, so those sum only the processes the daemon can read.

## Rules

Rule evaluation is deterministic and order-independent: guards always run
before remediation, at most **one remediation action runs per service per
cycle**, and when several remediation rules fire at once they are considered
in sorted name order — the first non-blocked action wins. Every declared check
and inline condition probe runs **at most once per cycle**; rules read the
cached results.

```yaml
rules:
  RULE_NAME:
    type: remediation | guard | alert
    if: { ... }       # condition tree
    for: { cycles: 3 }            # consecutive cycles (optional)
    # for: { duration: 6m }        # or consecutive wall-clock time
    within: { cycles: 15, min_matches: 5 }  # sliding window (optional)
    # within: { duration: 30m, min_matches: 3 } # or a time window
    notify: [ops-email]           # who gets this rule's alert messages (optional)
    emission: { events: on_change, notify: on_change } # or every_cycle
    then: { action: alert, message: "http is down" }
```

A rule's **`notify`** selects which notifiers receive its `alert` messages,
overriding the global default ([Notifications](configuration.md#default-selection-and-precedence)):
an explicit list wins, `notify: none` suppresses, and omitting it inherits the
global `notify` default. It applies to the rule's alert messages; remediation
operations are reported as events, not notifications. By default those automatic
alert events and notifications are emitted only when the rule enters a firing
episode, then `recovered` is emitted when it clears. Use rule-level
`emission.events` or `emission.notify` (`on_change` | `every_cycle`) to override
the global emission policy for that rule. Operation result events remain audit
events and are recorded whenever the operation is attempted.

For a recovered rule with exactly one direct check or metric leaf, the event also
records the current formatted value and its configured operator and threshold.
Byte values use `B`, `KB`, `MB`, `GB` or `TB` (and byte rates add `/s`), including
the configured threshold. This makes threshold flapping visible without having
to reconstruct the sample from the metrics history.

Actions and types are coupled: the operation actions (`restart`, `start`,
`stop`, `reload`, `resume`) belong to `type: remediation` rules — required there (a
notify-only rule is `type: alert`) and rejected elsewhere. `alert` (with a
`message`) may accompany any rule's actions; `block` is guard-only. A `then`
may carry one `action` or an `actions` list (e.g. alert + restart together).
Those operations use the same safety engine as manual CLI/Web actions, including
the active-service exact process identity gate before `restart`.

Conditions form a logical tree with `and`/`or`/`not` and leaves:

```yaml
if:
  or:
    - failed: { check: http }      # a named check failed
    - active: { check: backup-flag } # a named check passed
    - file: { path: /run/x, exists: true }
    - command: { user: postgres, command: ["/usr/local/bin/can-restart", "${service}"], timeout: 5s, expect_exit: 0 }
    - service: { state: active }
    - process: { exe: /usr/bin/mysqld, user: mysql, state: running }
    - metric: { scope: service, name: cpu, op: ">", value: 30% }
    - changed: { path: /lib64/libc.so.6 }  # the file changed since the last cycle
    - changed: { app: containerd, level: patch }  # app version changed
```

`command` is a direct condition leaf whose truth is the same as a command check:
exit status/output expectations pass. It must use array argv form and declare a
`timeout`; `user` is available with the same meaning as on a command check. It is
run without a shell, cached for the cycle like other inline probes, and must be
side-effect-free. `failed`/`active` may also take an inline
probe (`tcp`, `command`, ...) instead of a `check:` reference when you need the
named success/failure polarity.

`changed` is true when the file at `path` differs (size/mtime) from the baseline
tracked across artifact samples, or when `app` names a linked app whose version
changed at the selected `level` (`major`, `minor` or `patch`; default `patch`).
The first cycle adopts the current value (a daemon start never fires), and a
successful `restart`/`start` re-baselines it. A failed app version command is an
invalid sample: it does not fire and does not update the baseline. The `path`
form is the primitive behind `restart_on_change.libraries` (see Services →
Library services); the `app` form is the primitive behind
`restart_on_change.apps` for service-owned binaries such as `containerd`.
For service paths, linked apps and catalog libraries, the samples run at
`engine.artifact_interval` (or their local `interval`), not on every service cycle.

### Windows

Without `for`/`within`, a rule fires the cycle its condition is true.
`for` is consecutive: `for: {cycles: N}` requires N consecutive true cycles,
while `for: {duration: 6m}` requires the condition to stay true for at least
that wall-clock duration. `within` is a rolling window:
`within: {cycles: N, min_matches: M}` requires M true cycles out of the last N,
while `within: {duration: 30m, min_matches: M}` requires M true observed cycles
inside the last 30 minutes. `min_matches` is optional and defaults to `1`
(true at least once within the window). A rule cannot use both `for` and
`within`; a single window must choose either `cycles` or `duration`, not both.

Service rule-window progress is persisted in `paths.state`. If `sermod`
restarts while a `for` window is at 2/3 consecutive matches, the next observed
matching cycle continues from 2/3 instead of starting from zero. Duration-based
windows persist their timestamps too, so a restart does not restart a pending
`for: {duration: ...}` window.

### Guards

Guard rules block unsafe actions and use `action: block` with a `message`:

```yaml
block-during-backup:
  type: guard
  blocks: [restart, stop]
  if: { file: { path: /run/backup/in-progress, exists: true } }
  then: { action: block, message: "Backup is running" }
```

The shipped MySQL, MariaDB and PostgreSQL catalog services include a default
optional `backup` process check and a
`block-restart-during-backup` guard. The check matches common local backup
tools by exact resolved executable path (`exe_any`) and database backup user
(`backup_user`, defaulting to `mysql` or `postgres`). Override that check locally
when your backup runs under another user or from non-standard paths. If a
logged-in terminal user runs `sermoctl restart` while this backup guard blocks
the action, Sermo also sends that user a best-effort native TTY notice; cron and
other non-interactive runs are not notified.

The examples
[`examples/services/mariadb-backup-guard.yml`](../examples/services/mariadb-backup-guard.yml)
and
[`examples/services/mysql-wal-g-backup-guard.yml`](../examples/services/mysql-wal-g-backup-guard.yml)
show the same shape for extra app-linked tools or site-specific overrides. The
`apps:` list is an override, so a service that adds a backup app must keep the
database app too, for example `apps: [mysql, wal-g-mysql]` or
`apps: [mariadb, wal-g-mysql]`.

For PostgreSQL site-specific WAL-G paths, use the concrete materialized catalog
daemon and app for the installed version (for example `postgres-16`) and add
`wal-g-pg`:

```yaml
name: postgres-main
uses: postgres-16
apps: [postgres-16, wal-g-pg]

checks:
  wal-g-pg:
    type: process
    optional: true
    exe_any: ["${wal_g_pg_binary}", /usr/local/bin/wal-g-pg]
    user: postgres
    state: running

rules:
  block-restart-during-wal-g-pg:
    type: guard
    blocks: [restart, stop]
    if:
      active:
        check: wal-g-pg
    then:
      action: block
      message: "${display_name} WAL-G backup is running"
```

Guards are evaluated before remediation; a remediation action that a guard
blocks never runs.

`message:` strings may use runtime built-ins. `${date}` is the current RFC3339
timestamp, `${event}` is the firing rule's name and `${action}` is the rule's
primary action. `${rule.duration}` is the configured rule span (`10m`,
`3 cycles`, or `current cycle`) and `${rule.window}` is the fuller window
description (`for 10m`, `within 15 cycles (min 3)`, `immediate`). `${service}`
and `${host}` are resolved during configuration.

For rules whose condition has exactly one direct check or metric leaf, alert
messages may also use `${check.name}`, `${check.type}`, `${check.metric}`,
`${check.scope}`, `${check.op}`, `${check.threshold}` and `${check.value}`.
Complex conditions with multiple checks leave those `${check.*}` values empty
instead of guessing which check should describe the alert.
Byte-valued metric placeholders use the same `B` through `TB` presentation as
recovery events, for both the current value and the threshold.

For rules driven by a `changed:` leaf, alert messages may use
`${change.path}`, `${change.library}`, `${change.app}`, `${change.level}`,
`${change.old_version}` and `${change.new_version}`. Version old/new values are
filled for `changed: {app: ...}` rules.

```yaml
rules:
  alert-if-memory-high:
    type: alert
    if:
      active:
        check: memory-high
    for:
      duration: 10m
    then:
      action: alert
      message: >-
        During ${rule.duration}, ${service} ${check.metric} stayed above
        ${check.threshold} (current ${check.value}) at ${date}
```

## Remediation policy

```yaml
policy:
  cooldown: 5m
  max_actions: 5
  max_actions_window: 1h
  backoff: { initial: 1m, factor: 2, max: 30m }
```

Policy gates *automatic* remediation (only `sermod`, never manual `sermoctl`
actions): an action is suppressed within `cooldown` (extended by `backoff`
after consecutive remediations) or once `max_actions` is reached in the window.
`for`/`within` decide *when* a rule fires; policy decides whether it may act
*now*.

`backoff` grows the effective cooldown after each consecutive remediation:
`initial` the first time, then multiplied by `factor` each subsequent time,
capped at `max`. `factor` **defaults to `2`** when omitted (or set to ≤0).
Automatic-remediation state is also persisted in `paths.state`: `LastActionAt`,
recent action timestamps used by `max_actions`, and the current backoff survive a
`sermod` restart, so restarting the daemon does not bypass cooldown or rate
limits.

Use `dry_run: true` on a service (or in `defaults`) when you want remediation
rules to evaluate windows, guards and policy without executing the resulting
start/stop/restart/reload/resume operation. It emits `dry-run` events and does
not advance live remediation cooldown state. Dry-run also suppresses automatic
rule notifications except `wall`; manual operator actions are unaffected. See
[configuration](configuration.md#host-watches) for examples covering watches and
global defaults.
