---
name: sermo-remote-testing
description: >-
  Use when running safe Sermo validation or exploratory tests on remote Linux
  servers over SSH, using .env.ssh as the source of truth, local GOAMD64=v1
  builds, staged all-host runs, temporary /tmp validation, or explicit remote
  installations under /etc/sermo. Covers active-services-only config, unsupported
  active-service reports, all safely discoverable host watches, dry-run
  deployments, storage under 5 percent free with 5G expand, fstab-backed local/network/USB
  mount units, Docker containers, libvirt/QEMU virtual machines, SMART daily,
  hdparm every 6h, Web UI on 0.0.0.0:9797 with startup/readiness/access timing,
  reusable scripts under scripts/remote-deploy, and safe alert/notification
  checks that must not execute hooks or alter server behavior.
---

# Sermo Remote Testing

## Core Rules

- Build Sermo test binaries locally with `GOAMD64=v1` for broad x86_64 compatibility.
- Copy the locally built artifacts to remote servers and run them under `/tmp/`; never build on the remote server unless the user explicitly asks.
- Never install into `/usr`, `/etc`, `/var/lib`, `/run` or a service manager during remote validation-only runs.
- Install into `/etc/sermo`, `/usr`, `/var/lib`, `/run` or a service manager only when the user explicitly asks for remote installation, persistent configuration, or Web UI activation on the host. In that case follow the persistent installation workflow below instead of the temporary validation workflow.
- For validation-only runs, generate remote config as a temporary `config.yml` under the test directory, with runtime paths also under that directory.
- Use Sermo's own tools for the setup flow: `sermoctl` for validation/discovery/wizards and `sermod run` for the temporary daemon.
- Configure only services that are currently active on the remote server according to its init backend. Do not configure inactive services, stopped units, disabled candidates, volumes, interfaces, or VMs unless the user explicitly expands the scope for that run.
- If a code/catalog/schema change is required after any host has already been installed or configured, rebuild and redeploy the fixed payload/config to every already-touched host before continuing with new hosts. Do not leave earlier hosts on stale behavior.
- To expose the panel, set:

```yaml
web:
  address: 0.0.0.0
  password: "sermo-remote-admin"
  guest_password: "sermo-remote-readonly"
```

Keep the chosen `web.port` and auth settings explicit. Prefer `9797`; if it is already used by a verified `sermod` process, terminate that Sermo process and reuse `9797`.
When the requested run includes activating or exposing the Web UI, that request
is explicit permission to reclaim the chosen web port from another verified
`sermod` listener, including a non-temporary Sermo instance. Verify identity
before signaling, record what was stopped, and never kill a non-Sermo or
unverified listener.

- Preserve remote system behavior: do not start, stop, restart, reload, kill, package-install, or write permanent host config unless the user explicitly asks.
- Mutating remote install/update/apply scripts must never change type, mode, uid
  or gid on protected parent system paths: `/`, `/etc`, `/usr`, `/usr/lib`,
  `/etc/systemd`, `/usr/lib/tmpfiles.d`, `/etc/init.d` and `/usr/share`. Do not
  `chown`, `chmod`, extract archive directory entries, or preserve local
  workstation archive owners onto those paths. Capture before/after metadata for
  that exact list and fail the run if any entry changes.
- When problems are discovered, fix only the local project code, catalog, docs, or tests; redeploy new `/tmp` artifacts after the local change. Do not patch remote host files to make the test pass.
- If a serious error appears, stop the run, do not continue with the next destructive or state-changing step, and report the actions already performed.
- Treat broken basic commands (`cp`, `cat`, `ls`), failed remote shell startup, or an unexpected SSH disconnect during setup/validation as critical errors. Stop immediately and report what was already done.
- Never configure or execute `then.hook` during remote tests. Hooks can alter the server.
- To verify alert triggering without non-console side effects, prefer alert-only watches or target-level `dry_run: true` with a `notify` selection. Dry run logs/events what would happen and skips hook, expand, kill and non-wall notify delivery.

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
- When the user asks for the first `N` servers/hosts, use exactly the first `N`
  usable non-comment entries from `.env.ssh`.
- When the user asks to test **all servers**, **all hosts**, **the whole `.env.ssh` list**, or similar full-fleet scope, run in two stages:
  1. Test only the first four usable `.env.ssh` entries, end-to-end for the requested scope.
  2. Review the first-four results before continuing. Continue with the remaining entries only when the first stage has no critical failures: SSH/preflight works, `/tmp` setup works, generated config validates, requested one-shot checks pass, and no finding indicates a project/catalog bug that would invalidate the rest of the run.
