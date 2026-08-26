# Sermo TODO — mejoras futuras

Trabajo futuro movido fuera de `AGENTS.md` para que las instrucciones describan solo lo que
existe. Nada de lo que hay aquí es alcance comprometido; elige los elementos deliberadamente.

## Gate de lanzamiento de la versión 1.0

El alcance soportado de 1.0 es un supervisor Linux de host único: daemon, CLI y
Web UI; systemd y OpenRC; servicios configurados y watches de host; configuración
respaldada por el catálogo; histórico persistente; notificaciones; y operaciones
de servicio seguras y auditables. La coordinación distribuida, los agentes
remotos, el RBAC multi-tenant, una ABI de plugins y la orquestación de
dependencias entre targets son trabajo post-1.0. «Completamente funcional para
1.0» significa que toda ruta dentro de ese límite pasa el gate de lanzamiento,
no que se haya implementado cada integración futura de este archivo.

- [x] Base funcional del producto: carga y validación de configuración,
      resolución del catálogo, monitorización de servicios/watches, persistencia
      y SLA, representación CLI/Web, notificaciones, control systemd/OpenRC y el
      motor de operaciones con guards están implementados y cubiertos por los
      tests del repositorio.
- [x] Base de calidad automatizada: `make check` posee el formato, análisis
      estático y de seguridad, validación de dependencias/YAML/Markdown, tests
      unitarios/de integración, Web UI y checks WCAG, y el suelo de cobertura de
      paquetes de seguridad; CI también ejecuta el detector de carreras, fuzzing
      acotado y CodeQL.
- [ ] Congelar y documentar el contrato público de 1.0: distribuciones, sistemas
      init y arquitecturas soportados; modelo de privilegios y Web remoto/TLS;
      estabilidad de configuración, CLI y Web API; migración de la base de datos
      de estado; política de obsolescencia; y límites explícitamente no soportados.
- [ ] Construir una ruta de release reproducible desde un tag limpio y firmado:
      generar los binarios de las plataformas elegidas junto al catálogo,
      ejemplos y assets systemd/OpenRC; hacer que ambos binarios informen el tag;
      publicar notas de release, checksums, un SBOM y firma/procedencia; y
      verificar la instalación desde los artefactos publicados, no desde el checkout.
- [ ] Pasar la matriz de aceptación de instalación/actualización/rollback:
      empaquetado por etapas con `DESTDIR`, instalación nueva, actualización con
      configuración y estado reales, fallo de validación del candidato, rollback
      por fallo de readiness, reinicio del host y desinstalación no destructiva
      en hosts representativos systemd y OpenRC. Preservar credenciales, estado
      persistente y cada override `.local` del operador en todas las rutas aplicables.
- [ ] Pasar una campaña de flota con un release candidate usando el flujo por
      hosts en etapas: primero piloto y después cada host alcanzable; validar
      juntos el binario candidato y el catálogo; ejercitar solo ciclos de vida de
      servicios autorizados explícitamente; verificar CLI, liveness/readiness/
      autenticación Web y notificaciones sin ejecutar hooks; completar después
      un soak de reinicio del daemon/host sin fallos sin explicar, tormentas de
      alertas ni reparaciones inseguras.
- [ ] Cerrar la entrega de operaciones y seguridad: documentar backup y restore
      de configuración/estado, rotación del host para logs append-only opcionales,
      despliegue TLS con proxy inverso, contacto para reportes de seguridad,
      procedimiento de actualización y rollback, diagnóstico y limitaciones
      conocidas en ambos idiomas; verificar propietarios, modos y directorios
      runtime/estado instalados.
- [ ] Publicar 1.0 solo desde un commit exacto cuyo `make check` local, GitHub CI,
      CodeQL y jobs de race y fuzz estén verdes, cuyos issues bloqueantes del
      release estén cerrados y cuyos hashes de artefactos con árbol limpio y
      evidencias de flota estén archivados. Cuando se escribió este gate, `main`
      estaba verde pero el repositorio no tenía tag de release ni release publicada.

## Funcionalidades principales

- [ ] Modo de clúster distribuido
- [ ] Agentes remotos
- [x] Autenticación de la API web remota (roles HTTP Basic admin/guest, CSRF en
      mutaciones, loopback por defecto y proxy inverso TLS para acceso remoto)
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

- [x] Sonda genérica de salud del bus y de objetos D-Bus con nombre (`type:
      dbus`) con modos limitados `peer`, `introspect` y `property` escalar, sin
      autoactivar servicios; disponible para watches de host y de service.
- [x] La cobertura de catálogo incluye gestores systemd, NetworkManager,
      firewalld, TuneD, servicios de escritorio/hardware, systemd-logind,
      UDisks2, libvirt-dbus, Polkit y UPower; el preflight `config` de UDisks2
      sigue pendiente.

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

