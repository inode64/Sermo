# Sermo TODO — future improvements

Future work moved out of `AGENTS.md` so the instructions describe only what
exists. Nothing here is committed scope; pick items deliberately.

## Major features

- [ ] Distributed cluster mode
- [ ] Remote agents
- [ ] Remote API authentication
- [ ] Multi-tenant RBAC
- [ ] Plugin ABI
- [ ] Complex notification integrations (email, Slack, Teams + templates;
      additional sinks like file/syslog/generic webhook still pending)
- [ ] Sermo metrics export (Prometheus / OpenMetrics scrape endpoint — distinct
      from *monitoring* a Prometheus server; log/slog, JSON file, syslog and
      webhook sinks are likewise pending)
- [ ] Server MCP or gRPC API
- [ ] PolicyKit (polkit) integration beyond basic catalog daemon
- [ ] Native systemd D-Bus backend for service control (the command-based
      backend works today)

## Integrations and catalog

### D-Bus, storage and desktop

- [x] D-Bus system bus health probe (`type: dbus` in `internal/conn`) and
      `dbus` catalog daemon (service + native handshake check)
- [ ] UDisks2: native probe and richer checks (catalog daemon exists today;
      only `service` check — no `udisks` / D-Bus object probe, no preflight
      `config`)
- [x] `libvirt-dbus` catalog daemon (process match; no dedicated bus probe yet)

### Observability

- [x] Prometheus server catalog daemon (`promtool check config` preflight,
      native `prometheus` API probe, SIGHUP reload)
- [x] Prometheus exporters in catalog (`node_exporter`, `mysqld_exporter`,
      `smartctl_exporter`)
- [ ] OpenTelemetry: export traces/metrics/logs from the Sermo engine (OTLP
      sink and/or native checks against OTLP collectors — not the same as
      scraping Prometheus or monitoring Alloy/Loki)
- [x] Grafana Alloy collector daemon (`alloy validate` preflight)
- [x] Grafana Loki daemon (`-verify-config` preflight)
- [x] InfluxDB daemon (`influxd config validate` preflight)
- [x] Grafana server catalog daemon (HTTP `/api/health`; no config preflight yet)

### Process managers and runtimes

- [ ] PM2 (Node.js process manager): catalog daemon, health checks, safe start/
      stop/restart integration
- [x] Supervisor (`supervisord`) catalog daemon (service check only; no
      `preflight.config` yet)

## Catalog — preflight `config` checks

Batch already landed in the catalog (start/restart/reload gate):

- [x] Core infra: `systemd`, `docker`, `firewalld`, `nginx`, `apache`, `ssh`,
      `named`, `dhcpd`, `dnsmasq`, `syslog-ng`, `monit`, `fetchmail`
- [x] Mail / security: `dovecot`, `exim`, `rspamd`, `spamassassin`, `fail2ban`,
      `squid`, `proftpd`
- [x] Databases / caches: `mysql` (`--defaults-file` + `--validate-config`),
      `mariadb` (`--defaults-file` + `--help --verbose`), `postgres-%v`
      (`postgres --check`), `mongod`, `redis` still pending (no offline validator)
- [x] Backup: `bacula-*`, `bareos-*`
- [x] Observability / tunnels: `prometheus`, `alloy`, `loki`, `influxdb`,
      `filebeat`, `cloudflared`, `nebula`, `nebula-%i`
- [x] Other: `php-fpm%v`, `slapd`, `smbd`, `nmbd`, `cups`, `varnishd`,
      `containerd`, `openvpn-%i`

Still missing `preflight.config` where no reliable offline check exists (see
catalog audit / profile-author notes): most hardware helpers, JVM stacks without
a configtest CLI, `redis` / `keydb`, `mosquitto`, `supervisord`, `udisks2`,
`pm2`, etc.

## Engine and config

- [ ] Service priorities: configurable per-service `priority` (integer or named
      tier), validation and defaults; use in remediation/operation ordering when
      multiple services compete for the global semaphore; expose in `sermoctl
      services` (sort/filter), the web UI services table and detail panel, and
      the service wizard.
- [ ] `exec` rule action: not implemented. If scheduled, add an `ActionExec`
      model constant, validation, docs and safe execution through `execx` —
      `then: {action: exec, command: [...], timeout: ...}` (array form, never a
      shell string).
- [ ] Variable-to-variable references (`variables.x: "${y}"`), with cycle
  detection. Today a variable value containing `${...}` is a validation error.
