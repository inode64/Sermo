# Safety

Sermo's safety invariants are **not configurable in YAML**. Validation rejects
any `security:` toggle that tries to disable them.

## Hard invariants

1. **Never start, restart, reload or resume if a required preflight fails.** A
   required preflight failure blocks the action with `preflight_failed`.
2. **Never start, stop, restart, reload or resume if a guard blocks the action.**
   Guards are evaluated before remediation; a remediation action a guard blocks
   never runs.
3. **Active named runtime locks always block service actions.** The operation
   engine checks `<runtime>/locks` automatically — no rule needed.
4. **Never signal an unverified residual.** `force_kill: auto` derives authority
   only from named `processes:` selectors with both an exact executable and real
   user; a selector marked `delegated: true` is never signalled and contributes
   no authority at all; `force_kill: false` disables escalation.
5. **Never kill by process name.** A kill requires an exact match on the
   resolved `/proc/<pid>/exe` path **and** the real UID against an explicit
   `kill_only_if` selector or one paired strict `processes:` identity. A
   `processes.<name>.cmd` regex narrows both discovery and the paired identity
   for shared binaries, so a daemon and its workload children never collapse
   into one kill set; cmdline only ever restricts and never authorizes a kill on
   its own. A process whose exe cannot be resolved
   (permission, or a `(deleted)` binary) is never killed — it is reported as a
   residual instead.
6. **Never send terminating signals to PID 1 or kernel threads.** `SIGTERM`,
   `SIGKILL`, `SIGINT` and `SIGQUIT` are blocked centrally for PID 1 and for
   kernel threads (`kthreadd`/children with no userspace exe or cmdline). This is
   not configurable; protected residuals are reported instead.
7. **`force_kill: true` requires `kill_only_if`** with both a `users` selector
   and an `exe_any` selector, each non-empty. **`force_kill: auto`** requires
   no broad fallback: it authorizes only strict `processes:` identities and
   leaves services without one as `orphan_processes`.
8. **Native restart does not weaken the common gates or fall back.** It still
   requires locks, preflight, guards, any available restart identity, timeout
   and postflight; a failed backend `Restart` is a failed operation, never an
   implicit staged stop/start.
9. **A stray process is never signalled without its own authorization.** A
   control-group member that no selector claims can only be signalled by
   `sermoctl reap --apply`, and only through the service's own
   `reap.kill_only_if` selector, checked by the same gate as every other kill. No
   rule action can reap, and a service with no `reap:` block reports its strays
   and signals none.

## The operation engine

Every start/stop/restart/reload/resume — manual (`sermoctl`) or automatic (`sermod`) —
runs through the same engine:

1. Acquire the internal operation lock (`<runtime>/ops/<service>.lock`); a live
   holder fails fast with exit `75` ("operation in progress").
2. Block on any active named runtime lock.
3. Run required preflight (start/restart/reload/resume).
4. Block if any guard blocks the action.
5. Execute the action's service-manager phase:
   - Before either restart mode, a stable backend `inactive`/`failed` state plus
     surviving non-delegated service processes triggers stale-init
     reconciliation under the normal stop policy. `unknown` and transitional
     states never enter the reaper. Status-query, discovery or reset errors
     return `failed`, and any survivor returns `orphan_processes`; neither path
     reaches the backend restart.
   - `stop` and `restart_policy.mode: staged` restart: stop, wait
     `graceful_timeout`, discover residual processes and apply the configured
     signal escalation. A restart never starts while residuals remain. The
     narrow socket-reactivation exception is unchanged: when an isolated systemd
     stop succeeded, every residual is backend-attributed to that same unit and
     the unit is already `active`, Sermo accepts the backend reactivation and
     does not issue a second start.
   - `restart_policy.mode: native` restart: after the guarded stale-init case
     above, invoke one bounded backend `Restart` for the primary unit. There is
     no ordinary Sermo stop phase, stopped-artifact cleanup or staged fallback.
     Auxiliary `also_service` units remain active.
   - `start`, `reload` and `resume`: run their existing bounded backend action.