### Semántica post-1.0 de dependencias y mantenimiento

Esta iniciativa queda deliberadamente fuera del gate de 1.0. Debe modelar por
qué no se puede observar un target sin ocultar el fallo real del proveedor ni
permitir que una declaración de dependencia controle otro target implícitamente.

- [ ] Añadir un único grafo canónico de dependencias entre targets para servicios
      configurados y watches de host. Resolverlo una vez al cargar la
      configuración, rechazar targets desconocidos, autorreferencias y ciclos, y
      mantener el grafo resuelto inmutable y barato de consultar durante los
      ciclos del daemon. No sobrecargar con este contrato distinto el orden
      `requires` interno de los checks ni la propagación de ciclo de vida
      `also_apply`.
- [ ] Definir la disponibilidad de dependencias por separado de la salud del
      target. Cuando un proveedor requerido no esté disponible intencionadamente
      o no pueda aportar evidencia, exponer el dependiente como `blocked` con
      motivo y vínculo al proveedor, publicar resultados sintéticos de checks
      omitidos, crear un hueco de SLA y suprimir alertas y remediación del
      dependiente. `blocked` nunca debe significar sano; el proveedor upstream
      sigue siendo el único fallo raíz y la única notificación.
- [ ] Añadir adaptadores de proveedor para las familias conocidas y auditar las
      equivalentes: Docker/containerd hacia contenedores; daemons monolíticos o
      modulares de libvirt hacia máquinas y redes virtuales; D-Bus de sistema o
      sesión hacia sondas de bus con nombre; proveedores de red, ruta, DNS o VPN
      hacia checks de endpoint; y cadenas de almacenamiento como iSCSI,
      multipath, crypt, LVM y montajes remotos. La resolución de montajes debe
      seguir la dependencia real de fstab/unidad/protocolo: un cliente NFSv4 no
      necesita rpcbind y un montaje NFS remoto no implica depender de un servidor
      NFS local.
- [ ] Representar explícitamente el mantenimiento manual planificado. Las acciones
      hechas mediante Sermo reutilizan el settling de operaciones y el estado de
      monitorización persistido. Las acciones externas de `systemctl`,
      `rc-service` o del hipervisor/runtime no se pueden adivinar con seguridad,
      así que hay que proporcionar un lease/lock de mantenimiento acotado con
      propietario, motivo, alcance, vencimiento y auditoría. Un lease vencido o
      ambiguo falla cerrado hacia la observación normal en vez de silenciar una
      caída real.
- [ ] Reconciliar unidades con activación y stop manual. Los servicios activados
      por socket, D-Bus o path pueden volver a estar activos mientras Sermo aún
      registra la pausa del operador. Modelar explícitamente las unidades de
      activación donde sea seguro y/o reanudar la observación cuando evidencia
      autoritativa del backend pruebe la reactivación; nunca detener un disparador
      o proveedor solo porque lo nombre un dependiente. Esto incluye rpcbind,
      polkit, avahi/acpid y servicios tipo exporter.
- [ ] Hacer conservadora la recuperación: cuando un proveedor o lease de
      mantenimiento vuelva a estar disponible, ejecutar un ciclo solo de
      observación antes de permitir alertas o remediación automática. Preservar
      cooldowns, guards, locks de operación y resultados de auditoría; la
      incertidumbre de dependencias debe bloquear la mutación.
- [ ] Cubrir configuración, resolver, daemon, CLI y Web con tests focalizados para
      systemd/OpenRC, Docker, libvirt, D-Bus, montajes remotos, fan-out de
      dependencias, ciclos, vencimiento de mantenimiento y recuperación.
      Documentar la semántica de estado/SLA/alertas y añadir una campaña de flota
      que demuestre que perder un proveedor produce una alerta raíz y ninguna
      tormenta de reparación en los dependientes.

### Otros trabajos de engine y configuración

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
- [x] Watches de servicio — vista en vivo web: los `watches:` embebidos publican
      los mismos `Meter`/Lecturas derivados de snapshots que los watches de host
      y siguen siendo controlables (monitor/unmonitor) en la web UI.
- [ ] Watches de servicio — watch `process` acotado al árbol: el watch `process`
      con estado (condiciones cpu/memoria/io por PID y `kill`) se rechaza dentro
      de un servicio porque casa a nivel de host por nombre/usuario y podría matar
      procesos ajenos al servicio. Añadir una variante acotada al árbol de PIDs
      (restringir el matching y cualquier kill al conjunto de procesos descubierto
      del servicio) para ofrecerlo con seguridad; hoy usa `process_count`/`metric`
      para monitorización de procesos acotada al servicio.
