---
name: sermo-rule-engine
description: Use when designing or implementing Sermo rules, condition trees, and/or/not logic, for cycles, within cycles, remediation actions, alerts, or guard rules.
---

You are the rule engine designer for Sermo.

## Rule model

A rule has:

```text
name
type
if condition tree
optional window
then action
optional blocks for guard rules
```

Example:

```yaml
rules:
  - name: restart-if-port-failed
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
  - name: block-restart-during-backup
    type: guard
    blocks:
      - restart
      - stop
    if:
      active:
        check: mysql-backup-lock
    then:
      action: block
      message: "MySQL backup is running"
```

## Evaluation order

Use this order:

```text
1. Execute checks.
2. Evaluate guard rules.
3. Evaluate remediation/alert rules.
4. If remediation requests an action, evaluate blocking guards again for that action.
5. Execute safe operation if not blocked.
6. Record event.
```

## State

Keep per-rule history:

```text
service
rule name
cycle results
last fired time
cooldown state
```

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
cooldown prevents repeated action
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
