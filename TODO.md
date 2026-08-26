# Sermo TODO — future improvements

Future work moved out of `AGENTS.md` so the instructions describe only what
exists. Nothing here is committed scope; pick items deliberately.

## Version 1.0 release gate

The supported 1.0 scope is a single-host Linux supervisor: daemon, CLI and Web
UI; systemd and OpenRC; configured services and host watches; catalog-backed
configuration; persistent history; notifications; and safe, auditable service
operations. Distributed coordination, remote agents, multi-tenant RBAC, a
plugin ABI and cross-target dependency orchestration are post-1.0 work.
"Fully functional for 1.0" means every path inside that boundary passes the
release gate, not that every future integration in this file has landed.

- [x] Core product baseline: configuration loading and validation, catalog
      resolution, service/watch monitoring, persistence and SLA, CLI/Web
      representation, notifications, systemd/OpenRC control and the guarded
      operation engine are implemented and covered by repository tests.
- [x] Automated quality baseline: `make check` owns formatting, static and
      security analysis, dependency/YAML/Markdown validation, unit/integration
      tests, Web UI and WCAG checks, and the safety-package coverage floor; CI
      also runs the race detector, bounded fuzzing and CodeQL.
- [ ] Freeze and document the public 1.0 contract: supported distributions,
      init systems and architectures; privilege and remote-Web/TLS model;
      configuration, CLI and Web API stability; state-database migration;
      deprecation policy; and explicitly unsupported boundaries.
- [ ] Build a reproducible release path from a clean signed tag: generate the
      selected-platform binaries plus catalog, examples and systemd/OpenRC
      assets; make both binaries report the tag; publish release notes,
      checksums, an SBOM and signature/provenance; and verify installation from
      the published artifacts rather than from the checkout.
- [ ] Pass the install/upgrade/rollback acceptance matrix: staged `DESTDIR`
      packaging, fresh install, upgrade with real configuration and state,
      candidate-validation failure, readiness-failure rollback, reboot and
      non-destructive uninstall on representative systemd and OpenRC hosts.
      Preserve credentials, persistent state and every operator-owned `.local`
      override in all applicable paths.
- [ ] Pass a release-candidate fleet campaign using the staged-host workflow:
      pilot first, then every reachable host; validate candidate binary and
      catalog together; exercise only explicitly authorized service lifecycles;
      verify CLI, Web liveness/readiness/authentication and notifications
      without executing hooks; then complete a daemon restart/reboot soak with
      no unexplained failure, alert storm or unsafe repair.
- [ ] Close the operations and security handoff: document configuration/state
      backup and restore, host log rotation for optional append-only logs,
      reverse-proxy TLS deployment, security-reporting contact, upgrade and
      rollback procedure, troubleshooting and known limitations in both
      languages; verify installed owners, modes and runtime/state directories.
- [ ] Publish 1.0 only from an exact commit whose local `make check`, GitHub CI,
      CodeQL, race and fuzz jobs are green, whose release-blocking issues are
      closed, and whose clean-tree artifact hashes and fleet evidence are
      archived. At the time this gate was written, `main` was green but the
      repository had no release tag or published release.

## Major features

- [ ] Distributed cluster mode
- [ ] Remote agents
- [x] Remote web API authentication (HTTP Basic admin/guest roles, CSRF on
      mutations, loopback by default and TLS reverse proxy for remote access)
- [ ] Multi-tenant RBAC
- [ ] Plugin ABI
- [x] Core notification integrations: email, Slack, Teams and notifier
      templates.
- [ ] Additional notification sinks such as file, syslog, Discord and generic
      webhook.
- [ ] Sermo metrics export (Prometheus / OpenMetrics scrape endpoint — distinct
      from *monitoring* a Prometheus server; log/slog, JSON file, syslog and
      webhook sinks are likewise pending)
- [ ] Server MCP or gRPC API
- [ ] PolicyKit (polkit) integration beyond basic catalog service
- [ ] Native systemd D-Bus backend for service control (the command-based
      backend works today)

