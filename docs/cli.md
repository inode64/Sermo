# CLI

`sermoctl` is the operator and scripting interface. Run it with no arguments
or `--help` for the command index, and use `sermoctl help COMMAND` or
`sermoctl COMMAND --help` for focused usage, flags and examples.

## Root flags

```text
--config /etc/sermo/sermo.yml
--backend auto|systemd|openrc
--json
--quiet / -q
--timeout duration
--version / -V
--help / -h
```

Global flags may be placed before or after the command. Command-specific flags
are shown by `sermoctl help COMMAND`.

## sermod daemon flags

`sermod` is the long-running monitoring daemon. Packaged units normally start it
with the standard config path:

```bash
sermod run --config /etc/sermo/sermo.yml
```

Manual runs support these flags:

```text
sermod run [--config PATH] [--verbose|-v]
sermod version
sermod --version
```

- `--config PATH` loads the global config file. The default is
  `/etc/sermo/sermo.yml`. Use the same path with `sermoctl --config` when
  validating or reloading a non-standard tree.
- `--verbose` / `-v` enables debug logging, including config load details,
  backend detection and monitor-target counts.

Use `sermoctl daemon reload` to ask a running daemon to re-read the config file
it was started with.

## Command surface

```bash
sermoctl help [COMMAND]
sermoctl backend
sermoctl version
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl watch status WATCH
sermoctl watch monitor WATCH
sermoctl watch unmonitor WATCH
sermoctl watch probe WATCH
sermoctl watch pause RAID_WATCH --confirm MD_ARRAY
sermoctl watch resume RAID_WATCH
sermoctl start SERVICE [--no-cascade]
sermoctl stop SERVICE [--no-cascade]
sermoctl restart SERVICE [--no-cascade]
sermoctl resume SERVICE
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

sermoctl panic on|off|status          # daemon-wide emergency switch (see Panic mode)

sermoctl config validate

sermoctl daemon reload                 # reload sermod config, not services
sermoctl notifier test NAME            # send an explicit test message through one notifier

sermoctl services [all] [--long] [--notify NAME[,NAME]|all]   # catalog inventory, not runtime config
sermoctl apps [all] [--long]                                  # catalog apps (see Catalog inventory)
sermoctl libs [all] [--long]
sermoctl patterns

sermoctl sla [SERVICE]                  # service availability windows (all services, or one)
sermoctl sla --series SERVICE [--since DURATION]  # per-minute series; --since default 24h
sermoctl sla --process-uptime [SERVICE] # separately confirmed process-continuity coverage

sermoctl events [SERVICE] [--limit N]   # list recent events (global or for SERVICE)
sermoctl events clear [--before TIME]   # omit TIME to clear all; TIME may be non-future RFC3339 or positive duration
                                        # only events strictly before the timestamp are removed
sermoctl activity clear [--before TIME] # clears the same log shown in Events

sermoctl state compact [--before TIME]  # prunes old history and vacuums the state database
                                        # omit TIME for normal 366-day retention; TIME may be non-future RFC3339 or positive duration

sermoctl lock SERVICE [--name NAME] --reason REASON --ttl DURATION -- COMMAND...
sermoctl lock acquire SERVICE [--name NAME] --reason REASON --ttl DURATION
sermoctl lock release SERVICE [--name NAME]

sermoctl wizard
sermoctl wizard service|docker|vm|mount|volume|net|uplink
```

## Availability and process continuity

`sermoctl sla` is observed check availability: it only counts monitored daemon
cycles. `sermoctl sla --process-uptime` instead reports how much of each rolling
window Sermo can confirm a trusted service process was alive. It can include
time before a `sermod` restart when the process's start time proves that the
same process predates the new daemon.

This is evidence of process continuity, **not** a synthetic SLA sample or a
health result: it cannot make HTTP, TCP, command or any other check pass, and
it does not hide a recorded check failure. A window with no confirmed process
interval is `n/a`, not downtime.

Examples:

```bash
sermoctl help restart
sermoctl restart mysql-main
sermoctl services --notify ops-email
sermoctl notifier test ops-email
sermoctl daemon reload
sermoctl state compact --before 720h
```

## Panic mode

Panic mode is a daemon-wide emergency switch for maintenance windows, attacks,
denial-of-service, system malfunction or overload. While it is on, the daemon
keeps running its checks (so status stays visible) but **suspends all hooks,
alert notifications and automatic remediation**. Manual operations (`start`,
`stop`, `restart`, `reload`, `resume`) stay available, so you can drive services
by hand without the daemon fighting you.

```bash
sermoctl panic on        # suspend hooks, alerts and automatic remediation
sermoctl panic status    # show the current state (default when no argument)
sermoctl panic off       # resume normal operation
```

The flag is persisted in the state database (`paths.state`), so it survives
daemon restarts until you turn it off, and the CLI works without the web UI
enabled. The running daemon picks up a change within ~1 second. While active,
the daemon status reported by `/readyz` and the web header shows **`panic
mode`**. In the web UI the same toggle is the red **panic mode** button in the
footer (it asks for confirmation in both directions so it is not triggered by
accident). The CLI applies the change immediately without a prompt.

## Service target resolution

