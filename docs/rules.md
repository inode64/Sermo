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
    expect_json:                       # optional: response JSON must match (dotted paths)
      status: ok                       # equality (scalar)
      data.replicas: { op: ">=", value: 2 }   # operator: >, >=, <, <=, ==, !=, contains
      data.message: { op: contains, value: "healthy" }
```

It passes (health-style, `OK == true`) when the status matches **and** every
assertion holds. `json:` marshals the value and sets `Content-Type:
application/json` (override it via `headers`); `body:` sends a raw string. The
response is only read when `expect_body`/`expect_json` is set (capped at 1 MiB).
`expect_json` looks up **dotted paths** into nested objects. A scalar value is an
equality check (compared as a string); a `{op, value}` mapping uses an operator —
`>`, `>=`, `<`, `<=` (numeric), `==`, `!=` or `contains` (string) — handy for
asserting a JSON health endpoint's fields without parsing.

### Cert

A `cert` check connects to a TLS endpoint, reads its leaf certificate and alerts
(`OK == true`) on any configured problem. It is condition-style, so as a watch the
hook/notify fires on a problem and in rules `active: {check: api-cert}` is true.

```yaml
checks:
  api-cert:
    type: cert
    host: api.example.com      # required
    port: 443                  # optional, default 443
    server_name: api.example.com   # optional SNI + hostname to verify (default = host)
    expires_in_days: 14        # optional: warn this many days before expiry
    verify: true               # optional, default true: chain + hostname + validity
    on_algorithm_change: true  # optional: alert when the signature algorithm changes
    on_issuer_change: true     # optional: alert when the issuer (CA) changes / re-issue
    on_change: false           # optional: alert on any certificate rotation (fingerprint)
```

It alerts when the certificate is **expired or not yet valid**, **expires within
`expires_in_days`**, fails chain/hostname **verification** (`verify`, on by
default — catches self-signed, wrong host, expired chains), or — between cycles —
its **signature algorithm**, **issuer** or **fingerprint** changes. Result data
exposes `days_left`, `not_after`, `issuer`, `signature_algorithm`,
`public_key_algorithm` and `fingerprint`. A network/TLS error fetching the cert is
**not** an alert (use a `tcp`/`http` check for reachability).

The change conditions are **stateful** (they remember the previous value across
cycles), so they work as a **host watch** (built once); as a per-service check —
where checks are rebuilt each cycle — only the level conditions (expiry, validity)
apply.

Each check has an optional `timeout` (else `engine.default_timeout`) and an
optional `interval` to run it less often than the worker cycle — every
`round(interval / resolution)` cycles, reusing its last result in between (see
[per-check interval](configuration.md#per-check-interval)).

### Checks and host watches are the same types

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
scope: service   memory, cpu, process_count
scope: system    total_memory, total_cpu, load1, load5, load15
```

`cpu`/`total_cpu` are rates: they are **not ready** on the first cycle and a
condition over a not-ready value is false. A `%` threshold needs a metric with a
percentage form; a bare number needs an absolute form.

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
