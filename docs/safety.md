# Safety

Sermo's safety invariants are **not configurable in YAML**. Validation rejects
any `security:` toggle that tries to disable them.

## Hard invariants

1. **Never start or restart if a required preflight fails.** A required
   preflight failure blocks the action with `preflight_failed`.
2. **Never start, stop or restart if a guard blocks the action.** Guards are
   evaluated before remediation; a remediation action a guard blocks never runs.
3. **Active named runtime locks always block service actions.** The operation
   engine checks `<runtime>/locks` automatically — no rule needed.
4. **Never SIGKILL by default.** `force_kill` is false unless explicitly enabled.
5. **Never kill by process name.** A kill requires an exact match on the
   resolved `/proc/<pid>/exe` path **and** the real UID against an explicit
   `kill_only_if` selector. `argv[0]`/cmdline are never trusted, and a process
   whose exe cannot be resolved (permission, or a `(deleted)` binary) is never
   killed — it is reported as a residual instead.
6. **`force_kill: true` requires `kill_only_if`** with both a `users` selector
   and an `exe_any` selector, each non-empty.

## The operation engine

Every start/stop/restart — manual (`sermoctl`) or automatic (`sermod`) — runs
through the same engine (section 18):

1. Acquire the internal operation lock (`<runtime>/ops/<service>.lock`); a live
   holder fails fast with exit `75` ("operation in progress").
2. Block on any active named runtime lock.
3. Run required preflight (start/restart).
4. Block if any guard blocks the action.
5. Stop, wait `graceful_timeout`, discover residual processes.
6. If residuals remain and `force_kill` is false → `orphan_processes` (do **not**
   start). If true, SIGTERM then SIGKILL only the processes that exactly match
   `kill_only_if`, rediscovering between steps.
7. Start, verify status, run required postflight.

A residual Sermo is not allowed to identify and kill is **reported, not killed**:
a clean `orphan_processes` failure is safer than killing the wrong process.

## Rate limiting

Only *automatic* remediation is rate limited (`cooldown`, `max_actions`,
`backoff`). Manual `sermoctl` actions are deliberate and not subject to cooldown,
but remain subject to locks, guards and preflight.

## Pausing monitoring

`sermoctl unmonitor SERVICE` pauses monitoring for a service; `monitor SERVICE`
resumes it. While paused, the daemon runs no checks, rules or remediation for that
service — useful during maintenance so a deliberate stop is not "remediated" by an
automatic restart. The pause is a marker file under `<paths.runtime>/paused`, so
it persists across daemon restarts until cleared. `sermoctl status SERVICE` shows
`monitoring=paused` (and `"paused": true` in `--json`). Pausing only affects
Sermo's monitoring; it does not stop the service itself, and manual `sermoctl`
actions still work.

## System metrics

A `scope: system` metric ("is the machine under pressure?") is **not** a sound
trigger to restart one service, so it is allowed only in `alert` rules — never in
remediation rules, directly or via a check reference.
