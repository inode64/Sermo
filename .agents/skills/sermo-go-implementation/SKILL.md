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
11. Use exactly the same name for a concept in variables, parameters, comments
    and struct fields. Look at the model structs first; prefer the public field
    name as the canonical term (see AGENTS.md "Naming and terminology").
12. Prefer the Go standard library or a Go-module alternative over external
    commands. When one is genuinely required, never call `os/exec` directly:
    route it through the `execx` runner (context + timeout). See AGENTS.md
    "Native by default".
13. All service start/stop/restart/reload/resume/signals must go through the
    `internal/operation` package. Do not bypass it. See AGENTS.md "Service
    operations".
14. Keep documentation, catalog examples and `docs/configuration.md` / `docs/rules.md`
    in step with any config, check, notifier or behavior change. See AGENTS.md
    "Documentation lockstep".
15. Introduce new check types, watches, notifiers and rule actions only through
    the central builder functions. Do not scatter construction logic. See
    AGENTS.md "Central builders".
16. Bound every blocking operation with a timeout from configuration or a named
    constant. No magic durations in application logic. See AGENTS.md "Timeout
    discipline".

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

Use these packages. This is the current full `internal/` layout; it must match
the repository — do not invent or drop packages:

```text
internal/app          sermod daemon, scheduler, in-memory state, event log and web preflight
internal/appinspect   catalog app/library inspection and installed-version discovery
internal/assist       wizard prompt helpers and assistant flow primitives
internal/buildinfo    build/version metadata
internal/cfgval       typed config value parsing and validation helpers
internal/checks       check implementations and central check builders
internal/cli          sermoctl command implementations
internal/config       YAML model, catalog loading, services/watches/clones, merge, render, variables, validation
internal/conn         protocol probes used by connection checks
internal/control      daemon control socket/client helpers
internal/diag         diagnostics assembly
internal/dockerctl    Docker control helpers
internal/execx        command runner
internal/locks        runtime locks and external lock checks
internal/metrics      CPU/memory/process collectors and time-series helpers
internal/mountctl     mount/umount operation helpers
internal/notify       notifier implementations
internal/operation    safe start/stop/restart/reload/resume workflows shared by sermod and sermoctl
internal/process      process discovery, identity matching and signaling
internal/rules        rule engine, windows and remediation state
internal/servicemgr   systemd/OpenRC abstraction
internal/state        persisted daemon state and migrations
internal/virt         virtualization control helpers
internal/volume       volume/storage expansion helpers
internal/web          web API contracts and embedded UI server
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
