# CLI

`sermoctl` is the operator and scripting interface. Run it with no arguments
for the authoritative usage; `sermoctl <command> --help`-style hints are shown
on usage errors.

## Root flags

```text
--config /etc/sermo/sermo.yml
--backend auto|systemd|openrc
--json
--quiet
--timeout duration
```

## Command surface

```bash
sermoctl backend
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl start SERVICE
sermoctl stop SERVICE
sermoctl restart SERVICE

sermoctl preflight SERVICE
sermoctl processes SERVICE
sermoctl locks SERVICE

sermoctl config validate [SERVICE]
sermoctl config render SERVICE
sermoctl config diff BASE SERVICE

sermoctl daemon list
sermoctl daemon show DAEMON

sermoctl service list
sermoctl service show SERVICE
sermoctl service clone SOURCE TARGET

sermoctl events [SERVICE] [--limit N]   # list recent events (global or for SERVICE)
sermoctl events clear [--before TIME]   # omit TIME to clear all; TIME may be RFC3339 or duration (e.g. 1h)
                                        # only events strictly before the timestamp are removed

sermoctl lock SERVICE --reason REASON --ttl DURATION -- COMMAND...
sermoctl lock acquire SERVICE --reason REASON --ttl DURATION
sermoctl lock release SERVICE
```

Plus `apps`, `libs`, `patterns`, `services`, `monitor`/`unmonitor`, `sla`,
`diagnose`, `reload` and `wizard` — see the usage output and
[configuration](configuration.md).

## Exit codes

```text
0   success / active / allowed
1   expected false condition, such as inactive or a failed check
2   internal or runtime error / backend not detected
64  usage error (bad flags or arguments)
75  temporarily blocked action, such as an active backup lock or guard
78  configuration invalid (syntax, schema or `config validate` failure)
```

The `2` vs `78` distinction: use `78` whenever the problem is in the config
files the operator can fix (YAML syntax, missing kind/name, unknown variable,
unresolved uses/clone, failed `config validate`). `2` is everything else that
is not a clean false (`1`), a usage error (`64`) or a temporary block (`75`):
I/O errors, backend not detected, an exec that could not be launched, an
unexpected panic recovered at the top level.

`is-active` maps directly: `0` active, `1` not active, `2` error.
