# ICMP host watch: `icmp` check with per-metric hooks - historical design

**Date:** 2026-06-06
**Status:** Historical design. ICMP host watches are implemented; use
[`docs/configuration.md`](../../configuration.md#icmp-watches)
and [`docs/rules.md`](../../rules.md) for current operator-facing behavior.
**Builds on:** `2026-06-06-host-watches-disk-design.md` (watch/hook framework) and
`2026-06-06-net-interface-watch-design.md` (per-metric hooks + grouped config).

## Problem

The host-watch framework monitors local resources (`disk`, `net`). We now want
to monitor **external hosts via ICMP** (ping): whether a host is reachable
(up/down) and its round-trip latency, with a **different hook per metric** — the
same grouped-per-interface model already used by `net`, now per host.

## Decisions (from brainstorming, all confirmed)

1. **ICMP method:** native, using `golang.org/x/net/icmp` + `ipv4` (a **new
   dependency**). Requires `sermod` to have `CAP_NET_RAW` (or the
   `net.ipv4.ping_group_range` sysctl); documented. The probe is behind an
   **injectable sampler** so tests need no privileges/network.
2. **Metrics:** `state` (reachable→up / unreachable→down) and `latency` (RTT ms).
3. **state metric:** `on: change` (up↔down transition) or `expect: up|down`
   (same semantics as `net` state).
4. **latency metric:** `threshold: {op, value}` (ms) **or** `change: {delta}`
   (ms). Because latency is continuous, "change" requires a `delta` magnitude:
   fire when `|rtt − rtt_prev| > delta`. Latency only evaluates when the host is
   reachable (RTT known); when unreachable it does not fire (reachability is the
   `state` metric's job).
5. **IPv4 only** this iteration (IPv6 out of scope).
6. **count** default 3; the probe timeout is the check's timeout
   (`default_timeout` unless overridden).
7. **Builder DRY:** generalise the existing `buildNetWatches` into a generic
   `buildMetricWatches` shared by `net` and `icmp` (same expansion shape).
8. **New dependency:** `golang.org/x/net`.

## Configuration

```yaml
watches:
  ping-gw:
    enabled: true                 # optional, default true
    interval: 30s                 # optional, default engine.interval; applies to all metrics
    check: { type: icmp, host: 8.8.8.8, count: 3 }   # count optional, default 3
    metrics:
      state:
        on: change                # OR: expect: up | down
        for: { cycles: 1 }        # optional per-metric window
        then:
          hook: { command: [/usr/local/bin/host-state.sh, "8.8.8.8"] }
      latency:
        threshold: { op: ">", value: 100 }   # ms — OR change: { delta: 50 }
        then:
          hook: { command: [/usr/local/bin/host-latency.sh, "8.8.8.8"] }
```

- The **hook is per metric** (inside each `metrics.<name>.then.hook`).
- An `icmp` watch expands into one internal `*Watch` per metric; all units share
  the base watch name (`ping-gw`); `SERMO_METRIC` and `SERMO_HOST` distinguish
  them.
- `metrics` keys are restricted to `state`, `latency`.

## Components

### 1. `icmp` check — `internal/checks/icmp.go`

A new check type in the shared `buildCheck` switch. Like `net`, it is **stateful
across cycles** (remembers the previous sample for `on: change` / `change`),
implemented as a pointer type `*icmpCheck`; safe because each watch ticks
sequentially on its own goroutine.

- **Params:** `host` (required); `count` (default 3 — applied in `buildCheck`,
  like net's `counters` default, so inline use gets it too); `metric`
  (state|latency); the per-metric condition fields.
- **Timeout:** the check reads `base.timeout` (= `default_timeout`, since the
  per-metric inline entry carries no `timeout` key) and passes it to the sampler
  as the probe deadline. A per-watch timeout override is not plumbed (consistent
  with net) — decision 6.
- **Sampling:** injectable `Deps.PingSampler func(host string, count int, timeout
  time.Duration) (PingSample, error)`. `PingSample{Reachable bool, RTTms float64,
  RTTKnown bool}`. The default implementation uses `golang.org/x/net/icmp` +
  `ipv4`: resolve the host to an IPv4 address, open `icmp.ListenPacket("ip4:icmp",
  "0.0.0.0")`, send `count` echo requests (with a per-probe deadline derived from
  `timeout`), collect RTTs; `Reachable` = at least one reply; `RTTms` = mean of
  successful RTTs; `RTTKnown` = at least one reply.
- **Behavior per metric** (`Result.OK == true` means "fire"):
  - `state` + `expect: up|down` → OK when current reachability state equals
    `expect`. `Data`: `value` = state ("up"/"down"); no `old`/`new`.
  - `state` + `on: change` → OK on an up↔down transition vs the previous cycle.
    First cycle: record, OK=false. `Data`: `old`, `new`, `value=new`.
  - `latency` + `threshold: {op, value}` → OK when the host is reachable and
    `compareFloat(rtt, op, value)`. Unreachable (RTT unknown) → no fire.
    `Data`: `value` = rtt.
  - `latency` + `change: {delta}` → OK when reachable and `|rtt − rtt_prev| >
    delta`. First reachable cycle primes, OK=false. Unreachable cycles do not
    fire and do not corrupt the baseline. `Data`: `old`, `new`, `value=new`.
- **Data (always):** `host`, `metric`, plus the metric-specific keys above.
- **Growth point:** packet-loss metric and IPv6 are future additions following
  the same shape.

### 2. Builder generalisation — `internal/app/watch_build.go`

Rename/generalise `buildNetWatches` to `buildMetricWatches(name, entry,
checkEntry, deps, interval)` used by **both** `net` and `icmp`:

- For each metric key, build the check entry as a copy of the watch's
  `checkEntry` base fields (`type`, `host`/`interface`, `count`, …) plus
  `metric: <key>` plus every key from the metric block **except** `then`, `for`,
  `within` (those are watch-unit concerns). This is equivalent to the prior
  net-specific explicit copy of `on/expect/counters/delta`, but type-agnostic, so
  it serves icmp's `threshold/change` fields too. **Builder-set keys take
  precedence:** copy the metric-block keys first, then set `type`/`metric` (and
  the base check fields from `checkEntry`) so a stray `metric`/`type` inside a
  metric block cannot override the builder's values.
- Build the check via `checks.BuildInline`, the per-metric hook via `parseHook`
  (reads `then.hook`), the window from the metric's `for`/`within`; one `*Watch`
  per metric with `Name` = base watch name, `CheckType` = the check type.

`BuildWatches` dispatch: `case "net", "icmp": buildMetricWatches(...)`; default
→ `buildSingleWatch` (disk). Net behaviour is unchanged (covered by its tests).

### 3. Validation — `internal/config/validate.go`

Add `case "icmp"` to `validateWatches` calling `validateICMPCheck`:

- `host` required (non-empty string); `count`, if present, a positive integer.
- non-empty `metrics` map; keys ∈ {`state`, `latency`}.
- `state`: validated by a shared `validateStateMetric(prefix, m, add)` extracted
  from the current net state logic (`expect: up|down` or `on: change`).
- `latency`: exactly one of `threshold` or `change` is required; `threshold` is a
  `{op, value}` mapping (valid op, numeric value); `change` is a `{delta}` mapping
  (numeric delta).
- per-metric hook via `validateHookBlock(prefix, m, add)` (only if a `then` is
  present on the metric) and window via `validateMetricWindow(prefix, m, add)`
  (both already shared).

(Note: design assumed every metric would declare a `then`. The final
implementation allows omitting `then` on a metric/watch for alert-only mode:
"firing" events are still emitted for the web UI and logs.)

The net `state` validation is refactored to call the shared
`validateStateMetric` (no behaviour change).

### 4. Dependency

Add `golang.org/x/net` (for `icmp` + `ipv4`) via `go get golang.org/x/net` +
`go mod tidy`. Only the default `PingSampler` imports it.

## Testing

- **`icmp` check** (injected `PingSampler`): state on-change up→down with
  `old`/`new`; expect-state fires only in that state; latency threshold fires
  above the limit and not when unreachable; latency change fires when the delta
  is exceeded and primes on the first reachable cycle; unreachable cycle does not
  fire latency nor corrupt the baseline; sampler error → no fire.
- **`buildMetricWatches`:** an `icmp` host with `state`+`latency` builds 2 watches
  with distinct hooks; the existing `net` expansion tests still pass against the
  generalised function.
- **Validation:** good icmp config passes; each malformed case (missing host, bad
  count, empty metrics, unknown metric key, bad state condition, latency with
  neither threshold nor change, bad threshold op/value, bad change delta, empty
  hook command) yields a watch-scoped issue.
- The default ICMP sampler is not unit-tested (needs privileges/network),
  consistent with the `statfs`/net default samplers.

## Out of scope (structure ready, not built)

- IPv6 ICMP.
- Packet-loss metric.
- Unprivileged datagram-ping fallback (`udp4`) — documented as an operator option
  but the default uses raw `ip4:icmp`.
- TCP/HTTP reachability (already covered by existing `tcp`/`http` checks for
  services; this watch is ICMP-specific).

## Documentation

- `docs/configuration.md`: extend "Host watches" with the `icmp` check, its
  metrics, per-metric hooks, env vars (`SERMO_HOST`, `SERMO_METRIC`,
  `SERMO_VALUE`, `SERMO_OLD`/`SERMO_NEW`), and the `CAP_NET_RAW` requirement.
- `configs/sermo.yml`: a commented, disabled-by-default `ping-gw` example.
- `README.md`: extend the host-watches line to mention external hosts (ICMP).
