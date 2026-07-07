# Servicios

Un servicio de catálogo es una definición base reutilizable para una aplicación. Un
servicio configurado `uses` un servicio de catálogo y solo sobrescribe lo que difiere.
Un fichero de servicio vive bajo `paths.services`, que es lo que lo marca como servicio
configurado — no se necesita ningún `kind:` (véase
[configuration](configuration.es.md): el kind de un documento se deriva de su
ubicación).

```yaml
name: apache-main
uses: apache
variables:
  health_path: /health
watches:
  restart-if-http-failed:
    check:
      url: "http://${host}:${port}${health_path}"
```

El catálogo empaquetado cubre familias de servicios comunes como servidores web,
bases de datos, runtimes de contenedores, ayudantes NFS/libvirt y servicios de
hardware/sistema. En el árbol de fuentes es `catalog/`; en builds empaquetados
Sermo lee el directorio de catálogo compilado en el binario. Los perfiles de
catálogo definen variables, preflight, procesos, watches, stop_policy, política
de remediación y reglas, de modo que un servicio configurado normalmente solo
establece unos pocos overrides. Los servicios de catálogo de alto impacto como
bases de datos, cachés y colas pueden llevar ajustes de `policy` locales más
estrictos que los valores por defecto globales, con cooldowns más largos, rate
limits y backoff para evitar bucles de reinicio.

## Categorías

Los documentos de catálogo se agrupan por el subdirectorio en el que viven bajo la
raíz del catálogo empaquetado:

```
catalog/
  services/   # long-running services (apache, nginx, mariadb, ...)
  apps/       # installed tools/runtimes (java, perl, sqlite, go, git, ...)
  libs/       # shared libraries used as restart triggers (glibc, pam)
  patterns/   # output-analysis rule sets referenced by a check's analyze: block
```

