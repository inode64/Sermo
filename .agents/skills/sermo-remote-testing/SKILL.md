---
name: sermo-remote-testing
description: Use when running safe Sermo validation or exploratory tests on remote Linux servers over SSH, using the host list from .env.ssh as the source of truth, local GOAMD64=v1 builds copied to /tmp, Sermo wizards/tools for temporary setup, complete host discovery only for explicit remote installation or complete remote configuration requests, exposing the web UI with web.address 0.0.0.0 and reclaiming port 9797 from verified Sermo instances, configuring only currently active services, reporting unsupported active services per host, preserving database/LDAP dump stop blockers, limiting start/stop operation tests to acpid, and safe alert/notification checks that must not execute hooks or alter server behavior.
---

# Sermo Remote Testing

## Core Rules

- Build Sermo test binaries locally with `GOAMD64=v1` for broad x86_64 compatibility.
- Copy the locally built artifacts to remote servers and run them under `/tmp/`; never build on the remote server unless the user explicitly asks.
- Never install into `/usr`, `/etc`, `/var/lib`, `/run` or a service manager during remote validation.
- Generate remote config as a temporary `config.yml` under the test directory, with runtime paths also under that directory.
- Use Sermo's own tools for the setup flow: `sermoctl` for validation/discovery/wizards and `sermod run` for the temporary daemon.
- Configure only services that are currently active on the remote server according to its init backend. Do not configure inactive services, stopped units, disabled candidates, volumes, interfaces, or VMs unless the user explicitly expands the scope for that run.
- To expose the panel, set:

```yaml
web:
  address: 0.0.0.0
```

Keep the chosen `web.port` and auth settings explicit. Prefer `9797`; if it is already used by a verified `sermod` process, terminate that Sermo process and reuse `9797`.

- Preserve remote system behavior: do not start, stop, restart, reload, kill, package-install, or write permanent host config unless the user explicitly asks.
- When problems are discovered, fix only the local project code, catalog, docs, or tests; redeploy new `/tmp` artifacts after the local change. Do not patch remote host files to make the test pass.
- If a serious error appears, stop the run, do not continue with the next destructive or state-changing step, and report the actions already performed.
- Treat broken basic commands (`cp`, `cat`, `ls`), failed remote shell startup, or an unexpected SSH disconnect during setup/validation as critical errors. Stop immediately and report what was already done.
- Never configure or execute `then.hook` during remote tests. Hooks can alter the server.
- To verify alert triggering without side effects, prefer alert-only watches or `then.dry_run: true` with a `notify` selection. Dry run logs/events what would happen and skips hook, notify delivery, and expand.

## Local Preparation

1. Inspect local state first:

```sh
git status --short --branch
```

2. Build temporary binaries:

```sh
GOAMD64=v1 go build -o /tmp/sermod-remote-test ./cmd/sermod
GOAMD64=v1 go build -o /tmp/sermoctl-remote-test ./cmd/sermoctl
```

3. Package the current catalog if the remote test needs catalog discovery:

```sh
tar -C "$PWD" -czf /tmp/sermo-catalog-remote-test.tgz catalog
```

Use the current checkout as the source of truth. If remote findings require code/catalog changes, modify only the local project and redeploy new `/tmp` artifacts.

## SSH Workflow

- Use `.env.ssh` as the authoritative host list for remote test runs.
- Treat each non-empty, non-comment line in `.env.ssh` as one SSH target.
- Preserve the line value as provided, including user, host, port, aliases, or SSH options if present.
- Do not invent, scan, or prompt for additional hosts while `.env.ssh` exists; if it is missing or has no usable hosts, stop and report that there are no remote targets to test.
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
- Confirm basic remote commands work: `cp`, `cat`, `ls`, `mkdir`, `chmod`, and `rm`.
- Detect the init backend as `systemd` or `openrc`; if neither is reliable, record the host as unsupported for service setup.
- Check architecture and skip hosts that cannot run the locally built `GOAMD64=v1` Linux amd64 binaries.
- Check that `/tmp` is writable and executable, including a tiny executable probe; if `/tmp` is `noexec`, stop for that host.
- Check free space in `/tmp`; stop for that host if there is not enough room for binaries, catalog, config, logs, and JSON outputs.
- Check whether port `9797` is free before choosing it for the web panel. If it is occupied by another verified Sermo daemon, terminate that process and reuse `9797`. If it is occupied by anything other than Sermo, do not touch it; choose another explicit high port and report it.

## Port 9797 Reuse

When `9797` is already listening:

- Identify the listener PID with `ss -ltnp` or an equivalent read-only command.
- Verify the process before signaling it: `/proc/<pid>/exe` or `/proc/<pid>/cmdline` must identify `sermod`.
- Prefer reclaiming only temporary test instances whose command path or working directory is under `/tmp/sermo-remote-test-*`.
- If the listener is a verified `sermod`, record PID, exe, cmdline, and log path when known, then send `SIGTERM` and wait briefly.
- If the same verified `sermod` PID is still alive after the graceful wait, send `SIGKILL` only to that PID and record it in the final report.
- If PID identity cannot be verified, or the listener is not Sermo, do not kill it; select another explicit high port and report why `9797` was not reclaimed.

## Remote Setup

For each host:

1. Create a unique remote directory under `/tmp`.
2. Copy `sermod`, `sermoctl`, and optional catalog archive into that directory.
3. Write `config.yml` in that directory. Minimal shape for catalog/app checks:

```yaml
paths:
  catalog: [/tmp/sermo-remote-test-XXX/catalog]
  runtime: /tmp/sermo-remote-test-XXX/run

defaults:
  policy: { cooldown: 5m }

web:
  address: 0.0.0.0
  port: 9797
```

Use the real remote directory instead of `XXX`. If a wizard-generated config is used, rewrite only the temporary copy and keep `web.address: 0.0.0.0`.

Use Sermo wizards and tools for configuration generation:

- Prefer `sermoctl wizard service` for active services, and the matching Sermo wizard/tool for other explicitly requested target types.
- Do not hand-write the initial service set when a Sermo wizard can generate it.
- Keep generated config granular: one file per service, mount, notifier, storage watch, network watch, interface, VM, container, app or other target. Watch and notifier fragment files may contain `watches:` or `notifiers:`, but only one named entry.
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
- For ICMP/uplink checks, use `8.8.8.8` as the default check host unless the user or an existing generated config gives a different target.
- Preview generated entries before running `sermod`.
- Prefer detected names. Do not ask the operator to invent service, volume, interface, or VM names when discovery provides candidates.
- If a generated path does not exist on a host, record the host, app/service, expected path, and observed alternative. Fix the local project catalog/config generation, then redeploy.
- Do not let the wizard create host hooks. Choose monitor-only, default notification, or dry-run notification options depending on the test objective.

## Complete Remote Installation Configuration

Only when the user explicitly asks for a remote installation or a complete remote
configuration, expand discovery beyond active services and add host-resource
watches that match the server. Do not add these host-resource watches during
ordinary remote validation, service-specific checks, catalog/app probes, or any
partial test run.

- Discover host resources with read-only probes before generating YAML: CPU
  count/load (`nproc`, `/proc/loadavg`), memory (`/proc/meminfo`), swap
  (`/proc/swaps`, `/proc/vmstat`), PID table (`/proc/loadavg`,
  `/proc/sys/kernel/pid_max`), PSI (`/proc/pressure/*`), mounted local
  filesystems (`findmnt`, `/proc/self/mountinfo`, `df -PT`), network interfaces
  and default routes (`ip addr`, `ip route`), and any explicitly requested
  uplink/ICMP targets.
- Generate one fragment per host watch under the matching temporary directory
  loaded by `paths.storages`, `paths.networks` or `paths.watches`; every fragment
  must contain a top-level `watches:` map with exactly one entry.
- Include baseline watches for memory, load and PID pressure on every complete
  config. Add swap only when swap exists. Add PSI cpu/memory/io only when the
  kernel exposes the matching `/proc/pressure/*` file. Add storage only for
  mounted local filesystems that are currently mounted and safe to monitor; skip
  pseudo filesystems, bind mounts and transient container/runtime mounts unless
  the user explicitly asks for them.
- Prefer portable, conservative thresholds suitable for validation, not
  remediation: memory available/used percentage, load with `per_cpu: true`,
  swap usage and swap IO, pids used percentage, PSI `some_avg60`, and storage
  used/free percentage. Do not add hooks. If notifications are requested only to
  test routing, use `then.dry_run: true`.
- Validate after host watches are added, then run one-shot checks or start the
  temporary daemon only after the full generated config passes.
- Report which host checks were generated, which were skipped, and why
  (for example: no swap configured, PSI unsupported, filesystem excluded as
  pseudo/transient, no default route).

## Unsupported Active Services

If a remote host has active services that Sermo cannot map to a catalog daemon or generated service:

- Do not create approximate or guessed service definitions on the remote host.
- Keep testing the supported active services unless the unsupported service blocks the requested scenario.
- Record unsupported active services per server with: host, init backend, unit/init name, active state, executable or main PID when known, and any obvious catalog alias candidate.
- Include this list in the final report so the user can decide which catalog entries should be added next.

## Operation Test Safety

- Do not test start, stop, restart, reload, or signal operations on arbitrary remote services.
- When an operation test is required, use only `acpid`.
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
# Alert-only: visible in web/events/logs, no hook and no notification delivery.
watches:
  sample:
    check: { type: load, load1: { op: ">", value: 0 } }
```

```yaml
# Notification-route rehearsal: replace ops-email with an existing notifier.
# Logs/events show the dry-run action; no hook, notifier delivery, or expand runs.
watches:
  sample:
    check: { type: load, load1: { op: ">", value: 0 } }
    then:
      notify: [ops-email]
      dry_run: true
```

Rules:

- Do not add `hook:` to `then`.
- Do not configure remediation actions just to test alerting.
- Use `notify: [none]` for monitor-only entries when no notification route should be tested.
- To inherit the top-level `notify`, omit `then.notify` only when a global `notify` is configured; do not write `notify: [default]` in final YAML.
- Use `dry_run: true` whenever a notify route is present solely to prove that an alert would fire.
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
- Clean only directories matching the exact remote prefix created for the run, e.g. `/tmp/sermo-remote-test-*`.
- Before removing a directory, verify it starts with `/tmp/sermo-remote-test-`.
- If cleanup fails due to DNS/SSH, report the host and remote directory path.

## Final Report

Summarize:

- hosts reached and hosts failed;
- panel URLs started;
- active services configured;
- unsupported active services per server;
- alerts that fired or would fire in dry-run;
- `acpid` operation tests run or skipped, with reason;
- missing paths, unsupported apps, or catalog gaps to fix locally;
- serious errors encountered and the actions completed before stopping;
- remote `/tmp` directories left behind, if any;
- commands/tests run locally.
