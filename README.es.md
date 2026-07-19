# Sermo

Sermo es un supervisor de servicios portable sobre **systemd** y **OpenRC**. Valida
los servicios antes de actuar, detecta estado operativo bloqueante (locks de runtime
con nombre, backups, configuración inválida), descubre los procesos reales de un servicio,
y aplica reglas de remediación **con salvaguardas** — nunca reiniciando a ciegas.

Incluye dos binarios:

- **`sermoctl`** — la CLI del operador (status, start/stop/restart/reload/resume seguros, validación
  de config, locks, procesos, preflight, disponibilidad/SLA por servicio).
- **`sermod`** — el daemon: un worker independiente por servicio ejecuta los checks,
  evalúa las reglas y dirige la remediación a través del mismo engine de operación segura
  que usa `sermoctl`. También ejecuta **host watches** (ver
  [host watches](docs/configuration.es.md#host-watches)) que disparan comandos de hook
  y/o **notificaciones** (email, Slack, Teams), y puede servir un **dashboard
  web** (configura `web.port`, recomendado `9797`) con checks por servicio, historial
  de SLA, gráficas de latencia y un feed de eventos — HTTP en loopback con auth opcional;
  expónlo solo detrás de un reverse proxy TLS
  ([cómo](docs/configuration.es.md#behind-a-reverse-proxy-required-to-expose-it)).

## Build

```sh
make build      # produces bin/sermoctl and bin/sermod
make test       # run the test suite
```

Requiere Go 1.26+. Dependencias de runtime: `systemctl` o `rc-service` en el host.

**`sermod` se ejecuta como root.** Gestiona servicios que pertenecen a distintos usuarios y
accede a áreas privilegiadas (control de servicios, envío de señales a procesos de otros usuarios,
inspección de `/proc` entre usuarios incluyendo IO por proceso, sockets ICMP raw), por lo que las
unidades empaquetadas lo ejecutan como root; avisa al arrancar si no lo es. Por tanto la config es
entrada confiable, propiedad de root — los checks `command` y los `hook`s se ejecutan como root
(nunca a través de un shell), así que mantén `/etc/sermo` solo para root y pon los secretos en el
entorno (`${env:NAME}`). Ver [safety](docs/safety.es.md#privileges-the-daemon-runs-as-root).
Los comandos de `sermoctl` de solo lectura (status, config, etc.) no necesitan root.

## Install

`make install` respeta las variables estándar de directorios GNU y el staging de
`DESTDIR`, e instala los binarios, el catálogo completo (manteniendo el
layout `services/apps/libs/patterns`), un `sermo.yml` de ejemplo, la plantilla de
notificación por defecto, la config de tmpfiles.d, y tanto la unidad de systemd como el
script de init de OpenRC (con sus rutas de binario/config reescritas para coincidir):

```sh
sudo make install PREFIX=/usr                 # /usr/bin, /usr/sbin or merged-/usr /usr/bin, /etc/sermo, ...
make install DESTDIR=/tmp/stage PREFIX=/usr    # stage for packaging
```

Variables clave (anula en la línea de comandos): `DESTDIR`, `PREFIX`/`prefix`,
`bindir`, `sbindir`, `datadir`, `sysconfdir`, `TMPFILESDIR`,
`SYSTEMD_UNITDIR`, `OPENRC_INITDIR`. También hay targets granulares disponibles:
`install-bin`, `install-catalog`, `install-config`, `install-templates`,
`install-tmpfiles`, `install-systemd`, `install-openrc` (y `uninstall`). Un
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

# List catalog inventory, not configured runtime targets
sermoctl services      # packaged catalog service profiles (nginx, mariadb, ...)
sermoctl services all  # include profiles not installed on this host
sermoctl services --notify ops-email  # email a services inventory report
sermoctl apps          # tools/runtimes (only installed)
sermoctl apps all      # include not-installed
sermoctl libs          # shared libraries (restart triggers)

# Validate configuration
sermoctl config validate

# Operate a configured service through the safe engine
sermoctl restart apache-main

# Pause / resume monitoring of a service (e.g. for maintenance)
sermoctl unmonitor apache-main   # daemon stops checking it
sermoctl monitor apache-main     # resume
sermoctl daemon reload           # ask sermod to re-read its config

# Availability (SLA) per service over rolling windows (hour..year)
sermoctl sla                     # all services
sermoctl sla apache-main         # one service
sermoctl sla --series apache-main --since 168h  # per-minute series (graph data)
sermoctl sla --process-uptime apache-main       # continuidad de proceso confirmada, no salud de checks

# Run the daemon
sermod run --config /etc/sermo/sermo.yml
```

Las definiciones empaquetadas viven en [`catalog/`](catalog/), las configs de ejemplo en
[`examples/`](examples/), las unidades de empaquetado en [`packaging/`](packaging/). El
layout de archivos en el host está en
[configuration → layout](docs/configuration.es.md#layout).
Los flags del daemon (`--verbose`) están en
[CLI → sermod daemon flags](docs/cli.es.md#sermod-daemon-flags).

## Documentation

- [Configuration](docs/configuration.es.md) — config global, catalog services, servicios,
  merge y variables; [`docs/sermo-all.yml`](docs/sermo-all.yml) es el
  ejemplo anotado completo.
- [Rules](docs/rules.es.md) — checks, condiciones, ventanas, guards, política de
  remediación.
- [Services](docs/services.es.md) — escritura y override de servicios.
- [CLI](docs/cli.es.md) — comandos, flags y códigos de salida.
- [Safety](docs/safety.es.md) — los invariantes que no se pueden desactivar: sin
  acciones sin salvaguarda, sin SIGKILL por defecto, nunca matar por nombre (solo
  coincidencia exacta de exe-resuelto + UID).
