---
name: sermo-remote-testing
description: Use when running Sermo validation or exploratory tests on remote Linux servers over SSH, using the hosts inventory, local GOAMD64=v1 builds, staged all-host runs, temporary /tmp validation, or explicit remote installation under /etc/sermo.
---

# Sermo Remote Testing

This is a fleet runbook, not product schema. Do not copy its storage, cert,
Docker, SMART or Web UI thresholds into catalog, docs or production defaults.

## Core Rules

- Build Sermo test binaries locally with `GOAMD64=v1` for broad x86_64 compatibility.
- Copy the locally built artifacts to remote servers and run them under `/tmp/`; never build on the remote server unless the user explicitly asks.
- Never install into `/usr`, `/etc`, `/var/lib`, `/run` or a service manager during remote validation-only runs.
- Install into `/etc/sermo`, `/usr`, `/var/lib`, `/run` or a service manager only when the user explicitly asks for remote installation, persistent configuration, or Web UI activation on the host. In that case follow the persistent installation workflow below instead of the temporary validation workflow.
- For validation-only runs, generate remote config as a temporary `config.yml` under the test directory, with runtime paths also under that directory.
- Use Sermo's own tools for the setup flow: `sermoctl` for validation/discovery/wizards and `sermod run` for the temporary daemon.
- Configure only services that are currently active on the remote server according to its init backend. Do not configure inactive services, stopped units, disabled candidates, volumes, interfaces, or VMs unless the user explicitly expands the scope for that run.
- If a code/catalog/schema change is required after any host has already been installed or configured, rebuild and redeploy the fixed payload/config to every already-touched host before continuing with new hosts. Do not leave earlier hosts on stale behavior.
- To expose the panel, point `web` at hashed credential files. The `password`
  and `guest_password` keys are retired and rejected by `config validate`:

```yaml
web:
  address: 0.0.0.0
  port: 9797
  password_file: /tmp/sermo-remote-test-XXX/credentials.env
  guest_password_file: /tmp/sermo-remote-test-XXX/guest-credentials.env  # optional
```

Create each file with the run's own `sermoctl`, one hashed credential per line,
mode `0600`; `sermod` refuses to start when a configured file is unreadable or
holds no credential. `--generate` writes the hash line to stdout and the secret
once to stderr; keep that secret in the local result directory, never in the
repository or this skill:

```sh
umask 077
/tmp/sermo-remote-test-XXX/sermoctl web hash-password --generate --name remote-test \
  > /tmp/sermo-remote-test-XXX/credentials.env
```

A validation run that does not need the panel omits the `web` section; the
daemon then opens no port and alerts are read from `sermod.log`.

Keep the chosen `web.port` and auth settings explicit. Prefer `9797` when it is
free. The fleet runs an installed `sermod` on `0.0.0.0:9797`, so a temporary run
on such a host uses another explicit high port. Reclaim a port only from a
verified temporary `sermod` under `/tmp/sermo-remote-test-*`; never signal an
installed `sermod` outside its init system. Stopping the installed daemon needs
the user's explicit authorization for that host, goes through the init system,
and the daemon is started again the same way at cleanup.

- Preserve remote system behavior: do not start, stop, restart, reload, kill, package-install, or write permanent host config unless the user explicitly asks.
- Mutating remote install/update/apply scripts must never change type, mode, uid
  or gid on protected parent system paths: `/`, `/etc`, `/usr`, `/usr/lib`,
  `/etc/systemd`, `/usr/lib/tmpfiles.d`, `/etc/init.d` and `/usr/share`. Do not
  `chown`, `chmod`, extract archive directory entries, or preserve local
  workstation archive owners onto those paths. Capture before/after metadata for
  that exact list and fail the run if any entry changes.
- When problems are discovered, fix only the local project code, catalog, docs, or tests; redeploy new `/tmp` artifacts after the local change. Do not patch remote host files to make the test pass.
- Classify every failure before deciding whether to stop the fleet:
  - **Host-local skip:** SSH/authentication failure, disconnected host, broken shell or
    basic command, incompatible architecture, unsupported init, `/tmp` unavailable,
    `noexec`, out of space or inodes, a host-specific service failure, or a pre-existing
    local configuration error. Record the host, phase, evidence and whether anything
    changed; then continue with the next host. Do not free space, alter the host or
    weaken validation merely to make that host pass.
  - **Sermo blocker:** a failure reproducible with the same checked-out binary,
    catalog, generated configuration or deployment script on more than one host; an
    invalid artifact generated by Sermo; an unsafe protected-path metadata change; or a
    regression introduced by the run. Stop before touching further hosts, fix it
    locally, validate it and redeploy the fixed artifact to every host already touched.
