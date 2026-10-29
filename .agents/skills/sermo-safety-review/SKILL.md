---
name: sermo-safety-review
description: Use for any Sermo change involving start, stop, restart, reload, resume, kill, signal, process discovery or matching, pidfiles, /proc, cgroups, residual processes, locks, preflight, guards, remediation rules, or automatic actions.
---

Safety review for Sermo. Assume production servers and control of databases,
web servers, caches and mail. Operator policy: `docs/safety.md`. AGENTS.md
lists the hard boundaries. This skill is the review checklist for code that
touches them.

## Operation path

1. Every service action runs through `internal/operation` with a timeout, the
   operation lock, guards and required preflight. CLI, daemon and web never
   call a backend lifecycle method; any other signal goes through
   `process.Signaler`/`process.Reaper` (pidfd) with its own explicit authority.
2. Start/restart/reload/resume/repair is blocked by a failed required preflight; an
   optional preflight warns. A `verify: true` check failing after the operation
   returns `postflight_failed`.
3. Automatic remediation uses the same path as manual actions, requires a
   positive resolved `policy.cooldown`, honors `max_actions` and never triggers
   from a `scope: system` metric. Manual actions skip cooldown only; they still
   pass guards, locks and preflight.
4. The operation lock is released on every exit path. Named runtime locks live
   under `<engine.runtime>/locks`, operation locks under `<engine.runtime>/ops`;
   they never share a directory and are never loaded from `/etc/sermo`.
   A named lock read/parse warning for the target service prevents its operation;
   do not treat a partially written lock as absent.
5. Locks are staged with `O_CREAT|O_EXCL`, synced and closed before no-replace
   hard-link publication, and are TTL-bounded. A stale lock
   (expired or dead owner) is reclaimed through a logged path, never silently
   overwritten: every locker (operation, named, watch control, expansion,
   mount) reports the reclaim, and daemon, web and sermoctl record it as the
   same warning event (`app.LockReclaim`).
6. Every executed or blocked action records exactly one auditable event.
7. One slow service never blocks monitoring of another; shared check
   concurrency stays bounded.

8. Restart is stop/verify/reconcile/start/verify under the same lock and
   deadline. A Docker target positively confirmed inactive only starts; an
   inspect 404 may create a stopped target from a registered Compose source,
   then start its verified new ID. Original restart guards still apply, and
   the audit retains both requested and effective actions and any created ID.
   There is no backend restart mode. Fresh process observations must
   distinguish confirmed absence from incomplete discovery. Process-free services
   cannot be judged by missing PIDs.
9. A failed stop command may have stopped the process: retain the error, verify
   absence and reconcile init before proceeding. A failed start requires a trusted
   live process, active init and postflight. Never discard reset errors or infer
   reload/resume success from liveness. systemd reset-failed does not deactivate
   an active unit; OpenRC zap must never hide a surviving daemon.
10. Accept systemd reactivation only after verifying old process generations
    exited across the complete process snapshot, including outside the current
    cgroup, and the same unit owns the new generation. Never signal the replacement
    or accept an unchanged daemon as a completed restart. Restore stopped
    auxiliary units even when the primary start is skipped.
11. Deleted-executable evidence with the main selector's previous path and real
    user may permit a backend stop, but never validates a new start. Keep it
    separate from current exact identity; workers cannot substitute for an exact
    main selector, and zombies cannot prove a process is running.
12. Incomplete rediscovery stops signal escalation. Keep stop-command,
    auxiliary-stop and artifact warnings in the one audit result even if a
    subsequent phase fails or times out.

## Process identity and signaling

- Signal authority requires the exact resolved `/proc/<pid>/exe` path and the
  real UID, or the cgroup/session evidence below. Name, basename, substring,
  argv[0] and cmdline never authorize anything: a cmd-only selector may
  attribute a process for monitoring but never authorizes a signal, and `cmd`
  on an exe selector only narrows it.
- Signal delivery uses a pidfd bound before revalidating the discovered start
  time, exact exe and real UID. Missing pidfd support fails closed; never fall
  back to signaling a numeric PID after authorizing a process snapshot.
- An unreadable exe never matches. A `(deleted)` exe cannot prove a healthy
  start; residual cleanup requires the exact previous path, real UID, verified
  kernel-held file and service ownership.
  Leaving an unidentified process alive beats killing the wrong one.
- Prefer extra evidence: pidfile, systemd cgroup or MainPID, parent tree,
  OpenRC supervise metadata, a listening port owned by the PID.
- `SIGKILL` requires `force_kill: true` plus a `kill_only_if` clause with both
  `exe_any` and `users`; `force_kill: auto` with its explicit `kill_only_if`
  or, without one, the exe+user(+cmd) pairs `EnableAutomaticReaping` derives
  from strict principal selectors; or proven membership of the unit's own
  cgroup with `force_kill: true` or `auto`. Unset or `false` rules out SIGKILL.
  The mysql, mariadb, postgres, mongod, influxdb, redis, keydb, memcached, etcd
  and patroni profiles pair the inherited `auto` with an explicit `kill_only_if`
  and long grace periods; only garbd, which holds no data, keeps the defaults.
