# Network interface watch: `net` check with per-metric hooks - historical design

**Date:** 2026-06-06
**Status:** Historical design. Network interface watches are implemented; use
[`docs/configuration.md`](../../configuration.md#network-interface-watches-net)
and [`docs/rules.md`](../../rules.md) for current operator-facing behavior.
**Builds on:** `2026-06-06-host-watches-disk-design.md` (the watch/hook framework)

## Problem

The host-watch framework currently ships one resource check: `disk`. A watch is
**one check → one hook**. We now want to monitor **network interfaces**, where a
single interface has several independent aspects worth watching, each needing its
**own** hook:

- **state**: the interface goes up/down (a change) or is in a given state.
- **speed**: the link speed changes.
- **errors**: rx/tx error counters rise beyond a threshold.

So one interface produces multiple metrics, and each metric must trigger a
different hook. This is the first watch type that needs **per-metric hooks**, and
the first that needs **state across cycles** (change detection vs a baseline).

## Decisions (from brainstorming)

1. **Config shape:** grouped per interface. One `watches` entry of `type: net`
   names the interface once and lists a `metrics` map; each metric carries its
   own condition and its own `then.hook`. (Chosen over one-watch-per-metric.)
2. **No engine change:** `BuildWatches` **expands** a `net` watch into one
   internal `*Watch` per metric. The scheduler and `Watch.RunCycle` are unchanged
   — they already run a list of watches, each with its own check/window/hook.
3. **state metric:** supports both `on: change` (fires on an up↔down transition
   vs the previous cycle) and `expect: up|down` (fires while in that state).
4. **errors metric:** `delta` — fires when the summed counters **increase** by
   more than the threshold since the previous cycle (a per-cycle rate). Not an
   absolute total. First cycle establishes the baseline and never fires.
5. **speed metric:** `on: change` only (no speed threshold this iteration).
6. **interface selection:** a single named interface per watch (no glob/"all"
   this iteration).
7. **state definition:** "up" = the interface is administratively UP **and** has
   carrier (`net.Flags` `FlagUp` and `FlagRunning`); otherwise "down".
8. **`hookEnv` generalised:** every `Result.Data` key is exported as
   `SERMO_<UPPER_KEY>`, replacing the disk-specific mapping (while keeping
   `SERMO_PATH`/`SERMO_VALUE` working for disk).

## Configuration

```yaml
watches:
  net-eth0:
    enabled: true                 # optional, default true
    interval: 30s                 # optional, default engine.interval; applies to all metrics
    check: { type: net, interface: eth0 }
    metrics:
      state:
        on: change                # OR: expect: up | down
        for: { cycles: 1 }        # optional per-metric window
        then:
          hook: { command: [/usr/local/bin/net-state.sh], timeout: 10s }
      speed:
        on: change
        then:
          hook: { command: [/usr/local/bin/net-speed.sh] }
      errors:
        counters: [rx_errors, tx_errors]   # optional, default [rx_errors, tx_errors]
        delta: { op: ">", value: 100 }
        then:
          hook: { command: [/usr/local/bin/net-err.sh] }
```

- The **hook is per metric** (inside each `metrics.<name>.then.hook`), not at the
  watch top level. (Disk keeps its top-level `then.hook`.)
- A `net` watch expands into internal watch units named `net-eth0:state`,
  `net-eth0:speed`, `net-eth0:errors`. The unit's `SERMO_WATCH` is the base watch
  name (`net-eth0`); `SERMO_METRIC` and `SERMO_INTERFACE` distinguish them.
- `metrics` keys are restricted to `state`, `speed`, `errors`.

## Components

### 1. `net` check — `internal/checks/net.go`

A new check type registered in the shared `buildCheck` switch. Unlike the other
checks it is **stateful across cycles**: it remembers the previous sample to
detect changes/deltas. This is safe because each watch ticks sequentially on its
own goroutine (the scheduler never runs a watch's cycle concurrently with
itself), so a single check instance sees no concurrency. Implemented as a pointer
type (`*netCheck`) so the state persists across `Run` calls.

- **Params:** `interface` (required); `metric` (state|speed|errors); plus the
  per-metric condition fields.
- **Sampling:** injectable `Deps.NetSampler func(iface string) (NetSample, error)`;
  the default reads `net.Interfaces()` flags and
  `/sys/class/net/<iface>/{speed,statistics/<counter>}` (Linux-only runtime).
  `NetSample` carries: `State string` ("up"/"down"), `SpeedMbps int64` (with a
  validity flag — `/sys/.../speed` is -1 or unreadable when the link is down),
  `Counters map[string]uint64`.
- **Behavior per metric** (`Result.OK == true` means "fire"):
  - `state` + `expect: up|down` → OK when the current state equals `expect`.
  - `state` + `expect: up|down` `Data`: `value` = the current state (e.g. "down");
    no `old`/`new` keys in this mode.
  - `state` + `on: change` → OK when the current state differs from the previous
    cycle's. First cycle: record, OK=false. `Data`: `old`, `new`, `value=new`.
  - `speed` + `on: change` → OK when the current valid speed differs from the
    previous valid speed (both must be known; a missing reading does not fire,
    leaving link-up/down to the `state` metric). `Data`: `old`, `new`, `value=new`.
  - `errors` + `delta: {op, value}` → OK when `(sum(counters now) - sum(prev))`
    satisfies the comparison. First cycle: record, OK=false. `Data`: `value=delta`,
    `total`, `counters` (the names). Negative deltas (counter reset) are treated
    as 0.
- **Data (always):** `interface`, `metric`, plus the metric-specific keys above.
- **Growth point:** future `files` metric/check is a new `case` in the same
  switch + its fields; the per-metric expansion already generalises.

### 2. `BuildWatches` expansion — `internal/app/watch_build.go`

- `disk` watches build exactly as today (one `*Watch`, top-level `then.hook`).
- `net` watches expand: read `check.interface` and the `metrics` map; for each
  metric key build a `*Watch` with:
  - `Name` = base watch name (e.g. `net-eth0`); `CheckType` = "net".
  - `Check` = a `net` check built via `checks.BuildInline` with an entry merging
    `{type: net, interface, metric: <key>, <metric condition fields>}`.
  - `Window` = `rules.Rule{For, Within}` from the metric's `for`/`within`.
  - `Hook` = from `metrics.<key>.then.hook` (command required, optional timeout).
  - `Interval` = the watch-level `interval` (default the passed `defaultInterval`).
  - `Now`/`Emit` from `deps`.
- Malformed entries are skipped with a warning (same convention as disk/services).

The check-type dispatch in `BuildWatches` becomes a small switch on
`check.type` (`disk` → single; `net` → expand). Helper functions stay focused.

### 3. Generalised `hookEnv` — `internal/app/watch.go`

Replace the disk-specific env mapping with a generic one:

- Always set `SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_MESSAGE`.
- For every `k, v` in `res.Data`: set `SERMO_<UPPER(k)>` = stringified `v`
  (e.g. `path`→`SERMO_PATH`, `value`→`SERMO_VALUE`, `interface`→`SERMO_INTERFACE`,
  `metric`→`SERMO_METRIC`, `old`→`SERMO_OLD`, `new`→`SERMO_NEW`).
- Key names are uppercased; non-alphanumeric chars become `_`.

To preserve disk's documented `SERMO_VALUE`, the **disk check** now adds a
`value` key to its `Data` (the breaching percentage). Precedence: use `used_pct`
when a `used_pct` predicate is configured, else `free_pct` — key off **predicate
presence** (the disk check knows its `preds`), which matches the prior fallback
order. This replaces the previous `hookEnv` fallback logic, which is removed. `SERMO_PATH` continues to come from
the existing `path` key. (Disk's `used_pct`/`free_pct`/`free_bytes`/`total_bytes`
also surface as `SERMO_USED_PCT` etc. — harmless, and documented.)

### 4. Validation — `internal/config/validate.go`

Extend `validateWatches` with a per-`check.type` branch:

- `disk` (unchanged): `validateDiskCheck` + top-level `validateWatchHook`.
- `net`: require `interface` (non-empty string) and a non-empty `metrics`
  mapping. For each metric:
  - key ∈ {`state`, `speed`, `errors`} (else issue).
  - `state`: exactly one of `on: change` or `expect: up|down` (valid value).
  - `speed`: `on: change`.
  - `errors`: `delta` is a `{op, value}` mapping with a valid op and numeric
    value; `counters`, if present, is a non-empty list of strings.
  - each metric that declares a `then` requires `then.hook.command` (non-empty
    array); optional `then.hook.timeout` a valid positive duration.
  - optional per-metric `for`/`within` validated with the same window checks.

(Note: design-time assumption was that every metric/watch would have a `then`.
The implementation was later relaxed so that omitting `then` (or the per-metric
equivalent) is valid for alert-only watches: they produce "firing" state/events
for the web UI and logs but no delivery/actions.)
- Top-level `interval` validated as today.

**Hook-validation reuse boundary:** the existing `validateWatchHook(name, entry, add)`
reads the *watch-level* `entry["then"]["hook"]`. For `net`, the hook lives at
`entry["metrics"][<key>]["then"]["hook"]`, so it cannot be reused unchanged.
Generalise it to accept the sub-map and a scoped message prefix (e.g.
`validateHookBlock(prefix string, block map[string]any, add)`), and call it with
`watches.<name>` for disk and `watches.<name>.metrics.<key>` for each net metric.

Issue messages are watch-scoped, e.g.
`watches.net-eth0.metrics.errors.delta has an invalid op "=>"`.

### 5. Daemon

No change beyond what already exists — `BuildWatches` returns the expanded
`*Watch` list and the daemon already passes it to the scheduler.

## Testing

- **`net` check** (injected `NetSampler`): state on-change up→down fires with
  `old`/`new`; expect-state fires only in that state; speed on-change fires on a
  numeric change and not on a missing reading; errors delta fires above threshold
  and not on first cycle; counter reset yields delta 0 (no spurious fire).
- **`BuildWatches` net expansion:** one `net-eth0` with three metrics builds 3
  watches with the right names, check types, and distinct hook commands; disabled
  watch skipped; malformed metric warns.
- **Generalised `hookEnv`:** disk still yields `SERMO_PATH` and `SERMO_VALUE`;
  net yields `SERMO_INTERFACE`, `SERMO_METRIC`, `SERMO_VALUE`, `SERMO_OLD`,
  `SERMO_NEW`.
- **Validation:** good net config passes; each malformed case (missing interface,
  empty metrics, unknown metric key, bad state condition, bad errors op/value,
  empty hook command) yields a watch-scoped issue.

## Out of scope (structure ready, not built)

- `files` (file-count) check/metric.
- Speed threshold (only change detection now).
- Interface globbing / "all interfaces".
- `dropped`/other counters beyond what `counters:` lists (the list already allows
  any `/sys/.../statistics/<name>`, but defaults to rx/tx errors).
- Absolute (`total`) error threshold (delta only).

## Documentation

- `docs/configuration.md`: extend "Host watches" with the `net` check, its
  metrics, per-metric hooks, and the generalised `SERMO_*` env rule.
- `examples/sermo.yml`: a commented, disabled-by-default `net-eth0` example.
- `README.md`: extend the host-watches line to mention network interfaces.
