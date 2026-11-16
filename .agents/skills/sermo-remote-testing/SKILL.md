---
name: sermo-remote-testing
description: Use when running Sermo validation or exploratory tests on remote Linux servers over SSH, using the hosts inventory, local GOAMD64=v1 builds, staged all-host runs, temporary /tmp validation, or explicit remote installation under /etc/sermo.
---

# Sermo Remote Testing

A runbook for running a locally built Sermo on remote hosts without changing
them. It is not product schema: do not copy its thresholds into catalog, docs
or defaults. Persistent installation and fleet deployment are done with the
operator's private fleet tooling and its own documentation, never from this
repository.

## Boundaries

- A validation run lives entirely in one remote directory,
  `/tmp/sermo-remote-test-<timestamp>-<pid>`: binaries, catalog, `config.yml`,
  `engine.runtime` and `engine.state`. Nothing is written to `/usr`, `/etc`,
  `/var/lib`, `/run` or a service manager, and nothing is built on the host.
- The host keeps its behavior: no start, stop, restart, reload, kill, package
  install or permanent config unless the user asks for that action on that
  host. No `then.hook`, ever.
- The temporary daemon observes and never acts: `defaults.dry_run: true`, and
  no target of the run sets `dry_run: false`. A catalog profile brings its own
  remediation (a restart after two failed cycles), and the cooldown the config
  must declare is what arms it.
- A finding is fixed in the local checkout, then redeployed to every host
  already touched. Never patch a remote file to make a test pass.
- The installed `sermod` of a host is production. It acts for real
  (`dry_run: false`), so it restarts a service a test stops: before stopping a
  service the user approved, use the installed `sermoctl stop SERVICE` or
  `sermoctl unmonitor SERVICE`, and restore it at cleanup. Never signal the
  installed daemon; stopping it needs the user's authorization for that host,
  goes through the init system and is undone the same way. A shadow daemon
  built from a copy of `/etc/sermo` sets every `dry_run: false` of the copy to
  `true` (they are per target and beat `defaults`).
- Persistent installation happens only on an explicit request, through the
  private tooling. Its invariants: binaries built with
  `SERMO_DATADIR=/usr/share/sermo`; payloads extracted with
  `tar --no-same-owner`; type, mode, uid and gid unchanged on `/`, `/etc`,
  `/usr`, `/usr/lib`, `/etc/systemd`, `/usr/lib/tmpfiles.d`, `/etc/init.d` and
  `/usr/share` (capture them before and after, fail on any difference); only
  active or failed catalog-supported services; the operator's `.local` layers
  and `credentials.env` preserved. When a code, catalog or schema change alters
  what the installer must generate, report it so the tooling changes in the
  same piece of work.

## Inventory and staging

- The inventory is the operator's `hosts` file, outside this repository
  (`SERMO_SSH_ENV` points to it). Only the first column is a target; the other
  columns are customer data and are never printed, logged, reported or passed
  to `ssh`. Hosts, addresses, customer names and fleet scripts never enter
  this repository.
- A target is a bare `[user@]host` (alphanumerics, `@`, `.`, `_`, `-`),
  `root` when no user is given. Record and skip any other entry. "The first N
  hosts" are the first N usable entries. With no usable inventory, stop: do
  not invent, scan or ask for hosts.
- The inventory may include the machine the run starts from. Compare
  `hostname` first: there `/tmp`, `bin/` and the running `sermod` are local,
  and a local results directory must not reuse the remote run directory's name.
- A mutating run (artifacts executed, install, update, config apply) over more
  than four hosts does the first four end to end, reviews them, then
  continues. Read-only collection covers every host in one pass.
- Classify each failure. A **host-local skip** (SSH, shell, architecture,
  init, `/tmp` not writable or `noexec`, no space, a pre-existing local error)
  is recorded with host, phase and evidence, and the run continues; never free
  space or alter the host to make it pass. A **Sermo blocker** (reproducible
  with the same artifact on more than one host, an invalid generated artifact,
  a protected-path metadata change, a regression from the run) stops the run
  before any further host.
- `ssh` and `scp` use
  `-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=2`.
  Local results go under `/tmp/sermo-remote-<timestamp>/`, grouped by host.

## Build and stage

```sh
git status --short --branch
REMOTE_DIR=/tmp/sermo-remote-test-$(date +%Y%m%d%H%M%S)-$$
GOAMD64=v1 SERMO_DATADIR="$REMOTE_DIR" make build
tar -C "$PWD" -czf /tmp/sermo-catalog-remote-test.tgz catalog
```

The catalog directory is compiled in, so the config names no catalog path:
unpack the archive to `$REMOTE_DIR/catalog`. This build overwrites `bin/` with
binaries bound to the temporary directory; rebuild before anything ships
`bin/` to a persistent install.

