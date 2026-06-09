# Checks, conditions and rules

## Checks

Checks are single-shot probes under `checks` (and `preflight`/`postflight`,
which reuse the same schema). MVP types:

| type          | passes when                                                        |
|---------------|--------------------------------------------------------------------|
| `tcp`         | a TCP connection to `host:port` succeeds                           |
| `ports`       | a set of `host` ports satisfy an open/closed expectation (see Ports)|
| `http`        | the response matches `expect_status` (and optional headers/body/JSON, see HTTP)|
| `command`     | the command exits with `expect_exit` (default 0), array form only  |
| `service`     | the backend status equals `expect` (active/inactive/failed/unknown)|
| `file_exists` | a foreign flag/lock file exists (never under `<runtime>/locks`)     |
| `binary`      | a path exists and is executable                                    |
| `libraries`   | `ldd <binary>` resolves all shared libraries                       |
| `process`     | a process matching `exe`/`user` is in `state` (running/zombie/absent)|
| `metric`      | a sampled metric satisfies `op value` (see Metrics)                |
| `count`       | the number of entries in a directory satisfies `op value` (see Count)|
| `disk`        | a filesystem's space/inode predicates hold (used_pct/free_pct/inodes_*)|
| `autofs`      | the autofs automounter is active (autofs mountpoints present — `path`/`count`) (see Autofs)|
| `load`        | a load-average threshold holds (load1/load5/load15, optional per_cpu)|
| `fds`         | system file descriptors vs `fs.file-max` (used_pct/free/allocated)  |
| `conntrack`   | the netfilter conntrack table vs its max (used_pct/free/count)      |
| `entropy`     | available kernel entropy satisfies `avail {op, value}`              |
| `zombies`     | the count of zombie processes satisfies `count {op, value}`         |
| `oom`         | the kernel OOM-kill count rose by `delta {op, value}` since last cycle|
| `cert`        | a TLS certificate is expiring/invalid, or its algorithm/issuer changed (see Cert)|
| `mysql` / `mariadb` | a connection to a MySQL/MariaDB server authenticates and responds (see Database) |
| `mongodb` / `mongo` | a connection to a MongoDB server authenticates, pings and reports its version (see Database) |
| `postgres` / `postgresql` | a connection to a PostgreSQL server authenticates and responds (see Database) |
| `redis` / `valkey` | a connection to a Redis/Valkey server authenticates and answers PING (see Database) |
| `imap`        | an IMAP server greets OK (anonymous) and, with credentials, LOGIN succeeds (see Database) |
| `pop` / `pop3` | a POP3 server greets +OK (anonymous) and, with credentials, USER/PASS succeeds (see Database) |
| `smtp`        | an SMTP server greets 220 + EHLO (anonymous) and, with credentials, AUTH PLAIN succeeds (see Database) |
| `nntp` / `nntps` | an NNTP server greets 200/201 (anonymous) and, with credentials, AUTHINFO USER/PASS succeeds (see Database) |
| `ftp`         | an FTP server greets 220 (anonymous) and, with credentials, USER/PASS login succeeds (see Database) |
| `ssh`         | an SSH server completes key exchange (anonymous: host key + banner); with credentials, login succeeds; `on_change` alerts on host-key change (see Database) |
| `fpm` / `php-fpm` | a PHP-FPM pool answers a FastCGI `/ping` with `pong` (Unix socket or TCP, see Database) |
| `dns`         | a DNS server answers a query (NOERROR/NXDOMAIN) for `query` (see Database) |
| `ntp`         | an NTP server answers with a synchronized time (server mode, stratum 1–15) (see Database) |
| `snmp`        | an SNMP agent answers a system GET (v2c community or v3 user/password); `on_change` alerts on device-identity change (see Database) |
| `tftp`        | a TFTP server answers an RRQ with a valid packet (DATA or ERROR) (see Database) |
| `ldap`        | an LDAP directory accepts an anonymous bind, or a simple bind with credentials (see Database) |
| `ajp`         | an AJP13 connector (e.g. Tomcat's 8009) answers a CPing with CPong (see Database) |
| `ipp` / `cups` | an IPP server (CUPS/cupsd) answers an IPP request with a valid response (see Database) |
| `rsync` / `rsyncd` | an rsync daemon sends its `@RSYNCD:` greeting (see Database) |
| `dhcp` / `dhcpd` | a DHCP server answers a DHCPDISCOVER with a DHCPOFFER (see Database) |
| `rspamd`      | an rspamd worker answers `GET /ping` with `pong` (see Database) |
| `libvirt` / `libvirtd` | a libvirt daemon answers RPC (opens a connection and reports its version) (see Database) |
| `dbus`        | a D-Bus daemon completes the auth/Hello handshake and answers `GetId` (see Database) |
| `avahi` / `avahi-daemon` | the Avahi daemon answers `GetVersionString` over its D-Bus API (see Database) |
| `syncthing`   | a Syncthing instance answers `/rest/noauth/health` with `{"status":"OK"}` (see Database) |
| `unifi` / `unifi-controller` | a UniFi Network controller answers `GET /status` with `meta.rc == "ok"` on 8443 (see Database) |
| `influxdb` / `influx` | an InfluxDB server answers `/health` (or `/ping`) and reports its version on 8086 (see Database) |
| `prometheus` / `prom` | a Prometheus server answers `/api/v1/status/buildinfo` (or `/-/healthy`) on 9090 (see Database) |
| `clamd` / `clamav` | a ClamAV daemon answers `VERSION` with its engine version (see Database) |
| `spamd` / `spamassassin` | the SpamAssassin daemon answers `PING` with `PONG` (see Database) |
| `smb` / `samba` / `cifs` | an SMB/CIFS server negotiates (and, with credentials, authenticates) (see Database) |
| `acpid`       | the ACPI event daemon accepts a connection on its Unix socket (see Database) |
| `fail2ban`    | fail2ban-server accepts a connection on its control socket (see Database) |
| `lvmpolld`    | LVM's poll daemon answers a `hello` request with `OK` over its socket (see Database) |
| `rpcbind` / `portmap` / `portmapper` | the RPC portmapper answers an RPC NULL call (see Database) |
| `nfs` / `nfs-server` / `nfsd` | an NFS server answers an RPC NULL call on 2049 (see Database) |
| `mountd` / `rpc.mountd` / `nfs-mountd` | the NFS mount daemon answers an RPC NULL call to MOUNT (100005) (see Database) |
| `statd` / `rpc.statd` / `nsm` | the NFS status monitor answers an RPC NULL call to NSM (100024) (see Database) |
| `nebula` / `nebula-vpn` | a Nebula mesh-VPN node answers an unknown-tunnel packet with a `recv_error` on 4242/udp (see Database) |
| `openvpn` / `ovpn` | an OpenVPN server answers a hard-reset-client with a hard-reset-server on 1194 (see Database) |
| `rdp` / `ms-wbt-server` | a Remote Desktop server answers the X.224 connection negotiation (see Database) |
| `guacd` / `guacamole` | the Guacamole proxy daemon answers a `select` with a Guacamole instruction (see Database) |
| `asterisk` / `ami` | an Asterisk PBX sends its AMI `Asterisk Call Manager/<version>` greeting (see Database) |
| `sieve` / `managesieve` | a ManageSieve server sends its capability greeting ending in `OK` (see Database) |
| `mqtt`        | an MQTT broker accepts a CONNECT (CONNACK return code 0) (see Database) |
| `varnish` / `varnishadm` | the Varnish management CLI answers with its banner/auth challenge (see Database) |
| `ceph` / `ceph-mon` | a Ceph monitor sends its messenger `ceph v…` banner (see Database) |
| `glusterfs` / `glusterd` / `gluster` | a GlusterFS node's glusterd answers an RPC NULL on 24007 (see Database) |
| `openvswitch` / `ovs` / `ovsdb` / `ovsdb-server` | ovsdb-server answers an OVSDB `list_dbs` JSON-RPC request (see Database) |
| `sqlite` / `sqlite3` | a SQLite database file passes `PRAGMA integrity_check` (see SQLite) |
| `sql`         | a SQL query's scalar result compares (`== != > >= < <= =~`) against a value (see SQL query) |
| `mongodb-query` | a MongoDB document count / aggregation / command result compares against a value (see MongoDB query) |
| `influxdb-query` | an InfluxQL (1.x) or Flux (2.x) query's scalar result compares against a value (see InfluxDB query) |
| `size`        | a file/directory grows by at least `grow_by` within `within` (runaway growth) (see Size growth) |
| `websocket` / `ws` | a WebSocket endpoint completes the RFC 6455 opening handshake (see WebSocket) |

The `disk` check also verifies the **mount** of its `path` — see
[Disk and mount](configuration.md#host-watches).

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
  natively-dialed connection-protocol check (the TCP/UDP probes — `redis`, `imap`,
  `smtp`, `dns`, `ntp`, `nfs`, `dhcp`, `openvpn`, `nebula`, `tftp`, …, plus the
  `influxdb`/`prometheus` HTTP probes) honor the **full list + `interface_match`**.
  The standalone `http` check honors a **single** interface (the first listed). It
  is **not** honored by checks that dial through a third-party library — the SQL
  drivers (`mysql`/`postgres`), `mongodb`, `ldap`, `libvirt`, and the
  `syncthing`/`unifi`/`rspamd`/`ipp` HTTP probes.

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

To **restart** a service when a library or file is updated (the other half of the
example — "if the pam library was updated, restart"), use a remediation rule with
a [`changed:`](#rules) condition (or `restart_on_change: {libraries: […]}`):

```yaml
rules:
  restart-on-pam:
    type: remediation
    if: { changed: { library: pam } }   # or { path: /lib64/security/pam_unix.so }
    then: { action: restart }
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
states across cycles), so it works as a **host watch** (built once); as a
per-service check, where checks are rebuilt each cycle, only the open/closed
expectation applies.

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
    expect_body: "ready"               # substring, or { op, value } (see below)
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
**Squid** (`http://[user:pass@]host:port`; `http`, `https` or `socks5` schemes —
credentials, when present, go in the URL). This both monitors that the proxy
forwards correctly and that the target is reachable through it; for an `https://`
target the proxy is used via `CONNECT`, and certificate inspection (below) still
applies to the target's certificate. `json:` marshals the value and sets `Content-Type:
application/json` (override it via `headers`); `body:` sends a raw string. The
response is only read when `expect_body`/`expect_json` is set (capped at 1 MiB).
`expect_json` looks up **dotted paths** into nested objects. A scalar value is an
equality check (compared as a string); a `{op, value}` mapping uses an operator —
`>`, `>=`, `<`, `<=` (numeric), `==`, `!=`, `contains` (string), or `=~` (regex).

**Response comparisons.** Beyond the substring shorthand, `expect_status`,
`expect_body` and `expect_latency` accept an `{op, value}` mapping using the
shared operator set `== != > >= < <=` (numeric, or string for `==`/`!=`) and
`=~` (Go/RE2 regular expression) — the same operators as the [`sql`](#sql-query-sql)
check:

- `expect_status: { op: "<", value: 500 }` — compare the status code numerically
  (in addition to the code/class/list forms).
- `expect_body: { op: "=~", value: "^OK" }` — compare the **trimmed** response
  body: numeric when both sides parse as numbers (`>`, `<`, …), otherwise string
  equality, or a regex with `=~`. The plain string form (`expect_body: "ready"`)
  is still a substring match.
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
means healthy) — the opposite of the standalone `cert` check. When inspection
runs, the result data carries the same certificate fields the `cert` check
exposes (`issuer`, `subject`, `dns_names`, `not_after`, `days_left`,
`fingerprint`, …). To read the certificate even when it is expired or otherwise
invalid, the request skips transport-level verification and verifies the chain
manually; `cert_verify: false` disables that verification. The change conditions
are **stateful** (they remember the previous cycle), so they only apply when the
check is built once — as a host watch. For raw TLS endpoints or local
certificate files, use the standalone [`cert`](#cert) check.

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
a **local file** (`path`) — and alerts (`OK == true`) on any configured problem. It
is condition-style, so as a watch the hook/notify fires on a problem and in rules
`active: {check: api-cert}` is true.

```yaml
checks:
  api-cert:                    # live endpoint
    type: cert
    host: api.example.com      # host XOR path (exactly one required)
    port: 443                  # optional, default 443
    server_name: api.example.com   # optional SNI + hostname to verify (default = host)
    expires_in_days: 14        # optional: warn this many days before expiry
    verify: true               # optional, default true: chain + hostname + validity
    on_algorithm_change: true  # optional: alert when the signature algorithm changes
    on_issuer_change: true     # optional: alert when the issuer (CA) changes / re-issue
    on_change: false           # optional: alert on any certificate rotation (fingerprint)

  tls-keypair:                 # local file
    type: cert
    path: /etc/ssl/private/api.key   # host XOR path
    on_change: true            # optional: alert if the file's fingerprint changes
```

**Host source.** It alerts when the certificate is **expired or not yet valid**,
**expires within `expires_in_days`**, fails chain/hostname **verification**
(`verify`, on by default — catches self-signed, wrong host, expired chains), or —
between cycles — its **signature algorithm**, **issuer** or **fingerprint** changes.
A network/TLS error fetching the cert is **not** an alert (use a `tcp`/`http` check
for reachability).

**File source (`path`).** Reads and parses a local file, recognising natively (no
external tools): PEM **certificate**, **certificate request** (CSR), PKCS#1 / EC /
PKCS#8 **private keys**, PKIX **public key**, **OpenSSH** private key, and **OpenSSH**
public key (`authorized_keys` line). Certificates are checked for expiry/validity as
above; material that does not expire (keys, CSRs) alerts only on
`on_change`/`on_algorithm_change`. A **missing, unreadable or unparseable file is an
alert** (a local configuration problem, unlike a transient network error). `verify`,
`port` and `server_name` do not apply to files.

**Result data** exposes `kind` (certificate / certificate_request / private_key /
public_key / openssh_private_key / openssh_public_key / …), `source`,
`signature_algorithm`, `public_key_algorithm`, `key_bits`, `subject` and
`fingerprint`. Certificates additionally expose `days_left`, `not_before`,
`not_after`, `issuer`, `serial_number` (hex) and `dns_names` (SANs).

The change conditions are **stateful** (they remember the previous value across
cycles), so they work as a **host watch** (built once); as a per-service check —
where checks are rebuilt each cycle — only the level conditions (expiry, validity)
apply.

Each check has an optional `timeout` (else `engine.default_timeout`) and an
optional `interval` to run it less often than the worker cycle — every
`round(interval / resolution)` cycles, reusing its last result in between (see
[per-check interval](configuration.md#per-check-interval)).

### Database connection (`mysql` / `mariadb`)

A connection-protocol check connects to a server over its wire protocol, with a
user and password, and verifies it responds. The check type **is** the protocol
name. Supported protocols:

- `mysql` (alias `mariadb`) — default port 3306; `tls`: `false` | `true` | `skip-verify`.
- `postgres` (alias `postgresql`) — default port 5432; `tls`: `false` | `true` |
  `skip-verify`, or a PostgreSQL sslmode (`disable`/`require`/`prefer`/
  `verify-ca`/`verify-full`).
- `mongodb` (alias `mongo`) — default port 27017; `tls`: `false` | `true` |
  `skip-verify`. `user` is **optional** (MongoDB may run without auth); with
  credentials it authenticates against `auth_source` (defaults to `database`, then
  `admin`). Connects, verifies a `ping`, and reads the server version via
  `buildInfo`. Uses the official `go.mongodb.org/mongo-driver`. To run a query and
  compare a result, see the **MongoDB query** check (`mongodb-query`).
- `redis` (alias `valkey`) — default port 6379; `tls`: `false` | `true` |
  `skip-verify`. `user` is **optional** (legacy `requirepass` uses a password
  only, or no auth at all); a password-only check sends `AUTH <password>`.
  Probed natively over RESP (no driver), verifying `PING` → `PONG`.
- `imap` — default port 143; `tls`: `false` | `true` | `skip-verify` (implicit
  TLS / IMAPS — use port 993). `user` is **optional**: with no credentials it is
  an **anonymous** check that verifies the server greets `* OK`; with a
  user/password it performs an IMAP `LOGIN`. Probed natively (RFC 3501).
- `pop` (alias `pop3`) — default port 110; `tls`: `false` | `true` |
  `skip-verify` (implicit TLS / POP3S — use port 995). `user` is **optional**:
  with no credentials it is an **anonymous** check (server greets `+OK`); with a
  user/password it performs `USER`/`PASS`. Probed natively (RFC 1939).
- `smtp` — default port 25; `tls`: `false` | `true` | `skip-verify` (implicit
  TLS / SMTPS — use port 465; for submission use port 587). `user` is
  **optional**: with no credentials it is an **anonymous** check (greeting
  `220` + `EHLO`); with a user/password it performs `AUTH PLAIN`. Probed
  natively (RFC 5321).
- `nntp` (alias `nntps`) — default port 119; `tls`: `false` | `true` |
  `skip-verify` (implicit TLS / NNTPS — use port 563). `user` is **optional**:
  with no credentials it is an **anonymous** check (server greets `200` posting
  allowed / `201` posting prohibited — reported as `posting_allowed`); with a
  user/password it performs `AUTHINFO USER`/`PASS`. Probed natively (RFC
  3977/4643).
- `ftp` — default port 21; `tls`: `false` | `true` | `skip-verify` (implicit TLS
  / FTPS — use port 990). `user` is **optional**: with no credentials it is an
  **anonymous** check (greeting `220`); with a user/password it performs
  `USER`/`PASS` (a password with no user logs in as `anonymous`). Probed natively
  (RFC 959).
- `ssh` — default port 22 (no `tls`: SSH has its own transport crypto). `user`
  is **optional**: with no credentials it is an **anonymous** check that
  completes the key exchange to capture the server's host key — authentication
  then fails, which is expected; with a user/password it requires login to
  succeed. Result data exposes `fingerprint` (SHA256 of the host key),
  `host_key_algo`, `server_version` and `protocol`. Set **`on_change: true`**
  (on a host watch, where the check is built once) to alert when the host-key
  fingerprint changes between cycles — a possible re-key or man-in-the-middle.
  Uses `golang.org/x/crypto/ssh`.
- `fpm` (alias `php-fpm`) — PHP-FPM over FastCGI. Set `socket` to the pool's
  Unix socket (e.g. `/run/php/php8.2-fpm.sock`), or use `host`/`port` (default
  9000) for a TCP pool. No auth. It performs a FastCGI request to `/ping` and
  expects `pong`, so the pool must have **`ping.path = /ping`** enabled. Probed
  natively (FastCGI).

- `ipp` (alias `cups`) — default port 631; `tls`: `false` | `true` |
  `skip-verify` (IPPS). No auth. POSTs an IPP `CUPS-Get-Default` request over
  HTTP and verifies a valid IPP response — any parseable reply proves cupsd is up
  and speaking IPP. Result data carries the IPP version and status. Probed
  natively (RFC 8010/8011).
- `rspamd` — default port 11334 (the controller worker); `tls`: `false` | `true`
  | `skip-verify` (HTTPS). No auth. Sends `GET /ping` and expects `200` with a
  `pong` body — the unauthenticated liveness endpoint every rspamd worker
  exposes (point `port` at 11333 for the normal scanning worker or 11332 for the
  proxy). Result data carries the rspamd version, read from the `Server` header.
  Probed natively (HTTP).
- `libvirt` (alias `libvirtd`) — opens an RPC connection to a libvirt daemon and
  reads its version; both succeeding prove libvirtd is up and answering. It runs
  no write operation. **Transport:** with no `socket` and no `host` it dials the
  local Unix socket `/var/run/libvirt/libvirt-sock`; set `socket` for a different
  path, or set `host` to use plain **TCP** (default port 16509). TLS/SASL is not
  supported. **Connect URI:** `query` selects the driver, default `qemu:///system`
  (e.g. `lxc:///`, `xen://`). No auth — local socket access is governed by the
  socket's permissions/polkit. Result data carries the libvirt version, connect
  URI, transport and the daemon hostname. Uses
  `github.com/digitalocean/go-libvirt` (pure Go).

  ```yaml
  checks:
    libvirt-local:
      type: libvirt              # dials /var/run/libvirt/libvirt-sock
    libvirt-tcp:
      type: libvirt
      host: 10.0.0.4             # plain TCP on 16509
      query: "qemu:///system"    # optional connect URI (default qemu:///system)
  ```
- `dbus` — connects to a D-Bus daemon and completes its SASL auth +
  `org.freedesktop.DBus.Hello` handshake — which alone proves the bus is up — then
  calls `org.freedesktop.DBus.GetId` to read the bus UUID. It runs no write
  operation. **Target:** defaults to the system bus
  (`unix:path=/var/run/dbus/system_bus_socket`); set `socket` for a different
  socket path, or `query` for a full D-Bus address (`unix:abstract=…`,
  `tcp:host=…,port=…`). Socket-based, so there is no TCP port. No auth — access is
  governed by the socket's permissions. Result data carries the bus id, address
  and the connection's unique name. Uses `github.com/godbus/dbus/v5` (pure Go).

  ```yaml
  checks:
    dbus-system:                 # dials unix:path=/var/run/dbus/system_bus_socket
      type: dbus
    dbus-custom:
      type: dbus
      socket: /run/dbus/system_bus_socket   # or use `query` for a full address
  ```
- `avahi` (alias `avahi-daemon`) — the Avahi mDNS/DNS-SD (zeroconf) daemon,
  probed over its D-Bus API (`org.freedesktop.Avahi`). Connects to the system bus
  (SASL auth + Hello) and calls
  `org.freedesktop.Avahi.Server.GetVersionString` — a reply proves avahi-daemon is
  up and registered on the bus — reporting the `version` (pair with
  `on_version_change`) and, best-effort, the `hostname` and server `state`
  (`running` when AVAHI_SERVER_RUNNING). **Target:** like `dbus`, defaults to the
  system bus (`unix:path=/var/run/dbus/system_bus_socket`); set `socket` for a
  different bus socket or `query` for a full D-Bus address. Socket-based, so there
  is no TCP port. No auth — access is governed by the bus permissions. Uses
  `github.com/godbus/dbus/v5` (pure Go).
- `syncthing` — default port 8384; `tls`: `false` | `true` | `skip-verify`
  (`skip-verify` covers Syncthing's default self-signed GUI certificate). Sends
  `GET /rest/noauth/health` and expects `200` with `{"status":"OK"}` — the
  unauthenticated liveness endpoint. With an **API key** in `password` (sent as
  `X-API-Key`) it also reads `/rest/system/version` and reports the Syncthing
  version (`os`/`arch` too); a rejected key fails the check. No user. Probed
  natively (HTTP/REST).

  ```yaml
  checks:
    syncthing:
      type: syncthing
      host: 127.0.0.1
      # tls: skip-verify            # if the GUI is on HTTPS
      # password: "${env:ST_KEY}"   # optional API key -> also reports version
  ```
- `unifi` (aliases `unifi-controller`, `unifi-network`) — a UniFi Network
  controller (Ubiquiti). Default port 8443. The controller is **HTTPS-only** and
  ships a self-signed certificate, so `tls` here selects only verification:
  certificate verification is **skipped by default**; set `tls: true` to require a
  valid certificate. No user. Sends `GET /status` (the unauthenticated liveness
  endpoint) and expects `200` with JSON `meta.rc == "ok"`, reporting the
  controller's `server_version` (pair with `on_version_change`) and `uuid`. Probed
  natively (HTTP/REST). Note: this targets the self-hosted UniFi Network
  application; on a UniFi OS console (UDM/Cloud Key) the controller is proxied
  under `/proxy/network/`, which this check does not follow.
- `ajp` — default port 8009 (TCP). No auth. Sends an **AJP13 CPing** and expects
  a **CPong** — the same liveness probe Apache/nginx use against Tomcat's AJP
  connector. Probed natively (AJP13).
- `rsync` (alias `rsyncd`) — default port 873 (TCP). No auth. Reads the rsync
  daemon's `@RSYNCD: <version>` greeting; receiving it proves the daemon is up
  and speaking the rsync protocol. Result data carries the protocol version.
  Probed natively.
- `ldap` — default port 389; `tls`: `false` | `true` | `skip-verify` (implicit
  TLS / LDAPS — use port 636). `user` is **optional**: with no credentials it
  does an **anonymous bind** (a successful bind, or an LDAP-level rejection, both
  prove the directory is up — only a transport error fails); with a
  user/password it does a **simple bind** where `user` is the bind DN and must
  succeed. Result data carries the bind mode and result. Uses
  `github.com/go-ldap/ldap/v3`.
- `snmp` — default port 161 (UDP). With **no `user`** it uses **SNMPv2c** with a
  community string (`password`, default `public` — the anonymous/shared-secret
  model). With a **`user`** it uses **SNMPv3 USM**: a `password` adds SHA
  authentication (authNoPriv), otherwise noAuthNoPriv. It reads the system
  description and object id; result data carries `sys_object_id`, `snmp_version`
  and the description (as the version banner). Set **`on_change: true`** (on a
  host watch) to alert when `sysObjectID` (the device identity — model/firmware)
  changes. Uses `github.com/gosnmp/gosnmp`.
- `tftp` — default port 69 (UDP). No auth. Sends a read request (RRQ) for
  `query` (default `sermo-tftp-check`) and verifies the server answers with a
  valid TFTP packet: a `DATA` reply (the file is served) or an `ERROR` reply
  (e.g. file not found) both pass — either proves the server is up and speaking
  TFTP. Result data carries the reply kind and, for an error, the TFTP error
  code/message. Probed natively (RFC 1350).
- `ntp` — default port 123 (UDP). No auth. Sends a client request and verifies
  the server answers in **server mode** with a synchronized **stratum (1–15)**;
  a kiss-o'-death (stratum 0) or unsynchronized (stratum 16) reply fails. Result
  data carries `stratum` and the clock `offset_seconds`. Probed natively (RFC
  5905).
- `clamd` (alias `clamav`) — default port 3310 (TCP), or a Unix socket via
  `socket` (e.g. `/run/clamav/clamd.ctl`). No auth, no TLS. Sends the clamd
  `VERSION` command and verifies a `ClamAV <version>/…` reply — proof the daemon
  is up and speaking the clamd protocol. Result data carries the engine
  `version` (the daily signature-database part is dropped, so `on_version_change`
  stays quiet across routine DB updates) and the full `version_string`. Probed
  natively.
- `rpcbind` (aliases `portmap`, `portmapper`) — default port 111 (UDP). No auth.
  Sends an ONC RPC **NULL** call to the portmapper program (100000 v2) and
  verifies a well-formed RPC reply — proof the daemon is up and speaking RPC. Any
  reply (accepted or denied) passes; result data carries the `rpc_status`. Probed
  natively (RFC 5531/1833).
- `glusterfs` (aliases `glusterd`, `gluster`) — default port 24007 (TCP, the
  glusterd management daemon). No auth. Sends an ONC RPC **NULL** call to the
  GlusterFS handshake program (record marking over TCP) and verifies a
  well-formed RPC reply — proof that node's glusterd is up and speaking RPC;
  result data carries the `rpc_status`. Probed natively (reuses the rpcbind RPC
  machinery). **This checks one node.** To alert when **any node** in a cluster
  is down, configure one check per node (one `host` each) — the failing node's
  check fires:

  ```yaml
  checks:
    gluster-n1: { type: glusterfs, host: 10.0.0.1 }
    gluster-n2: { type: glusterfs, host: 10.0.0.2 }
    gluster-n3: { type: glusterfs, host: 10.0.0.3 }
  ```

  Cluster-wide peer status is not gathered in-protocol (it would need
  authenticated GlusterD management RPC).
- `ceph` (alias `ceph-mon`) — default port 3300 (TCP, the Ceph monitor's
  messenger v2; use port 6789 for the legacy v1). No auth. On connect a Ceph
  daemon sends a messenger banner (`ceph v2\n` for v2, `ceph v027` for v1);
  reading a `ceph v` banner proves it is a Ceph endpoint. Result data carries the
  `messenger` version (`v1`/`v2`). The banner precedes the authenticated
  handshake, so no credentials. Probed natively.
- `varnish` (alias `varnishadm`) — default port 6082 (TCP, the Varnish `-T`
  management CLI). No auth. On connect varnishd sends a CLI response (a
  `<status> <length>` line and a body); status **200** carries the banner (with
  the version) and **107** is an authentication challenge (a CLI secret is set) —
  either proves the management CLI is up and speaking the protocol. Result data
  carries the `cli_status` and, for a banner, the Varnish `version`. The CLI
  secret authentication is not performed (liveness only). Probed natively.
- `openvswitch` (aliases `ovs`, `ovsdb`, `ovsdb-server`) — default port 6640
  (TCP, the Open vSwitch configuration database server `ovsdb-server`), or a Unix
  socket via `socket` (commonly `/run/openvswitch/db.sock`); `tls`: `false` |
  `true` | `skip-verify` (SSL). No auth. Issues an OVSDB (RFC 7047) `list_dbs`
  JSON-RPC request and verifies a result listing the served databases — proof
  ovsdb-server is up and speaking OVSDB; result data carries the `databases`
  list. When the `Open_vSwitch` database is present it follows up with a
  `transact` select reading `ovs_version`, reported as the `version`. Probed
  natively.
- `mqtt` — default port 1883 (TCP); `tls`: `false` | `true` | `skip-verify`
  (MQTTS, port 8883). Performs an MQTT 3.1.1 `CONNECT` handshake and verifies the
  broker answers `CONNACK` accepting the connection (return code 0). With no
  credentials it is an anonymous connect; `user`/`password` authenticate. A
  refused CONNACK (e.g. `not-authorized`, `bad-username-or-password`) fails the
  check with the reason; result data carries the `connack` status. Probed
  natively (MQTT 3.1.1).
- `sieve` (alias `managesieve`) — default port 4190 (TCP); `tls`: `false` |
  `true` | `skip-verify` (implicit TLS). No auth. On connect the server sends a
  greeting of capability lines terminated by an `OK` response (RFC 5804); reading
  it and seeing the `OK` proves the server is up and speaking ManageSieve. The
  `IMPLEMENTATION` capability is reported as the server `version` (a `NO`/`BYE`
  greeting, e.g. a connection-limit refusal, fails the check). Probed natively.
- `asterisk` (alias `ami`) — default port 5038 (TCP); `tls`: `false` | `true` |
  `skip-verify` (AMI over TLS). No auth. On connect, Asterisk's Manager Interface
  sends an `Asterisk Call Manager/<version>` greeting before any login; reading
  it proves AMI is up and yields the manager `version` (result data also carries
  the full `banner`). Pair with `on_version_change` (host watch) to alert on an
  Asterisk upgrade. Probed natively.
- `guacd` (alias `guacamole`) — default port 4822 (TCP). No auth. Opens the
  Guacamole handshake by sending a `select` instruction for a protocol (`query`,
  default `vnc`) and verifies guacd replies with a well-formed Guacamole
  instruction — an `args` reply (protocol available) or an `error` (e.g. plugin
  missing) both prove guacd is up and speaking the protocol. Result data carries
  the selected protocol and the reply `opcode`. Probed natively (Guacamole
  protocol).
- `rdp` (alias `ms-wbt-server`) — default port 3389 (TCP). No auth. Sends an
  X.224 **Connection Request** with an RDP Negotiation Request and verifies the
  server answers with an X.224 **Connection Confirm** — proof it is up and
  speaking RDP. A negotiation failure still counts as up (the server answered).
  Result data carries the negotiated `security` protocol (`rdp` = standard RDP
  security, `tls`, `hybrid` = CredSSP/NLA, `hybrid-ex`). Probed natively
  (MS-RDPBCGR); the negotiation precedes authentication, so no credentials.
- `nfs` (aliases `nfs-server`, `nfsd`) — default port 2049 (TCP). No auth. Sends
  an ONC RPC **NULL** call to the NFS program (100003) — using RPC record marking
  over TCP — and verifies a well-formed RPC reply, which proves the server is up
  and speaking RPC. A version-mismatch reply (e.g. an NFSv4-only server answering
  a v3 NULL) still passes; result data carries the `rpc_status`. Probed natively
  (RFC 5531/1813). Reuses the rpcbind RPC machinery.
- `mountd` (aliases `rpc.mountd`, `nfs-mountd`) — the NFS mount daemon. Default
  port 20048 (TCP), the common fixed mountd port. No auth. Sends an ONC RPC
  **NULL** call to the MOUNT program (100005) — using RPC record marking over TCP
  — and verifies a well-formed RPC reply, which proves the daemon is up and
  speaking RPC. A version-mismatch reply still passes; result data carries the
  `rpc_status`. rpc.mountd has **no fixed well-known port** — it registers a
  (often random) port with rpcbind — so if the daemon is not on 20048, set `port`
  to its configured port (query it with `rpcinfo -p <host>`). Probed natively
  (RFC 5531/1813). Reuses the rpcbind RPC machinery.
- `statd` (aliases `rpc.statd`, `nsm`, `nfs-statd`) — the NFS status-monitor
  daemon (NSM, used for NFS lock recovery). Default port 662 (TCP), the
  conventional fixed statd port. No auth. Sends an ONC RPC **NULL** call to the
  NSM program (100024) — using RPC record marking over TCP — and verifies a
  well-formed RPC reply, which proves the daemon is up and speaking RPC. A
  version-mismatch reply still passes; result data carries the `rpc_status`.
  rpc.statd has **no fixed well-known port** — it registers a (often random) port
  with rpcbind — so if the daemon is not on 662, set `port` to its configured
  port (query it with `rpcinfo -p <host>`). Probed natively (RFC 5531/1813).
  Reuses the rpcbind RPC machinery.
- `nebula` (alias `nebula-vpn`) — a [Nebula](https://github.com/slackhq/nebula)
  mesh-VPN node. Default port 4242 (**UDP**). No auth. A real tunnel needs a
  CA-signed certificate, but a node answers a data packet for a tunnel index it
  does not know with a plaintext **recv_error** (telling the sender to
  re-handshake), so the check sends a Nebula `message` packet carrying a random
  index and verifies the node replies with a `recv_error` echoing it — proof the
  node is up and speaking Nebula, with no credentials. Probed natively (16-byte
  Nebula header over UDP). The reply is governed by the node's
  `listen.send_recv_error` setting (default `always`); a node set to `never` — or
  to `private` when probed from a public address — stays silent and reads as
  down, so probe lighthouses/nodes from an address their config answers.
- `openvpn` (alias `ovpn`) — an OpenVPN server. Default port 1194; `transport`
  selects the transport (`udp`, the default, or `tcp` — set it to match the
  server's `proto`). No auth. The first step of the OpenVPN handshake is
  unauthenticated (TLS comes after): the check sends a
  `P_CONTROL_HARD_RESET_CLIENT_V2` carrying a random session id and verifies the
  server answers with a `P_CONTROL_HARD_RESET_SERVER_V2` that acknowledges that
  session id — proof the server is up and speaking OpenVPN, with no credentials.
  Result data carries the `transport`. Probed natively (OpenVPN control-channel
  wire format). **Caveat:** the reset only gets a reply from a server without
  `tls-auth`/`tls-crypt`; those HMAC-wrap (or encrypt) control packets, so a bare
  reset is dropped and the server stays silent — silence is then expected and is
  not proof it is down.
- `influxdb` (alias `influx`) — an InfluxDB server. Default port 8086; `tls`:
  `false` (plain HTTP, the default) | `true` | `skip-verify` (https). No auth. GETs
  `/health` (InfluxDB 2.x / 1.8+) and verifies a JSON `status` of `pass`, reporting
  the server `version` (pair with `on_version_change`); on older servers without
  `/health` it falls back to `/ping`, which answers `204` with the version in the
  `X-Influxdb-Version` header. Probed natively (HTTP/REST). This is a
  liveness/version check; to run an InfluxQL query and compare a result, see the
  **InfluxDB query** check (`influxdb-query`).
- `prometheus` (alias `prom`) — a Prometheus server. Default port 9090; `tls`:
  `false` (plain HTTP, the default) | `true` | `skip-verify` (https). GETs
  `/api/v1/status/buildinfo` and verifies a `success` status, reporting the server
  `version` (pair with `on_version_change`); on older servers it falls back to
  `/-/healthy` (liveness only). An optional `user`/`password` is sent as HTTP Basic
  auth (for a reverse proxy fronting the API). Probed natively (HTTP/REST).
- `fail2ban` — fail2ban-server. **Socket-only** (no TCP port); defaults to
  `/var/run/fail2ban/fail2ban.sock`, override with `socket`. fail2ban speaks a
  Python pickle command protocol that is not worth reimplementing for a liveness
  check, so the check is the **connect itself**: a successful connection proves
  fail2ban-server is listening (a stale socket left by a dead server refuses the
  connection). It exchanges no commands. No auth. Probed natively.
- `acpid` — the ACPI event daemon. **Socket-only** (no TCP port); defaults to
  `/var/run/acpid.socket`, override with `socket`. acpid is an event broadcaster
  with no request/response protocol, so the check is the **connect itself**: a
  successful connection proves acpid is listening (a stale socket left by a dead
  daemon refuses the connection). It reads nothing — reading would block until an
  ACPI event — and there is no version. No auth. Probed natively.
- `lvmpolld` — LVM's poll daemon. **Socket-only** (no TCP port); defaults to
  `/run/lvm/lvmpolld.socket`, override with `socket`. Unlike acpid/fail2ban it is
  probed by protocol: it speaks LVM's generic daemon framework, so the check
  sends a `hello` request and verifies the daemon replies `OK` — proof lvmpolld is
  up and speaking its protocol (a stale socket left by a dead daemon refuses the
  connection). It also guards against pointing at a different LVM daemon
  (lvmetad, dmeventd) by checking the reported protocol name. Result data carries
  the `protocol` and `protocol_version`; the handshake exposes no lvm2 software
  version. No auth. Probed natively.
- `smb` (aliases `samba`, `cifs`) — default port 445 (TCP). `user` is
  **optional**. It first runs an SMB2 `NEGOTIATE` (proving the server is up) and
  reports the negotiated **dialect** as the `version` (`2.0.2`/`2.1`/`3.0`/
  `3.0.2`/`3.1.1` — pair with `on_version_change`), the `protocol` family
  (`SMB2`/`SMB3`) and whether **signing is required**. With a `user` it then
  authenticates over **NTLM** (a failed login fails the check), counts the
  shares (`shares`), and — if a share is named in `query` — verifies it can be
  **mounted** (`share_access`). The domain may be embedded in `user`
  (`DOMAIN\user` or `user@domain`). Uses `github.com/cloudsoda/go-smb2` for the
  authenticated session; the NEGOTIATE is native.

  ```yaml
  checks:
    fileserver:
      type: smb
      host: 10.0.0.9
      user: "WORKGROUP\\monitor"     # optional; enables NTLM auth + share checks
      password: "${env:SMB_PASS}"
      query: "data"                   # optional: verify this share mounts
  ```
- `spamd` (alias `spamassassin`) — default port 783 (TCP), or a Unix socket via
  `socket`. No auth. Sends a SPAMC/SPAMD `PING` and verifies spamd answers
  `SPAMD/<v> 0 PONG` — proof it is up and speaking the protocol. Result data
  carries the SPAMD protocol version. Probed natively.
- `dns` — default port 53 (UDP). No auth. Sends an `A` query for `query`
  (default `localhost`) to the server and verifies it answers: `NOERROR` or
  `NXDOMAIN` pass (the server is up and speaking DNS); `SERVFAIL`, `REFUSED`, a
  timeout or a transport error fail. Result data carries the `rcode` and answer
  count. Probed natively (RFC 1035 message). Set `query` to a name the server
  should answer (e.g. a zone it is authoritative for).
- `dhcp` (alias `dhcpd`) — default port 67 (UDP). **Linux only.** No auth. Sends
  a `DHCPDISCOVER` and verifies the server replies with a `DHCPOFFER` — proof it
  is up and handing out leases. It never sends a `DHCPREQUEST`, so **no real
  lease is consumed**. Two modes: set `interface` to **broadcast** the DISCOVER
  out that link and discover any server (`255.255.255.255`); omit it to
  **unicast** to `host` (a known server or relay). The client hardware address
  is a random, anonymous locally-administered MAC by default; set `mac` to use a
  fixed address (e.g. a server that only answers reserved clients). Result data
  carries the offered IP, server id, subnet mask and lease time. **Requires
  elevated privileges** to bind the DHCP client port 68 (and `CAP_NET_RAW` for
  the per-interface bind), like the `icmp` check; the host should not run a
  competing DHCP client on that interface. Probed natively (RFC 2131).

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

The `socket` field (Unix socket path) is generic; when set the check dials the
socket instead of `host`/`port`. The `query` field is the per-protocol lookup
target (the DNS name for `dns`).

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
fires on it. It reuses the MySQL/PostgreSQL connection builders and the SQLite
read-only open of the other checks.

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
    op: ">"                     # == | != | > | >= | < | <= | =~
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

A `mongodb-query` check runs a MongoDB query and compares a **scalar result**
against a `value`, the document-store counterpart of the `sql` check. It is
**condition-style** (`OK == true` means the comparison holds). It uses the same
connection variables as the `mongodb` connection check (`host`/`port`/`user`/
`password`/`database`/`tls`, plus `auth_source`) and the official MongoDB driver.
Three query shapes are supported:

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
    op: "<"                       # == | != | > | >= | < | <= | =~
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
  `==`/`!=` numeric-or-string; `=~` RE2 regexp).
- **Auth:** with a `user`, credentials are checked against `auth_source` (default
  `database`, then `admin`). The check only reads — point it at a read-only user.
- Result data carries `mode`, `op`, `threshold`, the raw `result` and, when
  numeric, a `value` for hooks/rules.

### InfluxDB query (`influxdb-query`)

An `influxdb-query` check runs a query against InfluxDB and compares a **scalar
result** against a `value`, the time-series counterpart of the `sql`/
`mongodb-query` checks. It is **condition-style** (`OK == true` means the
comparison holds) and reuses the `influxdb` connection variables (`host`/`port`/
`user`/`password`/`tls`). The **`language`** selects the query API:

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
    op: "<"                     # == | != | > | >= | < | <= | =~
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
  `==`/`!=` numeric-or-string; `=~` RE2 regexp).
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
fast", so `active: {check: …}` fires) and **stateful**, so run it as a **host
watch** where the same instance ticks each cycle and remembers recent sizes.

```yaml
watches:
  log-runaway:
    type: size
    path: /var/log/app.log     # a file, or a directory (recursive sum of file sizes)
    grow_by: 1GB               # alert if it grows at least this much…
    within: 1h                 # …within this sliding window
```

Each cycle it samples the path's size (a file's bytes, or the recursive sum of
regular-file sizes under a directory), keeps the samples seen in the last
`within`, and compares the current size against the oldest one still in the
window. It fails when `current − baseline ≥ grow_by`. The first cycle only
baselines (no alert). Sizes accept human units (`1GB`, `500MB`, `2GiB`, or a
plain byte count). Result data carries `current_bytes`, `baseline_bytes`,
`growth_bytes`, the `window` and `value` (the growth) for hooks/rules. A
directory walk reads the whole subtree each cycle, so point it at a bounded path.

### WebSocket (`websocket` / `ws`)

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
    type: mysql                 # mariadb, postgres, mongodb/mongo, influxdb/influx, prometheus/prom, redis, valkey, imap, pop, smtp, nntp/nntps, ftp, ssh, ldap, ajp, ipp/cups, rspamd, rsync, libvirt, dbus, avahi, syncthing, unifi, clamd, spamd, smb/samba, acpid, fail2ban, rpcbind, nfs, mountd/rpc.mountd, statd/rpc.statd, nebula, openvpn, rdp, guacd, asterisk, sieve, mqtt, varnish, ceph, glusterfs, openvswitch/ovs, lvmpolld, fpm, dns, dhcp, ntp, snmp, tftp
    # user is required for SQL protocols; optional for redis/imap/pop/smtp (anonymous); fpm/dns use no auth
    host: 127.0.0.1             # default 127.0.0.1
    port: 3306                  # default: the protocol's port (mysql 3306, postgres 5432)
    user: monitor               # required
    password: "${env:DB_PASS}"  # resolved from the environment at load (never store secrets in plaintext)
    database: ""                # optional
    tls: false                  # optional (see per-protocol values above)
    timeout: 5s                 # optional (engine.default_timeout)
```

It passes (health-style, `OK == true`) when it connects, authenticates as
`user`, and the server answers a ping. Result data exposes `protocol`, `host`,
`port` and the server `version`. A network/auth failure fails the check with the
error. This is meant to be added to a database service's `checks:` so a
restart/alert can fire when it stops accepting connections.

**Response comparisons (`expect`).** Any protocol check can assert the values
its probe returns — the server `version` or any field the protocol puts in its
result data (e.g. `answers`/`rcode` for `dns`, `stratum`/`offset_seconds` for
`ntp`, `sys_object_id` for `snmp`, `offered_ip`/`lease_seconds` for `dhcp`,
`ipp_version` for `ipp`, …). `expect` is a mapping of field → value (equality) or
field → `{op, value}` using the shared operators `== != > >= < <=` (numeric, or
string for `==`/`!=`) and `=~` (Go/RE2 regex). All assertions must hold, **in
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
message. This reuses the same comparison engine as the `http` and `sql` checks,
so it works for every registered protocol with no per-protocol configuration.

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
(on a **host watch**, so the check is built once and keeps state across cycles) to
alert when the server's version changes between cycles — e.g. after a package
upgrade. The tracked identity is the protocol's reported `version` (mysql,
postgres, redis, ssh, snmp, rspamd, libvirt, syncthing) or, for protocols that
only return a greeting banner (`smtp`, `imap`, `pop`, `ftp`), that banner. The
first cycle baselines silently; a later change **fails** the check and the result
data carries `version`/`version_old`. It composes with `on_change` (the SSH/SNMP
fingerprint identity) — both can be enabled at once.

```yaml
watches:
  mail-version:
    type: smtp
    host: mail.example.com
    on_version_change: true     # alert when the SMTP banner/version changes
```

More protocols are added the same way — the check type, dispatch and validation
are protocol-agnostic, so a new protocol only registers itself.

Every type above is a **single-shot check** (`Check.Run → Result`) and is usable in
**both** places:

- a service's `checks:`/`preflight:`/`postflight:` (and referenced from rules), and
- a host **watch** (`watches:`, firing a hook) — see [configuration](configuration.md#host-watches).

The host-resource checks (`disk`, `load`, `fds`, `conntrack`, `entropy`, `zombies`,
`oom`, `cert`) are condition-style — `OK == true` means there is a problem — so in
rules `active: {check: x}` fires on it, and as a watch the hook fires on it.
The health checks (`tcp`, `ports`, `http`, `command`, `service`, `file_exists`,
`binary`, `libraries`) are the opposite (`OK == true` is healthy), so as a watch
they fire the hook on **failure**.

Two watch families stay watch-only because they are not single-shot: the
multi-metric watches (`net`, `icmp`, `swap`, with a `metrics:` map and one hook per
metric) and the multi-target watches (`file`, `process`, one event/hook per
changed path or matching pid). `service`/`metric`/`process` checks need per-service
context (backend status, a metric sampler, process discovery) and so are not
available as standalone watches.

### Autofs

The `autofs` check verifies the autofs **automounter** (`automount`) is active.
autofs has no socket or port — the daemon talks to the kernel over an internal
pipe — so the liveness signal is the **mount table**: while `automount` runs it
maintains its configured map roots as `autofs`-type mountpoints in
`/proc/mounts` (they disappear when the daemon stops). Unlike `disk`/`count`,
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

A `count` check tallies the entries in a directory and compares the total to a
threshold. Like `metric`, it is condition-style: it passes (so `active`/`failed`
on it is true) when `op value` holds — useful for "too many queued files",
"backlog not draining", "spool directory empty", etc.

```yaml
checks:
  spool-backlog:
    type: count
    path: /var/spool/myapp        # required: directory to scan
    of: file                      # any (default) | file | dir | symlink
    recursive: false              # optional, default false
    op: ">"                       # >=, >, <=, <, ==, !=
    value: 1000                   # numeric threshold
```

- **`of`** selects which entries are counted. Entries are classified by their own
  type without following symlinks, so a symlink counts as `symlink` (never as the
  file or directory it points to); `any` counts every entry.
- **`recursive: true`** descends the whole subtree (the directory itself is never
  counted); unreadable subdirectories are skipped. Default counts only the
  immediate entries.
- A missing or unreadable `path` makes the check fail. The observed total is
  exposed in the check's result data as `count`.

## Metrics

Service metrics measure the discovered process set; system metrics measure the
machine. `value` is a number with an optional trailing `%`.

```
scope: service   memory, swap, cpu, cpu_thread, process_count, io, io_read, io_write, fds, threads
scope: system    total_memory, total_cpu, load1, load5, load15
```

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
Like `cpu` it is a rate (not ready on the first cycle).

`cpu`/`cpu_thread`/`total_cpu` and the `io*` metrics are rates: they are **not
ready** on the first cycle and a condition over a not-ready value is false. A `%`
threshold needs a metric with a percentage form (`memory`, `swap`, `cpu`,
`cpu_thread`, `total_memory`, `total_cpu`; `swap`/`memory` also have an absolute
byte form); a bare number needs an absolute form (everything else, including
`io*`/`fds`/`threads`, which are absolute only). Reading another process's I/O or fd count
requires privilege, so those sum only the processes the daemon can read.

## Rules

```yaml
rules:
  RULE_NAME:
    type: remediation | guard | alert
    if: { ... }       # condition tree
    for: { cycles: 3 }            # consecutive cycles (optional)
    within: { cycles: 15, min_matches: 5 }  # sliding window (optional)
    then: { action: restart }
```

Conditions form a logical tree with `and`/`or`/`not` and leaves:

```yaml
if:
  or:
    - failed: { check: http }      # a named check failed
    - active: { check: backup-flag } # a named check passed
    - file: { path: /run/x, exists: true }
    - service: { state: active }
    - process: { exe: /usr/sbin/mysqld, user: mysql, state: running }
    - metric: { scope: service, name: cpu, op: ">", value: 30% }
    - changed: { path: /lib64/libc.so.6 }  # the file changed since the last cycle
```

`failed`/`active` may also take an inline probe (`tcp`, `command`, ...) instead
of a `check:` reference.

`changed` is true when the file at `path` differs (size/mtime) from the baseline
tracked across cycles. The first cycle adopts the current value (a daemon start
never fires), and a successful `restart`/`start` re-baselines it. It is the
primitive behind `restart_on_change` (see Profiles → Library profiles).

### Windows

Without `for`/`within`, a rule fires the cycle its condition is true. `for: N`
requires N consecutive true cycles; `within: {cycles, min_matches}` requires
`min_matches` true cycles out of the last `cycles`. A rule cannot use both.

### Guards

Guard rules block unsafe actions and use `action: block` with a `message`:

```yaml
block-during-backup:
  type: guard
  blocks: [restart, stop]
  if: { active: { check: mariabackup } }
  then: { action: block, message: "Backup is running" }
```

Guards are evaluated before remediation; a remediation action that a guard
blocks never runs.

`message:` strings may use the runtime built-ins `${date}` (RFC3339), `${event}`
(the firing rule's name) and `${action}`, plus the resolved `${service}`/`${host}`
— e.g. `message: "[${host}] ${service}: ${event} → ${action} at ${date}"`.

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
