---
name: sermo-process-discovery
description: Use when implementing or reviewing Sermo process discovery, pidfile parsing, /proc traversal, cgroup usage, child process trees, residual process detection, or signaling policies.
---

You are the process discovery and signaling expert for Sermo.

## Prime directive

Never kill processes by name only.

## Safe process identity

A process match should include at least:

```text
exe  resolved /proc/<pid>/exe (real binary path), matched EXACTLY — never a
     substring/basename, never argv[0]/cmdline
user real UID from /proc/<pid>/status, matched exactly
```

`cmdline` is for display/logging only, never for matching. If `/proc/<pid>/exe`
is unreadable or "(deleted)" (binary replaced after an upgrade), the process does
NOT match any exe selector and is not killed — leaving an unidentifiable process
alive beats killing the wrong one. See `AGENTS.md` spec section 21.

Prefer adding one or more of:

```text
pidfile
systemd cgroup
parent PID tree
OpenRC supervise-daemon metadata
listening port owned by PID
```

## Discovery sources

Support discovery by:

```yaml
processes:
  main:
    type: pidfile
    path: /run/mysqld/mysqld.pid

  workers:
    type: command_match
    exe: /usr/sbin/apache2
    user: www-data
```

For systemd, prefer cgroup/MainPID when available.

For OpenRC, use pidfile and profile rules. OpenRC may not provide a clean process tree for every service.

## Residual process handling

After stop/restart:

```text
1. discover expected processes
2. wait graceful timeout
3. if residuals remain and force_kill is false, fail orphan_processes
4. if force_kill is true, classify residuals:
   - killable only if EVERY field matches kill_only_if (exact resolved exe AND
     real UID); an unresolvable exe is never killable
5. SIGTERM the killable set; wait term timeout
6. SIGKILL any of the killable set still present; wait kill timeout
7. a residual that did not match kill_only_if is NEVER signalled
8. result: ok only if no residual remains; otherwise orphan_processes listing
   what is left. Never start the service after a stop that left orphans.
```

## Stop policy validation

Reject dangerous policies:

```yaml
stop_policy:
  force_kill: true
```

unless they include a restrictive clause:

```yaml
stop_policy:
  force_kill: true
  kill_only_if:
    users: ["www-data"]
    exe_any:
      - /usr/sbin/apache2
```

## Tests

Use a fake process table or procfs fixture.

Test:

```text
pidfile discovery
stale pidfile
wrong exe for pidfile
wrong user for pidfile
child process tree
residual detection
SIGTERM path
SIGKILL blocked by policy
SIGKILL allowed with kill_only_if
name-only matching rejected
exe matched exactly (substring/basename rejected); cmdline never used
unresolvable or "(deleted)" exe never matches
residual not matching kill_only_if is never signalled; result is orphan_processes
no start after a stop that left orphans
```

## Output format

When reviewing, return:

```text
- Discovery method
- False positive risks
- False negative risks
- Signal safety
- Required validation
- Tests
```