Per host, before copying: SSH connects; init is `systemd` or `openrc`; the
architecture runs linux/amd64; `/tmp` is writable and executable (run a tiny
probe) with room for the artifacts; which port is free for the panel.

## Configuration

```yaml
engine:
  runtime: /tmp/sermo-remote-test-XXX/run
  state: /tmp/sermo-remote-test-XXX/state

defaults:
  dry_run: true
  policy: { cooldown: 5m }

web:                # only when the run needs the panel
  address: 0.0.0.0
  port: 9797
  password_file: /tmp/sermo-remote-test-XXX/credentials.env
```

- Generate targets with Sermo's own wizards (`sermoctl wizard service`, and
  the matching wizard for another target type) against the temporary config;
  keep detected names and the wizard's defaults, except its dry-run question,
  which defaults to no: answer yes. One file per target. Adjust only the
  generated files under the run directory.
- Configure only services the init backend reports active. Volumes,
  interfaces, VMs and the full set of host watches are added only when the
  user asks for a complete configuration.
- An active service with no catalog profile is not approximated: record host,
  init backend, unit, state, executable and any obvious catalog candidate, and
  report the list.
- The panel never runs without `password_file`: with no credentials it serves
  admin actions to anyone who reaches the port. Create it with the run's own
  binary; the secret goes to stderr once and stays in the local results:

  ```sh
  umask 077
  "$REMOTE_DIR/sermoctl" web hash-password --generate --name remote-test \
    > "$REMOTE_DIR/credentials.env"
  ```

- Port `9797` usually belongs to the installed `sermod`; then use another
  explicit high port. Reclaim it only from a temporary `sermod` whose
  `/proc/<pid>/exe` resolves under `/tmp/sermo-remote-test-`.
- To prove an alert fires, use an alert-only watch (no `then`), or
  `dry_run: true` with `then.notify` naming an existing notifier. Dry run
  logs what would happen and skips remediation, the watch actions and
  non-wall notify delivery; a top-level `event_notify` route still delivers
  unless panic mode is on. `notify: [none]` is monitor-only. Real delivery
  needs the user's request.
- A generated database or directory profile keeps its backup guards: stop
  and restart blocked while `mysqldump`, `mariadb-dump` or `wal-g-mysql` runs
  (MySQL, MariaDB), `pg_dumpall` (PostgreSQL) or `slapcat` (OpenLDAP). If one
  is missing, add it to the temporary file and report the catalog gap; never
  bypass an active guard.

`sermoctl config validate --config "$REMOTE_DIR/config.yml"` passes before any
`sermod` starts.

## Running

```sh
"$REMOTE_DIR/sermoctl" --config "$REMOTE_DIR/config.yml" apps all --json
nohup "$REMOTE_DIR/sermod" run --config "$REMOTE_DIR/config.yml" \
  > "$REMOTE_DIR/sermod.log" 2>&1 &
echo $! > "$REMOTE_DIR/sermod.pid"
```

- Operation tests (start, stop, restart, reload) use only `acpid`, only when
  it is active and in the temporary config, and only through Sermo's command
  path. No other operation is tested on a remote host: `repair`, `mount`,
  `umount`, `watch pause`/`resume` and the kill commands act on production
  mounts, arrays and sessions, and `pause`/`resume` on production VMs and
  containers, unless the user names a disposable target. Stop operation
  testing at the first unexpected result.
- A full-feature run the user asked for is observed at least six minutes after
  readiness, sampling `/api/host`, `/api/daemon/metrics?since=10m`,
  `/api/events` and `ps` of the daemon into the local results. Report what
  stayed firing or near its threshold with read-only evidence; never stress
  the host or change its state to reproduce a finding.

## Cleanup

Stop the temporary daemon before removing its directory: a removed directory
leaves `sermod` running from a deleted binary, invisible to any check that
looks for files.

```sh
pid="$(cat "$REMOTE_DIR/sermod.pid")"
case "$(readlink "/proc/${pid}/exe")" in
  /tmp/sermo-remote-test-*) kill -TERM "$pid" ;;
  *) echo "pid ${pid} is not this run's sermod; not signaling" ;;
esac
```

Then confirm no process runs from `/tmp/sermo-remote-test-`; `kill -KILL` only
a PID that passed the same check. Remove only the run's exact directory.

## Report

Hosts reached, skipped (phase, evidence, whether anything changed) and
blocked; what was configured and what was left out, with the reason;
unsupported active services per host; alerts that fired or would fire;
operation tests run or skipped; catalog and code gaps to fix locally; daemons
stopped and directories left behind; the protected-path check for a persistent
install; local commands run.