- Never configure or execute `then.hook` during remote tests. Hooks can alter the server.
- To verify alert triggering without non-console side effects, prefer alert-only watches or target-level `dry_run: true` with a `notify` selection. Dry run logs/events what would happen and skips service remediation operations, the watch actions `hook`, `expand`, `remount`, `kill` and `makestep`, and non-wall notify delivery. The top-level `event_notify` route still delivers unless panic mode is on.

## Local Preparation

1. Inspect local state first:

   ```sh
   git status --short --branch
   ```

2. Choose the absolute remote run directory before building, then build temporary
   binaries with the catalog directory compiled to that same remote path:

   ```sh
   REMOTE_DIR=/tmp/sermo-remote-test-$(date +%Y%m%d%H%M%S)-$$
   GOAMD64=v1 SERMO_DATADIR="$REMOTE_DIR" make build
   ```

   This overwrites `bin/sermod` and `bin/sermoctl` with binaries whose catalog
   path is the temporary directory. `update_fleet.sh --skip-build` and
   `deploy_binaries_only_fleet.sh` ship `bin/` as it is, so rebuild with
   `SERMO_DATADIR=/usr/share/sermo` (or let `update_fleet.sh` build) before any
   fleet deploy.

3. Package the current catalog if the remote test needs catalog discovery:

   ```sh
   tar -C "$PWD" -czf /tmp/sermo-catalog-remote-test.tgz catalog
   ```

Use the current checkout as the source of truth. If remote findings require code/catalog changes, modify only the local project and redeploy new `/tmp` artifacts.

## SSH Workflow

- The host inventory is supplied by the operator: a `hosts` file kept outside this repository (point the tools at it with `SERMO_SSH_ENV`). The maintained fleet inventory and the deployment tooling live in a private operations repository; never copy hosts, IP addresses, customer names or fleet scripts into this repository. Read the inventory's first column afresh before choosing the first four hosts.
- Treat the first column of each non-empty, non-comment line in `hosts` as one SSH target. The other columns hold customer data: never print, log or report them.
- The target is a bare `[user@]host` (alphanumerics, `@`, `.`, `_`, `-`); one without a user logs in as `root`. Record and skip an entry carrying a port, option or any other character, and never pass the other columns to `ssh`.
- The fleet can be deployed from any inventory host so the inventory may include the deploy host itself. Identify it with `hostname` at the start of each run instead of assuming one: its `/tmp`, `bin/` and running `sermod` are local ones, and a local run directory must not reuse a remote work-dir name.
- Do not invent, scan, or prompt for additional hosts while `hosts` exists; if it is missing or has no usable hosts, stop and report that there are no remote targets to test.
- When the user asks for the first `N` servers/hosts, use exactly the first `N`
  usable non-comment entries from `hosts`.
- When a full-fleet scope includes a **mutating** Sermo action—temporary artifact execution, installation, update, config apply, daemon activation or service-manager change—run in two stages:
  1. Run the requested mutating workflow end-to-end on only the first four usable `hosts` entries.
  2. Review the first-four results before continuing. Continue with the remaining entries when every first-stage outcome is either successful or a documented host-local skip, and no Sermo blocker indicates a project/catalog/deployment bug that would invalidate the rest of the run.
- The first-four gate does **not** apply to read-only collection or analysis: inventory, logs, events, service status and other SSH reads may cover every selected host in one pass. Record failures and Sermo findings, but do not pause that read-only collection after the first four hosts.
- Apply the same first-four gate to any selected mutating set larger than four hosts, including "first N" installation or update requests.
- If the mutating first-four stage finds a Sermo blocker, stop before changing the remaining hosts and report the exact failing host, command phase, output path, and whether any remote `/tmp/sermo-remote-test-*` directory was created. A host-local skip is not a fleet blocker: continue after reporting it.
- Use batch SSH options and bounded connection timeouts:

