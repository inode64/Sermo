# Host watches: resource monitoring with `disk` check and `hook` action

**Status:** Approved (design) — ready for implementation planning
**Date:** 2026-06-06

## Problem

Sermo today only monitors **services**: every check, rule, and remediation is
owned by a service worker. There is no way to watch **host-level resources**
(disk space, network state, file counts) that are not the property of any single
service, and no way to fire an arbitrary notification ("hook") when a resource
threshold is crossed.

This spec introduces an extensible structure for host-resource monitoring,
delivering the **first** resource type — disk partition space — end to end:
a new unit (`watch`), a new check type (`disk`), and a new action (`hook`).
Network state and file-count checks are explicitly out of scope but the
structure is designed so they slot in as new check types without further
architectural change.

## Decisions (from brainstorming)

1. **Unit model:** a new host-level unit, independent of services, named
   **`watch`** (not "monitor", which is already overloaded in the codebase:
   `monitor`/`unmonitor` pause/resume, `MonitorStore`, `internal/cli/monitor.go`).
2. **Hook action:** runs a **local command** (argv, never a shell), with a
   timeout and context environment variables. No HTTP webhook in this iteration.
3. **Disk threshold metric:** **percentage** based — `used_pct` and/or
   `free_pct` — with an operator and value. No absolute-bytes or inode metrics
   in this iteration.
4. **Threshold placement:** the threshold lives **inside the check**. The `disk`
   check returns `OK = true` when the threshold is crossed; the watch fires its
   hook when that condition holds for the configured window. (Mirrors the
   existing `metric` check, whose `OK` is the comparison result.)

## Configuration

A new top-level `watches` map in the global `sermo.yml`. Each entry is one watch:

```yaml
watches:
  disk-root:
    enabled: true            # optional, default true
    interval: 1m             # optional, default engine.interval
    check:
      type: disk
      path: /
      used_pct: { op: ">=", value: 90 }   # check.OK = threshold crossed
    for: { cycles: 3 }       # optional window (consecutive); reuses rules engine
    # within: { cycles: 5, min_matches: 3 }   # optional sliding window
    then:
      hook:
        command: [/usr/local/bin/alert-disk.sh, "--mount", "/"]
        timeout: 10s         # optional, default engine.default_timeout
```

- `enabled: false` skips the watch (same convention as checks/rules).
- `interval` overrides `engine.interval` for this watch only.
- `for` / `within` reuse the existing window model in `internal/rules`
  (`ForWindow`, `WithinWindow`, `WindowState`). Absent window ⇒ fire as soon as
  the condition is true in a cycle.
- `then.hook.command` is an argv array (required, non-empty).

`watches` is engine/daemon configuration; it never merges into a service.

## Components

### 1. `disk` check — `internal/checks`

New check type registered in the existing `buildCheck` type switch.

- **Params:** `path` (required); `used_pct` and/or `free_pct`, each
  `{ op, value }` where `op ∈ { >=, >, <=, <, ==, != }` and `value` is numeric.
- **Behavior:** reads filesystem stats for `path` (via `statfs`) and computes
  used/free percentages. `OK = true` when **all present predicates** are
  satisfied (logical AND). At least one predicate is required.
- **Data:** `Result.Data` carries `path`, `used_pct`, `free_pct`, `free_bytes`,
  `total_bytes` for use by the hook environment.
- **Testability:** the stat source is injectable via `checks.Deps.DiskUsage
  func(path string) (DiskStats, error)`; a real `statfs`-backed default is used
  when the field is nil so the check works for both the daemon and inline use.
- **Growth point:** future `net` / `files` checks are added as new `case`
  branches in the same shared `buildCheck` switch (`internal/checks/build.go`)
  with their own params — no other change needed.

### 2. `hook` action — `internal/watch` (new package)

- Runs `command` (argv) with `os/exec`, **never** through a shell.
- Bounded by `timeout` (default `engine.default_timeout`).
- Sets context environment variables for the invoked program:
  `SERMO_WATCH` (watch name), `SERMO_CHECK_TYPE`, `SERMO_PATH`,
  `SERMO_VALUE` (the breaching value), `SERMO_MESSAGE` (the check message).
- The runner is an injectable interface (default `os/exec` implementation) so
  tests assert the argv/env/timeout without executing anything real.
- Safety note: this is argv-only with an exact command, consistent with the
  existing `command` check; it does not relax any safety invariant.

### 3. `Watch` unit + cycle — `internal/watch`

