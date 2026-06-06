# Checks, conditions and rules

## Checks

Checks are single-shot probes under `checks` (and `preflight`/`postflight`,
which reuse the same schema). MVP types:

| type          | passes when                                                        |
|---------------|--------------------------------------------------------------------|
| `tcp`         | a TCP connection to `host:port` succeeds                           |
| `http`        | the response status equals `expect_status` (default 200)           |
| `command`     | the command exits with `expect_exit` (default 0), array form only  |
| `service`     | the backend status equals `expect` (active/inactive/failed/unknown)|
| `file_exists` | a foreign flag/lock file exists (never under `<runtime>/locks`)     |
| `binary`      | a path exists and is executable                                    |
| `libraries`   | `ldd <binary>` resolves all shared libraries                       |
| `process`     | a process matching `exe`/`user` is in `state` (running/zombie/absent)|
| `metric`      | a sampled metric satisfies `op value` (see Metrics)                |

Each check has an optional `timeout` (else `engine.default_timeout`).

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