- Apply the same first-four gate to any selected set larger than four hosts,
  including "first N" installation requests.
- If the first-four stage has a critical failure, stop before touching the remaining hosts and report the exact failing host, command phase, output path, and whether any remote `/tmp/sermo-remote-test-*` directory was created. Continue anyway only when the user explicitly asks to proceed despite the failure.
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
- Check that `/tmp` is writable and executable, including a tiny executable probe; if `/tmp` is `noexec`, stop for that host.
- Check free space in `/tmp`; stop for that host if there is not enough room for binaries, catalog, config, logs, and JSON outputs.
- Check whether port `9797` is free before choosing it for the web panel. If the
  run includes activating or exposing the Web UI and the port is occupied by
  another verified Sermo daemon, terminate that process and reuse `9797`,
  including when the listener is not under `/tmp`. If it is occupied by anything
  other than Sermo, do not touch it; choose another explicit high port and
  report it.

## Port 9797 Reuse

When `9797` is already listening:

- Identify the listener PID with `ss -ltnp` or an equivalent read-only command.
- Verify the process before signaling it: `/proc/<pid>/exe` or `/proc/<pid>/cmdline` must identify `sermod`.
- Prefer reclaiming temporary test instances whose command path or working
  directory is under `/tmp/sermo-remote-test-*`. When the requested scope
  includes activating or exposing the Web UI, do not fall back to another port
  solely because the verified `sermod` listener is non-temporary; stop that
  verified Sermo process and reuse `9797`.
- If the listener is a verified `sermod`, record PID, exe, cmdline, and log path when known, then send `SIGTERM` and wait briefly.
- If the same verified `sermod` PID is still alive after the graceful wait, send `SIGKILL` only to that PID and record it in the final report.
- If PID identity cannot be verified, or the listener is not Sermo, do not kill it; select another explicit high port and report why `9797` was not reclaimed.

## Remote Setup

For each host:

1. Create the `REMOTE_DIR` chosen during local preparation under `/tmp` on that
   host.
2. Copy `bin/sermod`, `bin/sermoctl`, and optional catalog archive into that
   directory.
3. Write `config.yml` in that directory. Minimal shape for catalog/app checks:

```yaml
paths:
  runtime: /tmp/sermo-remote-test-XXX/run

defaults:
  policy: { cooldown: 5m }

web:
  address: 0.0.0.0
  port: 9797
```

Use the real remote directory instead of `XXX`. Do not add a catalog path to the
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
- For ICMP/uplink checks, use `8.8.8.8` as the default check host unless the user or an existing generated config gives a different target.
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

Use this section for explicit remote installation or persistent configuration
requests. It overrides the validation-only `/tmp` restrictions above.

- Keep reusable install/generation scripts in `scripts/remote-deploy/`. Extend
  those scripts instead of rewriting one-off shell snippets when future installs
  need the same behavior.
- Build locally with `GOAMD64=v1` and `SERMO_DATADIR=/usr/share/sermo`, package
  `sermoctl`, `sermod`, units and catalog locally, then copy the payload to the
  host. Do not build on the host. Payload/config tarballs must be root-owned
  (`--owner=0 --group=0 --numeric-owner` or equivalent), must not contain
  protected parent directory entries (`/`, `/etc`, `/usr`, `/usr/lib`,
  `/etc/systemd`, `/usr/lib/tmpfiles.d`, `/etc/init.d`, `/usr/share`), and must
  be extracted remotely with `tar --no-same-owner`. Extract only members needed
  for the detected init backend; skip systemd/OpenRC/tmpfiles members whose
  parent directory is absent instead of creating a protected parent path.
- Stage read-only host evidence first: active init units, catalog service
  discovery, `findmnt`, `/etc/fstab`, `/proc/mounts`, `/proc/swaps`, `lsblk`,
  network inventory, cert candidates and feature probes.
- Generate the installed config from scratch under `/etc/sermo` when requested.
  List `/etc/sermo/watches`, `/etc/sermo/networks`, `/etc/sermo/storages` and
  `/etc/sermo/mounts` under `paths.watches`.
- Configure only catalog-supported services whose init unit is currently active.
  Record active services that cannot be represented instead of inventing service
  definitions.