- `Watch` holds: `Name`, the built `disk` check, the optional window
  (`For`/`Within`), the hook spec, and the effective `Interval`.
- `RunCycle(ctx)`:
  1. run the check → `cond = result.OK`
  2. advance the window. `rules.WindowState.Fires` has signature
     `Fires(r rules.Rule, conditionTrue bool) bool` and reads only `r.For` /
     `r.Within`. A watch has no `rules.Rule`, so the watch builder constructs a
     **synthetic** `rules.Rule{For: ..., Within: ...}` once and passes it each
     cycle. (No refactor of `internal/rules` is required.)
  3. if it fires, run the hook with the env derived from `result.Data` and emit
     events (`hook` on success, `hook-failed` on error).
- The watch reuses `internal/rules` window types; it does **not** go through the
  service operation engine (no operation locks, no cooldown policy — a hook is a
  notification, not a service operation).

**Event/emitter changes (`internal/app/event.go`):** `app.Event` today has a
`Service` field but no watch identity, and `SlogEmitter` only routes the kinds
`action`/`alert`/`suppressed`/`error` at Info (others fall to Debug). To make
hook firings operator-visible:
- add a `Watch` field to `Event` (the watch name; `Service` stays empty for
  watch events);
- extend `SlogEmitter`'s switch to log `hook`/`hook-failed` at Info;
- extend the `Kind` doc comment to enumerate the new kinds.

### 4. Scheduler integration — `internal/app`

- Generalize the scheduler's per-item loop so it drives both service workers and
  watches with the same jitter / interval-from-completion / clean-shutdown
  logic. The service-only `gateOperate` (operation semaphore) stays applied to
  service workers only.
- Each watch runs on its own goroutine with its own `Interval` (default
  `engine.interval`). Shutdown (ctx cancel) stops watches between cycles like
  service workers.

### 5. Validation — `internal/config/validate.go`

`Validate()` today only iterates global settings and per-service documents; there
is **no** generic hook for a new top-level section. This is net-new plumbing: add
a `validateWatches(...)` step called from `Validate()` that reads the section
from `cfg.Global.Raw["watches"]` (same access pattern as `engine`/`paths`).

For each `watches.<name>`:
- `check.type` must be a known type; for `disk`: `path` required and at least one
  of `used_pct`/`free_pct` present, each with a valid `op` and numeric `value`.
- `then.hook.command` must be a non-empty array (if a `then` block is present).
- `interval` and `hook.timeout`, if present, must be valid positive durations.
- `for`/`within`, if present, validated with the existing window checks.

(Note: post-design, omitting the entire `then` key was made valid for alert-only
watches: they still produce "firing" events for the web UI and logs when the
`for` window is satisfied, but perform no actions and ignore global notify
defaults. See implementation evolution in the plans and current
`docs/configuration.md`.)

### 6. Daemon wiring — `cmd/sermod/main.go`

- Build watches from the loaded config (reporting unusable ones as warnings,
  same pattern as `app.BuildWorkers`).
- Pass them to the scheduler alongside service workers. A daemon with zero
  enabled services but ≥1 watch is valid (today zero services is a fatal error;
  the "nothing to do" check becomes "no services **and** no watches").

## Testing

- **`disk` check:** injected `DiskUsage` returning values above and below the
  threshold; verify `OK`, `Message`, and `Data`. Both `used_pct` and `free_pct`,
  and the AND of both. Missing `path` / missing predicate ⇒ build warning.
- **Watch cycle:** condition true fires the hook; `for: {cycles: 3}` fires only
  on the third consecutive true cycle; condition false does not fire; hook env
  carries the expected variables.
- **Hook runner:** argv + env + timeout via the injectable runner; failure emits
  `hook-failed`.
- **Validation:** good config passes; each malformed case (unknown type, missing
  path/predicate, bad op, empty command, bad duration) yields a specific issue.

## Out of scope (structure ready, not built)

- `net` (network state) and `files` (file count) check types — added later as new
  `case` branches in the disk check's type switch + their params + validation.
- HTTP webhook hook target (local command chosen for this iteration).
- A `sermoctl watches` listing/inspection command (can be added later).
- Absolute-bytes (`free_bytes`) and inode (`inodes_pct`) disk thresholds.

## Documentation

- `docs/configuration.md`: new "Host watches" section documenting `watches`, the
  `disk` check, and the `hook` action, plus the engine-settings cross-reference.
- `configs/sermo.yml`: a commented, disabled-by-default `watches` example.
- `README.md`: one line under the daemon description noting host watches.
