---
name: sermo-go-implementation
description: Use when implementing Go code for Sermo, especially CLI commands, internal packages, interfaces, command runners, config loading, checks, rules, locks, or operations.
---

You are implementing Go code for Sermo.

## Coding rules

1. Write idiomatic Go.
2. Keep functions small and readable.
3. Use `context.Context` for all blocking operations.
4. Every external command must have a timeout.
5. Do not call `os/exec` directly from many packages. Use an injectable command runner.
6. Wrap errors with context.
7. Prefer table-driven tests.
8. Do not use package-level mutable state unless unavoidable.
9. Keep exported APIs minimal.
10. Keep Linux-specific behavior behind interfaces where possible.
11. Use consistent names for the same concept across variables, parameters,
    comments, and struct fields; when in doubt, prefer the existing struct/API
    field name as the canonical term. (See AGENTS.md "Naming and terminology".)

## External command pattern

Service commands, config checks and app checks must use a runner abstraction, for example:

```go
type Runner interface {
    Run(ctx context.Context, name string, args ...string) (Result, error)
}
```

The result should include:

```text
stdout
stderr
exit code
duration
```

Tests should use a fake runner.

## Context and timeout

Do not write this:

```go
exec.Command("systemctl", "restart", service).Run()
```

Prefer:

```go
ctx, cancel := context.WithTimeout(parent, timeout)
defer cancel()
runner.Run(ctx, "systemctl", "restart", service)
```

## Error messages

Errors must help a sysadmin.

Good:

```text
restart mysql via openrc: rc-service mysql restart failed: exit code 1: service not found
```

Bad:

```text
error
failed
```

## Package guidance

Use these packages. This is the full internal/ layout; it must match
`AGENTS.md` and AGENTS.md — do not invent or drop packages:

```text
internal/app          daemon, scheduler and in-memory state (sermod)
internal/checks       check implementations
internal/cli          sermoctl command implementations
internal/config       YAML model, loading, merge, render, variables, validation
internal/events       structured event model and logger
internal/execx        command runner
internal/locks        runtime locks and external lock checks
internal/metrics      cpu/memory collectors (service and system scope)
internal/operation    safe start/stop/restart workflows (shared by sermod+sermoctl)
internal/preflight    preflight runner (reuses the check runner)
internal/process      process discovery and signaling
internal/profiles     profiles, services, clones, render
internal/rules        rule engine, windows and remediation state
internal/servicemgr   systemd/openrc abstraction
```

## Test requirements

When adding code, add tests for:

```text
success path
failure path
timeout path
invalid input
unsafe input
edge cases
```

Use fake runners and temporary directories. Do not run real service commands in unit tests.

## Output expectation

When modifying code, summarize:

```text
- packages changed
- new behavior
- safety checks preserved
- tests added
- commands run
```
