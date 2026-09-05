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
- Use the public model field name for the concept everywhere: variables,
  parameters, struct fields, comments, JSON, YAML.
- Split functions by responsibility. Do not extract a private helper just to
  shrink a function that the complexity analyzers already accept.
- Pass `context.Context` into every blocking call and bound it with a timeout
  from configuration or a named constant.
- Wrap errors with what was attempted, for a sysadmin reader:

  ```text
  restart mysql via openrc: rc-service mysql restart failed: exit code 1: service not found
  ```

- Keep exported APIs small. Do not add new package-level mutable state;
  registration belongs in the existing registry. Linux-specific behavior stays
  behind an interface with a fake for tests.
- External commands go through `execx`:

  ```go
  ctx, cancel := context.WithTimeout(parent, timeout)
  defer cancel()
  res, err := runner.Run(ctx, "systemctl", "restart", service)
  ```

  The `Result` carries stdout, stderr, exit code and duration.
- HTTP uses `internal/httpx`, never `http.DefaultClient`.
- Files Sermo locates on the host (procfs, sysfs, catalog, pidfiles, runtime
  locks and logs) are read through `internal/hostfs`, which accepts only
  absolute, clean paths.
- Fix analyzer findings at the source. A `//nolint` names the analyzer and the
  design reason.

## Protocol probes

Every `internal/conn` probe honors `cfg.Interface`. Built-ins and aliases are
registered in `internal/conn/registry.go`; registered probes enter the shared
executor once and obtain the prepared target through the existing helpers. Do
not add package-init registration or duplicate endpoint defaults.

Stream probes dial through `BindDialer`; packet listeners use
`BindListenConfig`. A library is acceptable only if it is codec-only or
accepts Sermo's dialer/connection. Reject libraries that perform unhookable
internal I/O.

Keep documented transport exceptions local: chronyd's Unix datagram client and
DHCP's per-datagram `IP_PKTINFO` path. A protocol may rebuild an endpoint only
when its wire format genuinely selects a different target, with the reason
documented at that call.

Length and count fields on the wire go through the `wire*` helpers in
`internal/conn/wire.go`; they return an error instead of truncating.

## Wizards

`internal/assist` owns the assistant flow. Use its shared `Prompt` helpers,
detected targets and the canonical monitor/interval flow. Do not invent a
separate prompt parser or ask for a target name that detection can provide.
`none` and `default` remain selectable with no configured notifier; an empty
inherited default degrades to monitor-only. Preview and confirm generated
files; offer cleanup only for targets proven absent by detection.

## Tests that accompany code

Cover the success, failure, timeout, invalid-input and unsafe-input paths the
change actually has, with table-driven subtests, fake runners and temporary
directories. Do not invent a blocked or timeout case for a pure formatter.
Never run real service commands or signal host processes from a test.
