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

## Performance — daemon startup and steady-state cost

Measured on 2026-09-06 across 62 fleet hosts (`sermod` 40abe372, default
`engine.interval: 30s`, 23-67 services + 30-88 watches per host), plus one
controlled host (23 services, 50 watches):

- Startup phases total 0.7 s median (max 2.5 s on healthy hosts; 15 s on one
  host with a slow disk). `build workers` (median 309 ms) and `load config and
  detect backend` (188 ms) dominate; `build web backend` reaches 0.9 s on
  several hosts. `/livez` answers in 1-3 s.
- `/readyz` takes 30-40 s on every host. That is not work: the scheduler
  spreads every target's first cycle over one whole `engine.interval`
  (`internal/app/scheduler.go`, `staggerOffset`) and readiness waits for the
  last one.
- Steady state: 0.3-1 % of one core (median 0.1 % over 3 h as the daemon
  reports itself; ~90 ms CPU per 30 s cycle on the controlled host), 100-150 MB
  RSS, 23-36 threads.
- The daemon writes **120-300 KB/s to disk continuously** (1.1 MB/s peaks),
  about 85 write syscalls per second, i.e. 10-25 GB/day per host, against a
  state database of 13-40 MB. `/proc/<pid>/io` on one host: 3.7 MB and 2 540
  write syscalls per 30 s cycle.
- `/api/dashboard` costs 0.5 s median per request (1.5 s on some hosts, 10 s on
  the slow-disk one) at 100-180 KB, versus 4-15 ms for `/api/watches`; on the
  controlled host it is 9 ms of CPU, so the fleet cost is I/O bound.

Work items, highest expected effect first. Each is a self-contained change
with tests; measure before and after with the same three numbers (write bytes
per cycle from `/proc/<pid>/io`, CPU per cycle, seconds to `/readyz`).

- [ ] Cut disk writes per cycle by an order of magnitude. Done: service and
      watch check snapshots are written only when a reading changes (upsert of
      the changed rows through the prepared-statement cache, delete of removed
      checks; `internal/app/snapshot_persist.go`), and sermod flushes the
      latest times on a clean stop. Left: checks whose data or message carries
      a live value (latency, load, memory, metric values) still change every
      cycle; batch every host-watch write (snapshot, metric, band, SLA,
      runtime state) into one transaction per cycle the way
      `cycleWriter.RecordCycle` does for services (`internal/app/watch.go`
      `RunCycle`); measure the result. A watch or rule window rewrites its
      runtime record every cycle while its condition holds: `within:`
      durations append a `TimedHistory` sample per true cycle, cycle-counted
      windows and the clear hold advance their counters
      (`internal/rules/window.go`); `for: {duration}` is stable. Give those a
      cheaper record or a rate limit.
- [ ] Make readiness real instead of a fixed interval: stagger first cycles by
      a bounded step (for example 100-250 ms per target, capped) or spread only
      app/library watches across their own 5 min interval, so `/readyz` flips
      after the first real cycle instead of after `engine.interval`. Keep the
      documented staggering guarantee in `docs/configuration.md` in step.
- [ ] One process sample per service per cycle. Done: `BackendPIDs` is one
      `process.CachedPIDs` per unit and generation (`control.TargetCache`),
      shared by the worker, web and metric-watch discoverers for
      `SystemFreshness`; operations still query live. Left: a `type: metric`
      service watch keeps its own per-PID counter sweep
      (`internal/app/daemon.go` `watchMetricSourceFactory`); it runs on its own
      goroutine and schedule, so it cannot read the worker's per-cycle memo,
      but it could share a time-bounded observation through
      `Collector.SampleServiceObserved` while keeping its own rate baselines.
- [ ] Use the shared caching `/proc` reader everywhere. Done: a host
      `type: process` watch and the alert-only `process_policy` watch, when at
      least as slow as `SystemFreshness`, sample identities from
      `deps.ProcReader` (kill re-checks stay live), and the `zombies` check
      counts the shared snapshot while it is fresh. Left: the
      `unowned_processes` watch, which has a kill path, still walks `/proc` per
      cycle through `procSamplerFromDeps`.
- [ ] One `systemctl` status query per cycle instead of one fork per service:
      the `service` check forks `systemctl is-active` (plus `show -p LoadState`
      when inactive) for every service every cycle
      (`internal/checks/servicecheck.go`, `internal/servicemgr/manager.go`).
      The web backend already caches this fleet-wide; give the daemon path the
      same single `list-units` refresh per cycle.
- [ ] Trim the live sampler. Done: one `metrics.ProcessObservation` per
      worker cycle shares each PID's `statm`, `io`, `fd/` and `task/` readings
      between `liveSampler` and the engine sampler. Left: `processEntryCount`
      uses `os.ReadDir`, which sorts thousands of fd entries just to count them
      (`internal/metrics/procfs.go`). Count with an unsorted read, reuse the
      thread count, and make the detail-only totals interval-bound.
- [ ] Fetch `CanReload` lazily instead of blocking the listener bind on it
      (`internal/app/webbackend.go` `attachServiceRuntime`). The worker and web
      runtimes stay separate (operation recording, session closers and reload
      support differ); their duplicated `systemctl` queries are shared through
      `control.TargetCache` (unit, process identity, backend PIDs).