- Include running Docker containers and running libvirt/QEMU virtual machines in
  complete persistent configurations. Generate them as controlled service
  documents under `paths.services`, not as generic host watches: Docker services
  use `category: docker`, `control.type: docker`, `control.container` and a
  read-only `type: docker` watch; VM services use `category: virtual-machine`,
  `control.type: libvirt`, `control.domain`, `uri: qemu:///system`, the detected
  local libvirt socket and a read-only `type: libvirt` watch. Keep them
  `dry_run: true`. Do not monitor stopped containers or shutoff domains unless
  the user explicitly asks for inactive targets; report them as skipped.
- Treat Docker and libvirt/QEMU discovery as a required persistent-install
  checklist item, not a best-effort afterthought. For every host, record running
  containers/VMs generated, stopped containers/VMs skipped, missing socket/tool
  evidence, and any duplicate generated service names that caused a target to be
  skipped. Do not mark the host complete until this is present in the report.
- For catalog services whose watches probe a local endpoint, derive
  host-specific `variables.host` and `variables.port` from the service's own
  configuration before falling back to catalog defaults. At minimum, discover
  Cloudflare Tunnel from `/etc/cloudflared/config.yml` `metrics:`, BIND/named
  from `listen-on` declarations matched against the host's real IPv4 addresses,
  and Prometheus MySQL Exporter from `--web.listen-address` in service config
  files. Use matching listening sockets only as a fallback. Do not assume
  `127.0.0.1` when the config or socket shows the service binds another local
  address.
- For catalog services whose process selector uses `variables.user`, derive the
  value from the active process owner when read-only `/proc` evidence is
  available. This is required for Cloudflare Tunnel on hosts where the package
  runs `cloudflared` as `root`: the endpoint check can pass while OpenRC hosts
  still stay in `collecting` if runtime process metrics cannot match the
  catalog default user.
- If an OpenRC Cloudflare Tunnel process reports `/proc/<pid>/exe` as a deleted
  binary, do not weaken operation kill safety. Override only the process-metrics
  selector with a narrow command-line regex for `cloudflared ... tunnel run`;
  leave stop/kill policy on the catalog exact executable guard so deleted or
  untrusted executables remain fail-safe for operations.
- Default every generated service and watch to `dry_run: true`. Before exposing
  manual Web UI controls for expand, mount, umount or mount-user alerts, verify
  those controls also respect target dry-run. If they do not, fix the project
  code first, add tests, rebuild and redeploy.
- Web UI defaults are `address: 0.0.0.0`, `port: 9797`, password
  `sermo-remote-admin`. Verify `/livez`, `/readyz`, HTML, `/api/services`,
  `/api/watches` and `/api/mounts` after apply.
- Measure and report daemon/Web UI speed on every apply or update: seconds from
  restart/start command completion until `/livez` succeeds, seconds until
  `/readyz` returns `ready:true`, and the HTTP response time/status for the HTML
  shell plus `/api/status` or equivalent Web UI API. If `/livez` is fast but
  `/readyz` is still false, wait at least one scheduler cycle before treating it
  as a failure, then report both the initial and final timings.
- Storage defaults are `free_pct: { op: "<", value: "5%" }`, `mounted: true`
  for mount-point paths, `then.expand: { by: 5G }`, `then.notify: [none]`,
  sustained `for: { cycles: 3 }`, and a conservative cooldown policy.
- Generate SMART watches every `24h` and hdparm watches every `6h` when the host
  exposes the corresponding tool/device safely.
- Generate mount units for every currently mounted, non-pseudo, fstab-backed
  target that can be represented safely:
  - Local storage targets use the same `storage-*` watch file under
    `/etc/sermo/storages` with an added top-level `mount:` block. This is what
    makes the Web UI Mount units panel show `/`, `/boot/EFI`, `/var/lib/...`,
    and similar local mount points without duplicate watches.
  - Network and USB/removable fstab targets may use separate `mount-*` watch
    files under `/etc/sermo/mounts` when keeping them mount-only avoids stale
    `statfs` behavior.
  - Never create mount units for pseudo filesystems, container/runtime mounts,
    transient bind mounts, unmounted fstab guesses, or paths not declared in
    `/etc/fstab`.