```sh
ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 ...
scp -o BatchMode=yes -o ConnectTimeout=10 ...
```

- Network SSH/SCP normally needs escalation approval. Request it with a concise justification.
- Keep per-run local results in `/tmp/sermo-remote-<timestamp>/`.
- Keep per-host remote files in `/tmp/sermo-remote-test-<timestamp>-<pid>/`.

## Remote Preflight

Run this before copying or executing Sermo artifacts on each host:

- Confirm SSH connects with batch mode and a bounded timeout.
- Confirm basic remote commands work: `cp`, `cat`, `ls`, `mkdir`, and `rm`.
  If an executable probe is needed, run it through the shell or under the
  temporary run directory only; never use `chmod` on system paths.
- Detect the init backend as `systemd` or `openrc`; if neither is reliable, record the host as unsupported for service setup.
- Check architecture and skip hosts that cannot run the locally built `GOAMD64=v1` Linux amd64 binaries.
- Check that `/tmp` is writable and executable, including a tiny executable probe; if `/tmp` is `noexec`, skip that host and continue.
- Check free space in `/tmp`; skip that host when there is not enough room for binaries, catalog, config, logs and JSON outputs. Report the filesystem, available bytes and required artifact size; never delete data or expand storage automatically.
- Check whether port `9797` is free before choosing it for the web panel. If it
  is held by a verified temporary test `sermod` under `/tmp/sermo-remote-test-*`,
  reclaim it as below; if it is held by the installed `sermod` or by anything
  else, do not touch it: choose another explicit high port and report it.

## Port 9797 Reuse

When `9797` is already listening:

- Identify the listener PID with `ss -ltnp` or an equivalent read-only command.
- Verify the process before signaling it: `/proc/<pid>/exe` or `/proc/<pid>/cmdline` must identify `sermod`.
- Reclaim only temporary test instances whose command path or working directory
  is under `/tmp/sermo-remote-test-*`. An installed `sermod` (for example
  `/usr/bin/sermod` under systemd or OpenRC) is never signalled directly: use
  another port, or, with the user's explicit authorization, stop it through the
  init system and start it again the same way at cleanup.
- If the listener is a verified temporary `sermod`, record PID, exe, cmdline, and log path when known, then send `SIGTERM` and wait briefly.
- If the same verified temporary `sermod` PID is still alive after the graceful wait, send `SIGKILL` only to that PID and record it in the final report.
- If PID identity cannot be verified, or the listener is not Sermo, do not kill it; select another explicit high port and report why `9797` was not reclaimed.

## Remote Setup

For each host:

1. Create the `REMOTE_DIR` chosen during local preparation under `/tmp` on that
   host.
2. Copy `bin/sermod`, `bin/sermoctl`, and optional catalog archive into that
   directory.
3. Write `config.yml` in that directory. Minimal shape for catalog/app checks:

```yaml
engine:
  runtime: /tmp/sermo-remote-test-XXX/run
  state: /tmp/sermo-remote-test-XXX/state

defaults:
  policy: { cooldown: 5m }

web:
  address: 0.0.0.0
  port: 9797   # another explicit high port when the installed sermod holds 9797
  password_file: /tmp/sermo-remote-test-XXX/credentials.env
```

Use the real remote directory instead of `XXX`. Never expose the panel without
`password_file`: with no credentials it serves admin actions to anyone who can
reach the port. Drop the `web:` block entirely when the run does not need the
panel. Do not add a catalog path to the
config; for catalog/app checks, unpack the catalog archive to
`/tmp/sermo-remote-test-XXX/catalog` and use binaries built with
`SERMO_DATADIR=/tmp/sermo-remote-test-XXX`. If a wizard-generated config is
used, rewrite only the temporary copy and keep `web.address: 0.0.0.0`.

Use Sermo wizards and tools for configuration generation:

- Prefer `sermoctl wizard service` for active services, and the matching Sermo wizard/tool for other explicitly requested target types.
- Do not hand-write the initial service set when a Sermo wizard can generate it.
- Keep generated config granular: one file per service, notifier, storage watch,
  network watch, interface, VM, container, app or other target. Storage,
  network/uplink and mount files are all watch documents loaded from
  `paths.watches`, with top-level `name:` plus the watch fields; notifier
  fragment files contain `notifiers:` with one named entry.