6. After a clean explicit stop or staged-restart stop, reconcile the init's
   recorded state with reality — `systemctl reset-failed` (systemd) or
   `rc-service … zap` (OpenRC). Best effort: it never fails a stop that already
   succeeded.
7. Verify backend status where applicable and run required postflight for
   start/restart/reload/resume.

The dashboard's **close SSH session** is a separate manual engine operation,
never a rule action or automatic remediation. It takes the same operation and
named locks, guards, timeout and one-result event path, but does not restart or
postflight the SSH daemon. Immediately before the only signal, Sermo re-reads
the logged-in terminal and its `/proc` ancestry to an exact configured `sshd`
executable and real user, and requires the same terminal, session PID and
process start ticks.
Any missing boundary, changed terminal or recycled PID is rejected. A successful
close sends one `SIGTERM` to the per-session process; it never escalates to
`SIGKILL`.

The `terminal_sessions` check is observation-only. It runs a bounded,
argv-only `tmux` or `screen` listing as the explicitly configured account;
it never attaches, detaches, kills or otherwise controls a terminal session.
The separate manual close of an empty terminal source is available only for a
tmux source with an explicit configured socket. It shares the operation and
named locks, guards, timeout and one-event path; it re-lists the exact source,
requires a live server with zero sessions, invokes only `tmux -S SOCKET
kill-server` as the configured user, then verifies the namespace disappeared.
If tmux leaves a stale socket, it removes only the same socket generation
captured before the close after that verification (inode identity plus mtime,
so an inode recycled after unlink+recreate is not mistaken for the old socket);
a recreated socket is retained. Any missing server or newly active session
rejects the operation.

A residual Sermo is not allowed to identify and kill is **reported, not killed**:
a clean `orphan_processes` failure is safer than killing the wrong process.

Implementation contract: the engine registers exactly two deferred steps —
emit one event from the final result (registered first, so it fires on every
exit path), and release the operation lock (registered only after a successful
acquire). Every later step may return early; cleanup never repeats per return,
and a blocked, failed or panicking operation cannot leak the lock or skip its
event. Result statuses: `ok`, `blocked`, `preflight_failed`,
`postflight_failed`, `failed`, `orphan_processes`. A reload (SIGHUP) or shutdown
cancels an in-flight operation, so the engine's bounded waits report
`operation cancelled during <phase>` instead of a timeout: an interrupted action
must not read as a slow service, and every `--with-config` deployment reloads the
daemon. The engine does not
implement cooldown itself — that gates the *decision* to act and runs in the
daemon's rule evaluation before the engine is called, which is how manual and
automatic actions share one engine while only automatic remediation is rate
limited.

## Rate limiting

Only *automatic* remediation is rate limited (`cooldown`, `max_actions`,
`backoff`). Manual `sermoctl` actions are deliberate and not subject to cooldown,
but remain subject to locks, guards and preflight.
The automatic-remediation rate-limit state is stored in `paths.state`, so a
`sermod` restart or host reboot does not clear cooldown/backoff or the
`max_actions` window.

## Pausing monitoring

`sermoctl unmonitor SERVICE` pauses monitoring for a service; `monitor SERVICE`
resumes it. While paused, the daemon runs no checks, rules or remediation for that
service — useful during maintenance so a deliberate stop is not "remediated" by an
automatic restart. The pause is recorded in the persistent state store under
`paths.state` (the `monitor_state` table), so it persists across daemon
restarts and reboots until cleared. `sermoctl status SERVICE` shows
the single operator state `started` or `stopped` while monitoring is paused
(`"state": "started"`/`"stopped"` and `"paused": true` in `--json`). Pausing only
affects Sermo's monitoring; it does not stop the service itself, and manual
`sermoctl` actions still work.

A successful manual `stop` from `sermoctl` or the web UI also pauses monitoring
when the service was monitored. The state row records that the pause came from a
manual stop, so a later successful manual `start` restores monitoring only in
that case. If the service was already unmonitored before the stop, the later
start preserves that operator choice.

## System metrics

