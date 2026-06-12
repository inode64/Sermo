# Stopped-state invariants + richer process matching — design

## Goal

When a service is stopped, verify it **really** stopped and left nothing behind:
no residual processes, no pidfile, no stale socket/lock files. Enrich the process
definition (`cmd` cmdline regex, `group`) so shared-binary daemons are matched the
way the legacy `rc-services.sh` did (`java.*unifi`, `openvpn.*tun1.conf`,
processes by user/group). Inspired by that script's per-service residual killing
and stale-socket removal (`rm /run/postgresql/.s.PGSQL*`).

Confirmed decisions: invariants live in `stop_policy:`; a violated *pidfile/files*
invariant **warns** (the stop still succeeds); stale-file removal is **opt-in**
(`remove`, default report-only).

## Part 1 — Richer `command_match` selectors

Today `command_match` matches **exe + user** only; the cmdline is captured but
"never matched" by design, and there is no group match (`internal/process`). Add,
opt-in:

```yaml
processes:
  unifi: { type: command_match, cmd: "java .*unifi", user: unifi, group: unifi }
  mongo: { type: command_match, exe: /usr/bin/mongod, user: unifi }
```

- **`cmd`** — a Go RE2 regex matched against the process cmdline (argv joined with
  spaces). The "never match cmdline" stance is relaxed **only when `cmd` is set**;
  exe matching keeps its fail-safe `/proc/<pid>/exe` semantics.
- **`group`** — matches the process real GID, like `user` matches the real UID
  (resolve via the group database; capture GID in the process Identity, which
  currently captures only UID).
- Matching is the **AND** of the present fields; at least one of `exe`/`cmd` is
  required (so a selector is never user-only). Validation: require `exe` or `cmd`;
  `user`/`group` optional.

Because the residual **reaper** and the `process` check use the same selectors,
this immediately makes both richer — the reaper now catches and kills
`java.*unifi`-style residuals on stop (keeping the existing
`ResultOrphanProcesses` semantics when it cannot).

Touch points: `internal/process/model.go` (Selector `Cmd`/`Group`, Identity
`GID`), `discover.go` (`matches` AND-logic, snapshot GID, compile `cmd` regex
once), `internal/config/validate_service.go` (relax command_match validation),
`internal/checks` process check (it builds selectors — inherits the fields).

## Part 2 — Stopped-state invariants in `stop_policy:`

```yaml
stop_policy:
  graceful_timeout: 30s
  force_kill: false
  # new — verified AFTER a successful stop:
  pidfile_absent: true                      # the declared pidfile must be gone
  files_absent: [/run/postgresql/.s.PGSQL*] # stale sockets/locks (globs)
  remove_stale: false                       # opt-in: delete stale files, then re-verify
```

Semantics (after the stop's residual handling completes cleanly):
- **PID snapshot**: the engine records the PIDs discovered **before** the stop, so
  after stopping it can report precisely which known PID survived (in addition to
  the reaper's discover-after check). A surviving known PID is the existing
  orphan-process path (red) — unchanged.
- **pidfile_absent / files_absent** (the *new* invariants): after a clean stop,
  the engine checks the declared pidfile (resolved from `pidfile:` /
  `processes.pidfile`) and each `files_absent` glob. A still-present artifact:
  - with `remove_stale: false` → **warns** — the stop Result stays `ResultOK` but
    its Message notes the stale artifact (the same message-fold pattern as
    `also_service` best-effort stop errors), surfaced orange in CLI/web/notifier.
  - with `remove_stale: true` → the engine deletes the artifact, then re-verifies;
    a delete failure (or a still-present file) downgrades to the same warning.
- Residual **processes** keep their current behavior (the reaper kills them; an
  unkillable residual is `ResultOrphanProcesses`, red) — the richer matching just
  feeds it more candidates. Only the new pidfile/files invariants warn.

## Engine integration

`internal/operation/engine.go`, in the stop block after `clearResiduals` +
`ResetState` (the clean-stop point): run a `verifyStopped` step that consults
`Engine.StopArtifacts` (pidfile path, files globs, remove flag) and folds any
warning into the final message (stash in a local slice like the also_service stop
errors, applied where the success message is built). The PID snapshot is taken at
the start of the stop block.

`internal/operation/build.go` + callers (cli, webbackend): resolve
`config.StopArtifacts(tree)` (pidfile path + files + remove + pidfile_absent
flags) for the active backend and set `Engine.StopArtifacts` — mirroring how
`AlsoUnits` is passed (caller resolves, engine receives), so daemon + CLI + web
all get it.

## Config accessor & validation

- `internal/config/model.go`: `StopArtifacts(tree)` → `{ PidfileAbsent bool,
  PidfilePath string, Files []string, Remove bool }`, reading `stop_policy` and
  the declared pidfile (`processes.pidfile.path` or the `pidfile:` shorthand).
- `internal/config/validate_service.go`: validate the new `stop_policy` keys
  (booleans, `files_absent` a string list); a `pidfile_absent: true` with no
  declared pidfile is a warning (nothing to check).

## Optional stopped-state health check (noted, scoped out of v1)

A monitoring-time check that, when the service is detected **inactive**, asserts
the same invariants (no residual matching process, no pidfile, no stale files) —
catching a service that crashed leaving junk while the init reports it down. This
reuses the selectors + StopArtifacts and is a natural follow-up; v1 focuses on the
stop **operation** path the user described.

## Testing

- `matches`: cmd-regex match, group match, AND-combination, exe-or-cmd required.
- engine: a clean stop with a lingering pidfile/file warns (ResultOK + message);
  `remove_stale` deletes then passes; a surviving known PID still reports orphan;
  no `stop_policy` invariants → unchanged behavior.
- config: StopArtifacts resolution; validation of new keys and relaxed
  command_match.
- A catalog example (postgres-style `files_absent` socket, or unifi `cmd` match).

## Out of scope (v1)

- The standalone inactive-state health check (above).
- Killing residuals by `files_absent` ownership (files only, not fuser-style).
- Cross-service artifact checks.
