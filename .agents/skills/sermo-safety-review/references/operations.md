# Operation path

1. Start/restart/reload/resume/repair is blocked by a failed required preflight; an
   optional preflight warns. A `verify: true` check failing after the operation
   returns `postflight_failed`.
2. The operation lock is released on every exit path. Named runtime locks live
   under `<engine.runtime>/locks`, operation locks under `<engine.runtime>/ops`;
   they never share a directory and are never loaded from `/etc/sermo`.
   A named lock read/parse warning for the target service prevents its operation;
   do not treat a partially written lock as absent.
3. Locks are staged with `O_CREAT|O_EXCL`, synced and closed before no-replace
   hard-link publication, and are TTL-bounded. A stale lock
   (expired or dead owner) is reclaimed through a logged path, never silently
   overwritten: every locker (operation, named, watch control, expansion,
   mount) reports the reclaim, and daemon, web and sermoctl record it as the
   same warning event (`app.LockReclaim`).
4. An explicit `operation_group` serializes related lifecycle actions only:
   acquire its separate group lease and the service lease before any backend
   work, release both on every path and keep contention nonblocking. A cascade
   releases those leases between members, so it cannot deadlock itself.
5. Restart is stop/verify/reconcile/start/verify under the same lock and
   deadline. A Docker target positively confirmed inactive only starts; an
   inspect 404 may create a stopped target from a registered Compose source,
   then start its verified new ID. Original restart guards still apply, and
   the audit retains both requested and effective actions and any created ID.
   There is no backend restart mode. Fresh process observations must
   distinguish confirmed absence from incomplete discovery. Process-free services
   cannot be judged by missing PIDs.
6. A failed stop command may have stopped the process: retain the error, verify
   absence and reconcile init before proceeding. Without a strict selector the
   only absence proof is that every generation seen before the stop left the
   process table and the backend reports the service inactive or failed; an
   empty PID lookup alone proves nothing. A native init gets the postflight
   window to read the exit (a just-killed daemon is a zombie that OpenRC still
   reads as started), with the process table read again at every retry. A failed start requires a trusted
   live process, active init and postflight. Never discard reset errors or infer
   reload/resume success from liveness. systemd reset-failed does not deactivate
   an active unit; OpenRC zap must never hide a surviving daemon.
7. Accept systemd reactivation only after verifying old process generations
   exited across the complete process snapshot, including outside the current
   cgroup, and the same unit owns the new generation. Never signal the replacement
   or accept an unchanged daemon as a completed restart. Restore stopped
   auxiliary units even when the primary start is skipped.
   A newly observed backend generation still starting gets the bounded
   readiness window before classification; uncertainty never authorizes a
   signal to that replacement. Recheck reactivation on every rediscovery,
   including after SIGTERM and during init reconciliation. A positive activating
   observation may wait for a new PID; a reset permission or query error cannot
   be recovered merely because the service later looks active.
8. Deleted-executable evidence with the main selector's previous path and real
   user may permit a backend stop, but never validates a new start. Keep it
   separate from current exact identity; workers cannot substitute for an exact
   main selector, and zombies cannot prove a process is running. For an `exe`
   that is a link to a versioned file, the previous path also covers the
   deleted previous version beside it (`internal/process/versioned_exe.go`).
   That naming evidence adds no signal authority (`kill_only_if` stays exact),
   and a live match re-reads the link instead of guessing. A live executable
   below a replaced directory link (a JVM's `bin/java`) also requires that
   current exact resolution; it does not extend deleted-executable naming.
9. Incomplete rediscovery stops signal escalation. Keep stop-command,
   auxiliary-stop and artifact warnings in the one audit result even if a
   subsequent phase fails or times out.
   Before init reconciliation, transient strict-identity absence uncertainty
   may retry fresh observations within the existing operation deadline and a
   bounded attempt count. Never reset or start while uncertainty remains;
   newly observed residuals, snapshot errors and cancellation still refuse it.