- If the wizard output needs adjustment, edit only the generated files under the remote `/tmp/sermo-remote-test-*` directory, then run `sermoctl config validate` again.
- If an adjustment reveals a project/catalog bug, fix the local project and redeploy new `/tmp` artifacts instead of patching permanent remote host files.

Do not start `sermod` until the temporary config passes:

- `sermoctl config validate --config /tmp/sermo-remote-test-XXX/config.yml`
- catalog loading/discovery checks needed by the run

If validation fails, stop before starting `sermod`, save the failure output locally, and modify only the local project if a code or catalog fix is needed.

## Wizard And Discovery

When the task asks to add remote targets:

- Use the Sermo wizard/assist flow only against temporary config files under `/tmp`.
- Select only services reported active by the remote init backend.
- Skip volumes, interfaces, and VMs unless the user explicitly asks for them in the current task.
- For ICMP/uplink checks, follow the fleet generator: a complete install pings the default gateway. In the wizard keep its default target (`1.1.1.1`) unless the user or an existing generated config gives a different one.
- Preview generated entries before running `sermod`.
- Prefer detected names. Do not ask the operator to invent service, volume, interface, or VM names when discovery provides candidates.
- If a generated path does not exist on a host, record the host, app/service, expected path, and observed alternative. Fix the local project catalog/config generation, then redeploy.
- Do not let the wizard create host hooks. Choose monitor-only, default notification, or dry-run notification options depending on the test objective.

## Complete Remote Installation Configuration

Only when the user explicitly asks for a remote installation or a complete remote
configuration, expand discovery beyond active services and add the full set of
host watches that match the server. Do not add these host-resource watches during
ordinary remote validation, service-specific checks, catalog/app probes, or any
partial test run.

## Persistent Remote Installation Defaults

Explicit remote installation or persistent configuration requests override the
validation-only `/tmp` restrictions above. They are carried out with the
private fleet tooling, which also documents what a persistent install must
generate under `/etc/sermo`; it is not part of this repository. The invariants
above still apply: local `GOAMD64=v1` builds with
`SERMO_DATADIR=/usr/share/sermo`, root-owned payloads extracted with
`tar --no-same-owner`, no metadata change on protected parent paths, only
active or failed catalog-supported services, the operator `.local` layers and
`credentials.env` preserved, and the first-four gate for mutating fleet runs.
When a code, catalog or schema change alters what the installer must generate,
report it so the private tooling is updated in the same piece of work.

## Full Daemon Resource Observation

Use this section only when the user explicitly asks to activate `sermod` with all
features, run the full Web UI, or leave the temporary daemon active for analysis.
It extends the complete remote installation configuration above; it does not
apply to ordinary CLI-only validation.

- Start `sermod` only after `sermoctl config validate` passes for the complete
  temporary config. Keep it under the remote `/tmp/sermo-remote-test-*`
  directory and record `sermod.pid`, `sermod.log`, the selected web port and the
  full command line.
- Observe the daemon for at least six minutes after readiness before declaring a
  full-feature run healthy. Poll at a fixed interval such as 30s and save raw
  samples locally:
  - `/api/host` for host CPU, memory and load readings;
  - `/api/daemon/metrics?since=10m` for `sermod` CPU, memory and IO history;
  - `/api/events` or the relevant watch/event endpoint when available;
  - `ps -p <sermod_pid> -o pid,etime,pcpu,pmem,rss,vsz,cmd` as a fallback and
    cross-check for daemon CPU/RAM usage.
- For any generated pressure, depletion, saturation or absence-sensitive watch
  that remains firing or near-threshold for more than five minutes, include a
  watch-specific report using its readings plus safe read-only host samples that
  explain the condition when available. Do not run stress tools or change host
  state to reproduce a finding.
- For feature-dependent watch types that were skipped because the host did not
  expose the underlying resource, include the read-only evidence that established
  the absence when that evidence is meaningful over the six-minute observation
  window.
- Include every generated host-watch family from the run-time inventory in the
  observation output, not only the subset covered by older remote-test runs. For
  each discovered type that was skipped, carry forward the skip reason from
  configuration generation.
- Include storage and mount watch results in the observation output. Report any
  filesystem with less than 5% free space, any configured storage watch whose
  mount condition failed, and any generated local/network/USB mount target that
  was skipped because it was not fstab-backed.