El directorio establece la categoría de catálogo (`service` / `app` / `library` /
`patterns`) y, por tanto, el kind del documento (`service` / `app` / `lib` /
`patterns`), de modo que un `kind:` de nivel superior es redundante y se omite; los ficheros colocados
directamente en la raíz del catálogo empaquetado se rechazan. Use un fichero YAML
por documento de catálogo: un servicio, app, lib o pattern en cada fichero.
`sermoctl services`, `sermoctl apps` y `sermoctl libs` listan cada categoría,
mostrando cuáles están instalados, la versión que su comando de versión reporta, y
si resuelven sin error (añada `all` para incluir los no instalados).
Las instancias de servicio configuradas (bajo `paths.services`) se listan
en la web UI y `GET /api/services`, no en `sermoctl services` — véase
[cli.md](cli.es.md#catalog-inventory).
`sermoctl patterns` lista los conjuntos de patterns y sus conteos de reglas (véase el
bloque `analyze:` en [rules.md](rules.es.md)).

Los documentos de catálogo pueden declarar `aliases: [...]` para nombres de distro o de paquete que
los operadores escriben de forma natural. Por ejemplo, el servicio de catálogo canónico
`name: apache` puede llevar aliases como `apache2` y `httpd`, de modo que un servicio
configurado puede escribir `uses: apache2` mientras resuelve al mismo perfil de catálogo. Un
servicio configurado también puede declarar aliases; `sermoctl` normaliza esos aliases al
nombre de servicio configurado canónico antes de los comandos de status, start, stop, restart,
reload, monitor, SLA y de proceso/lock. Los aliases de catálogo también son utilizables
como nombres de servicio solo en el caso conservador de un servicio en que un servicio
configurado tiene el mismo nombre que el servicio de catálogo, como `name: smb`,
`uses: smb`, con el alias de catálogo `samba`.

## Servicios de librería

Un servicio de librería describe una librería compartida para que los servicios configurados puedan reiniciarse
cuando se actualiza. Solo necesita identidad más el fichero a vigilar:

```yaml
name: glibc
display_name: "GNU C Library"
description: "Standard C library (libc)"
variables:
  binary: "/lib64/libc.so.6"        # the file watched for changes (and its version)
preflight:
  file: { type: file, path: "${binary}" }
```

Un servicio configurado (o una definición de servicio de catálogo) se suscribe con `restart_on_change`:

```yaml
restart_on_change:
  libraries: [glibc, pam]
```

En la resolución esto se desazucara en una regla de remediación por librería que reinicia
el servicio cuando el fichero de esa librería cambia:

```yaml
rules:
  restart-on-change-glibc:
    type: remediation
    if: { changed: { library: glibc, path: /lib64/libc.so.6 } }
    then: { action: restart }
```

El reinicio corre a través del motor seguro normal (guards, cooldown, max_actions),
y el cambio se reconoce una vez que el reinicio tiene éxito, así que se dispara una vez por
actualización en lugar de cada ciclo. Los nombres referenciados deben ser servicios `library`.

## Reload al cambiar la configuración (`reload_on_change`)

Muchos servicios releen su configuración **sin un reinicio** — systemd
(`systemctl daemon-reload`), nginx (`nginx -s reload`), named (`rndc reload`),
rsyslog, … `reload_on_change` vigila ficheros/directorios de configuración y, cuando uno
cambia, ejecuta la acción **reload** en lugar de un reinicio disruptivo:

```yaml
# catalog/services/systemd.yml
reload:
  command: ["systemctl", "daemon-reload"]
  when: always
reload_on_change:
  paths: [/etc/systemd/system, /lib/systemd/system]
```

En la resolución esto se desazucara en una regla de remediación por path:

```yaml
rules:
  reload-on-change-1:
    type: remediation
    if: { changed: { path: /etc/systemd/system } }
    then: { action: reload }
```

La acción **`reload`** corre a través del mismo motor seguro que restart pero en
sitio: ejecuta **preflight primero** (de modo que una configuración inválida — detectada por el
check `config` del servicio — bloquea el reload), recarga y luego verifica la salud.
`reload` también es una acción de regla válida por sí sola (`then: { action: reload }`) y
está bloqueada por guards que listan `reload`, como cualquier otra acción de servicio.

**Qué ejecuta "reload".** Por defecto es el reload por unidad del backend —
`systemctl reload <unit>` (que ejecuta el `ExecReload` de la unidad, p. ej. `nginx -s
reload`) o el `reload` del init-script de OpenRC. Un servicio de catálogo puede sobrescribir esto con
**`reload.command`** cuando el reload no es una operación por unidad — el propio systemd
recarga con `systemctl daemon-reload`, no `systemctl reload systemd`:

```yaml
reload:
  command: ["systemctl", "daemon-reload"]
  when: always
```

Si el backend de init informa que no soporta reload y el servicio no tiene un
fallback válido con `reload.command` o `reload.signal`, Sermo rechaza la acción
`reload` antes de ejecutarla. La CLI avisa de que el reload no está soportado y
la web UI desactiva el botón mediante `can_reload=false`.

### Reload nativo (`reload:`) — cuando el init no puede, Sermo sí

Algunos servicios recargan en sitio (p. ej. `sshd`, `snmpd`, `proftpd`, `prometheus`,
`loki` releen su configuración al recibir **`SIGHUP`**) pero su unidad **systemd** no define
**ningún `ExecReload`**, así que `systemctl reload <unit>` falla — aunque el propio servicio
lo soporte (el mismo servicio bajo OpenRC normalmente sí recarga, vía un
`reload()` de init-script que envía la señal). El bloque `reload:` cierra esa
brecha: declara un **reload nativo** que Sermo realiza por sí mismo, señalizando el
proceso principal del servicio o ejecutando un comando.

```yaml
reload:
  signal: HUP        # send this signal to the main process (HUP, USR1, USR2, …)
  when: auto         # auto (default): use the init's reload if the unit/script
                     #   has one, otherwise do this; always: never use the init,
                     #   always do this
# or, instead of a signal, a command:
reload:
  command: ["nginx", "-s", "reload"]
  when: auto
```

- **`when: auto`** (por defecto) pregunta al backend si puede recargar — el
  `CanReload` de systemd (la unidad tiene un `ExecReload`), o un init-script de OpenRC que
  define `reload`. Si puede, corre el reload del init; si no puede, Sermo ejecuta el
  reload nativo. Así, la *misma* definición de servicio de catálogo recarga correctamente en un host
  cuya unidad expone reload **y** en uno cuya unidad no lo hace.
- **`when: always`** siempre ejecuta el reload nativo y nunca el del init — la
  elección correcta para reloads que no son operaciones por unidad. Un
  `reload: { command: [...] }` simple por defecto es `when: auto`, así que ponga `when: always`
  cuando el comando deba ejecutarse siempre.
- **Destino de la señal.** La señal va al `MainPID` de systemd, o — en OpenRC, o
  cualquier unidad sin MainPID — al PID en el `pidfile:` del servicio. El fallback al pidfile
  solo se usa cuando ese PID también coincide con un selector `processes:` con
  `exe` y `user` exactos; un pidfile obsoleto no debe señalizar un proceso no relacionado.
  Un reload por señal sin ninguno de los destinos disponibles falla. Los servicios sin metadatos de pidfile
  recargan por señal solo en systemd; en OpenRC dependen del init
  script propio `reload` (`when: auto`).

#### Checklist para autores de catálogo: init scripts y fallbacks

Antes de publicar o cambiar un servicio de catálogo con `reload.signal`, verifique cada
backend de init listado en `service:` y cada fallback que Sermo pueda usar. No verifique
solo la plataforma donde el perfil se escribió por primera vez.

1. Inspeccione las definiciones de init empaquetadas reales. Para OpenRC, lea
   `/etc/init.d/<unit>` y el `/etc/conf.d/<unit>` correspondiente; para systemd, lea
   la unidad y sus metadatos de reload/PID reportados.
2. Registre si el backend de init puede recargar por sí mismo. Con `when: auto`, Sermo
   prefiere el reload del backend cuando systemd reporta `CanReload=yes` o el script
   de OpenRC define `reload()`. Si un host carece de esa vía, el fallback nativo de Sermo
   debe seguir siendo seguro.
3. Para cualquier `reload.signal` capaz en OpenRC, declare un candidato `/run/...`
   canónico en `pidfile:` y un selector `processes:` con `exe` y `user` exactos.
   El ejecutable debe ser el path `/proc/<pid>/exe` resuelto (normalmente a través
   de la variable binary de la app enlazada), y el user debería ser una variable de servicio
   para que las diferencias de empaquetado locales puedan sobrescribirlo.
4. Si los scripts de OpenRC difieren por distribución, codifique los candidatos de pidfile reales
   como una lista o una rama `os:`. No publique un único path que se verificó en
   solo una distro.
5. Si un backend no tiene pidfile o no tiene una identidad fiable de `exe` más `user`, no
   confíe en `reload.signal` para ese backend. Use un `reload.command` con argv, o
   confíe solo en el reload del backend de init cuando cada backend configurado valide.
6. Ejecute los tests de validación del catálogo para ambos backends de init antes del release.

Checks de host útiles:

```bash
sermoctl backend
systemctl cat <unit>
systemctl show -p CanReload -p MainPID -p PIDFile -p User <unit>
sed -n '/^reload()/,/^}/p' /etc/init.d/<unit>
grep -E '^(command|command_user|pidfile|.*PIDFILE)=' /etc/init.d/<unit> /etc/conf.d/<unit>
readlink -f /usr/sbin/<service>
namei -l /run/<service>.pid
```

Auditoría de catálogo útil mientras se desarrolla:

```bash
go test ./internal/config -run 'TestRealCatalog(AllServicesValidate|ReloadServicesResolve)$' -count=1
```

El reload elegido por el backend o por `reload:` es lo que la **acción
`reload`**, `reload_on_change`, el comando `sermoctl reload <svc>` y el botón de
reload de la web UI ejecutan todos. Es un concepto de control de servicios: se
aplica a servicios, no a los watches de host, que observan métricas de host y
disparan hooks en lugar de recargar una unidad.

## Dependencias de app (`apps`)

Un servicio puede enlazar una o más **apps** de `catalog/apps` (java, openssl,
perl, …). Una app posee los checks de **binary**, **health** y **version** de la herramienta.
Enlácelas con `apps:`:

```yaml
# catalog/services/tomcat.yml — Tomcat runs on the JVM
apps: [java, "tomcat-${version}"]
```

En la resolución, los checks de preflight de cada app enlazada se inyectan en el
preflight del servicio bajo claves con namespace por el nombre de la app (`<app>-<check>`), llevando
el path `variables.binary` propio de la app, la sonda de health y el comando de versión. Cuando un
servicio enlaza
varias apps, los checks de cada una se mantienen distintos — p. ej. `apps: [backrest, restic]`
de `backrest`
produce `backrest-binary`, `backrest-health`, `backrest-version`,
`restic-binary`, `restic-health`, `restic-version`, de modo que un `restic` ausente o no sano
levanta su propia alerta separada de `backrest`:

```yaml
preflight:
  java-binary:  { type: binary, path: /usr/bin/java }
  java-health:  { type: command, command: ["/usr/bin/java", "-help"] }
  java-version: { type: command, command: ["/usr/bin/java", "-version"] }
```

Las variables de app también están disponibles para el servicio. Siempre se exponen con un
prefijo normalizado del nombre de la app (`${java_binary}`, `${php_fpm_binary}`, ...). Si el
servicio enlaza exactamente una app, esas variables están adicionalmente disponibles sin
el prefijo como valores por defecto, de modo que los checks específicos del servicio pueden usar `${binary}` mientras la
app conserva la propiedad del path real. Las entradas `variables:` locales en el servicio de catálogo
o en el servicio configurado sobrescriben cualquiera de las formas; cuando se enlazan varias apps,
use los nombres con prefijo.

Como corren en **preflight**, un runtime ausente o de versión incorrecta hace fallar el
preflight del servicio, que **bloquea start/restart/reload/resume** (una operación con preflight fallido
nunca ejecuta la acción) — no arranca, reinicia, recarga ni
resume un servicio cuyo runtime está ausente.
El enlace es muchos-a-muchos: un servicio lista varias apps, y una app es compartida por
cada servicio que la lista. La validación reporta una entrada `apps:` que no
resuelve a una app de catálogo, de modo que los enlaces de runtime colgantes se detectan antes del despliegue.
El servicio conserva sus propios checks `variables.binary`,
`version` y `config` (el test **config** es siempre específico del servicio,
nunca movido a una app). Los nombres referenciados deben ser servicios `app`.

## Campos de metadatos

Un servicio de catálogo o un servicio configurado puede llevar metadatos opcionales orientados a humanos:

```yaml
name: mariadb
display_name: "MariaDB"      # pretty label; falls back to name when absent
description: "..."           # free-text note; shown verbatim, nothing when absent
category: "database"         # optional WebUI grouping/filter label
type: "database"             # optional free-form classification; recorded, not acted on
```

Estos campos son opcionales y se comportan de forma diferente cuando faltan:

- **`display_name`** es la etiqueta usada dondequiera que Sermo muestre la entrada de catálogo a
  un humano (p. ej. `sermoctl services`, `sermoctl apps` y la Web UI). Cuando está
  ausente o en blanco, Sermo recurre a `name`. Establézcalo solo cuando añada algo
  sobre `name` — una marca propia (`MariaDB`, `PostgreSQL`, `OpenSSH`) o una versión
  (`PHP-FPM 8.3`). Si el display name solo repetiría `name`, omítalo y
  deje que aplique el fallback.
- **`description`** es una nota de texto libre opcional. **No tiene fallback**: cuando está
  ausente, no se muestra nada para ella — Sermo nunca sustituye `name`. Úsela para
  una frase real, no una repetición del nombre.
- **`category`** agrupa y filtra Services y aplicaciones instaladas en la
  WebUI. Cuando está ausente o en blanco, los servicios usan `service` y las apps usan `app`.
- **`type`** es una etiqueta de clasificación de texto libre opcional (p. ej. `database`,
  `cache`, `queue`, `webserver`, `appserver`, `tunnel`) usada en el catálogo para
  organizar entradas. Se registra pero **no se consume actualmente** por el motor
  y no tiene efecto en monitorización, agrupación o remediación.

`display_name`, `description` y `category` deben ser strings si están presentes;
la validación rechaza valores que no sean string.

### Variables integradas

Las variables de la tabla de abajo siempre están disponibles durante la resolución
**sin ser declaradas** bajo `variables` — de modo que un servicio de catálogo puede parametrizar
strings orientados a humanos (y paths) en lugar de hardcodearlos:

```yaml
rules:
  block-restart-during-maintenance:
    type: guard
    blocks: [restart, stop]
    then:
      action: block
      message: "${display_name} maintenance is active" # → "MariaDB maintenance is active"
variables:
  binary: "/usr/bin/qemu-system-${arch}"             # → /usr/bin/qemu-system-x86_64
preflight:
  binary: { type: binary, path: "${binary}" }
```

Una entrada `variables` explícita del mismo nombre siempre tiene precedencia sobre una
integrada. `${arch}`/`${os}` se cuecen **al cargar** (en todas partes — valores de variables
y paths de descubrimiento de apps incluidos); el resto resuelve por servicio, y
las de runtime (`${date}`/`${event}`/`${action}`) solo en strings `message:` de regla. Las variables de entorno
`SERMO_ARCH` / `SERMO_OS` / `SERMO_HOST` / `SERMO_HOSTNAME` /
`SERMO_INIT` / `SERMO_USER` sobrescriben la integrada correspondiente
(útil para testear o construir config fuera del host).

`${user}` es una integrada de carga de config. Usa `SERMO_USER` cuando está establecida, de lo contrario
el usuario que ejecuta Sermo. Está intencionalmente separada del resolver de runtime
`engine.user_lookup` usado para selectores de proceso y `kill_only_if`; establezca
`SERMO_USER` cuando necesite que `${user}` sea determinista mientras genera o
valida config fuera del host.

| Variable          | Valor                                          | Resuelta        |
|-------------------|------------------------------------------------|-----------------|
| `${name}`         | el nombre de servicio resuelto                 | resolución      |
| `${display_name}` | el display name (recurre a name)               | resolución      |
| `${service}`      | el nombre de unidad principal del servicio     | resolución      |
| `${host}`         | hostname (override `SERMO_HOST`)               | resolución¹     |
| `${hostname}`     | hostname corto (`SERMO_HOSTNAME`)              | resolución⁵     |
| `${init}`         | sistema init detectado (`SERMO_INIT`)          | resolución      |
| `${user}`         | el usuario de Sermo (override `SERMO_USER`)    | resolución⁴     |
| `${pidfile}`      | `/run/<unit>.pid` convencional                 | resolución⁴     |
| `${port}`         | el campo `port:` de nivel superior (si está)   | resolución³     |
| `${arch}`         | arquitectura de la máquina (`SERMO_ARCH`)      | carga (cocida)  |
| `${os}`           | id de os-release (`SERMO_OS`)                  | carga (cocida)  |
| `${date}`         | timestamp del evento (RFC3339)                 | runtime²        |
| `${event}`        | el nombre de la regla que dispara              | runtime²        |
| `${action}`       | la acción tomada (restart/start/stop/reload/resume) | runtime²        |

¹ `${host}` solo aplica cuando el servicio no define una variable `host` (una
dirección de bind como `127.0.0.1`); un `host` explícito siempre gana.

⁵ `${hostname}` es el hostname **corto** — la primera etiqueta antes del primer punto
(`node1` en `node1.example.com`) — distinta de `${host}` (que conserva el hostname
completo detectado / fallback a dirección de bind). Úsela para unidades de instancia de systemd
indexadas por identidad de host, p. ej. `service: "ceph-mon@${hostname}"` → `ceph-mon@node1`.
Para servicios multi-instancia numéricos (p. ej. un OSD por dispositivo) use una plantilla de servicio
`%n` cuyo `service:` lleve `${n}`. Sermo materializa `ceph-osd0…N` a partir de
unidades activas como `ceph-osd@0.service`, luego enlaza la app genérica `ceph-osd`
para validación de binary. Una variable `hostname` explícita (o `SERMO_HOSTNAME`)
gana.

⁴ `${user}` y `${pidfile}` son fallbacks: el `user` propio de un servicio (una cuenta
de servicio como `www-data`) o la variable `pidfile` siempre ganan. Ponga la variable de pidfile
en el `pidfile: "${pidfile}"` de nivel de servicio, y use `user: "${user}"`
dentro de cualquier selector `processes:` que deba estar atado a la cuenta de servicio.

Los paths de runtime en la config de Sermo usan la grafía canónica `/run`. No escriba
nuevos pidfiles, sockets o lockfiles en `/var/run` en servicios de catálogo, servicios
generados o ejemplos. Linux mantiene `/var/run` como compatibilidad para `/run`, y
los init scripts más antiguos, gestores de servicios o configs empaquetadas pueden seguir reportando esa
grafía; los paths detectados deberían normalizarse a `/run/...` antes de
ser confirmados en config.
Antes de añadir un nuevo path de runtime, compruebe si él o un directorio padre es un
symlink (`readlink -f <path>` o `namei -l <path>`), luego registre el path target canónico
en lugar del alias.

² `${date}`/`${event}`/`${action}` se sustituyen cuando el worker emite un mensaje
de regla, así que pertenecen a strings `message:` — p. ej.
`message: "[${host}] ${service}: ${event} → ${action} at ${date}"`. En otros sitios se
mantienen literales.

³ `${port}` refleja un campo `port:` de nivel superior en el servicio configurado (o servicio de
catálogo), de modo que una instancia puede establecer su puerto de escucha una vez y hacer que cada
referencia `${port}` resuelva a él:

```yaml
name: db-inst2
uses: dbserver
port: 3307          # → ${port} everywhere in the catalog service
```

A diferencia de las otras integradas, **no tiene fallback**: declare `port:` (o una
`variables.port`, que gana) dondequiera que se use `${port}`, o la resolución reporta
`${port}` como indefinida. Este es el equivalente de primera clase a poner `port`
bajo `variables:` (como sigue mostrando el ejemplo multi-instancia de abajo).

### Bloques específicos de OS (os:)

Más allá del string `${os}`, una clave `os:` en cualquier lugar de un documento selecciona un
sub-bloque entero por OS. El bloque para el OS detectado (o un bloque `default`) se fusiona
en su padre y el resto se descarta — al cargar, antes de la resolución. No está
limitado al bloque service; úselo en checks, processes, policy, variables, en cualquier sitio:

```yaml
service:
  os:
    gentoo: { systemd: [apache],  openrc: [apache]  }
    debian: { systemd: [apache2], openrc: [apache2] }

watches:
  http:
    check:
      type: http
      timeout: 5s          # kept for every OS
      os:
        gentoo: { url: "http://localhost/gentoo-health" }
        debian: { url: "http://localhost/debian-health" }

policy:
  os:
    debian:  { cooldown: 1m }
    default: { cooldown: 9m }   # used when the OS has no branch
```

Los hermanos de `os:` se preservan y la rama seleccionada se fusiona sobre ellos. `os` está
reservado como clave de selector dondequiera que su valor sea un map.

Una rama también puede ser una **lista o escalar** en lugar de un map. Cuando `os:` es la única
clave en su padre, la rama seleccionada *reemplaza* el valor (en lugar de fusionarse),
lo cual es útil para listas de candidatos específicas de OS como los paths de pidfile:

```yaml
pidfile:                        # the resolved value becomes the OS's list
  os:
    fedora: [/run/postgres.pid]
    gentoo: [/run/postgres${port}.pid, /run/postgres.pid]
    default: [/run/postgres.pid]
```

El `pidfile:` de nivel de servicio acepta un único path o una **lista de candidatos**.
El descubrimiento los prueba en orden y usa el primero que apunta a un proceso
en ejecución, de modo que las ubicaciones de pidfile por OS o versionadas resuelven todas sin
config personal. Use `pidfiles:` en su lugar cuando un servicio intencionalmente posee varios
procesos residentes que cada uno tiene su propio pidfile.

Para cargadores oneshot que no mantienen un proceso residente (por ejemplo cargadores
de firewall), establezca `processes: {}` explícitamente. Eso evita que Sermo derive un
selector de proceso de los metadatos de init y evita que la WebUI muestre totales de
proceso de CPU/memoria para un servicio que no puede tenerlos.

### `control: libvirt` — máquinas virtuales QEMU/libvirt

Un servicio puede ser controlado como una máquina virtual libvirt/QEMU en lugar de una
unidad systemd/OpenRC:

```yaml
name: vm-web01
control:
  type: libvirt
  uri: qemu:///system
  domain: web01
  socket: /run/libvirt/libvirt-sock     # or /run/libvirt/virtqemud-sock on modular libvirt

watches:
  vm:
    check:
      type: libvirt
      socket: /run/libvirt/libvirt-sock
      query: qemu:///system
      params: { domain: web01 }

processes:
  qemu:
    exe: /usr/bin/qemu-system-x86_64
    cmd: "web01|2b3f3d26-bb45-4b25-b65a-1e3ef86fc1a4"
    user: qemu
```

`control.domain` es el dominio libvirt que Sermo opera. `uri` por defecto es
`qemu:///system`; `socket` por defecto es `/run/libvirt/libvirt-sock` salvo que `host`
esté establecido para una conexión TCP libvirt remota. Los despliegues modulares de libvirt a menudo
exponen dominios QEMU a través de `/run/libvirt/virtqemud-sock`; establezca `socket` a ese
path cuando el socket monolítico está ausente. `uuid` es opcional y, cuando está establecido,
Sermo busca el dominio por UUID en lugar de por nombre.

El motor de operación seguro no cambia: locks, guards, preflight, postflight,
timeouts de operación y política de remediación siguen aplicando. Las acciones primitivas son
operaciones libvirt:

- `start` crea/arranca el dominio definido (`DomainCreate`).
- `stop` solicita un apagado grácil del guest (`DomainShutdown`); no
  destruye la VM.
- `restart` sigue siendo el flujo seguro stop+start de Sermo.
- `resume` resume un dominio pausado (`DomainResume`).
- `reload` no está soportado para dominios de VM salvo que se añada un mecanismo
  específico de servicio futuro.

El status de libvirt mapea al status de Sermo así: running/blocked → `active`,
paused/pmsuspended → `paused`, shutoff/shutdown/nostate → `inactive`, crashed →
`failed`. La CLI y la web UI siguen exponiendo `status=paused` del backend; el
estado agregado del service es `failed` mientras la monitorización está activa, o
`stopped` cuando la monitorización de Sermo está pausada.

El descubrimiento de procesos es intencionalmente explícito en esta primera integración de VM. Si
quiere métricas de proceso o reporte de procesos residuales para el proceso QEMU, añada un
selector `processes:` restrictivo como arriba: `exe` y `user` exactos más una
regex `cmd` que estreche el binary QEMU compartido al dominio o UUID pretendido. El
selector de cmdline estrecha el descubrimiento; la señalización residual sigue estando
autorizada solo por `stop_policy.kill_only_if`.

`sermoctl wizard vm` puede generar esta forma de servicio a partir de dominios
detectados a través del socket libvirt local. Sondea tanto
`/run/libvirt/libvirt-sock` como `/run/libvirt/virtqemud-sock` y escribe el
socket que realmente usó en el servicio y el check generados.

### `control: docker` — contenedores Docker

Un servicio puede ser controlado como un contenedor Docker en lugar de una unidad systemd/OpenRC:

```yaml
name: web-container
control:
  type: docker
  container: web
  socket: /run/docker.sock

watches:
  docker:
    check:
      type: docker
      socket: /run/docker.sock
      container: web
      on_change: true
      expect:
        container.status: { op: "==", value: running }
        container.health: { op: "==", value: healthy }
```

`control.container` es el nombre o id del contenedor Docker que Sermo opera. Sin
`socket` ni `host`, el control usa `/run/docker.sock`; establezca `socket` para otro
socket local, o establezca `host` y opcionalmente `port`/`tls` para un endpoint TCP de
la Docker API. `control.interface` no está soportado para control; el egress ligado a
interface sigue disponible en los checks de Docker.

El motor de operación seguro no cambia: locks, guards, preflight, postflight,
timeouts de operación y política de remediación siguen aplicando. Las acciones primitivas son
operaciones de la Docker Engine API:

- `start` llama al endpoint de start del contenedor.
- `stop` llama al endpoint de stop del contenedor sin escalada de kill del lado de Docker;
  el timeout de operación de Sermo es el límite externo, y el manejo de residuales permanece en
  la stop policy de Sermo.
- `restart` sigue siendo el flujo seguro stop+start de Sermo.
- `resume` despausa un contenedor pausado.
- `reload` no está soportado para contenedores Docker salvo que se añada un mecanismo
  específico de servicio futuro.

El status de Docker mapea al status de Sermo así: running -> `active`, paused ->
`paused`, created/exited -> `inactive`, restarting/dead/removing -> `failed`.
La CLI y la web UI siguen exponiendo `status=paused` del backend; el estado
agregado del service es `failed` mientras la monitorización está activa, o
`stopped` cuando la monitorización de Sermo está pausada.

Para métricas de proceso y reporte de procesos residuales, Sermo lee el
`State.Pid` del contenedor de Docker inspect y descubre ese árbol de procesos. Normalmente no
necesita un selector `processes:` para un contenedor controlado. La señalización residual
sigue estando autorizada solo por `stop_policy.kill_only_if`.

`sermoctl wizard docker` puede generar esta forma de servicio a partir de contenedores
detectados a través del socket Docker local.

### `also_service` — unidades init auxiliares

Un servicio puede nombrar **unidades init auxiliares propias** (un `.socket`, `.timer`,
unidad acompañante) que se arrancan/paran/reinician **junto con la principal**,
en la misma operación. Refleja la forma de `service:` (listas por init, resueltas
para el backend activo):

```yaml
service:
  systemd: [docker]
  openrc:  [docker]
also_service:
  systemd: [docker.socket]
```

Estas son unidades init simples conducidas directamente por el gestor de servicios (no servicios
monitorizados separados — eso es `also_apply`). Se actúa sobre ellas en orden de
**wrap / activación por socket**: arrancadas **antes** de la principal (estricto — un fallo
aborta la operación antes de que la principal arranque), y paradas **después** de ella
(best-effort — un fallo de stop se reporta en el mensaje de resultado pero no hace fallar
un stop ya exitoso). `reload` toca solo la principal. Los guards, locks y preflight de la principal
envuelven toda la operación. Listar la unidad principal en
`also_service` se rechaza.

### `also_apply` — cascada a otros servicios

Donde `also_service` actúa sobre *unidades init de este servicio*, `also_apply` actúa sobre
**otros servicios de Sermo**: cuando este servicio se arranca/para/reinicia (por una
regla de remediación o un `sermoctl` manual), la misma acción corre sobre cada servicio
listado a través de **su propia** operación con guards.

```yaml
also_apply: [nginx, varnish]
```

- **Orden consciente de dependencias**: en `start`/`restart` la principal actúa primero, luego
  las adicionales (un dependiente sube después de aquello de lo que depende); en `stop` las
  adicionales actúan primero, luego la principal.
- **Cada target conserva sus propios guards/locks/preflight** (corre su operación
  real). El cooldown de remediación de un target y su estado pausado/`unmonitor`
  *no* se consultan — `also_apply` es una relación explícita.
- **Best-effort y a prueba de bucles**: un target fallido/bloqueado se reporta (un evento
  `cascade`; un target bloqueado se reintenta una vez) pero no hace fallar la principal; los ciclos
  se cortan con un conjunto de visitados.
- Las entradas deben ser servicios configurados y no deben incluir el propio servicio.
- `sermoctl start|stop|restart <svc> --no-cascade` actúa sobre exactamente un servicio.
- `sermoctl reload <svc>` y `sermoctl resume <svc>` actúan solo sobre la principal
  (sin cascada). Use `sermoctl daemon reload` para recargar la configuración del `sermod`
  en ejecución. En la web UI el botón **reload** por servicio se habilita solo
  cuando el servicio está `active` y Sermo informa `can_reload=true` desde el
  backend de init (`ExecReload`/OpenRC `reload`) o desde un fallback `reload:`
  válido; **resume** solo mientras está `paused`.

`also_apply` (otros servicios) y `also_service` (las unidades init de este servicio) son
complementarios; un servicio puede usar ambos.

### `processes:` por ejecutable o cmdline

Un selector `processes:` coincide con un proceso por el **AND** de los campos que establece;
se requiere al menos uno de `exe`/`cmd`. La clave del map es el nombre de rol del selector
en status, métricas y alertas:

```yaml
processes:
  unifi: { cmd: "java .*unifi", user: unifi, group: unifi }
  mongo: { exe: "${mongod_binary}", user: unifi }
```

- `exe` — el `/proc/<pid>/exe` resuelto exacto (fail-safe; nunca cmdline).
- `cmd` — una regex Go RE2 emparejada contra el **cmdline** del proceso (argv unido).
  Úsela para binarios compartidos (`java .*unifi`, `openvpn .*tun1\.conf`) cuando un
  ejecutable sirve a varias instancias. El cmdline es spoofable, así que `cmd` solo
  estrecha el descubrimiento; la señalización residual sigue estando autorizada solo por
  `stop_policy.kill_only_if` (`exe_any` más `users`).
- `user` / `group` — el UID / GID real propietario del proceso.

Estos alimentan la monitorización **y** el reaper residual, de modo que un selector más rico permite a un
stop atrapar y matar más restos (un residual no matable permanece como
`orphan_processes`). El *check* `process` sigue coincidiendo solo por `exe`/`user`.

### Invariantes de estado parado (`stop_policy`)

Tras un stop **limpio**, el motor puede verificar que el servicio no dejó nada detrás:

```yaml
stop_policy:
  graceful_timeout: 30s
  pidfile_absent: true                      # the declared pidfile must be gone
  files_absent: [/run/postgresql/.s.PGSQL*] # stale sockets/locks (globs)
  clean_after_stop: false                   # master opt-in: delete on stop
```

- Un pidfile persistente o una coincidencia de `files_absent` es un **warning** (el stop sigue
  teniendo éxito, `ResultOK`) integrado en el mensaje de resultado y mostrado en CLI/web —
  significa que el servicio crasheó o dejó basura. Los *procesos* residuales mantienen su
  manejo más fuerte de `orphan_processes` (rojo) vía el reaper.
- **`clean_after_stop`** es el único interruptor maestro para *todo* el borrado activo
  tras un stop limpio. Es **opt-in (por defecto `false`)**: con él apagado el motor
  solo **verifica y avisa** — nunca borra. Establézcalo a `true` para habilitar
  la limpieza, que entonces hace dos cosas:
  1. **borra** cualquier artefacto persistente de `pidfile_absent`/`files_absent` (el viejo
     comportamiento de `rm` al parar), volviendo a avisar solo si el borrado falla; y
  2. **borra** la lista `clean_on_stop` de abajo.

`clean_on_stop` lista ficheros y directorios a **borrar** en un stop limpio (una
limpieza de mantenimiento, distinta del invariante `files_absent`). Solo borra
cuando `clean_after_stop: true`; listado sin el flag maestro es inerte (así que puede
preparar la lista y habilitarla después):

```yaml
stop_policy:
  clean_after_stop: true                        # required to actually delete
  clean_on_stop:
    - /run/svc/foo.tmp                          # a file
    - /tmp/svc-*.lock                           # a glob (files)
    - { path: /var/cache/svc, recursive: true } # a directory tree
```

- Una entrada simple (string o glob) se borra con `Remove` (fichero o dir vacío);
  `{ path, recursive: true }` borra un árbol de directorios (`RemoveAll`).
- **Seguridad (estricta):** cada path debe ser absoluto; una entrada `recursive` debe ser un
  path concreto (no-glob) de al menos dos niveles de profundidad y no la raíz del filesystem ni
  un directorio de sistema superficial (`/`, `/etc`, `/usr`, `/var`, `/var/lib`, …) — esos
  se rechazan en tiempo de validación. Un fallo de borrado es un warning, no un fallo.

### Atajo `pidfile:` y `pidfiles:` (selectores + health checks)

Un servicio de catálogo puede declarar un `pidfile: <path>` de nivel superior para conectar **ambos** usos de un
pidfile desde una línea:

```yaml
pidfile: /run/named/named.pid
```

Cuando un servicio de catálogo usa legítimamente nombres de pidfile diferentes entre distribuciones,
declare candidatos en orden de preferencia:

```yaml
pidfile:
  - /run/mysqld/mariadb.pid
  - /run/mysqld/mysqld.pid
```

Cuando el pidfile es útil en un backend pero está legítimamente ausente en otro
(por ejemplo OpenRC lo escribe mientras una unidad systemd ejecuta el daemon en
primer plano), conserve la fuente de pidfile para descubrimiento pero haga auxiliar
el health check generado:

```yaml
pidfile: { path: /run/rngd.pid, optional: true }
```

Use `/run` aquí, no `/var/run`. Si un init script de distro o un gestor de servicios
reporta `/var/run/...`, escriba el path equivalente `/run/...` en la definición del servicio
de catálogo preservando la compatibilidad Linux/init. Antes de confirmar un nuevo
path de pidfile o socket, resuélvalo con `readlink -f` o inspecciónelo con
`namei -l`; si algún componente es un symlink, use el target canónico resuelto.

En la resolución esto crea (a) un selector interno de descubrimiento de pidfile — de modo que el
proceso padre **y sus descendientes** se descubren y monitorizan sin
añadir una entrada `processes:` pública — y (b) un health check `pidfile` controlado por
`requires: [service]`. Debido al control, un pidfile ausente u obsoleto se
reporta como un **error solo mientras el servicio está activo** (significa que el servicio
murió o perdió su pidfile sin que el gestor de servicios lo notara); un servicio
legítimamente parado se omite, no se alarma. Un check ya llamado `pidfile` se
respeta, de modo que un servicio de catálogo que necesite un check personalizado todavía puede deletrearlo. Las entradas
`processes:` públicas se mantienen limitadas a selectores `exe`/`cmd` con `user`/`group`
opcionales; no ponga `pidfile` bajo `processes:`. El path del atajo puede
referenciar variables (p. ej. `pidfile: "${pidfile}"`) y acepta un path escalar,
una lista de candidatos, o `{path: ..., optional: true}`. Las listas de candidatos
se prueban en orden y pasan en el primer pidfile vivo; si ninguno existe, el fallback
de PID del backend todavía puede satisfacer el health check controlado. `optional: true`
mantiene un pidfile ausente como warning en vez de hacer que el servicio no esté sano.

Cuando un único servicio posee varios procesos residentes independientes, use
`pidfiles:` como un map indexado por rol de proceso. Cada rol también debe existir bajo
`processes:` con `exe` y `user` exactos, de modo que el PID del pidfile pueda atarse de vuelta
a la identidad de proceso que Sermo tiene permitido observar:

```yaml
pidfiles:
  smbd: /run/samba/smbd.pid
  nmbd: /run/samba/nmbd.pid

processes:
  smbd:
    exe: "${smbd_binary}"
    user: root
  nmbd:
    exe: "${nmbd_binary}"
    user: root
```

Cada `pidfiles.<role>` crea su propio selector de pidfile interno y su propio
health check controlado (`pidfile-smbd`, `pidfile-nmbd`, ...). Un valor todavía puede ser una
lista de candidatos para ese rol específico. No combine `pidfile:` y
`pidfiles:` en el mismo servicio: `pidfile:` significa "un PID lógico con
paths candidatos"; `pidfiles:` significa "todos estos roles deben tener un
pidfile vivo."

### Atajo `socket:` (health check controlado)

Un servicio de catálogo puede declarar un path de socket Unix de nivel superior cuando el servicio activo debe
dejar un socket detrás:

```yaml
variables:
  socket: /run/cups/cups.sock
socket: { path: "${socket}", optional: true }
```

En la resolución esto crea un health check `socket` controlado por `requires: [service]`
y elimina la clave de nivel superior. Como `pidfile:`, `socket:` acepta un path escalar,
una lista de candidatos, o `{path: ..., optional: true}`. Úselo para sockets de runtime
propiedad del servicio; los checks de protocolo como `redis`, `dbus` o `libvirt` siguen
usando su propio campo `socket` dentro del cuerpo del check.

### Atajo `lockfile:` (health check controlado)

Un servicio de catálogo puede declarar un lockfile regular creado por el servicio activo:

```yaml
lockfile: /run/lock/subsys/smb
```

En la resolución esto crea un health check `lockfile` controlado por
`requires: [service]` y elimina la clave de nivel superior. Como `socket:`, `lockfile:`
acepta un path escalar, una lista de candidatos, o `{path: ..., optional: true}`. Es
solo evidencia de que el servicio dejó su propio artefacto de lock de runtime; no
bloquea start/stop/restart/reload/resume y no debe apuntar bajo
`<paths.runtime>/locks`, que está reservado para los locks de operación de Sermo.

## Servicios versionados

Algunas aplicaciones envían un binary por versión y varias pueden estar instaladas a la
vez (php-fpm, postgres, tomcat, erlang/beam, berkeley db). En lugar de un fichero
por versión, escriba una única **plantilla de versión de app** cuyo `name:` contenga
`%v`, con `${version}` en el path de descubrimiento. Una plantilla de servicio con el mismo
token enlaza esa app.

```yaml
name: postgres-%v
display_name: "PostgreSQL ${version}"
variables:
  binary: "/usr/lib64/postgresql-${version}/bin/postgres"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }

---
name: postgres-%v
display_name: "PostgreSQL ${version}"
service:
  systemd: ["postgresql-${version}", "postgres-${version}"]
  openrc: ["postgresql-${version}", "postgres-${version}"]
apps: ["postgres-${version}"]
variables:
  data_dir: /var/lib/postgresql/${version}/data
pidfile: "${data_dir}/postmaster.pid"
```

Al cargar, Sermo descubre versiones de app haciendo glob del path `variables.binary` de la
app enlazada con `${version}` como comodín (aquí
`/usr/lib64/postgresql-*/bin/postgres`) y extrayendo lo que lo llenó. Las plantillas de servicio
en `catalog/services` prefieren el servicio de init activo como fuente de
verdad: los candidatos `service:` con token se emparejan contra unidades systemd/OpenRC
activas, y solo materializan los servicios que coinciden. Cada coincidencia se convierte en un
app o servicio concreto con `%v` y `${version}` sustituidos en todas partes (name,
display_name, service, app links, ...) — `postgres-14`, `postgres-16`, ... — y
las propias plantillas se descartan. Si nada está instalado o ningún servicio
coincidente está activo, la plantilla no produce nada. El nombre de fichero YAML no tiene
que coincidir con `name:`; mantenga un fichero descriptivo para la plantilla y trate `name:`
como el identificador de catálogo. `%v` puede estar en cualquier lugar del nombre (`db%vsql` →
`db4.8sql`). Nota: `%v` se sustituye solo en el nombre; dentro del cuerpo use siempre
`${version}` (p. ej. en `service` o `apps`).

Prefiera el descubrimiento de aplicación en `catalog/apps` cuando el path del binary instalado
identifique la versión o instancia. Un servicio versionado o instanciado que enlaza una
app coincidente, como `apps: ["postgres-${version}"]` o
`apps: ["php-fpm${version}"]`, usa esa app para validación de binary de runtime. Para
servicios de catálogo, ponga los mismos tokens en `service:` para que el servicio materialice
desde la unidad que está realmente activa en el backend de init seleccionado.

`variables.binary` puede ser un string o una lista de candidatos. Úselo cuando el
path versionado es también el ejecutable de runtime que los checks de preflight y versión
deberían sondear. Para plantillas de app y librería que descubren desde `versions.from` y
no declaran `variables.binary`, el documento materializado vincula
`${binary}` al path que coincidió; mantenga `versions.from` para fuentes de descubrimiento
que no sean el ejecutable de runtime.

Cuando una app o librería no puede descubrir desde su ejecutable de runtime, use
`versions.from` allí y enlace la app genérica o versionada que posee el binary:

```yaml
name: myservice-%i
versions:
  from: "/etc/myservice/${instance}.conf"
variables:
  binary: /usr/sbin/myservice
preflight:
  binary: { type: binary, path: "${binary}" }
```

`versions.from` es metadato solo de descubrimiento; nunca aparece en apps o servicios
materializados. Las coincidencias se deduplican por su tupla de token materializada.

Una versión descubierta debe empezar con un dígito, de modo que los hermanos de un placeholder
final sin límite (un symlink `php-fpm` simple, un `php-fpm.conf`) no se confundan
con versiones. Aun así, un placeholder acotado en ambos lados (p. ej.
`/usr/lib64/php${version}/bin/php-fpm`, en el path `variables.binary` de la app) descubre con más
precisión.

### Placeholders de entero e instancia

`%v`/`${version}` acepta una versión que empieza con dígito (`8.3`, `12.0.2`); use
`%n`/`${n}` cuando el valor es un **entero simple** — coincide solo con números
enteros, de lo contrario funcionando exactamente como `%v`:

```yaml
name: python%n
display_name: "Python ${n}"
variables:
  binary: "/usr/bin/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
```

`/usr/bin/python*` entonces materializa `python2`/`python3`, pero no `python3.11` ni
`python-config`.

Cuando una plantilla `%v` o `%n` simple también tiene un binary de slot activo sin versión,
Sermo lo materializa automáticamente. Si `/usr/bin/python` existe, esto registra
`python` además de `python2`/`python3`; cuando está ausente, solo se registran los
binarios numerados. El token vacío se sustituye antes de que `name`,
`display_name` y `description` se recorten, de modo que `display_name: "Python ${n}"`
se convierte en `Python` para el slot activo. Las plantillas compuestas (`%i` más `%v`, un
token separador, etc.) no infieren esa entrada de `versions.from`; declare
`versions.current_from` cuando tengan un ejecutable de slot activo concreto como
`/usr/bin/java`. Ese path materializa el nombre base sin versión antes del
primer token (`java-%i-%v` -> `java`) y se convierte en su `${binary}` cuando la
plantilla no declara uno. `current_from` también puede ser una lista de paths directos:

```yaml
versions:
  current_from: /usr/bin/java
```

Establezca `versions.unversioned: false` para ignorar el slot activo sin marcador o de
`current_from`; una forma de map todavía puede sobrescribir campos para la instancia sin versión
cuando una plantilla necesita una etiqueta personalizada:

Si una plantilla materializaría un `name:` que ya existe como documento explícito
en la misma categoría de catálogo, la validación reporta una colisión. Elimine
una definición o ajuste el descubrimiento de la plantilla; Sermo no elige silenciosamente
entre un documento explícito y uno generado.

Las plantillas también pueden usar `${current}` en `display_name` o `description`. Durante
la materialización se convierte en `current` solo para la entrada versionada cuyo binary es
la misma entrada de filesystem que el binary de slot activo, ya sea descubierto desde el
path sin marcador o declarado con `versions.current_from` (por ejemplo
`/usr/bin/php -> /usr/bin/php8.2` o `/usr/bin/java` apuntando al JVM activo);
de lo contrario se convierte en vacío antes de que los metadatos se recorten. Esto permite que
`display_name: "PHP ${version} ${current}"` se renderice como `PHP 8.2 current` para la
versión activa y `PHP 8.3` para las demás sin ejecutar comandos de versión
durante la carga de config. Los symlinks se resuelven antes de la comparación. Los comandos de
inventario de app/servicio todavía pueden añadir la etiqueta `current` en tiempo de inspección cuando un
wrapper de slot activo reporta el mismo `version_short` que una versión
materializada, lo que mantiene wrappers como el Java genérico de Gentoo sin metadatos de
catálogo `from_file`.

```yaml
name: python%n
display_name: "Python ${n}"
versions:
  unversioned:
    description: "Active Python interpreter"
variables:
  binary: "/usr/bin/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
```

Use `%i`/`${instance}` para instancias de init con nombre descubiertas desde metadatos de
servicio acotados. Limite el descubrimiento específico de backend a candidatos de servicio
coincidentes; por ejemplo, un perfil OpenRC heredado puede exponer solo `service.openrc:
["openvpn.${instance}"]`, mientras una plantilla systemd puede exponer
`service.systemd: ["openvpn-client@${instance}"]`.

### Nombres compuestos con un separador (`%s`)

Algunos servicios codifican **tanto** una versión como un entorno/pool en un nombre, unidos
por `-` o `_` — `tomcat-8.5-main`, `tomcat-9-guacamole`, `php-fpm8.4_airbnb`. Use
`%s`/`${sep}` para ese separador de unión, que coincide con un string vacío, `-` o
`_`. Un nombre puede llevar varios tokens (`tomcat-%v%s%i`); para plantillas de servicio
se descubren juntos desde unidades de servicio activas cuyos candidatos `service:`
contienen los mismos marcadores, y se vinculan todos a la vez. Un `%v` no final está
acotado para que se detenga en el separador (`8.5`), y la instancia puede estar vacía —
cuando lo está, el separador también colapsa, de modo que un `tomcat@8.5.service` simple
materializa `tomcat-8.5` sin un `-` final:

```yaml
name: tomcat-%v%s%i
service:
  openrc: ["tomcat-${version}${sep}${instance}"]
  systemd: ["tomcat@${version}${sep}${instance}"]
```

### Descubrimiento propiedad del servicio

Una plantilla de servicio en `catalog/services` normalmente descubre desde unidades init
activas. Ponga cada grafía de servicio soportada en `service:` y divídala por backend
cuando los nombres systemd/OpenRC difieren. La app enlazada (genérica como `openvpn`, o
versionada como `php-fpm${version}`) sigue suministrando `${binary}` para preflight e
identidad de proceso. Un servicio nunca descubre desde su propio *binary*.

Cuando el descubrimiento viene de metadatos de servicio de init, deje que la app enlazada posea la
validación de binary de runtime cuando esté versionada. Por ejemplo, PHP-FPM enlaza
`php-fpm${version}`; esa app ya valida `/usr/sbin/php-fpm${version}` o
`/usr/bin/php-fpm${version}`, de modo que el servicio no repite los mismos candidatos
en `versions.require`:

```yaml
service:
  systemd:
    - "php-fpm@${version}${sep}${instance}"
    - "php-fpm@php${version}${sep}${instance}"
    - "php-fpm-php${version}${sep}${instance}"
    - "php${version}${sep}${instance}-fpm"
    - "php-fpm${version}"
  openrc:
    - "php-fpm-php${version}${sep}${instance}"
    - "php${version}${sep}${instance}"
    - "php-fpm${version}${sep}${instance}"
    - "php-fpm${version}"
apps: ["php-fpm${version}"]
pidfile:
  - "/run/php-fpm/php-fpm-${version}${sep}${instance}.pid"
  - "/run/php-fpm/php-fpm-php${version}${sep}${instance}.pid"
  - "/run/php-fpm-php${version}${sep}${instance}.pid"
watches:
  pidfile:
    check:
      type: pidfile
      optional: true
      path:
        - "/run/php-fpm/php-fpm-${version}${sep}${instance}.pid"
        - "/run/php-fpm/php-fpm-php${version}${sep}${instance}.pid"
        - "/run/php-fpm-php${version}${sep}${instance}.pid"
      requires: [service]
```

Ponga la instancia systemd exacta primero en `service.systemd`, p. ej.
`php-fpm@${version}${sep}${instance}` para `php-fpm@8.2.service`. Evite un fallback
`php-fpm` systemd genérico en plantillas versionadas: puede hacer que varias
versiones de PHP-FPM descubiertas operen sobre la misma unidad. El check de pidfile es
opcional porque algunas unidades systemd publican `MainPID` incluso cuando el
`PIDFile=` declarado no se escribe.

### Componentes opcionales (`enable_if`)

Una entrada bajo `processes`, `watches` o `preflight` puede llevar un
guard `enable_if` que la mantiene solo cuando una clave en un fichero de config de distro satisface
un predicado; de lo contrario la entrada se descarta durante la resolución del servicio. Esto
modela componentes que son opcionales por host — p. ej. un perfil de Samba que enlaza un
app `winbindd` puede monitorizar `winbindd` solo cuando el
`daemon_list` de `/etc/conf.d/samba` lo nombra:

```yaml
processes:
  winbindd:
    exe: ${winbindd_binary}
    enable_if:
      file: /etc/conf.d/samba
      key: daemon_list
      contains: winbindd          # or: equals: <value> | matches: <regex>
watches:
  winbindd:
    enable_if:
      file: /etc/conf.d/samba
      key: daemon_list
      contains: winbindd
    check:
      type: process
      exe: ${winbindd_binary}
      state: running
```

Un fichero ausente o una clave ausente poda la entrada (fail-safe). El guard se elimina
de las entradas supervivientes. `config validate` todavía comprueba las entradas deshabilitadas antes
de que se poden, de modo que los typos en definiciones de proceso/check opcionales se reportan.
`enable_if` intencionalmente no está soportado bajo `rules`, `policy`, `guards` u
otras secciones que afecten a la seguridad.

### Variables leídas desde un fichero de config (`from_file`)

Una variable puede tomar su valor de un fichero de config en lugar de un literal, útil cuando
un puerto o path está definido en la propia config del servicio. `directive:` lee el token
tras una línea `key value` (estilo OpenVPN/sshd); `pattern:` lee el grupo de captura 1 de
una regex; `default:` aplica cuando el fichero o la clave está ausente:

```yaml
variables:
  config: "/etc/openvpn/${instance}.conf"
  port:
    from_file: "${config}"
    directive: port              # "port 1194" -> 1194
    default: 1194               # required fallback when file/key is absent
  # tomcat: pattern: '<Connector[^>]*?\bport="(\d+)"'
```

Se evalúa durante la resolución (de modo que puede referenciar otras variables como
`${config}`) y se reevalúa en cada recarga de config. `pattern` también puede
referenciar variables como `${instance}`; esos valores se escapan como literales
de regex antes de leer el fichero. La spec de variable debe definir `from_file`,
`default`, y exactamente uno de `directive` o `pattern`. `pattern` debe compilar
e incluir un grupo de captura. Un fichero ausente o una clave no coincidente usa
`default`; specs malformadas o variables desconocidas en `from_file` / `pattern`
son errores de validación.

### Listar aplicaciones instaladas

`sermoctl apps` reporta las aplicaciones descritas por apps de catálogo: cuáles están
instaladas (su binary está presente y es ejecutable), si su comando `health`
tiene éxito cuando está configurado, y la versión que su comando `version`
reporta. La columna VERSION muestra la versión corta por defecto; añada `--long` para
mostrar el string crudo completo.

```text
APPLICATION   VERSION  STATUS
Nginx         1.24.0   ok
Python 3      3.11.2   ok
Redis         -        error: /usr/bin/redis-server is not executable
```

```text
$ sermoctl apps --long
APPLICATION   VERSION                      STATUS
Nginx         nginx version: nginx/1.24.0  ok
Python 3      Python 3.11.2                ok
```

Solo se muestran las aplicaciones instaladas; `sermoctl apps all` también lista el resto como
`not installed`. Los mismos `--long` y `all` aplican a `sermoctl libs` y
`sermoctl services`. Con plantillas de versión esto lista cada versión instalada como
su propia fila (p. ej. `PHP-FPM 8.3`, `PHP-FPM 7.4`). Para `sermoctl services`, los comandos
de versión son datos de inventario best-effort: una sonda de versión específica de distro fallida
deja la versión desconocida en lugar de marcar el servicio instalado como un error.
`--json` no se ve afectado por `--long` — siempre emite ambos, con los estructurados
`name`, `display_name`, `binary`, `version`, `version_short`,
`version_source`, `installed`, `ok` y `status`.

Cuando una app declara `health`, Sermo lo usa como la sonda de health preferida para
`sermoctl apps`/`libs`/`services` y la lista de aplicaciones de la WebUI. Solo el código
de salida se evalúa (`expect_exit`, por defecto `0`, o una lista como `[0, 1]`);
los matchers de stdout/stderr y la salida impresa se ignoran para health. El comando
`version` solo se usa como sonda de health de fallback cuando no existe ningún comando
`health`; cuando existe `health`, `version` reporta datos de display y un
fallo de versión no anula a health.
No marque una sonda `version` de app como opcional salvo que la app también tenga una sonda
`health`; de lo contrario Sermo solo puede probar que el binary existe, no que puede ejecutarse.
Para apps de catálogo que son binarios separados del mismo paquete, `version_from`
puede apuntar a otra app de catálogo cuya sonda de versión suministre la versión
mostrada. La app todavía comprueba su propio `variables.binary` y health;
`version_from` solo
establece `version`/`version_short` cuando la app no tiene resultado de versión local.

Las apps de catálogo pueden usar `version_match` cuando un nombre de binary es compartido por
implementaciones compatibles. Corre contra el stdout/stderr combinado del comando
`version` local y soporta `contains`, `excludes` y `regex`. Si falla,
la app se trata como no instalada en lugar de como una app instalada con una versión
mala. Por ejemplo, MariaDB acepta `mysqld` solo cuando la salida contiene
`MariaDB`, mientras MySQL excluye ese token para que el `mysqld` de compatibilidad de MariaDB
no aparezca como MySQL.

`version` es la primera línea cruda que el comando de versión imprime (p. ej. `nginx version:
nginx/1.30.2`); `version_short` la reduce a solo la versión numérica y como
máximo el patchlevel (`1.30.2`), tomando el primer token `major.minor[.patch]` y
descartando cualquier componente de build y sufijo posterior (de modo que `2.8.4.1-0+g…` se convierte
en `2.8.4` y `4.2.8p18` se convierte en `4.2.8`). Si no hay token con puntos, un
token `version N` solo-entero acotado se acepta para proyectos como polkit y
releases de numad con código de fecha. Está vacío cuando la línea de versión no lleva ningún
número reconocible.

Un servicio de catálogo puede en su lugar declarar un comando `version_short` dedicado (bajo
`preflight` o `commands`, junto a `version`) que imprime la versión bare
él mismo, esquivando la regex cuando una herramienta puede reportarla directamente. Su primera
línea de salida no vacía se usa entonces verbatim. Las apps de intérprete empaquetadas hacen
esto con su binary resuelto — p. ej. PHP ejecuta `php -r 'echo PHP_VERSION;'`,
Python ejecuta `python -c 'import platform;print(platform.python_version())'`, Node
`node -p process.versions.node` — de modo que su versión corta nunca depende de
parsear. Cuando no se configura tal comando (o da error o no imprime nada),
`version_short` recurre a parsear la línea `version` como arriba.

```yaml
preflight:
  health:        { type: command, command: ["${binary}","-h"], timeout: 10s }
  version:       { type: command, command: ["${binary}","-v"], timeout: 10s }
  version_short: { type: command, command: ["${binary}","-r","echo PHP_VERSION;"], timeout: 10s }
```

Una plantilla de servicio puede `uses` un servicio base para heredar sus checks, procesos y
reglas, mientras una app enlazada suministra el binary específico de instancia o versión. El
servicio empaquetado `nebula-%i` se construye sobre el servicio base `nebula` y enlaza la
app `nebula-${instance}`:

```yaml
name: nebula-%i
uses: nebula
display_name: "Nebula ${instance}"
apps: ["nebula-${instance}"]
```

Un servicio configurado entonces apunta a una instancia concreta, p. ej. `uses: nebula-vpn0`.

## Unidad de servicio

La identidad del servicio es su `name`; `service` declara el nombre o nombres de
unidad init sobre los que operar. La forma más simple es un único nombre que funciona en ambos
sistemas init:

```yaml
service: apache2
```

Cuando el nombre de unidad difiere entre sistemas init, liste candidatos por init; Sermo
resuelve el primero que el backend activo realmente conoce (systemd vía
`systemctl cat`, OpenRC vía el init script):

```yaml
service:
  systemd: [apache2, httpd]
  openrc:  [apache2, apache]
```

Los candidatos son nombres bare — systemd añade `.service` automáticamente. Se
prueban en orden y se deduplican, y el nombre resuelto se usa para todas las operaciones
posteriores. Un `service` **escalar** se confía incluso cuando la sonda no puede
mostrarlo (p. ej. unidades generadas por sysv). Una **lista por init** primero requiere una
coincidencia de backend; si la sonda no puede mostrar una, Sermo loguea o imprime un warning y
recurre a la unidad seed configurada para que `sermod`, la web UI y `sermoctl` se comporten
igual en setups históricos de init-service. Un sistema init sin entrada significa que el
servicio *no está disponible* allí. Los servicios que usan `control:` (libvirt/docker) no
usan el fallback de init-unit.

Una instancia habilitada puede sobrescribir la unidad con un escalar (p. ej.
`service: redis-cache`) para correr como su propia unidad, u omitir `service` por completo para
heredar los candidatos del servicio de catálogo.

## Clonado

Un servicio puede `clone` otro servicio para hacer una segunda instancia:

```yaml
name: redis-cache
clone: redis-main
variables:
  port: 6380
  pidfile: /run/redis-cache/redis.pid
```

Clone copia el origen **antes** de la expansión de variables, de modo que sobrescribir solo la
variable `port` es suficiente — cada check que referencia `${port}` resuelve al
nuevo valor. Las cadenas de clone resuelven transitivamente; los ciclos se rechazan.

## Múltiples instancias de una aplicación

Para correr varias instancias de la misma aplicación — mismo binary, mismos checks y
reglas, diferente puerto de escucha, pidfile y fichero de config — deje que cada instancia `uses`
el servicio de catálogo y sobrescriba solo sus variables únicas.

El servicio de catálogo parametriza todo lo que varía con placeholders `${...}` y
enhebra cada uno en los comandos y checks que lo consumen. En particular el path del
fichero de config debería ser una variable conectada a cada comando que lo lee, de modo que
dos instancias nunca recojan la configuración de la otra:

```yaml
name: dbserver
variables:
  port:    3306
  pidfile: /run/dbserver/main.pid
  config:  /etc/dbserver/main.cnf
pidfile: "${pidfile}"
watches:
  tcp:
    check: { type: tcp, port: "${port}" }
  config:
    check: { type: command, command: ["dbserverd", "--defaults-file=${config}", "--help"] }
```

Cada instancia sobrescribe las tres variables y se da a sí misma una unidad init (una
instancia de plantilla systemd o un nombre de unidad distinto) con un `service` escalar:

```yaml
name: db-inst1
uses: dbserver
service: db-inst1
variables:
  port:    3306
  pidfile: /run/dbserver/inst1.pid
  config:  /etc/dbserver/inst1.cnf
```

Una segunda instancia es el mismo fichero con su propio nombre/unidad y variables (p. ej.
`name: db-inst2`, `service: db-inst2`, `port: 3307`, los paths `inst2.*`).

Prefiera `uses` sobre [`clone`](#cloning) aquí: cada instancia deriva del
*servicio de catálogo* y solo sobrescribe variables. Recurra a `clone` solo cuando una instancia
deba copiar *otro servicio concreto* casi verbatim. Véase [`docs/sermo-all.yml`](sermo-all.yml)
para una configuración trabajada completa.

## Deshabilitar y borrar entradas heredadas

```yaml
watches:
  http:
    enabled: false   # keep but disable
  ping:
    delete: true     # remove the inherited entry
```

## Flag de monitorización

El flag `monitor` de nivel superior establece el comportamiento de monitorización de un servicio cuando el
daemon arranca:

```yaml
name: web
uses: nginx
monitor: enabled    # enabled (default) | disabled | previous
```

- **`enabled`** (el valor por defecto cuando el flag está ausente): siempre monitorizar al arrancar.
- **`disabled`**: nunca monitorizar — el worker existe pero cada ciclo se omite.
- **`previous`**: restaurar el estado de runtime que el servicio tenía antes de que el daemon
  parara por última vez. En la primera ejecución (sin estado registrado) por defecto es
  monitorizado.

`enabled: false` de nivel superior deshabilita el servicio por completo; no se construye ningún worker.
Con `monitor`, el worker existe y solo cambia la ejecución de check/regla.

El estado vivo se conmuta en runtime con `sermoctl monitor <svc>` /
`sermoctl unmonitor <svc>` y se persiste en la base de datos de estado bajo
`paths.state` (véase [configuration](configuration.es.md)). Como esa base de datos
sobrevive reboots, un servicio `previous` vuelve a subir en el estado en que un
operador lo dejó por última vez.

Los documentos de watch de host usan los mismos valores de nivel superior
`monitor: enabled | disabled | previous`; véase
[configuration](configuration.es.md#host-watches).

Un servicio también puede llevar su propio bloque `watches:` — watches por
servicio que pueden disparar un hook/notificación o un `then.action` compacto,
y pueden usar los tipos `service`/`metric` y el `process_count` acotado por PIDs. Véase
[Watches de servicio](configuration.es.md#watches-de-servicio-acotados-a-un-servicio).

## Comandos auxiliares

`commands` declara comandos auxiliares con nombre. Sermo nunca los ejecuta como checks
genéricos, pero los **nombres reservados** son consumidos por features:

- **`health`** — ejecutado por los listados `sermoctl apps`/`libs`/`services` y la
  lista de aplicaciones de la WebUI para decidir si una aplicación instalada está sana.
  Usa la misma búsqueda `preflight.<name>` luego `commands.<name>` que
  `version`, pero solo comprueba el código de salida. Cuando está presente, tiene precedencia
  sobre `version` para health de app; `version` permanece solo-display.
- **`version`** (y `version_short`) — ejecutado por los listados `sermoctl apps`/`libs`/
  `services` para reportar la versión de un servicio, y **cada ciclo** por el
  monitor `version.on_change` (véase [Condiciones de salud del servicio](rules.es.md#service-health-conditions-version--state--config)).
  Ese monitor compara el `version_short` numérico, y un opcional
  `version.on_change.level` (`major`/`minor`/`patch`, por defecto `patch`) selecciona en
  qué granularidad `a.b.c` debería alertar un cambio.
  El monitor hereda el `dry_run` del service, por lo que la entrega de notificaciones
  no-console se suprime mientras el service esté en dry-run.
  Cuando ambos existen, `preflight.version` tiene precedencia sobre `commands.version`.
  También declaran variables `version` y `version_short` con valores por defecto vacíos
  para expansión; las apps enlazadas las exponen a los servicios como `${app_version}` y
  `${app_version_short}`. Otros valores derivados de comando pueden declararse con
  `export:`, cuya fuente por defecto es el stdout recortado y cuyo valor por defecto es
  vacío.

Cualquier otra entrada es solo informativa. Una ejecución puede afirmar su resultado, de la misma
forma que un hook de watch o un check `command` lo hace: `expect_exit` (por defecto 0, o una lista
como `[0, 1]`) y matchers opcionales `expect_stdout`/`expect_stderr` — un
substring o una comparación `{op, value}` (`== != > >= < <= contains =~`).
Los comandos reservados también pueden establecer `user` (nombre de usuario o UID numérico) para ejecutar el
argv como ese usuario del OS cuando Sermo tiene permiso para cambiar de usuario.

```yaml
commands:
  version:
    user: www-data
    command: ["apachectl", "-v"]
    timeout: 5s
    expect_exit: 0                                   # optional, default 0
    expect_stdout: { op: "=~", value: "Apache/2" }   # optional: match the output
```
