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
   call a backend or signal a process directly.
2. Start/restart/reload/resume is blocked by a failed required preflight; an
   optional preflight warns. A `verify: true` check failing after the operation
   returns `postflight_failed`.
3. Automatic remediation uses the same path as manual actions, requires a
   positive resolved `policy.cooldown`, honors `max_actions` and never triggers
   from a `scope: system` metric. Manual actions skip cooldown only; they still
   pass guards, locks and preflight.
4. The operation lock is released on every exit path. Named runtime locks live
   under `<paths.runtime>/locks`, operation locks under `<paths.runtime>/ops`;
   they never share a directory and are never loaded from `/etc/sermo`.
5. Locks are created with `O_CREAT|O_EXCL` and are TTL-bounded. A stale lock
   (expired or dead owner) is reclaimed through a logged path, never silently
   overwritten.
6. Every executed or blocked action records exactly one auditable event.
7. One slow service never blocks monitoring of another; shared check
   concurrency stays bounded.

## Process identity and signaling

- A process matches only on the exact resolved `/proc/<pid>/exe` path and the
  real UID. Name, basename, substring, argv[0] and cmdline never authorize
  anything; `processes.<name>.cmd` only narrows a match the operator declared.
- An unreadable or `(deleted)` exe never matches. Leaving an unidentified
  process alive beats killing the wrong one.
- Prefer extra evidence: pidfile, systemd cgroup or MainPID, parent tree,
  OpenRC supervise metadata, a listening port owned by the PID.
- `SIGKILL` requires `force_kill: true` plus a `kill_only_if` clause with both
  `exe_any` and `users`. Database catalog services keep `force_kill: false`.
- Residuals after stop: only processes matching every `kill_only_if` field get
  SIGTERM, then SIGKILL after the term timeout. Any other residual yields
  `orphan_processes`, and a start never follows a stop that left orphans.
- External commands are argv arrays through the `execx` runner with a context
  and timeout; no shell, no interpolated user input, no ignored errors or
  cancellation.

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
lets named locks and operation locks share a directory or load from /etc
serializes every service through one loop
```

## Required tests

A safety-sensitive change covers the relevant rows:

```text
guard blocks start/stop/restart/reload/resume
required preflight failure blocks; optional preflight warns
lock blocks every action; paths.locks rejected; lock dirs derive from paths.runtime
force_kill false never sends SIGKILL; force_kill true requires kill_only_if
exe matched by resolved /proc/<pid>/exe; substring/basename/name-only rejected
unresolvable or (deleted) exe never matches
residual not matching kill_only_if is never signalled and yields orphan_processes
no start after orphans
cooldown prevents loops; manual action exempt; missing or zero cooldown rejected
system-scope metric in a remediation rule rejected
operation lock released on every early-return path; exactly one event recorded
stale pidfile, wrong exe or wrong user for a pidfile, child process tree
```

Tests use fake runners and a fake process table or procfs fixture. They never
run `systemctl`, `rc-service`, `kill`, `pkill`, `sudo` or `doas`.
