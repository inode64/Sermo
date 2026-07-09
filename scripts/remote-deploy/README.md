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
- `remote_repair_catalog.sh` replaces only the packaged catalog from a payload.
- `remote_final_check.sh` validates `/etc/sermo`, service state, port `9797`,
  `/livez`, `/readyz`, and the HTML shell.
- `collect_endpoint_hints.sh` collects sanitized endpoint hints for already
  installed hosts without replacing `/etc/sermo`.
- `collect_runtime_targets.sh` collects Docker containers and libvirt/QEMU
  domains for already installed hosts without replacing `/etc/sermo`.

The generated config defaults to monitoring only installed catalog services
whose init unit is active, `dry_run: true`, Web UI on `0.0.0.0:9797`, storage
free-space threshold `< 5%`, expansion by `5G`, fstab-backed storage mount
units, running Docker containers, running libvirt/QEMU virtual machines, SMART
every `24h`, and hdparm every `6h`. Use
`--include-inactive-installed-services` only for catalog audits where inactive
installed profiles are intentionally desired.

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