A `scope: system` metric ("is the machine under pressure?") is **not** a sound
trigger to restart one service, so it is allowed only in `alert` rules — never in
remediation rules, directly or via a check reference. See
[Metrics](rules.md#metrics) for the `scope: service` and `scope: system` metric
lists.

## Privileges: the daemon runs as root

`sermod` is designed to **run as root** (the packaged systemd unit and OpenRC
service do). It manages services owned by different users and touches privileged
areas, so several features need it:

- **Service control** — start/stop/restart/reload via systemd/OpenRC,
  start/stop/restart/resume of VM domains via libvirt when a service declares
  `control.type: libvirt`, and start/stop/restart/resume of Docker containers
  when it declares `control.type: docker`.
- **Signalling other users' processes** — the stop policy reaps residual
  processes that match the `kill_only_if` selector, across UIDs.
- **Cross-user `/proc` inspection** — resolving a process's `/proc/<pid>/exe`,
  status and the per-process IO (`/proc/<pid>/io`) of another user's process.
- **`icmp` checks** — opening a raw ICMP socket needs `CAP_NET_RAW` (root, or that
  capability granted to the binary).

It still **starts unprivileged**, but those features silently degrade, so it
**logs a warning at startup** when it is not root (`euid != 0`). Run it as root,
or grant the specific capabilities you need (e.g. `CAP_NET_RAW` for ICMP,
`CAP_KILL`/`CAP_SYS_PTRACE` for cross-user signalling/inspection) if you prefer a
least-privilege setup.

## Trust model

Because the daemon runs as root:

- **`then.expand` and `then.makestep` are policy-gated.** Both
  `then.makestep` change the host, so they run at most once per
  `policy.cooldown`, and every attempt starts the cooldown so a failing target is
  not retried each cycle. `then.makestep` — which asks the local chronyd to step
  the system clock — additionally *requires* a positive cooldown and acts only on
  an offset breach. **Never enable it on a ceph mon or osd host**: a clock jump
  can cost a monitor its quorum, so alert there instead.
- **The config is trusted, root-owned input.** `command` checks and watch `hook`s
  run their `argv` **as root** (never via a shell). Keep `/etc/sermo` writable
  only by root; anyone who can edit it can run code as root. Secrets belong in the
  environment (`${env:NAME}`), not in the file.
- **The web UI** (when enabled) can start/stop/restart/reload/resume services and
  monitor/unmonitor targets as root, so it is hardened by default: it **binds to
  loopback** (`127.0.0.1`), supports
  **authentication** with a read-only guest role, requires the **`X-Sermo-Csrf`
  header** on every state-changing request (blocking cross-site forgery from a
  browser), and sets HTTP timeouts. It speaks plain HTTP, so to reach it from off
  the host you **must** put it behind a TLS-terminating reverse proxy
  (nginx/Apache) — see
  [behind a reverse proxy](configuration.md#behind-a-reverse-proxy-required-to-expose-it).
  Keep `web.address` on loopback; never publish the port directly. The daemon logs
  a warning if the UI runs without authentication.
- **No shell, no name-based kills, no SIGKILL by default** — see the hard
  invariants above; these bound what even a misconfiguration can do.

## Locks

Two complementary blocking mechanisms guard operations:

1. **Named runtime locks** — files under `<paths.runtime>/locks` (default
   `/run/sermo/locks`), named `<service>[.<name>].lock`. The operation engine
   blocks automatically on any active one; no rule is needed. Created by
   `sermoctl lock` (wrap a command), `lock acquire` / `lock release`
   (see [cli.md](cli.md)).
2. **External lock checks gated by a guard** — a check (`file_exists`,
   `process`, …) over a signal Sermo does *not* own: a backup process, a
   foreign flag file. Never point such a check under `<paths.runtime>/locks` —
   that duplicates mechanism 1.

A service-created `lockfile:` in the catalog is different: it is a gated health
check for a regular runtime artifact, like `socket:`, and does not block
operations unless the operator also writes an explicit guard rule.

The **internal operation lock** (`<paths.runtime>/ops/<service>.lock`)
serializes start/stop/restart/reload/resume for one service. It is deliberately outside the
named-lock namespace so it cannot collide with a user lock named `op`, is never
listed as a named lock, and cannot be released by `sermoctl lock release`. A
live holder makes a second operation fail fast with exit `75` ("operation in
progress") — the engine never waits or queues.

Lock files are JSON:

```json
{
  "service": "mysql",
  "name": "backup",
  "reason": "backup mysql",
  "owner_pid": 12345,
  "owner_start_ticks": 884512,
  "created_at": "2026-06-05T12:00:00Z",
  "expires_at": "2026-06-05T16:00:00Z"
}
```

`owner_start_ticks` is the owner's start time (field 22 of
`/proc/<pid>/stat`), recorded so a stale lock can be told apart from a live one
even after PID reuse.

Lifecycle:

- **Acquire atomically** with `O_CREAT|O_EXCL`; write the JSON and fsync file
  and directory, so an existing lock is always complete and readable.
- A lock is **stale** (ignored, reclaimable) when its TTL elapsed, its owner
  PID is dead, or the PID is alive with a different start time (reuse). A live
  lock is **never silently overwritten**.
- **Reclaim is logged**: read, confirm still stale, unlink, acquire fresh;
  abort if it turned active in between.
- The wrap form unlinks the lock when the wrapped command exits (any path);
  the TTL still bounds the lock's lifetime if the owner crashes. Pick a TTL
  safely above the protected work's real duration — one that expires
  mid-backup would wrongly unblock restarts.

## Mount operations

Mount units (loaded from storage watch documents listed in `paths.watches`, when
they define `mount:`) are manual operator actions exposed by
`sermoctl mount|umount` and the Web UI **Mount units** panel; they are not
daemon-cycle remediation. They still use the same safety posture:

- Mount source, type and options come only from `/etc/fstab`. Sermo runs
  `mount <path>` / `umount <path>` with argv directly and a timeout; it never
  builds a shell command from YAML.
- Each target has an operation lock under `<paths.runtime>/mounts/ops`, so two
  callers cannot race the same mount.
- With `mount.refcount: true` (the default), `mount` increments a runtime counter and
  `umount` decrements it; the real unmount is attempted only when the counter
  reaches zero.
- The root filesystem (`/`) is never unmounted by Sermo. CLI and Web/API
  `umount`, blocker alerts and blocker signalling for `/` are rejected before any
  `umount`, process discovery or signal is attempted.
- Busy unmounts are reported with the processes using the mount. Sermo does not
  signal them unless the operator explicitly requests `sermoctl umount
  --kill-blockers` or checks `kill blockers` in the Web UI.
- The Web UI can send a native TTY alert to logged-in users that own current
  blockers. This uses the same Go TTY notifier as normal notifications; it does
  not run `wall`, `write` or a shell.
- Mount blocker signalling requires `mount.stop_policy.kill_only_if` with
  restrictive `users` and `exe_any` selectors. Only blockers that match that
  selector are signalled; cmdline is display data and never authorizes a kill.
- Forced and lazy unmount are per-action choices: `--force` / Web `force`
  permits `umount -f`, and `--lazy` / Web `lazy` permits `umount -l` as the last
  fallback.

## Process identity and matching

Kill decisions depend on how process facts are read, so this is fixed:

- **Exe** is the resolved target of `/proc/<pid>/exe` — the absolute real path
  of the running binary. It is matched by **exact equality** after canonicalizing
  both sides; no basename, prefix or substring matching.
- **UID** is the real UID from `/proc/<pid>/status`; user selectors match it
  exactly.
- **User/group names are resolved to numeric IDs before matching.**
  `engine.user_lookup` controls that lookup. Static `CGO_ENABLED=0` builds can
  use the default `auto` mode to fall back to `getent` for NSS-backed users
  while keeping the Sermo binary static. If a configured name cannot be
  resolved, the selector fails closed and no process is matched or signaled by
  that name. Numeric UID/GID selectors remain deterministic.
- **Cmdline** is normally display/logging data, but a `processes.<name>.cmd` field
  is an explicit RE2 regex over the joined argv. Use it only to make discovery
  more specific when the same executable runs several roles, e.g. Java or QEMU
  wrappers. Cmdline is spoofable, so it does not satisfy `kill_only_if` and does
  not make a process killable by itself.
- A selector with several fields (`exe`, `cmd`, `user`, `group`) requires **all**
  of them to match.
- **Unresolvable exe fails safe**: if `/proc/<pid>/exe` cannot be read or
  resolves to a `(deleted)` path (binary replaced by an upgrade), the process
  matches no exe selector — it is reported as a residual with exe unknown and
  is never signaled. Sermo does record *which* path a deleted binary occupied,
  so the `stale_binary` check can name it, but that is diagnostic only: such a
  process still resolves no exe, matches nothing and is never signaled. Do not
  make a deleted path authorize matching or killing.
- **PID 1 and kernel threads are protected** from terminating signals even if a
  future selector or signal path would otherwise target them. Non-terminating
  reload signals such as `SIGHUP` are not blocked by this guard.
- **Native signal reloads use the same identity model.** On OpenRC, or any
  service with no backend `MainPID`, the pidfile PID is signaled only after it
  matches a `processes:` selector with exact `exe` and `user`. Catalog authors
  must verify each shipped init script, pidfile fallback and identity selector
  together before declaring `reload.signal`.

Discovery order: backend information (systemd MainPID/cgroup; OpenRC status)
→ configured pidfiles → `processes:` selectors → child process tree from
`/proc`, deduplicated by PID.
For `pidfiles:` maps, each pidfile role must be backed by a same-named
`processes:` selector with exact `exe` and `user`; the pidfile is evidence, not
a name-only authority.

## Stop and signal escalation

`stop_policy` fields omitted by a catalog service or service inherit from
`defaults.stop_policy`. The stop phase of an explicit stop or a `staged`
restart:

1. Backend `Stop`, wait `graceful_timeout`, discover residuals.
2. No residuals → clean stop.
3. Residuals with `force_kill: false` → `orphan_processes` (and a restart does
   **not** start).
4. Residuals with `force_kill: true` or `auto` → classify each one: KILLABLE
   only when every explicit `kill_only_if` field matches, or when it matches a
   single paired strict `processes:` identity (exact resolved exe **and** real
   UID; unresolvable exe and protected PIDs are never killable). SIGTERM the
   killable set, wait `term_timeout`, rediscover; SIGKILL what remains of the
   killable set, wait `kill_timeout`, rediscover. A residual that never matched
   is never signaled.
5. The result is `ok` only when no residuals remain at all — whether the
   survivor was deliberately spared or outlived SIGKILL, the result is
   `orphan_processes` and lists every remaining process.

## Stray processes and `reap`

A **stray** is a process the init backend attributes to the service's control
group that no configured selector claims (no `processes:` match, no pidfile), that
is not the unit's principal process, and that is not part of that principal's live
process tree.

Control-group membership is the kernel's own attribution, so a stray does belong
to the service — Sermo just cannot say what it is. Excluding the principal's tree
is what makes the label useful: a daemon's workers are its descendants, so a
healthy unit produces no strays at all, while a process that reached the control
group without an ancestry chain back to the principal was reparented to PID 1.
That is the signature of a leftover — a probe that daemonized, a child the daemon
never reaped, a survivor of an earlier incarnation.

Strays appear in `sermoctl processes` as `stray=true`, in the dashboard's process
table with `stray` in the Role column, and as the injected `strays` check (see
[configuration.md](configuration.md)). Nothing else changes: a stray is still
discovered, still counted in the service's process totals, and still a residual of
a stop like any other process.

### A stop never reaps

`reap.kill_only_if` is **not** consulted during a stop, so a restart never clears a
stray. The stop phase signals exactly what `stop_policy` authorizes, and a stray it
cannot identify is reported, not killed.

Whether that blocks a restart depends on the unit's `KillMode`:

- `KillMode=control-group` (the systemd default): the stop takes the whole control
  group with it, so no stray survives to be a residual and nothing changes.
- `KillMode=process` / `none` (sshd, NetworkManager, libvirt's daemons): survivors
  remain. Those a `delegated: true` selector claims are excluded from residuals by
  design; a stray is not, so it ends the operation in `orphan_processes` with the
  service left stopped.
- `restart_policy: native`: the init backend performs one atomic restart, so there
  is no residual phase at all.

In that third case the result names the strays and points at the verb that clears
them:

```console
$ sermoctl restart ssh
ssh restart orphan_processes
reason: 1 residual process(es) remain after stop (1 stray, unaccounted for by any
  selector; `sermoctl reap` lists them and, with reap.kill_only_if declared, clears them)
  residual pid=4711 exe=/usr/bin/tmux stray=true
```

The operator then chooses: mark it `delegated: true` if the unit keeps it alive on
purpose, add a selector if it is a role Sermo should know, or declare
`reap.kill_only_if` and clear it. Making a stop reap on its own would mean
automatic remediation killing processes Sermo cannot name, which is the risk this
design refuses.

`sermoctl reap SERVICE` lists them and reports how many would be signalled. It
takes no lock, emits no event and touches nothing.

`sermoctl reap SERVICE --apply` signals them through the normal operation path —
operation lock, active named runtime locks, guards, exactly one event — and
relaxes no invariant:

- Authority comes only from the service's own `reap.kill_only_if`, the same paired
  `users` + `exe_any` selector `stop_policy` uses, checked by the same gate.
  Without the block nothing is authorized, so `--apply` reports every stray and
  signals none.
- Delegated processes, an unresolvable exe, PID 1 and kernel threads are refused
  exactly as they are during a stop.
- Escalation is SIGTERM, `term_timeout`, rediscover, SIGKILL, `kill_timeout`,
  rediscover, using the service's own `stop_policy` timings and re-reading live
  `/proc` between rounds.
- The result is `ok` only when no stray remains; a spared or surviving one makes it
  `orphan_processes` and lists what is left.

No rule action can reap. Reaping means terminating a process Sermo cannot name,
and that decision stays with the operator.

### sermod's own startup hygiene

sermod terminates whatever it finds in **its own** init unit control group when it
starts, before it has spawned anything itself — so anything there belongs to a
previous incarnation the init system did not clean up (`KillMode=process` or
`KillMode=none`). This is the one exception to "all service signalling goes
through the operation engine", and it is deliberately narrow:

- Only sermod's own control group, and only when that group is a systemd
  **service** unit. Started from a login shell sermod shares its scope with the
  operator's shell and sshd, so it does nothing at all there.
- `SIGTERM` only. A leftover that ignores it is reported and left alone.
- One event per process signalled.
- `engine.reap_own_strays: false` turns it off.

## Scheduler and concurrency

Each enabled service is monitored by its own worker with an independent ticker
at `engine.interval` (per-service `interval` overrides). Workers never share a
cycle: a multi-minute restart on one service cannot block monitoring of
another. Within a service the cycle is synchronous — checks, rule evaluation,
then at most one operation.

- **Tick overlap**: if a worker's cycle is still running when its next tick
  fires, that tick is **skipped, not queued** — an overrunning operation causes
  skips, never a backlog of catch-up cycles. Skips are per service and logged.
- **Jitter**: workers start with a small per-service offset so ticks spread
  across the interval.
- **Bounded concurrency**: each service runs at most one operation at a time
  (the cross-process operation lock), and automatic remediation is rate-limited
  by the mandatory per-service `policy` block (cooldown, `max_actions`,
  backoff). Check execution shares a global pool
  (`engine.max_parallel_checks`). A check that cannot get a slot waits — it is
  not skipped.
- **Shutdown** (SIGTERM/SIGINT): stop starting cycles, cancel worker contexts;
  an in-flight operation observes cancellation, its deferred cleanup releases
  the lock and emits the event, and a partially stopped service is left as-is —
  never force-killed because of shutdown.
- **Daemon reload** validates the new config, swaps workers/watches while
  preserving per-service runtime state, and keeps the running generation when
  the new config is invalid.