- After applying config, run `sermoctl config validate`, restart/enable `sermod`
  through the host init system, wait for `readyz.ready=true`, and report service
  count, watch file count, monitored target count, generated mount units and the
  protected-path metadata check result. A difference in protected path metadata
  is a critical failure; stop and report the before/after diff.

Complete means complete for the exact Sermo checkout and test binaries used in
that remote run, not a fixed hand-picked subset maintained in this skill. At the
start of every complete remote installation/configuration run, before generating
YAML, build a fresh host-watch inventory from the local checkout that will be
compiled and deployed. Inspect the watch builder, the central checks builder/type
registry, validation, examples and the host-watch docs; save the resulting
inventory in the local result directory. Use that inventory to drive remote
discovery and generation. Do not copy a static list from this skill into remote
setup scripts, and do not silently omit a discovered watch type because an older
run covered only a smaller legacy subset.

For each watch type found in that run-time inventory:

- Generate it when its target, predicates and thresholds can be derived safely
  from read-only host evidence or from an explicit user-selected target.
- **Availability gate:** do not generate, install or retain a host-watch YAML
  merely because Sermo supports its type. Require positive, read-only host
  evidence that its backing kernel interface, device, daemon, tool or data source
  is available and safe to probe. If that evidence is absent, omit the watch
  entirely; do not configure it just to report that the capability is unavailable.
  For example, create an `edac` watch only when the host exposes EDAC memory
  controllers under `/sys/devices/system/edac/mc`; otherwise record
  `edac: skipped — EDAC controllers not exposed` in the generation report.
- Skip it when the host lacks the feature, the target would be guessed, the probe
  requires privileges the run does not have, the watch is service-scoped rather
  than host-scoped, or the user did not authorize the necessary target. Record the
  concrete skip reason.
- Prefer Sermo wizards/helpers when they already know how to generate that watch
  type. Otherwise derive the minimal valid YAML from the same checked-out docs
  and validation rules used to build the inventory.
- Validate the generated config with the deployed `sermoctl`; if validation
  rejects a discovered watch type, fix the local project when it reveals a code
  or docs mismatch, or report the watch as skipped with the validation error.

Discover host resources with read-only probes chosen from the run-time inventory
and the corresponding docs/schema. The discovery script must be data-driven from
that inventory: adding a new host watch type to Sermo should require no edit to
this skill before remote installation runs start considering it.
- Generate one file per host watch under the matching temporary directory.
  Storage, network and generic watch directories are all listed under
  `paths.watches`; every file is a watch document with top-level `name:` plus
  the watch fields.
- Include baseline watches for every safely discoverable host resource on every
  complete config according to the run-time inventory. Do not use a hardcoded
  allow-list. For feature-dependent watches, generate entries only when the
  remote host exposes the required source data read-only; otherwise record the
  skip reason. Skip pseudo filesystems, bind mounts and transient
  container/runtime mounts unless the user explicitly asks for them.
- Every generated storage watch must alert when free space is below 5%. Put
  `free_pct: { op: "<", value: "5%" }` in the watch's `check:` block rather
  than an inverted `used_pct` threshold. For paths that are expected mount
  points, include `mounted: true` in that same `check:` block so an unmounted
  network or USB path alerts before `statfs` can report the parent filesystem.
  Do not configure `fstype`, `device` or `options` as predicates; they are
  result data only.

```yaml
name: storage-mnt-backup
category: storage
check:
  type: storage
  path: /mnt/backup
  mounted: true
  free_pct: { op: "<", value: "5%" }
```

  If real notification delivery is part of the requested remote installation,
  attach the selected notifier or inherit the configured global notify. If the
  run is only validating routing, use target-level `dry_run: true`; otherwise keep the
  storage watch alert-only or monitor-only according to the requested mode.
- Include `mount:` blocks for fstab-backed mount targets that are safe to expose.
  Detect them with read-only probes (`findmnt --fstab`, `/etc/fstab`, `lsblk`,
  `/dev/disk/by-*` and `/proc/self/mountinfo`); never mount or unmount during
  discovery. For mounted local storage targets, append `mount:` to the existing
  `storage-*` watch under `storages/` so the Storage and Mount units panels share
  the same target. For network and USB/removable fstab targets, write one
  storage watch file per target under a `mounts/` directory listed in
  `paths.watches` when mount-only handling is safer. Network candidates include
  NFS/NFS4, CIFS/SMB, SSHFS/fuse.sshfs, Ceph, GlusterFS and similar remote
  storage. USB candidates include removable devices or filesystems whose source
  resolves through USB/removable block devices. Keep the mount policy
  conservative:

```yaml
# <paths.watches>/mounts/mount-mnt-backup.yml  → watch
name: mount-mnt-backup
display_name: Backup mount
category: storage
check:
  type: storage
  path: /mnt/backup
  mounted: true
mount:
  refcount: true
  umount:
    allow_sigkill: false
    allow_lazy: false
```

  Do not write `source`, `fstype`, `options` or class metadata into the mount
  YAML. If a target is not present in `/etc/fstab`, report it as skipped instead
  of inventing a mount unit.
- Include certificate watches for `/etc/ssl` on every complete config. Discover
  only candidate certificate-like regular files (`*.crt`, `*.cer`, `*.pem`) that
  are immediate children of `/etc/ssl`, using read-only commands equivalent to
  `find /etc/ssl -maxdepth 1 -type f` with filename filters. The certificate
  check is non-recursive and must not descend into `/etc/ssl/certs`,
  `/etc/ssl/private` or any other subdirectory unless the user explicitly
  expands the scope for that run. Generate at most one watch per discovered
  direct file. Skip non-certificate private key material unless the user
  explicitly asks to monitor keys too.
  Missing, unreadable or unparseable files must be treated as alert findings.
  Expired certificates, not-yet-valid certificates and certificates with fewer
  than 15 days left must notify through the selected safe notification mode.
  Use `type: cert`, `path`, `expires_in_days: 15`, and optional stateful change
  checks:

```yaml
name: cert-etc-ssl-example
interval: 12h
check:
  type: cert
  path: /etc/ssl/example.pem
  expires_in_days: 15
  on_algorithm_change: true
  on_issuer_change: true
```

  Do not set `host`, `port`, `server_name` or `verify` for file-based certificate
  checks. Add `then.notify` with the selected notifier, or omit it to inherit a
  configured global notify, only when real delivery is part of the requested
  remote installation. If real notification delivery was not explicitly
  requested, keep these watches alert-only, `then.notify: [none]`, or
  target-level `dry_run: true` with the selected notifier.
- Prefer portable, conservative thresholds suitable for validation, not
  remediation, across every generated watch type. Choose the predicate names and
  event semantics from the run-time inventory, docs and validation code for that
  exact checkout. Do not add hooks. If notifications are requested only to test
  routing, use target-level `dry_run: true`.
- For complete configs that will be run under `sermod`, add monitor-only
  sustained host checks for the validation window across all discovered watch
  types that represent pressure, depletion or saturation. Apply `for: { cycles:
  10 }` with `interval: 30s` where the check is naturally a sustained level
  predicate. For edge-triggered or stateful change watches, keep their native
  event semantics instead of forcing a sustained `for` window. Do not fake
  absence-only watches when the check intentionally never fires without the
  underlying host feature; record absence in the observation report instead.
- Validate after host watches are added, then run one-shot checks or start the
  temporary daemon only after the full generated config passes.
- Report the run-time host-watch inventory, which concrete targets were
  generated for each discovered type, which discovered types were skipped, and
  why.

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

- Do not test start, stop, restart, reload, resume or signal operations on arbitrary remote services.
- When an operation test is required, use only `acpid` for start, stop, restart and reload paths.
- Skip `resume` operation tests unless a future explicitly supported control target has a real paused state.
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
- Clean only directories matching the exact remote prefix created for the run, e.g. `/tmp/sermo-remote-test-*`.
- Before removing a directory, verify it starts with `/tmp/sermo-remote-test-`.
- If cleanup fails due to DNS/SSH, report the host and remote directory path.

## Final Report

Summarize:

- hosts reached and hosts failed;
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
- direct-file `/etc/ssl` certificate findings: expiring within 15 days,
  expired, not yet valid, missing/unreadable/unparseable, issuer or algorithm
  changes, and any weak/obsolete-looking algorithm or key-size review notes;
- `acpid` operation tests run or skipped, with reason;
- missing paths, unsupported apps, or catalog gaps to fix locally;
- serious errors encountered and the actions completed before stopping;
- remote `/tmp` directories left behind, if any;
- protected-path metadata check status for `/`, `/etc`, `/usr`, `/usr/lib`,
  `/etc/systemd`, `/usr/lib/tmpfiles.d`, `/etc/init.d` and `/usr/share`;
- commands/tests run locally.