- Include direct-file `/etc/ssl` certificate watch results and related events in
  the observation output. Report any certificate that is expired, not yet valid,
  under the 15-day expiry threshold, missing, unreadable, unparseable, or changed
  by issuer/signature algorithm between cycles. For obsolete-looking algorithms
  or weak key sizes, report the `signature_algorithm`, `public_key_algorithm`
  and `key_bits` values as review findings; do not claim Sermo blocks them unless
  an explicit policy or check exists in the generated config.
- Treat these reports as observation only. Do not add hooks, do not trigger
  remediation, and do not change host swap, sysctl, service or package state.
- Include the sustained-resource observation results in the final report even
  when no alert fired: state whether the six-minute observation completed, which
  sustained conditions were seen, and where the raw JSON/log samples are stored.

## Unsupported Active Services

If a remote host has active services that Sermo cannot map to a catalog service or generated service:

- Do not create approximate or guessed service definitions on the remote host.
- Keep testing the supported active services unless the unsupported service blocks the requested scenario.
- Record unsupported active services per server with: host, init backend, unit/init name, active state, executable or main PID when known, and any obvious canonical catalog profile candidate.
- Include this list in the final report so the user can decide which catalog entries should be added next.

## Operation Test Safety

- Do not test start, stop, restart, reload, pause, resume, repair, mount/umount, watch pause/resume or signal operations on arbitrary remote services.
- When an operation test is required, use only `acpid` for start, stop, restart and reload paths.
- Skip `pause`/`resume` tests: they apply only to libvirt and Docker control targets, which are production VMs and containers, unless the user names a disposable target.
- Before testing `acpid`, confirm it is active and represented by the temporary config.
- If `acpid` is missing, inactive, unsupported, or its config/preflight fails, skip operation testing and report why.
- Run operation tests through Sermo's normal command path with the temporary config; do not call `systemctl`, `rc-service`, `kill`, or init scripts directly except for read-only status inspection.
- If any `acpid` operation produces an unexpected failure, stop further operation testing and report the exact command, output, log path, and remote state observed afterward.

## Stop Blockers

When generated service files or catalog profiles include operation safety for databases or directory services, preserve these blockers. If missing, add them only to generated `/tmp` files for the remote test and record the catalog/project gap:

- MySQL and MariaDB stops/restarts must be blocked while any `mysqldump`, `mariadb-dump`, or `wal-g-mysql` process is running.
- PostgreSQL stops/restarts must be blocked while `pg_dumpall` is running.
- OpenLDAP stops/restarts must be blocked while `slapcat` is running.

Never bypass these blockers during remote tests. If one is active, report the service as blocked and skip the operation.

## Alert And Notification Safety

Use one of these safe modes:

```yaml
# Alert-only: visible in web/events/logs, no hook and no notification delivery
# (a top-level event_notify route, when configured, still delivers events).
name: sample
check: { type: load, load1: { op: ">", value: 0 } }
```

```yaml
# Notification-route rehearsal: replace ops-email with an existing notifier.
# Logs/events show the dry-run action; no hook, non-wall notify, expand or kill runs.
name: sample
dry_run: true
check: { type: load, load1: { op: ">", value: 0 } }
then:
  notify: [ops-email]
```

Rules:

- Do not add `hook:` to `then`.
- Do not configure remediation actions just to test alerting.
- Use `notify: [none]` for monitor-only entries when no notification route should be tested.
- To inherit the top-level `notify`, omit `then.notify` only when a global `notify` is configured; do not write `notify: [default]` in final YAML.
- Use `dry_run: true` whenever a notify route is present solely to prove that an alert would fire.
- Certificate expiry or damaged-file notifications follow the same rule: real
  delivery is allowed only when the user explicitly requested notification
  delivery for the remote installation; otherwise keep the watch alert-only or
  dry-run.
- Keep test thresholds and intervals clearly temporary; restore or delete the `/tmp` config after the run.

## Running And Observing

- Run one-shot checks with the temporary config first:

```sh
/tmp/sermo-remote-test-XXX/sermoctl --config /tmp/sermo-remote-test-XXX/config.yml apps all --json
```

- Start `sermod` only from `/tmp`, never as a system service:

