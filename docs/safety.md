# Safety

Sermo's safety invariants are **not configurable in YAML**. Validation rejects
any `security:` toggle that tries to disable them.

## Hard invariants

1. **Never start, restart, reload or resume if a required preflight fails.** A
   required preflight failure blocks the action with `preflight_failed`.
2. **Never start, stop, restart, reload or resume if a guard blocks the action.**
   Guards are evaluated before remediation; a remediation action a guard blocks
   never runs. A guard that references a malformed check also denies the action,
   reporting the original construction error, including for optional checks.
   A guard that blocks `start` also denies `restart` and `repair`, and one that
   blocks `stop` also denies `restart`: what an action performs, not its name,
   decides which guards apply.
3. **Active named runtime locks always block service actions.** The operation
   engine checks `<runtime>/locks` automatically — no rule needed.
4. **Never signal an unverified residual.** `force_kill: auto` derives authority
   only from its explicit `kill_only_if`, which then replaces the derived pairs,
   or else from named `processes:` selectors with both an exact executable and
   real user; a selector marked `delegated: true` is observe-only: never signalled,
   never a residual and contributes no authority at all; `force_kill: false`
   disables escalation to `SIGKILL`.
   The only other authority is kernel attribution: a residual the kernel places
   in the service's own control group (systemd `<unit>.service`, OpenRC
   `openrc.<unit>`) may receive `SIGTERM`, and `SIGKILL` only with `force_kill`
   (`true` or `auto`). A process `stop_policy.keep_children` keeps is delegated
   and never reaches this authority.