## Integrations and catalog

### D-Bus, storage and desktop

- [x] Generic D-Bus bus and named-object health probe (`type: dbus`) with
      constrained `peer`, `introspect` and scalar `property` modes, without
      service auto-activation; available to host and service watches.
- [x] Catalog coverage includes systemd managers, NetworkManager, firewalld,
      TuneD, desktop/hardware services, systemd-logind, UDisks2, libvirt-dbus,
      Polkit and UPower; UDisks2 preflight `config` remains pending.

### Observability

- [x] Prometheus server catalog service (`promtool check config` preflight,
      native `prometheus` API probe, SIGHUP reload)
- [x] Prometheus exporters in catalog (`node_exporter`, `mysqld_exporter`,
      `smartctl_exporter`)
- [ ] OpenTelemetry: export traces/metrics/logs from the Sermo engine (OTLP
      sink and/or native checks against OTLP collectors — not the same as
      scraping Prometheus or monitoring Alloy/Loki)
- [x] Grafana Alloy collector daemon (`alloy validate` preflight)
- [x] Grafana Loki daemon (`-verify-config` preflight)
- [x] InfluxDB daemon (`influxd config validate` preflight)
- [x] Grafana server catalog service (HTTP `/api/health`; no config preflight yet)

### Process managers and runtimes

- [x] PM2 (Node.js process manager): catalog service + `pm2 ping` preflight/
      health/postflight checks
- [x] Supervisor (`supervisord`) catalog service (`supervisorctl status` health,
      optional `supervisord check` preflight)

## Catalog — preflight `config` checks

Batch already landed in the catalog (start/restart/reload gate):

- [x] Core infra: `systemd`, `docker`, `firewalld`, `nginx`, `apache`, `ssh`,
      `named`, `dhcpd`, `dnsmasq`, `syslog-ng`, `monit`, `fetchmail`
- [x] Mail / security: `dovecot`, `exim`, `rspamd`, `spamassassin`, `fail2ban`,
      `squid`, `proftpd`
- [x] Databases / caches with offline `preflight.config`: `mysql`
      (`--defaults-file` + `--validate-config`), `mariadb` (`--defaults-file` +
      `--help --verbose`), `postgres-%v` (`postgres --check`), `mongod`
      (`--outputConfig`)
- [ ] `redis` / `keydb` catalog `preflight.config` (no reliable offline validator
      shipped yet; live checks and restart rules exist in catalog)
- [x] Backup: `bacula-*`, `bareos-*`
- [x] Observability / tunnels: `prometheus`, `alloy`, `loki`, `influxdb`,
      `filebeat`, `cloudflared`, `nebula`, `nebula-%i`
- [x] Other: `php-fpm`, `slapd`, `smbd`, `nmbd`, `cups`, `varnishd`,
      `containerd`, `openvpn`

Still missing `preflight.config` where no reliable offline check exists (see
catalog audit / profile-author notes): most hardware helpers, JVM stacks without
a configtest CLI, `mosquitto`, `supervisord`, `udisks2`, `pm2`, etc. (`redis` /
`keydb` tracked above).

## Logging and audit

- [x] `access.log` (phase 1): `engine.access` append-only JSONL for mutating web
      POST `/api/**` traffic and state-changing `sermoctl` commands. Rotation and
      retention still TODO.
- [x] `event.log` (phase 1): `engine.events` append-only JSONL mirroring daemon
      events alongside the SQLite store. Rotation and retention still TODO.
- [x] `diagnostics.log` (phase 1): `engine.diagnostics` scheduled snapshots
      (`engine.diagnostics_interval`, default `1h`). Rotation and retention still
      TODO.

## Engine and config

### Post-1.0 dependency and maintenance semantics

This initiative is deliberately outside the 1.0 gate. It must model why a
target cannot be observed without hiding the provider's real failure or
allowing a dependency declaration to control another target implicitly.