```sh
nohup /tmp/sermo-remote-test-XXX/sermod run --config /tmp/sermo-remote-test-XXX/config.yml \
  > /tmp/sermo-remote-test-XXX/sermod.log 2>&1 &
echo $! > /tmp/sermo-remote-test-XXX/sermod.pid
```

- Report the panel URL as `http://<host>:<port>` after confirming the daemon is listening.
- Capture logs, JSON outputs, generated config, and failures into the local result directory.
- Always save the unsupported active service list and `sermod.log` locally under `/tmp/sermo-remote-<timestamp>/`, grouped by host.
- If `sermod` fails to start, exits unexpectedly, or reports configuration corruption, stop the run and summarize what was copied, generated, started, and observed before the failure.

## Cleanup

- Leave no persistent installation behind: no systemd unit, no OpenRC service, and no files outside `/tmp`.
- **Stop the temporary daemon before removing its directory.** Removing the
  directory alone leaves `sermod` running against a deleted binary and a deleted
  config, indefinitely and invisibly — the run looks cleaned up while the daemon
  survives every later check that only looks for files. Signal the PID recorded
  in `<dir>/sermod.pid`, and only after confirming `/proc/<pid>/exe` resolves
  under `/tmp/sermo-remote-test-`, so the run never signals a process it did not
  start:

  ```sh
  pid="$(cat /tmp/sermo-remote-test-XXX/sermod.pid)"
  case "$(readlink "/proc/${pid}/exe")" in
    /tmp/sermo-remote-test-*) kill -TERM "$pid" ;;
    *) echo "pid ${pid} is not this run's sermod; not signaling" ;;
  esac
  ```

- After the signal, confirm no process remains whose `/proc/<pid>/exe` resolves
  under `/tmp/sermo-`; escalate to `kill -KILL` only for a PID that passed the
  same identity check. Record every daemon stopped.
- Clean only directories matching the exact remote prefix created for the run, e.g. `/tmp/sermo-remote-test-*`.
- Before removing a directory, verify it starts with `/tmp/sermo-remote-test-`.
- If cleanup fails due to DNS/SSH, report the host and remote directory path.
- For persistent updates using `remote_update_payload.sh`, a successful run removes
  its exact `/tmp/sermo-update-<run-id>` work directory and uploaded
  `/tmp/sermo-*/<payload>.tgz` by default. A failed update keeps both for
  diagnosis. Use `SERMO_KEEP_REMOTE_ARTIFACTS=1` only when the successful-run
  evidence must remain on the host; otherwise copy the needed results locally
  before removing the exact staging directory.

## Final Report

Summarize:

- hosts reached and hosts failed;
- hosts skipped for local constraints, with host, phase, evidence, mutation status and
  safe remediation (for example, `host1: preflight — /tmp on /dev/root had 0 bytes
  available; no deployment was attempted; free space before retrying`);
- panel URLs started;
- daemon startup and Web UI timing per host: `/livez` seconds, `/readyz.ready`
  seconds, HTML/API response time and status;
- active services configured;
- Docker containers and libvirt/QEMU virtual machines generated or skipped, with
  skip reasons;
- unsupported active services per server;
- alerts that fired or would fire in dry-run;
- storage findings: filesystems below 5% free space, failed `mounted: true`
  checks, and local/network/USB mount units generated or skipped;
- complete host-watch coverage: the run-time inventory source, every generated
  watch type/target and every discovered type skipped with its reason;
- direct-file `/etc/ssl` certificate findings: expiring within 14 days,
  expired, not yet valid, missing/unreadable/unparseable, algorithm
  changes, and any weak/obsolete-looking algorithm or key-size review notes;
- `acpid` operation tests run or skipped, with reason;
- missing paths, unsupported apps, or catalog gaps to fix locally;
- Sermo blockers encountered, their reproducibility evidence and the actions completed before stopping;
- temporary daemons stopped, and any that could not be stopped, with host and PID;
- temporary artifacts removed after successful installations, retained evidence
  requested by the operator, and remote `/tmp` directories left behind, if any;
- protected-path metadata check status for `/`, `/etc`, `/usr`, `/usr/lib`,
  `/etc/systemd`, `/usr/lib/tmpfiles.d`, `/etc/init.d` and `/usr/share`;
- commands/tests run locally.
