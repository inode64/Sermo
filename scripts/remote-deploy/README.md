# Remote Deployment Scripts

These scripts support repeatable Sermo installations on remote hosts after a
local build. They are intentionally small wrappers around Sermo's own binaries:
stage read-only host inventory, generate one-file-per-target config, apply the
config under `/etc/sermo`, start `sermod`, and verify the Web UI.

Before changing init integration, run the read-only lifecycle prevalidation
against the complete `.env.ssh` inventory:

```sh
scripts/remote-deploy/prevalidate_init_contracts.py --env .env.ssh
```

It continues after unreachable hosts, validates the installed Sermo
configuration, detects systemd/OpenRC, and compares every standard
`/etc/sermo/services` target reported by `sermoctl status` with the init
manager's direct state and lifecycle metadata. Its TSV report contains host,
unit and state information only; values from `.env.ssh`, configuration content
and credentials are never copied into it. A non-zero exit means at least one
host was unreachable or had a mismatch and must be explained before an init
change is committed. Use repeatable `--host HOST` arguments for a focused rerun.

Typical flow:

```sh
RUN_ROOT=/tmp/sermo-install-$(date +%Y%m%d-%H%M%S)
mkdir -p "$RUN_ROOT"
GOAMD64=v1 SERMO_DATADIR=/usr/share/sermo make build
scripts/remote-deploy/prepare_payload.sh "$RUN_ROOT" "$PWD"
scripts/remote-deploy/generate_install_config.py \
  --stage-root "$RUN_ROOT/stage" \
  --configs-root "$RUN_ROOT/configs" \
  --report "$RUN_ROOT/config-report.json"
```

The remote scripts must run as root on the target host:

- `remote_stage.sh` installs the payload, replaces stale packaged catalog files,
  writes a minimal `/etc/sermo/sermo.yml`, and collects read-only host inventory.
- `remote_inventory_common.sh` contains read-only collectors shared by the
  install and update inventories, including EPMD ownership hints used to avoid
  competing with RabbitMQ-owned EPMD on OpenRC.
- `remote_apply.sh` replaces generated config directories under `/etc/sermo`,
  validates the config, enables/restarts `sermod`, and verifies the local Web UI.
- `remote_update_payload.sh` refreshes binaries/catalog on an already configured
  host. It stages the payload under its work directory and validates the current
  `/etc/sermo` with the **candidate** `sermoctl` and the detected init backend
  *before* replacing anything on disk. `update_fleet.sh` builds that validator
  with its catalog path set to the same run-specific staging directory, so
  validation always uses the candidate binary and candidate catalog together
  rather than the host's older packaged catalog. A configuration the candidate
  rejects aborts with exit `30` and the host untouched. Only then does the
  updater install the binaries and catalog, restart `sermod` and verify the local
  Web UI; if the daemon never becomes ready, or the init backend is unsupported,
  it restores the previous binaries and catalog, restarts, and exits `50` (`40`
  for the init case). HTTP probes are bounded to
  five seconds by default (`SERMO_HTTP_TIMEOUT_SECONDS`). After a successful
  update it deletes only its exact `/tmp/sermo-update-<run-id>` work directory
  and the uploaded `/tmp/sermo-*/<payload>.tgz`, freeing the payload and captured
  output. Failed updates keep those artifacts for diagnosis. Set
  `SERMO_KEEP_REMOTE_ARTIFACTS=1` to retain successful-update artifacts too;
  after copying any required evidence locally, remove the exact staging directory
  created for that run.

## Credentials deployment

`deploy_credentials.py` distributes dashboard passwords without putting them in
the inventory, command line or report. Its source inventory must have
`cliente` and `ip_vpn` columns, and the local password source has one
`cliente contraseña` entry per line. It writes only
`/etc/sermo/credentials.env`, preserving existing credential lines and adding
the server's own client password plus `inode64`; `amizalsa`, `bertolin`,
`euromeca`, `maberauto` and `realexport` receive `optiza` too. The destination
is atomically replaced as `root:root` mode `0600`; the daemon is not restarted.

```sh
scripts/remote-deploy/deploy_credentials.py \
  --inventory inventario-red-172.31.16.csv \
  --passwords .env.pass
```

For a retry against a known subset, repeat `--ip-vpn` for each exact address;
each address must appear in the supplied inventory.
Use `--exclude-ip-vpn` to leave selected inventory addresses untouched.

