# CLI

`sermoctl` is the operator and scripting interface. Run it with no arguments
or `--help` for the command index, and use `sermoctl help COMMAND` or
`sermoctl COMMAND --help` for focused usage, flags and examples.

## Root flags

```text
--config /etc/sermo/sermo.yml
--backend auto|systemd|openrc
--json
--quiet
--timeout duration
--version / -V
```

Global flags may be placed before or after the command. Command-specific flags
are shown by `sermoctl help COMMAND`.

## Command surface

```bash
sermoctl help [COMMAND]
sermoctl backend
sermoctl init                       # alias for sermoctl backend
sermoctl version
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl start SERVICE [--no-cascade]
sermoctl stop SERVICE [--no-cascade]
sermoctl restart SERVICE [--no-cascade]
sermoctl resume SERVICE [--no-cascade]
sermoctl reload SERVICE

sermoctl mount TARGET                 # TARGET is a configured mount name or absolute path
sermoctl umount TARGET
sermoctl mount status TARGET
sermoctl mount list

sermoctl preflight SERVICE
sermoctl processes SERVICE
sermoctl locks SERVICE
sermoctl monitor SERVICE
sermoctl unmonitor SERVICE

sermoctl config validate [SERVICE]

sermoctl daemon list
sermoctl daemon show DAEMON
sermoctl daemon reload                 # reload sermod config, not services

sermoctl service list
sermoctl service show SERVICE
sermoctl service clone SOURCE TARGET

sermoctl services [all] [--long]
sermoctl apps [all] [--long]
sermoctl libs [all] [--long]
sermoctl patterns

sermoctl events [SERVICE] [--limit N]   # list recent events (global or for SERVICE)
sermoctl events clear [--before TIME]   # omit TIME to clear all; TIME may be RFC3339 or duration (e.g. 1h)
                                        # only events strictly before the timestamp are removed
sermoctl activity clear [--before TIME] # clears the same log shown as Recent activity

sermoctl diagnose
sermoctl diagnose clean                 # clears stale control state for removed services/watches
sermoctl diagnose clear                 # alias for diagnose clean
sermoctl state compact [--before TIME]  # prunes old history and vacuums the state database
                                        # omit TIME for normal 366-day retention; TIME may be RFC3339 or duration

sermoctl lock SERVICE [--name NAME] --reason REASON --ttl DURATION -- COMMAND...
sermoctl lock acquire SERVICE [--name NAME] --reason REASON --ttl DURATION
sermoctl lock release SERVICE [--name NAME]

sermoctl wizard
sermoctl wizard service|docker|vm|mount|volume|net|uplink
```

Examples:

```bash
sermoctl help restart
sermoctl restart mysql-main
sermoctl daemon reload
sermoctl state compact --before 720h
```

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

`is-active` maps directly: `0` active, `1` not active (including `paused`),
`2` error.

## Mounts

Mount actions are fstab-backed and use `kind: mount` files from
`/etc/sermo/mounts` by default. A path target that is not configured is still
accepted, but it uses safe defaults and must exist in `/etc/fstab`. See
[mount units](configuration.md#mount-units).

`sermoctl wizard mount` lists mount points declared in `/etc/fstab` and writes
safe `kind: mount` files under `paths.mounts`; it does not execute mount or
umount while generating the config.
