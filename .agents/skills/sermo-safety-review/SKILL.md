---
name: sermo-safety-review
description: Use when changing or reviewing service operations, automatic actions, preflight, guards, locks, process discovery or authority to signal a process.
---

Safety review for Sermo. Assume production servers and control of databases,
web servers, caches and mail. Operator policy: `docs/safety.md`. AGENTS.md →
Safety lists the hard boundaries; this skill is the detail behind them that a
change to that code must preserve. Leaving an unidentified process alive beats
killing the wrong one.

For preflight, locking, restart, reconciliation or backend reactivation, follow
the [operation path](references/operations.md). For output draining or a
manual SSH, sudo, tmux or screen close, use the [terminal evidence and delivery
requirements](references/terminals.md) alongside the identity checks below.

## Process identity and signaling

- A cmd-only selector may attribute a process for monitoring but never
  authorizes a signal, and `cmd` on an exe selector only narrows it.
- Signal delivery uses a pidfd bound before revalidating the discovered start
  time, exact exe and real UID. Missing pidfd support fails closed; never fall
  back to signaling a numeric PID after authorizing a process snapshot.
- An unreadable exe never matches. A `(deleted)` exe cannot prove a healthy
  start; residual cleanup requires the exact previous path, real UID, verified
  kernel-held file and service ownership.
- Prefer extra evidence: pidfile, systemd cgroup or MainPID, parent tree,
  OpenRC supervise metadata, a listening port owned by the PID.
- `SIGKILL` requires `force_kill: true` plus a `kill_only_if` clause with both
  `exe_any` and `users`; `force_kill: auto` with its explicit `kill_only_if`
  or, without one, the exe+user(+cmd) pairs `EnableAutomaticReaping` derives
  from strict principal selectors; or proven membership of the unit's own
  cgroup with `force_kill: true` or `auto`. Unset or `false` rules out SIGKILL during service residual cleanup.
  Database and cache profiles pair the inherited `auto` with an explicit
  `kill_only_if` and long grace periods (`sermo-profile-author`).
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
  validation requires a strict principal selector. The exact generation kept
  before a stop stays kept through that operation (`RetainSurvivors`), the only
  evidence that tells a same-binary child from a surviving instance once its
  daemon is gone; a later operation has none, and it reads as a principal. A kept
  process stays listed but out of the service's metrics, so its workload cannot
  drive the daemon's rules.
- `processes.<name>.delegated: true` is observe-only: its matches and their
  descendants are attributed for metrics but never signalled, never residuals,
  never strays and contribute no kill authority. It is meant for targets
  another backend owns (generated libvirt VM and network services); the catalog
  ships none, and a daemon's own workload uses `keep_children`. A delegated
  selector alone keeps a process whose binary was replaced, since attributing it
  authorizes nothing; a service with one only ever alerts on a replaced binary,
  because restarting a VM to renew qemu would reboot the guest. When every
  selector is delegated (`Observation.ObserveOnly`) those processes are the only
  health and exit evidence: they satisfy the resident-process check and end the
  stop wait, and still never prove a failed start or authorize a signal.
- A stray (unit-cgroup member no selector claims, outside the principal tree)
  is diagnostic only. No command, button or rule action signals a process for
  being a stray; a stop handles it as any other residual. The only exception is
  sermod's own startup hygiene, `engine.reap_own_strays` (SIGTERM only, in
  sermod's own unit cgroup).
- A hook without its own `timeout` inherits `engine.default_timeout`.
- `sermoctl daemon reload` signals only the sermod installed alongside the
  sermoctl that runs it (its sibling or `../sbin/sermod`, symlinks resolved),
  running as the caller's real UID (any UID for a root caller), through the
  pidfd signaler. A program
  merely named sermod is refused with the reason.
- Generation checks treat an unknown start tick (0) as no generation:
  `SameGeneration` never matches it, so SIGKILL never escalates onto it.
  `CgroupOwnedBy` never matches an empty unit.

## Required tests

A safety-sensitive change covers the relevant rows:

```text
guard blocks every GuardBlocksSummary action, including implied blocks (stop/start -> restart, start -> repair, stop -> pause)
required preflight failure blocks; optional preflight warns
lock blocks every action; paths.locks rejected; lock dirs derive from engine.runtime
service residual cleanup: force_kill false never sends SIGKILL; check configured and cgroup authority separately
exe matched by resolved /proc/<pid>/exe; substring/basename/name-only rejected
unresolvable or (deleted) exe never matches a non-delegated selector
residual not matching kill_only_if nor the unit cgroup is never signalled and yields orphan_processes
unit-cgroup residual: SIGTERM without force_kill, SIGKILL with force_kill true or auto, never false; cgroup revalidated at delivery
replaced-binary children the stop attributed, still in the unit cgroup: stop policy applies however many survive; untracked, late or moved ones stay refused
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