After credential deployment, make active daemons load the file by adding
`--configure-web`. It replaces any direct `web.password` or prior
`web.password_file` with `web.password_file: /etc/sermo/credentials.env`,
validates the configuration, then restarts only an already-active `sermod` with
the detected init backend. On validation or restart failure it restores the
previous configuration and attempts to restore the prior daemon state.

## Opening dashboards locally

`scripts/open_sermo_dashboards.py` opens the complete CSV inventory in a
separate Chrome profile. It reads the `inode64` password at runtime and supplies
it to HTTP Basic challenges through a temporary, origin-restricted Chrome
extension, so the password is neither embedded in the dashboard URLs nor passed
on Chrome's command line. Close the window when finished so the temporary
profile and extension are deleted.

```sh
scripts/open_sermo_dashboards.py
```

It validates every mapping before contacting a host, preflights the complete
inventory, then applies to all reachable servers concurrently (eight at a time
by default). SSH uses `root@ip_vpn` with non-interactive, bounded connections.
A `report.tsv` under a private `/tmp/sermo-credentials-*` directory contains no
secret values.

## Fleet install orchestrator

`install_fleet.sh` is the fresh-install counterpart of `update_fleet.sh`, for
hosts that do not have Sermo yet. Per host it runs `remote_stage.sh`, fetches
the staged inventory, regenerates that host's configuration locally with
`generate_install_config.py` and applies it with `remote_apply.sh`, which
enables and starts `sermod` through the host's real init so the service survives
a reboot. When reinstalling an existing tree, `remote_stage.sh` keeps the dated
backup and restores its `/etc/sermo/credentials.env` as `root:root` mode `0600`
before generation, so the new config continues to use `web.password_file`.

```sh
scripts/remote-deploy/install_fleet.sh --hosts new-hosts.txt
scripts/remote-deploy/install_fleet.sh --dry-run web1 web2   # plan only
```

It records and skips unreachable or unhealthy hosts exactly like
`update_fleet.sh`. A host that already has `/etc/sermo` is reinstalled rather
than merged: `remote_stage.sh` overwrites `sermo.yml` and `remote_apply.sh`
replaces the generated directories. That is the intended path for a
configuration too old to normalize incrementally — back up `/etc/sermo` first.

## Fleet update orchestrator

`update_fleet.sh` drives a whole-fleet update from the local checkout: it
builds, prepares the payload once, then per host uploads it and runs
`remote_update_payload.sh` over SSH (as root). With `--with-config` it also
runs `remote_collect_inventory.sh` (read-only), regenerates that host's
configuration locally with `generate_install_config.py`, backs up `/etc/sermo`
to `/etc/sermo.backup.<run-id>` on the host and applies the generated tree with
`remote_apply.sh`. Regeneration replaces the generated config directories, so
review the run's `configs/<host>/` output before relying on it where hosts
carry manual tweaks.

```sh
scripts/remote-deploy/update_fleet.sh --with-config --hosts fleet.txt
# Include installed but stopped service profiles for an explicit lifecycle test.
# This does not enable or start those units by itself.
scripts/remote-deploy/update_fleet.sh --with-config --include-inactive-installed-services --hosts fleet.txt
# Restrict a lifecycle campaign to the explicitly authorized catalog services.
scripts/remote-deploy/update_fleet.sh --with-config --include-inactive-installed-services \
  --only-services acpid,rsync,snmpd,lm_sensors,lvm2-monitor,mdmonitor,smartd --hosts fleet.txt
scripts/remote-deploy/update_fleet.sh --dry-run web1 web2   # plan only
```

