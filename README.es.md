# Sermo

**Sermo es un supervisor de servicios portable y con la seguridad primero para
hosts Linux.** Se sitúa por encima de **systemd** y **OpenRC**, valida un
servicio antes de actuar sobre él, entiende los procesos *reales* del servicio, y
aplica reglas de remediación **con salvaguardas** — nunca reinicia a ciegas ni
mata el proceso equivocado.

Donde un sistema de init responde "¿está activa la unidad?", Sermo responde las
preguntas operativas difíciles: *¿está el servicio realmente sano, es seguro
tocarlo ahora mismo y, si no está sano, cuál es la acción segura?* Monitoriza,
diagnostica, remedia bajo invariantes estrictos, guarda historial de
disponibilidad (SLA), vigila recursos a nivel de host, envía notificaciones y
sirve un dashboard web en vivo — todo desde un único daemon.

## Por qué Sermo

Un supervisor de "reiniciar al fallar" simple es peligroso en un host real:
reinicia durante un backup, mata un proceso que casualmente comparte nombre de
binario, o actúa sobre un servicio cuya config está rota y empeora la caída.
Sermo se construye sobre el principio opuesto — **demostrar que es seguro y luego
actuar**:

- **Con salvaguardas, nunca a ciegas.** Cada start/stop/restart/reload/resume/pause,
  además de la reparación manual de un pidfile obsoleto, pasa por un único engine
  de operación que comprueba antes el preflight, los guards y los locks de
  runtime. Una acción bloqueada no se ejecuta.
- **Conoce los procesos reales.** Descubre los PIDs reales del servicio desde
  `/proc`, de modo que la salud, la detección de residuales y los (raros, opt-in)
  kills se basan en el ejecutable resuelto y el UID — nunca en un nombre de
  proceso.
- **Valida antes de tocar nada.** Config rota, un preflight requerido que falla o
  un lock con nombre activo bloquean la remediación en vez de amplificar una
  caída.
- **Ocupado no es caído.** Un servidor que responde "too many connections" o
  "loading" se reporta con su propio motivo, nunca se reinicia por ello.
- **Portable.** La misma configuración y comportamiento corren sobre systemd *y*
  OpenRC; el backend de init se autodetecta.
- **Disponibilidad honesta.** El SLA cuenta solo los ciclos que realmente
  observó — el tiempo anterior a que pudiera existir evidencia es un hueco, no se
  cuenta como caída.

## Características

**Monitorización y salud**
- Una **flota de workers independientes** — uno por servicio — cada uno
  ejecutando sus propios checks, evaluando reglas y dirigiendo la remediación en
  su propia cadencia; un panic en un worker nunca tumba el daemon.
