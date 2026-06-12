# Additional init units per service (`also_service`) — design

## Goal

Let a service act on **auxiliary init units of its own** (a `.socket`, `.timer`,
or companion unit) together with its primary unit, **inside the same operation**.
Unlike `also_apply` (which cascades to other *Sermo services* through their own
engines/guards), `also_service` units are plain init units driven directly by the
`servicemgr.Manager` as extra steps of *this* service's start/stop — so this
service's guards/locks/preflight already wrap them.

Motivating example: restarting `docker` should also restart `docker.socket`.

Confirmed decisions: wrap (socket-activation) ordering; strict on start,
best-effort on stop.

## YAML (mirrors `service:`)

```yaml
service:
  systemd: [docker]
  openrc:  [docker]

also_service:
  systemd: [docker.socket]
  openrc:  [docker-helper]
```

Resolved for the **active backend** only (systemd *or* openrc), exactly like
`service:`. A new accessor `config.AdditionalUnits(tree, backend) []string` reads
`also_service.<backend>` via `cfgval.StringList`. Empty/absent → no-op.

## Ordering (wrap / socket-activation)

The auxiliary units are the **outer layer** — up before the primary, down after:

- **start**: start `also_service` (in list order), **then** the primary unit.
- **stop**: stop the primary unit, **then** `also_service` (in **reverse** list
  order, for proper LIFO nesting).
- **restart** = stop+start, so it composes to: stop primary → stop also; start
  also → start primary (correct socket-activation order).

## Failure handling

- **start is strict**: if any `also_service` unit fails to start, the operation
  **fails** (a down `docker.socket` makes `docker` useless) — and because also
  units start *before* the primary, a failure aborts **before** the primary is
  started, leaving a clean "didn't start" state. The result message names the
  failing unit.
- **stop is best-effort**: a unit that fails to stop is logged/reported and the
  operation continues (the primary is already down). Failures are appended to the
  result message.
- `reload` acts on the **primary only** (sockets/timers have no reload).

## Engine integration (small, localized)

The engine acts on a single `e.Unit` today: `Manager.Stop(ctx, e.Unit)` at the
stop step and `Manager.Start(ctx, e.Unit)` at the start step (restart is built
from stop+start inside the engine). Add `Engine.AlsoUnits []string` and weave it
into those two steps:

- **stop step** (after `Manager.Stop(e.Unit)`): for each also unit in reverse
  order, `Manager.Stop(unit)` best-effort, collecting any error into the message.
- **start step** (BEFORE `Manager.Start(e.Unit)`): for each also unit in order,
  `Manager.Start(unit)`; on the first error, return a failed Result **without**
  starting the primary.

Residual-process handling, health checks and status remain primary-only; also
units get just the Manager action. `ResetState` stays primary-only.

## Touch points

- `internal/config/model.go`: `AdditionalUnits(tree, backend) []string` (sibling
  of `ServiceCandidates`).
- `internal/config/validate*.go`: validate `also_service` is a mapping of
  `systemd`/`openrc` → string lists (reuse the `service:` shape validation; an
  unknown key or non-list is an error). `also_service` survives `expandTree`
  untouched (generic copy) — confirm it is not stripped.
- `internal/operation/engine.go`: `AlsoUnits` field + the two step edits above.
- `internal/operation/build.go`: in `New`, resolve `also_service` for the active
  backend (the same `backend` already used to resolve the primary `Unit`) and set
  `Engine.AlsoUnits`. Because both the daemon and the CLI build engines through
  `build.New`, both surfaces get it for free.
- Docs: `docs/daemons.md` — the field, the wrap ordering, and the strict-start /
  best-effort-stop semantics; note it is per-active-backend.

## Edge cases

- **Backend with no list** (e.g. only `systemd:` set, host is openrc): the
  accessor returns empty → no-op, no error.
- **Also unit == primary unit**: validation rejects listing the primary in
  `also_service`.
- **Unknown/again-missing unit at runtime**: Manager surfaces the backend error;
  on start that fails the op (strict), on stop it is reported (best-effort) —
  consistent with how a missing primary unit already behaves.
- **`reload`**: also units untouched.

## Testing

- Engine: with a fake Manager recording calls, assert order — start = [also...,
  primary]; stop = [primary, also-reversed]; restart composes both. Strict start
  (a failing also unit aborts before the primary starts, op fails); best-effort
  stop (a failing also unit does not fail the op, primary already stopped).
- Config: `AdditionalUnits` per backend; validation (non-list, unknown key,
  self-reference).
- A catalog `docker.yml` example wiring `also_service: { systemd: [docker.socket] }`
  resolves and validates.

## Relationship to `also_apply`

Complementary, not overlapping: `also_service` = extra **init units of this
service** (one operation, this service's guards); `also_apply` = other **Sermo
services** (each its own guarded operation, cross-service orchestration). A
service may use both.

## Out of scope (v1)

- Per-also-unit ordering overrides or explicit before/after graphs.
- Acting on also units for `reload`.
- Health/residual handling for also units (they are auxiliary; the primary owns
  health).
