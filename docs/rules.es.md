# Comprobaciones, condiciones y reglas

## Comprobaciones

Las comprobaciones son sondas de un solo disparo bajo `checks` (y `preflight`,
que reutiliza el mismo esquema). Una entrada de `checks` marcada con `verify: true`
se ejecuta además como verificación de arranque tras la operación. Tipos admitidos:

El conjunto completo de tipos de comprobación de un solo disparo está definido de forma centralizada. Las pruebas fijan esa lista contra el despacho del constructor y la validación de configuración, y fijan la lista de protocolos de conexión de más abajo contra el registro `conn`, de modo que los tipos de comprobación anunciados no puedan divergir del código. (Las formas de watch multi-métrica como los watches `net`/`icmp`/`swap` y `file`/`process` se construyen sobre estas primitivas.)

Las comprobaciones de protocolo de conexión (MySQL, PostgreSQL, Redis, Docker, libvirt, etc.) se registran por su nombre de protocolo con alias comunes (p. ej. `mysql`/`mariadb`, `fpm`/`php-fpm`).

| type          | pasa cuando                                                        |
|---------------|--------------------------------------------------------------------|
| `tcp`         | una conexión TCP a `host:port` tiene éxito                          |
| `ports`       | un conjunto de puertos de `host` satisface una expectativa de abierto/cerrado (ver Ports)|
| `http`        | la respuesta coincide con `expect_status` (y cabeceras/cuerpo/JSON opcionales, ver HTTP)|
| `command`     | el comando termina con `expect_exit` (por defecto 0) y su salida coincide con `expect_stdout`/`expect_stderr` opcionales; un `user` opcional lo ejecuta como un usuario concreto del SO; `on_change` alerta cuando su salida cambia (p. ej. una versión), solo en forma de array |
| `config`      | un comando de prueba de configuración (`apachectl configtest`, `nginx -t`, …) pasa, y (con `on_change`) el `path` de configuración no ha cambiado (ver Condiciones de salud del servicio)|
| `service`     | el estado del backend es igual a `expect` (active/inactive/paused/failed/unknown)|
| `file_exists` | existe un archivo de bandera/bloqueo ajeno (nunca bajo `<runtime>/locks`)     |
| `file`        | una ruta existe y es un archivo regular                                |
| `lockfile`    | existe un candidato a archivo de bloqueo regular creado por el servicio — protégelo con `requires: [service]`; no bloquea operaciones |
| `binary`      | una ruta existe y es ejecutable                                    |
| `pidfile`     | un pidfile existe y referencia un proceso en ejecución — protégelo con `requires: [service]` de modo que un pidfile ausente/obsoleto sea un error solo mientras el servicio está activo |
| `socket`      | existe un candidato a socket Unix — protégelo con `requires: [service]` para los sockets creados por el servicio |
| `libraries`   | todas las bibliotecas compartidas DT_NEEDED del binario pueden resolverse (debug/elf nativo, sin ldd) |
| `process`     | un proceso que coincide con `exe`/`user` está en `state` (running/zombie/absent)|
| `metric`      | una métrica muestreada satisface `op value` (ver Metrics)                |
| `count`       | el número de entradas en un directorio satisface `op value` (ver Count)|
| `storage`     | se cumplen los predicados de espacio/inodos de un sistema de archivos (`*_pct` acepta `%`; `*_bytes` requiere K/M/G/T) |
| `autofs`      | el automounter autofs está activo (puntos de montaje autofs presentes — `path`/`count`) (ver Autofs)|
| `load`        | se cumple un umbral de carga media (load1/load5/load15, opcional per_cpu)|
| `users`       | el número de usuarios conectados (desde utmp) satisface `count {op, value}`|
| `process_count` | el número de procesos (de todo el host, o filtrado por `user`/`exe`/`exe_dir`) satisface `count {op, value}`|
| `hdparm`      | el rendimiento de lectura `hdparm` de un disco cruza un umbral (`read`/`cached` MB/s) (ver Rendimiento de disco)|
| `sensors`     | los sensores de hardware hwmon cruzan un umbral (`temp` °C / `fan` RPM / `voltage` V) (ver Sensores de hardware)|
| `smart`       | la salud/atributos SMART de una unidad (veredicto fallido, `reallocated`, `wear`, `temperature`) (ver Sensores de hardware)|
| `raid`        | un array RAID por software md de Linux está degradado/recuperándose (`degraded`/`recovering`/`arrays`) (ver Sensores de hardware)|
| `edac`        | errores de memoria ECC desde EDAC (`ce` corregibles / `ue` no corregibles) (ver Sensores de hardware)|
| `memory`      | RAM del sistema frente a MemAvailable del kernel (used_pct/available_pct/available_bytes) |
| `pressure`    | tiempo de bloqueo PSI del kernel para cpu/memory/io (`some_*`/`full_*` avg10/60/300) |
| `fds`         | descriptores de archivo del sistema frente a `fs.file-max` (used_pct/free/allocated)  |
| `pids`        | la tabla PID del kernel frente a `kernel.pid_max` (used_pct/free/count)      |
| `diskio`      | tasas de E/S por ciclo de un dispositivo de bloque (util_pct/read_bytes/write_bytes/await_ms) |
| `conntrack`   | la tabla conntrack de netfilter frente a su máximo (used_pct/free/count)      |
| `firewall_rules` | nftables/iptables tiene al menos `min_rules` reglas cargadas (ver Reglas de firewall) |
| `route`       | existe una ruta por defecto activa, opcionalmente saliendo por una `interface` dada (ver Ruta por defecto)|
| `net`         | una métrica de interfaz (`metric: state\|speed\|errors\|address`) se cumple — forma de métrica única del watch net |
| `icmp`        | una métrica de ping (`metric: state\|latency`) contra `host`, opcionalmente ligada a una `interface` |
| `swap`        | una métrica de swap (`metric: usage\|io`) se cumple — forma de métrica única del watch swap |
| `entropy`     | la entropía del kernel disponible satisface `avail {op, value}`              |
| `zombies`     | el número de procesos zombi satisface `count {op, value}`         |
| `oom`         | el contador de OOM-kill del kernel subió en `delta {op, value}` desde el último ciclo|
| `cert`        | un certificado TLS está caducando/inválido, o su algoritmo/emisor cambió (ver Cert)|
| `mysql` / `mariadb` | un servidor MySQL/MariaDB responde: sin credenciales lee el saludo del handshake (vivacidad + versión); con un usuario/contraseña autentica y hace ping (ver Base de datos) |
| `mongodb` / `mongo` | una conexión a un servidor MongoDB autentica, hace ping e informa de su versión y del `role` del replica-set para `expect`/`on_change` (ver Base de datos) |
| `postgres` / `postgresql` | una conexión a un servidor PostgreSQL autentica y responde (ver Base de datos) |
| `redis` / `valkey` | una conexión a un servidor Redis/Valkey autentica y responde a PING; expone role, replicación, persistencia y memoria desde INFO para `expect` (ver Base de datos) |
| `memcached` / `memcache` | un servidor memcached responde a `stats`; expone versión, conexiones, hits/misses, items, bytes y desalojos para `expect` (ver Base de datos) |
| `imap`        | un servidor IMAP saluda con OK (anónimo) y, con credenciales, LOGIN tiene éxito (ver Base de datos) |
| `pop` / `pop3` | un servidor POP3 saluda con +OK (anónimo) y, con credenciales, USER/PASS tiene éxito (ver Base de datos) |
| `smtp`        | un servidor SMTP saluda con 220 + EHLO (anónimo) y, con credenciales, AUTH PLAIN tiene éxito (ver Base de datos) |
| `nntp` / `nntps` | un servidor NNTP saluda con 200/201 (anónimo) y, con credenciales, AUTHINFO USER/PASS tiene éxito (ver Base de datos) |
| `ftp`         | un servidor FTP saluda con 220 (anónimo) y, con credenciales, el login USER/PASS tiene éxito (ver Base de datos) |
| `ssh`         | un servidor SSH completa el intercambio de claves (anónimo: clave de host + banner); con credenciales, el login tiene éxito; `on_change` alerta sobre cambios de la clave de host (ver Base de datos) |
| `fpm` / `php-fpm` | un pool PHP-FPM responde a un `/ping` FastCGI con `pong`; un `status_path` opcional expone métricas del pool para `expect` (socket Unix o TCP, ver Base de datos) |
| `dns`         | un servidor DNS responde a una consulta (NOERROR/NXDOMAIN) para `query` (ver Base de datos) |
| `ntp`         | un servidor NTP responde con una hora sincronizada (modo servidor, estrato 1–15); expone leap, precisión, retardo/dispersión raíz y reference id para `expect` (ver Base de datos) |
| `snmp`        | un agente SNMP responde a un GET del sistema (comunidad v2c o usuario/contraseña v3); expone sys name/contact/location/uptime para `expect`; `on_change` alerta sobre cambios de la identidad del dispositivo (ver Base de datos) |
| `tftp`        | un servidor TFTP responde a un RRQ con un paquete válido (DATA o ERROR) (ver Base de datos) |
| `ldap`        | un directorio LDAP acepta un bind anónimo, o un bind simple con credenciales (ver Base de datos) |
| `ajp`         | un conector AJP13 (p. ej. el 8009 de Tomcat) responde a un CPing con CPong (ver Base de datos) |
| `ipp` / `cups` | un servidor IPP (CUPS/cupsd) responde a una petición IPP con una respuesta válida (ver Base de datos) |
| `rsync` / `rsyncd` | un daemon rsync envía su saludo `@RSYNCD:` (ver Base de datos) |
| `dhcp` / `dhcpd` | un servidor DHCP responde a un DHCPDISCOVER con un DHCPOFFER (ver Base de datos) |
| `dhclient` / `dhcp-client` | un cliente DHCP local tiene UDP/68 ligado en `/proc/net/udp` (ver Base de datos) |
| `rspamd`      | un worker rspamd responde a `GET /ping` con `pong` (ver Base de datos) |
| `libvirt` / `libvirtd` | un daemon libvirt responde a RPC; expone recuentos de VM (`domains.active`…), capacidad del nodo y el estado de una VM para `expect`/`on_change` (ver Base de datos) |
| `dbus`        | un daemon D-Bus completa el handshake de auth/Hello y responde a `GetId` (ver Base de datos) |
| `udisks2`     | UDisks2 está registrado en el bus del sistema y responde a `Peer.Ping` en su objeto Manager (ver Base de datos) |
| `avahi` / `avahi-daemon` | el daemon Avahi responde a `GetVersionString` sobre su API D-Bus (ver Base de datos) |
| `syncthing`   | una instancia Syncthing responde a `/rest/noauth/health` con `{"status":"OK"}` (ver Base de datos) |
| `docker`      | el motor Docker responde a `/info`, exponiendo recuentos de contenedores (running/paused/stopped), imágenes y el estado/salud de un contenedor para `expect`/`on_change` (ver Base de datos) |
| `unifi` / `unifi-controller` / `unifi-network` | un controlador UniFi Network responde a `GET /status` con `meta.rc == "ok"` en 8443 (ver Base de datos) |
| `influxdb` / `influx` | un servidor InfluxDB responde a `/health` (o `/ping`) e informa de su versión en 8086 (ver Base de datos) |
| `prometheus` / `prom` | un servidor Prometheus responde a `/api/v1/status/buildinfo` (o `/-/healthy`) en 9090 (ver Base de datos) |
| `cloudflared` / `cloudflare-tunnel` | un daemon Cloudflare Tunnel responde a `/metrics` en 60123 con métricas `cloudflared_` (ver Base de datos) |
| `clamd` / `clamav` | un daemon ClamAV responde a `VERSION` con la versión de su motor (ver Base de datos) |
| `spamd` / `spamassassin` | el daemon SpamAssassin responde a `PING` con `PONG` (ver Base de datos) |
| `nut` / `ups` / `upsd` | el upsd de NUT responde a `VER`; un UPS expone sus variables (estado, carga/autonomía de batería, carga, voltajes) para `expect`/`on_change` (ver Base de datos) |
| `smb` / `samba` / `cifs` | un servidor SMB/CIFS negocia (y, con credenciales, autentica) (ver Base de datos) |
| `acpid`       | el daemon de eventos ACPI acepta una conexión en su socket Unix (ver Base de datos) |
| `fail2ban`    | fail2ban-server acepta una conexión en su socket de control (ver Base de datos) |
| `lvmpolld`    | el daemon de poll de LVM responde a una petición `hello` con `OK` sobre su socket (ver Base de datos) |
| `rpcbind` / `portmap` / `portmapper` | el portmapper RPC responde a una llamada RPC NULL (ver Base de datos) |
| `nfs` / `nfs-server` / `nfsd` | un servidor NFS responde a una llamada RPC NULL en 2049 (ver Base de datos) |
| `mountd` / `rpc.mountd` / `nfs-mountd` | el daemon de montaje NFS responde a una llamada RPC NULL a MOUNT (100005) (ver Base de datos) |
| `statd` / `rpc.statd` / `nsm` / `nfs-statd` | el monitor de estado NFS responde a una llamada RPC NULL a NSM (100024) (ver Base de datos) |
| `nebula` / `nebula-vpn` | un nodo mesh-VPN Nebula responde a un paquete de túnel desconocido con un `recv_error` en 4242/udp (ver Base de datos) |
| `openvpn` / `ovpn` | un servidor OpenVPN responde a un hard-reset-client con un hard-reset-server en 1194 (ver Base de datos) |
| `rdp` / `ms-wbt-server` | un servidor de Escritorio Remoto responde a la negociación de conexión X.224 (ver Base de datos) |
| `guacd` / `guacamole` | el daemon proxy de Guacamole responde a un `select` con una instrucción Guacamole (ver Base de datos) |
| `asterisk` / `ami` | una PBX Asterisk envía su saludo AMI `Asterisk Call Manager/<version>` (ver Base de datos) |
| `sieve` / `managesieve` | un servidor ManageSieve envía su saludo de capacidades terminado en `OK` (ver Base de datos) |
| `mqtt`        | un broker MQTT acepta un CONNECT (código de retorno CONNACK 0) (ver Base de datos) |
| `amqp` / `rabbitmq` | un broker AMQP 0-9-1 envía un saludo Connection.Start válido (ver Base de datos) |
| `kafka`       | un broker/controlador Kafka responde a una petición `ApiVersions` no autenticada; expone el `role` del listener (broker/controller) y los flags `produce_api`/`vote_api` para `expect` (ver Base de datos) |
| `varnish` / `varnishadm` | la CLI de gestión de Varnish responde con su banner/desafío de auth (ver Base de datos) |
| `ceph` / `ceph-mon` | un monitor Ceph envía su banner messenger `ceph v…` (ver Base de datos) |
| `glusterfs` / `glusterd` / `gluster` | el glusterd de un nodo GlusterFS responde a un RPC NULL en 24007 (ver Base de datos) |
| `openvswitch` / `ovs` / `ovsdb` / `ovsdb-server` | ovsdb-server responde a una petición JSON-RPC `list_dbs` de OVSDB (ver Base de datos) |
| `sqlite` / `sqlite3` | un archivo de base de datos SQLite pasa `PRAGMA integrity_check` (ver SQLite) |
| `sql`         | el resultado escalar de una consulta SQL se compara (`== != > >= < <= contains =~`) contra un valor (ver Consulta SQL) |
| `mongodb-query` | un recuento de documentos / agregación / resultado de comando de MongoDB se compara contra un valor (ver Consulta MongoDB) |
| `influxdb-query` | el resultado escalar de una consulta InfluxQL (1.x) o Flux (2.x) se compara contra un valor (ver Consulta InfluxDB) |
| `size`        | un archivo/directorio crece al menos `grow_by` dentro de `within` (crecimiento descontrolado) (ver Crecimiento de tamaño) |
| `websocket` | un endpoint WebSocket completa el handshake de apertura de RFC 6455 (ver WebSocket) |

