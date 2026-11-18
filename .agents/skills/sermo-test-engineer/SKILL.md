---
name: sermo-test-engineer
description: Use when writing or reviewing Sermo tests or benchmarks, selecting hermetic fixtures for host interactions or measuring performance changes.
---

AGENTS.md owns the test rules (owning package's style, no ambient state, which
paths to cover); `sermo-safety-review` owns the safety rows. This skill is
where the hermetic seams are.

## Hermetic seams

A plain `go test` of any package passes on a host with Sermo installed and
runs no host command, in any order (`-shuffle=on`). The Makefile's catalog pin
(`GO_TEST_LDFLAGS`, a default catalog that cannot exist) is only a second net.

- Commands: `execxtest.Runner` (`internal/execx/execxtest`) answers by command
  line, by name, queued or fixed, records calls, supports `RunEnv`/`RunUser`,
  and `RunOnly` makes a test fail closed.
- `internal/config`: `TestMain` clears `SERMO_BACKEND`/`SERMO_INIT`, pins
  `${init}` to systemd and empties `PATH`. Load through `loadConfig`, which
  adds an empty catalog and no active units on either backend; a test's own
  `WithCatalogDirs`/`withServiceUnits` still wins.
- `internal/app`: `loadTestConfig` (empty catalog), `hermeticMonitor` for a
  `Monitor` that reloads, `execxtest.Runner` for `Deps.ExecxRunner`, an
  `OSHookRunner{Runner: fake}` for hooks, and a fake `Deps.ProcReader` or
  `Discoverer.Reader` instead of the host `/proc`.
- `internal/cli`: wrap every `App{…}` in `hermeticApp(t, …)`, which fills the
  nil seams with an empty-catalog loader, a fake detector, manager and runner,
  and daemon-discovery seams that fail the test if reached. Load configs with
  `loadTestConfig` or `testConfigLoader(t)`.
- `cmd/sermod`: set `daemonLoadOptions` to an empty catalog for a test that
  boots `run()`.

Reading the host is the subject of these tests, and only these: the procfs and
pidfd tests of `internal/process` and `internal/metrics`, the process prober
of `internal/locks`, `execx`'s runner, the `checks` pidfile and `mountctl`
scans of the test's own process, the `sermoctl lock -- COMMAND` tests, the
`cli_integration_test.go` shims (their own `PATH`, shell builtins only) and
sermod's run-smoke test (read-only init status).

After touching a seam, prove it: shims for `systemctl`, `rc-status` and
`rc-service` on `PATH` that log every call must log nothing, and
`strace -f -e trace=execve` of the test binary shows only the exceptions
above.

## Fixtures

Inline first. A file fixture goes in `testdata/` beside the owning package,
never in a cross-package tree: config and preflight resolution under
`internal/config`, daemon and web preflight under `internal/app`. No fixture
keeps a retired field name alive.

## Benchmarks

A benchmark that decides a change runs on a hermetic fixture (fake process
table, generated config) at worst-case sizes and reports the size with
`b.ReportMetric`. A live-host benchmark such as `BenchmarkSnapshot` is a smoke
reading, too noisy to decide. Compare old and new with
`-run '^$' -bench <Name> -benchmem -count=10` and `benchstat`
(`go install golang.org/x/perf/cmd/benchstat@latest`).