It processes the whole selected fleet in one pass (this manually launched
orchestrator does not apply the first-four-host gate below), records and skips
unreachable or unhealthy hosts, fetches failed hosts' `out.tar.gz` artifacts
into its run root, and cleans up the exact remote staging directories it
created on success.
Every remote SSH command is bounded to 25 minutes by default; set
`SERMO_REMOTE_COMMAND_TIMEOUT_SECONDS` to a positive number of seconds when a
known-slow host needs a different ceiling. Each daemon-start phase has a
separate `SERMO_READY_WAIT_SECONDS` limit of ten minutes by default, which
covers hosts with a large generated service/watch tree; the individual local
Web UI probes remain bounded by `SERMO_HTTP_TIMEOUT_SECONDS` (five seconds by
default), so a stuck collector or Web endpoint is recorded and the next host
continues.
`--include-inactive-installed-services` is deliberately opt-in: it adds every
installed catalog service to the generated Sermo configuration even if its init
unit is stopped. It never changes the unit's enabled or active state; use it
only when an operator needs Sermo to manage an explicitly authorized lifecycle
test or inactive-service audit.
`--only-services` narrows that generated catalog-service set to a comma-separated
allow-list of canonical profile names; it is suitable for a bounded lifecycle
campaign and does not change units outside that list.
`remote_collect_inventory.sh` mirrors `remote_stage.sh`'s read-only evidence
collection for already installed hosts — keep both in step.

All three collectors pin `LC_ALL=C`. Several of the tools they parse translate
their output: a Spanish host's `virsh domstate` reports a running domain as
`ejecutando`, which the running-domain filter read as stopped. Regenerating such
a host's configuration would have dropped every one of its VM services. The
generator now also reports an unrecognized domain state as a parse failure under
`skipped_vms` instead of silently calling it stopped.