La comprobación `storage` también verifica el **montaje** de su `path` — ver
[storage y unidades de montaje](configuration.es.md#storage-y-unidades-de-montaje).

Las comprobaciones `process` y las hojas de condición de proceso coinciden con los valores reales de UID/GID leídos
de `/proc/<pid>/status`. Un nombre `user:` o `group:` configurado se resuelve
a través de `engine.user_lookup`; si el nombre no puede resolverse falla en modo cerrado y
no coincide con ningún proceso. Los valores numéricos de UID/GID evitan la ambigüedad del servicio de identidad del host.

La comprobación `command` afirma el resultado del comando: `expect_exit` (por defecto 0,
o una lista como `[0, 1]`) y los matchers opcionales `expect_stdout` / `expect_stderr`
— una cadena simple requiere esa subcadena, o un mapeo `{op, value}`
compara la salida recortada (`== != > >= < <= contains =~`):

```yaml
checks:
  queue-drained:
    type: command
    user: appqueue             # optional: username or numeric UID on the host
    command: [/usr/local/bin/queue-depth]
    expect_exit: 0
    expect_stdout: { op: "<", value: 100 }   # fewer than 100 items queued
    expect_stderr: ""                         # nothing written to stderr
```

`user` ejecuta el comando como ese usuario del SO (solo Linux). Sermo sigue ejecutando el
argv directamente, nunca a través de un shell; el proceso del daemon/CLI debe tener permiso
para cambiar de usuario (normalmente ejecutándose como root), y un usuario no resuelto o un
runner no soportado hace fallar la comprobación en modo cerrado.

Los mismos campos `expect_exit` / `expect_stdout` / `expect_stderr` están disponibles
en un hook de watch (`then.hook`) para validar el resultado del comando del hook, pero
`then.hook` no usa `user`.

#### Calificación de la salida con `analyze:` (conjuntos de patrones)

`expect_*` es una única aserción de pasa/falla. Para calificar la salida de un comando
que *por lo demás pasa* en **warning** o **error**, añade un bloque `analyze:`. Referencia conjuntos de reglas reutilizables de `catalog/patterns/`
(categoría `patterns`, `sermoctl patterns`) y puede añadir o silenciar reglas por
comprobación:

```yaml
checks:
  config:
    type: command
    command: ["/usr/bin/named-checkconf"]
    analyze:
      use: [common, named]     # inherit catalog/patterns sets, in order
      silence: [deprecated]     # drop inherited rules by id
      rules:                    # service-local rules, evaluated FIRST (precedence)
        - { id: zone-ok, match: "(?i)loaded serial", severity: ok }
```

Un conjunto de patrones es un documento `patterns` (bajo `catalog/patterns/`) con una lista de reglas ordenada y con id:

```yaml
name: common
rules:
  - { id: backup-now, match: "BACK UP DATA NOW",   severity: error }
  - { id: deprecated, match: "(?i)deprecated",      severity: warning }
```

- `match` es una regex Go RE2 (`(?i)` para insensible a mayúsculas); `severity` es
  `error` | `warning` | `ok`; el opcional `stream` es `stdout` | `stderr` | `both`
  (por defecto `both`).
- **Evaluación:** la lista de reglas resuelta es primero las `rules` locales de la comprobación (de modo que una
  whitelist `ok` del servicio o una regla más estricta sobreescribe una heredada), luego los
  conjuntos `use` en orden (menos los ids `silence`d). Por línea de salida gana la primera regla
  que coincide (una coincidencia `ok` mete esa línea en whitelist); la severidad de la comprobación es el
  máximo sobre todas las líneas.
- **Resultado:** `error` → la comprobación falla como requerida; `warning` → la comprobación
  falla como *opcional* (no bloquea start/restart/reload/resume
  ni impulsa remediación por sí misma); sin coincidencia → la comprobación pasa. El `pattern_id` que coincidió
  y la línea están en los datos del resultado.
- **Precedencia:** código de salida → `expect_*` → `analyze`. El analizador solo califica un
  comando que ya pasó sus comprobaciones de código de salida y `expect_*`.

### Condiciones de salud del servicio (versión / estado / configuración)

Un servicio puede habilitar tres monitores de salud estándar con dos bloques
declarativos cortos — **`version:`** y **`config:`** — que **reutilizan los comandos de versión
y configuración que el servicio del catálogo ya define** (`commands.version` y
`preflight.config`). Sermo sintetiza un monitor por servicio (un watch, construido una vez
para que la detección de cambios persista) a partir de cada uno:

```yaml
# catalog service (e.g. apache.yml) — already defines these, unchanged:
commands:
  version: { command: [apachectl, -v] }
preflight:
  config: { type: command, command: [apachectl, configtest] }

# service (services/apache.yml) — opt into the monitors:
uses: apache
version:
  on_change: { notify: [ops-email] }      # alert when the version changes
  # on_change: { notify: [ops-email], level: minor }   # …only on major/minor bumps
config:
  on_change: { notify: [ops-email] }      # alert when the config is invalid…
  path: [/etc/apache2/apache2.conf]       # …or (optional) when this file changes
```

- **Versión cambiada** — `version.on_change` ejecuta el comando de versión del servicio del catálogo y
  alerta (notificando a los notificadores listados) cuando su versión cambia — una actualización/degradación
  inesperada. Necesita `commands.version` (o `preflight.version`) en el
  servicio del catálogo. La comparación se hace sobre la **`version_short`** numérica (`a.b.c`),
  de modo que el ruido en el banner de versión (fechas de build, sufijos) no la dispara. Un
  **`level`** opcional elige la granularidad significativa:
  - `major` — solo los cambios de `a` disparan (`1.4.2 → 1.9.0` se ignora; `1.x → 2.x` dispara).
  - `minor` — los cambios de `a` o `b` disparan (releases de parche ignoradas).
  - `patch` *(por defecto)* — cualquier cambio de `a.b.c` dispara.

  Cuando la salida de versión no contiene un número parseable, el monitor recurre a
  comparar la línea cruda de modo que nunca se pierda un cambio.
- **Config inválida / cambiada** — `config.on_change` ejecuta la prueba
  `preflight.config` del servicio del catálogo y alerta cuando **falla** (config inválida); con un
  `path` también alerta cuando un archivo de configuración cambia. Un **`preflight:` personalizado** en
  el servicio reemplaza el `preflight.config` del servicio del catálogo, y el monitor usa entonces
  ese comando, incluido su campo `user` cuando esté presente.
- **Estado no errado** — la comprobación `service` existente cubre esto: alerta cuando
  la unidad no está en el estado esperado (`failed`/`unknown`) o el backend no puede
  consultarse.
  ```yaml
  checks:
    state: { type: service, expect: active }
  ```

`on_change.notify` sigue la precedencia de notificación habitual (omítelo para heredar el
default global `notify`, o `none` para suprimir). Un `dry_run: true` de service suprime
la entrega de notificaciones no-console para estos monitores del service; `wall` sigue
entregándose. Los tipos de comprobación subyacentes `command` (`on_change`) y `config`
también pueden usarse como documentos de watch de host cuando quieras un hook o un
comando independiente.

### Interfaz de salida (`interface`)

En un **host multi-homed** (varias NICs) una comprobación de red puede fijarse para salir
a través de una interfaz concreta con el campo opcional **`interface`**. El valor
puede ser un **nombre de interfaz**, una **IP** que la interfaz lleva, o su **MAC**, y
puede ser un **valor único o una lista**:

```yaml
checks:
  gw-via-wan:
    type: icmp
    host: 8.8.8.8
    metric: state
    expect: up
    interface: eth1                 # by name
  db-on-mgmt:
    type: tcp
    host: 10.0.0.5
    port: 5432
    interface: 192.168.1.2          # by IP it carries
  api-by-mac:
    type: http
    url: https://10.0.0.9/health
    interface: "00:11:22:33:44:55"  # by MAC
  gw-redundant:
    type: icmp
    host: 8.8.8.8
    metric: state
    expect: up
    interface: [eth0, eth1]         # two uplinks
    interface_match: any            # any (default) | all
```

- **Opcional.** Omite `interface` (el valor por defecto) y la sonda usa el enrutamiento normal
  a través de **todas** las interfaces, exactamente como antes — nada cambia salvo que lo establezcas.
- **Formas de valor.** `eth0` (nombre), `192.168.1.2` (una dirección en la interfaz), o
  `00:11:22:33:44:55` (MAC) — todas resuelven a la misma interfaz. Una **lista** fija
  la comprobación a varias interfaces.
- **`interface_match`** (solo significativo con una lista): **`any`** (por defecto) — la
  comprobación pasa si la sonda tiene éxito a través de **al menos una** interfaz (monitorización de
  failover/enlace redundante); **`all`** — pasa solo si la sonda tiene éxito
  a través de **cada** interfaz listada (verificar cada ruta de forma independiente). El
  resultado por interfaz está en los datos del resultado bajo `interfaces`.
- **Mecanismo.** Para TCP/UDP liga el socket con `SO_BINDTODEVICE`, forzando
  la salida a través de esa interfaz sin importar la tabla de enrutamiento; para `icmp`
  liga la sonda a la IPv4 de la interfaz (el mecanismo `ping -I <addr>`).
  **Solo Linux**, y `SO_BINDTODEVICE` necesita `CAP_NET_RAW` (root) — si la
  interfaz no existe o el daemon carece de privilegio, la comprobación **falla** en lugar
  de usar silenciosamente el enlace equivocado.
- **Dónde aplica.** `tcp`, `ports`, `icmp`, `websocket`, y toda
  comprobación de protocolo de conexión que marque TCP/UDP — sondas nativas y sondas
  respaldadas por driver con un dialer personalizado como `mysql`, `postgres`, `mongodb`, `ldap`,
  `libvirt`, `redis`, `smtp`, `dns`, `ntp`, `nfs`, `dhcp`, `openvpn`, `nebula`,
  `tftp`, …, más sondas de protocolo basadas en HTTP como
  `influxdb`/`prometheus`/`cloudflared`/`syncthing`/`unifi`/`rspamd`/`ipp` —
  honra la **lista completa + `interface_match`**. La comprobación `http` independiente
  honra una **única** interfaz (la primera listada).

### Interdependencias de comprobaciones (`requires` / `skip_when_changed`)

Cualquier comprobación puede declarar interdependencias para que sea **omitida** (no contada, sin
alerta, mostrada como `skipped`) en un ciclo donde no debería aplicar:

```yaml
checks:
  port:
    type: tcp
    host: 127.0.0.1
    port: 3306
  query:
    type: command
    command: ["/usr/bin/mysqladmin", "ping"]
    requires: [port]                              # skip while `port` is failing
    skip_when_changed: ["/etc/my.cnf", "/etc/pam.d/mysql"]  # skip while these changed
```

- **`requires: [check, …]`** — omitir esta comprobación mientras cualquier comprobación listada **falló**
  este ciclo. Esto evita alertas en cascada: si el `port` de MySQL está caído, la comprobación
  más profunda `query` se omite en lugar de reportarse también como fallida.
- **`skip_when_changed: [path, …]`** — omitir esta comprobación mientras cualquier archivo listado
  difiera de su línea base reconocida (p. ej. un archivo de configuración o una biblioteca acaba de
  actualizarse). La línea base se vuelve a reconocer tras un (re)arranque exitoso, así que la
  comprobación se reanuda una vez que el servicio está reconciliado.

Ambos aceptan un valor único o una lista. Las puertas se evalúan **después** de que las
comprobaciones del ciclo se ejecuten, así que la sonda todavía se ejecuta pero su resultado se suprime; usa el
`interval` de una comprobación o elimínala para evitar ejecutarla del todo.

Para **reiniciar** un servicio cuando una biblioteca o archivo se actualiza (la otra mitad del
ejemplo — "si la biblioteca pam se actualizó, reinicia"), usa una regla de remediación con
una condición [`changed:`](#rules) (o `restart_on_change: {libraries: […]}`):

```yaml
rules:
  restart-on-pam:
    type: remediation
    if: { changed: { library: pam } }   # or { path: /lib64/security/pam_unix.so }
    then: { action: restart }
```

### Ports

Una comprobación `ports` sondea varios puertos TCP en un host a la vez y evalúa una
expectativa cuantificada de abierto/cerrado. Es de estilo salud (`OK == true` significa que la
expectativa se cumple), así que un watch sobre ella dispara su hook cuando la expectativa se rompe.

```yaml
checks:
  web-ports:
    type: ports
    host: 10.0.0.5             # default 127.0.0.1
    ports: "80,443,1024-4000"  # comma-separated single ports and inclusive ranges
    expect: open               # per-port desired state: open | closed | any (default open)
    match: all                 # quantifier: all (AND) | any (OR) | none (NOT) (default all)
    on_change: false           # also fail when any port flips open<->closed between cycles
    connect_timeout: 1s        # per-port dial timeout (default 1s)
```

`expect` es el estado deseado de cada puerto y `match` el cuantificador sobre los puertos en
ese estado: **`all`** = cada puerto (AND), **`any`** = al menos uno (OR), **`none`**
= ningún puerto (NOT). Así `expect: open, match: all` pasa cuando **todos** los puertos están abiertos;
`expect: closed, match: any` pasa cuando **al menos uno** está cerrado. Un puerto está
*abierto* cuando acepta una conexión TCP dentro de `connect_timeout`, si no *cerrado*.

`expect: any` omite la expectativa de estado por completo — combínalo con
`on_change: true` para alertar puramente sobre **transiciones de estado** (un puerto que estaba abierto
volviéndose cerrado, o viceversa). Los datos del resultado exponen `open`, `closed`, `total` y
`changed`. Los puertos se deduplican; un escaneo está limitado a 16384 puertos y se ejecuta
de forma concurrente, pero un rango grande de puertos *filtrados* (sin respuesta) está limitado solo
por `connect_timeout`, así que prefiere rangos ajustados y un timeout corto.

Como `cert`, la detección `on_change` es **stateful** (recuerda los estados previos
entre ciclos). Funciona en comprobaciones de servicio y watches de host mientras la misma
instancia de comprobación esté viva; la línea base se reinicia cuando el worker del servicio o el watch
se reconstruye, por ejemplo tras una recarga de configuración.

### HTTP

Más allá del código de estado, una comprobación `http` puede enviar un método, cabeceras y un cuerpo
(crudo o JSON) y afirmar la respuesta:

```yaml
checks:
  api:
    type: http
    url: "https://api.example.com/v1/health"
    method: POST                       # any HTTP verb (default GET) — see below
    headers:
      Authorization: "Bearer ${token}" # any request headers
    json:                              # request body as JSON (sets Content-Type
      probe: true                      # automatically; or use `body:` for raw text)
    expect_status: 200                 # code, class (2xx), list, or { op, value }
    expect_body: { op: contains, value: "ready" } # body comparison (see below)
    expect_latency: { op: "<", value: 800 }   # optional: response time in ms
    proxy: "http://user:pass@squid:3128"   # optional: route the request through a proxy (Squid)
    expect_json:                       # optional: response JSON must match (dotted paths)
      status: ok                       # equality (scalar)
      data.replicas: { op: ">=", value: 2 }   # operator: >, >=, <, <=, ==, !=, contains, =~
      data.message: { op: contains, value: "healthy" }
      data.version: { op: "=~", value: "^v[0-9]+" }   # regex (Go/RE2)
```

Pasa (estilo salud, `OK == true`) cuando el estado coincide **y** cada
aserción se cumple. **`method`** acepta cualquier verbo HTTP estándar — `GET` (por defecto),
`HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`, `TRACE`, `CONNECT` — escrito
en cualquier caja (se normaliza a mayúsculas); un verbo desconocido se rechaza en la
validación de configuración. Un `body`/`json` de petición se envía para cualquier método que lleve
uno (`POST`/`PUT`/`PATCH`/…). **`http3: true`** envía la petición sobre **HTTP/3
(QUIC)** en lugar de TCP — ver más abajo. **`proxy`** enruta la petición a través de un forward proxy como
**Squid** (`http://[user:pass@]host:port`; esquemas `http`, `https` o `socks5` —
las credenciales, cuando están presentes, van en la URL). Esto monitoriza tanto que el proxy
reenvía correctamente como que el destino es alcanzable a través de él; para un destino `https://`
el proxy se usa vía `CONNECT`, y la inspección de certificado (más abajo) todavía
aplica al certificado del destino. `json:` serializa el valor y establece `Content-Type:
application/json` (sobreescríbelo vía `headers`); `body:` envía una cadena cruda. La
respuesta solo se lee cuando `expect_body`/`expect_json` está establecido (limitado a 1 MiB).
`expect_json` busca **rutas con puntos** en objetos anidados. Un valor escalar es una
comprobación de igualdad (comparado como cadena); un mapeo `{op, value}` usa un operador —
`>`, `>=`, `<`, `<=` (numérico), `==`, `!=`, `contains` (cadena), o `=~` (regex).

**Comparaciones de respuesta.** `expect_body` y `expect_latency` usan un mapeo `{op, value}`.
`expect_status` acepta o bien una forma de código/clase/lista o el mismo
mapeo `{op, value}`. Los operadores son `== != > >= < <=` (numérico, o cadena para
`==`/`!=`), `contains` (subcadena) y `=~` (expresión regular Go/RE2) — los
mismos operadores que la comprobación [`sql`](#sql-query-sql):

- `expect_status: { op: "<", value: 500 }` — comparar el código de estado numéricamente
  (además de las formas de código/clase/lista).
- `expect_body: { op: "=~", value: "^OK" }` — comparar el cuerpo de respuesta **recortado**:
  numérico cuando ambos lados parsean como números (`>`, `<`, …), si no igualdad de
  cadenas, coincidencia de subcadena `contains`, o una regex con `=~`.
- `expect_latency: { op: "<", value: 800 }` — fallar cuando el tiempo de respuesta en
  milisegundos no satisface la comparación.

Los datos del resultado llevan `status` y `latency_ms` para usar en reglas/hooks.

En una URL `https://` la misma comprobación también puede inspeccionar el **certificado del servidor**
presentado en la conexión de la petición, de modo que una comprobación cubre alcanzabilidad *y* salud
TLS. Añade cualquiera de estas claves opcionales (reutilizan la lógica de la comprobación `cert`):

```yaml
checks:
  api:
    type: http
    url: "https://api.example.com/v1/health"
    expect_status: 200
    cert_expires_in_days: 14         # warn this many days before expiry
    cert_verify: true                # verify chain + hostname (default true here)
    cert_on_change: false            # alert on any rotation (leaf fingerprint)
    cert_on_issuer_change: false     # alert when the issuer changes
    cert_on_algorithm_change: false  # alert when the signature algorithm changes
```

La inspección de certificado se activa cuando **cualquier** clave `cert_*` está presente, y
requiere una URL `https` — establecer una en una URL `http://` es un error de
configuración. Un problema de certificado (caducado/aún no válido, dentro de la
ventana `cert_expires_in_days`, fallo de verificación, o un cambio entre ciclos)
**hace fallar** la comprobación `http`, manteniendo su semántica de estilo salud (`OK == true`
significa sano), la misma polaridad que la comprobación `cert` independiente. Cuando
la inspección se ejecuta, los datos del resultado llevan los mismos campos de certificado que la comprobación `cert`
expone (`issuer`, `subject`, `dns_names`, `not_after`, `days_left`,
`fingerprint`, …). Para leer el certificado incluso cuando está caducado o de otro modo
inválido, la petición omite la verificación a nivel de transporte y verifica la cadena
manualmente; `cert_verify: false` desactiva esa verificación. Las condiciones de cambio son
**stateful** (recuerdan el ciclo previo). Funcionan en comprobaciones de servicio
y watches de host mientras la misma instancia de comprobación esté viva, y se reinician cuando el
worker del servicio o el watch se reconstruye. Para endpoints TLS crudos o archivos de certificado
locales, usa la comprobación [`cert`](#cert) independiente.

**HTTP/3 (QUIC).** Establece `http3: true` para enviar la petición sobre **HTTP/3** (QUIC,
UDP) en lugar de TCP:

```yaml
checks:
  api-h3:
    type: http
    url: "https://api.example.com/health"   # https only (QUIC is always TLS 1.3)
    http3: true
    expect_status: 200
    expect_latency: { op: "<", value: 300 }
```

Todas las aserciones de arriba (estado, cuerpo, JSON, latencia, métodos, e inspección de
certificado) funcionan igual sobre HTTP/3. El transporte QUIC **nunca recae a
TCP**, así que un servidor que no habla HTTP/3 — o un UDP/443 bloqueado — hace que la
petición falle y **dispara la alerta/hook de la comprobación**, que es como monitorizas que
HTTP/3 sigue disponible. El protocolo negociado se reporta en los datos del resultado como
`protocol` (p. ej. `HTTP/3.0`; para comprobaciones normales es `HTTP/2.0` o `HTTP/1.1`).
HTTP/3 requiere una URL `https` y no puede combinarse con `proxy` (ambos rechazados
en la validación de configuración). Usa `github.com/quic-go/quic-go` (Go puro).

### Cert

Una comprobación `cert` inspecciona material TLS — o bien un **endpoint TLS en vivo** (`host`) o
un **archivo local** (`path`). Es de estilo salud: `OK == true` significa que el certificado
o el material de clave es aceptable, y cualquier problema de certificado configurado hace que la
comprobación falle (`OK == false`). En reglas, alerta sobre problemas de certificado con
`failed: {check: api-cert}`. Como watch, el hook/notify se dispara cuando la comprobación
falla.

```yaml
checks:
  api-cert:                    # live endpoint
    type: cert
    host: api.example.com      # host XOR path (exactly one required)
    port: 443                  # optional, default 443
    server_name: api.example.com   # optional SNI + hostname to verify (default = host)
    expires_in_days: 14        # optional: warn this many days before expiry
    cert_verify: true          # optional, default true: chain + hostname + validity
    on_algorithm_change: true  # optional: alert when the signature algorithm changes
    on_issuer_change: true     # optional: alert when the issuer (CA) changes / re-issue
    on_change: false           # optional: alert on any certificate rotation (fingerprint)

  tls-keypair:                 # local file
    type: cert
    path: /etc/ssl/private/api.key   # host XOR path
    on_change: true            # optional: alert if the file's fingerprint changes

rules:
  alert-api-cert:
    if:
      failed: { check: api-cert }
    then:
      action: alert
      message: "api.example.com certificate is invalid, expiring soon or changed"
```

**Fuente host.** Falla cuando el certificado está **caducado o aún no es válido**,
**caduca dentro de `expires_in_days`**, falla la **verificación** de cadena/hostname
(`cert_verify`, activa por defecto — detecta autofirmados, host equivocado, cadenas caducadas), o —
entre ciclos — su **algoritmo de firma**, **emisor** o **fingerprint** cambia.
Un error de red/TLS al obtener el cert **no** es un fallo de `cert` (usa una
comprobación `tcp`/`http` para alcanzabilidad).

**Fuente archivo (`path`).** Lee y parsea un archivo local, reconociendo de forma nativa (sin
herramientas externas): **certificado** PEM, **solicitud de certificado** (CSR), **claves privadas**
PKCS#1 / EC / PKCS#8, **clave pública** PKIX, clave privada **OpenSSH**, y **clave pública**
**OpenSSH** (línea `authorized_keys`). Los certificados se comprueban por caducidad/validez como
arriba; el material que no caduca (claves, CSRs) falla solo con
`on_change`/`on_algorithm_change`. Un **archivo ausente, ilegible o no parseable hace
fallar la comprobación** (un problema de configuración local, a diferencia de un error de red
transitorio). `cert_verify`, `port` y `server_name` no aplican a archivos.

**Los datos del resultado** exponen `kind` (certificate / certificate_request / private_key /
public_key / openssh_private_key / openssh_public_key / …), `source`,
`signature_algorithm`, `public_key_algorithm`, `key_bits`, `subject` y
`fingerprint`. Los certificados exponen adicionalmente `days_left`, `not_before`,
`not_after`, `issuer`, `serial_number` (hex) y `dns_names` (SANs).

Las condiciones de cambio son **stateful** (recuerdan el valor previo entre
ciclos). Funcionan en comprobaciones de servicio y watches de host mientras la misma instancia de
comprobación esté viva; la línea base se reinicia cuando el worker del servicio o el watch se
reconstruye, por ejemplo tras una recarga de configuración.

Cada comprobación tiene un `timeout` opcional (si no `engine.default_timeout`) y un
`interval` opcional para ejecutarla con menos frecuencia que el ciclo del worker — cada
`round(interval / resolution)` ciclos, reutilizando su último resultado entre tanto (ver
[intervalo por comprobación](configuration.es.md#per-check-interval)).

### Conexión a base de datos (`mysql` / `mariadb`)

Una comprobación de protocolo de conexión se conecta a un servidor sobre su protocolo de cable y
verifica que responde; el tipo de comprobación **es** el nombre del protocolo. Unas pocas convenciones
mantienen cortas las entradas por protocolo:

- **`tls`** (donde se lista) acepta `false` (texto plano, el valor por defecto), `true`
  (TLS verificado) o `skip-verify` (TLS sin verificación de certificado). Las entradas
  añaden solo notas específicas del protocolo — el puerto de TLS implícito (p. ej. IMAPS 993) o
  modos extra (p. ej. los sslmodes de PostgreSQL).
- **Auth** se anota por entrada; muchos protocolos son anónimos.
- **`socket`** (una ruta de socket Unix) marca el socket en lugar de `host`/`port`;
  **`query`** es el objetivo de búsqueda por protocolo (p. ej. el nombre DNS para `dns`).

Protocolos, en el orden de la tabla de arriba:

- `mysql` (alias `mariadb`) — puerto por defecto 3306; `tls` soportado. `user` es
  **opcional**: sin usuario/contraseña lee el paquete inicial de handshake del servidor
  (enviado antes de auth) para probar vivacidad e informar de la versión — sin
  credenciales, como las sondas de saludo smtp/amqp. Con un usuario/contraseña
  autentica y hace ping vía `github.com/go-sql-driver/mysql` (la comprobación más
  profunda). Un handshake ERR (host bloqueado, demasiadas conexiones) hace fallar la sonda.
- `mongodb` (alias `mongo`) — puerto por defecto 27017; `tls` soportado. `user` es
  **opcional** (MongoDB puede ejecutarse sin auth); con credenciales autentica
  contra `auth_source` (por defecto `database`, luego `admin`). Conecta, verifica
  un `ping`, y lee la versión vía `buildInfo`. Un `hello` (con el legacy
  `isMaster` como respaldo) informa del `role` del replica-set
  (`primary`/`secondary`/`arbiter`/`standalone`), `set_name` y `read_only`, de modo que
  una regla `expect:` puede afirmar p. ej. `role == primary`. Para ejecutar una consulta y comparar
  un resultado, ver la comprobación **Consulta MongoDB**. Usa `go.mongodb.org/mongo-driver`.
- `postgres` (alias `postgresql`) — puerto por defecto 5432; `tls` soportado, más los
  sslmodes de PostgreSQL (`disable`/`require`/`prefer`/`verify-ca`/`verify-full`).
  Usa `github.com/lib/pq`.
- `redis` (alias `valkey`) — puerto por defecto 6379; `tls` soportado. `user` es
  **opcional** (el legacy `requirepass` usa solo una contraseña, o ninguna auth en absoluto); una
  comprobación solo con contraseña envía `AUTH <password>`. Verifica `PING` → `PONG` sobre RESP
  (sin driver). Un único `INFO` luego informa de la `version` del servidor (combínalo con
  `on_version_change`) más campos de salud expuestos para `expect:`: `role`,
  `master_link_status` (réplicas), `rdb_last_bgsave_status`,
  `aof_last_write_status`, `loading`, `used_memory`, `maxmemory`,
  `mem_fragmentation_ratio`, `connected_clients` y `uptime_seconds`.
- `memcached` (alias `memcache`) — puerto por defecto 11211; `socket` soportado (socket
  Unix), `tls` soportado. Sin auth (el protocolo de texto ASCII). Envía un único
  comando `stats` y verifica que el servidor responde con líneas `STAT` terminadas por
  `END` — prueba de que el daemon está activo. Informa de la `version` del servidor (combínalo con
  `on_version_change`) más contadores expuestos para `expect:`: `uptime`,
  `curr_connections`, `total_connections`, `rejected_connections`, `cmd_get`,
  `cmd_set`, `get_hits`, `get_misses`, `curr_items`, `total_items`, `bytes`,
  `evictions`, `limit_maxbytes` y `threads` (todos numéricos, así que `>`/`<`/`==` funcionan).
- `imap` — puerto por defecto 143; `tls` soportado (TLS implícito / IMAPS — usa puerto
  993). `user` es **opcional**: sin credenciales verifica que el servidor saluda
  `* OK`; con un usuario/contraseña realiza un `LOGIN` IMAP. RFC 3501.
- `pop` (alias `pop3`) — puerto por defecto 110; `tls` soportado (POP3S — usa puerto 995).
  `user` es **opcional**: anónimo verifica el saludo `+OK`; con un
  usuario/contraseña realiza `USER`/`PASS`. RFC 1939.
- `smtp` — puerto por defecto 25; `tls` soportado (SMTPS — usa puerto 465; submission
  587). `user` es **opcional**: anónimo comprueba el saludo `220` + `EHLO`; con
  un usuario/contraseña realiza `AUTH PLAIN`. RFC 5321.
- `nntp` (alias `nntps`) — puerto por defecto 119; `tls` soportado (NNTPS — usa puerto
  563). `user` es **opcional**: anónimo comprueba el saludo (`200` posting
  permitido / `201` prohibido — reportado como `posting_allowed`); con un usuario/contraseña
  realiza `AUTHINFO USER`/`PASS`. RFC 3977/4643.
- `ftp` — puerto por defecto 21; `tls` soportado (FTPS — usa puerto 990). `user` es
  **opcional**: anónimo comprueba el saludo `220`; con un usuario/contraseña
  realiza `USER`/`PASS` (una contraseña sin usuario inicia sesión como `anonymous`). RFC 959.
- `ssh` — puerto por defecto 22 (sin `tls`: SSH tiene su propia cripto de transporte). `user` es
  **opcional**: anónimo completa el intercambio de claves para capturar la clave de host
  del servidor (la autenticación luego falla, lo cual es esperado); con un usuario/contraseña el login
  debe tener éxito. Datos del resultado: `fingerprint` (SHA256 de la clave de host),
  `host_key_algo`, `server_version`, `protocol`. Establece **`on_change: true`** para
  alertar cuando el fingerprint de la clave de host cambia — un posible re-key o
  man-in-the-middle. Usa `golang.org/x/crypto/ssh`.
- `fpm` (alias `php-fpm`) — PHP-FPM sobre FastCGI. Establece `socket` al socket Unix del
  pool (p. ej. `/run/php/php8.2-fpm.sock`), o usa `host`/`port` (por defecto 9000) para
  un pool TCP. Sin auth. Realiza una petición FastCGI a `/ping` y espera `pong`, así que
  el pool debe tener **`ping.path = /ping`** habilitado. Establece **`status_path`** (el
  `pm.status_path` del pool) para además obtener la página de estado y exponer métricas
  del pool para `expect:`: `pool`, `process_manager`, `active_processes`,
  `idle_processes`, `total_processes`, `listen_queue`, `max_listen_queue`,
  `max_active_processes`, `max_children_reached`, `slow_requests`,
  `accepted_conn` y `uptime_seconds`.
- `dns` — puerto por defecto 53 (UDP). Sin auth. Envía una consulta `A` para `query` (por defecto
  `localhost`) y verifica la respuesta: `NOERROR`/`NXDOMAIN` pasan (el servidor está activo
  y hablando DNS); `SERVFAIL`, `REFUSED`, un timeout o un error de transporte fallan.
  Datos del resultado: el `rcode`, el número de respuestas y las `addresses` resueltas (los
  registros A/AAAA de la respuesta, ordenados y unidos por comas) — de modo que `expect` puede requerir una
  resolución real (`rcode: NOERROR`, `answers: {op: ">", value: 0}`) o una
  dirección concreta (`addresses: {op: "=~", value: "93\\.184\\..*"}`). Establece
  `query` a un nombre que el servidor deba responder (p. ej. una zona para la que sea autoritativo).
  Con `resolvconf: true` (en lugar de `host`, mutuamente exclusivos) la
  sonda pregunta al primer `nameserver` de `/etc/resolv.conf` — el servidor que el
  sistema preguntaría primero; con el `usepeerdns` de pppd, el resolver del
  proveedor, que es como el servicio del catálogo `pppd` verifica la resolución a través del
  uplink. Si ese resolver es local al host (loopback como
  `127.0.0.0/8`/`::1`, o cualquier dirección asignada a una interfaz local), un
  pin de `interface` se ignora para el paquete DNS porque el resolver debe
  alcanzarse localmente. RFC 1035.
- `ntp` — puerto por defecto 123 (UDP). Sin auth. Envía una petición de cliente y verifica que el
  servidor responde en **modo servidor** con un **estrato (1–15)** sincronizado; una
  respuesta kiss-o'-death (estrato 0) o no sincronizada (estrato 16) falla. Datos
  del resultado: `stratum`, el `offset_seconds` del reloj, el indicador `leap`
  (`none`/`add-second`/`del-second`/`unsynchronized`), `precision_seconds`,
  `root_delay_ms`, `root_dispersion_ms` y el `reference_id` (una etiqueta de refclock
  de estrato-1 como `GPS`, o la IP del servidor superior). Así una regla `expect:`
  puede afirmar p. ej. `leap == none` o un techo de `root_dispersion_ms`. RFC 5905.
- `snmp` — puerto por defecto 161 (UDP). Con **ningún `user`** usa **SNMPv2c** con una
  cadena de comunidad (`password`, por defecto `public` — el modelo anónimo/de secreto-compartido).
  Con un **`user`** usa **SNMPv3 USM**: una `password` añade autenticación SHA
  (authNoPriv), de lo contrario noAuthNoPriv. Lee el grupo del sistema;
  los datos del resultado llevan `sys_object_id`, `snmp_version`, la descripción (como el
  banner de versión) y — cuando el agente las expone — `sys_name`, `sys_contact`,
  `sys_location` y `sys_uptime_seconds` (afirmables vía `expect:`). Establece
  **`on_change: true`** para alertar cuando `sysObjectID` (la identidad del dispositivo —
  modelo/firmware) cambia. Usa `github.com/gosnmp/gosnmp`.
- `tftp` — puerto por defecto 69 (UDP). Sin auth. Envía una petición de lectura (RRQ) para `query`
  (por defecto `sermo-tftp-check`) y verifica un paquete TFTP válido: una respuesta `DATA`
  (el archivo se sirve) o una respuesta `ERROR` (p. ej. archivo no encontrado) ambas pasan. Datos
  del resultado: el tipo de respuesta y, para un error, el código/mensaje de error TFTP. RFC 1350.
- `ldap` — puerto por defecto 389; `tls` soportado (TLS implícito / LDAPS — usa puerto
  636). `user` es **opcional**: sin credenciales hace un **bind anónimo** (un
  bind exitoso, o un rechazo a nivel LDAP, ambos prueban que el directorio está activo —
  solo un error de transporte falla); con un usuario/contraseña hace un **bind simple**
  donde `user` es el DN de bind y debe tener éxito. Datos del resultado: el modo de bind y el
  resultado. Usa `github.com/go-ldap/ldap/v3`.
- `ajp` — puerto por defecto 8009 (TCP). Sin auth. Envía un **CPing AJP13** y espera un
  **CPong** — la misma sonda de vivacidad que Apache/nginx usan contra el conector AJP de
  Tomcat.
- `ipp` (alias `cups`) — puerto por defecto 631; `tls` soportado (IPPS). Sin auth. POSTea
  una petición IPP `CUPS-Get-Default` sobre HTTP y verifica una respuesta IPP válida —
  cualquier respuesta parseable prueba que cupsd está activo y hablando IPP. Datos del resultado: la
  versión IPP y el estado. RFC 8010/8011.
- `rsync` (alias `rsyncd`) — puerto por defecto 873 (TCP). Sin auth. Lee el saludo
  `@RSYNCD: <version>` del daemon rsync; recibirlo prueba que el daemon está activo.
  Los datos del resultado llevan la versión del protocolo.
- `dhcp` (alias `dhcpd`) — puerto por defecto 67 (UDP). **Solo Linux.** Sin auth. Envía un
  `DHCPDISCOVER` y verifica que el servidor responde con un `DHCPOFFER` — prueba de que está
  activo y entregando leases. Nunca envía un `DHCPREQUEST`, así que **no se consume
  ningún lease real**. Dos modos: establece `interface` para hacer **broadcast** del DISCOVER por ese
  enlace y descubrir cualquier servidor (`255.255.255.255`); omítelo para hacer **unicast** a
  `host` (un servidor o relay conocido). La dirección de hardware del cliente es una MAC aleatoria,
  anónima y de administración local por defecto; establece `mac` para usar una dirección fija
  (p. ej. un servidor que solo responde a clientes reservados). Datos del resultado: la IP ofrecida, el
  server id, la máscara de subred y el tiempo de lease. **Requiere privilegios elevados** para ligar
  el puerto 68 del cliente DHCP (y `CAP_NET_RAW` para el bind por interfaz), como la
  comprobación `icmp`; el host no debería ejecutar un cliente DHCP competidor en esa interfaz.
  RFC 2131.

  ```yaml
  checks:
    dhcp-broadcast:
      type: dhcp
      interface: eth0            # broadcast on this link (discovers any server)
      mac: "02:00:00:ab:cd:ef"   # optional; default is a random anonymous MAC
    dhcp-unicast:
      type: dhcp
      host: 10.0.0.1             # unicast to a known server/relay (no interface)
  ```
- `dhclient` (alias `dhcp-client`) — puerto por defecto 68 (UDP). **Solo Linux.** Esta
  es una comprobación de cliente DHCP local: `dhclient` recibe ofertas en UDP/68 y no
  provee un protocolo de servidor petición/respuesta. La comprobación lee `/proc/net/udp` y
  pasa cuando encuentra un socket UDP local ligado a `host:port` (`0.0.0.0:68` por
  defecto en el servicio de catálogo empaquetado). No envía paquetes y no consume
  un lease. Establece `lease_file` (el servicio de catálogo empaquetado usa por defecto
  `/var/lib/dhcp/dhclient.leases`; sobreescríbelo cuando tu distribución guarde los leases de
  ISC dhclient en otro lugar) para además requerir un lease no caducado. Si `interface`
  está establecido, el lease debe pertenecer a esa interfaz.
- `rspamd` — puerto por defecto 11334 (el worker controlador); `tls` soportado (HTTPS).
  Sin auth. Envía `GET /ping` y espera `200` con un cuerpo `pong` — el
  endpoint de vivacidad no autenticado que cada worker rspamd expone (apunta `port` a
  11333 para el worker de escaneo normal o 11332 para el proxy). Datos del resultado: la
  versión de rspamd, leída de la cabecera `Server`.
- `libvirt` (alias `libvirtd`) — abre una conexión RPC a un daemon libvirt y
  lee su versión; que ambas tengan éxito prueba que libvirtd está activo. No ejecuta operación
  de escritura. **Transporte:** sin `socket` y sin `host` marca el socket Unix local
  `/run/libvirt/libvirt-sock`; establece `socket` para una ruta distinta como
  `/run/libvirt/virtqemud-sock` en hosts libvirt modulares, o establece `host` para usar
  **TCP** plano (puerto por defecto 16509). TLS/SASL no está soportado.
  **URI de conexión:** `query` selecciona el driver, por defecto `qemu:///system` (p. ej.
  `lxc:///`, `xen://`). Sin auth — el acceso al socket local está gobernado por los
  permisos/polkit del socket. Usa `github.com/digitalocean/go-libvirt`.

  Más allá de la vivacidad expone variables para condiciones (de mejor esfuerzo — un driver que
  las rechaza todavía reporta activo): **`domains.active`** (VMs en ejecución),
  `domains.inactive`, `domains` (total), y la capacidad del nodo `node.cpus`,
  `node.memory_mb`. Establece **`domain`** a un nombre de VM para además leer su `domain.state`
  (`running`/`paused`/`shutoff`/`crashed`/…) y `domain.running`; `on_change` entonces
  alerta sobre las transiciones de estado de esa VM, y un dominio desconocido hace fallar la comprobación.
  Los datos del resultado también llevan la versión de libvirt, el URI de conexión, el transporte y el hostname.

  ```yaml
  checks:
    libvirt-local:
      type: libvirt              # dials /run/libvirt/libvirt-sock
      expect:
        domains.active: { op: ">=", value: 3 }   # alert if fewer than 3 VMs are running
    libvirt-modular:
      type: libvirt
      socket: /run/libvirt/virtqemud-sock
      query: "qemu:///system"
    libvirt-tcp:
      type: libvirt
      host: 10.0.0.4             # plain TCP on 16509
      query: "qemu:///system"    # optional connect URI (default qemu:///system)
    db-vm:
      type: libvirt
      domain: db01               # watch a single VM
      on_change: true            # alert on its state transitions
      expect:
        domain.state: { op: "==", value: running }
  ```
- `dbus` — se conecta a un daemon D-Bus y completa su handshake de auth SASL +
  `org.freedesktop.DBus.Hello` — que por sí solo prueba que el bus está activo — luego
  llama a `org.freedesktop.DBus.GetId` para leer el UUID del bus. No ejecuta operación de
  escritura. **Objetivo:** por defecto el bus del sistema
  (`unix:path=/run/dbus/system_bus_socket`); establece `socket` para una ruta de socket
  distinta, o `query` para una dirección D-Bus completa (`unix:abstract=…`,
  `tcp:host=…,port=…`). Basado en socket, así que no hay puerto TCP. Sin auth — el acceso está
  gobernado por los permisos del socket. Datos del resultado: el id del bus, la dirección y el
  nombre único de la conexión. Usa `github.com/godbus/dbus/v5`.

  ```yaml
  checks:
    dbus-system:                 # dials unix:path=/run/dbus/system_bus_socket
      type: dbus
    dbus-custom:
      type: dbus
      socket: /run/dbus/system_bus_socket   # or use `query` for a full address
  ```
- `udisks2` — el daemon de gestión de discos UDisks2 en el bus D-Bus del sistema. Se conecta
  al bus (auth SASL + Hello), verifica que `org.freedesktop.UDisks2` tiene un
  propietario de nombre, y llama a `org.freedesktop.DBus.Peer.Ping` en
  `/org/freedesktop/UDisks2/Manager` — prueba de que el servicio está registrado y
  respondiendo, no meramente que `dbus-daemon` está activo. **Objetivo:** como `dbus`, por defecto
  el bus del sistema; establece `socket` para un socket de bus distinto o `query` para una dirección
  D-Bus completa. Basado en socket, sin puerto TCP, sin auth. Datos del resultado: el nombre único de D-Bus
  que posee `org.freedesktop.UDisks2`. Usa `github.com/godbus/dbus/v5`.

  ```yaml
  checks:
    udisks2:
      type: udisks2
      timeout: 5s
  ```
- `avahi` (alias `avahi-daemon`) — el daemon Avahi mDNS/DNS-SD (zeroconf), sondeado
  sobre su API D-Bus (`org.freedesktop.Avahi`). Se conecta al bus del sistema (auth SASL
  + Hello) y llama a `org.freedesktop.Avahi.Server.GetVersionString` — una respuesta
  prueba que avahi-daemon está activo y registrado en el bus — reportando la `version`
  (combínalo con `on_version_change`) y, de mejor esfuerzo, el `hostname` y el `state`
  del servidor (`running` cuando AVAHI_SERVER_RUNNING). **Objetivo:** como `dbus`, por defecto
  el bus del sistema; establece `socket` para un socket de bus distinto o `query` para una dirección
  D-Bus completa. Basado en socket, sin puerto TCP, sin auth. Usa
  `github.com/godbus/dbus/v5`.
- `syncthing` — puerto por defecto 8384; `tls` soportado (`skip-verify` cubre
  el certificado autofirmado por defecto de la GUI de Syncthing). Envía `GET /rest/noauth/health`
  y espera `200` con `{"status":"OK"}` — el endpoint de vivacidad no autenticado.
  Con una **clave de API** en `password` (enviada como `X-API-Key`) también lee
  `/rest/system/version` e informa de la versión de Syncthing (`os`/`arch` también); una
  clave rechazada hace fallar la comprobación. Sin usuario.

  ```yaml
  checks:
    syncthing:
      type: syncthing
      host: 127.0.0.1
      # tls: skip-verify            # if the GUI is on HTTPS
      # password: "${env:ST_KEY}"   # optional API key -> also reports version
  ```
- `unifi` (alias `unifi-controller`, `unifi-network`) — un controlador UniFi Network
  (Ubiquiti). Puerto por defecto 8443, **solo HTTPS** con un certificado
  autofirmado, así que `tls` aquí selecciona solo la verificación: está **omitida por
  defecto**; establece `tls: true` para requerir un certificado válido. Sin usuario. Envía `GET
  /status` (el endpoint de vivacidad no autenticado) y espera `200` con JSON
  `meta.rc == "ok"`, reportando `server_version` (combínalo con `on_version_change`) y
  `uuid`. Apunta a la aplicación UniFi Network autoalojada; en una consola UniFi OS
  (UDM/Cloud Key) el controlador está proxyado bajo `/proxy/network/`, que esta
  comprobación no sigue.
- `influxdb` (alias `influx`) — un servidor InfluxDB. Puerto por defecto 8086; `tls`
  soportado (`true`/`skip-verify` → https; HTTP plano por defecto). Sin auth. GETea
  `/health` (InfluxDB 2.x / 1.8+) y verifica un `status` JSON de `pass`, reportando
  la `version` del servidor (combínalo con `on_version_change`); en servidores más antiguos sin
  `/health` recae a `/ping`, que responde `204` con la versión en la cabecera
  `X-Influxdb-Version`. Una comprobación de vivacidad/versión; para ejecutar una consulta InfluxQL
  y comparar un resultado, ver la comprobación **Consulta InfluxDB**.
- `prometheus` (alias `prom`) — un servidor Prometheus. Puerto por defecto 9090; `tls`
  soportado (https). GETea `/api/v1/status/buildinfo` y verifica un `status`
  `success`, reportando la `version` del servidor (combínalo con `on_version_change`); en servidores
  más antiguos recae a `/-/healthy` (solo vivacidad). Un `user`/`password` opcional
  se envía como auth HTTP Basic (para un reverse proxy delante de la API).
- `cloudflared` (alias `cloudflare-tunnel`) — el endpoint de métricas local de Cloudflare
  Tunnel. Puerto por defecto 60123; `tls` soportado (https, texto plano por defecto).
  GETea `/metrics`, requiere HTTP 200, y verifica que el texto Prometheus
  contiene nombres de métrica `cloudflared_`. Esto confirma que el propio endpoint del daemon
  cloudflared está respondiendo en lugar de solo comprobar que TCP acepta conexiones.
- `clamd` (alias `clamav`) — puerto por defecto 3310 (TCP), o un socket Unix vía `socket`
  (p. ej. `/run/clamav/clamd.ctl`). Sin auth, sin TLS. Envía el comando `VERSION` de clamd
  y verifica una respuesta `ClamAV <version>/…`. Datos del resultado: la `version` del motor (la
  parte de la base de datos diaria de firmas se descarta, de modo que `on_version_change` se mantiene en silencio
  a través de actualizaciones rutinarias de la DB) y el `version_string` completo.
- `spamd` (alias `spamassassin`) — puerto por defecto 783 (TCP), o un socket Unix vía
  `socket`. Sin auth. Envía un `PING` SPAMC/SPAMD y verifica que spamd responde
  `SPAMD/<v> 0 PONG`. Datos del resultado: la versión del protocolo SPAMD.
- `nut` (alias `ups`, `upsd`) — el upsd de NUT (Network UPS Tools); puerto por defecto 3493
  (TCP), `tls` soportado (TLS implícito — la actualización `STARTTLS` de upsd no se usa).
  `user`/`password` son **opcionales**: de forma anónima envía `VER` e informa de la
  `version` de upsd (combínalo con `on_version_change`). Con credenciales hace `LOGIN`
  al UPS para verificar el acceso (`USERNAME`/`PASSWORD` solos no son comprobados por upsd).

  Establece **`ups`** al nombre del dispositivo (u omítelo cuando el servidor tiene un único UPS —
  se autodetecta) para leer sus variables en el resultado, donde alertas sobre
  ellas con `expect` o sobre cambios de estado con `on_change`. Variables expuestas
  (cuando están presentes): `ups.status` (el estado de alimentación/batería — `OL` en línea, `OB` con
  batería, `LB` batería baja, `RB` reemplazar batería, `CHRG`/`DISCHRG` …), `ups.load`,
  `ups.temperature`, `ups.power`/`ups.realpower`, `battery.charge`,
  `battery.charge.low`, `battery.runtime`/`battery.runtime.low`, `battery.voltage`,
  `input.voltage`, `input.frequency`, `output.voltage`, `ups.mfr`, `ups.model`. Un
  `ups` desconocido hace fallar la comprobación.

  ```yaml
  checks:
    ups:
      type: nut
      host: 192.168.1.10
      ups: myups                              # omit to auto-detect a single UPS
      user: monuser                           # optional (verifies access via LOGIN)
      password: ${env:NUT_PASS}
      on_change: true                         # alert on any ups.status transition
      expect:
        ups.status: { op: "=~", value: "OL" }  # alert when not online (use =~: status is "OL CHRG")
        battery.charge: { op: ">", value: 30 } # alert when charge drops to 30%
  ```

  `on_change` rastrea `ups.status`; para la versión del software upsd usa
  `on_version_change`. Como `ups.status` es una lista de flags separados por espacios (p. ej.
  `OL CHRG`), compáralo con `=~` en lugar de `==`.
- `docker` — la API del motor Docker. Por defecto habla con el socket Unix local
  `/run/docker.sock`; establece `host` (y `port`, por defecto 2375 / 2376 con `tls`)
  para un daemon TCP, o `socket` para una ruta no por defecto. Sin `user`. GETea `/info`
  (probando que el daemon está activo), informa de la `version` del motor (combínalo con
  `on_version_change`), y expone recuentos: **`containers`**,
  **`containers.running`**, `containers.paused`, `containers.stopped`, `images`,
  y `warnings` (número de advertencias del daemon). Establece **`container`** (nombre o id) para
  además leer el `container.status` de ese contenedor (`running`/`exited`/`restarting`/…),
  `container.health` (`healthy`/`unhealthy`/`starting`/`none`), `container.running`,
  `container.restartcount` y `container.exitcode`; `on_change` entonces alerta sobre sus
  transiciones de estado/salud. Un contenedor desconocido hace fallar la comprobación.

  ```yaml
  checks:
    docker:
      type: docker                            # local socket by default
      expect:
        containers.running: { op: ">=", value: 4 }  # alert if fewer than 4 are up
        containers.stopped: { op: "==", value: 0 }  # alert on any stopped container
        warnings: { op: "==", value: 0 }            # alert on daemon warnings
    web-container:
      type: docker
      container: web                          # watch one container
      on_change: true                         # alert on status/health transitions
      expect:
        container.health: { op: "==", value: healthy }
        container.restartcount: { op: "<", value: 5 } # alert on a restart loop
  ```

  Condiciones más interesantes: `containers.running` (servicios esperados activos),
  `containers.stopped` (contenedores caídos/salidos), `status`/`health` por `container`
  y `restartcount`. La comprobación Docker es de solo lectura. Para permitir que Sermo arranque, pare,
  reinicie o reanude ese mismo contenedor a través del motor de operaciones seguras, añade un
  bloque `control: { type: docker, container: ... }` a nivel de servicio.
  y `restartcount` (bucles de reinicio), `warnings`, y `on_version_change` para
  actualizaciones del motor.
- `smb` (alias `samba`, `cifs`) — puerto por defecto 445 (TCP). `user` es **opcional**.
  Primero ejecuta un `NEGOTIATE` SMB2 (probando que el servidor está activo) e informa del
  **dialecto** negociado como la `version` (`2.0.2`/`2.1`/`3.0`/`3.0.2`/`3.1.1` —
  combínalo con `on_version_change`), la familia `protocol` (`SMB2`/`SMB3`) y si el
  **firmado es requerido**. Con un `user` luego autentica sobre **NTLM** (un
  login fallido hace fallar la comprobación), cuenta los shares (`shares`), y — si un share
  está nombrado en `query` — verifica que puede **montarse** (`share_access`). El dominio
  puede ir embebido en `user` (`DOMAIN\user` o `user@domain`). El NEGOTIATE es
  nativo; la sesión autenticada usa `github.com/cloudsoda/go-smb2`.

  ```yaml
  checks:
    fileserver:
      type: smb
      host: 10.0.0.9
      user: "WORKGROUP\\monitor"     # optional; enables NTLM auth + share checks
      password: "${env:SMB_PASS}"
      query: "data"                   # optional: verify this share mounts
  ```
- `acpid` — el daemon de eventos ACPI. **Solo socket** (sin puerto TCP; por defecto
  `/run/acpid.socket`, sobreescribir con `socket`). Es un difusor de eventos sin
  protocolo petición/respuesta, así que la comprobación es la **conexión en sí**: una
  conexión exitosa prueba que acpid está escuchando (un socket obsoleto dejado por un daemon
  muerto rechaza la conexión). No lee nada — leer bloquearía hasta un
  evento ACPI — y no hay versión. Sin auth.
- `fail2ban` — fail2ban-server. **Solo socket** (por defecto
  `/run/fail2ban/fail2ban.sock`, sobreescribir con `socket`). Su protocolo de comandos
  pickle de Python no vale la pena reimplementarlo para una comprobación de vivacidad, así que — como
  `acpid` — la comprobación es la **conexión en sí**; no intercambia comandos. Sin auth.
- `lvmpolld` — el daemon de poll de LVM. **Solo socket** (por defecto
  `/run/lvm/lvmpolld.socket`, sobreescribir con `socket`). A diferencia de acpid/fail2ban es
  sondeado por protocolo: habla el framework de daemon genérico de LVM, así que la comprobación envía
  una petición `hello` y verifica que el daemon responde `OK`, protegiendo también contra un
  daemon LVM distinto (lvmetad, dmeventd) por el nombre de protocolo reportado. Datos del
  resultado: el `protocol` y `protocol_version` (el handshake no expone versión de
  software lvm2). Sin auth.
- `rpcbind` (alias `portmap`, `portmapper`) — puerto por defecto 111 (UDP). Sin auth.
  Envía una llamada **ONC RPC NULL** (RFC 5531/1833) al programa portmapper (100000
  v2) y verifica una respuesta RPC bien formada — cualquier respuesta (aceptada o denegada) prueba
  que el daemon está activo y hablando RPC; los datos del resultado llevan el `rpc_status`. La misma
  sonda de llamada NULL respalda las comprobaciones `nfs`/`mountd`/`statd`/`glusterfs` de abajo.
- `nfs` (alias `nfs-server`, `nfsd`) — un ONC RPC NULL al programa NFS
  (100003) sobre TCP (record marking), como `rpcbind`; puerto por defecto 2049. Una
  respuesta de desajuste de versión (p. ej. un servidor solo NFSv4 respondiendo a un NULL v3) todavía
  pasa.
- `mountd` (alias `rpc.mountd`, `nfs-mountd`) — el daemon de montaje NFS: un ONC RPC
  NULL al programa MOUNT (100005) sobre TCP, como `nfs`. **Sin puerto fijo bien
  conocido** — mountd registra un puerto (a menudo aleatorio) con rpcbind; por defecto 20048,
  sobreescribir `port` (encuéntralo con `rpcinfo -p <host>`).
- `statd` (alias `rpc.statd`, `nsm`, `nfs-statd`) — el monitor de estado NFS (NSM,
  usado para recuperación de bloqueos): un ONC RPC NULL al programa NSM (100024), como
  `mountd`. Puerto por defecto 662; mismo caveat de puerto-no-fijo — sobreescribir `port`
  (`rpcinfo -p <host>`).
- `nebula` (alias `nebula-vpn`) — un nodo mesh-VPN [Nebula](https://github.com/slackhq/nebula).
  Puerto por defecto 4242 (**UDP**). Sin auth. Un túnel real necesita un
  certificado firmado por CA, pero un nodo responde a un paquete de datos para un índice de túnel que
  no conoce con un **recv_error** en texto plano (diciéndole al emisor que
  re-haga el handshake), así que la comprobación envía un paquete `message` de Nebula que lleva un
  índice aleatorio y verifica que el nodo responde con un `recv_error` que lo refleja — prueba de
  que el nodo está activo, sin credenciales. La respuesta está gobernada por el ajuste
  `listen.send_recv_error` del nodo (por defecto `always`); un nodo configurado a `never` — o a
  `private` cuando se sondea desde una dirección pública — se mantiene en silencio y se lee como caído, así que
  sondea lighthouses/nodos desde una dirección que su configuración responda.
- `openvpn` (alias `ovpn`) — un servidor OpenVPN. Puerto por defecto 1194; `transport`
  selecciona el transporte (`udp`, el valor por defecto, o `tcp` — coincide con el
  `proto` del servidor). Sin auth. El primer paso del handshake de OpenVPN es no autenticado
  (TLS viene después): la comprobación envía un `P_CONTROL_HARD_RESET_CLIENT_V2` que lleva un
  session id aleatorio y verifica que el servidor responde con un
  `P_CONTROL_HARD_RESET_SERVER_V2` reconociéndolo. Datos del resultado: el `transport`.
  **Caveat:** el reset solo obtiene respuesta de un servidor sin `tls-auth`/
  `tls-crypt`; esos envuelven con HMAC (o cifran) los paquetes de control, así que un reset desnudo
  es descartado — el silencio es entonces esperado y no es prueba de que esté caído.
- `rdp` (alias `ms-wbt-server`) — puerto por defecto 3389 (TCP). Sin auth. Envía una X.224
  **Connection Request** con una RDP Negotiation Request y verifica que el servidor
  responde con un X.224 **Connection Confirm**; un fallo de negociación todavía cuenta
  como activo (el servidor respondió). Datos del resultado: el protocolo de `security` negociado
  (`rdp` = seguridad RDP estándar, `tls`, `hybrid` = CredSSP/NLA, `hybrid-ex`).
  MS-RDPBCGR; la negociación precede a la autenticación, así que sin credenciales.
- `guacd` (alias `guacamole`) — puerto por defecto 4822 (TCP). Sin auth. Abre el
  handshake de Guacamole enviando una instrucción `select` para un protocolo (`query`,
  por defecto `vnc`) y verifica que guacd responde con una instrucción Guacamole bien formada —
  una respuesta `args` (protocolo disponible) o un `error` (p. ej. plugin
  faltante) ambas prueban que guacd está activo. Datos del resultado: el protocolo seleccionado y el
  `opcode` de la respuesta.
- `asterisk` (alias `ami`) — puerto por defecto 5038 (TCP); `tls` soportado (AMI sobre
  TLS). Sin auth. Al conectar, la Manager Interface de Asterisk envía un saludo `Asterisk Call
  Manager/<version>` antes de cualquier login; leerlo produce la `version` del manager
  (los datos del resultado también llevan el `banner` completo). Combínalo con
  `on_version_change` para alertar sobre una actualización de Asterisk.
- `sieve` (alias `managesieve`) — puerto por defecto 4190 (TCP); `tls` soportado
  (TLS implícito). Sin auth. Al conectar el servidor envía un saludo de líneas de capacidad
  terminado por una respuesta `OK` (RFC 5804); leerlo y ver el `OK`
  prueba que el servidor está activo. La capacidad `IMPLEMENTATION` se reporta como la `version`
  del servidor (un saludo `NO`/`BYE`, p. ej. un rechazo por límite de conexión, hace fallar la
  comprobación).
- `mqtt` — puerto por defecto 1883 (TCP); `tls` soportado (MQTTS, puerto 8883). Realiza un
  handshake `CONNECT` MQTT 3.1.1 y verifica que el broker responde `CONNACK`
  aceptando la conexión (código de retorno 0). Sin credenciales es una conexión anónima;
  `user`/`password` autentican. Un CONNACK rechazado (p. ej. `not-authorized`,
  `bad-username-or-password`) hace fallar la comprobación con la razón; datos del resultado: el
  estado `connack`.
- `amqp` (alias `rabbitmq`) — puerto por defecto 5672 (TCP); sin auth. Envía la cabecera
  de protocolo AMQP 0-9-1 y verifica el método Connection.Start no solicitado del broker.
  Informa de la `version` del broker más campos de mejor esfuerzo `product`, `platform`
  y `cluster_name` para `expect`/`on_version_change`.
- `kafka` — puerto por defecto 9092 (TCP); `tls` soportado. Sin auth. Envía una petición
  `ApiVersions` (API key 18, v0), que un broker o un controlador KRaft
  responde antes de la autenticación, y verifica que el correlation id de la respuesta coincide —
  prueba de que el par habla el protocolo de cable de Kafka. Del conjunto de APIs anunciado
  deriva `role` (`broker` cuando la API Produce del plano de datos está presente, `controller`
  cuando la API de quórum `Vote` de Raft lo está, y Produce no) y los flags `produce_api` /
  `vote_api` (`yes`/`no`), más `api_count` y `error_code` — todos afirmables
  vía `expect`. Usado por los servicios de catálogo `kafka-broker` (9092, `expect role=broker`) y
  `kafka-controller` (9093, `expect role=controller`).
- `varnish` (alias `varnishadm`) — puerto por defecto 6082 (TCP, la CLI de gestión `-T` de
  Varnish). Sin auth. Al conectar varnishd envía una respuesta CLI (una línea `<status>
  <length>` y un cuerpo); el estado **200** lleva el banner (con la versión)
  y **107** es un desafío de autenticación (hay un secreto CLI establecido) — cualquiera prueba
  que la CLI de gestión está activa. Datos del resultado: el `cli_status` y, para un banner, la
  `version` de Varnish. La autenticación de secreto CLI no se realiza (solo vivacidad).
- `ceph` (alias `ceph-mon`) — puerto por defecto 3300 (TCP, el messenger v2 del monitor
  Ceph; usa puerto 6789 para el v1 legacy). Sin auth. Al conectar un daemon Ceph envía un
  banner messenger (`ceph v2\n` para v2, `ceph v027` para v1); leer un banner `ceph v`
  prueba que es un endpoint Ceph. Datos del resultado: la versión `messenger`
  (`v1`/`v2`). El banner precede al handshake autenticado, así que sin credenciales.
- `glusterfs` (alias `glusterd`, `gluster`) — puerto por defecto 24007 (TCP, el
  daemon de gestión glusterd). Sin auth. Un ONC RPC NULL al programa de handshake de
  GlusterFS sobre TCP (record marking), como `rpcbind`; los datos del resultado llevan el
  `rpc_status`. **Esto comprueba un nodo.** Para alertar cuando **cualquier nodo** en un cluster
  está caído, configura una comprobación por nodo (un `host` cada una) — la comprobación del nodo
  fallido se dispara:

  ```yaml
  checks:
    gluster-n1: { type: glusterfs, host: 10.0.0.1 }
    gluster-n2: { type: glusterfs, host: 10.0.0.2 }
    gluster-n3: { type: glusterfs, host: 10.0.0.3 }
  ```

  El estado de pares de todo el cluster no se recopila dentro del protocolo (necesitaría RPC de
  gestión GlusterD autenticado).
- `openvswitch` (alias `ovs`, `ovsdb`, `ovsdb-server`) — puerto por defecto 6640 (TCP,
  el servidor de base de datos de configuración de Open vSwitch `ovsdb-server`), o un socket Unix
  vía `socket` (comúnmente `/run/openvswitch/db.sock`); `tls` soportado (SSL). Sin
  auth. Emite una petición JSON-RPC `list_dbs` de OVSDB (RFC 7047) y verifica un resultado
  que lista las bases de datos servidas — los datos del resultado llevan la lista `databases`. Cuando la
  base de datos `Open_vSwitch` está presente le sigue con un `transact` select leyendo
  `ovs_version`, reportado como la `version`.

### Integridad SQLite (`sqlite` / `sqlite3`)

Una comprobación `sqlite` verifica que un archivo de base de datos SQLite local está sano ejecutando
la comprobación de integridad de SQLite. Es una comprobación de **archivo local** (no un protocolo de red).

```yaml
checks:
  app-db:
    type: sqlite
    path: /var/lib/app/app.db   # required
    quick: false                # optional: true runs the faster PRAGMA quick_check
```

Pasa (estilo salud, `OK == true`) cuando `PRAGMA integrity_check` reporta
`ok`. Un archivo ausente/ilegible, un archivo que no es una base de datos SQLite, o
corrupción reportada hacen fallar la comprobación con el detalle. El archivo se abre
**solo lectura**, así que la comprobación nunca lo modifica. `quick: true` ejecuta
`PRAGMA quick_check` (más rápido, omite algunas comprobaciones por fila) para bases de datos grandes.

### Consulta SQL (`sql`)

Una comprobación `sql` ejecuta una consulta contra una base de datos y compara su **resultado escalar**
(la primera columna de la primera fila) contra un `value`. Es de **estilo condición**
(`OK == true` significa que la comparación se cumple), así que en reglas `active: {check: …}`
se dispara sobre ella. Usa los mismos campos de conexión que las comprobaciones MySQL/PostgreSQL
y abre bases de datos SQLite en solo lectura.

```yaml
checks:
  jobs-backlog:
    type: sql
    engine: postgres            # mysql | mariadb | postgres | postgresql | sqlite | sqlite3
    host: 127.0.0.1             # mysql/postgres: host/port/user/password/database/tls
    user: monitor
    password: "${env:PGPASS}"
    database: app
    query: "SELECT count(*) FROM jobs WHERE state = 'queued'"
    op: ">"                     # == | != | > | >= | < | <= | contains | =~
    value: "100"
  schema-version:
    type: sql
    engine: sqlite
    path: /var/lib/app/app.db   # sqlite: a path, opened read-only
    query: "SELECT value FROM meta WHERE key = 'schema'"
    op: "=~"                    # regular expression (Go/RE2)
    value: "^v[0-9]+$"
```

- **Operadores:** `>`, `>=`, `<`, `<=` comparan numéricamente (resultado y `value`
  deben parsear como números); `==` / `!=` comparan numéricamente cuando ambos son números,
  si no como cadenas (igual/diferente); `=~` compara el resultado contra `value`
  como una expresión regular Go (RE2).
- **Motores:** `mysql`/`mariadb` y `postgres`/`postgresql` usan los mismos
  campos de conexión que sus comprobaciones de protocolo (`host`/`port`/`user`/`password`/
  `database`/`tls`) y **requieren un `user`**; `sqlite`/`sqlite3` toman un `path`
  y lo abren en **solo lectura**.
- Los datos del resultado llevan `engine`, `query`, `op`, `threshold`, la cadena cruda `result`
  y, cuando es numérico, un `value` para hooks/reglas. Un error de consulta, una base de datos
  ausente o un resultado `NULL` hacen fallar la comprobación. La comprobación solo lee — apúntala a
  un usuario de solo lectura.

### Consulta MongoDB (`mongodb-query`)

Una comprobación `mongodb-query` ejecuta una consulta MongoDB, compara un **resultado escalar** con
`value`, y es de **estilo condición** (`OK == true` significa que la comparación se cumple).
Usa las mismas variables de conexión que la comprobación de conexión `mongodb`
(`host`/`port`/`user`/`password`/`database`/`tls`, más `auth_source`) y el
driver oficial de MongoDB. Se admiten tres formas de consulta:

```yaml
checks:
  failed-jobs:                    # 1) document count
    type: mongodb-query
    host: 127.0.0.1               # host/port/user/password/database/tls/auth_source
    user: monitor
    password: "${env:MGPASS}"
    database: app
    collection: jobs
    filter: '{"status":"failed"}' # optional JSON filter; default {} (count all)
    op: "<"                       # == | != | > | >= | < | <= | contains | =~
    value: "10"
  queued-jobs:                    # 2) aggregation pipeline (scalar at `result`)
    type: mongodb-query
    database: app
    collection: jobs
    pipeline: '[{"$match":{"state":"queued"}},{"$count":"n"}]'
    result: "n"                   # dotted path into the first result document
    op: ">"
    value: "100"
  connections:                    # 3) database command (scalar at `result`)
    type: mongodb-query
    database: app                 # command runs here; defaults to admin
    command: '{"serverStatus":1}'
    result: "connections.current" # dotted path into the reply
    op: "<"
    value: "5000"
```

- **Formas de consulta** (exactamente una): una **`collection`** (+ `filter` JSON opcional)
  compara el **recuento de documentos** que coinciden; una `collection` + **`pipeline`** JSON
  ejecuta una agregación; un **`command`** ejecuta un comando de base de datos. `pipeline` y
  `command` extraen un escalar en la ruta con puntos **`result`** (un recuento de colección
  no necesita `result`). `filter`/`pipeline`/`command` aceptan **JSON extendido**
  relajado (así que `$oid`, `$date`, etc. funcionan). Una consulta de colección requiere una
  `database`; `command` por defecto es `admin`.
- **Los operadores** se comportan exactamente como los de la comprobación `sql` (`>` `>=` `<` `<=` numérico;
  `==`/`!=` numérico-o-cadena; `contains` subcadena; `=~` regexp RE2).
- **Auth:** con un `user`, las credenciales se comprueban contra `auth_source` (por defecto
  `database`, luego `admin`). La comprobación solo lee — apúntala a un usuario de solo lectura.
- Los datos del resultado llevan `mode`, `op`, `threshold`, el `result` crudo y, cuando es
  numérico, un `value` para hooks/reglas.

### Consulta InfluxDB (`influxdb-query`)

Una comprobación `influxdb-query` ejecuta una consulta InfluxDB, compara un **resultado escalar**
con `value`, y es de **estilo condición** (`OK == true` significa que la comparación
se cumple). Usa las variables de conexión de `influxdb` (`host`/`port`/`user`/
`password`/`tls`). El **`language`** selecciona la API de consulta:

- **`influxql`** (por defecto) — InfluxDB **1.x** `GET /query` contra una `database`.
- **`flux`** — InfluxDB **2.x** `POST /api/v2/query` contra una `org` con un
  `token`.

```yaml
checks:
  cpu-load:                     # InfluxQL (1.x)
    type: influxdb-query
    host: 127.0.0.1             # host/port/user/password/tls (https when tls set)
    user: monitor               # optional: sent as HTTP Basic auth
    password: "${env:INFLUXPW}"
    database: telegraf          # required for influxql
    query: "SELECT mean(usage_user) FROM cpu WHERE time > now() - 5m"
    op: "<"                     # == | != | > | >= | < | <= | contains | =~
    value: "80"
  disk-flux:                    # Flux (2.x)
    type: influxdb-query
    language: flux
    host: 127.0.0.1
    tls: true                   # InfluxDB 2.x is usually https
    org: my-org                 # required for flux
    token: "${env:INFLUX_TOKEN}" # required for flux (Authorization: Token …)
    query: >
      from(bucket: "telegraf")
        |> range(start: -5m)
        |> filter(fn: (r) => r._measurement == "disk" and r._field == "used_percent")
        |> mean()
    op: "<"
    value: "90"
```

- **Selección de escalar.** *InfluxQL* devuelve filas de `[time, …]`; por defecto el
  resultado es la **última columna** de la primera fila de la primera serie (el
  valor agregado, dado que `time` es el primero). *Flux* devuelve CSV anotado; por
  defecto el resultado es la columna **`_value`** de la primera fila de datos. Establece
  **`column`** para leer una columna con nombre en cualquier modo. Una consulta que no coincide con nada
  hace fallar la comprobación ("no value").
- **Los operadores** se comportan exactamente como los de la comprobación `sql` (`>` `>=` `<` `<=` numérico;
  `==`/`!=` numérico-o-cadena; `contains` subcadena; `=~` regexp RE2).
- **Auth.** *InfluxQL:* un `user`/`password` se envía como auth HTTP Basic; un
  `token` opcional (compatibilidad 1.8+/2.x) se envía como `Authorization: Token …`
  y tiene precedencia. *Flux:* el `token` es requerido. La comprobación solo lee —
  apúntala a un usuario/token de solo lectura.
- Los datos del resultado llevan `language`, `query`, `op`, `threshold`, la `database`/`org`
  en uso, el `result` crudo y, cuando es numérico, un `value` para hooks/reglas. Un error de
  consulta (p. ej. base de datos desconocida, token incorrecto) hace fallar la comprobación.

### Crecimiento de tamaño (`size`)

Una comprobación `size` observa un archivo o directorio y **alerta cuando crece** al
menos `grow_by` dentro de la ventana `within` — útil para detectar un log descontrolado, un
spool que llena el disco o una caché con fugas. Solo los **aumentos** la disparan: una ruta estable o
que encoge pasa. Es de **estilo condición** (`OK == true` significa "creció demasiado
rápido", así que `active: {check: …}` se dispara) y **stateful**. El historial de crecimiento persiste
mientras el worker del servicio o el watch está vivo y se reinicia cuando ese worker/watch se
reconstruye, por ejemplo tras una recarga de configuración.

```yaml
watches:
  log-runaway:
    check:
      type: size
      path: /var/log/app.log   # a file, or a directory (recursive sum of file sizes)
      grow_by: 1GB             # alert if it grows at least this much…
      within: 1h               # …within this sliding window
    then:
      notify: [ops-email]      # and/or a hook
```

(Los ejemplos de este archivo usan mapas globales `watches:` compactos. En un archivo
bajo `paths.watches`, escribe el mismo watch como documento `name: log-runaway` y deja
los campos internos en el nivel superior.)

(Nota: `within` aquí es el **campo propio** de la comprobación de tamaño — la duración de su
ventana de crecimiento — no la ventana de disparo `within: {cycles|duration, min_matches}` a
nivel de watch, que un watch de tamaño normalmente no necesita.)

Cada ciclo muestrea el tamaño de la ruta (los bytes de un archivo, o la suma recursiva de
tamaños de archivos regulares bajo un directorio), guarda las muestras vistas en el último
`within`, y compara el tamaño actual contra el más antiguo que todavía está en la
ventana. Falla cuando `current − baseline ≥ grow_by`. El primer ciclo solo
establece la línea base (sin alerta). `grow_by` usa la misma gramática de tamaño que cualquier otro campo de
tamaño (`free_bytes`, `expand.by`): un sufijo explícito `K`/`M`/`G`/`T` (opcional
`B`/`iB`), unidades binarias (`1G` = 2³⁰), con recuentos de bytes simples rechazados. Los datos
del resultado llevan `current_bytes`, `baseline_bytes`,
`growth_bytes`, la `window` y `value` (el crecimiento) para hooks/reglas. Un
recorrido de directorio lee todo el subárbol cada ciclo, así que apúntala a una ruta acotada.

### WebSocket (`websocket`)

Una comprobación `websocket` verifica que un endpoint WebSocket completa el handshake de apertura
de RFC 6455: envía la petición HTTP `Upgrade` y comprueba que el servidor responde
`101 Switching Protocols` con un `Sec-WebSocket-Accept` que coincide con la clave enviada
(así que confirma un servidor WebSocket real, no solo cualquier HTTP 101).

```yaml
checks:
  realtime:
    type: websocket
    url: "wss://example.com/socket"   # ws:// | wss:// | http:// | https://
    # tls: skip-verify                # accept a self-signed cert (wss/https)
    # origin: "https://example.com"   # optional Origin header
    # subprotocol: "chat"             # optional Sec-WebSocket-Protocol
    # headers: { Authorization: "Bearer ${token}" }   # optional extra headers
```

Pasa (estilo salud, `OK == true`) cuando el handshake se completa. `ws`/`http`
conectan en texto plano; `wss`/`https` usan TLS (`tls: skip-verify` acepta un
certificado autofirmado). El puerto por defecto sigue el esquema (80 / 443) salvo que
la URL dé uno. Los datos del resultado llevan el `subprotocol` negociado. Sondeado
de forma nativa (sin biblioteca externa).

```yaml
checks:
  db:
    type: mysql                 # any protocol from the supported-types table above
    # Auth depends on protocol: postgres requires user; mysql/mongodb can probe without it;
    # redis/imap/pop/smtp may be anonymous; fpm/dns/amqp use no auth.
    host: 127.0.0.1             # default 127.0.0.1
    port: 3306                  # default: the protocol's port (mysql 3306, postgres 5432)
    user: monitor               # optional/required by protocol
    password: "${env:DB_PASS}"  # resolved from the environment at load (never store secrets in plaintext)
    database: ""                # optional
    tls: false                  # optional (see per-protocol values above)
    timeout: 5s                 # optional (engine.default_timeout)
```

Pasa (estilo salud, `OK == true`) cuando conecta, autentica como
`user`, y el servidor responde a un ping. Los datos del resultado exponen `protocol`, `host`,
`port` y la `version` del servidor. Un fallo de red/auth hace fallar la comprobación con el
error. Esto está pensado para añadirse a los `checks:` de un servicio de base de datos de modo que un
restart/alerta pueda dispararse cuando deja de aceptar conexiones.

**Comparaciones de respuesta (`expect`).** Cualquier comprobación de protocolo puede afirmar los valores
que su sonda devuelve — la `version` del servidor o cualquier campo que el protocolo ponga en sus
datos de resultado (p. ej. `answers`/`rcode` para `dns`, `stratum`/`offset_seconds` para
`ntp`, `sys_object_id` para `snmp`, `offered_ip`/`lease_seconds` para `dhcp`,
`ipp_version` para `ipp`, …). `expect` es un mapeo de campo → valor (igualdad) o
campo → `{op, value}` usando los operadores compartidos `== != > >= < <=` (numérico, o
cadena para `==`/`!=`), `contains` (subcadena) y `=~` (regex Go/RE2). Todas las
aserciones deben cumplirse, **además** de que la sonda tenga éxito:

```yaml
checks:
  resolver:
    type: dns
    host: 1.1.1.1
    query: example.com
    expect:
      rcode: NOERROR                 # equality (scalar)
      answers: { op: ">", value: 0 } # operator comparison
  clock:
    type: ntp
    host: pool.ntp.org
    expect:
      stratum: { op: "<=", value: 3 }
```

Un campo referenciado que la sonda no devolvió hace fallar la comprobación con un mensaje
claro. Los mismos operadores de comparación funcionan para cada campo de protocolo registrado.

**Latencia de respuesta (`expect_latency`).** Cualquier comprobación de protocolo también acepta
`expect_latency: { op, value }` (milisegundos), como la comprobación `http` — falla
cuando el tiempo de respuesta de la sonda no satisface la comparación. Los datos del resultado
siempre llevan `latency_ms`:

```yaml
checks:
  cache:
    type: redis
    password: "${env:REDIS_PASS}"
    expect_latency: { op: "<", value: 50 }   # alert when Redis answers slowly
```

**Detección de cambio de versión (`on_version_change`).** Establece `on_version_change: true`
en una comprobación de servicio o watch de host para alertar cuando la versión del servidor cambia
entre ciclos — p. ej. tras una actualización de paquete. La identidad rastreada es la
`version` reportada del protocolo — para comprobaciones de protocolo de conexión
como `mysql`, `postgres`, `redis`, `ssh`, `snmp`, `rspamd`, `libvirt` o `syncthing` —
o, para protocolos que solo devuelven un banner de saludo (como `smtp`, `imap`,
`pop`, `ftp`), ese banner. Cualquier protocolo de conexión registrado que reporte
versión o banner participa; estos nombres son ejemplos, no el conjunto completo. El primer ciclo establece la línea base en silencio;
un cambio posterior **hace fallar** la comprobación y los datos del resultado llevan
`version`/`version_old`. La línea base vive en la instancia de comprobación, así que persiste
mientras el worker del servicio o el watch está vivo y se reinicia cuando ese worker/watch se
reconstruye, por ejemplo tras una recarga de configuración. Se compone con `on_change` (la
identidad de fingerprint SSH/SNMP) — ambos pueden habilitarse a la vez.

```yaml
watches:
  mail-version:
    monitor: disabled
    check:
      type: smtp
      host: mail.example.com
      on_version_change: true   # alert when the SMTP banner/version changes
      expect_latency: { op: "<", value: 500 }
    then:
      notify: [ops-email]
```

Se añaden más protocolos de la misma forma — el tipo de comprobación, el despacho y la validación
son agnósticos al protocolo, así que un nuevo protocolo solo se registra a sí mismo.

Cada tipo de arriba es una **comprobación de un solo disparo** (`Check.Run → Result`) y es usable en
**ambos** lugares:

- los `checks:`/`preflight:` de un servicio (y referenciado desde reglas),
- un documento de **watch** de host (o entrada global `watches:`, disparando un hook) — ver [configuración](configuration.es.md#host-watches), y
- el propio bloque `watches:` embebido de un servicio (disparando un hook acotado al servicio, incluidos los tipos `service`/`metric` y el `process_count` acotado por PIDs) — ver [Watches de servicio](configuration.es.md#watches-de-servicio-acotados-a-un-servicio).

Las comprobaciones de recursos del host (`storage`, `load`, `memory`, `pressure`, `fds`, `pids`,
`diskio`, `hdparm`, `sensors`, `smart`, `raid`, `edac`, `conntrack`, `entropy`,
`zombies`, `oom`, entre otras) son de
estilo condición — `OK == true` significa que hay un problema — así que en reglas
`active: {check: x}` se dispara sobre ella, y como watch el hook se dispara sobre ella.
Las comprobaciones de salud (`tcp`, `ports`, `http`, `command`, `service`, `file_exists`,
`file`, `lockfile`, `binary`, `pidfile`, `socket`, `process`, `libraries`, `config`,
`autofs`, `route`, `firewall_rules`, `cert`, `sqlite`/`sqlite3`,
`websocket`, y comprobaciones de protocolo de conexión como `mysql`/`smtp`) son lo
opuesto (`OK == true` es sano), así que como watch disparan el hook sobre
**fallo**.

Los watches multi-métrica (`net`, `icmp`, `swap`) mantienen la forma de su mapa `metrics:`
(un hook por métrica) solo como watch, pero su **forma de métrica única** — un
campo `metric:` explícito que produce un resultado, p. ej. `{type: net, interface: ppp0, metric:
state, expect: up}` o `{type: icmp, host: 1.1.1.1, metric: state, expect: up}` —
funciona en los `checks:` de un servicio como cualquier otra comprobación (usado por el daemon de catálogo
`pppd` para observar su uplink). Los watches multi-objetivo (`file`, `process`, un
evento/hook por ruta cambiada o pid coincidente) se mantienen solo como watch.
Las comprobaciones `service`/`metric`/`process` necesitan contexto por servicio (estado del backend, un
muestreador de métricas, descubrimiento de procesos) y por eso no están disponibles como watches
independientes.

### Ruta por defecto (`route`)

La comprobación `route` verifica que el kernel tiene una **ruta por defecto activa** — leída
de forma nativa desde `/proc/net/route` (IPv4, el valor por defecto) o `/proc/net/ipv6_route`
(`family: ipv6`). Con `interface`, una ruta por defecto debe salir a través de esa
interfaz. Es una comprobación de salud (OK significa que la ruta está ahí); como watch se
dispara cuando la ruta desaparece.

Cierra la brecha de uplink que las capas de enlace y ping dejan: tras una renegociación PPP
fallida la interfaz puede permanecer `up` con la ruta por defecto desaparecida, y un
ping ligado a la interfaz no puede distinguir "sin ruta" de "proveedor caído". El
servicio de catálogo `pppd` superpone las tres (`net` state, `route`, `icmp`).

```yaml
checks:
  route:
    type: route          # IPv4 by default; family: ipv6 for the v6 table
    interface: ppp0      # optional: the default route must leave through ppp0
```

El resultado reporta la interfaz de salida coincidente y el gateway (cuando la ruta
tiene uno — los enlaces punto a punto no tienen) en sus datos, y `value` lleva el
número de rutas por defecto coincidentes.

### Reglas de firewall (`firewall_rules`)

La comprobación `firewall_rules` verifica que las reglas de nftables o iptables están cargadas.
Es de estilo salud: un servicio o watch falla cuando el recuento de reglas está por debajo de
`min_rules` (por defecto `1`). `backend: auto` prueba nftables primero (leído vía
netlink, sin requerir el binario `nft`) y recae a iptables/ip6tables.

```yaml
checks:
  service: { type: service, expect: active }
  firewall:
    type: firewall_rules
    backend: auto        # auto | nftables | iptables
    min_rules: 1
    requires: [service]  # useful for oneshot firewall loaders
```

Como watch, dispara el hook cuando las reglas de firewall desaparecen. Extras del hook:
`SERMO_BACKEND`, `SERMO_RULES`, `SERMO_MIN_RULES`.

### Rendimiento de disco (`hdparm`)

La comprobación `hdparm` cronometra el rendimiento de lectura de un disco y alerta cuando cruza un
umbral — útil para detectar una unidad que se **degrada gradualmente**. Ejecuta `hdparm` en
`device` y expone dos valores en MB/s: **`read`** (lecturas de disco con búfer, `hdparm -t`
— la velocidad real del dispositivo) y **`cached`** (lecturas cacheadas, `hdparm -T` — rendimiento
de memoria/caché). Los predicados son `{op, value}` en **MB/s**; al menos uno de `read`/
`cached` es requerido, y **solo se ejecutan las medidas que un predicado necesita** (una
comprobación solo de `cached` omite el pase lento con búfer).

```yaml
watches:
  disk-speed:
    interval: 24h                       # hdparm -t reads ~3s and adds I/O — run rarely
    check:
      type: hdparm
      device: /dev/sda
      timeout: 30s                      # give the benchmark room
      read:   { op: "<", value: 100 }   # alert when buffered reads drop below 100 MB/s
      cached: { op: "<", value: 3000 }  # optional: cache/memory throughput
    then:
      notify: [ops-email]
```

`hdparm` es de **estilo condición**: un predicado expresa la condición de *alerta*
(p. ej. `read < 100`), así que el hook/notify del watch se dispara cuando se cumple.
**`hdparm` necesita root** (acceso crudo al dispositivo); sin él la
comprobación falla con el error de hdparm. Como `-t` lee del plato durante unos pocos
segundos y añade carga real de I/O, prográmalo en un **`interval` largo** (p. ej. `24h`)
con un `timeout` generoso. Los `read`/`cached` medidos se colocan en los datos del
resultado (y las variables de hook `SERMO_READ`/`SERMO_CACHED`), y se **registran como una
serie temporal y se grafican** en el detalle del servicio (web UI) para que puedas detectar la **degradación
gradual** de una unidad con el tiempo. (Este graficado de métricas con nombre por comprobación es
genérico: cualquier comprobación que publique campos numéricos `Result.Data` puede optar por él.)

### Sensores de hardware

Las comprobaciones de salud física son de **estilo condición**: los predicados son condiciones de
alerta. Los valores numéricos se **registran a lo largo del tiempo y se grafican** en el detalle del
servicio de modo que la degradación gradual es visible.

- **`sensors`** — entradas **hwmon** al estilo lm-sensors (sin herramienta externa; lee
  `/sys/class/hwmon`). Agregados: `temp` (la temperatura coincidente más caliente, °C),
  `fan` (el ventilador coincidente más lento, RPM — detecta un ventilador parado) y `voltage` (el
  rail coincidente más bajo, V). Al menos un predicado es requerido; las subcadenas opcionales `chip` y
  `label` acotan qué entradas cuentan.

  ```yaml
  checks:
    cpu-temp:
      type: sensors
      chip: coretemp                 # optional: only this chip
      temp: { op: ">", value: 85 }   # alert when the hottest core exceeds 85 °C
      fan: { op: "<", value: 400 }   # optional: alert on a stalled fan
  ```

- **`smart`** — la salud **SMART** de una unidad vía `smartctl -j` (necesita smartmontools
  y root). Sin predicado alerta cuando el veredicto SMART general es
  **FAILED**; los predicados añaden `temperature` (°C), `reallocated` (recuento de sectores, un
  signo de fallo de HDD), `wear` (porcentaje usado de SSD/NVMe) y `power_on_hours`.
  Complementa `hdparm` (rendimiento) con predicción de fallos.

  ```yaml
  checks:
    ssd-health:
      type: smart
      device: /dev/nvme0
      interval: 1h
      reallocated: { op: ">", value: 0 }   # any reallocated sector
      wear: { op: ">", value: 90 }         # SSD/NVMe nearly worn out
  ```

- **`raid`** — **RAID por software md** de Linux desde `/proc/mdstat` (nativo). Sin
  predicado alerta cuando cualquier array está **degradado**; los predicados añaden recuentos `degraded`,
  `recovering` y `arrays`. Un host sin arrays md nunca alerta.

  ```yaml
  checks:
    raid: { type: raid }                   # alert if any md array is degraded
  ```

- **`edac`** — **errores de memoria ECC** del subsistema EDAC del kernel (nativo,
  `/sys/devices/system/edac`). `ce` es el recuento acumulado de corregibles y `ue`
  el recuento de no corregibles; sin predicado alerta sobre `ue > 0`. La comprobación falla
  cuando la plataforma no expone controladores EDAC (así te das cuenta de que ECC no se reporta).

  ```yaml
  checks:
    ecc:
      type: edac
      ce: { op: ">", value: 100 }          # also alert on many correctable errors
  ```

### Autofs

La comprobación `autofs` verifica que el **automounter** autofs (`automount`) está activo.
autofs no tiene socket ni puerto — el daemon habla con el kernel sobre un pipe
interno — así que la señal de vivacidad es la **tabla de montaje**: mientras `automount` se ejecuta
mantiene sus raíces de mapa configuradas como puntos de montaje de tipo `autofs` en
`/proc/mounts` (desaparecen cuando el daemon se detiene). A diferencia de `storage`/`count`,
esta es una comprobación de **salud**: pasa (OK) cuando el automounter está activo como se
configuró, y falla cuando no lo está.

```yaml
checks:
  automounter:
    type: autofs                  # with no path/count: require >= 1 autofs mountpoint
  home-automount:
    type: autofs
    path: /home                   # require this exact autofs mountpoint to be active
  all-maps:
    type: autofs
    count: { op: ">=", value: 3 } # require at least 3 autofs mountpoints
```

- Con **ningún `path` y ningún `count`**, la comprobación pasa cuando al menos un punto de montaje
  autofs está presente (el automounter está ejecutándose con mapas).
- **`path`** requiere que ese punto de montaje exacto sea un montaje autofs activo.
- **`count` `{op, value}`** compara el número de puntos de montaje autofs (`op` es uno
  de `>=, >, <=, <, ==, !=`). `path` y `count` son mutuamente exclusivos.
- Los datos del resultado llevan el `count` de puntos de montaje autofs y los `mountpoints`
  unidos por comas. Los montajes bajo demanda disparados por acceso aparecen bajo estas raíces como
  su sistema de archivos real (p. ej. `nfs`), no como `autofs`, así que no se cuentan —
  la comprobación rastrea las raíces de mapa, es decir, que el propio automounter está activo.

### Count

Una comprobación `count` cuenta las entradas en un directorio y compara el total con un
umbral. Es de **estilo condición** (`OK == true` significa que la comparación se cumple),
así que en reglas `active: {check: …}` se dispara cuando la comparación se cumple y
`failed: {check: …}` se dispara cuando no.

```yaml
checks:
  spool-backlog:
    type: count
    path: /var/spool/myapp        # required: directory to scan
    of: file                      # any (default) | file | dir | symlink
    recursive: false              # optional, default false
    op: ">"                       # >=, >, <=, <, ==, !=
    value: 1000                   # numeric threshold
```

- **`of`** selecciona qué entradas se cuentan. Las entradas se clasifican por su propio
  tipo sin seguir symlinks, así que un symlink cuenta como `symlink` (nunca como el
  archivo o directorio al que apunta); `any` cuenta cada entrada.
- **`recursive: true`** desciende por todo el subárbol (el directorio en sí nunca se
  cuenta); los subdirectorios ilegibles se omiten. Por defecto cuenta solo las
  entradas inmediatas.
- Una `path` ausente o ilegible hace fallar la comprobación. El total observado se
  expone en los datos del resultado de la comprobación como `count`.
- El umbral también puede escribirse como un predicado anidado —
  `count: { op: ">", value: 1000 }` — coincidiendo con la forma `{op, value}` que las otras
  comprobaciones usan. Usa una forma u otra, no ambas.

## Métricas

Las métricas de servicio miden el conjunto de procesos descubierto; las métricas del sistema miden la
máquina. `value` es un número con un `%` final opcional.

```
scope: service   memory, swap, cpu, cpu_thread, process_count, io, io_read, io_write, fds, threads
scope: system    total_memory, total_swap, total_cpu, load1, load5, load15
```

**Una métrica `scope: system` solo puede impulsar reglas `alert`, nunca remediación.** Ella
describe la máquina entera, no un servicio, así que una regla `restart`/`start`/`stop` que
lee una métrica del sistema — directamente, o a través de una referencia `failed`/`active`
a una comprobación `type: metric, scope: system` — se descarta en la carga de configuración con una
advertencia. Esto es un invariante de seguridad: una señal de toda la máquina nunca debe actuar sobre un
servicio individual (ver [docs/safety.md](safety.es.md)).

**Las métricas de servicio suman a lo largo de todo el árbol de procesos descubierto** — los procesos
coincidentes *y* sus procesos hijos/descendientes — así que el `cpu`, `memory`, `io`, `fds`, etc. de un
servicio contabilizan sus workers y ayudantes, no solo el proceso principal. `io`/`io_read`/`io_write`
son tasas de bytes/segundo sobre la I/O real de la capa de bloque (`io` es lectura+escritura); `fds` es el
recuento de descriptores de archivo abiertos y `threads` el recuento de hilos.

`memory` es la **RSS** sumada (memoria residente) del árbol de procesos, como bytes
y como porcentaje del total de RAM. `swap` es la memoria **swapeada** sumada
(`VmSwap`) del árbol, como bytes y — cuando existe un dispositivo de swap — como
porcentaje del total de swap; se reporta solo en hosts donde la contabilidad de swap es
legible.

El porcentaje `cpu` es el tiempo de CPU sumado del servicio (padre + hijos) sobre
el tiempo de pared transcurrido, **normalizado por el total de CPUs lógicas del servidor** (los
hilos de hardware, contados desde `/proc/stat` de modo que la cifra refleja toda la
máquina aunque Sermo esté fijado a un subconjunto de CPU). Así `100%` significa que los procesos del
servicio están saturando cada hilo de CPU del servidor, y un único núcleo completamente ocupado en un
host de 8 hilos se lee como `~12.5%`. `total_cpu` usa la misma base de toda la máquina.

`cpu_thread` complementa `cpu` para el caso de **un solo hilo**: es el **proceso individual más
ocupado** del árbol (padre o cualquier hijo) medido contra **un** hilo de CPU,
así que `100%` significa que un proceso está saturando un núcleo completo. Como el
`cpu` de toda la máquina diluye un único proceso caliente entre todos los núcleos (un proceso ligado a un
núcleo en un host de 8 hilos muestra solo `~12.5%` ahí), `cpu_thread` es sobre lo que
alertas para detectar un proceso — especialmente uno de un solo hilo — clavando su
hilo: `metric` `scope: service`, `metric: cpu_thread`, `op: ">"`, `value:
"90%"`. Un proceso multi-hilo que abarca varios núcleos puede leer por encima de `100%`.
`cpu_thread` es una tasa, así que no está lista en el primer ciclo.

`cpu`/`cpu_thread`/`total_cpu` y las métricas `io*` son tasas: **no están
listas** en el primer ciclo y una condición sobre un valor no-listo es falsa. Un umbral `%`
necesita una métrica con una forma de porcentaje (`memory`, `swap`, `cpu`,
`cpu_thread`, `total_memory`, `total_swap`, `total_cpu`;
`swap`/`memory`/`total_memory`/`total_swap` también tienen una forma de bytes absoluta); un número desnudo necesita una forma absoluta (todo lo demás, incluyendo
`io*`/`fds`/`threads`, que son solo absolutos). Leer la I/O o el recuento de fd de otro proceso
requiere privilegio, así que esos suman solo los procesos que el daemon puede leer.

## Reglas

La evaluación de reglas es determinista e independiente del orden: los guards siempre se ejecutan
antes que la remediación, como máximo **una acción de remediación se ejecuta por servicio por
ciclo**, y cuando varias reglas de remediación se disparan a la vez se consideran
en orden de nombre — la primera acción no bloqueada gana. Cada comprobación declarada
y sonda de condición inline se ejecuta **como máximo una vez por ciclo**; las reglas leen los
resultados cacheados.

```yaml
rules:
  RULE_NAME:
    type: remediation | guard | alert
    if: { ... }       # condition tree
    for: { cycles: 3 }            # consecutive cycles (optional)
    # for: { duration: 6m }        # or consecutive wall-clock time
    within: { cycles: 15, min_matches: 5 }  # sliding window (optional)
    # within: { duration: 30m, min_matches: 3 } # or a time window
    notify: [ops-email]           # who gets this rule's alert messages (optional)
    then: { action: alert, message: "http is down" }
```

El **`notify`** de una regla selecciona qué notificadores reciben sus mensajes `alert`,
sobreescribiendo el default global ([Notificaciones](configuration.es.md#default-selection-and-precedence)):
una lista explícita gana, `notify: none` suprime, y omitirlo hereda el
default global `notify`. Aplica a los mensajes de alerta de la regla; las operaciones de
remediación se reportan como eventos, no como notificaciones.

Las acciones y los tipos están acoplados: las acciones de operación (`restart`, `start`,
`stop`, `reload`, `resume`) pertenecen a reglas `type: remediation` — requeridas ahí (una
regla solo de notificación es `type: alert`) y rechazadas en otros lugares. `alert` (con un
`message`) puede acompañar las acciones de cualquier regla; `block` es solo de guard. Un `then`
puede llevar un `action` o una lista `actions` (p. ej. alert + restart juntos).

Las condiciones forman un árbol lógico con `and`/`or`/`not` y hojas:

```yaml
if:
  or:
    - failed: { check: http }      # a named check failed
    - active: { check: backup-flag } # a named check passed
    - file: { path: /run/x, exists: true }
    - command: { user: postgres, command: ["/usr/local/bin/can-restart", "${service}"], timeout: 5s, expect_exit: 0 }
    - service: { state: active }
    - process: { exe: /usr/bin/mysqld, user: mysql, state: running }
    - metric: { scope: service, name: cpu, op: ">", value: 30% }
    - changed: { path: /lib64/libc.so.6 }  # the file changed since the last cycle
    - changed: { app: containerd, level: patch }  # app version changed
```

`command` es una hoja de condición directa cuya verdad es la misma que una comprobación de comando:
las expectativas de estado de salida/salida pasan. Debe usar la forma de array argv y declarar un
`timeout`; `user` está disponible con el mismo significado que en una comprobación de comando. Se
ejecuta sin un shell, se cachea para el ciclo como otras sondas inline, y debe ser
libre de efectos secundarios. `failed`/`active` también pueden tomar una sonda inline
(`tcp`, `command`, ...) en lugar de una referencia `check:` cuando necesitas la
polaridad de éxito/fallo con nombre.

`changed` es verdadero cuando el archivo en `path` difiere (tamaño/mtime) de la línea base
rastreada entre ciclos, o cuando `app` nombra una app enlazada cuyo comando de versión
cambió en el `level` seleccionado (`major`, `minor` o `patch`; por defecto `patch`).
El primer ciclo adopta el valor actual (un arranque del daemon nunca dispara), y un
`restart`/`start` exitoso vuelve a establecer su línea base. La forma `path` es la primitiva
detrás de `restart_on_change` (ver Daemons → Daemons de biblioteca); la forma `app` es para
binarios propiedad del servicio como `containerd`.

### Ventanas

Sin `for`/`within`, una regla se dispara el ciclo en que su condición es verdadera.
`for` es consecutivo: `for: {cycles: N}` requiere N ciclos verdaderos consecutivos,
mientras que `for: {duration: 6m}` requiere que la condición permanezca verdadera durante al menos
esa duración de tiempo de pared. `within` es una ventana deslizante:
`within: {cycles: N, min_matches: M}` requiere M ciclos verdaderos de los últimos N,
mientras que `within: {duration: 30m, min_matches: M}` requiere M ciclos verdaderos observados
dentro de los últimos 30 minutos. `min_matches` es opcional y por defecto es `1`
(verdadero al menos una vez dentro de la ventana). Una regla no puede usar a la vez `for` y
`within`; una única ventana debe elegir o bien `cycles` o `duration`, no ambos.

El progreso de la ventana de reglas de servicio se persiste en `paths.state`. Si `sermod`
reinicia mientras una ventana `for` está en 2/3 coincidencias consecutivas, el siguiente ciclo
coincidente observado continúa desde 2/3 en lugar de empezar desde cero. Las ventanas basadas en
duración persisten sus timestamps también, así que un reinicio no reinicia una ventana
`for: {duration: ...}` pendiente.

### Guards

Las reglas guard bloquean acciones inseguras y usan `action: block` con un `message`:

```yaml
block-during-backup:
  type: guard
  blocks: [restart, stop]
  if: { file: { path: /run/backup/in-progress, exists: true } }
  then: { action: block, message: "Backup is running" }
```

Los servicios de catálogo MySQL, MariaDB y PostgreSQL que se envían incluyen una comprobación de proceso
`backup` opcional por defecto y un
guard `block-restart-during-backup`. La comprobación coincide con herramientas comunes de backup local
por ruta de ejecutable resuelta exacta (`exe_any`) y usuario de backup de base de datos
(`backup_user`, por defecto `mysql` o `postgres`). Sobreescribe esa comprobación localmente
cuando tu backup se ejecuta bajo otro usuario o desde rutas no estándar. Si un
usuario de terminal conectado ejecuta `sermoctl restart` mientras este guard de backup bloquea
la acción, Sermo también envía a ese usuario un aviso TTY nativo de mejor esfuerzo; cron y
otras ejecuciones no interactivas no son notificadas.

Los ejemplos
[`examples/services/mariadb-backup-guard.yml`](../examples/services/mariadb-backup-guard.yml)
y
[`examples/services/mysql-wal-g-backup-guard.yml`](../examples/services/mysql-wal-g-backup-guard.yml)
muestran la misma forma para herramientas extra enlazadas a la app o sobreescrituras específicas del sitio. La
lista `apps:` es una sobreescritura, así que un servicio que añade una app de backup debe mantener la
app de base de datos también, por ejemplo `apps: [mysql, wal-g-mysql]` o
`apps: [mariadb, wal-g-mysql]`.

Para rutas WAL-G específicas del sitio de PostgreSQL, usa el daemon de catálogo materializado concreto
y la app para la versión instalada (por ejemplo `postgres-16`) y añade
`wal-g-pg`:

```yaml
name: postgres-main
uses: postgres-16
apps: [postgres-16, wal-g-pg]

checks:
  wal-g-pg:
    type: process
    optional: true
    exe_any: ["${wal_g_pg_binary}", /usr/local/bin/wal-g-pg]
    user: postgres
    state: running

rules:
  block-restart-during-wal-g-pg:
    type: guard
    blocks: [restart, stop]
    if:
      active:
        check: wal-g-pg
    then:
      action: block
      message: "${display_name} WAL-G backup is running"
```

Los guards se evalúan antes que la remediación; una acción de remediación que un guard
bloquea nunca se ejecuta.

Las cadenas `message:` pueden usar los built-ins de runtime `${date}` (RFC3339), `${event}`
(el nombre de la regla que se dispara) y `${action}`, más los resueltos `${service}`/`${host}`
— p. ej. `message: "[${host}] ${service}: ${event} → ${action} at ${date}"`.

## Política de remediación

```yaml
policy:
  cooldown: 5m
  max_actions: 5
  max_actions_window: 1h
  backoff: { initial: 1m, factor: 2, max: 30m }
```

La política controla la remediación *automática* (solo `sermod`, nunca acciones manuales de
`sermoctl`): una acción se suprime dentro de `cooldown` (extendido por `backoff`
tras remediaciones consecutivas) o una vez que se alcanza `max_actions` en la ventana.
`for`/`within` deciden *cuándo* se dispara una regla; la política decide si puede actuar
*ahora*.

`backoff` hace crecer el cooldown efectivo tras cada remediación consecutiva:
`initial` la primera vez, luego multiplicado por `factor` cada vez subsiguiente,
limitado a `max`. `factor` **por defecto es `2`** cuando se omite (o se establece a ≤0).
El estado de remediación automática también se persiste en `paths.state`: `LastActionAt`,
los timestamps de acciones recientes usados por `max_actions`, y el backoff actual sobreviven a un
reinicio de `sermod`, así que reiniciar el daemon no evita el cooldown ni los límites de
tasa.

Usa `dry_run: true` en un service (o en `defaults`) cuando quieras que las reglas
de remediación evalúen ventanas, guards y política sin ejecutar la operación
start/stop/restart/reload/resume resultante. Emite eventos `dry-run` y no avanza
el estado de cooldown de remediación en vivo. Dry-run también suprime las
notificaciones automáticas de reglas salvo `wall`; las acciones manuales del
operador no se ven afectadas. Ver [configuración](configuration.es.md#host-watches)
para ejemplos de watches y defaults globales.