For a configured service, `sermoctl status`, `is-active` and service
operations resolve the same control target that `sermod` and the web UI use.
When `sermod` is running with `web` enabled, `sermoctl status` prefers the
daemon's computed state (including `starting` during startup settling); if the
web API is unreachable it falls back to the init backend plus local monitor
metadata, as before. Service states are: `disabled`, `stopped`, `started`
(backend active but not monitored), `starting` (startup/operation settling),
`collecting` (active and monitored, but graphs/indicators are not complete yet),
`monitored` (active, monitored and observability-ready) and `failed`. Without
the daemon view, a configured active monitored service falls back to
`collecting`; an active service that is not known to be monitored falls back to
`started`. **`sermoctl is-active` is different:** it always probes the
init backend (`active` / `inactive` / `paused`) for the exit code and plain-text
output. A monitored service still settling with an inactive backend therefore
shows `state=starting` in `status` but exits **1** from `is-active` until the
unit reports active. The same preference applies to `sermoctl watch status
WATCH` and to the STATUS column of `sermoctl apps` for installed applications
monitored by the daemon. Catalog apps whose binary is not installed are omitted
from `sermoctl apps` and do not participate in startup settling.

When the daemon has current watch readings, `sermoctl watch status WATCH` also
prints them (including RAID operation and rebuild percentage) and the separate
last-check timestamp; `--json` exposes the same readings in a `readings` array.

`sermoctl watch monitor|unmonitor WATCH` pauses or resumes a single watch,
persisted under `paths.state` and read live by the daemon. `WATCH` is a host
watch name or a service-embedded watch `"<service>:<watch>"`; a watch's monitor
state is independent of its service's, so `unmonitor` on a service never pauses
its watches.

`sermoctl watch probe WATCH` asks the running daemon to run one fresh sample for
a host `hdparm`, `lvm`, `raid` or `smart` watch and prints the resulting
readings when available (for LVM this includes health, VG, LV, VG free and
reasons). The first three are read-only samples. A `smart` probe starts the
device's short SMART self-test with `smartctl --test=short DEVICE`; success means
the device accepted the test, not that it has passed it. Normal scheduled SMART
checks remain read-only health/attribute reads. The command records a `probe`
event and last-check time, but does not run rules, notifications or remediation.
A RAID
watch with `raid_control.pause_resume: true` and an explicit `check.array` also
supports `watch pause` and `watch resume`.
Pausing requires `--confirm MD_ARRAY` in addition to naming the watch; both
actions re-check the array, use an exclusive runtime operation lock and verify
the resulting kernel state. Resume accepts any currently paused configured
array, including one paused outside Sermo.

The daemon records both `probe/running` when a manual sample starts and its
`probe/ok` or `probe/failed` completion event with the elapsed time. A SMART
self-test remains `testing` in `watch status` until the device reports it has
ended. RAID/LVM device work is also reported as `testing`, `recovering`,
`rebuilding`, `repairing`, `moving` or `merging`, including the reported
percentage where available; those states describe work, not health. Only one
manual sample for a watch may run at once; `sermoctl watch probe` waits for that
same daemon task and reports an already-running sample instead of starting a
second disk, LVM, RAID or SMART command.
Sermo reads the service's `service:` candidates, picks the first unit known by
the active backend, and normalizes systemd names with `.service` when needed.

If the backend probe cannot surface a configured init unit but the service still
has a usable configured seed, Sermo falls back to that unit and prints a warning,
matching the daemon/web behavior used for historic init-service setups. There is
no fallback for invalid `control:` targets or a per-backend `service:` map with
no candidate for the active backend; those are configuration errors.

## Catalog inventory

`sermoctl services`, `sermoctl apps`, `sermoctl libs` and `sermoctl patterns`
list **catalog definitions** shipped in the packaged catalog (see
[services.md](services.md)): which profiles are installed, the version their
version command reports, and whether they resolve. Add `all` to include entries
whose binary or library file is not present on the host.

This is **not** the list of **configured runtime targets** that `sermod`
monitors. Those are the service files under `paths.services` (and the
matching names in the global config tree).

| Question | Where to look |
| --- | --- |
| Which catalog service profiles exist / are installed? | `sermoctl services [all]` |
| Which catalog apps / libs / pattern sets exist? | `sermoctl apps`, `sermoctl libs`, `sermoctl patterns` |
| Which services are enabled in *my* config right now? | YAML under `paths.services`, or the web UI **Services** panel (`GET /api/services`) |
| One configured service's live state | `sermoctl status SERVICE`, `sermoctl is-active SERVICE` |
| Availability history for configured services | `sermoctl sla [SERVICE]` |

The web UI uses the same split: **Services** shows configured runtime services;
**Applications** (`GET /api/applications`) and **Libraries**
(`GET /api/libraries`) are installed catalog inventories, aligned with
`sermoctl apps` and `sermoctl libs`, not `sermoctl services`.

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

Mount actions are fstab-backed and use storage watch files with a `mount:` block
from directories listed in `paths.watches` (the wizard writes
`/etc/sermo/mounts` by default). A path target that is not configured is still
accepted, but it uses safe defaults and must exist in `/etc/fstab`. See
[storage and mount units](configuration.md#storage-and-mount-units).
`sermoctl umount /` is always rejected; Sermo never unmounts the root
filesystem.
`sermoctl umount TARGET --force` permits `umount -f` after the normal unmount
fails, `--lazy` permits `umount -l` as the last fallback, and
`--kill-blockers` signals only blockers that match
`mount.stop_policy.kill_only_if`.

`sermoctl wizard mount` lists mount points declared in `/etc/fstab` and writes
safe storage watch files under `mounts/`, adding that directory to
`paths.watches`; it does not execute mount or umount while generating the config.
