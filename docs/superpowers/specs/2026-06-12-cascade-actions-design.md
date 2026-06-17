# Cascade actions to additional services (`also_apply`) - historical design

Status: historical design. `also_apply` is implemented; use
[`docs/daemons.md`](../../daemons.md#also_apply--cascade-to-other-services) and
[`docs/safety.md`](../../safety.md) for current operation and safety behavior.

## Goal

Let a service declare additional services that are started/stopped/restarted
**together with it**. When the service performs an action — rule-driven
remediation or a manual `sermoctl start|stop|restart` — the same action runs on
each declared service through its **own operation** (so the dependent's guards,
preflight and locks are honored), in a **dependency-aware order**, sequentially,
best-effort, with loop protection.

Confirmed decisions: mirror-list YAML; full-operation mechanism (Level B);
dependency-aware ordering; triggers on rules **and** manual CLI. Scope: `start`,
`stop`, `restart`. `reload` exists as a safe primary-service operation, but this
cascade design intentionally keeps it primary-only.

## YAML

```yaml
# in a service/daemon body
also_apply: [nginx, varnish]
```

On this service's `start`/`stop`/`restart`, the **same** action runs on each
listed service. Entries name **enabled services** in the same config (validated).

## Mechanism (Level B — run the target's own engine)

The operation Engine itself enforces **operation/runtime locks**
(`internal/operation/engine.go`) and **guards** (built per-service by
`guardClosure`, `internal/operation/build.go`) and runs preflight/postflight. So
"do action A on target T" = **run T's engine for A** → T's guards/locks/preflight
are honored for free, because the engine is built from T's own resolved tree.

**What cascade does NOT inherit (documented limitation):** the target's
remediation **cooldown/policy** and its **paused/unmonitor** state live in T's
worker rule-loop (`internal/app/worker.go`), not in the engine. A cascade is an
explicit operational relationship, so it operates T **regardless** of T's own
cooldown or paused-monitoring state (pausing monitoring silences T's *own* rules;
it does not sever an explicit cascade). This is intentional and documented; an
operator who wants to decouple removes the `also_apply` entry.

## Ordering — the cascade is an ORCHESTRATOR, not a trailing hook

**Critical:** the cascade must run as a wrapper that *controls the order of the
whole group*, because for `stop` the additionals must go down **before** the
primary — which is impossible if cascade fires after the primary already acted.
So the cascade owns the primary action's placement:

- `start` / `restart`: **primary first, then additionals** (a dependent comes up
  after the thing it depends on). Pre-order over the `also_apply` graph.
- `stop`: **additionals first, then primary** (dependents go down before it).
  Post-order over the graph.

The group is operated **strictly sequentially** (see Concurrency). The primary's
own Result is what drives the worker's remediation bookkeeping
(`RecordsRemediation`, change-acknowledgement); additionals are side effects.

## Concurrency & deadlock safety (hard constraints)

Every `Operate` acquires a slot from a small **global operation pool**
(`max_parallel_operations`, default 2; `OpGate.acquire` **blocks**). Therefore:

- The cascade orchestrator runs **outside** any held slot (the gate wraps only
  `Worker.Operate` via `gateOperate`, not `runRemediation`), and operates targets
  **one at a time** (never fans out while holding a slot) — concurrent fan-out
  across a 2-slot pool self-deadlocks.
- Workers run concurrently, and a target's per-service operation lock is
  **fail-fast**: if target T is mid-cycle when the cascade reaches it, T's engine
  returns blocked/lock-failed. Cascade treats a lock-contended target as a
  **best-effort miss** (reported, not fatal); it allows **one** retry after a
  small fixed backoff (e.g. 1s) that simply re-calls `op(ctx, svc, action)` for
  that target (no re-ordering).

## Architecture

A driver-agnostic orchestrator owns the ordering + visited/depth + best-effort
reporting, parameterized by how to operate one service:

```
type operateFn func(ctx, service, action string) operation.Result

cascade(ctx, root, action, targets, visited, op operateFn, lookup func(svc) []string, emit):
    seq = orderedGroup(root, action, targets, visited, lookup)   // pre-order for start/restart, post-order for stop
    for svc in seq:                                              // sequential
        res = op(ctx, svc, action)
        emit(cascade event: root, svc, action, res.Status)
    return primary's Result   // the root's own op result
```

`orderedGroup` walks the `also_apply` graph DFS with a `visited` set (cycle cut +
`cascade-skip` log) and a **recursion-depth** cap (16, a pure backstop for
acyclic-but-deep fan-out — it counts recursion depth, not total nodes, so a wide
shallow graph is not truncated); it places the root first (start/restart) or last
(stop), recursing so a target's own `also_apply` is honored at the right depth.

Two drivers supply `op`:

1. **Daemon (rules):** `Monitor` gains a `map[string]*Worker` index and
   `OperateService(ctx, name, action) Result` → the target worker's `Operate`
   (its engine). The source `Worker` gets a `Cascade func(ctx, action) Result`
   (nil when no `also_apply`); `runRemediation` calls `w.Cascade` **instead of**
   `w.Operate` when set, after policy/guards pass — so the orchestrator owns the
   group including the primary. Wired in `daemon.go` capturing the monitor.
2. **Manual CLI:** driven from `runAction` (NOT from the injectable `App.Operate`
   seam). After resolving the primary, if it has `also_apply` and not
   `--no-cascade`, build the ordered group and operate each via a shared
   engine-build helper extracted from `defaultOperate`, reusing **one** `OpGate`
   across the whole group. The per-target engine is built from
   `cfg.Resolve(target)` like the primary.

## Touch points

- `internal/config`: a `CascadeTargets(tree)` accessor (`cfgval.StringList(tree["also_apply"])`).
  `also_apply` is a plain key that survives `expandTree` untouched and is not
  stripped. Validation: entries non-empty, **exist in `Config.ServiceNames`**, and
  no self-reference. This requires threading the service-name set into
  `validateResolved` (parallel to how `notifiers` is threaded today) — a small
  signature change to call out.
- `internal/app/cascade.go` (new): the orchestrator (`orderedGroup` + sequential
  driver + events), driver-agnostic via `operateFn`.
- `internal/app/monitor.go`: worker index + `OperateService`.
- `internal/app/worker.go`: `Cascade` field; call it in `runRemediation` in place
  of `w.Operate` when set (only on the success path semantics preserved — the
  orchestrator returns the primary Result).
- `internal/app/daemon.go`: wire each worker's `Cascade` to the monitor + the
  service's targets.
- `internal/app/event.go`: route `"cascade"` to the `SlogEmitter` **Info** case
  and `"cascade-skip"` to the **Warn** case so the events surface (Kind is
  free-form). **Required pre-merge check:** verify `internal/web/index.html`'s
  event filter/rendering does not drop an unknown `"cascade"` Kind (apply the
  `AGENTS.md` web-cohesion rules if a new event style is needed) — this is the
  one claim the design has not yet confirmed against the code.
- `internal/cli/cli.go`: `--no-cascade` flag (manual flag loop), and the
  `runAction` cascade driver + extracted engine-build helper sharing one `OpGate`.
- `internal/web`: render `cascade` events on the service detail.
- Docs: `docs/configuration.md` / `docs/daemons.md` — the field, dependency
  ordering, and the guard-honored / cooldown-and-pause-bypassed semantics.

## Edge cases

- **Unknown target / self-reference**: validation error at load (fail fast).
- **Cycle A↔B**: terminated by `visited`; logged `cascade-skip`, not an error.
- **Lock-contended target** (mid-cycle): best-effort miss, one bounded retry.
- **`start` of an already-running target**: engine start is idempotent (OK).
- **Failing/blocked target**: reported as its own `cascade` event; does **not**
  fail the primary action (additionals are side effects).
- **Manual single-service intent**: `sermoctl … --no-cascade` operates exactly one
  service.

## Testing

- `orderedGroup`: pre-order for start/restart, **post-order for stop**, root
  placement, visited cuts a cycle, depth cap.
- orchestrator: sequential, best-effort (a failing target does not fail the run),
  `cascade` events emitted.
- Config: parse/validate (unknown target, self-reference, non-list).
- Worker: a firing remediation routes through `Cascade` when set; primary Result
  still drives bookkeeping; no `also_apply` → bare `Operate`.
- CLI: manual restart cascades in dependency order sharing one OpGate;
  `--no-cascade` suppresses it.

## Out of scope (v1)

- `reload` cascade (`reload` remains primary-only).
- Cross-host cascade (services on this node only).
- Per-action target maps (`also: {restart: […], stop: […]}`) — sugar over the
  same orchestrator, addable later.
- Waiting for the primary to be *healthy* before cascading start/restart
  (fire-forward in v1; a `wait_healthy` option can come later).