- [ ] Add one canonical cross-target dependency graph for configured services
      and host watches. Resolve it once when loading configuration, reject
      unknown targets, self-references and cycles, and keep the resolved graph
      immutable and cheap to consult during daemon cycles. Do not overload a
      check's existing intra-target `requires` ordering or lifecycle
      `also_apply` propagation with this different contract.
- [ ] Define dependency availability independently from target health. When a
      required provider is intentionally unavailable or cannot supply evidence,
      expose the dependent as `blocked` with a reason and provider link, publish
      synthetic skipped check results, create an SLA gap, and suppress the
      dependent's alerts and remediation. `blocked` must never mean healthy;
      the upstream provider remains the single root failure and notification.
- [ ] Add provider adapters for the known families and audit equivalent ones:
      Docker/containerd to containers; libvirt's monolithic or modular daemons
      to virtual machines and virtual networks; system/session D-Bus to named
      bus probes; network, route, DNS or VPN providers to endpoint checks; and
      storage chains such as iSCSI, multipath, crypt, LVM and remote mounts.
      Mount resolution must follow the actual fstab/unit/protocol dependency:
      an NFSv4 client does not require rpcbind, and a remote NFS mount does not
      imply a dependency on a local NFS server.
- [ ] Represent planned manual maintenance explicitly. Actions made through
      Sermo reuse operation settling and persisted monitor state. External
      `systemctl`, `rc-service` or hypervisor/runtime actions cannot be guessed
      safely, so provide a bounded maintenance lease/lock with owner, reason,
      scope, expiry and audit trail. An expired or ambiguous lease fails closed
      to normal observation rather than silently muting a real outage.
- [ ] Reconcile activation-driven units with manual stop. Socket-, D-Bus- or
      path-activated services may become active again while Sermo still records
      the operator pause. Model activation units explicitly where safe and/or
      resume observation when authoritative backend evidence proves reactivation;
      never stop a trigger or provider merely because a dependent names it.
      This includes rpcbind, polkit, avahi/acpid and exporter-style services.
- [ ] Make recovery conservative: when a provider or maintenance lease returns
      to available, run one observation-only cycle before alerts or automatic
      remediation become eligible. Preserve cooldowns, guards, operation locks
      and audit outcomes; dependency uncertainty must block mutation.
- [ ] Cover configuration, resolver, daemon, CLI and Web behavior with focused
      tests for systemd/OpenRC, Docker, libvirt, D-Bus, remote mounts, dependency
      fan-out, cycles, maintenance expiry and recovery. Document the state/SLA/
      alert semantics and add a fleet campaign that proves loss of a provider
      produces one root alert and no dependent repair storm.

### Other engine and config work

- [ ] Service priorities: configurable per-service `priority` (integer or named
      tier), validation and defaults; use in remediation ordering when multiple
      services queue actions in the same cycle; expose in `sermoctl services`
      (sort/filter), the web UI services table and detail panel, and the
      service wizard.
- [ ] `exec` rule action: not implemented. If scheduled, add an `ActionExec`
      model constant, validation, docs and safe execution through `execx` —
      `then: {action: exec, command: [...], timeout: ...}` (array form, never a
      shell string).
- [ ] Variable-to-variable references (`variables.x: "${y}"`), with cycle
  detection. Today a variable value containing `${...}` is a validation error.
- [x] Service watches — web live view: embedded `watches:` publish the same
      snapshot-derived `Meter`/`Readings` as host watches and remain controllable
      (monitor/unmonitor) in the web UI.
- [ ] Service watches — tree-scoped `process` watch: the stateful `process` watch
      (per-PID cpu/memory/io conditions and `kill`) is rejected inside a service
      because it matches host-wide by name/user and could kill processes outside
      the service. Add a PID-tree-scoped variant (constrain matching and any kill
      to the service's discovered process set) to offer it safely; today use
      `process_count`/`metric` for service-scoped process monitoring.
