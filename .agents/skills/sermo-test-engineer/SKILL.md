---
name: sermo-test-engineer
description: Use when adding or reviewing tests for Sermo, especially config resolution, backend detection, rule evaluation, locks, guards, process discovery, and safe operations.
---

Match the owning package's existing test style before adding a pattern of
your own. AGENTS.md owns the ambient-state ban; `sermo-safety-review` owns
the safety rows.

## Style

- Table-driven subtests with `name`, inputs, `want` and `wantErr`.
- Existing fakes and injectable seams over new mocking frameworks. Command
  execution is scripted with `execxtest.Runner` (`internal/execx/execxtest`):
  answers by command line, by name, queued or fixed, with recorded calls,
  `RunEnv`/`RunUser` support and `RunOnly` for fail-closed tests.
- Temporary directories; no dependency on ambient `/etc`, `/proc`, network or
  init state. Tests run with `-shuffle=on`, so order must not matter.
- Preserve contract distinctions such as nil versus empty.
- Short literal timeouts are fine to bound the test itself.

## Never in a test

```text
systemctl or rc-service start/stop/restart/reload
kill, pkill, killall, sudo, doas against real processes
```

Mock command execution and process tables instead.

## What to cover

Cover the paths the change actually has: success, invalid input, blocked
unsafe input, timeout/error. Do not invent a blocked or timeout case for a
pure formatter. Safety-sensitive changes also cover the relevant rows in
`sermo-safety-review`.

## Fixtures

Prefer inline fixtures. When a file is needed, create `testdata/` beside the
owning package; never a cross-package fixture tree. Config resolution and
preflight-resolution fixtures belong under `internal/config`; daemon and web
preflight fixtures under `internal/app`. Do not add fixtures that keep retired
field names alive.
