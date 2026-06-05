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

Use these packages:

```text
internal/execx        command runner
internal/servicemgr   systemd/openrc abstraction
internal/config       YAML loading and validation
internal/profiles     profiles, services, clones, render
internal/checks       check implementations
internal/rules        rule engine
internal/locks        lock management
internal/process      process discovery and signaling
internal/operation    safe start/stop/restart workflows
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