Both collectors also record active `tmux` and GNU `screen` namespaces. For
`tmux`, evidence is one real `/tmp/tmux-<uid>/<socket>` socket whose owner maps
to a local account; for `screen`, it is an owned socket below a known runtime
root (`/run/screen`, `/var/run/screen` or GNU Screen's `/tmp/screen` fallback).
When the SSH catalog service is generated, each discovered namespace becomes a
read-only `terminal_sessions` service watch with an absolute binary, explicit
user and, for `tmux`, absolute socket. The checks use `reports: state`, appear
in the SSH service detail, and never attach to or control a terminal session.
Installed multiplexers without an active, safely attributable namespace do not
produce a check.

## Per-host overrides that survive regeneration

Every generated config directory may have a `<dir>.local` sibling —
`/etc/sermo/services.local`, `storages.local`, and so on. Sermo loads them and
merges each document onto the generated one of the same name, so a host tunes a
packaged threshold once and keeps it:

```yaml
# /etc/sermo/services.local/prometheus.yml
name: prometheus
watches:
  alert-if-io-high:
    check: { value: 838860800 }
```

`remote_apply.sh` deliberately leaves those directories out of its `rm -rf`, and
`remote_stage.sh` — which moves the whole tree aside on a reinstall — restores
them from the backup alongside `credentials.env` and `templates/`. The layer is
discovered from the directory layout, not from `paths` in `sermo.yml`, precisely
so that regenerating `sermo.yml` cannot de-register it. See
[docs/configuration.md](../../docs/configuration.md#per-host-overrides-dirlocal).

## Web credentials

The fresh-install orchestrator takes the admin password from
`SERMO_WEB_PASSWORD` (default `sermo-remote-admin`) and hands it to
`generate_install_config.py`. The update orchestrator requires the existing
`/etc/sermo/credentials.env` on every selected host and refers to that file
only: it never transports the password in SSH command arguments, generated
configuration, backups or reports.

Before generating, each host is probed for `/etc/sermo/credentials.env`. When it
is there, the generated `sermo.yml` gets `password_file:` pointing at that file
instead of a `password:` line holding a second copy of the secret. A host that
already manages its own credentials keeps managing them, and the copy that would
otherwise travel into config backups and paste buffers never exists. A host
without the file still gets the literal password, so a first install needs no
preparation.

During an update or config apply, the remote readiness and Web UI checks prefer
the running daemon's owner-only `<paths.runtime>/web.token`. They therefore keep
working when an existing host uses a rotated credential file or hashed web
credentials; the fresh-install password remains the fallback before a daemon
token exists.

`password` and `password_file` are mutually exclusive in `sermo.yml`; the
generator emits exactly one of them.

## Fleet install and update failure handling

The first-four-host gate applies only to workflows that install, update, apply
configuration, start a daemon or otherwise mutate remote Sermo state. Read-only
inventory, event collection and event analysis may inspect the whole selected
fleet in one pass; they do not pause after four hosts. Record any SSH failure or
Sermo finding, but do not use a read-only finding as a reason to stop collecting
the remaining hosts' evidence.

For a fleet run, record and skip a host that cannot be reached, cannot execute
from `/tmp`, lacks temporary disk space or inodes, or has a pre-existing local
configuration/service problem. Continue with the next host; do not delete data,
alter storage or relax validation to force it through. The final report must
include the host, phase, command evidence, whether it changed state and the safe
remediation. For example: `kvm5 — preflight: /tmp is on /dev/root with 0 bytes
available; skipped before upload; free space before retrying`.

Stop the fleet only for a Sermo blocker: a defect reproducible from the same
binary, catalog, generated configuration or deployment script, an invalid Sermo
artifact, or a protected-path metadata change. Fix and validate that defect
locally before redeploying every host already touched.
- `remote_update_binary_catalog.sh` refreshes only `sermoctl`, `sermod` and the
  packaged catalog. It snapshots `/etc/sermo`, rejects payloads containing any
  other path, and rolls back the binaries and catalog if validation, restart or
  authenticated Web UI checks fail.
- `remote_normalize_retired_engine_keys.sh` removes only the retired
  `engine.max_parallel_operations` key from an existing `/etc/sermo`, so a host
  configured before the global operation semaphore was removed validates against
  the current binary. It edits that key only inside a top-level `engine:` block,
  validates with the candidate `sermoctl` and restores `/etc/sermo` if validation
  fails. Run it *before* `remote_update_payload.sh` on hosts configured before
  the removal: their `/etc/sermo` fails validation against the current binary,
  which blocks the update.
  Hosts whose configuration carries further retired keys (for example
  `paths.catalog`) are too old to normalize incrementally — back up `/etc/sermo`
  and reinstall them through `remote_stage.sh` + `remote_apply.sh` instead.
- `remote_normalize_retired_umount_keys.sh` removes only the retired
  `mount.umount.allow_lazy: false` and `allow_sigkill: false` keys from existing
  YAML. It rejects non-false values, validates with the candidate binary and
  restores `/etc/sermo` if validation fails.
- `remote_update_network_watches.sh` refreshes only `/etc/sermo/networks` from
  a generated payload. It rejects any other archive member, validates the
  retained configuration, restarts `sermod`, and restores the prior network
  watches when validation or restart fails.
- `remote_repair_catalog.sh` replaces only the packaged catalog from a payload.
- `remote_final_check.sh` validates `/etc/sermo`, service state, port `9797`,
  `/livez`, `/readyz`, the HTML shell and current protected-path metadata.
- `collect_endpoint_hints.sh` collects sanitized endpoint hints for already
  installed hosts without replacing `/etc/sermo`.
- `collect_runtime_targets.sh` collects Docker containers and libvirt/QEMU
  domains for already installed hosts without replacing `/etc/sermo`.

Remote payload/config extraction must never preserve local workstation
ownership onto system paths. Payload tarballs are written with numeric
`root:root` ownership, remote extraction uses `tar --no-same-owner`, and the
remote scripts extract only the payload members needed for the detected init
backend. Do not add archive entries for protected parent directories such as
`/`, `/etc`, `/usr`, `/usr/lib`, `/etc/systemd`,
`/usr/lib/tmpfiles.d`, `/etc/init.d` or `/usr/share`; extracting those entries
as root can rewrite host metadata. Each mutating remote script records
`protected_path_metadata.before`, `protected_path_metadata.after` and
`protected_path_metadata.diff`, and exits with status `70` if any protected path
changes type, mode, uid or gid.

The generated config defaults to monitoring installed catalog services whose
init unit is active **or failed** — a failed unit is installed, enabled and
broken, so excluding it would blind the monitoring to precisely the services
that need it. The narrow exception is an OpenRC `epmd` unit reported as crashed
when every observed EPMD process is owned by RabbitMQ: it is not generated as a
separate control target because operating it could compete with RabbitMQ's EPMD;
repair `rabbitmq` instead.

The generated defaults also set `dry_run: true`, Web UI on `0.0.0.0:9797`, storage
free-space threshold `< 5%`, expansion by `5G`, fstab-backed non-root storage
mount units, running Docker containers, running libvirt/QEMU virtual machines,
SMART every `24h` and hdparm every `6h`. The hdparm buffered-read floor follows
the medium reported by `lsblk` — `20` MB/s for rotational disks, `100` MB/s for
flash — because one shared floor either alerts on every healthy HDD or never
fires on an SSD. Explicit zero-capacity whole disks (normally empty USB card
reader slots) are omitted from diskio, SMART and hdparm monitoring. The EDAC
watch likewise requires a numbered `mcN` controller, not only the empty EDAC
sysfs directory. LVM space and logged-in-user checks are disabled. The root
filesystem retains its storage-capacity watch but is not a mount unit. Use
`--include-inactive-installed-services` only for catalog audits where inactive
installed profiles are intentionally desired.

A container that is **not** running is generated when Docker was asked to keep it
alive and it exited non-zero — the container equivalent of a failed unit. The
container list API reports neither fact (its `HostConfig` carries only
`NetworkMode`), so the collectors add `docker_stopped.tsv`, one line per
non-running container as `<name> <status> <exit code> <restart policy>`. A
restart policy of `always`, `unless-stopped` or `on-failure` with a non-zero exit
is an outage; policy `no` is a one-off `docker run` and a zero exit is a
completed or deliberately stopped container. Without that evidence — no docker
CLI on the host, or a host staged before it was collected — the container is left
out rather than guessed at from the exit code, since a failed one-off is not a
service outage. Every decision is recorded under `containers` in
`config-report.json`.

A **thin arbiter** is generated as its own service on the host that serves it.
The arbiter of a replica 2 volume is neither a brick nor a peer: `gluster volume
status` never lists it and `volume info --xml` omits it, so a volume whose
arbiter was never started reports a perfectly healthy topology while the clients
log `Failed to lookup/create thin-arbiter id file` and the volume runs with no
split-brain protection. The collectors therefore stage the declaration from
`gluster volume info`'s text output, resolving the arbiter host on the target —
that name is usually internal to the storage network. When it resolves to one of
the host's own addresses, the `gluster-ta-volume` catalog service is generated
even though its unit is currently inactive, which is the same reasoning that
keeps a failed unit monitorable. The profile deliberately carries no endpoint
probe: the packaged unit binds port `24007`, the port glusterd itself listens on,
so a connect proves nothing wherever the two share a host. Each declaration,
local or remote, is recorded under `thin_arbiters` in `config-report.json`.

Every locally mounted, non-pseudo filesystem discovered from `findmnt` gets a
safe `storage` check with `mounted: true` and the free-space threshold. This
covers ext2/3/4, XFS, btrfs, vfat/exfat and the other local filesystem types in
the generator inventory without invoking filesystem repair tools. The generation
report records each selected path and filesystem type under `filesystems`.
Network filesystems remain mount-only checks, but every `nfs` or `nfs4` entry in
`/etc/fstab` also gets a native read-only NFS endpoint check against the server
named in its source. This catches a reachable-but-stale mount, DNS or routing
failure, and an unavailable NFS listener without mounting or touching the share;
the generated endpoints are listed under `nfs_endpoints` in the report. The
staging inventory resolves the NFS source and records its route, so the generated
probe binds to that egress interface when one is known. Every fstab-backed
network filesystem also receives its `mounted: true` watch even when it is not
currently listed by `findmnt`, so an already-failed mount remains visible.

A FUSE network client is spelled two ways for the same mount — `glusterfs` in
`/etc/fstab`, `fuse.glusterfs` in `findmnt` — and both classify as a network
filesystem. Matching the raw string alone let the `findmnt` spelling fall through
to the generic `fuse.*` local-storage rule, so a Gluster client mount was
generated as a capacity watch carrying an `expand` action that can never grow a
remote volume, and the watch kind flipped between hosts depending on whether the
mount happened to be up when the host was staged.

An entry mounted on demand rather than at boot — `noauto` or
`x-systemd.automount` in its fstab options — gets **no** `mounted: true` watch:
being unmounted is its configured state, so the watch would alert on operator
intent instead of on a fault. The decision is recorded under `skipped_watches`.
Its NFS endpoint check is still generated, because the server's reachability is
meaningful whether or not the share is currently mounted.

A `net-<iface>` watch is generated per non-loopback link that is both
administratively up and carrying a signal. A link the kernel flags `NO-CARRIER`
is skipped: its `state` metric alerts on the *unhealthy* value (`expect: down`),
so watching an already-down link would fire continuously. libvirt/QEMU `vnet`
taps are skipped too — they come and go with their guests, which are monitored
in their own right.

Every generated configuration includes tier 1 of the three-tier clock-drift
policy: an alert-only `watch-clock-drift`. It queries `time.cloudflare.com` and
`pool.ntp.org` every five minutes and alerts after two consecutive samples whose
wall-clock drift exceeds `1s`. It never corrects time.

Every generated configuration includes `watch-failed-units`, an alert-only count
of the init units the host reports as failed, naming them. Service monitoring only
covers the generated services, so a unit with no catalog profile — a site-local
backup job, a failed `.mount` or `.timer` — was invisible: on k2keu2
`backup_kvm.service` had been failed for days and the host reported `degraded`
while Sermo reported nothing. The watch names the host's real init backend rather
than `auto`, so the check does not re-detect the init system every cycle, and it
carries no remediation: restarting an arbitrary unit Sermo knows nothing about is
not a safe action. A unit that *does* have a generated service is reported twice
— once by its service check, once here — deliberately: the watch is the
systemd-level view, and excluding the generated units would make a unit's failure
invisible again the moment an operator unmonitors its service.

`watch-firewall-rules` is generated only when the init inventory reports an
active supported firewall manager (`firewalld`, `firehol`, `nftables`,
`iptables`, `ufw`, `shorewall`, `ferm`, or a persistent iptables loader). The
presence of `nft` or `iptables` binaries alone is not firewall evidence: those
tools are frequently installed as dependencies on hosts with no firewall policy.
When no supported manager is active, the generator omits the watch and records
the reason in `skipped_watches`.

Tier 2, `watch-clock-step`, is the forced correction at `5s`: Sermo asks the
local chronyd to step the clock over its Unix command socket, natively and with
no external process. It is generated **only** where it is both applicable and
safe, and the generation report records why it was left out otherwise:

- never on a host running any ceph daemon — a step is a discontinuity, not a
  slew, and a clock jump can cost a monitor its paxos quorum;
- only on a host whose time daemon is chrony. Neither ntpd nor
  systemd-timesyncd has a step command at all; on those hosts the forced
  correction is the `restart-if-clock-drifting` watch their catalog service
  already ships.

Like every generated watch it carries `dry_run: true`, so the step is reported
and not performed until the host is taken out of dry-run. Tier 3 (alert at `5ms`
for ceph nodes, never stepping) stays an opt-in example: `5ms` is site-specific
and noisy on clusters with worse hardware.

Every generated configuration also includes `watch-dead-letter`, an alert-only
size threshold on `/root/dead.letter`. `mail(1)`/`mailx` write an undeliverable
message body there, so a non-empty file means a cron job or a script tried to
send mail and it never left the host — silently, since the sender had already
exited. The threshold is edge-triggered, so the crossing is reported once rather
than every cycle, and again after a restart or a config reload, which re-arms the
watcher's baseline.

The collector also records credential-free process identity evidence
(`process_policy.tsv`): PID, real UID/account and resolved, deleted or unresolved
`/proc/<pid>/exe` path. It never stages command arguments. When it finds a running
`postgres` account with a reviewed distribution postmaster path, the generator
adds `security-user-postgres`: an alert-only `process_policy` watch with exact
binary paths. It deliberately does not learn arbitrary binaries from the host,
and it does not generate a policy for an account whose evidence is only
unreviewed paths. A deleted or unresolved executable remains an alert at runtime
until the operator resolves it; this generator never restarts PostgreSQL.

For every md array discovered in the staged `/proc/mdstat`, the generator writes
one individual `raid-<array>` watch. It watches degradation and exposes rebuild
progress, while `sysfs_changes: true` tracks the array's member `state`,
`errors`, `bad_blocks` and `mismatch_cnt`. The generated install remains
`dry_run: true` and has no hook or external notifier.

LVM space watches are disabled, even when `lvs` reports logical volumes. The
generation report records that exclusion under `skipped_watches`.

When `/usr/share/GeoIP` exists on a target, the generated configuration also
adds an alert-only recursive file watch. It reports each GeoIP database file
whose modification age exceeds `20` days (`older_than: 480h`); it has no hook
or external notification action. Its summary includes the observed age, limit
and number of regular database files scanned.

When endpoint hints are available, generated service files override catalog
`variables.host` and `variables.port` for Cloudflare Tunnel, BIND/named and
Prometheus MySQL Exporter. For catalog profiles whose process selector depends
on `variables.user`, the generator also overrides that user from the active
process owner, for example Cloudflare Tunnel packages that run as `root` instead
of `cloudflared`. When OpenRC exposes an active Cloudflare process whose
`/proc/<pid>/exe` target is marked `(deleted)`, the generator replaces the
runtime metrics selector with a narrow `cmd` selector for `cloudflared ... tunnel
run`; stop/kill safety still relies on the catalog's exact executable policy.
The generator prefers service config files such as
`/etc/cloudflared/config.yml`, BIND `listen-on` declarations and
`mysqld_exporter` `--web.listen-address`, then falls back to matching listening
sockets.

For active catalog services, the generator also cross-checks every profile
after applying the host's `os:` catalog selector, so distro-specific unit names
such as Ubuntu's `smartmontools.service` are not omitted. It then checks every
`tcp`, `http`, `dns` and `ports` watch against a listening socket owned by that
service's process. A matching endpoint keeps the profile watch. Without that
evidence the generated service explicitly disables that endpoint watch and
records the reason in `config-report.json`; it never turns unrelated listeners
into checks. Disabled source watches are removed before catalog resolution can
derive a check or remediation rule from them. HTTP and DNS therefore run only
for discovered active endpoints.

The same gate covers every **protocol probe** registered in `internal/conn`
(`ceph`, `fpm`, `statd`, `mountd`, `mysql`, `nut`, …), which dial a host:port
exactly like a `tcp` watch. Without it a profile probing `${host}` (`127.0.0.1`
by default) alerts forever wherever the service binds another address, listens
on a unix socket, or takes an rpcbind-assigned port — and for `ceph-mon` that
watch carries a `restart` action, so it proposed restarting healthy quorum
members. Two rules keep the gate from silencing real checks:

- A probe whose port cannot be resolved to a number is **not** gated. Absence of
  proof is not proof of absence, and disabling an unevaluated check would hide
  an outage.
- Evidence is protocol/host/port only; the owning process is not required. A
  profile's process key legitimately differs from the running binary (`mariadbd`
  vs `mysqld`) and kernel sockets (`nfsd`) report no process at all, so
  requiring a match turned "cannot attribute" into "proven absent".

A service whose unit has **failed** is generated with no endpoint gating at all:
it has no listening sockets by definition, and gating it would disable exactly
the checks that report the outage.

Exim's `tidy-callout-db-if-large` and `tidy-retry-db-if-large` are gated from
the hints databases themselves. Both run a SQLite query, but Exim writes SQLite
hints only when it was built for them; the ordinary build leaves Berkeley DB or
tdb at the same paths, where the query can only ever fail. The collectors record
`exim_hints`, one tab-separated line per hints file:

```
<path> <sqlite|sqlite-no-tblblob|other|absent|unknown>
```

The generator keeps each watch only where its file is SQLite and contains the
`tblblob` table queried by the watch. It disables incompatible files and records
the decision per service under `exim_hints_checks` in `config-report.json`. A
host staged before this fact was collected reports nothing; a file whose schema
could not be inspected reports `unknown`. In both cases the unevaluated watch is
left enabled rather than silently switched off.

The PostgreSQL replication watches are gated the same way, from cluster facts
instead of listening sockets. `remote_collect_inventory.sh` writes
`postgres_clusters`, one tab-separated line per running postmaster:

```
<datadir> <primary|standby> <slots> <walsenders>
```

(the separator is a literal tab, as in `nfs_routes`)

Every field comes from `/proc` and the data directory — the role from
`standby.signal`/`recovery.conf`, the slot count from `pg_replslot/` (a slot
exists on disk even with no consumer attached, which is the case that silently
retains WAL), the walsender count from the postmaster's children. No database
connection and no credentials are involved, so the script stays read-only.

The generator then keeps `alert-if-replication-slot-backlog`,
`alert-if-logical-slot-unconfirmed` and `alert-if-replication-slot-inactive`
only where a slot exists, `alert-if-replication-replay-lag` only on a primary
with a connected walsender, and `alert-if-standby-replay-delay` only on a
standby. The rest are written as `enabled: false`, and every decision — kept or
dropped, with its reason — is recorded per service under `replication_checks` in
`config-report.json`. A host with no running cluster gets all five disabled
rather than sensors that could never fire.