- Residuals after stop: under `force_kill: true` or `auto`, processes matching
  `kill_only_if` (or an `auto`-derived pair) get SIGTERM, then SIGKILL after the
  term timeout. A residual the kernel places in
  the unit's own cgroup (`servicemgr.CgroupOwnedBy`, revalidated by the pidfd)
  gets SIGTERM even without `force_kill`, and SIGKILL with `force_kill: true` or
  `auto`. Any other
  residual yields `orphan_processes`, and a start never follows a stop that left
  orphans.
- `stop_policy.keep_children: true` delegates every non-principal process at
  discovery. A principal is a pidfile PID, a non-delegated selector match
  (replaced binary included) with no ancestor matched by the same selector, or an
  unreadable exe outside any matched tree. Never trust the backend's first PID on
  its own (after a systemd stop it is a kept workload process), and never let a
  surviving daemon instance (reparented to PID 1) become a kept child;
  validation requires a strict principal selector.
- `processes.<name>.delegated: true` is observe-only: its matches and their
  descendants are attributed for metrics but never signalled, never residuals,
  never strays and contribute no kill authority. It is meant for targets
  another backend owns (generated libvirt VM and network services); the catalog
  ships none, and a daemon's own workload uses `keep_children`.
- A stray (unit-cgroup member no selector claims, outside the principal tree)
  is diagnostic only. No command, button or rule action signals a process for
  being a stray; a stop handles it as any other residual. The only exception is
  sermod's own startup hygiene, `engine.reap_own_strays` (SIGTERM only, in
  sermod's own unit cgroup).
- External commands are argv arrays through the `execx` runner with a context
  and timeout; no shell, no interpolated user input, no ignored errors or
  cancellation. A hook without its own `timeout` inherits
  `engine.default_timeout`.
- `sermoctl daemon reload` signals only the sermod installed alongside the
  sermoctl that runs it (its sibling or `../sbin/sermod`, symlinks resolved),
  running as the caller's real UID (any UID for a root caller), through the
  pidfd signaler. A program
  merely named sermod is refused with the reason.
- Generation checks treat an unknown start tick (0) as no generation:
  `SameGeneration` never matches it, so SIGKILL never escalates onto it.
  `CgroupOwnedBy` never matches an empty unit.

Manual session close is distinct from service residual cleanup: split OpenSSH
session servers may survive their listener and stay kept (`keep_children`)
during stop. Closing one requires remote utmp/terminal ancestry, an exact helper
executable and real UID, the live configured listener's non-root cgroup, fresh
generation verification, and pidfd delivery. A deleted helper also needs its
pinned kernel-held file.

A residual `sudo` terminal close takes no configured authority (no
`kill_only_if`): the evidence is the authority. It requires a remote utmp
session whose leader is reparented to PID 1, the exact `/usr/bin/sudo` or
`/bin/sudo` executable, the utmp user's real UID, a monitor child running the
same executable, both in the sshd listener's cgroup. Before each signalling
round the session verifier re-derives the whole boundary (utmp, sudo
executables, UID, listener cgroup) and it must match; the pidfd revalidates
generation, executable and real UID at delivery. SIGTERM, then SIGKILL after
the service's `stop_policy.term_timeout` to survivors, then verified exit.
Missing evidence fails closed.

Whole-session tmux/screen close belongs only on the session row, with its
effects on windows, programs and attached clients explicit in confirmation.

## Red flags

```text
matches killable processes by name, basename, substring, argv[0] or cmdline
runs a shell or ignores command errors or context cancellation
executes a service action outside internal/operation
lets remediation bypass locks, guards, preflight or cooldown
triggers remediation from a system-scope metric
restarts after a stop that left residual processes
leaks the operation lock on an early return
does not log a blocked action
adds a config toggle that disables a hard invariant
lets a busy observation (too many connections, loading) drive a lifecycle action
evaluates a rule for an operation with Eval instead of EvalRule
lets named locks and operation locks share a directory or load from /etc
serializes every service through one loop
```

## Required tests

A safety-sensitive change covers the relevant rows:

```text
guard blocks every GuardBlocksSummary action, including implied blocks (stop/start -> restart, start -> repair, stop -> pause)
required preflight failure blocks; optional preflight warns
lock blocks every action; paths.locks rejected; lock dirs derive from engine.runtime
force_kill false never sends SIGKILL; force_kill true requires kill_only_if
exe matched by resolved /proc/<pid>/exe; substring/basename/name-only rejected
unresolvable or (deleted) exe never matches
residual not matching kill_only_if nor the unit cgroup is never signalled and yields orphan_processes
unit-cgroup residual: SIGTERM without force_kill, SIGKILL with force_kill true or auto, never false; cgroup revalidated at delivery
keep_children keeps non-principals; a reparented daemon instance stays principal; the backend's first PID alone is not a principal
delegated selector: observed, never signalled, never a residual or stray
residual sudo close: refused on any missing or changed evidence; TERM, then KILL after term_timeout
no start after orphans
cooldown prevents loops; manual action exempt; missing or zero cooldown rejected
system-scope metric in a remediation rule rejected
operation lock released on every early-return path; exactly one event recorded
stale pidfile, wrong exe or wrong user for a pidfile, child process tree
```

Tests use fake runners and a fake process table or procfs fixture. They never
run `systemctl`, `rc-service`, `kill`, `pkill`, `sudo` or `doas`.
