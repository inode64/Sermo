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
4. **Never SIGKILL by default.** `force_kill` is false unless explicitly enabled.
5. **Never kill by process name.** A kill requires an exact match on the
   resolved `/proc/<pid>/exe` path **and** the real UID against an explicit
   `kill_only_if` selector. A `processes.<name>.cmd` regex may narrow process
   discovery for shared binaries, but cmdline never authorizes a kill; a process
   whose exe cannot be resolved (permission, or a `(deleted)` binary) is never
   killed — it is reported as a residual instead.
6. **Never send terminating signals to PID 1 or kernel threads.** `SIGTERM`,
   `SIGKILL`, `SIGINT` and `SIGQUIT` are blocked centrally for PID 1 and for
   kernel threads (`kthreadd`/children with no userspace exe or cmdline). This is
   not configurable; protected residuals are reported instead.
7. **`force_kill: true` requires `kill_only_if`** with both a `users` selector
   and an `exe_any` selector, each non-empty.

## The operation engine

Every start/stop/restart/reload/resume — manual (`sermoctl`) or automatic (`sermod`) —
runs through the same engine:

1. Acquire the internal operation lock (`<runtime>/ops/<service>.lock`); a live
   holder fails fast with exit `75` ("operation in progress").
2. Block on any active named runtime lock.
3. Run required preflight (start/restart/reload/resume).
4. Block if any guard blocks the action.
5. For stop/restart, stop, wait `graceful_timeout`, discover residual processes.
6. If residuals remain and `force_kill` is false → `orphan_processes`; a failed
   restart does **not** start. If true, SIGTERM then SIGKILL only the processes
   that exactly match `kill_only_if`, rediscovering between steps.
7. After a clean stop (no residuals), reconcile the init's recorded state with
   reality — `systemctl reset-failed` (systemd) or `rc-service … zap` (OpenRC) —
   so a lingering failed/stuck marker can't disagree with the actual processes.
   Best effort: it never fails a stop that already succeeded.
8. For start/restart, start and verify status; for reload, reload in place; for
   resume, resume the target and verify status. Run required postflight for
   start/restart/reload/resume.

A residual Sermo is not allowed to identify and kill is **reported, not killed**:
a clean `orphan_processes` failure is safer than killing the wrong process.

Implementation contract: the engine registers exactly two deferred steps —
emit one event from the final result (registered first, so it fires on every
exit path), and release the operation lock (registered only after a successful
acquire). Every later step may return early; cleanup never repeats per return,
and a blocked, failed or panicking operation cannot leak the lock or skip its
event. Result statuses: `ok`, `blocked`, `preflight_failed`,
`postflight_failed`, `failed`, `orphan_processes`. The engine does not
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

- **The config is trusted, root-owned input.** `command` checks and watch `hook`s
  run their `argv` **as root** (never via a shell). Keep `/etc/sermo` writable
  only by root; anyone who can edit it can run code as root. Secrets belong in the
  environment (`${env:NAME}`), not in the file.
- **The web UI** (when enabled) can start/stop/restart/reload/resume services and
  monitor/unmonitor targets as root, so it is hardened by default: it **binds to
  loopback** (`127.0.0.1`), supports
  **authentication** with a read-only guest role, requires the **`X-Sermo-CSRF`
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

Mount units (loaded from storage documents under `paths.storages`, default
`/etc/sermo/storages`, when they define `mount:`) are manual operator actions exposed by
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
- Busy unmounts are reported with the processes using the mount. Sermo does not
  signal them unless `mount.umount.allow_sigkill: true` or
  `mount.stop_policy.force_kill: true` is explicitly configured.
- The Web UI can send a native TTY alert to logged-in users that own current
  blockers. This uses the same Go TTY notifier as normal notifications; it does
  not run `wall`, `write` or a shell.
- Any mount policy that can send SIGKILL must define
  `stop_policy.kill_only_if` with restrictive `users` and `exe_any` selectors.
  Cmdline narrowing may help discovery, but it never authorizes a kill by
  itself.
- Lazy unmount (`umount -l`) is disabled unless `umount.allow_lazy: true` is set
  on that mount.

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
  is never signaled.
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
`defaults.stop_policy`. The stop phase of a stop/restart:

1. Backend `Stop`, wait `graceful_timeout`, discover residuals.
2. No residuals → clean stop.
3. Residuals with `force_kill: false` → `orphan_processes` (and a restart does
   **not** start).
4. Residuals with `force_kill: true` → classify each one: KILLABLE only when
   every `kill_only_if` field matches (exact resolved exe **and** real UID;
   unresolvable exe and protected PIDs are never killable). SIGTERM the killable set, wait
   `term_timeout`, rediscover; SIGKILL what remains of the killable set, wait
   `kill_timeout`, rediscover. A residual that never matched is never signaled.
5. The result is `ok` only when no residuals remain at all — whether the
   survivor was deliberately spared or outlived SIGKILL, the result is
   `orphan_processes` and lists every remaining process.

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
- **Bounded concurrency**: operations across all services share the global
  semaphore (`engine.max_parallel_operations`); check execution shares a
  separate global pool (`engine.max_parallel_checks`). A check that cannot get
  a slot waits — it is not skipped.
- **Shutdown** (SIGTERM/SIGINT): stop starting cycles, cancel worker contexts;
  an in-flight operation observes cancellation, its deferred cleanup releases
  the lock and emits the event, and a partially stopped service is left as-is —
  never force-killed because of shutdown.
- **Daemon reload** validates the new config, swaps workers/watches while
  preserving per-service runtime state, and keeps the running generation when
  the new config is invalid.
