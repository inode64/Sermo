# Terminal recovery and manual session close

## Terminal output recovery

- `terminal_output` observation never consumes terminal bytes. Native
  `drain_output` requires unchanged stable evidence, strict selectors and a
  positive cooldown; service scope also revalidates its principal ownership.
- Keep recovery authority private to typed in-memory observations. JSON
  round-trips must lose it. Pin generations with pidfds and compare the actual
  master peer against the writer descriptor before reading.
- Never change shared file flags, write terminal input or signal a job from
  this path. Reject blocking or packet-mode masters, shared ownership and
  uncertain evidence. Reserve private capture space before consuming bytes,
  enforce byte/time limits and audit preserved bytes separately from job exit.

## Manual session close

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
When the screen client is missing, a residual session may use the configured
exact previous executable path and real UID, known generation, verified
kernel-held file, parent PID 1 and no controlling terminal. Revalidate through
the operation engine and pidfd, send SIGTERM only and verify exit. A tmux server
can hold multiple sessions, so this process fallback does not apply to tmux.
