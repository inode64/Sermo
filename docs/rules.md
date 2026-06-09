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
| `load`        | a load-average threshold holds (load1/load5/load15, optional per_cpu)|
| `fds`         | system file descriptors vs `fs.file-max` (used_pct/free/allocated)  |
| `conntrack`   | the netfilter conntrack table vs its max (used_pct/free/count)      |
| `entropy`     | available kernel entropy satisfies `avail {op, value}`              |
| `zombies`     | the count of zombie processes satisfies `count {op, value}`         |
| `oom`         | the kernel OOM-kill count rose by `delta {op, value}` since last cycle|
| `cert`        | a TLS certificate is expiring/invalid, or its algorithm/issuer changed (see Cert)|
| `mysql` / `mariadb` | a connection to a MySQL/MariaDB server authenticates and responds (see Database) |
| `postgres` / `postgresql` | a connection to a PostgreSQL server authenticates and responds (see Database) |
| `redis` / `valkey` | a connection to a Redis/Valkey server authenticates and answers PING (see Database) |
| `imap`        | an IMAP server greets OK (anonymous) and, with credentials, LOGIN succeeds (see Database) |
| `pop` / `pop3` | a POP3 server greets +OK (anonymous) and, with credentials, USER/PASS succeeds (see Database) |
| `smtp`        | an SMTP server greets 220 + EHLO (anonymous) and, with credentials, AUTH PLAIN succeeds (see Database) |
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
| `sqlite` / `sqlite3` | a SQLite database file passes `PRAGMA integrity_check` (see SQLite) |

The `disk` check also verifies the **mount** of its `path` — see
[Disk and mount](configuration.md#host-watches).

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
    method: POST                       # default GET
    headers:
      Authorization: "Bearer ${token}" # any request headers
    json:                              # request body as JSON (sets Content-Type
      probe: true                      # automatically; or use `body:` for raw text)
    expect_status: 200                 # code, class (2xx) or list (default 200)
    expect_body: "ready"               # optional: response must contain this substring
    proxy: "http://user:pass@squid:3128"   # optional: route the request through a proxy (Squid)
    expect_json:                       # optional: response JSON must match (dotted paths)
      status: ok                       # equality (scalar)
      data.replicas: { op: ">=", value: 2 }   # operator: >, >=, <, <=, ==, !=, contains
      data.message: { op: contains, value: "healthy" }
```

It passes (health-style, `OK == true`) when the status matches **and** every
assertion holds. **`proxy`** routes the request through a forward proxy such as
**Squid** (`http://[user:pass@]host:port`; `http`, `https` or `socks5` schemes —
credentials, when present, go in the URL). This both monitors that the proxy
forwards correctly and that the target is reachable through it; for an `https://`
target the proxy is used via `CONNECT`, and certificate inspection (below) still
applies to the target's certificate. `json:` marshals the value and sets `Content-Type:
application/json` (override it via `headers`); `body:` sends a raw string. The
response is only read when `expect_body`/`expect_json` is set (capped at 1 MiB).
`expect_json` looks up **dotted paths** into nested objects. A scalar value is an
equality check (compared as a string); a `{op, value}` mapping uses an operator —
`>`, `>=`, `<`, `<=` (numeric), `==`, `!=` or `contains` (string) — handy for
asserting a JSON health endpoint's fields without parsing.

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
- `ajp` — default port 8009 (TCP). No auth. Sends an **AJP13 CPing** and expects
  a **CPong** — the same liveness probe Apache/nginx use against Tomcat's AJP
  connector. Probed natively (AJP13).
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
- `dns` — default port 53 (UDP). No auth. Sends an `A` query for `query`
  (default `localhost`) to the server and verifies it answers: `NOERROR` or
  `NXDOMAIN` pass (the server is up and speaking DNS); `SERVFAIL`, `REFUSED`, a
  timeout or a transport error fail. Result data carries the `rcode` and answer
  count. Probed natively (RFC 1035 message). Set `query` to a name the server
  should answer (e.g. a zone it is authoritative for).

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

```yaml
checks:
  db:
    type: mysql                 # mariadb, postgres, redis, valkey, imap, pop, smtp, ftp, ssh, ldap, ajp, ipp/cups, fpm, dns, ntp, snmp, tftp
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
scope: service   memory, cpu, process_count, io, io_read, io_write, fds, threads
scope: system    total_memory, total_cpu, load1, load5, load15
```

**Service metrics sum across the whole discovered process tree** — the matched
processes *and* their child/descendant processes — so a service's `cpu`,
`memory`, `io`, `fds`, etc. account for its workers and helpers, not just the
main process. `io`/`io_read`/`io_write` are byte/second rates over actual
block-layer I/O (`io` is read+write); `fds` is the open file-descriptor count and
`threads` the thread count.

`cpu`/`total_cpu` and the `io*` metrics are rates: they are **not ready** on the
first cycle and a condition over a not-ready value is false. A `%` threshold needs
a metric with a percentage form (`memory`, `cpu`, `total_memory`, `total_cpu`); a
bare number needs an absolute form (everything else, including `io*`/`fds`/
`threads`, which are absolute only). Reading another process's I/O or fd count
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
