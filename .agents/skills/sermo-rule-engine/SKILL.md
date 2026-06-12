---
name: sermo-rule-engine
description: Use when designing or implementing Sermo rules, condition trees, and/or/not logic, for cycles, within cycles, remediation actions, alerts, or guard rules.
---

You are the rule engine designer for Sermo.

## Rule model

`rules` is a map keyed by rule name (like `checks`/`preflight`/`processes`), not
a list. The key is the rule name; there is no inner `name` field. This lets a
service override or disable a single inherited rule. An entry has:

```text
type           remediation | guard | alert        (RuleType)
if             condition tree
for / within   optional window
then           a single Action { action, message, ... }   (ActionType)
blocks         list of actions a guard blocks (guard rules only)
```

Example:

```yaml
rules:
  restart-if-port-failed:
    type: remediation
    if:
      failed:
        check: tcp-783
    for:
      cycles: 3
      mode: consecutive
    then:
      action: restart
```

`RuleType`/`ActionType` constants and the `Action` struct are defined in
`AGENTS.md` spec section 16. `block` and `alert` actions require a
`message`; only guard rules use `action: block`, and a guard must list `blocks`.

## Condition tree

Support:

```text
and
or
not
failed
active
metric
service
process
file
command
```

Example:

```yaml
if:
  and:
    - failed:
        check: http
    - not:
        active:
          check: backup-running
```

`metric` leaves carry a `scope` (`service` default, or `system`). Remediation
rules may only trigger on `scope: service` metrics; a `scope: system` metric may
drive `alert` only — never restart/start/stop a single service.

Conditions are read-only predicates. The evaluator runs every distinct probe (a
declared check or an inline condition) at most once per cycle and caches the
result, so a probe shared by several rules never executes twice in a cycle, and a
condition must never change system state. Inline `command` conditions must be
side-effect-free, array form, with a timeout. See `AGENTS.md`
section 14.

## Windows

`for` means consecutive matches:

```yaml
for:
  cycles: 3
  mode: consecutive
```

`within` means rolling window:

```yaml
within:
  cycles: 15
  min_matches: 3
```

Do not allow ambiguous windows. If MVP does not support using `for` and `within` together, validation must reject it.

## Rule types

Support these concepts:

```text
remediation: may start/stop/restart
guard: blocks actions
alert: records or notifies
```

Guards must run before remediation.

Example guard:

```yaml
rules:
  block-restart-during-backup:
    type: guard
    blocks:
      - restart
      - stop
    if:
      active:
        check: mariabackup
    then:
      action: block
      message: "${display_name} backup is running"
```

## Evaluation order

Use this order:

```text
1. Run all declared checks and any inline rule probes once; cache the results for
   this cycle (each distinct probe runs at most once).
2. Evaluate guard rules.
3. Evaluate remediation/alert rules.
4. If remediation requests an action, evaluate blocking guards for that action.
5. If not blocked, consult the service remediation policy (cooldown, max_actions);
   if suppressed, log and skip the action.
6. Execute the safe operation through the shared engine if allowed.
7. Update remediation state and record the event.
```

Step 5 applies to automatic remediation only. Manual `sermoctl` actions are
exempt from cooldown but still pass guards, locks and preflight.

## State

There are two distinct kinds of state; do not conflate them.

Per-rule window state (for evaluating `for`/`within`):

```text
service
rule name
cycle results history (consecutive count, or rolling window of matches)
```

Per-service remediation policy state (for cooldown/rate-limit), in
`internal/rules/state.go`:

```text
LastActionAt    time of the last executed remediation action
RecentActions   timestamps still inside max_actions_window
CurrentBackoff  current backoff duration (0 when disabled)
```

Cooldown and rate limiting are a per-service `policy` block (mandatory positive
cooldown, optional max_actions/max_actions_window/backoff), NOT per-rule. The
daemon checks this resolved policy before invoking the operation engine
(evaluation order step 5). A rule may keep firing every cycle while the cooldown
suppresses repeated execution. See `AGENTS.md` spec section 16.

## Testing

Add tests for:

```text
and true/false
or true/false
not true/false
nested conditions
failed check
active check
metric comparisons
for cycles
within cycles
guard before remediation
metric scope (service vs system); system metric rejected in remediation
a probe shared by several rules runs at most once per cycle
cooldown suppresses repeated remediation; zero/missing resolved cooldown is invalid
max_actions rate limits within window
manual action is exempt from cooldown but still passes guards/locks/preflight
invalid rule rejected
```

## Output format

When designing or reviewing rules, return:

```text
- YAML shape
- Evaluation semantics
- State required
- Edge cases
- Tests
```
