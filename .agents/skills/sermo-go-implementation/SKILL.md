---
name: sermo-go-implementation
description: Use when implementing Go code for Sermo, especially CLI commands, internal packages, interfaces, command runners, config loading, checks, rules, locks, or operations.
---

AGENTS.md owns repository invariants (ownership, operation path, execx,
timeouts, builders, docs, gates). This skill is the coding habits that keep
those cheap to follow.

## Habits

- Find the owner first: `rg` for the concept, the model struct and the central
  builder. Put new behavior there, or extract/move if that owner cannot hold
  it. A new helper sits beside the owner, not in a new package, until ownership
  actually crosses a boundary.
- Split functions by responsibility. Do not extract a private helper just to
  shrink a function that the complexity analyzers already accept.
- Wrap errors with what was attempted, for a sysadmin reader:

  ```text
  stop mysql via openrc: rc-service mysql stop failed: exit code 1: service not found
  ```

- Keep exported APIs small. Do not add new package-level mutable state;
  registration belongs in the existing registry. Linux-specific behavior stays
  behind an interface with a fake for tests.
- External commands go through `execx`:

  ```go
  ctx, cancel := context.WithTimeout(parent, timeout)
  defer cancel()
  res, err := runner.Run(ctx, "systemctl", "stop", service)
  ```

  The `Result` carries stdout, stderr, exit code and duration.
- HTTP uses `internal/httpx`, never `http.DefaultClient`.

## Protocol probes

Every `internal/conn` probe honors `cfg.Interface`. Built-ins and aliases are
registered in `internal/conn/registry.go`; registered probes enter the shared
executor once and obtain the prepared target through the existing helpers. Do
not add package-init registration or duplicate endpoint defaults.

Stream probes dial through `BindDialer`; packet listeners use
`bindListenConfig`. A library is acceptable only if it is codec-only or
accepts Sermo's dialer/connection. Reject libraries that perform unhookable
internal I/O.

Keep documented transport exceptions local: chronyd's Unix datagram client and
DHCP's per-datagram `IP_PKTINFO` path. A protocol may rebuild an endpoint only
when its wire format genuinely selects a different target, with the reason
documented at that call.

A server that answers it cannot serve now (too many connections, loading,
starting up, a 421 greeting) is a busy verdict, not an error: classify it
through `internal/conn/busy.go` and return `busyResult(...)` with a nil error,
or `AsBusy` for driver errors. Add new codes there, as constants, so probes,
`sql` and `db_queries` share one vocabulary.

Variable length and count fields on the wire go through the `wire*` helpers in
`internal/conn/wire.go`; they return an error instead of truncating. A conversion
with an explicit local bound, such as FastCGI string lengths below 128, can
convert directly. Keep checks at helpers that accept arbitrary buffers or ports.

## Wizards

`internal/assist` owns the assistant flow. Use its shared `Prompt` helpers,
detected targets and the canonical monitor/interval flow. Do not invent a
separate prompt parser or ask for a target name that detection can provide.
`none` and `default` remain selectable with no configured notifier; an empty
inherited default degrades to monitor-only. Preview and confirm generated
files; offer cleanup only for targets proven absent by detection.

## Tests that accompany code

`sermo-test-engineer`.
