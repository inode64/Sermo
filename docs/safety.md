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
7. After a clean stop (no residuals), reconcile the init's recorded state with
   reality — `systemctl reset-failed` (systemd) or `rc-service … zap` (OpenRC) —
   so a lingering failed/stuck marker can't disagree with the actual processes.
   Best effort: it never fails a stop that already succeeded.
8. Start, verify status, run required postflight.

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
the single operator state `running` or `stopped` while monitoring is paused
(`"state": "running"`/`"stopped"` and `"paused": true` in `--json`). Pausing only
affects Sermo's monitoring; it does not stop the service itself, and manual
`sermoctl` actions still work.

## System metrics

A `scope: system` metric ("is the machine under pressure?") is **not** a sound
trigger to restart one service, so it is allowed only in `alert` rules — never in
remediation rules, directly or via a check reference.

## Privileges: the daemon runs as root

`sermod` is designed to **run as root** (the packaged systemd unit and OpenRC
service do). It manages services owned by different users and touches privileged
areas, so several features need it:

- **Service control** — start/stop/restart/reload via systemd/OpenRC.
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
- **The web UI** (when enabled) can stop/restart/reload services as root, so it is
  hardened by default: it **binds to loopback** (`127.0.0.1`), supports
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
