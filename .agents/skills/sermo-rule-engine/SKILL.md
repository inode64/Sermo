---
name: sermo-rule-engine
description: Use when designing or implementing Sermo rules, condition trees, and/or/not logic, for cycles, within cycles, remediation actions, alerts, or guard rules.
---

Rule-engine design for Sermo. The public YAML surface is in `docs/rules.md`;
the types are `RuleType`, `ActionType` and `Action` in
`internal/rules/model.go`; policy state lives in `internal/rules/state.go`.

## Model

- `rules` is a map keyed by rule name so a service can override or disable one
  inherited rule. Types: `remediation`, `guard`, `alert`.
- `then` is one action (`then: { action: restart }`) or
  `then: { actions: [...] }`. `block` and `alert` require a `message`; only
  guards use `block`, and a guard must list `blocks`.
- Condition leaves: `and`, `or`, `not`, `failed`, `active`, `metric`,
  `service`, `process`, `file`, `command`, `changed`.
- `for: { cycles }` counts consecutive matches; `within: { cycles,
  min_matches }` is a rolling window. A rule declares at most one of them.
  `mode` belongs only to `defaults.rule_window`.

## Invariants

- Conditions are read-only predicates. Every distinct probe (declared check or
  inline condition) runs at most once per cycle and its result is cached;
  inline `command` conditions are side-effect-free argv arrays with a timeout.
- A remediation rule triggers only on `scope: service` metrics. A
  `scope: system` metric may drive `alert` only.
- Guards run before remediation; a blocked action logs the guard's block
  message and records one event.
- Cooldown, `max_actions`, `max_actions_window` and backoff are the per-service
  `policy`, not per-rule. The daemon consults the policy before the operation
  engine; a rule may keep firing while the policy suppresses execution.
- A resolved policy needs a positive `cooldown`; a missing or zero value is a
  validation error. Manual actions skip cooldown only.
- An unavailable or erroring guard leaf fails closed.

## Evaluation order

```text
1. run declared checks and inline probes once; cache per cycle
2. evaluate guard rules
3. evaluate remediation and alert rules
4. for a requested action, evaluate the guards that block it
5. apply the service policy (cooldown, max_actions); log and skip if suppressed
6. execute through internal/operation
7. update window and policy state; record the event
```

Two kinds of state exist and must not be conflated: per-rule window history
(consecutive count or rolling matches) and per-service policy state
(`LastActionAt`, `RecentActions`, `CurrentBackoff`).

## Tests a rule change needs

```text
and/or/not truth tables and nesting
failed, active, metric comparisons and the % suffix
for and within windows; both declared is rejected
guard evaluated before remediation; guard leaf error fails closed
system-scope metric rejected in remediation
shared probe runs once per cycle
cooldown suppresses; zero or missing cooldown invalid; max_actions within window
manual action exempt from cooldown but not from guards, locks or preflight
```