- [ ] Run `BuildWatches` and `BuildArtifactWatches` through the existing
      `ForEachParallel` alongside `BuildWorkers`. Config resolution repeats
      per start (about seven service passes and nine watch passes) but was
      measured at about 2.5 ms per service pass and 0.3 ms per watch pass on a
      25-service, 55-watch host, about 20 ms per start, so it is not cached;
      a cache on `*Config` would also break the `from_file` refresh
      `ResolveServices` promises.
- [ ] Startup I/O on the single write connection: `applyMonitorModeFor`
      writes one row per service and per watch and `loadRuleState` reads two
      per service while eight wiring goroutines wait behind
      `SetMaxOpenConns(1)`; batch them into one read and one write. Delay the
      first state maintenance pass until readiness instead of running it at
      t=0.
- [ ] Dashboard request cost: memoize the per-watch `checkreadings` rendering
      by snapshot time so repeat polls do not rebuild every reading. The last
      event per service now reads `event_log_service_id_idx` per service
      instead of scanning 500 rows (the store, not the ring, is authoritative:
      sermoctl records events from its own process).
- [ ] Allocation trim on the hot path: store the joined cmdline on
      `process.Identity` instead of joining it for every PID × selector
      (`internal/process/discover.go`).
- [ ] Add a repeatable measurement: a `make` target or `sermoctl` diagnostic
      that reports write bytes per cycle, CPU per cycle and seconds to ready,
      so every item above lands with a before/after number.

## Major features

- [ ] Distributed cluster mode
- [ ] Remote agents
- [ ] Multi-tenant RBAC
- [ ] Plugin ABI
- [ ] Additional notification sinks such as file, syslog, Discord and generic
      webhook.
- [ ] Telegram `silent` for levels below warning.
- [ ] Dashboard severity filter for the event log and the watch list.
- [ ] `levels:` for latency thresholds (tcp, http, connection protocols) and
      for the capacity and temperature predicates of `lvm`, `storcli` and
      `ssacli`, whose verdicts are composite today.
- [ ] Sermo metrics export (Prometheus / OpenMetrics scrape endpoint — distinct
      from *monitoring* a Prometheus server; log/slog, JSON file, syslog and
      webhook sinks are likewise pending)
- [ ] Server MCP or gRPC API
- [ ] PolicyKit (polkit) integration beyond basic catalog service
- [ ] Native systemd D-Bus backend for service control (the command-based
      backend works today)

## Integrations and catalog

### Observability

- [ ] OpenTelemetry: export traces/metrics/logs from the Sermo engine (OTLP
      sink and/or native checks against OTLP collectors — not the same as
      scraping Prometheus or monitoring Alloy/Loki)

## Catalog — preflight `config` checks

- [ ] `redis` / `keydb` catalog `preflight.config` (no reliable offline validator
      shipped yet; live checks and restart rules exist in catalog)

Still missing `preflight.config` where no reliable offline check exists (see
catalog audit / profile-author notes): most hardware helpers, JVM stacks without
a configtest CLI, `systemd`, `openvpn`, `mosquitto`, `udisks2`, `grafana`, etc.
(`redis` / `keydb` tracked above).

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

- [ ] Hermetic tests — binary discovery: `internal/config` tests still probe the
      host's `/usr/bin`, `/usr/sbin` and `/usr/local/*` through `binDirSearch`
      and the version-template `Stat`/`Glob` discovery. Add one seam for those
      probes (defaulting to the host) so a test states which binaries exist;
      stubbing `binDirSearch` alone breaks ~90 assertions that rely on today's
      first-candidate fallback.
- [ ] Hermetic tests — remaining `/proc` defaults in `internal/app`:
      `daemon.go`, `procwatch.go`, `restartnotice.go` and `serviceruntime.go`
      still build `process.OSReader{}` when their caller injects nothing; route
      them through `Deps.ProcReader` so a test never reads the host process
      table by omission.

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
- [ ] Service watches — tree-scoped `process` watch: the stateful `process` watch
      (per-PID cpu/memory/io conditions and `kill`) is rejected inside a service
      because it matches host-wide by name/user and could kill processes outside
      the service. Add a PID-tree-scoped variant (constrain matching and any kill
      to the service's discovered process set) to offer it safely; today use
      `process_count`/`metric` for service-scoped process monitoring.
- [ ] Busy servers — follow-ups to the busy verdict (`internal/conn/busy.go`,
      `rules.Evaluator.EvalRule`):
      - MQTT 5 reason codes 0x88 (server unavailable), 0x89 (server busy) and
        0x97 (quota exceeded): the probe speaks MQTT 3.1.1, so only CONNACK
        code 3 is classified today.
      - Saturation that never answers looks dead: mosquitto at
        `max_connections` closes without a CONNACK, postfix with every smtpd
        busy leaves the connection in the backlog until it times out. Pair
        those probes with a connection-usage reading before trusting a restart.
      - `verify:` treats a busy check as a failed postflight; Redis `LOADING`
        right after a restart could instead count as settling until the
        operation deadline.
      - A busy badge in the dashboard's check list, next to unavailable.
      - Review the `restart-if-worker-thread-hot` CPU-saturation restarts the
        other ~20 catalog profiles still ship (apache, nginx, exim, squid,
        varnishd, …); redis, keydb and memcached already alert instead.
