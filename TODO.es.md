# Sermo TODO — mejoras futuras

Trabajo futuro movido fuera de `AGENTS.md` para que las instrucciones describan solo lo que
existe. Nada de lo que hay aquí es alcance comprometido; elige los elementos deliberadamente.

## Funcionalidades principales

- [ ] Modo de clúster distribuido
- [ ] Agentes remotos
- [ ] Autenticación de API remota
- [ ] RBAC multi-tenant
- [ ] ABI de plugins
- [x] Integraciones de notificación principales: email, Slack, Teams y plantillas
      de notificadores.
- [ ] Sinks de notificación adicionales como file, syslog, Discord y webhook
      genérico.
- [ ] Exportación de métricas de Sermo (endpoint de scrape Prometheus / OpenMetrics — distinto
      de *monitorizar* un servidor Prometheus; los sinks log/slog, archivo JSON, syslog y
      webhook están igualmente pendientes)
- [ ] API MCP o gRPC del servidor
- [ ] Integración con PolicyKit (polkit) más allá del catalog service básico
- [ ] Backend nativo de systemd D-Bus para el control de servicios (el backend basado
      en comandos funciona hoy)

## Integraciones y catálogo

### D-Bus, almacenamiento y escritorio

- [x] Sonda de salud del bus de sistema D-Bus (`type: dbus` en `internal/conn`) y
      catalog service `dbus` (servicio + check de handshake nativo)
- [x] UDisks2: sonda D-Bus nativa `udisks2` en el catalog service (Manager
      `Peer.Ping` + comprobación de name-owner); preflight `config` aún pendiente
- [x] catalog service `libvirt-dbus` (coincidencia de proceso; aún sin sonda de bus dedicada)

### Observabilidad

- [x] catalog service de servidor Prometheus (preflight `promtool check config`,
      sonda nativa de la API `prometheus`, reload SIGHUP)
- [x] Exporters de Prometheus en el catálogo (`node_exporter`, `mysqld_exporter`,
      `smartctl_exporter`)
- [ ] OpenTelemetry: exportar traces/métricas/logs desde el engine de Sermo (sink
      OTLP y/o checks nativos contra colectores OTLP — no es lo mismo que
      hacer scraping de Prometheus o monitorizar Alloy/Loki)
- [x] Daemon colector Grafana Alloy (preflight `alloy validate`)
- [x] Daemon Grafana Loki (preflight `-verify-config`)
- [x] Daemon InfluxDB (preflight `influxd config validate`)
- [x] catalog service de servidor Grafana (HTTP `/api/health`; aún sin preflight de config)

### Gestores de procesos y runtimes

- [x] PM2 (gestor de procesos de Node.js): catalog service + checks de preflight/
      health/postflight `pm2 ping`
- [x] catalog service Supervisor (`supervisord`) (health `supervisorctl status`,
      preflight opcional `supervisord check`)

## Catálogo — checks de preflight `config`

El lote ya aterrizó en el catálogo (gate de start/restart/reload):

- [x] Infra principal: `systemd`, `docker`, `firewalld`, `nginx`, `apache`, `ssh`,
      `named`, `dhcpd`, `dnsmasq`, `syslog-ng`, `monit`, `fetchmail`
- [x] Mail / seguridad: `dovecot`, `exim`, `rspamd`, `spamassassin`, `fail2ban`,
      `squid`, `proftpd`
- [x] Bases de datos / cachés con `preflight.config` offline: `mysql`
      (`--defaults-file` + `--validate-config`), `mariadb` (`--defaults-file` +
      `--help --verbose`), `postgres-%v` (`postgres --check`), `mongod`
      (`--outputConfig`)
- [ ] `preflight.config` de catálogo para `redis` / `keydb` (aún sin validador offline
      fiable disponible; existen checks en vivo y reglas de restart en el catálogo)
- [x] Backup: `bacula-*`, `bareos-*`
- [x] Observabilidad / túneles: `prometheus`, `alloy`, `loki`, `influxdb`,
      `filebeat`, `cloudflared`, `nebula`, `nebula-%i`
- [x] Otros: `php-fpm`, `slapd`, `smbd`, `nmbd`, `cups`, `varnishd`,
      `containerd`, `openvpn`

Aún falta `preflight.config` donde no existe un check offline fiable (ver
auditoría del catálogo / notas del autor de perfiles): la mayoría de los helpers de hardware, stacks JVM sin
una CLI configtest, `mosquitto`, `supervisord`, `udisks2`, `pm2`, etc. (`redis` /
`keydb` registrados arriba).

## Logging y auditoría

- [x] `access.log` (fase 1): `engine.access` JSONL append-only para tráfico web
      POST `/api/**` mutante y comandos `sermoctl` que cambian estado. Rotación y
      retención aún TODO.
- [x] `event.log` (fase 1): `engine.events` JSONL append-only que refleja los eventos
      del daemon junto al almacén SQLite. Rotación y retención aún TODO.
- [x] `diagnostics.log` (fase 1): snapshots programados `engine.diagnostics`
      (`engine.diagnostics_interval`, por defecto `1h`). Rotación y retención aún
      TODO.

## Engine y configuración

- [ ] Prioridades de servicio: `priority` configurable por servicio (entero o nivel
      con nombre), validación y valores por defecto; usar en el orden de remediación
      cuando varios servicios encolan acciones en el mismo ciclo; exponer en `sermoctl
      services` (orden/filtro), la tabla de servicios y el panel de detalle de la web UI, y
      el wizard de servicios.
- [ ] Acción de regla `exec`: no implementada. Si se planifica, añadir una constante
      de modelo `ActionExec`, validación, documentación y ejecución segura a través de `execx` —
      `then: {action: exec, command: [...], timeout: ...}` (forma de array, nunca una
      cadena de shell).
- [ ] Referencias de variable a variable (`variables.x: "${y}"`), con detección
  de ciclos. Hoy un valor de variable que contiene `${...}` es un error de validación.
- [ ] Watches de servicio — vista en vivo web: los `watches:` embebidos de un servicio
      se listan y controlan (monitor/unmonitor) en la web UI pero omiten el
      Meter/Lecturas en vivo, porque la ruta de vista en vivo web acotada al host
      no modela el árbol de PIDs del servicio. Cablear una vista en vivo acotada
      al servicio (reutilizando las deps `serviceRuntime` por servicio del web
      backend) para que sus gauges se rendericen como los watches de host.
- [ ] Watches de servicio — watch `process` acotado al árbol: el watch `process`
      con estado (condiciones cpu/memoria/io por PID y `kill`) se rechaza dentro
      de un servicio porque casa a nivel de host por nombre/usuario y podría matar
      procesos ajenos al servicio. Añadir una variante acotada al árbol de PIDs
      (restringir el matching y cualquier kill al conjunto de procesos descubierto
      del servicio) para ofrecerlo con seguridad; hoy usa `process_count`/`metric`
      para monitorización de procesos acotada al servicio.