- Un amplio **catálogo de checks** ([lista completa](docs/rules.md#checks)):
  estado/versión/config del servicio, puertos TCP, endpoints HTTP(S) y
  WebSocket, expiración de certificados TLS, sondas nativas de protocolo
  (PostgreSQL, MySQL/MariaDB, Redis, MongoDB, SMTP/IMAP, DNS, LDAP, SNMP, AMQP,
  …), consultas SQL, replicación y sentencias de larga duración, integridad
  SQLite, salud del almacenamiento (SMART, RAID, LVM, rendimiento con
  `hdparm`), sensores de hardware, interfaz de salida, ruta por defecto, reglas
  de firewall, deriva de reloj, crecimiento de tamaño de ficheros/dirs y
  métricas de proceso/conteo.
- **Detección de código reemplazado** — servicios que siguen ejecutando un
  binario o una librería compartida que una actualización reemplazó en disco, y
  `restart_on_change` para reiniciar un servicio cuando cambia una de sus apps o
  de sus ficheros de configuración.
- **Interdependencias entre checks** (`requires` / `skip_when_changed`) para que
  un probe solo se ejecute cuando se cumplen sus prerrequisitos.
- **Host watches** — vigilan recursos que no son servicios (montajes, arrays
  RAID, uplinks de red, certificados, …), disparando comandos de hook y/o
  notificaciones, y reparando el almacenamiento cuando se pide (`then.expand`
  amplía un volumen, `then.remount` recupera un montaje colgado).
- **Watches de seguridad** — `process_policy` avisa cuando un usuario ejecuta un
  programa que su política no permite, y `unowned_processes` lista los procesos
  de los que no responde ninguna unidad de init ni paquete instalado. Aparecer
  en la lista no autoriza nada: solo el `kill_only_if` del propio watch o la
  elección verificada de un administrador le envían una señal.

**Remediación segura**
- Un único **engine de operación** compartido por la CLI y el daemon: lock de
  operación → locks de runtime con nombre → preflight requerido → guards →
  stop → verificar procesos y conciliar init → start →
  verificación + postflight.
- **Locks de runtime con nombre** para acotar ventanas de mantenimiento (backups,
  migraciones): `sermoctl lock … -- COMANDO` mantiene un lock con TTL durante la
  ejecución de un comando.
- **Guards, ventanas y política de remediación** para expresar *cuándo* y *con
  qué frecuencia* puede ejecutarse una acción, con escalado.
- **Invariantes de seguridad duros** que el YAML no puede desactivar (ver abajo).

**Disponibilidad e historial**
- **SLA en ventanas móviles** (hora → año) para cada servicio y watch de
  disponibilidad, guardado como un archivo por ventana de gráfica para que un año
  de historial siga acotado sin perder ningún incidente.
- Un **registro de eventos/actividad** con retención y compactación del almacén
  de estado.

**Operar**
- Una **CLI** de operador enfocada (`sermoctl`) para status, acciones de ciclo de
  vida seguras, validación de config, locks, procesos, preflight, inventario, SLA
  y eventos.
- **Notificaciones** a email, Slack, Teams, Telegram, ntfy, Gotify y los
  terminales con sesión abierta (`tty`, `wall`) con una plantilla de mensaje por
  defecto, graduadas en cinco niveles
  de severidad (debug → critical): cada notifier fija el nivel mínimo que
  recibe, un umbral puede escalar de warning a error y a critical, y un
  incidente se anuncia una vez por nivel en lugar de una vez por watch.
- Un **bot de informes de Telegram** opcional e interactivo (solo lectura):
  pregúntale `/status`, `/services`, `/sla` y responde con informes en vivo —
  solo long polling, sin puerto de entrada, respondiendo a los chats de la lista
  permitida.
- Un **interruptor de pánico** a nivel de daemon para pausar toda la remediación
  automática al instante.
- **Asistentes guiados** para configuraciones comunes (service, docker, vm,
  mount, volume, net, uplink).

**Dashboard web** (opcional)
- Un dashboard en vivo y autocontenido: checks por servicio, historial de SLA,
  gráficas de latencia, un feed de eventos, y el inventario completo (servicios /
  apps / librerías) y los host watches.
- Un panel de **Sesiones**: sesiones interactivas de SSH, tmux y screen, y las
  sentencias en ejecución en MySQL, MariaDB y PostgreSQL; un administrador puede
  cerrar una sesión verificada o cancelar una sentencia reverificada.
- **Dirigido por push** vía server-sent events con fallback a polling, y
  construido con accesibilidad **WCAG 2.2 AA**.
- HTTP en loopback por defecto, con accesos de administrador y de invitado de
  solo lectura desde ficheros de credenciales con hash — expónlo solo detrás de
  un reverse proxy TLS.

## Cómo funciona

Un único daemon (`sermod`) carga la configuración y el catálogo empaquetado, los
resuelve en un árbol de servicios, y construye una **flota**: un *Worker* por
servicio y un *Watch* por recurso de host o app. Un scheduler los ejecuta en
bucle. La CLI y la web UI hablan con el daemon vía HTTP y señales. Cada acción
sobre un servicio — manual o automática — pasa por `operation.Engine`, que
coordina locks, preflight, guards y el backend de init, de modo que la CLI y el
daemon nunca puedan divergir en cómo actúan.

```
clientes ── sermoctl (CLI) ─┐                    ┌── operation.Engine ── systemd / OpenRC
            navegador (Web) ─┤                    ├── locks con nombre (oplock + scanner)
                            │   sermod (daemon)   │
      señales ── SIGHUP ────┼── Monitor ─ Scheduler ─ Flota ┤── almacén de estado (SLA · eventos · métricas)
                 SIGTERM ───┘        │   (Worker por servicio│── notifiers (email/slack/teams/telegram/…)
                                     │    Watch por recurso) │
      config + catálogo empaquetado ─┘                       └── web.Server (dashboard + /api)
```

Ver [docs/architecture.md](docs/architecture.md) para los diagramas fieles
al código (pipeline de operación, estados de locks, ciclo de monitorización).

## Los dos binarios

- **`sermoctl`** — la CLI del operador: status, start/stop/restart/reload/
  resume/repair seguros (y pause para VMs de libvirt y contenedores Docker),
  validación de config, locks, procesos, preflight, disponibilidad/SLA,
  inventario y eventos. Los comandos de solo lectura no necesitan root.
- **`sermod`** — el daemon: un worker independiente por servicio ejecuta los
  checks, evalúa las reglas y dirige la remediación a través del mismo engine de
  operación segura que usa `sermoctl`. También ejecuta **host watches** que
  disparan comandos de hook y/o **notificaciones** (email, Slack, Teams,
  Telegram, ntfy, Gotify, terminales), y puede servir el **dashboard web**
  (configura `web.port`,
  recomendado `9797`) — HTTP en loopback con auth opcional; expónlo solo detrás
  de un reverse proxy TLS
  ([cómo](docs/configuration.md#behind-a-reverse-proxy-required-to-expose-it)).

## Invariantes de seguridad

No se pueden desactivar en YAML — la validación rechaza cualquier toggle
`security:` que lo intente. Completos en [docs/safety.md](docs/safety.md):

1. **Ninguna acción con un preflight requerido fallido** — bloqueada con
   `preflight_failed`.
2. **Ninguna acción que un guard bloquee** — los guards se evalúan antes de la
   remediación, y lo que hace una acción decide qué guards le aplican (un guard
   sobre `start` también bloquea `restart` y `repair`).
3. **Los locks de runtime con nombre activos siempre bloquean las acciones de
   servicio** — comprobado automáticamente, sin necesidad de regla.
4. **Nunca enviar señales a un residual sin verificar** — un residual solo
   recibe señales cuando lo nombra un selector `kill_only_if` explícito o una
   identidad estricta de `processes:`, o cuando el kernel lo sitúa en el grupo
   de control propio del servicio. SIGKILL además necesita `force_kill`: el
   valor por defecto del motor es `false`; el `sermo.yml` que se instala fija
   `auto`.
5. **Nunca matar por nombre de proceso** — un kill requiere coincidencia exacta
   de la ruta `/proc/<pid>/exe` resuelta **y** del UID real; la línea de comandos
   solo acota una coincidencia, y cualquier cosa que Sermo no pueda identificar
   positivamente se *reporta, no se mata*.
6. **Nunca enviar señales de terminación a PID 1 ni a hilos del kernel** —
   bloqueado centralmente, no configurable.
7. **`force_kill: true` requiere `kill_only_if`** con selectores `users` y
   `exe_any` no vacíos.
8. **Un restart siempre verifica el stop y el start** bajo los mismos locks,
   preflight, guards y plazo; los procesos que sobreviven bloquean el nuevo
   start.
9. **Un proceso *stray* es un diagnóstico, nunca un objetivo** — un miembro del
   grupo de control que ningún selector reclama se reporta, y nada le envía
   señales por serlo.
10. **`process_policy` solo avisa, y `unowned_processes` no autoriza nada** —
    aparecer en la lista nunca convierte un proceso en objetivo de una señal.
11. **Nunca se destruye una red de libvirt con interfaces de invitados vivas.**
12. **Una sentencia de base de datos solo se cancela tras reverificarla** — una
    sentencia que cambió o terminó nunca se mata.
13. **Un servidor ocupado nunca se reinicia por estar ocupado** — "too many
    connections", "loading" o "starting up" significa que está vivo.

## Build

```sh
make build      # produces bin/sermoctl and bin/sermod
make test       # run the test suite
```

Requiere Go 1.27.3+. Dependencias de runtime: `systemctl` o `rc-service` en el host.

**`sermod` se ejecuta como root.** Gestiona servicios que pertenecen a distintos usuarios y
accede a áreas privilegiadas (control de servicios, envío de señales a procesos de otros usuarios,
inspección de `/proc` entre usuarios incluyendo IO por proceso, sockets ICMP raw), por lo que las
unidades empaquetadas lo ejecutan como root; avisa al arrancar si no lo es. Por tanto la config es
entrada confiable, propiedad de root — los checks `command` y los `hook`s se ejecutan como root
(nunca a través de un shell), así que mantén `/etc/sermo` solo para root y pon los secretos en el
entorno (`${env:NAME}`). Ver [safety](docs/safety.md#privileges-the-daemon-runs-as-root).
Los comandos de `sermoctl` de solo lectura (status, config, etc.) no necesitan root.

## Install

`make install` respeta las variables estándar de directorios GNU y el staging de
`DESTDIR`, e instala los binarios, el catálogo completo (manteniendo el
layout `services/apps/libs/patterns`), los ejemplos, un `sermo.yml` de ejemplo, la plantilla de
notificación por defecto, la config de tmpfiles.d, y tanto la unidad de systemd como el
script de init de OpenRC (con sus rutas de binario/config reescritas para coincidir).
El script de OpenRC escribe su log en `/var/log/sermod.log`, e `install-openrc`
añade la regla de logrotate que lo rota en el sitio (`copytruncate`):

```sh
sudo make install PREFIX=/usr                 # /usr/bin, /usr/sbin or merged-/usr /usr/bin, /etc/sermo, ...
make install DESTDIR=/tmp/stage PREFIX=/usr    # stage for packaging
```

Variables clave (anula en la línea de comandos): `DESTDIR`, `PREFIX`/`prefix`,
`bindir`, `sbindir`, `datadir`, `sysconfdir`, `TMPFILESDIR`,
`SYSTEMD_UNITDIR`, `OPENRC_INITDIR`, `LOGROTATEDIR`. También hay targets granulares disponibles:
`install-bin`, `install-catalog`, `install-examples`, `install-config`,
`install-templates`, `install-tmpfiles`, `install-systemd`, `install-openrc`
(y `uninstall`). Un
`sermo.yml` existente nunca se sobrescribe. `make install` no crea
`/var/lib/sermo`; la config de tmpfiles.d instalada se encarga de crear ese directorio.

En hosts con merged-/usr donde `/usr/sbin` es un symlink a `/usr/bin`, el
`sbindir` por defecto colapsa a `$(bindir)` para que los paquetes con `DESTDIR` no materialicen un
directorio `usr/sbin` real y reemplacen el symlink del host al extraerse. Pasa un
`sbindir=...` explícito solo cuando el destino tenga realmente un directorio sbin distinto.

No despliegues un árbol `DESTDIR` extrayendo un archivo tar directamente en `/`
con los metadatos de directorio preservados. Un árbol staged contiene entradas de directorio como
`./`, `etc/` y `usr/`; un `tar -xpf` simple puede aplicar esos modos a directorios
de sistema existentes. Usa el gestor de paquetes, copia los archivos instalados directamente,
o extrae archivos de prueba ad-hoc con:

```sh
sudo tar --no-overwrite-dir -C / -xpf sermo-stage.tar
```

## Quick start

```sh
# Inspect a unit (no config needed)
sermoctl backend
sermoctl status nginx
sermoctl is-active nginx

# Listar los servicios configurados (unidades init, contenedores Docker, VMs)
sermoctl services                     # SERVICE / TYPE / STATE / MONITORED
sermoctl services --notify ops-email  # enviar por email un informe de salud
sermoctl patterns                     # juegos de patrones en uso

# Listar el inventario del catálogo
sermoctl services catalog      # perfiles de servicio del catálogo (nginx, mariadb, ...)
sermoctl services catalog all  # incluir perfiles no instalados en este host
sermoctl apps          # tools/runtimes (only installed)
sermoctl apps all      # include not-installed
sermoctl libs          # shared libraries (restart triggers)

# Validate configuration
sermoctl config validate

# Operate a configured service through the safe engine
sermoctl restart apache-main
sermoctl pause vm-web            # suspender una VM de libvirt o pausar un contenedor Docker

# Pause / resume monitoring of a service (e.g. for maintenance)
sermoctl unmonitor apache-main   # daemon stops checking it
sermoctl monitor apache-main     # resume
sermoctl daemon reload           # ask sermod to re-read its config

# Fence a maintenance window with a named runtime lock
sermoctl lock apache-main --reason backup --ttl 1h -- /usr/local/bin/backup.sh

# Disponibilidad (SLA) en ventanas móviles (hora..año)
sermoctl sla                     # cada servicio y watch de disponibilidad
sermoctl sla apache-main         # un servicio
sermoctl sla --series apache-main --since 168h  # serie guardada; los tramos siguen a la ventana

# Run the daemon
sermod run --config /etc/sermo/sermo.yml
```

Las definiciones empaquetadas viven en [`catalog/`](catalog/), las configs de ejemplo en
[`examples/`](examples/), las unidades de empaquetado en [`packaging/`](packaging/). El
layout de archivos en el host está en
[configuration → layout](docs/configuration.md#layout).
Los flags del daemon (`--verbose`) están en
[CLI → sermod daemon flags](docs/cli.md#sermod-daemon-flags).

## Documentation

- [Configuration](docs/configuration.md) — config global, catalog services, servicios,
  merge y variables; [`docs/sermo-all.yml`](docs/sermo-all.yml) es el
  ejemplo anotado completo.
- [Rules](docs/rules.md) — checks, condiciones, ventanas, guards, política de
  remediación.
- [Services](docs/services.md) — escritura y override de servicios.
- [CLI](docs/cli.md) — comandos, flags y códigos de salida.
- [Safety](docs/safety.md) — los invariantes que no se pueden desactivar: sin
  acciones sin salvaguarda, sin SIGKILL por defecto, nunca matar por nombre (solo
  coincidencia exacta de exe-resuelto + UID).
- [Web dashboard](docs/configuration.md#web-ui) — qué significa cada panel,
  badge y gráfica del dashboard.
- [Architecture](docs/architecture.md) — diagramas de extremo a extremo del
  daemon, el pipeline de operación, los estados de locks y el ciclo de
  monitorización.
