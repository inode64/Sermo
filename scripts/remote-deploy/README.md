# Remote Deployment Scripts

These scripts support repeatable Sermo installations on remote hosts after a
local build. They are intentionally small wrappers around Sermo's own binaries:
stage read-only host inventory, generate one-file-per-target config, apply the
config under `/etc/sermo`, start `sermod`, and verify the Web UI.

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
- `remote_apply.sh` replaces generated config directories under `/etc/sermo`,
  validates the config, enables/restarts `sermod`, and verifies the local Web UI.
- `remote_update_payload.sh` refreshes binaries/catalog on an already configured
  host, validates the current `/etc/sermo` with the detected init backend, then
  restarts `sermod` and verifies the local Web UI.
- `remote_update_binary_catalog.sh` refreshes only `sermoctl`, `sermod` and the
  packaged catalog. It snapshots `/etc/sermo`, rejects payloads containing any
  other path, and rolls back the binaries and catalog if validation, restart or
  authenticated Web UI checks fail.
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

The generated config defaults to monitoring only installed catalog services
whose init unit is active, `dry_run: true`, Web UI on `0.0.0.0:9797`, storage
free-space threshold `< 5%`, expansion by `5G`, fstab-backed storage mount
units, running Docker containers, running libvirt/QEMU virtual machines, SMART
every `24h`, and hdparm every `6h`. Use
`--include-inactive-installed-services` only for catalog audits where inactive
installed profiles are intentionally desired.

Every generated configuration also includes an alert-only clock watch. It queries
`time.cloudflare.com` and `pool.ntp.org` every five minutes and alerts after two
consecutive samples whose wall-clock drift exceeds `3s`; it never corrects time.

When `/usr/share/GeoIP` exists on a target, the generated configuration also
adds an alert-only recursive file watch. It reports each GeoIP database file
whose modification age exceeds `20` days (`older_than: 480h`); it has no hook
or external notification action.

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