5. **Never kill by process name.** A kill requires an exact match on the
   resolved `/proc/<pid>/exe` path **and** the real UID against an explicit
   `kill_only_if` selector or one paired strict `processes:` identity, or
   kernel proof that the residual is a member of the service's own control
   group (revalidated through the pidfd before delivery), or — only for the
   manual close of a residual SSH `sudo` terminal — the verified session
   evidence described in [The operation engine](#the-operation-engine). A
   `processes.<name>.cmd` regex narrows both discovery and the paired identity
   for shared binaries, so a daemon and its workload children never collapse
   into one kill set; cmdline only ever restricts and never authorizes a kill on
   its own. An unreadable executable never authorizes a signal. A deleted executable
   permits residual cleanup only when the kernel-held file, exact previous path,
   real UID and service ownership are verified as described below.
6. **Never send terminating signals to PID 1 or kernel threads.** `SIGTERM`,
   `SIGKILL`, `SIGINT` and `SIGQUIT` are blocked centrally for PID 1 and for
   kernel threads (`kthreadd`/children with no userspace exe or cmdline). This is
   not configurable; protected residuals are reported instead.
7. **`force_kill: true` requires `kill_only_if`** with both a `users` selector
   and an `exe_any` selector, each non-empty. **`force_kill: auto`** requires
   no broad fallback: it authorizes only its explicit `kill_only_if` (held to
   the same two non-empty selectors), which then replaces the derived pairs, or
   else strict `processes:` identities, and leaves services without either as
   `orphan_processes` unless their residuals are proven members of the unit's
   control group.
8. **Restart always verifies stop and start under the common gates.** Locks,
   preflight, guards, process identity, timeout and postflight wrap the complete
   operation. An init command error is recoverable only after verifying the
   requested state; surviving processes or uncertain evidence block a new start.
9. **A stray process is a diagnostic, never a target of its own.** A
   control-group member that no selector claims is reported, and no command,
   dashboard button or rule action signals it as a stray. A stop or restart
   handles it like any other residual under invariant 4: `SIGTERM` when the
   kernel places it in the service's own control group, `SIGKILL` only with
   `force_kill`, nothing at all when the service sets
   `stop_policy.keep_children`. See [Stray processes](#stray-processes).
10. **`process_policy` only observes and alerts, and `unowned_processes`
    detection authorizes nothing.** `process_policy` has no operation runner,
    service backend or signal path: validation permits only `then.notify` and
    `then.notify_interval`, so a policy violation cannot restart, repair, kill
    or otherwise alter a process or service. `unowned_processes` lists the
    processes no init unit accounts for or whose executable belongs to no
    installed package; being listed never makes a process a signal target. Its
    `then.kill` signals only what its own `kill_only_if` (users **and**
    `exe_any`) authorizes, through the same gate as every other kill, and a
    dashboard kill signals only a process an administrator picked whose
    identity (PID, start time, exact executable, real user) was re-read
    immediately before — SIGTERM, with SIGKILL only on explicit request. Both
    honour the watch's `dry_run`. When attribution is impossible (no per-unit
    control groups, no package database, an unreadable executable) the watch
    reports that and lists nothing on that criterion.
11. **A libvirt virtual network with live guest interfaces is never
    destroyed.** `control: libvirt-network` stop/restart verifies every
    non-shut-off domain (paused and crashed included — their taps stay
    attached) against the network name and its bridge; any attachment, or any
    guest that cannot be verified, blocks the destroy. No configuration option
    relaxes this.
12. **A database statement is cancelled only after it is re-verified.** A
    `kill_query` (manual or the opt-in automatic one) re-reads the target and
    requires the exact listed statement identity before acting; a changed or
    finished statement is never killed. See
    [Database statement kills](#database-statement-kills).
13. **A busy server is never restarted, started, stopped, reloaded or
    resumed for being busy.** A server that answers "too many connections",
    "loading" or "starting up" is alive; no rule operation fires on that
    answer and no option re-enables it. See [Busy servers](#busy-servers).

## The operation engine

Every start/stop/restart/reload/resume — manual (`sermoctl`) or automatic (`sermod`) —
runs through the same engine. The manual-only `repair` and `pause` actions use
that engine too, but are never eligible for automatic remediation. `pause`
(libvirt suspend, Docker pause) runs no preflight, like `stop`, but honors locks
and guards — a guard that blocks `stop` also blocks `pause` — and succeeds only
once the backend reports the target `paused`. A successful manual pause pauses
monitoring, as a manual stop does, until a successful manual `resume` (or
`start`, `restart` or `repair`) restores it; see
[Pausing monitoring](#pausing-monitoring).

The daemon worker, Web UI and CLI build that engine from the same resolved
service runtime: control target, backend process roots, process selectors, check
dependencies, metric source and operation locks. `sermoctl preflight` uses the
same prepared target and check dependencies. A CLI command prepares each target
once and reuses it for reload capability, the action and the bounded status
query after a failed postflight, so changing the caller cannot change a safety
decision.

1. Acquire the internal operation lock (`<runtime>/ops/<service>.lock`); a live
   holder fails fast with exit `75` ("operation in progress").
2. Block on any active named runtime lock.
3. Run required preflight (start/restart/reload/resume/repair).
4. Block if any guard blocks the action.
5. Before start/restart, compare init state with fresh process evidence:
   - An active service's restart also observes external deleted-executable
     candidates before stop. A known unkillable candidate blocks the restart
     while the current daemon is still running. Current unit members retain
     their normal graceful stop, including a replaced main executable.
   - Stable `inactive`/`failed` with surviving processes that are neither
     delegated nor kept triggers cleanup under `stop_policy`. Unmatched
     survivors block start.
   - `active` with a proven-absent resident daemon triggers reconciliation.
     OpenRC uses `zap`; systemd uses stop and clears a failed marker with
     `reset-failed`. An already inactive systemd unit needs no reset: systemd
     may unload a cleanly stopped unit before reconciliation. An unreadable or
     transitional state blocks reconciliation; reset failures remain errors.
     Unknown/transitional state, incomplete reads and missing identity do not
     prove a divergence. OpenRC `inactive` (started, readiness still pending,
     as with `mark_service_inactive`) is transitional, not a stable stop, and
     reports `unknown`. Process-free services retain their own lifecycle.
6. Restart always composes stop and start, never a backend restart command.
   Stop waits up to `graceful_timeout`, discovers residuals and applies the configured
   signal escalation. Incomplete rediscovery stops escalation, including
   SIGKILL. Before starting, Sermo revalidates process absence,
   reconciles init bookkeeping and verifies its inactive state. A reset error
   or an inconsistent state blocks the next phase. Previously observed process
   generations remain tracked if they leave the cgroup or stop matching a
   selector; disappearance from discovery is not proof of exit.
7. A stop command error is retained while Sermo checks the actual outcome. Only
   confirmed process absence and successful init reconciliation permit recovery.
   A start command error requires a trusted live process and active init state;
   postflight must also pass. Recovered errors remain warnings in the single
   auditable result, including when a later phase fails. Auxiliary-stop and
   stopped-artifact warnings are retained too. Cancellation/timeout never
   extends the operation deadline.
8. A socket/D-Bus reactivation during restart can replace the start phase only
   when the same systemd unit is active with trusted backend processes from new
   generations and no old non-delegated generation remains anywhere in the
   process snapshot, including outside the unit's current cgroup. PID alone or an
   unchanged active daemon is insufficient evidence. Auxiliary units are still
   started again; only the primary start is skipped.
9. Verify backend and resident-process state before sampling required postflight
   checks. A settling attempt never runs or caches a successful probe; losing
   readiness invalidates earlier postflight success. Verify required postflight for
   start/restart/reload/resume/repair. Reload/resume command errors remain errors:
   a running process alone cannot prove those effects.

`repair` is intentionally narrower than a general cleanup command. It first
requires the init backend to report the service failed or inactive. It can then
remove only a regular pidfile below `/run` whose exact PID is absent from
`/proc`; a live PID, a PID or process table that cannot be read completely, a
malformed file, symlink or non-runtime path fails closed.
It then applies the same survivor reconciliation as start (step 5): surviving
processes that are neither delegated nor kept are cleaned up under
`stop_policy` or block the repair with `orphan_processes`. Only then, for a failed unit, does it clear the init
backend's failed marker, after revalidating process absence and verifying the
backend reports inactive, before the normal guarded start and postflight. A
repair never starts a second instance beside a survivor. It must still report
`active` at the end of postflight, including for services without resident
processes; `inactive` or `unknown` cannot count as a successful repair.

Stop-artifact cleanup (`clean_on_stop`, pidfiles and `files_absent` with
`clean_after_stop`) rejects symlinked ancestors at deletion time. Each parent
is pinned before deleting, so replacing an ancestor after config validation or
during traversal cannot redirect removal into another tree. A symlink at the
final path is removed as a link; its target is preserved. Cleanup failures are
retained as warnings in the operation result.

The dashboard's **close SSH session** is a separate manual engine operation,
never a rule action or automatic remediation. It takes the same operation and
named locks, guards, timeout and one-result event path, but does not restart or
postflight the SSH daemon. For a connected session, immediately before the only signal, Sermo re-reads
the logged-in terminal and its `/proc` ancestry to an exact configured `sshd`
executable and real user, and requires the same terminal, session PID and
process start ticks. The verified session executable and real UID are carried
through to pidfd signal revalidation for ordinary and sudo sessions alike.
An unreadable session executable disables direct close.
Any missing boundary, changed terminal or recycled PID is rejected. A successful
connected-session close sends one `SIGTERM` to the per-session process; it never escalates to
`SIGKILL`.
An SSH terminal whose ancestry cannot be verified remains visible as an
unavailable issue. On systemd, a remote issue with a live utmp leader also
exposes its PID and a login1-managed close. That path sends no signal to the
uncertain process: immediately before `TerminateSession`, it requires unchanged
process start ticks plus an exact login1 session ID, leader PID, terminal,
`Remote=true` and `Service=sshd`. Other unavailable issues remain non-actionable.

A remote terminal left alive by `sudo` after SSH disconnects is shown as
`residual`, not as an active network connection. Its manual close needs no
configuration: the authority is the verified session evidence itself, never a
`kill_only_if` selector. Sermo requires:

- a remote utmp session whose leader is reparented to PID 1 with no
  controlling terminal;
- a leader whose resolved executable is exactly `/usr/bin/sudo` or
  `/bin/sudo` and whose real UID is the account utmp records (sudo's effective
  root UID is not its real user identity);
- a direct monitor child on the displayed PTY running the same executable with
  the same real UID;
- valid process start ticks for both, and both in the non-root cgroup v2 of a
  live, exactly configured sshd listener.

The session verifier re-checks the whole boundary, cgroup included, before the
first signal and again before escalating, and each delivery revalidates the
process through a pidfd: generation, executable and real UID. Sermo sends
`SIGTERM`, waits up to the
service's `stop_policy.term_timeout`, verifies the survivors again, sends
`SIGKILL` to what is left of the pair and waits for exit. A stopped child can
keep sudo alive after TERM, so successful signal delivery alone is not success.
No other session, listener, workload PID or entire cgroup is directly signalled.
A replaced workload binary does not invalidate the verified sudo boundary, but
a replaced sudo binary does. Missing cgroup evidence fails closed. No automatic
remediation uses this path. The session user remains the account recorded by
utmp, which may be the account selected through sudo rather than the original
SSH user.

Administrators always see a close button on SSH session and attribution-issue
rows. It is disabled, with an explanation, when no safe close identity is
available; visibility never substitutes for backend verification.

For direct and residual closes, Sermo waits for the selected process generation
to exit before reporting success. A surviving or unreadable process yields an
error, no later than the operation timeout, rather than a successful
signal-delivery report.

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
event. A panic is audited as `failed`, never as the `ok` the result starts
with, and still propagates to the caller. Result statuses: `ok`, `blocked`, `preflight_failed`,
`postflight_failed`, `failed`, `orphan_processes`. A reload (SIGHUP) or shutdown
cancels an in-flight operation, so the engine's bounded waits report
`operation cancelled during <phase>` instead of a timeout: an interrupted action
must not read as a slow service, and every `--with-config` deployment reloads the
daemon. The engine does not
implement cooldown itself — that gates the *decision* to act and runs in the
daemon's rule evaluation before the engine is called, which is how manual and
automatic actions share one engine while only automatic remediation is rate
limited.

### Database statement kills

A [`db_queries`](rules.md#running-database-statements-db_queries) watch lists
the statements a MySQL, MariaDB or PostgreSQL server is running. Cancelling one
is the `kill_query` operation. It sends no OS signal; it asks the database
server to cancel a statement or close a connection.

**Who can request it.** An administrator, from the dashboard's Sessions panel or
with `sermoctl sessions kill SERVICE WATCH ID [--connection]`
(`POST /api/services/{name}/db-queries/{watch}/kill`), and a service watch's own
opt-in `then.kill_query`. It is never a rule action, and a host watch cannot
kill a statement: the operation needs a service's engine. (The host watches
that signal OS processes do so only through the shared kill gate: a
`type: process` watch's [`then.kill`](configuration.md#thenkill--terminate-the-matched-process)
is bound to the exact executable and user its check matches, and
[`unowned_processes`](configuration.md#unowned_processes--processes-no-unit-or-package-accounts-for)
is bound by invariant 10 above.)

**The request carries only an identity.** The client sends the statement id and
the opaque identity the inventory displayed. The connection, credentials and
engine come from the service's own configured watch, never from the request.

**One engine path.** The kill runs through the service's operation engine like
a session close: operation lock, named runtime locks, guards (`blocks:
[kill_query]` — only that entry denies it, since a kill neither starts nor stops
the service), the operation timeout and exactly one audit event with action
`kill_query`. Panic mode suppresses the automatic kill; a manual kill stays
available, like every other manual operation.

**Re-verification.** Immediately before acting, the engine opens a fresh
connection, reads only the requested statement and requires the same connection
id *and* the same statement (its fingerprint and start time). A statement that
finished, or a connection now running another statement, is refused with "the statement
is no longer running; refresh the list". The automatic kill additionally
re-checks its own condition (`after` and the `users`/`databases` selector) on
that fresh sample.

**What is cancelled, per engine.**

- **MariaDB** cancels by statement: `KILL QUERY ID <query_id>` stops exactly the
  verified statement and can never reach a later one on the same connection.
- **MySQL** has no per-statement kill: `KILL QUERY <id>` targets the connection.
  If the verified statement ends in the sub-second gap between the target read
  and the `KILL`, the next statement of that connection may be cancelled
  instead. That race is inherent to MySQL and is why the automatic kill must be
  scoped to named users or databases.
- **PostgreSQL** verifies and signals in **one** statement:
  `pg_cancel_backend(pid)` (or `pg_terminate_backend`) runs only for the row
  whose pid, `backend_start` and `query_start` still match, so a recycled pid or
  a new statement is never signalled. A backend `idle in transaction` runs no
  statement: cancelling it would succeed and stop nothing, so `mode: query`
  refuses it and only `mode: connection` ends such a session.
- `mode: connection` (`--connection`) closes the whole connection
  (`KILL CONNECTION`, `pg_terminate_backend`) instead of cancelling the
  statement; the client sees its session dropped.

**Automatic kill contract.** `then.kill_query` is opt-in per service watch and
never shipped by the catalog (a catalog test enforces it). Validation requires
`after` at least the check's `min_duration`, at least one of `users` or
`databases`, and a `policy:` with a positive `cooldown`. At most one statement is
killed per cycle, the longest eligible first. The watch's policy budget is its
own — it is not the service's restart budget, and a kill never consumes or
resets it. A kill the policy holds back, a dry-run (`would kill_query`) and a
panic-suppressed kill are each reported once per statement. `dry_run: true`
never kills.

**Redaction.** Statement text is redacted (`IDENTIFIED BY`, `PASSWORD()`,
`SET PASSWORD`, `MASTER_PASSWORD`/`SOURCE_PASSWORD`, PostgreSQL
`PASSWORD '…'`) before it reaches the inventory, events, notifiers or hooks.

## Rate limiting

Only *automatic* remediation is rate limited (`cooldown`, `max_actions`,
`backoff`). Manual `sermoctl` actions are deliberate and not subject to cooldown,
but remain subject to locks, guards and preflight.
The automatic-remediation rate-limit state is stored in `engine.state`, so a
`sermod` restart or host reboot does not clear cooldown/backoff or the
`max_actions` window.

## Pausing monitoring

`sermoctl unmonitor SERVICE` pauses monitoring for a service; `monitor SERVICE`
resumes it. While paused, the daemon runs no checks, rules or remediation for that
service — useful during maintenance so a deliberate stop is not "remediated" by an
automatic restart. The pause is recorded in the persistent state store under
`engine.state` (the `monitor_state` table), so it persists across daemon
restarts and reboots until cleared. `sermoctl status SERVICE` shows
the single operator state `started` or `stopped` while monitoring is paused
(`"state": "started"`/`"stopped"` and `"paused": true` in `--json`). Pausing only
affects Sermo's monitoring; it does not stop the service itself, and manual
`sermoctl` actions still work.

A successful manual `stop` or `pause` from `sermoctl` or the web UI also pauses
monitoring when the service was monitored. The state row records that the pause
came from that manual action, so a later successful manual `start`, `restart`,
`resume` or `repair` — or one of them whose postflight failed while the service
stayed active — restores monitoring only in that case. If the service was
already unmonitored before the stop, the later start preserves that operator
choice.

## System metrics

A `scope: system` metric ("is the machine under pressure?") is **not** a sound
trigger to restart one service, so it is allowed only in `alert` rules — never in
remediation rules, directly or via a check reference. See
[Metrics](rules.md#metrics) for the `scope: service` and `scope: system` metric
lists.

## Busy servers

A probe whose server answers that it cannot serve now — MySQL error 1040
"Too many connections", PostgreSQL "sorry, too many clients already", Redis
`LOADING`, an SMTP or FTP `421` — reports the service as failing, with the
server's own reason, and never as a reason to act on it. The cause is outside
the daemon: a full disk that stalls commits, a client that leaks connections,
a load spike, a dataset that is still loading. A restart drops every client the
server still serves, throws away the load in progress and, on a database, can
add minutes of recovery: a monitor that restarts MariaDB on "Too many
connections" while commits wait for disk space turns a saturation into a long
outage, and the disk is still full when it comes back.

So a rule or watch whose `then` restarts, starts, stops, reloads or resumes
the service reads a busy check as unknown: it does not fire, and a busy cycle
resets its `for:` window. A watch whose check is busy runs none of its
actions either — no hook, expand, makestep or remount — so a hook cannot
restart the server behind Sermo's back. Alerts and `event_notify` still report
the failure, and guards still fail closed. A server that does not answer at all — refused,
timed out, closed — is not busy, and its restart rule fires as before. The
codes each protocol reports as busy are listed in
[rules](rules.md#busy-servers).

## Privileges: the daemon runs as root

`sermod` is designed to **run as root** (the packaged systemd unit and OpenRC
service do). It manages services owned by different users and touches privileged
areas, so several features need it:

- **Service control** — start/stop/restart/reload via systemd/OpenRC,
  start/stop/restart/pause/resume of VM domains via libvirt when a service declares
  `control.type: libvirt`, and start/stop/restart/pause/resume of Docker containers
  when it declares `control.type: docker`.
- **Signalling other users' processes** — the stop policy signals the residual
  processes it authorizes, across UIDs.
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

- **`then.expand`, `then.remount` and `then.makestep` are policy-gated.** They
  change the host, so they run at most once per
  `policy.cooldown`, and every attempt starts the cooldown so a failing target is
  not retried each cycle. `then.makestep` — which asks the local chronyd to step
  the system clock — additionally *requires* a positive cooldown and acts only on
  an offset breach. **Never enable it on a ceph mon or osd host**: a clock jump
  can cost a monitor its quorum, so alert there instead. `then.remount`
  likewise requires a positive cooldown and acts only on a missing or hung
  mount: its forced unmount fails the I/O pending on that mount, never signals
  a process, and refuses `/`.
- **The config is trusted, root-owned input.** `command` checks and watch `hook`s
  run their `argv` **as root** (never via a shell). Keep `/etc/sermo` writable
  only by root; anyone who can edit it can run code as root. Secrets belong in the
  environment (`${env:NAME}`), not in the file.
- **Host path validation is not a filesystem sandbox.** `internal/hostfs`
  rejects relative paths, unclean paths and NUL bytes, but accepts any clean
  absolute path and follows symlinks when opening files. Configured paths must
  come from the trusted operator configuration. Code that builds paths from
  request parameters must validate those components before joining them;
  normalizing a path does not authorize its destination.
- **The web UI** (when enabled) can start/stop/restart/reload/pause/resume/repair services and
  monitor/unmonitor targets as root, so it is hardened by default: it **binds to
  loopback** (`127.0.0.1`), supports
  **authentication** with a read-only guest role (a password-only login form
  that issues an `HttpOnly` session cookie and throttles repeated failures, or
  HTTP Basic for API clients), requires the **`X-Sermo-Csrf`
  header** on every state-changing request except the login form's POST, which
  a plain HTML form cannot send it from and which is rejected unless it comes
  from the dashboard's own origin (blocking cross-site forgery from a
  browser), and sets HTTP timeouts. It speaks plain HTTP, so to reach it from off
  the host you **must** put it behind a TLS-terminating reverse proxy
  (nginx/Apache) — see
  [behind a reverse proxy](configuration.md#behind-a-reverse-proxy-required-to-expose-it).
  Keep `web.address` on loopback; never publish the port directly. The daemon logs
  a warning if the UI runs without authentication.
- **No shell, no name-based kills, no SIGKILL by default** — see the hard
  invariants above; these bound what even a misconfiguration can do.

A failure to rediscover processes between signal rounds stops escalation and
records a failed outcome. An unreadable process table is never treated as
proof that all survivors exited. Signal rounds check cancellation before each
process and again after user resolution; an expired operation cannot begin a
new TERM or KILL delivery.

Process discovery records the kernel start time. Residual cleanup, mount blocker
signalling, process-watch and `unowned_processes` kills (watch and dashboard),
SSH session closes, native signal reloads, sermod's startup hygiene and
`sermoctl daemon reload` bind delivery to a Linux pidfd and
revalidate that generation, resolved executable and real UID before sending.
An unreadable identity, changed generation or unavailable pidfd support blocks
delivery; there is no fallback to a numeric-PID signal. Operators using these
actions need a kernel that supports `pidfd_open` and `pidfd_send_signal` and a
security policy that permits them.

## Locks

Service and lock names must be single identifiers without path separators or
`.` / `..` components. Before joining a lock filename to its runtime directory,
Sermo also checks that it is local and contains no directory components or NUL
bytes. Invalid names fail without reading or deleting a lock. The runtime
directory is trusted operator configuration and must not be writable by
untrusted users.

Every removal (owner release, explicit release or stale reclamation) requires
exclusive directory locking. Contention fails promptly without removing the lock;
a failure to acquire exclusion never permits an unlocked removal. An owner
release retries that exclusion briefly (bounded well under a second), because
releases and reclaims of other services share the directory; if it still fails,
the operation's result carries a `release operation lock` warning and the lock
expires at its TTL. Each new lock
also records an acquisition identifier, so an old handle cannot release a newer
lock even when both were acquired by the same process. Older lock files without
that identifier remain readable and can be reclaimed under the same exclusion.

Two complementary blocking mechanisms guard operations:

1. **Named runtime locks** — files under `<engine.runtime>/locks` (default
   `/run/sermo/locks`), named `<service>[\<name>].lock`. A literal backslash
   separates the service and lock name. The operation engine blocks automatically
   on any active one; no rule is needed. Created by
   `sermoctl lock` (wrap a command), `lock acquire` / `lock release`
   (see [cli.md](cli.md)).
2. **External lock checks gated by a guard** — a check (`file_exists`,
   `process`, …) over a signal Sermo does *not* own: a backup process, a
   foreign flag file. Never point such a check under `<engine.runtime>/locks` —
   that duplicates mechanism 1.

A service-created `lockfile:` in the catalog is different: it is a gated health
check for a regular runtime artifact, like `socket:`, and does not block
operations unless the operator also writes an explicit guard rule.

If a named lock for the service cannot be read or parsed, the operation fails
before any service action. New locks publish their complete payload atomically.
An incomplete or corrupt lock left by an older writer or external modification
is not proof that maintenance has finished; lock listings retain the diagnostic
warning. A malformed lock for a different service does not block this service.

The **internal operation lock** (`<engine.runtime>/ops/<service>.lock`)
serializes start/stop/restart/reload/resume/pause/repair, the manual session
and terminal-source closes and `kill_query` for one service. It is deliberately outside the
named-lock namespace so it cannot collide with a user lock named `op`, is never
listed as a named lock, and cannot be released by `sermoctl lock release`. A
live holder makes a second operation fail fast with exit `75` ("operation in
progress") — the engine never waits or queues.

Lock files are JSON:

```json
{
  "acquisition_id": "QJ4XW2ZK7MN5RTB3DLYHVE6GFA",
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
even after PID reuse. An owned lock cannot be acquired without a verified,
non-zero start time. Older locks with an unknown start time stay active while
the owner is alive, until TTL expiry; a later successful process read cannot
turn that missing evidence into proof of PID reuse.

Lifecycle:

- **Acquire atomically**: create a staging file with `O_CREAT|O_EXCL`, write
  and fsync its JSON, close it, then publish a hard link without replacing an
  existing lock. Sync the directory. A crash before publication leaves only an
  ignored `.tmp` file; a published lock always has a complete TTL. A corrupt
  legacy lock still blocks operations with a parse diagnostic and requires
  operator inspection; it is never guessed to be expired.
- A lock is **stale** (ignored, reclaimable) when its TTL elapsed, its owner
  PID is dead, or the PID is alive with a different start time (reuse). A live
  lock is **never silently overwritten**.
- **Reclaim is logged**: read, confirm still stale, unlink, acquire fresh;
  abort if it turned active in between. Every reclaim — of an operation,
  named, watch-control, expansion or mount lock, by the daemon, the Web UI or
  `sermoctl` — is recorded as a `warning` event ("reclaimed stale … (reason)").
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
- Each target has an operation lock under `<engine.runtime>/mounts/ops`, so two
  callers cannot race the same mount. Lock and counter identifiers are
  injective: distinct mounts never share either, and a counter recording
  another path is refused.
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
  Delivery uses the same pidfd path as residual cleanup, bound to the blocker's
  start time, exact executable and real UID; a blocker whose identity cannot be
  verified is reported, not signalled, and each refused delivery is named in
  the result.
- Forced and lazy unmount are per-action choices: `--force` / Web `force`
  permits `umount -f`, and `--lazy` / Web `lazy` permits `umount -l` as the last
  fallback.
- Every `mount`/`umount` invocation has its own timeout. A Web/API action is
  bounded as a whole by the sum of its escalation steps (each command timeout
  plus `umount.term_timeout` and `umount.kill_timeout`), so a hung first
  `umount` still leaves the requested `-f`, blocker signalling and `-l` their
  time; the HTTP response deadline is sized to cover that budget.

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
- **Unreadable exe fails safe**: an unreadable executable authorizes no signal.
  A deleted executable remains distinct from a current executable: it cannot
  prove a healthy new start or authorize a signal reload. Residual cleanup may
  send TERM/KILL only when the exact previous path and real UID match the
  configured policy, and opening `/proc/PID/exe` verifies a regular file
  with zero links in the same mount namespace. Permissions may have been cleared
  during package removal: the kernel's executable link proves the file's role,
  without requiring permission to execute it again. The file stays open during
  delivery; device/inode, PID generation, UID, previous path and cgroup are
  revalidated after opening the pidfd. The pathname alone is insufficient.
  This exception is limited to service residual cleanup. Host process watches
  lack service ownership evidence and continue to refuse deleted executables.
- **External deleted executables require ownership evidence**: a process owned
  by another service or container scope is excluded. A process from an old login
  session can be a candidate when a strict named selector matches, cgroup
  ownership is readable and a strict main selector rules out competing live
  instances. Every matching main root must also have readable, non-foreign
  ownership. Ambiguous or unknown ownership blocks cleanup. Use instance-specific
  selectors for shared executables; Sermo never resolves ambiguity by process name.
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
Monitoring (worker cycles, the dashboard, service metric watches, the `zombies`
check, and host `type: process` and `process_policy` watches that run no faster
than that window)
shares one `/proc` snapshot and one backend PID answer per unit for half an
`engine.interval`. Every
observation an operation acts on (stop, residual cleanup, reaper rounds, start
verification) and every kill re-check reads both live, and the pidfd
revalidates the process at delivery.
Native init services exclude selector matches owned by a different init unit.
Libvirt domains, libvirt networks and Docker targets do not use their target
names as init unit names; their backend evidence and configured process
selectors determine discovery. Missing ownership evidence still blocks cleanup
of external deleted executables.
Cgroup paths must be canonical absolute hierarchy paths below the cgroup root;
the root itself and paths containing traversal components provide no PID
ownership evidence. OpenRC unit names must be single local filenames before
Sermo probes init scripts, configuration, runtime metadata or reload support.
Invalid names are rejected even when the resolver trusts an unprobed unit.
For `pidfiles:` maps, each pidfile role must be backed by a same-named
`processes:` selector with exact `exe` and `user`; the pidfile is evidence, not
a name-only authority.

## Stop and signal escalation

`stop_policy` fields omitted by a catalog service or service inherit from
`defaults.stop_policy`. The stop phase of an explicit stop or a restart:

1. Backend `Stop`, observe processes until confirmed absent or `graceful_timeout`
   expires, then discover residuals. Process-free services use inactive init state
   instead of process absence. A service without a strict `exe` + `user`
   selector can never prove absence, so its wait ends early only when every
   process generation observed before the stop is gone from the complete
   process table and init then reports the unit inactive or failed (systemd
   records a daemon that died of the stop signal as failed). Ending the wait
   early proves nothing more: residual discovery and init reconciliation still
   need positive evidence before a start. A stop request against a unit
   that was already inactive with no observed process does not wait at all.
   A process that was already a stray before the stop is never waited for: the
   init's stop does not address it, so it would not exit on its own, and the
   wait ends as soon as the service's own processes are gone.
   Declaring the selector remains the stronger evidence. An incomplete
   observation fails closed. A later
   absent or stale pidfile candidate cannot erase an earlier read error; a live
   candidate or backend process still supplies positive discovery evidence.
   Docker's stop request shares this grace period (10 seconds when omitted or
   zero), so an unresponsive container cannot consume the whole operation deadline
   before residual handling. A request timeout is retained as a warning; an
   expired or cancelled overall operation prevents escalation and start.
   For Docker containers without selectors, reconciliation after a request error
   requires a complete snapshot proving the old process generations exited and
   an inactive container state. Disappearing backend PIDs alone are insufficient.
2. No residuals → clean stop.
3. With `stop_policy.keep_children: true`, discovery has already kept every
   process that is not a principal (see
   [Kept children](#kept-children-keep_children)), so only principals can be
   residuals. Otherwise every residual must go before a start: one the kernel
   places in the unit's own control group is a `SIGTERM` target even with
   `force_kill: false`. With `force_kill: false` nothing else is signalled and a
   survivor is `orphan_processes` (and a restart does **not** start).
4. Residuals with `force_kill: true` or `auto` → classify each one: KILLABLE
   only when every explicit `kill_only_if` field matches, when — under `auto`
   without an explicit `kill_only_if` — it matches a single paired strict
   `processes:` identity (exact resolved exe **and** real
   UID; deleted executables require the additional file and ownership proof
   above, and protected PIDs are never killable), or when it is a proven member
   of the unit's control group. SIGTERM the
   killable set, wait up to `term_timeout`, rediscover; SIGKILL what remains of the
   killable set, wait up to `kill_timeout`, rediscover. Each wait ends early when
   fresh discovery confirms that no residual remains. A residual that never matched
   is never signaled. SIGKILL never targets a replacement generation that
   appeared after the TERM round.
5. The result is `ok` only when no residuals remain at all — whether the
   survivor was deliberately spared or outlived SIGKILL, the result is
   `orphan_processes` and lists every remaining process. The CLI and operation
   JSON retain the previous executable path, selector role and signal refusal
   reason. Every attempted signal and delivery error is included in the single
   persisted operation event, even when cleanup eventually succeeds.

## Kept children (`keep_children`)

`stop_policy.keep_children: true` declares that only the service's **principal**
processes must exit on stop; everything else it owns — SSH sessions, GlusterFS
bricks, guest and network helpers, container shims, running cron jobs — is kept
alive, never signalled and never a residual. It is the declarative form of an
init unit with `KillMode=process`.

A principal is:

- a pidfile PID — the pidfile names the daemon explicitly;
- a process matched by a non-delegated `processes:` selector, including a binary
  replaced on disk (`exe_previous`), that descends from no process matched by
  that same selector;
- a process whose executable cannot be read and that descends from no matched
  process at all — it cannot be told apart from the daemon (fail closed).

Every other discovered process is kept. What the daemon forked is kept even when
it re-executes the same binary as the same user (a cronie job runner, an OpenSSH
< 9.8 session), while a surviving earlier daemon instance — reparented to PID 1 —
is still a principal and still blocks the start, so a second daemon is never
started beside it. A supervisor declared by its own selector stays a principal
without hiding the daemon below it. The backend's first PID is deliberately not
trusted on its own: once a systemd unit's daemon has exited, the first PID it
lists is just a kept workload process (a shim, a job). An explicit
`delegated: true` selector is applied first and always wins. Validation rejects
`keep_children: true` without a non-delegated selector that has an exact `exe`
and `user`.

Without `keep_children` (the default) a restart needs every residual gone; see
step 3 of [Stop and signal escalation](#stop-and-signal-escalation).

`processes.<name>.delegated: true` is a different tool: an **observe-only**
selector. Sermo attributes the matched processes and their descendants to the
service for monitoring, but never signals them, never counts them as residuals
and never takes kill authority from them. It is meant for processes whose
lifecycle another backend owns, such as a libvirt guest's QEMU process; to keep
a daemon's own workload alive across a restart, use `keep_children`. See
[services.md](services.md#processes-by-executable-or-cmdline).

## Stray processes

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
abandoned, a survivor of an earlier incarnation.

Strays appear in `sermoctl processes` as `stray=true`, in the dashboard's process
table with `stray` in the Role column and in the services table's Strays count,
and as the injected `strays` check (see
[configuration.md](configuration.md#strays--processes-the-service-cannot-account-for)).
A stray is a diagnostic: no command, dashboard button or rule action signals a
process because it is a stray. It is still discovered, still counted in the
service's process totals, and still a residual of a stop like any other process.
A process kept by `stop_policy.keep_children` or matched by a `delegated: true`
selector is never a stray.

### What a stop or restart does with a stray

Whether a stray survives a stop, and whether it blocks the restart, depends on
the unit's `KillMode` and on `stop_policy.keep_children`:

- `KillMode=control-group` (the systemd default): the stop takes the whole control
  group with it, so no stray survives to be a residual.
- `KillMode=process` / `none`, or OpenRC: survivors remain.
  - With `keep_children: true` (the catalog's sshd, NetworkManager, libvirt,
    GlusterFS, container and cron profiles) every non-principal process is
    kept, so there is no stray at all: no residual, no signal, no blocked start.
  - Otherwise a residual the kernel places in the service's own control group
    (systemd `<unit>.service`, OpenRC `openrc.<unit>`) receives `SIGTERM`, even
    with `force_kill: false`, and `SIGKILL` only with `force_kill: true` or
    `auto`. This also covers a stray that discovery only tracks from before the
    stop — an OpenRC unit stops listing its control group once the daemon takes
    its pidfile along: membership of the unit's own group still proves
    ownership, while one that moved to another group keeps its refusal. A
    residual that survives, or one without control-group proof (such as on an
    OpenRC host without per-service cgroups), ends the operation in
    `orphan_processes` with the service left stopped.

When a stray blocks the restart, the result names it and the setting that keeps
such a workload on purpose:

```console
$ sermoctl restart myapp
myapp restart orphan_processes
reason: 1 residual process(es) remain after stop (1 stray, unaccounted for by any
  selector; set stop_policy.keep_children if the unit keeps it alive on purpose)
  residual pid=4711 exe=/usr/bin/tmux stray=true
```

The operator then chooses:

- set `stop_policy.keep_children: true` if the unit keeps its workload alive on
  purpose;
- add a `processes:` selector if the process is a role Sermo should know;
- or let the next stop or restart terminate it.

A residual that survives the signals its stop policy allows, or that Sermo
cannot attribute, stays `orphan_processes`: review it and end it yourself before
starting the service.

### sermod's own startup hygiene

sermod terminates whatever it finds in **its own** init unit control group when it
starts, before it has spawned anything itself — so anything there belongs to a
previous incarnation the init system did not clean up (`KillMode=process` or
`KillMode=none`). This is the one exception to "all service signalling goes
through the operation engine", and it is deliberately narrow:

- Only sermod's own control group, and only when that group is a systemd
  **service** unit named exactly `sermod.service`, the packaged daemon unit.
  Prefix matches such as `sermod-helper.service` and custom template instances
  do not authorize cleanup. Started from a login shell sermod
  shares its scope with the operator's shell and sshd; run inside a unit named
  for something else — a CI agent's service, a container supervisor, a
  systemd-run wrapper — the neighbouring processes belong to that something
  else. In both cases it does nothing at all.
- `SIGTERM` only. A leftover that ignores it is reported and left alone.
- Delivery requires a verifiable process generation, executable and UID and
  available pidfd support; an unverifiable target produces a failure event.
  The generation is pinned before the PID's control group is re-read, and the
  signal is bound to it, so a listed leftover that exits and whose PID is
  recycled outside the unit is skipped, never signalled.
- One event per process signalled.
- `engine.reap_own_strays: false` turns it off.

## Scheduler and concurrency

Each enabled service is monitored by its own worker with an independent ticker
at `engine.interval` (per-service `interval` overrides). Workers never share a
cycle: a multi-minute restart on one service cannot block monitoring of
another. Within a service the cycle is synchronous — checks, rule evaluation,
then at most one operation.

- **No tick overlap**: each worker waits one interval from the end of its
  previous cycle, so an overrunning operation delays that service's next cycle
  and never queues catch-up cycles.
- **Stagger**: the first cycles are spread evenly across the interval, so
  workers do not all start at once.
- **Bounded concurrency**: each service runs at most one operation at a time
  (the cross-process operation lock), and automatic remediation is rate-limited
  by the mandatory per-service `policy` block (cooldown, `max_actions`,
  backoff). Check execution shares a global pool
  (`engine.max_parallel_checks`). A check that cannot get a slot waits — it is
  not skipped — and its timeout starts only once it gets the slot.
  Cancellation while it waits (shutdown, reload, an operation's deadline)
  reports it unavailable without starting the probe.
- **Configuration health**: a service's `preflight.config` is copied into the
  `configuration` check — warning-grade unless the entry declares its own
  severity, and never counted against SLA — and runs through that same bounded pool,
  every `15m` by default. The original required preflight remains in the
  operation path and can block an action before any service mutation.
- **Shutdown** (SIGTERM/SIGINT): stop starting cycles, cancel worker contexts;
  an in-flight operation observes cancellation, its deferred cleanup releases
  the lock and emits the event, and a partially stopped service is left as-is —
  never force-killed because of shutdown.
- **Daemon reload** validates the new config, swaps workers/watches while
  preserving per-service runtime state, and keeps the running generation when
  the new config is invalid.

A start, restart, reload or resume is reported as failed when the backend status
cannot be read after the action, even when no postflight checks are configured.

The opt-in `stale_libraries` check is read-only. Only complete, stable findings
on a unique main process with an exact executable and real-user selector may
drive an explicitly configured service restart rule. Host inventories,
child-only findings, missing replacements and uncertain identities remain
advisory (`action_blocked`), including under negated conditions. The operation
engine retains every normal lock, guard and preflight. Package transactions
must hold the affected services' named locks; the observation stability window
does not detect an update's completion. See the
[configuration and limits](configuration.md#stale_libraries--processes-using-replaced-shared-libraries).

### Surviving interactive sessions

A manual SSH session close may identify a split OpenSSH session server whose
listener has exited. Sermo verifies the remote utmp leader, exact helper
executable and real UID, live terminal ancestry and the configured listener's
non-root cgroup v2. Deleted executables require the kernel-held file identity,
revalidated through a pidfd before SIGTERM. A residual `sudo` terminal is closed
on its own verified evidence, with no configured authority; see
[The operation engine](#the-operation-engine). This session-specific authority
never changes the protection `stop_policy.keep_children` gives the sessions
during service stop or restart.

Only a tmux or screen session row offers whole-session close, which terminates
its windows, panes and programs and disconnects attached clients. The
configured client revalidates the named session generation.
