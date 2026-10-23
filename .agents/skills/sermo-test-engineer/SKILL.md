---
name: sermo-test-engineer
description: Use when adding or reviewing tests or benchmarks for Sermo, especially config resolution, backend detection, rule evaluation, locks, guards, process discovery, safe operations, and performance comparisons.
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
  init state. Tests run with `-shuffle=on`, so order must not matter. A plain
  `go test` of any package must pass on a host with Sermo installed and run no
  host command; the Makefile's catalog pin (`GO_TEST_LDFLAGS`, a default
  catalog that cannot exist) is only a second net.
- Use the package's hermetic helpers:
  - `internal/config`: `TestMain` clears `SERMO_BACKEND`/`SERMO_INIT`, pins
    `${init}` to systemd and empties `PATH`; load through `loadConfig`, which
    adds an empty catalog and no active units on either backend (a test's own
    `WithCatalogDirs`/`withServiceUnits` still wins).
  - `internal/app`: `loadTestConfig` (empty catalog), `hermeticMonitor` for a
    `Monitor` that reloads, `execxtest.Runner` for `Deps.ExecxRunner`, an
    `OSHookRunner{Runner: fake}` for hooks, and a fake `Deps.ProcReader` or
    `Discoverer.Reader` instead of the host `/proc`.
  - `internal/cli`: wrap every `App{…}` in `hermeticApp(t, …)`, which fills the
    nil seams with an empty-catalog loader, fake detector, manager and runner,
    and daemon-discovery seams that fail the test if reached; load configs with
    `loadTestConfig` or `testConfigLoader(t)`.
  - `cmd/sermod`: set `daemonLoadOptions` to an empty catalog for a test that
    boots `run()`.
- Named exceptions, because reading the host is what they test: the procfs and
  pidfd tests of `internal/process` and `internal/metrics`, `internal/locks`'s
  process prober, `execx`'s runner, `checks` pidfile and `mountctl`'s
  `/proc` scan of its own process, the `sermoctl lock -- COMMAND` tests, the
  `cli_integration_test.go` shims (their own `PATH`, shell builtins only) and
  sermod's run-smoke test (read-only init status).
- Prove hermeticity after touching seams: shims for `systemctl`, `rc-status`
  and `rc-service` on `PATH` that log every call must log nothing, and
  `strace -f -e trace=execve` of the test binary shows only the exceptions.
- Preserve contract distinctions such as nil versus empty.
- Short literal timeouts are fine to bound the test itself.

## What to cover

AGENTS.md lists the paths. Do not invent a blocked or timeout case for a pure
formatter. Safety-sensitive changes also cover the relevant rows in
`sermo-safety-review`.

## Benchmarks

A benchmark that decides a change runs on a hermetic fixture (fake process
table, generated config) at worst-case sizes and reports the size with
`b.ReportMetric`. A live-host benchmark such as `BenchmarkSnapshot` is a smoke
reading, too noisy to decide. Compare old and new with
`-run '^$' -bench <Name> -benchmem -count=10` and `benchstat`
(`go install golang.org/x/perf/cmd/benchstat@latest`).

## Fixtures

Prefer inline fixtures. When a file is needed, create `testdata/` beside the
owning package; never a cross-package fixture tree. Config resolution and
preflight-resolution fixtures belong under `internal/config`; daemon and web
preflight fixtures under `internal/app`. Do not add fixtures that keep retired
field names alive.
