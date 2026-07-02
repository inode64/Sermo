# Configuración

La configuración de Sermo se divide por tipo de destino: **definiciones de
service/app/lib/pattern del catálogo**, **services** como instancias concretas
monitorizadas, **notifiers** como destinos de entrega, **watches** como monitores
a nivel de host, y **mounts** como unidades de montaje respaldadas por fstab. Los
archivos de watch y notifier son fragmentos globales con un mapa de nivel superior
`watches:` o `notifiers:`; esos fragmentos no usan `kind:`.

La nueva configuración debe usar un archivo YAML por destino. Esto significa una
app, daemon, lib o pattern del catálogo por archivo; un service por archivo; un
mount por archivo; un notifier por archivo; y un host watch por archivo (`storage`,
`network`, `uplink`, `load` y otros fragmentos de watch). Los archivos de fragmentos
globales siguen teniendo el mapa de nivel superior `watches:` o `notifiers:`, pero
ese mapa debe contener exactamente una entrada con nombre. Esto mantiene la
configuración generada fácil de comparar, reemplazar y limpiar por destino.

El **kind de un documento se determina por dónde reside** — su subdirectorio de
catálogo (`services/` → service, `apps/` → app, `libs/` → lib, `patterns/` →
patterns) o la ruta configurada desde la que se carga (`paths.services` → service,
`paths.mounts` → mount). Una definición `services/` del catálogo (un *catalog
service*) y una instancia de `paths.services` (un *configured service*) comparten el
kind `service`; se mantienen distintos por ubicación. Por tanto, una clave de nivel
superior `kind:` es **opcional y redundante**; cuando está presente en un archivo
desplegado debe coincidir con la ubicación, lo que detecta un archivo colocado en el
directorio equivocado. La configuración distribuida la omite.

> **Ejemplo completo anotado.** [`docs/sermo-all.yml`](sermo-all.yml) muestra
> cada superficie de configuración en un solo lugar — configuración global, watches y
> un documento de cada kind (un service, app, lib, pattern del catálogo, un service
> configurado y un mount), más un ejemplo de service clonado — y está validado por la
> suite de pruebas, por lo que no puede
> desviarse del esquema. Es solo un paquete de referencia; los despliegues reales
> mantienen un destino por archivo. La configuración operativa distribuida es
> `examples/sermo.yml`.
> Desde un checkout del código fuente, usa `examples/sermo-dev.yml` para validar el
> árbol de ejemplos incluido sin reescribir las rutas instaladas en `/etc/sermo`.

## Cambios de esquema

El esquema documentado es el contrato actual. Cuando se elimina un campo de
configuración propiedad de Sermo, un alias o una forma YAML, no mantengas fixtures de
compatibilidad ni pruebas que sigan deletreando la forma eliminada. Las pruebas deben
cubrir la forma canónica actual y, cuando la validación estricta necesite cobertura,
usar campos o tipos desconocidos genéricos en lugar de nombres de configuración
retirados. Los requisitos de compatibilidad externa, como los metadatos de Linux/init
que aún reportan `/var/run` y se normalizan a `/run`, deben documentarse como
excepciones explícitas en el propietario.

## Disposición

```
/etc/sermo/sermo.yml              global config
/usr/share/sermo/catalog/{services,apps,libs,patterns}/*.yml   packaged catalog
/usr/share/sermo/examples/        packaged examples operators may copy/adapt
/etc/sermo/catalog-available/{services,apps,libs,patterns}/*.yml   user catalog definitions
/etc/sermo/services/*.yml concrete service documents
/etc/sermo/apps/*.yml     host-specific app documents
/etc/sermo/mounts/*.yml   fstab-backed mount documents
/etc/sermo/notifiers/*.yml notifier fragments
/etc/sermo/storages/*.yml storage watch fragments
/etc/sermo/networks/*.yml network watch fragments
/etc/sermo/watches/*.yml  generic host watch fragments
/etc/sermo/templates/*.yml notification templates
```

Los directorios que Sermo lee provienen de `paths` en la configuración global:

```yaml
paths:
  catalog:
    - /usr/share/sermo/catalog
    - /etc/sermo/catalog-available
  services:
    - /etc/sermo/services
  apps:
    - /etc/sermo/apps
  notifiers:
    - /etc/sermo/notifiers
  storages:
    - /etc/sermo/storages
  networks:
    - /etc/sermo/networks
  watches:
    - /etc/sermo/watches
  mounts:
    - /etc/sermo/mounts
  runtime: /run/sermo
  state: /var/lib/sermo
  templates: /etc/sermo/templates
```

Las listas de directorios bajo `paths.catalog`, `paths.services`, `paths.apps`,
`paths.notifiers`, `paths.storages`, `paths.networks`, `paths.watches` y
`paths.mounts` aceptan o bien una cadena de ruta o un mapeo explícito:

```yaml
paths:
  services:
    - /etc/sermo/services          # recursive: false
    - path: /etc/sermo/services.d
      recursive: true
```

Cuando se omite `recursive`, su valor por defecto es `false`. Una entrada no recursiva
carga solo los archivos `.yml`/`.yaml` directamente dentro de ese directorio.
`recursive: true` desciende por todo el subárbol, cargando aún los archivos en orden
ordenado determinista. Las claves desconocidas bajo `paths` se rechazan para que los
errores tipográficos no deshabiliten silenciosamente una fuente configurada.
Para `paths.catalog`, los documentos del catálogo deben residir bajo los directorios
de categoría inmediatos `services/`, `apps/`, `libs/` o `patterns/`. Esos
directorios de categoría son parte de la disposición del catálogo y se leen incluso
cuando `recursive` es false; `recursive: true` solo controla los directorios por
debajo de esos directorios de categoría.

`paths.runtime` es la raíz para los locks de runtime con nombre (`<runtime>/locks`,
un archivo por lock llamado `<service>[.<name>].lock`) y los locks de operación
internos (`<runtime>/ops/<service>.lock`). Reside en tmpfs y se borra al reiniciar.
`paths.locks` **no** está soportado. Consulta [Locks](safety.es.md#locks) para la
semántica de TTL y de reclamación de locks obsoletos.

Si se omite `paths.catalog`, Sermo lee los valores por defecto del catálogo
instalado: `/usr/share/sermo/catalog` y `/etc/sermo/catalog-available`.

Solo los directorios de documentos de service, app y mount tienen alternativas
relativas a la configuración. Si se omite `paths.services`, `paths.apps` o
`paths.mounts`, Sermo recurre a `services/`, `apps/` o `mounts/` junto al archivo
`sermo.yml` cargado. Con el estándar `/etc/sermo/sermo.yml` esto significa
`/etc/sermo/services`, `/etc/sermo/apps` y `/etc/sermo/mounts`.

Los directorios de fragmentos globales no tienen alternativa implícita. Si se omite o
está vacío `paths.notifiers`, `paths.storages`, `paths.networks` o `paths.watches`,
Sermo no carga ningún fragmento de ese tipo; un directorio hermano `notifiers/`,
`storages/`, `networks/` o `watches/` junto a `sermo.yml` se ignora hasta que se
liste explícitamente bajo `paths`.

Cada nuevo fragmento de service, mount, notifier o watch bajo directorios configurados
debe aislarse en su propio archivo `.yml`, incluso cuando varios destinos se generan
en la misma ejecución del asistente. Los documentos de mount son intencionadamente
distintos de los documentos de service y los fragmentos de watch porque son acciones
del operador, no services monitorizados.

Usa `/run` para las rutas de runtime en la configuración y los ejemplos de Sermo. No
escribas nuevos pidfiles, sockets, lockfiles ni directorios de runtime en `/var/run`
en la configuración propiedad de Sermo.
Linux mantiene `/var/run` como ruta de compatibilidad para `/run`, y los scripts de
init más antiguos, gestores de servicios o configuraciones empaquetadas pueden seguir
reportándola; Sermo normaliza esas rutas proporcionadas por el host a la grafía
`/run/...` equivalente.
Usa `pidfile:` para un proceso lógico con rutas candidatas de pidfile, y
`pidfiles:` para varios roles de proceso requeridos. `pidfiles.<role>` debe tener un
`processes.<role>` coincidente con `exe` y `user` exactos.
Usa `lockfile:` solo para un archivo de runtime regular creado por el propio servicio;
es un artefacto de salud como `socket:`, no un lock de operación.

Antes de añadir una nueva ruta de runtime, resuélvela en el host de destino:

```sh
readlink -f /var/run/example.pid
namei -l /var/run/example.pid
```

Si la ruta se resuelve a través de un symlink, configura la ruta de destino canónica
en su lugar. Esto es especialmente común para la compatibilidad de Linux `/var/run` →
`/run`, pero también puede ocurrir con directorios de runtime específicos de la app.

Las apps del catálogo pueden declarar `version_from: <app-name>` cuando un binario
diferente del mismo paquete tiene la sonda de versión autorizada. La app sigue
comprobando su propio `variables.binary` para la instalación y la salud; `version_from`
solo rellena la
versión mostrada cuando la app no tiene resultado de versión local. Los comandos
locales `health`, `version` y `version_short` siguen prevaleciendo. La app referenciada
debe ser otra app del catálogo direccionada por su nombre canónico, y las cadenas
`version_from` no deben formar ciclos.
Esto no es una dependencia operativa y no inyecta comprobaciones de preflight en los
services.

Las apps del catálogo también pueden declarar `version_match` para demostrar la
identidad de un binario de compatibilidad antes de considerar la app instalada. El
matcher se evalúa contra la salida combinada stdout/stderr del comando `version` de la
app y soporta los matchers de cadena `contains`, `excludes` y `regex`. Un
`version_match` fallido marca la app como no instalada, incluso cuando el binario
existe; esto permite que MariaDB use una alternativa `/usr/sbin/mysqld` más antigua sin
mostrar además la app MySQL del catálogo en hosts con MariaDB. Cuando un service enlaza
la app a través de `apps:`, el matcher se copia en el preflight de versión con espacio
de nombres de esa app.

Los documentos de catalog service y service pueden declarar `aliases: [...]`, una lista
de nombres simples alternativos. Los alias son metadatos: resuelven nombres pero nunca
se fusionan con el cuerpo del service en runtime. Los alias de catálogo permiten que
`uses:` acepte grafías de distribución como `apache2` para el perfil canónico `apache`.
Los alias de service permiten que los comandos `sermoctl` acepten nombres alternativos
y operen sobre el service configurado canónico. Los alias no deben duplicar otro nombre
o alias del mismo kind de documento.

Cuando un catalog service o service lista apps, cada variable de la app también está
disponible para ese catalog service/service con un prefijo de nombre de app
normalizado: una app con
`variables: { binary: /usr/bin/cupsd, cups_config: /usr/bin/cups-config }`
expone `${cupsd_binary}` y `${cupsd_cups_config}`. Las entradas de preflight de comando
llamadas `version` o `version_short` también declaran `${cupsd_version}` y
`${cupsd_version_short}` con valores por defecto vacíos; un `export:` de comando
explícito puede declarar variables adicionales. En runtime, las comprobaciones de
comando exitosas publican los mismos nombres exportados en el `data` del resultado de la
comprobación; un comando `version` también deriva `version_short` de su stdout,
prefiriendo `major.minor[.patch]` y aceptando salida `version N` solo-entero protegida,
incluyendo lanzamientos codificados por fecha, cuando no hay una versión con puntos
presente. Los guiones y otros caracteres no alfanuméricos se convierten en guiones
bajos. Esto permite que un service reutilice rutas de binarios propiedad de una o más
apps sin colisiones de nombres. Cuando se enlaza exactamente una app, sus variables
también se exponen sin el prefijo como valores por defecto, de modo que un service puede
usar `${binary}` mientras la app sigue siendo la propietaria de la ruta del binario. Una
entrada `variables:` local con el mismo nombre prefijado o sin prefijo sigue
prevaleciendo para sustituciones específicas del host. Cuando se enlazan varias apps,
usa los nombres prefijados para evitar ambigüedad.

`paths.state` (por defecto `/var/lib/sermo`) es la raíz de la base de datos de estado
persistente `sermo.db` (SQLite). A diferencia de `paths.runtime`, sobrevive a los
reinicios, que es lo que permite que el flag `monitor: previous` de un service o watch
restaure su último estado de monitorización. También almacena el cooldown/backoff de
remediación automática y el progreso de las ventanas `for`/`within` de las reglas, de
modo que reiniciar `sermod` no restablece cuándo una regla puede actuar de nuevo. Las
mediciones de SLA y de comprobaciones, además del historial de métricas de proceso de
service y daemon mostrado en la interfaz web, también viven ahí. El esquema está
versionado y se migra automáticamente hacia adelante, de modo que las funciones futuras
pueden añadir tablas sin una actualización manual.

Ambos directorios se crean **0700, propietario root**. En systemd provienen del
`tmpfiles.d/sermo.conf` distribuido (instalado en `/usr/lib/tmpfiles.d/sermo.conf`),
aplicado al arrancar por `systemd-tmpfiles-setup` o inmediatamente con
`systemd-tmpfiles --create sermo.conf` en lugar de provenir del
`RuntimeDirectory=`/`StateDirectory=` de la unidad `sermod.service`. En OpenRC el
`checkpath` del script de init los crea en 0700. El daemon también crea cualquiera de
ellos en 0700 si tiene que hacerlo, de modo que el modo se mantiene incluso fuera del
empaquetado.

`paths.templates` (por defecto `/etc/sermo/templates`) es el directorio para las
plantillas de notificación. `make install` lo crea e instala
`default-alert.yml`.

## Unidades de montaje

Un documento de mount define un destino de montaje con nombre controlado por
`sermoctl mount` y `sermoctl umount`. Las unidades de montaje residen bajo
`paths.mounts` (por defecto `/etc/sermo/mounts`) y usan deliberadamente `/etc/fstab`
como fuente de verdad: el YAML contiene la ruta de montaje y solo la política de Sermo,
no `source`, `fstype`, `options` ni metadatos de clase.

```yaml
name: mount-backup
display_name: Backup mount
category: storage

path: /mnt/backup
refcount: true

umount:
  term_timeout: 12s
  kill_timeout: 5s
  allow_sigkill: false
  allow_lazy: false
```

La CLI acepta o bien el nombre configurado o la ruta de montaje absoluta:

```sh
sermoctl mount mount-backup
sermoctl mount /mnt/backup
sermoctl umount mount-backup
sermoctl umount /mnt/backup
sermoctl mount status mount-backup
sermoctl mount list
```

El panel **Mount units** de la interfaz web expone los nombres de mount
configurados a los administradores. Puede montar/desmontar, mostrar los mismos
procesos bloqueadores antes de desmontar, enviar una alerta TTY nativa a los
usuarios con sesión que estén bloqueando el montaje, y ejecutar `kill+umount`
solo mediante la política explícita de kill de montaje descrita abajo.

Con `refcount: true` (el valor por defecto), cada `mount` exitoso incrementa el
contador de runtime de Sermo y `umount` lo decrementa. El `umount` real solo se ejecuta
cuando el contador llega a cero; si la ruta aún no está montada, el primer `mount`
ejecuta `mount <path>` y requiere una entrada `/etc/fstab` coincidente. El contador se
mantiene bajo `<paths.runtime>/mounts/state`, y cada operación de montaje usa un lock
por destino bajo `<paths.runtime>/mounts/ops`.

El desmontaje normal es conservador: Sermo primero ejecuta `umount <path>`. Si el
montaje está ocupado, reporta los procesos que usan la ruta. Solo envía señales a los
bloqueadores cuando `umount.allow_sigkill: true` o `stop_policy.force_kill: true` está
explícitamente establecido, y la validación entonces requiere un selector restrictivo
`stop_policy.kill_only_if`. El desmontaje perezoso (`umount -l`) también está
desactivado por defecto y solo se usa cuando `umount.allow_lazy: true`.

## Ajustes del motor

El bloque `engine` es configuración de ámbito de daemon consumida por `sermod`; nunca
se fusiona con un service:

```yaml
engine:
  backend: auto               # auto | systemd | openrc
  interval: 30s               # default cycle interval; per-service overridable
  max_parallel_checks: 8        # bound on concurrent checks across all services
  max_parallel_operations: 2  # bound on concurrent start/stop/restart/reload/resume operations
  default_timeout: 10s        # default per-check timeout
  operation_timeout: 90s        # outer deadline for safe service actions
  app_interval: 5m            # cadence for inspecting installed apps for errors
  startup_delay: 0            # grace period before the first cycle (0 disables)
  user_lookup: auto           # auto | native | getent | numeric
  user_lookup_timeout: 250ms  # per-getent lookup timeout; cached in-process
  state_cache_size: 64M       # SQLite page cache for the state database
  # Optional append-only JSONL export logs (opt-in: omit a key to disable it).
  # access: /var/log/sermo/access.log
  # events: /var/log/sermo/event.log
  # diagnostics: /var/log/sermo/diagnostics.log
  # diagnostics_interval: 1h  # scheduled diagnostics when diagnostics is set
```

Los opcionales `engine.access`, `engine.events` y `engine.diagnostics` habilitan la
exportación append-only en JSON Lines bajo rutas absolutas. Cada ruta debe ser absoluta
cuando se establece; los directorios padre se crean según sea necesario (directorios
`0750`, archivos `0640`). Omite una clave para dejar ese canal desactivado.

- `engine.events` refleja cada evento del daemon que la interfaz web y `sermoctl
  activity` ya registran (acciones, alertas, hooks, supresiones, …) además del
  almacén SQLite.
- `engine.access` registra el tráfico mutador del operador: acciones POST a través de la
  API web y comandos `sermoctl` que cambian el estado (`monitor`, `start`, `lock`, …).
  El polling GET rutinario no se registra.
- `engine.diagnostics` ejecuta diagnósticos programados de configuración/host en segundo
  plano (intervalo por defecto `1h`, sustituible con `engine.diagnostics_interval`)
  y añade cada snapshot como una línea JSON al archivo. Rota y conserva el archivo con
  las herramientas de logs de tu host (por ejemplo logrotate); Sermo no lo poda.

`engine.interval` es la cadencia por defecto a la que se ejecutan las comprobaciones de
cada service. Cada service ejecuta todas sus comprobaciones una vez por ciclo.

`engine.app_interval` (por defecto `5m`) es la cadencia a la que el daemon inspecciona
las aplicaciones instaladas (las apps del catálogo mostradas en la interfaz web) en
busca de errores. Cuando la sonda de versión/salud de una app empieza a fallar, el
daemon emite un evento con el detalle del error y notifica una vez (en el flanco de
subida) al valor por defecto global `notify:`, y emite un evento `recovered` cuando
vuelve a pasar — el mismo comportamiento disparado por flancos que los host watches. Las
apps cambian raramente y cada inspección ejecuta el binario de la app, por lo que el
valor por defecto es lento; la interfaz web muestra los eventos recientes de cada app
cuando expandes su fila.

`engine.backend: auto` detecta el sistema de init: sondea systemd (`systemctl` existe,
`/run/systemd/system` existe, `is-system-running` es utilizable — `degraded` cuenta
como utilizable) y OpenRC (`rc-service` existe, `/run/openrc` existe o `rc-status`
funciona). Con exactamente uno disponible se usa ese; con ambos, **gana el sistema de
init activo** (PID 1 / estado de systemd, o de lo contrario un OpenRC en
funcionamiento) — nunca la mera presencia del comando; con ninguno, o un empate
irresoluble, el arranque falla pidiendo `--backend`, `SERMO_BACKEND` o `engine.backend`.
Ese es también el orden de sustitución: flag de CLI > entorno > config >
autodetección.
Para los services oneshot de OpenRC cuyo comando `status` no puede reportar
limpiamente, Sermo recurre a `rc-status -a` y confía en el estado del init.

`engine.max_parallel_operations` limita cuántas acciones seguras de service
(`start`, `stop`, `restart`, `reload`, `resume`) pueden ejecutarse al mismo tiempo a
través de la remediación automática, la interfaz web y `sermoctl`. Es independiente de
`max_parallel_checks`: muchas comprobaciones pueden ejecutarse mientras solo unas pocas
operaciones de service avanzan. Los slots se comparten entre procesos bajo
`<paths.runtime>/op-slots` (por defecto `/run/sermo/op-slots`); cuando todos los slots
están ocupados, otra acción espera hasta que uno quede libre. El valor por defecto es
`2`.

`engine.operation_timeout` es el plazo externo para un start/stop/restart/reload/resume
seguro. El motor puede aumentarlo por service cuando el `stop_policy` resuelto necesita
más tiempo (parada elegante más escalado de señales). El mismo límite se aplica a la
remediación automática, las acciones de `sermoctl` y las operaciones iniciadas desde la
web. Cuando la interfaz web está habilitada, `sermod` también establece el timeout de
escritura del servidor HTTP a partir del plazo resuelto más largo, de modo que una
operación larga no se corte a mitad de la petición. El valor por defecto es `90s`.

`engine.startup_delay` es una duración no negativa que retiene el daemon antes de
iniciar su primer ciclo de comprobación, dando al host tiempo para terminar de arrancar
de modo que los services que aún están subiendo no se marquen ni remedien
prematuramente. La espera se aplica una vez, al arrancar, antes de que ningún worker se
ejecute; una señal de apagado durante la espera aborta limpiamente sin iniciar ningún
worker. El valor por defecto `0` lo desactiva.

`engine.user_lookup` controla cómo Sermo convierte los nombres de usuario/grupo en
valores UID/GID para la identidad de proceso en runtime:

- `auto` (por defecto): si el binario se compiló con CGO habilitado, el `os/user` de Go
  usa libc/NSS, de modo que los usuarios respaldados por LDAP/SSSD/NIS se resuelven a
  través de la pila de identidad normal del host. Si el binario se compiló estático con
  `CGO_ENABLED=0`, Sermo primero usa el lector nativo de passwd/group y luego recurre a
  `getent passwd` / `getent group` de modo que el binario estático pueda seguir
  consultando la configuración NSS del host.
- `native`: usa solo el `os/user` de Go. Con CGO deshabilitado esto normalmente
  significa `/etc/passwd` y `/etc/group` locales.
- `getent`: prefiere `getent passwd|group`, luego recurre al lookup nativo.
- `numeric`: deshabilita el lookup por nombre. Los selectores numéricos UID/GID siguen
  funcionando; los selectores con nombre fallan de forma cerrada y las columnas de
  propietario muestran IDs numéricos cuando no hay un nombre disponible.

`engine.user_lookup_timeout` limita cada llamada `getent`; los resultados, incluyendo
los fallos, se cachean en el proceso en ejecución de modo que la monitorización normal
no genere un comando por cada proceso en cada ciclo. Si un nombre no puede resolverse,
Sermo no adivina: los selectores de proceso y `kill_only_if.users` que usan ese nombre
no coinciden. Para políticas de parada críticas, los UIDs/GIDs numéricos son la forma
más determinista.

`engine.state_cache_size` (por defecto `64M`) establece la caché de páginas SQLite para
la base de datos de estado (`paths.state`). La BD de estado acumula historial de SLA,
mediciones y métricas por minuto, cuyos índices crecen hasta decenas de MB; la caché
mantiene esas páginas calientes en memoria de modo que una ráfaga de escrituras por
ciclo no las relea desde el disco y atasque un `monitor`/`unmonitor` interactivo (cada
sentencia comparte una conexión). Súbela en hosts con un historial grande y RAM de
sobra (el valor es un tamaño en bytes con sufijo `K`/`M`/`G`); se toma de la
configuración del daemon en ejecución y se aplica la próxima vez que `sermod` abra la
base de datos (un reinicio, ya que el handle se mantiene abierto durante toda la vida
del daemon).

Cuando `sermoctl daemon reload` pide al daemon en ejecución que recargue, `sermod` lee
la configuración desde la ruta pasada a `sermod run --config` (el mismo archivo que usa
`sermoctl`). `sermod` valida la nueva config, reconstruye sus workers de service y los
host watches, y los intercambia sin reiniciar el proceso. El estado de runtime por
service se preserva a través de la recarga:
los contadores de ciclo de monitorización y las líneas base de archivos vigilados para
las condiciones `changed:` permanecen en memoria, mientras que el cooldown/backoff de
remediación y las ventanas `for`/`within` de reglas también se persisten en
`paths.state` y sobreviven a un reinicio completo del proceso `sermod`. Una config
inválida, o una config sin services ni watches incluidos, se rechaza y la generación
actual sigue ejecutándose; se registra un evento `reload` o `error`. La recarga no
repite `startup_delay` ni marca `/readyz` como apagándose.
Las líneas base de tasa de CPU por service solo se restablecen cuando un service se
elimina de la config en ejecución; el historial de métricas y eventos persistido
permanece en `paths.state` hasta la retención normal o un `sermoctl state compact`
explícito.

Dispara una recarga de configuración del daemon con:

```sh
sermoctl daemon reload
```

Solo una instancia de `sermod` puede ejecutarse por directorio `<paths.runtime>` (por
defecto `/run/sermo`). Al arrancar toma un lock exclusivo sobre
`<paths.runtime>/sermod.lock`; si otra instancia ya lo tiene, el nuevo proceso registra
una advertencia, sale con estado **1** y no inicia un segundo bucle de monitor.

El daemon escribe `<paths.runtime>/sermod.pid` (por defecto `/run/sermo/sermod.pid`)
al arrancar para hacer fiable `sermoctl daemon reload`. Si no hay pidfile presente,
`sermoctl daemon reload` recurre a localizar el proceso `sermod` en ejecución por
nombre — un escaneo nativo de `/proc`, sin necesidad de `pidof`/`pgrep` externos.

`sermoctl daemon reload` recarga la propia configuración de `sermod` (como se indica
arriba). `sermoctl reload <service>` es una operación diferente — recarga *ese service*
en su sitio a través del motor (preflight → reload → health). Cómo recarga un service,
incluyendo el bloque `reload:` que permite a Sermo enviar una señal a un service cuando
su unidad de init no tiene recarga, está documentado en
[services.md](services.es.md#reload-on-config-change-reload_on_change).

### Intervalo por service

`engine.interval` establece el valor por defecto para cada service. Un service puede
sustituirlo con su propio `interval` de nivel superior, de modo que los services baratos
pueden comprobarse a menudo y los caros raramente sin cambiar el valor por defecto
global:

```yaml
name: nginx
interval: 10s            # optional, default engine.interval; positive duration
checks:
  http: { type: http, url: "http://127.0.0.1/health", expect_status: 200 }
```

La sustitución rige todo el ciclo del worker para ese service (sus comprobaciones,
reglas y remediación), exactamente como el intervalo global — solo difiere su cadencia.
Por tanto, los recuentos de ventana (`for`/`within` con `cycles`) se cuentan en los
propios ciclos de ese service; las ventanas de duración usan el tiempo de reloj
transcurrido entre esos ciclos observados. Los arranques de workers aún se reparten a lo
largo de un intervalo global, de modo que una flota de services no sondee toda en el
mismo tick.

### Intervalo por comprobación

Una comprobación individual puede ejecutarse **con menos frecuencia** que el ciclo del
worker con `interval`. El worker sigue tickeando a su resolución; la comprobación se
ejecuta cada `round(interval / resolution)` ciclos y **reutiliza su último resultado**
entre ejecuciones, manteniendo completas las cachés de comprobación y las ventanas de
reglas.

```yaml
interval: 30s            # the service resolution (or engine.interval)
checks:
  http:
    type: http
    url: "http://127.0.0.1/health"   # runs every cycle (30s)
  version:
    type: command
    command: ["/usr/bin/nginx", "-v"]
    interval: 30m                     # runs every 60 cycles (30m / 30s)
```

Un `interval` por comprobación **no puede ser más corto que la resolución** y debería
ser un **múltiplo** de ella. Si no lo es, el daemon lo redondea al múltiplo más cercano
(al menos un ciclo) y **registra una advertencia al arrancar** — nunca falla al
arrancar.

## Interfaz web

El daemon puede servir un pequeño panel web para ver services y host watches. Los
administradores pueden monitorizar/desmonitorizar ambos, y pueden
iniciar/detener/reiniciar/recargar/reanudar services sobre el mismo motor de
operaciones seguras que usa la CLI.

Un service normalmente se resuelve a una unidad de systemd/OpenRC. En su lugar puede
declarar un destino `control:` por service para recursos que no son de init:
`control.type: libvirt` para VMs de QEMU/libvirt o `control.type: docker` para
contenedores Docker. Esos destinos siguen usando los mismos locks, guards,
comprobaciones de preflight y timeouts de operación; consulta
[services](services.es.md#control-docker--docker-containers).

Debajo de la tabla de services, el panel lista las **aplicaciones instaladas** (los
daemons de app del catálogo cuyo binario está presente), mostrando el nombre y la
versión corta de cada aplicación; un comando `health` de la app, cuando está
configurado, decide OK/error a partir de su código de salida antes de considerar el
comando de versión. Si no hay ningún comando `health` configurado, el comando `version`
es la sonda alternativa mientras se obtiene la versión mostrada. La lista es ordenable
por nombre, categoría o versión, y al expandir una fila se revela la cadena de versión
completa, la ubicación del archivo del binario y sus permisos. Cuando una versión se
hereda a través de `version_from`, la fila de la API incluye `version_source` con el
nombre de la app proveedora. Los services y aplicaciones pueden filtrarse y agruparse
por su campo de metadatos `category` de nivel superior.
Los mismos datos están disponibles desde `sermoctl apps` y `GET /api/applications`.
El panel cachea la lista hasta 30 segundos, de modo que las autoactualizaciones no
reejecutan cada sonda de versión de app.
Para un mapa editable panel por panel, consulta
[webui-representation.md](webui-representation.es.md).

**La interfaz web solo se activa cuando `web.port` está explícitamente definido.** Si se
omite el bloque `web:`, o si hay un bloque `web:` presente sin una clave `port` (aunque
otras claves como `address` estén establecidas), el servidor HTTP no se inicia. Al
arrancar `sermod` registra una advertencia: "web ui disabled; no port will be opened".

```yaml
web:
  address: 127.0.0.1        # optional, default 127.0.0.1 (loopback only)
  port: 9797                # REQUIRED to activate the web UI (9797 recommended)
```

- **Regla de activación:** la interfaz web ("servicio web") **no se inicia** a menos que
  `web.port` esté presente y sea válido. Omitir la clave (o todo el bloque `web:`)
  deja el panel deshabilitado; `sermod` registra el motivo exacto al arrancar.
- **Puerto recomendado: `9797`.** Es fácil de recordar y evita los puertos comunes de
  monitorización (`9090` Prometheus, `9093` Alertmanager, `9100` node-exporter,
  `3000` Grafana, `8080`).
- **La autenticación** es opcional pero recomendada antes de exponerlo. Sin ella, la
  interfaz se enlaza a **loopback (`127.0.0.1`) por defecto** y está completamente
  abierta.

### Autenticación

Establece contraseñas en el bloque `web` para autenticación HTTP Basic con dos roles:

```yaml
web:
  port: 9797
  password: "s3cret"           # admin: read + actions (start/stop/restart/reload/resume, monitor/unmonitor)
  guest_password: "lookonly"   # optional: a read-only login
  guest: true                  # optional: allow anonymous read-only access
```

- **admin** — acceso completo. Otorgado por `password`.
- **guest** — **solo lectura**: puede ver todo pero cada acción (un `POST`) se rechaza
  con `403`. Otorgado por `guest_password`, y/o a cualquiera cuando `guest: true`
  (solo lectura anónima).

La **contraseña**, no el nombre de usuario, selecciona el rol — en el prompt del
navegador introduce cualquier nombre de usuario y la contraseña de admin o guest; las
contraseñas se comparan en tiempo constante. Con `guest: true` el panel se carga en solo
lectura sin prompt, y un enlace **"log in"** (`GET /login`) dispara el prompt para
escalar a admin. La interfaz oculta los botones de acción a los invitados; la API lo
impone de todos modos. Cuando no se establece ninguna contraseña/guest, la autenticación
está deshabilitada (abierta) y el daemon **registra una advertencia** al arrancar.
`GET /api/whoami` reporta el rol del llamante.

### Detrás de un proxy inverso (requerido para exponerlo)

El servidor web habla **solo HTTP plano** y se enlaza a loopback por defecto. Para
alcanzarlo desde cualquier cosa que no sea el host local, **ponlo detrás de un proxy
inverso** (nginx, Apache, …) que termine **TLS** — **no** amplíes `web.address` a una
interfaz pública. Mantén Sermo en `127.0.0.1` y deja que el proxy sea el único cliente:

```yaml
web:
  address: 127.0.0.1   # leave on loopback
  port: 9797
  password: "${env:SERMO_WEB_PASSWORD}"
```

**nginx** (TLS por delante, proxy a loopback):

```nginx
server {
    listen 443 ssl;
    server_name sermo.example.com;
    ssl_certificate     /etc/ssl/certs/sermo.crt;
    ssl_certificate_key /etc/ssl/private/sermo.key;

    location / {
        proxy_pass         http://127.0.0.1:9797;
        proxy_set_header   Host $host;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_set_header   X-Forwarded-For $remote_addr;
    }
}
```

**Apache** (`mod_ssl` + `mod_proxy` + `mod_proxy_http`):

```apache
<VirtualHost *:443>
    ServerName sermo.example.com
    SSLEngine on
    SSLCertificateFile    /etc/ssl/certs/sermo.crt
    SSLCertificateKeyFile /etc/ssl/private/sermo.key

    ProxyPreserveHost On
    ProxyPass        / http://127.0.0.1:9797/
    ProxyPassReverse / http://127.0.0.1:9797/
</VirtualHost>
```

Notas:

- El proxy y el panel comparten un **origen**, de modo que la cabecera `X-Sermo-CSRF` y
  la propia autenticación admin/guest de Sermo siguen funcionando a través de él — el
  navegador reenvía la cabecera `Authorization`. Puedes confiar en los roles de Sermo,
  añadir la propia autenticación del proxy (basic/OIDC/mTLS) por encima, o ambas.
- Redirige HTTP→HTTPS en el proxy y deja que él maneje los certificados (Sermo no tiene
  TLS nativo). Restringe el acceso ahí también (allow-lists, SSO) si es necesario.
- Nunca publiques el puerto `9797` directamente; solo el proxy debería conectarse a él.

Endpoints de solo lectura:

- `GET /` — el panel.
- `GET /livez` — liveness, ver abajo.
- `GET /readyz` — readiness, ver abajo. El panel sondea `/readyz?verbose` para mostrar
  un banner de **Starting** o **Shutting down** mientras la monitorización aún no está
  activa.
- `GET /api/whoami` — rol del llamante, permisos y visibilidad de funciones.
- `GET /api/services` — lista de services de **runtime configurado** (los archivos de
  service bajo `paths.services`): name, `state` (`disabled`, `running`,
  `paused`, `stopped`, `monitorized`, `failed`), estado del backend, `check_health`,
  `checks_failing`, locks activos, estado/fuente/marca de tiempo de monitor, backend,
  unidad, cooldown, estado de remediación, próxima acción elegible y último evento. Esto
  no es `sermoctl services`, que lista los perfiles de service del catálogo — consulta
  [cli.md](cli.es.md#catalog-inventory).
- `GET /api/services/{name}` — detalle del service: últimas comprobaciones, SLA móvil,
  locks de runtime con nombre, procesos descubiertos, estado de la política de
  remediación automática y progreso de la ventana de reglas.
- `GET /api/services/{name}/sla?since=24h` — historial de disponibilidad por minuto;
  `since` es una duración, por defecto 24h, limitada a la retención de 366 días (~1 año).
- `GET /api/services/{name}/metrics?check=NAME&since=24h` — historial de latencia de la
  comprobación + resumen. Añade `metric=KEY` para una métrica numérica con nombre
  publicada por esa comprobación, ver abajo.
- `GET /api/services/{name}/runtime?since=24h` — historial de CPU, memoria e IO del
  árbol de procesos del service.
- `GET /api/services/{name}/events?limit=N` — eventos de un service.
- `GET /api/watches` — host watches, estado de monitor, condiciones, notificaciones,
  lecturas en vivo cuando están disponibles y actividad reciente.
- `GET /api/notifiers` — destinos de notifier configurados.
- `GET /api/applications` — aplicaciones del catálogo instaladas.
- `GET /api/daemon` — ajustes de daemon/backend/runtime y uptime del host.
- `GET /api/daemon/metrics?since=24h` — historial persistente de CPU, memoria e IO de
  sermod para el proceso de daemon actual, más PID actual, descriptores de archivo e
  hilos.
- `GET /api/host` — métricas actuales de CPU, memoria y carga a nivel de host.
- `GET /api/locks` — locks de runtime con nombre con TTL, estado del propietario, edad,
  acciones bloqueadas y elegibilidad de liberación.
- `GET /api/activity` — resumen de actividad reciente usado por la cabecera del panel.
- `GET /api/monitoring` — recuentos de services monitorizados frente a pausados.
- `GET /api/events?limit=N` — feed global de eventos, los más nuevos primero. Filtros
  opcionales: `service`, `watch`, `kind`, `status` y `only_errors=1`.
- `GET /api/ops` — uso global de slots de operación: `{in_use, total}` para
  `engine.max_parallel_operations`.

Los endpoints que cambian el estado están protegidos contra CSRF y requieren permisos de
admin cuando la autenticación está habilitada:

- `POST /api/services/{name}/preflight` — ejecuta las mismas comprobaciones de preflight
  que `sermoctl preflight SERVICE`, sin iniciar ni detener nada.
- `POST /api/services/{name}/{action}` — acción de service. `action` es `monitor`,
  `unmonitor`, `start`, `stop`, `restart`, `reload` o `resume`;
  start/stop/restart/reload/resume pasan por el motor de operaciones seguras.
- `POST /api/watches/{name}/{action}` — acción de host watch. `action` es
  `monitor`, `unmonitor` o `expand`.
- `POST /api/locks/{service}/release?name=NAME` — libera un lock de runtime con nombre
  inactivo obsoleto/expirado; los locks activos se rechazan.
- `POST /api/events/clear?before=TIME` — limpia el log persistido de eventos/actividad;
  `before` puede ser RFC3339 o una duración. Omítelo para limpiar todos los eventos.
- `POST /api/state/compact?before=TIME` — poda el historial antiguo de SLA, mediciones,
  métricas de daemon, métricas de runtime de service y eventos, luego compacta la base
  de datos de estado; coincide con `sermoctl state compact`.
- `POST /api/reload` — solicita una recarga de configuración de `sermod`, equivalente a
  `sermoctl daemon reload`.

### Liveness (`/livez`)

`GET /livez` es una sonda de liveness para el daemon: si su servidor web responde, el
proceso está vivo, por lo que siempre devuelve **200**. Una petición plana devuelve un
cuerpo `text/plain` `ok`; `GET /livez?verbose` devuelve JSON con `status`, `uptime`
(y `uptime_seconds`), `started_at`, `now`, el número de `services` y la versión del
runtime de Go. A diferencia de cualquier otro endpoint se sirve **sin autenticación**
(y está exento de CSRF), de modo que un monitor, balanceador de carga, orquestador de
contenedores o el proxy inverso puede sondearlo sin credenciales:

```sh
curl -fsS http://127.0.0.1:9797/livez            # -> ok
curl -fsS http://127.0.0.1:9797/livez?verbose    # -> {"status":"ok","uptime":"3h12m0s",...}
```

Reporta solo la liveness del proceso; para la salud de configuración/host/base de datos
usa [diagnósticos](#diagnostics).

### Readiness (`/readyz`)

`GET /readyz` es una sonda de readiness: devuelve **200** solo después de que `sermod`
haya terminado `engine.startup_delay` (si lo hay) **y cada destino monitorizado —
services, host watches y apps instaladas — haya completado su primer ciclo**, de modo
que el daemon realmente tiene datos en lugar de simplemente haberse lanzado. Mientras se
asienta, el `message` verbose reporta el progreso (`starting: 3/10 monitored targets
have reported`) y la cabecera de la interfaz web muestra `status: starting` con un
favicon de pestaña gris neutral. Cada service monitorizado, host watch y app instalada
también reporta `state: starting` hasta que su primer ciclo de observación se haya
completado. Los services que aún esperan un backend de init `active` completan el
asentamiento en la primera sonda de estado (afloran como `failed` mientras están
inactivos); las comprobaciones y la remediación esperan hasta que el backend está
activo.
Solo las aplicaciones del catálogo **instaladas** con un app-monitor activo participan en
ese registro de asentamiento; las entradas del catálogo cuyo binario no está presente se
omiten de `GET /api/applications` y nunca muestran `starting`. Durante el asentamiento,
las apps instaladas pueden aparecer con `state: starting` antes de que su primer ciclo
de app-watch se complete;
durante esa ventana Sermo no ejecuta comprobaciones de service (mientras el backend aún
está inactivo), y suprime alertas, hooks, notificaciones y remediación automática en el
primer ciclo de observación activa. Durante
el periodo de gracia de arranque, el asentamiento del primer ciclo, o mientras el daemon
se está apagando, devuelve **503**. Para evitar una estampida de arranque, el primer
ciclo de toda la flota se escalona a lo largo de un `engine.interval` (la cadencia lenta
por app se usa solo después de esa primera comprobación); una **recarga de config no
vuelve a bloquear** `/readyz` — el daemon permanece `ready` y la cabecera/favicon web no
vuelven al estado `starting` gris. Los destinos monitorizados recién añadidos o
cambiados aún pueden reportar `state: starting` individualmente hasta que su primer ciclo
de observación se complete. Una
petición plana devuelve `ok` o `starting` / `shutting_down` como `text/plain`;
`GET /readyz?verbose` devuelve JSON con `ready`, `status`, `backend`, `services`,
`watches` (host watches más monitores de app instaladas) y un `message` opcional. Como
`/livez`, se sirve **sin autenticación**:

```sh
curl -fsS http://127.0.0.1:9797/readyz                 # -> ok (when monitoring)
curl -fsS http://127.0.0.1:9797/readyz?verbose         # -> {"ready":true,"status":"ok",...}
```

Usa `/livez` para saber que el proceso está vivo; usa `/readyz` antes de enviar tráfico
o para bloquear un proxy inverso hasta que la monitorización haya comenzado realmente.

Un **service monitorizado cuyo backend de init permanece inactivo** (por ejemplo una
unidad que mantienes detenida intencionadamente) completa la observación de arranque en
la primera sonda de estado: reporta `state: failed` y ya no bloquea `/readyz`. Sermo aún
aplaza las comprobaciones de service y la remediación automática hasta que esa unidad se
vuelve activa. Los workers de service, host watches y monitores de app instaladas usan
claves de asentamiento separadas, de modo que un service y una app del catálogo que
comparten un nombre (por ejemplo `redis`) cuentan ambos hacia la readiness de forma
independiente.

Los eventos son la actividad del daemon — acciones, alertas, supresiones, resultados de
hook/notify y errores — mantenidos en un anillo en memoria (los últimos 1000); también
van al log del daemon. `limit` por defecto es 100 (máx 1000). El panel muestra un feed
global; el detalle de un service muestra sus propios eventos.

Los resultados de comprobación del detalle son los **últimos observados** por el worker
(publicados cada ciclo), por lo que no cuesta nada verlos y reflejan la cadencia propia
de cada comprobación (ver [intervalo por comprobación](#per-check-interval)); una
comprobación aún no ejecutada muestra "not run yet". La sección de gráficos usa un
selector de ventana para las mediciones de SLA y runtime. Su línea de tiempo de SLA
proviene de los mismos datos que `sermoctl sla`: traza las muestras por minuto sobre la
ventana seleccionada (1h/24h/7d/30d/1y), marca cada minuto degradado como un incidente a
su hora local, y deja huecos donde el service estuvo sin monitorizar.

### Gráfico de latencia

Para cada comprobación `tcp`, `ports`, `http` y `service`, el daemon registra la
**latencia** de la comprobación (milisegundos) en cada ciclo observado — la misma idea
que la métrica de latencia `icmp` — y el detalle del service dibuja un **gráfico de
latencia** para la comprobación seleccionada. Un selector de ventana cubre la **última
hora, día, semana, mes y año**, y para el periodo elegido el panel muestra el
**promedio, mínimo y máximo** más una línea (promedio a lo largo del tiempo) con una
banda mín–máx. Los datos están en
`GET /api/services/{name}/metrics?check=NAME&since=DURATION` como `{summary:{count,
avg,min,max}, points:[{start,n,avg,min,max}], unit:"ms"}`. Añade `metric=KEY` para leer
una métrica numérica con nombre para comprobaciones que publican una, como `hdparm`
`read`/`cached`, `sensors` `temp`/`fan`, `smart` `temperature`/`wear` o `edac`
`ce`/`ue`; en ese caso `unit` es la unidad de la métrica en lugar de `ms`.
Las mediciones se mantienen por minuto durante aproximadamente un año (podadas como las
muestras de SLA); una comprobación que solo se ejecuta cada N ciclos ([intervalo por
comprobación](#per-check-interval)) registra una muestra solo cuando realmente se
ejecuta, de modo que el promedio no se sesga.

Los gráficos de proceso de `Daemon / Engine settings` usan la misma base de datos de
estado persistente para el propio historial de CPU, memoria e IO de sermod, de modo que
esos gráficos sobreviven a un reinicio del daemon o del host. Se podan a la misma
ventana de retención de 366 días (~1 año).

Los gráficos de CPU, memoria e IO del detalle del service usan la misma base de datos de
estado persistente para cada árbol de procesos de service, de modo que esos gráficos
también sobreviven a un reinicio del daemon o del host. Empiezan a llenarse en cuanto el
service se monitoriza; las tasas de CPU e IO necesitan dos ciclos antes de que exista el
primer punto de tasa, mientras que la memoria puede renderizarse desde la primera
muestra de proceso. Los buckets de métricas de runtime se podan a la misma ventana de
retención de 366 días (~1 año).

Los cambios de monitor disparados desde la web se registran con la fuente `web` en el
almacén de estado (`cli`, `config` y `daemon` son los otros valores). El panel y
`GET /api/services` / `GET /api/watches` exponen `state`, `monitor_source` y
`monitor_changed_at` de modo que un service running/paused/stopped no monitorizado o un
watch desmonitorizado muestre quién lo pausó y cuándo. Los host watches no tienen los
estados `running` o `stopped` del gestor de servicios; el panel los filtra como
`ok`, `failed`, `unmonitorized` o `disabled`.
Las operaciones toman el lock de operación por service, de modo que nunca se solapan con
la acción de un worker sobre el mismo service.

Como el daemon se ejecuta como root, la interfaz está endurecida: se enlaza a loopback
por defecto, soporta autenticación (arriba), establece timeouts HTTP y requiere una
cabecera **`X-Sermo-CSRF`** en cada petición de acción (POST) — el panel la envía; un
cliente de API también debe hacerlo (p. ej. `curl -H 'X-Sermo-CSRF: 1' -X POST …`). Esto
bloquea la falsificación de peticiones entre sitios desde un navegador. Consulta
[safety](safety.es.md#trust-model).

## Disponibilidad (SLA)

El daemon registra una muestra de disponibilidad por ciclo de monitorización por
service, de modo que puedas ver con qué frecuencia cada service ha estado sano a lo largo
del tiempo. No se necesita configuración — está activo para cada service monitorizado.

Un service está **disponible** en un ciclo cuando ninguna de sus comprobaciones
**requeridas** falló. Las comprobaciones opcionales (advertencias) no le afectan, y un
service sin comprobaciones requeridas siempre está disponible. Las comprobaciones de
estilo salud (`tcp`, `http`, `service`, `process`, `cert`, `firewall_rules`, etc.)
fallan cuando `OK=false`; las comprobaciones de estilo condición (`fds`, `oom`, umbrales
de recursos, etc.) fallan solo cuando se dispara la condición de alerta. Las muestras se
acumulan en buckets por minuto en la BD de estado (`/var/lib/sermo/sermo.db`); el daemon
poda los buckets de más de un año al arrancar.

Solo cuentan los ciclos **observados**, por lo que estos periodos se **excluyen** del
SLA en lugar de contarse como downtime:

- **El propio Sermo está detenido** — no se ejecutan ciclos, por lo que esos minutos no
  tienen muestras.
- **El service está pausado** (`unmonitor`, o `monitor: disabled`) — el ciclo retorna
  antes de cualquier comprobación, sin registrar nada.
- **El service está deshabilitado** (`enabled: false`) — no se construye ningún worker
  para él.
- **Una comprobación está deshabilitada/eliminada** — está ausente del ciclo, por lo que
  ni pasa ni falla; la disponibilidad refleja solo las comprobaciones que realmente se
  ejecutaron.

Así, las ventanas de mantenimiento y los cortes del propio Sermo nunca deprimen el SLA de
un service.

Reporta la disponibilidad sobre ventanas móviles (la última hora, día, semana, mes y
año) con `sermoctl sla`:

```sh
sermoctl sla                 # every configured service
sermoctl sla apache-main     # one service
sermoctl --json sla          # machine-readable: up/total/ratio per window
```

Una ventana sin muestras se lee como `n/a` (disponibilidad desconocida), no `0%`.

### Series temporales de disponibilidad

Las muestras se mantienen como buckets por minuto, que es también la **serie temporal**
en bruto a partir de la cual se construye un gráfico. Exporta la serie de un service (los
más antiguos primero) con `--series`:

```sh
sermoctl sla --series apache-main                  # last 24h (default)
sermoctl sla --series apache-main --since 168h     # last 7 days
sermoctl --json sla --series apache-main           # points: start, up, total, ratio
```

Cada punto es un minuto monitorizado; **los minutos no monitorizados están ausentes**
(huecos), de modo que un gráfico puede renderizar un periodo excluido (Sermo caído, o el
service pausado/deshabilitado) de forma distinta al downtime real. El panel web usa los
mismos puntos para colocar marcadores de incidente en el minuto en que se observó el
problema.

## Notificaciones

Los `notifiers` son destinos de entrega con nombre y tipados a los que un watch puede
enviar cuando se dispara, como alternativa o complemento a ejecutar un hook local. Son
configuración global del daemon; nunca se fusionan con un service. Cada notifier tiene un
**name** (la clave del mapa) referenciado desde la lista `then.notify` de un watch, de
modo que distintos watches puedan notificar a distintos destinos.

Los fragmentos de notifier residen bajo directorios listados en `paths.notifiers`
(comúnmente `/etc/sermo/notifiers`). Cada archivo contiene exactamente un notifier bajo
el mapa de nivel superior `notifiers:`:

```yaml
# /etc/sermo/notifiers/ops-email.yml
notifiers:
  ops-email:                 # the name referenced by then.notify
    enabled: true             # optional; defaults to true
    type: email
    template: default-alert    # optional; loads /etc/sermo/templates/default-alert.yml
    dsn: "smtp://user:pass@smtp.example.com:587"   # smtp:// (STARTTLS) or smtps:// (implicit TLS)
    from: "Sermo <sermo@example.com>"
    to: [ops@example.com, oncall@example.com]       # one or more recipients
```

Tipos de notifier:

- **`email`** — envía por SMTP.
  - **`dsn`** — `smtp://[user:pass@]host[:port]` (STARTTLS cuando se ofrece; puerto por
    defecto 587) o `smtps://…` (TLS implícito; puerto por defecto 465). Las credenciales,
    cuando están presentes, solo se envían sobre una conexión cifrada.
  - **`from`** — la dirección del remitente (un `addr` desnudo o `Name <addr>`).
  - **`to`** — una o más direcciones de destinatario.
- **`slack`** — publica en un **incoming webhook** de Slack.
  - **`webhook`** — la URL del incoming-webhook (`https://hooks.slack.com/services/…`).
    El asunto de la notificación es la línea principal y su detalle (los campos
    `SERMO_*`) sigue en un bloque de código.

```yaml
notifiers:
  team-slack:
    type: slack
    webhook: "https://hooks.slack.com/services/T0000/B0000/XXXXXXXX"
```

- **`teams`** — publica en un **incoming webhook** de Microsoft Teams (una URL de Teams
  Workflows / Power Automate "when a webhook request is received").
  - **`webhook`** — la URL de POST HTTP del workflow. La notificación se envía como una
    Adaptive Card: el asunto como línea principal en negrita, el detalle (los campos
    `SERMO_*`) en un bloque monoespaciado.

```yaml
notifiers:
  ops-teams:
    type: teams
    webhook: "https://prod-01.westeurope.logic.azure.com:443/workflows/…"
```

- **`tty`** — escribe directamente en las sesiones de terminal Linux activas, similar a
  `write(1)` pero implementado dentro de Sermo sin invocar un comando externo. El
  notifier integrado llamado `tty` está siempre disponible y puede sustituirse para
  apuntar a usuarios específicos:

```yaml
notify: [tty]      # optional global default: notify logged-in terminal users
```

  Para personalizarlo o deshabilitarlo, define un notifier normal con el mismo nombre:

```yaml
notifiers:
  tty:
    type: tty
    users: [root, deploy]   # optional; omit to target every active terminal
```

  El notifier `tty` lee `/run/utmp` (recurriendo a `/var/run/utmp`) y escribe en el
  dispositivo `/dev/<tty>` correspondiente con I/O nativa de Go no bloqueante. Respeta
  los permisos de terminal como `mesg n`; si el usuario del daemon no puede escribir en
  un terminal, la entrega a ese terminal falla y Sermo registra un evento
  `notify-failed`.

- **`wall`** — difunde a cada sesión de terminal Linux activa usando la misma
  implementación nativa de Go de utmp/TTY que `tty`, pero sin filtro de usuario. El
  notifier integrado llamado `wall` está siempre disponible:

```yaml
notify: [wall]     # broadcast to every logged-in terminal session
```

  `wall` intencionadamente no tiene opción `users`; usa `tty` cuando necesites apuntar
  solo a usuarios seleccionados.

Los tipos de notifier soportados hoy son `email`, `slack`, `teams`, `tty` y
`wall`.

Establece **`enabled: false`** en cualquier notifier para mantenerlo definido pero
omitir la entrega. Los notifiers deshabilitados aún pueden ser referenciados por las
selecciones `notify`.

`sermoctl services --notify NAME[,NAME]` envía un informe ad-hoc del inventario de
services a través de los notifiers configurados. Los notifiers de email reciben un
mensaje multipart de texto plano/HTML con tarjetas de resumen y una tabla de services;
Slack y Teams reciben el fallback de texto, y los notifiers de terminal escriben el
informe de texto en las sesiones TTY con sesión iniciada. `--notify all` apunta a cada
notifier habilitado, incluyendo los notifiers integrados `tty` y `wall` a menos que hayan
sido explícitamente deshabilitados. Cuando una selección de notify contiene tanto `tty`
como `wall`, Sermo envía solo `wall` porque ya cubre cada terminal activo. La CLI
renderiza este informe directamente; no se usan plantillas de notifier.

`none` es una **palabra clave reservada** y no puede usarse como nombre de notifier.

### Plantillas de notificación

Cualquier notifier puede optar por una plantilla con nombre con `template: <name>`. Los
nombres se mapean a `<paths.templates>/<name>.yml`, de modo que `template: default-alert`
carga `/etc/sermo/templates/default-alert.yml` por defecto. El target de instalación
distribuye esa plantilla como una línea base neutral:

```yaml
subject: "{{ .Subject }}"
body: |
  {{ .Body }}
```

Las plantillas son archivos `text/template` de Go envueltos en YAML con claves opcionales
`subject` y `body`. Si se omite cualquiera de las claves, Sermo mantiene el asunto o
cuerpo generado original para esa parte. Los datos disponibles son:

- **`.Subject`** — el asunto generado por Sermo.
- **`.Body`** — el cuerpo generado por Sermo.
- **`.Field "SERMO_SERVICE"`** — un campo de contexto estructurado por nombre; los
  campos faltantes se renderizan como una cadena vacía.
- **`.SortedFields`** — todos los campos estructurados como entradas estables
  `{Name, Value}`, útil para `range`.

Ejemplo:

```yaml
subject: '[{{ .Field "SERMO_SERVICE" }}] {{ .Subject }}'
body: |
  {{ .Body }}

  Context:
  {{ range .SortedFields }}{{ .Name }}={{ .Value }}
  {{ end }}
```

Los nombres de plantilla pueden contener letras, dígitos, `_`, `-` y `.`, pero no
separadores de ruta ni `..`. Sermo valida los archivos de plantilla referenciados cuando
se carga la configuración; una plantilla faltante o inválida se reporta como un problema
de config, y el notifier afectado es omitido por `sermod`.

### Selección por defecto y precedencia

Una clave **`notify`** de nivel superior establece los notifiers por defecto que se
aplican a cada sitio de notificación (el `then.notify` de un watch y el `notify` de una
regla) — de modo que configuras el enrutamiento una vez en lugar de repetirlo en cada
watch y regla:

```yaml
notify: [ops-email]      # default for every site that declares none of its own
# notify: none           # (or omit the key) for no default
```

Cada sitio entonces **sustituye** el valor por defecto — la elección por sitio siempre
prevalece:

- una lista explícita (`notify: [team-slack]`) reemplaza el valor por defecto para ese
  sitio;
- `notify: none` suprime la entrega para ese sitio — válido **en cualquier lugar donde
  haya una selección de notify**, con o sin un valor por defecto global configurado. Un
  watch cuya única acción es `notify: [none]` (dentro de un `then` explícito) es un watch
  *solo-monitor* deliberado: aún se ejecuta, muestra su estado en el panel y registra
  eventos, simplemente nunca entrega;
- omitir `notify` (dentro de un `then` explícito) hereda el valor por defecto global.

`none` no puede combinarse con nombres de notifier en la misma lista. Omitir toda la
clave `then` en un watch (o por métrica) es otra forma de obtener comportamiento puro de
solo-alerta (estado de disparo + eventos en la interfaz y el log, pero sin acciones y sin
herencia de los globales). Consulta la sección de host watches a continuación para el
ejemplo de `check` + `for` desnudo.

## Host watches

Los `watches` monitorizan recursos a nivel de host independientemente de cualquier
service y ejecutan un **hook** (un comando local) y/o envían **notificaciones** (a
`notifiers` con nombre) cuando se cruza un umbral. Son configuración del daemon; nunca se
fusionan con un service.

> **Consejo — genera la configuración interactivamente.** `sermoctl wizard` puede
> escribir tres superficies diferentes. Los asistentes de watch (`volume`, `net`,
> `uplink`) imprimen una previsualización `watches:` y, si se acepta, escriben un watch
> por archivo bajo un directorio de tipo de watch como `/etc/sermo/storages` o
> `/etc/sermo/networks`; el asistente añade ese directorio al `paths.*` coincidente
> (escribiendo primero un `.bak`). Los asistentes de service (`service`, `docker`, `vm`)
> escriben un archivo de service por destino bajo `services/` y aseguran que
> `paths.services` lo cargue; `docker` y `vm` añaden `control.type: docker` o
> `control.type: libvirt` más comprobaciones de solo lectura coincidentes. El asistente
> de mount (`mount`) lista los puntos de montaje de `/etc/fstab` y escribe archivos de
> montaje seguros bajo `paths.mounts`; no monta ni desmonta mientras genera la config.
>
> `sermoctl wizard volume` crea comprobaciones de almacenamiento para sistemas de
> archivos locales y de red/distribuidos montados (umbral como porcentaje o tamaño,
> auto-expansión opcional para sistemas de archivos respaldados por LVM). `sermoctl
> wizard net` cubre el estado de la interfaz, errores, velocidad y dirección; escribe
> `active` para elegir las interfaces no-loopback actualmente activas. `sermoctl wizard
> uplink` genera el conjunto de uplink de internet por capas para una interfaz: estado
> del enlace, dirección asignada, ruta por defecto, ping enlazado y resolución DNS a
> través del resolver del sistema; escribe `default` para usar la interfaz de ruta por
> defecto detectada.
> `sermoctl wizard service` detecta los services del catálogo instalados y los habilita
> con archivos de service (ver [services](services.es.md)); cuando se seleccionan varios
> services, las sustituciones de puerto se omiten a menos que se revisen explícitamente,
> y los archivos de config conocidos pueden añadirse como una entrada periódica
> `checks.config` con un intervalo por defecto de `60m`. Ejecuta sin argumento para
> elegir de la lista.
>
> Al finalizar, el asistente ofrece eliminar los archivos gestionados cuyo destino ya no
> se detecta desde los directorios de salida generados actuales. Se pueden añadir nuevos
> tipos de asistente con el tiempo. En cualquier prompt de selección múltiple puedes
> escribir números de elemento (`1,3`), la palabra clave `all`, o el nombre de una
> opción. Cuando se pregunta por destinos de notificación, la lista numerada muestra solo
> los notifiers definidos en la config; las respuestas reservadas `all` / `none` /
> `default` se ofrecen en la propia pregunta — incluso cuando la config no define
> notifiers: escribe `all` para notificar a cada notifier configurado, `default` para
> heredar el valor por defecto global, o `none` para generar `notify: [none]` y suprimir
> la entrega. `none` y `default` siempre se aceptan. Cuando `default` no tiene nada que
> heredar (ningún `notify` global configurado) degrada a un watch solo-monitor
> (`notify: [none]`) con una nota de una línea — nunca vuelve a preguntar ni aborta. El
> asistente pregunta a las entradas monitorizadas por el estado de monitor
> (`enabled`/`disabled`/`previous`) y un intervalo de comprobación opcional; los archivos
> de montaje no son entradas monitorizadas, por lo que el asistente de mount omite esas
> preguntas. Consulta [wizards](wizards.md) para el flujo completo.

El bloque `then` de un watch (cuando está presente) declara las acciones tomadas cuando
se dispara — un `hook`, una lista `notify`, un `expand` (solo storage), un `kill`
(solo process), o cualquier combinación.

**Omitir `then` por completo** está soportado y significa *solo-alerta / solo-monitor*:
el `check` + `for` (o condiciones por métrica) aún se evalúan; cuando la ventana se
satisface, el watch emite un evento `firing` (visible en las tiles de Alerts/Watches de
la interfaz web, la insignia de estado "failed", el filtro de fallidos, y en el log de
eventos bajo la expansión del watch). Cuando un watch previamente disparado se limpia,
emite `recovered` y el watch vuelve a `ok`. No se ejecuta ningún hook y no se entregan
notificaciones (los valores por defecto globales `notify:` **no** se heredan para los
watches desnudos).

```yaml
watches:
  memory:
    monitor: disabled
    interval: 30s
    check:
      type: memory
      used_pct: { op: ">=", value: "90%" }
    for: { cycles: 3 }
    # no then: alert-only (web + events only; no notify/hook even if globals exist)
```

Si quieres acciones, escribe un bloque `then:` explícito. Dentro de él, omitir la
sub-clave `notify` hereda el valor por defecto global (o puedes listar nombres, o usar
`notify: [none]` para excluirte mientras sigues declarando, p. ej., un hook).

Usa `notify: [none]` (en un `then` explícito) para suprimir notificaciones: junto a otra
acción (por ejemplo `expand`), o por sí sola como un watch solo-monitor (estado y eventos
sin entrega). Siempre es válido, esté o no configurado un valor por defecto global
`notify`.

**Cadencia de notificación.** Un watch disparado entrega su `notify` **una vez**, en el
flanco de subida — cuando empieza la alerta. No re-notifica cada ciclo mientras la
condición persiste (el evento `firing` aún se registra cada ciclo para la interfaz web, y
el `hook` aún se ejecuta cada ciclo). Cuando el watch se limpia y luego se dispara de
nuevo, el siguiente episodio notifica de nuevo. Para obtener un **recordatorio** periódico
mientras un watch permanece disparado, establece `then.notify_interval` a una duración
positiva: la notificación se reenvía una vez que ese intervalo transcurre. Solo afecta a
la entrega, por lo que requiere destinos `notify`. Tanto el valor por defecto disparado
por flancos como `notify_interval` se aplican a los tipos de watch estándar (watches de
recursos de host como `storage`, las comprobaciones de service de un solo disparo, y los
watches de métrica `net`/`icmp`/`swap`). Los watches `file` y `process` tienen su propio modelo de
notificación — un evento por ruta cambiada o pid coincidente — e ignoran
`notify_interval`.

```yaml
watches:
  storage-root:
    monitor: previous
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "90%" }
    for: { cycles: 3 }
    then:
      notify: [ops-email]
      notify_interval: 30m     # re-notify every 30m while still firing
```

Usa `then.dry_run: true` cuando quieras mantener el watch y sus acciones cableados para
una ejecución de prueba, pero aún no quieras ningún efecto secundario. El watch aún
ejecuta su comprobación y ventana, emite el evento `firing` normal cuando se dispararía,
luego emite un evento `dry-run` describiendo las acciones que ejecutaría. **No** ejecuta
`hook`, envía `notify`, ni ejecuta `expand`. `dry_run` es un modificador en un bloque de
acción explícito, por lo que debe emparejarse con una acción real `hook`, `notify` o
`expand`; por sí solo no es una acción.

```yaml
watches:
  load:
    monitor: previous
    check:
      type: load
      per_cpu: true
      load5: { op: ">", value: 1.5 }
    for: { cycles: 3 }
    then:
      dry_run: true
      hook: { command: [/usr/local/bin/sermo-load-alert.sh] }
      notify: [ops-email]
```

Usa `dry_run` para host watches mientras pruebas umbrales, argv/env de hook, enrutamiento
de notifier o el gating de política de `then.expand`. Si una expansión estuviera
actualmente bloqueada por política, el evento `dry-run` reporta la supresión, pero el
dry-run no avanza el estado de cooldown/backoff. Quítalo cuando la acción deba ejecutarse
realmente. Si solo quieres una señal de panel/log a largo plazo, omite `then` por
completo o usa `notify: [none]`; esas son configuraciones solo-monitor, no ensayos de
acción.

`dry_run` y el `shadow` de remediación son intencionadamente separados:

- `then.dry_run` pertenece a un host watch bajo `watches:`. Omite los efectos
  secundarios del watch: `hook`, `notify` y `expand`.
- `remediation.shadow` pertenece a la remediación de service. Evalúa las reglas de
  remediación de service, las ventanas `for`/`within`, los guards y la política, luego
  emite eventos `shadow` sin ejecutar operaciones de service como `restart`, `start`,
  `stop` o `reload`. No suprime los hooks de host watch.

```yaml
name: apache-main
uses: apache
remediation:
  shadow: true
rules:
  restart-http:
    type: remediation
    if: { failed: { check: http } }
    then: { action: restart }
```

Un watch soporta el mismo flag `monitor` de nivel superior que un service/daemon:
`enabled` (el valor por defecto) fuerza la monitorización activa al iniciar/recargar el
daemon, `disabled` construye el watch pero lo inicia pausado, y `previous` restaura el
último estado de runtime persistido. Esto es distinto de `enabled: false`, que
deshabilita la entrada de watch estructuralmente y no se construye ningún watch de
runtime. Usa `monitor: disabled` cuando quieras que el watch sea visible en la interfaz
web y disponible para que un admin lo reanude con **monitor**.

Los directorios de watch (`paths.storages`, `paths.networks` y `paths.watches`) contienen
fragmentos de watch. Un fragmento de watch es un archivo YAML normal con un mapa de nivel
superior `watches:` y exactamente un watch:

```yaml
# /etc/sermo/storages/storage-root.yml
watches:
  storage-root:
    monitor: previous
    check: { type: storage, path: /, used_pct: { op: ">=", value: "90%" } }
    then:
      notify: [ops-email]
```

Mantener la salida del asistente en archivos separados facilita eliminar o revisar un
watch sin reescribir toda la config global. Los fragmentos de notifier siguen la misma
regla de una entrada bajo un mapa de nivel superior `notifiers:` en `paths.notifiers`.

Estas convenciones mantienen cortas las secciones por tipo a continuación:

- **Entorno del hook.** Cada hook de watch recibe `SERMO_WATCH` (el nombre del watch),
  `SERMO_CHECK_TYPE`, `SERMO_VALUE` (la lectura que viola el umbral) y `SERMO_MESSAGE`,
  más **cada clave que la comprobación pone en su resultado `Data`, exportada como
  `SERMO_<UPPER_KEY>`** (los caracteres no alfanuméricos se convierten en `_`). Cada
  tipo lista solo sus claves extra notables como *Hook extras*.
- **Resultado del hook.** Un hook puede afirmar lo que devolvió su comando. Por defecto
  una salida distinta de cero hace que el hook falle (un evento `hook-failed`); establece
  `expect_exit` para tratar otro código, o una lista de códigos como `[0, 1]`, como
  éxito. `expect_stdout` / `expect_stderr` además comprueban la salida capturada — una
  cadena simple requiere esa subcadena, o un mapeo `{op, value}` compara la salida
  recortada con los mismos operadores que el `expect_body` de una comprobación http
  (`== != > >= < <= contains =~`). Una afirmación fallida es un evento `hook-failed` con
  el detalle del desajuste.

  ```yaml
  then:
    hook:
      command: [/usr/local/bin/notify, alert]
      timeout: 10s
      expect_exit: 0                          # default; success exit code
      expect_stdout: "queued"                 # output must contain this
      expect_stderr: { op: "==", value: "" }  # …or an {op, value} comparison
  ```

  Los mismos campos `expect_exit` / `expect_stdout` / `expect_stderr` funcionan en una
  comprobación `command` (ver [Checks](rules.es.md#checks)). Las comprobaciones de comando
  también soportan `user` para ejecutar el argv como un usuario del SO específico; los
  comandos de hook no.
- **Modelo de evaluación.** Una **comprobación de nivel** (`storage`, `memory`,
  `pressure`, `load`, `fds`, `pids`, `conntrack`, `entropy`, `zombies`, swap `usage`) se
  dispara cuando **todos los predicados presentes se cumplen**
  — un predicado es `{op, value}` con el conjunto de operadores `>= > <= < == !=`;
  declara al menos uno, y añade `for: { cycles: N }` o `for: { duration: 6m }` para
  requerir primero una condición sostenida.
  Los valores de predicado comparten una gramática en cada comprobación de nivel: un campo
  `*_pct` acepta un número o un sufijo `%` explícito en 0–100 (`90` o `"90%"`), un campo
  `*_bytes` **requiere** un sufijo de tamaño (`K`/`M`/`G`/`T`, p. ej. `10G`), y
  cualquier otro campo es un número simple. Una
  **comprobación con estado** (deltas de contador — net `errors`, swap `io`, `oom`; y
  detección de cambios — net/icmp `state`/`speed`/`latency`, `file`, `process`; y cálculo
  de tasas — `diskio`) compara
  contra una línea base mantenida a través de los ciclos: el **primer ciclo prepara la
  línea base y nunca se dispara**, y un reset de contador limita el delta por ciclo a
  cero.

### `then.expand` — crecimiento de volumen (watches de storage)

Un watch `storage` puede hacer crecer automáticamente el sistema de archivos respaldado
por LVM bajo la ruta comprobada cuando se queda bajo. La expansión es nativa (Sermo la
orquesta en Go, invocando solo `lvs`/`vgs`/`lvextend` y la herramienta de crecimiento del
sistema de archivos — `resize2fs`, `xfs_growfs` o `btrfs` — que no tienen API de Go):

```yaml
watches:
  expand-backup:
    check: { type: storage, path: /mnt/backup, free_pct: { op: "<", value: "10%" } }
    for: { cycles: 3 }                    # confirm low for 3 cycles first
    policy: { cooldown: 30m }             # at most one expansion per 30m (see below)
    then:
      expand: { by: 5G }                  # grow by up to 5G (capped to VG free)
      notify: [ops-email]                 # optional: report the outcome
```

`expand.by` es la cantidad por la que crecer (`K`/`M`/`G`/`T`, unidades binarias). Está
**limitada al espacio libre del grupo de volúmenes**, y cuando el VG no tiene espacio
libre la acción falla y se reporta — Sermo nunca reduce ni reformatea. Alcance:
volúmenes lógicos LVM con un sistema de archivos ext2/3/4, xfs o btrfs; un volumen no-LVM
o de otro modo no soportado falla limpiamente en lugar de adivinar.

Como un watch se dispara **cada ciclo** que la condición se cumple, una acción `expand`
siempre debería llevar un bloque **`policy`** a nivel de watch (los mismos campos que la
remediación de service: `cooldown`, `backoff`, `max_actions`/`max_actions_window`) de
modo que el volumen no se extienda en cada tick mientras permanece bajo. La acción se
ejecuta como máximo una vez por ventana de cooldown; cada intento — éxito o fallo —
inicia el cooldown, de modo que una expansión fallida no se reintenta cada ciclo. Los
resultados se registran como eventos `expand` / `expand-skipped` / `expand-failed`.

Cuando la interfaz web está habilitada, un watch de storage con `then.expand` también
muestra una acción **expand**. Esa acción manual usa los mismos valores configurados
`check.path` y `expand.by` del YAML; el navegador no envía una ruta ni un tamaño.

`then.notify` lista nombres de notifier (cada uno debe estar definido bajo `notifiers`).
Para los watches multimétrica (`net`, `icmp`, `swap`) el `notify`/`hook` viven en el
propio `then` de cada métrica, de modo que una métrica puede tener sus propios destinos.
El asunto/cuerpo de la notificación llevan el mensaje del watch y los mismos campos
`SERMO_*` que recibe un hook.

**Las checks y los watches comparten los mismos tipos de comprobación.** Cualquier
comprobación de un solo disparo — las de recursos de host de abajo (`storage`, `memory`,
`pressure`, `load`, `fds`, `pids`, `conntrack`, `entropy`, `zombies`, `oom`, entre otras) *y* las
comprobaciones de service (`tcp`, `ports`, `http`, `command`, `file_exists`, `file`,
`lockfile`, `binary`, `pidfile`, `socket`, `libraries`, `config`, `autofs`, `route`,
`firewall_rules`, `cert`, `sqlite`/`sqlite3`, `websocket`, `count`, y las comprobaciones
de protocolo de conexión como `mysql`/`smtp`) — pueden usarse como un watch
aquí, y las de recursos de host pueden igualmente usarse en los `checks:`/reglas de un
service (ver [Checks](rules.es.md#checks)). Un watch dispara su hook con el resultado de
**alerta** de la comprobación: umbral cruzado para comprobaciones de condición, **fallo**
para comprobaciones de salud (`tcp`/`http`/`firewall_rules`/`cert`/…), de modo que p. ej.
un watch `http` alerta cuando el endpoint está caído, un watch `firewall_rules` alerta
cuando el recuento de reglas cargadas está por debajo de `min_rules`, y un watch `cert`
alerta cuando el certificado es inválido o está caducando. La
forma de watch multimétrica (`net`, `icmp`, `swap`) de abajo (un mapa `metrics:`, un hook
por métrica) y los tipos multidestino (`file`, `process`) son solo-watch;
la forma de métrica única de `net`/`icmp`/`swap` (un campo `metric:` explícito) también
funciona en los `checks:` de un service (ver [Checks](rules.es.md#checks)).
La WebUI muestra lecturas en vivo solo para sondas locales baratas; las comprobaciones
intensivas en comando/red dependen de sus eventos de watch normales.

```yaml
watches:
  storage-root:
    enabled: true          # optional, default true
    interval: 1m           # optional, default engine.interval
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "90%" } # check fires when crossed
    for: { cycles: 3 }     # optional window; reuses the rules engine
    then:
      hook:
        command: [/usr/local/bin/alert-storage.sh, "/"]
        timeout: 10s       # optional, default engine.default_timeout
```

La comprobación `storage` lee el uso del sistema de archivos para `path` y es verdadera
cuando todos los predicados presentes se cumplen (`op ∈ >=,>,<=,<,==,!=`). Los predicados
cubren el **espacio de bloques** —
`used_pct`, `free_pct`, `used_bytes`, `free_bytes` — y los **inodos** —
`inodes_used_pct`, `inodes_free_pct`, `inodes_free` (recuento absoluto).
`*_pct.value` acepta un número o un sufijo `%` explícito en 0–100, p. ej. `90` o `90%`.
`*_bytes.value` debe incluir un sufijo de tamaño (`K`/`M`/`G`/`T`, con `B`/`iB`
opcional), p. ej. `10G`; los valores de bytes sin unidad como `10` se rechazan.
Los predicados de inodo detectan el "disco lleno" que `df` oculta: un sistema de archivos
sin inodos (millones de archivos diminutos) rechaza nuevos archivos mientras los bytes
aún están libres.
```yaml
watches:
  storage-root:
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "90%" }     # block space
      free_bytes: { op: "<", value: 10G }       # absolute free space
      inodes_used_pct: { op: ">=", value: "90%" } # inode table
    then:
      hook: { command: [/usr/local/bin/alert-storage.sh, "/"] }
```

Un sistema de archivos que no reporta inodos (`inodes_total == 0`, p. ej. btrfs) nunca
dispara un predicado de inodo, por lo que no puede malinterpretar `0/0`.

#### Condiciones de montaje

La comprobación `storage` también verifica el **montaje** de su `path`, de modo que el
montaje de un sistema de archivos y su espacio se configuran en una entrada (sin `path`
duplicado). Esto también hace que una comprobación de espacio sea fiable: una ruta que
debería ser un montaje pero no lo es haría de otro modo que `statfs` reportara
silenciosamente el sistema de archivos *padre*. Añade `mounted` cuando quieras afirmar el
estado de montaje de la ruta:

```yaml
watches:
  data:
    check:
      type: storage
      path: /data
      mounted: true            # require it to be a mount point (set false to require NOT mounted)
      used_pct: { op: ">=", value: "90%" } # space predicate(s), optional alongside mount
    then:
      hook: { command: [/usr/local/bin/alert-storage.sh, "/data"] }
```

Una comprobación de storage necesita **al menos uno** de un predicado de espacio/inodo o
una condición de montaje (solo-montaje está bien). El montaje se comprueba primero desde
`/proc/mounts`: si falta cuando `mounted: true` (o está presente cuando `mounted:
false`), la comprobación alerta sobre eso y los predicados de espacio se omiten (sus
números no tendrían sentido). `fstype`, `device` y `options` no son predicados
configurables; se reportan como datos de resultado y se muestran en la WebUI como
información en vivo del sistema de archivos.

Cuando la condición se cumple para la ventana `for`/`within`, el hook se ejecuta (solo
argv, nunca una shell) y/o los notifiers se disparan, con estas variables de entorno:
`SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_PATH`, `SERMO_VALUE` (la lectura del primer
predicado), `SERMO_MESSAGE`, más el resto de los datos de la comprobación
(`SERMO_USED_PCT`, `SERMO_INODES_USED_PCT`, `SERMO_MOUNTED`, `SERMO_FSTYPE`, …).

### `net` — interfaz de red

Un watch `net` monitoriza una interfaz, agrupada bajo una sola entrada que nombra la
interfaz una vez y lista las métricas que le interesan. Cada métrica es independiente:
tiene su propia condición **y su propio hook**. Internamente la entrada se expande en un
watch por métrica, de modo que las métricas nunca comparten estado y se disparan (y
remedian) por separado.

```yaml
watches:
  net-eth0:
    monitor: disabled
    interval: 30s
    check: { type: net, interface: eth0 }
    metrics:
      state:                       # interface up/down
        on: change                 # fire on any state change; or `expect: up|down`
        then:
          hook:
            command: [/usr/local/bin/sermo-net-state.sh, eth0]
      speed:                       # link speed (Mbps)
        on: change                 # speed only supports change detection
        then:
          hook:
            command: [/usr/local/bin/sermo-net-speed.sh, eth0]
      errors:                      # rx/tx error counters
        counters: [rx_errors, tx_errors]   # optional, this is the default
        delta: { op: ">", value: 100 }     # fire when the per-cycle delta crosses
        then:
          hook:
            command: [/usr/local/bin/sermo-net-errors.sh, eth0]
      address:                     # assigned IP addresses (non-link-local)
        on: change                 # fire on renumbering; or `expect: present|absent`
        then:
          hook:
            command: [/usr/local/bin/sermo-ddns-update.sh, eth0]
```

Las cuatro métricas y sus condiciones:

- **`state`** — interfaz up/down. Usa `on: change` para disparar en cualquier transición,
  o `expect: up` / `expect: down` para disparar siempre que el estado **sea** el valor
  esperado.
- **`speed`** — velocidad del enlace en Mbps. Soporta solo `on: change` (se dispara
  cuando la velocidad difiere de la línea base).
- **`errors`** — suma los `counters` nombrados (por defecto `rx_errors`, `tx_errors`) y
  se dispara cuando el **delta** por ciclo satisface `delta: {op, value}`.
- **`address`** — las direcciones asignadas de la interfaz (IPv4 + IPv6 global; la
  link-local se excluye). Usa `on: change` para disparar cuando el conjunto cambia — una
  renumeración forzada por el proveedor o una reconexión, el disparador natural para un
  hook de DNS dinámico — o `expect: present` / `expect: absent` para disparar siempre que
  las direcciones **estén** en el estado esperado (una sesión PPP puede estar activa con
  IPCP fallido y sin dirección asignada; el catalog service `pppd` usa `expect:
  present`).

Hook extras: `SERMO_INTERFACE`, `SERMO_METRIC`, y — para las métricas de cambio
(`state`/`speed`/`address`) — `SERMO_OLD`/`SERMO_NEW`.

### `icmp` — host externo (ping)

Un watch `icmp` monitoriza un **host externo** mediante eco ICMP (ping): alcanzabilidad y
latencia de ida y vuelta. El host se nombra una vez y cada métrica es independiente, con
su propia condición **y su propio hook**. La entrada se expande en un watch por métrica,
de modo que las métricas no comparten estado.

```yaml
watches:
  ping-gw:
    monitor: disabled
    interval: 30s
    check: { type: icmp, host: 8.8.8.8, count: 3 }   # count optional, default 3
    metrics:
      state:                       # reachable / unreachable
        on: change                 # fire on any transition; or `expect: up|down`
        then:
          hook:
            command: [/usr/local/bin/sermo-host-state.sh, "8.8.8.8"]
      latency:                     # round-trip time (ms)
        threshold: { op: ">", value: 100 }   # fire when rtt crosses the threshold
        then:
          hook:
            command: [/usr/local/bin/sermo-host-latency.sh, "8.8.8.8"]
```

Las dos métricas y sus condiciones:

- **`state`** — host alcanzable (`up`) o inalcanzable (`down`). Usa `on: change` para
  disparar en cualquier transición, o `expect: up` / `expect: down` para disparar siempre
  que el estado **sea** el valor esperado.
- **`latency`** — tiempo de ida y vuelta en milisegundos. Usa o bien
  `threshold: {op, value}` (el mismo conjunto de operadores que storage) para disparar
  cuando el RTT cruza un límite fijo, **o** `change: {delta}` para disparar en un salto
  abrupto (`|rtt − rtt_prev| > delta`); establece exactamente uno. Las condiciones de
  latencia solo se aplican mientras el host es alcanzable; un ciclo inalcanzable nunca
  dispara la latencia y nunca actualiza la línea base de cambio (de modo que la línea base
  es el último RTT *alcanzable*).

Hook extras: `SERMO_HOST`, `SERMO_METRIC`, y — para las métricas de cambio —
`SERMO_OLD`/`SERMO_NEW`.

ICMP requiere privilegios elevados: el daemon necesita la capacidad `CAP_NET_RAW` (o el
sysctl `net.ipv4.ping_group_range` del host debe incluir el gid del daemon) para abrir un
socket ICMP raw. Esta iteración es **solo-IPv4**.

### `swap` — swap del sistema

Un watch `swap` monitoriza el swap del sistema como dos métricas independientes, agrupadas
como `net`/`icmp` (cada una con su propia condición **y su propio hook**). `usage` detecta
el swap llenándose (una comprobación de nivel); `io` detecta el thrashing de swap (un
delta de contador — paginación intensa de entrada/salida, un signo clásico de presión de
memoria).

```yaml
watches:
  swap:
    monitor: disabled
    interval: 30s
    check: { type: swap }
    metrics:
      usage:                                 # how full swap is (level check)
        used_pct: { op: ">=", value: 80 }    # any of used_pct / free_pct / free_bytes
        then: { hook: { command: [/usr/local/bin/sermo-swap-usage.sh] } }
      io:                                    # paging activity (counter delta)
        delta: { op: ">", value: 1000 }      # pages swapped in+out per cycle
        then: { hook: { command: [/usr/local/bin/sermo-swap-io.sh] } }
```

- Predicados de **`usage`**: `used_pct`, `free_pct` (del swap total) y `free_bytes`
  (un tamaño con sufijo `K`/`M`/`G`/`T`, p. ej. `1G` — la misma gramática que la
  comprobación de storage). Un host **sin swap configurado** nunca se dispara (de modo que
  un predicado `free_bytes` no se dispara erróneamente en una máquina sin swap).
- **`io`** suma las páginas intercambiadas **de entrada y salida** (`pswpin`+`pswpout` de
  `/proc/vmstat`); el umbral `delta` es páginas por intervalo, de modo que escala con
  `interval`.
- Hook extras: `SERMO_METRIC` (`usage`|`io`), `SERMO_TOTAL_BYTES`,
  `SERMO_FREE_BYTES`.

### `load` — carga media del sistema

Un watch `load` comprueba las cargas medias de 1/5/15 minutos contra umbrales. Con
`per_cpu: true` las cargas se dividen primero por el recuento de CPU, de modo que un
umbral significa **carga por núcleo** (≈1.0 está completamente utilizado) y la misma
config funciona en máquinas de cualquier tamaño.

```yaml
watches:
  load:
    monitor: disabled
    interval: 30s
    check:
      type: load
      per_cpu: true                  # optional, default false: divide by NumCPU
      load5: { op: ">", value: 1.5 }    # any of load1 / load5 / load15
      load15: { op: ">", value: 1.0 }
    for: { cycles: 3 }
    then: { hook: { command: [/usr/local/bin/sermo-load-alert.sh] } }
```

Predicados: `load1`, `load5`, `load15`. Prefiere `load5`/`load15` para saturación
sostenida (`load1` es picudo). Hook extras: `SERMO_LOAD1`/`SERMO_LOAD5`/
`SERMO_LOAD15` (en bruto) y `SERMO_NUM_CPU`.

### `memory` — RAM del sistema

Un watch `memory` comprueba la RAM del sistema contra umbrales. Está construido sobre la
estimación **MemAvailable** del kernel (de `/proc/meminfo`) — la memoria que las nuevas
asignaciones pueden reclamar sin hacer swap — de modo que la caché de páginas y los
buffers reclamables nunca se leen como "usados". Detecta la fuga lenta o el host
sobrecargado antes de que lo haga el OOM killer.

```yaml
check:                                   # in a watches: entry like `load` above
  type: memory
  used_pct: { op: ">=", value: "90%" }   # (total - available) / total
  # available_bytes: { op: "<", value: 1G }   # absolute headroom, alternatively
```

Predicados: `used_pct`, `available_pct` (de la RAM total) y `available_bytes`
(sufijo de tamaño requerido, p. ej. `1G` — la gramática de tamaño compartida). Un host
cuyo `/proc/meminfo` no reporta total nunca se dispara. Emparéjalo con `for: { cycles: 3
}` de modo que un pico momentáneo no alerte. Hook extras: `SERMO_TOTAL_BYTES`,
`SERMO_AVAILABLE_BYTES`, `SERMO_USED_PCT`, `SERMO_AVAILABLE_PCT`.

### `pressure` — tiempo de stall PSI del kernel

Un watch `pressure` comprueba un recurso **PSI** del kernel (`/proc/pressure/cpu`,
`memory` o `io`) contra umbrales de porcentaje de stall. PSI reporta la fracción del
tiempo de reloj que las tareas pasaron **bloqueadas** esperando el recurso — la propia
señal del kernel de "este host está sufriendo". Complementa `load` (profundidad de cola)
y `memory` (headroom) con el stall realmente experimentado: un host puede verse bien en
ambos y aún estar con thrashing.

```yaml
check:                                   # in a watches: entry like `load` above
  type: pressure
  resource: memory                       # required: cpu | memory | io
  some_avg10: { op: ">", value: 10 }     # % of time SOME tasks stalled (10s avg)
  # full_avg60: { op: ">", value: 5 }    # % of time ALL tasks stalled (60s avg)
```

Predicados (cada uno un porcentaje de stall, ventanas móviles de 10s/60s/300s):
`some_avg10`/`some_avg60`/`some_avg300` y `full_avg10`/`full_avg60`/
`full_avg300`. `some` significa al menos una tarea bloqueada; `full` significa que todas
las tareas no inactivas están bloqueadas (la forma severa; para `cpu` es 0 o ausente en
kernels más antiguos). Prefiere `some_avg60`/`full_avg60` con una ventana `for` para
presión sostenida. Un kernel construido sin PSI (`CONFIG_PSI=n`) nunca se dispara. Hook
extras: `SERMO_RESOURCE` y las seis medias `SERMO_SOME_*`/`SERMO_FULL_*`.

### `oom` — OOM kills del kernel

Un watch `oom` se dispara cuando el OOM killer del kernel ha segado procesos desde el
último ciclo — un delta de contador sobre el contador acumulativo `oom_kill` de
`/proc/vmstat`.

```yaml
watches:
  oom:
    check: { type: oom }            # delta optional; default fires on any kill (> 0)
    then: { hook: { command: [/usr/local/bin/sermo-oom-alert.sh] } }
```

El caso común es "alertar en cualquier OOM kill", por lo que `delta` puede omitirse (por
defecto `> 0`); establece un umbral más alto para alertar solo en una ráfaga. Un host
cuyo kernel no expone `oom_kill` nunca se dispara. Hook extras: `SERMO_TOTAL` (kills
acumulativos).

### `fds` — descriptores de archivo del sistema

Un watch `fds` comprueba los descriptores de archivo abiertos a nivel de sistema contra
el máximo del kernel (`fs.file-max`, de `/proc/sys/fs/file-nr`). El agotamiento de fds
hace que cada `open()`/`socket()`/`accept()` falle con `EMFILE`/`ENFILE`, por lo que vale
la pena detectarlo pronto.

```yaml
check:                                   # in a watches: entry like `load` above
  type: fds
  used_pct: { op: ">=", value: 85 }      # allocated / file-max
  # free: { op: "<", value: 10000 }      # absolute headroom, alternatively
```

Predicados: `used_pct` (porcentaje del límite), `free` (`file-max − allocated`) y
`allocated` (absoluto). Hook extras: `SERMO_ALLOCATED`, `SERMO_MAX`,
`SERMO_USED_PCT`, `SERMO_FREE`.

### `diskio` — tasas de I/O de dispositivo de bloques

Un watch `diskio` monitoriza la I/O de un dispositivo de bloques, calculada a partir de
los deltas por ciclo de `/proc/diskstats`: **utilización** (fracción del tiempo de reloj
que el dispositivo estuvo ocupado), **throughput** y **latencia media de petición**.
Úsalo para discos saturados o degradados que las comprobaciones de espacio de
almacenamiento no pueden ver. Es **con estado**: el primer ciclo solo establece la línea
base (nunca se dispara), y un reset de contador limita el delta a cero.

```yaml
watches:
  sda-busy:
    interval: 30s
    check:
      type: diskio
      device: sda                          # required: a /proc/diskstats name
      util_pct: { op: ">=", value: 90 }    # % of the cycle the device was busy
      await_ms: { op: ">", value: 50 }     # avg ms per completed request
      # read_bytes:  { op: ">", value: 100M }  # bytes/second, size suffix
      # write_bytes: { op: ">", value: 50M }
    for: { cycles: 3 }
    then: { hook: { command: [/usr/local/bin/sermo-diskio-alert.sh, sda] } }
```

Predicados: `util_pct` (0–100), `await_ms` (ms simples), y `read_bytes`/
`write_bytes` — **bytes por segundo**, escritos con la gramática de tamaño compartida
(`50M` = 50 MiB/s). Todos los predicados presentes deben cumplirse (AND), de modo que
`util_pct` + `await_ms` juntos distinguen "ocupado y lento" de meramente ocupado. Un
dispositivo ausente de `/proc/diskstats` nunca se dispara (la comprobación reporta el
error). Hook extras: `SERMO_DEVICE`, `SERMO_UTIL_PCT`, `SERMO_READ_BYTES`,
`SERMO_WRITE_BYTES`, `SERMO_AWAIT_MS`.

### `pids` — tabla de PID del kernel

Un watch `pids` comprueba la tabla de PID del kernel — el total de entidades de
planificación vivas (hilos; cada una consume un PID, del cuarto campo de
`/proc/loadavg`) contra `kernel.pid_max`. Una tabla llena hace que cada
`fork()`/`clone()` falle con `EAGAIN` en todo el host: el estado final que alcanza un
bucle de fork descontrolado o un pool de hilos con fugas, y donde la advertencia de
crecimiento de [`zombies`](#zombies--defunct-processes) termina llegando.

```yaml
check:                                   # in a watches: entry like `load` above
  type: pids
  used_pct: { op: ">=", value: 90 }      # threads / kernel.pid_max
  # free: { op: "<", value: 5000 }       # absolute headroom, alternatively
```

Predicados: `used_pct` (porcentaje del límite), `free` (`pid_max − threads`) y
`count` (hilos absolutos). Un `pid_max` ilegible deja `used_pct`/`free` desconocidos
(nunca se disparan); `count` sigue funcionando. Hook extras: `SERMO_COUNT`,
`SERMO_MAX`, `SERMO_USED_PCT`, `SERMO_FREE`.

### `conntrack` — tabla de conexiones de netfilter

Un watch `conntrack` comprueba la tabla de seguimiento de conexiones de netfilter contra
su máximo (`nf_conntrack_max`, de `/proc/sys/net/netfilter`). Una tabla llena
**descarta silenciosamente nuevas conexiones** (y registra `nf_conntrack: table full`),
por lo que vale la pena detectarlo en gateways, proxies y cajas NAT ocupadas antes de que
se sature.

```yaml
check:                                   # in a watches: entry like `load` above
  type: conntrack
  used_pct: { op: ">=", value: 90 }      # count / nf_conntrack_max
  # free: { op: "<", value: 20000 }      # absolute headroom, alternatively
```

Predicados: `used_pct` (porcentaje del máximo), `free` (`nf_conntrack_max − count`)
y `count` (absoluto). Necesita el módulo `nf_conntrack` cargado; sin él la comprobación
nunca se dispara. Hook extras: `SERMO_COUNT`, `SERMO_MAX`, `SERMO_USED_PCT`,
`SERMO_FREE`.

### `firewall_rules` — reglas de firewall cargadas

Usa `firewall_rules` para cargadores de firewall como FireHOL que salen tras instalar las
reglas. Es una comprobación de salud: como watch se dispara cuando el recuento de reglas
nftables/iptables cargadas cae por debajo de `min_rules` (por defecto `1`).

```yaml
watches:
  firewall:
    check: { type: firewall_rules, backend: auto, min_rules: 1 }
    then: { hook: { command: [/usr/local/bin/firewall-missing.sh] } }
```

`backend` es `auto`, `nftables` o `iptables`. Hook extras:
`SERMO_BACKEND`, `SERMO_RULES`, `SERMO_MIN_RULES`.

### `entropy` — pool de entropía del kernel

Un watch `entropy` comprueba la entropía disponible del kernel (bits) de
`/proc/sys/kernel/random/entropy_avail` contra un umbral. La baja entropía hace que las
lecturas de `/dev/random` se bloqueen y ralentiza la criptografía y los handshakes TLS —
más visible en VMs y hosts headless/embebidos sin un RNG por hardware.

```yaml
check:                                   # in a watches: entry like `load` above
  type: entropy
  avail: { op: "<", value: 200 }         # fire when available entropy drops below 200 bits
```

El único predicado `avail: {op, value}` es requerido; la forma usual es
`avail < N`. Hook extras: `SERMO_AVAIL` (el mismo valor que `SERMO_VALUE`, bits
disponibles).

### `zombies` — procesos difuntos

Un watch `zombies` cuenta los procesos en estado de ejecución zombie (difunto) — los que
han salido pero cuyo padre no los ha segado — contra un umbral. Unos pocos son
transitorios y normales; un recuento creciente significa que un padre está perdiendo
slots de hijos y eventualmente agotará la tabla de PID.

```yaml
check:                                   # in a watches: entry like `load` above
  type: zombies
  count: { op: ">", value: 20 }          # fire when more than 20 zombies persist
```

El único predicado `count: {op, value}` es requerido; emparéjalo con una ventana `for` de
modo que una ráfaga momentánea de hijos saliendo no se dispare. Hook extras:
`SERMO_ZOMBIES` (el mismo valor que `SERMO_VALUE`, el recuento).

### `file` — atributos de archivo/directorio

Un watch `file` monitoriza un archivo o directorio en busca de cambios de atributos —
tamaño, permisos, propietario y eliminación — y ejecuta el hook de la entrada **una vez
por cambio**. Es con estado: recuerda los atributos de cada ruta a través de los ciclos y
reporta solo las transiciones, adoptando la línea base silenciosamente en el primer ciclo
(un arranque del daemon nunca se dispara). Con `recursive: true` vigila todo el subárbol,
de modo que un hook se dispara por entrada cambiada.

```yaml
watches:
  app-data:
    monitor: disabled
    interval: 30s
    check:
      type: file
      path: /var/lib/myapp            # file or directory
      recursive: true                 # optional, default false (whole subtree)
      size: { op: ">", value: 1048576 }   # edge threshold; or `size: { on: change }`
      permissions: { on: change }     # mode bits (perm + setuid/setgid/sticky)
      owner: { on: change }           # owning uid/gid
      existence: { on: delete }       # a previously-seen path is gone
    then:
      hook:
        command: [/usr/local/bin/sermo-file-change.sh]
        timeout: 10s
```

Las condiciones (declara al menos una):

- **`size`** — o bien `{ on: change }` (disparar siempre que el tamaño en bytes difiera
  del último ciclo) o un umbral `{op, value}` (el mismo conjunto de operadores que
  storage). El umbral es **disparado por flancos**: se dispara una vez cuando el tamaño
  cruza hacia la condición y se rearma solo después de que vuelve a salir — no cada ciclo
  mientras está violado.
- **`permissions`** — `on: change`; se dispara cuando los bits de permiso cambian.
- **`owner`** — `on: change`; se dispara cuando el uid o gid propietario cambia.
- **`existence`** — `on: delete`; se dispara cuando una ruta que existía deja de existir
  (la recreación se adopta entonces silenciosamente). La eliminación es la única
  transición reportada.

Cuando `recursive: true` y la ruta es un directorio, cada entrada del subárbol se rastrea
independientemente (los symlinks se vigilan como enlaces, nunca se siguen). Las nuevas
entradas se adoptan silenciosamente; las entradas eliminadas disparan `existence` si está
configurado. Cada cambio detectado es **un evento y una ejecución de hook**, de modo que
un ciclo que encuentra varios cambios se dispara varias veces.

Hook extras: `SERMO_PATH` (la ruta cambiada), `SERMO_CHANGE`
(`size`|`size_threshold`|`permissions`|`owner`|`deleted`), `SERMO_OLD`/`SERMO_NEW`
(valor antiguo/nuevo), y `SERMO_SIZE`/`SERMO_OP` para condiciones de tamaño.

### `process` — proceso por nombre

Un watch `process` rastrea los procesos cuyo **nombre** coincide (el basename del exe
resuelto o su ruta completa), opcionalmente filtrado por el `user` propietario, y dispara
el hook **una vez por PID coincidente** cuando ese proceso ha estado vivo al menos `for`
y/o su CPU/memoria/IO cruza un umbral. Es distinto de la comprobación `process` por
service, que reporta el estado running/zombie/absent.

```yaml
watches:
  hot-workers:
    monitor: disabled
    interval: 30s
    check:
      type: process
      name: myworker                  # exe basename (e.g. myworker) or full path
      user: www-data                  # optional: also match the owning user
      for: 5m                         # optional: observed alive at least this long
      cpu: { op: ">", value: 80 }     # optional: CPU % (rate)
      memory: { op: ">", value: 524288000 }   # optional: RSS bytes
      io: { op: ">", value: 10485760 }         # optional: read+write bytes/sec
      gone: true                      # optional: fire when a tracked PID disappears
    then:
      hook:
        command: [/usr/local/bin/sermo-proc-alert.sh]
        timeout: 10s
```

Declara al menos uno de `for`, `cpu`, `memory`, `io`, `gone`. Las condiciones de presencia
(`for`/`cpu`/`memory`/`io`) **todas** deben cumplirse para que un PID se dispare (AND),
y el disparo es **disparado por flancos por PID**: el hook se ejecuta una vez cuando las
condiciones se vuelven verdaderas y se rearma solo después de que dejan de cumplirse — no
cada ciclo. `cpu` e `io` son tasas, por lo que necesitan dos muestras: un PID nuevo nunca
se dispara con ellas en su primer ciclo. Cada PID coincidente se rastrea
independientemente — **un evento y un hook por PID** — de modo que un pool de workers
produce un hook por worker infractor.

`gone: true` es lo inverso — se dispara una vez cuando un PID coincidente previamente
visto **desaparece** (y se rearma si vuelve), de modo que nunca se dispara meramente
porque el proceso está presente. Establécelo solo para una alerta pura de liveness
("nginx is gone"), o junto a las condiciones de presencia. Con múltiples PIDs
coincidentes se dispara por PID salido.

Hook extras: `SERMO_PID` (el pid coincidente), `SERMO_PROCESS` (el nombre configurado),
`SERMO_CHANGE` (`threshold` para un disparo de presencia, `gone` para una desaparición),
`SERMO_USER` (si está establecido), `SERMO_AGE_SECONDS`, `SERMO_MEMORY` (bytes RSS), y —
una vez que una tasa está disponible — `SERMO_CPU` (porcentaje) y `SERMO_IO`
(bytes/seg).

`for` se mide desde cuando el daemon **observó por primera vez** el proceso, de modo que
un reinicio del daemon lo restablece (el tiempo real transcurrido desde el inicio no se
rastrea a través de reinicios). `io` lee `/proc/<pid>/io`, que requiere que el daemon
tenga permiso para leerlo (típicamente ejecutándose como root); cuando es ilegible la
condición de IO nunca se dispara. El filtro opcional `user:` se resuelve a través de
`engine.user_lookup`; los UIDs numéricos se aceptan y evitan la ambigüedad del servicio
de identidad del host. La WebUI muestra las coincidencias actuales, los PIDs y los
contadores agregados RSS/IO.

#### `then.kill` — terminar el proceso coincidente

Un process watch puede **terminar el PID coincidente de forma nativa**, sin un
script de hook externo, con una acción `then.kill`. Reutiliza el propio
señalizador de procesos del daemon (la misma vía `kill(2)` que usan la parada de
servicios y la política `kill+umount` de los mounts), de modo que la política vive
enteramente en la configuración:

```yaml
watches:
  kill-stale-sudo:
    monitor: enabled
    interval: 1m
    check:
      type: process
      name: sudo
      for: 120m            # observado vivo al menos 120 minutos
    then:
      kill:
        signal: TERM       # opcional, por defecto TERM; TERM o KILL
        # escalate: true     # opcional: seguir la señal con SIGKILL para un superviviente
        # term_timeout: 10s  # opcional (solo escalate): margen antes del SIGKILL
        # kill_timeout: 5s   # opcional (solo escalate): margen tras el SIGKILL
```

- **`signal`** es la señal a enviar, `TERM` (por defecto) o `KILL`. La valida el
  mismo parser que usa el daemon, así que un error tipográfico o una señal
  inapropiada falla en `config validate`.
- **`escalate: true`** convierte la señal única en el modelo TERM→KILL de la
  política de parada: envía la señal, espera `term_timeout` y —tras **re-verificar
  que el PID sigue coincidiendo** con este watch (defensa contra reuso de PID
  durante el margen)— envía `SIGKILL` a un superviviente.
- Se dispara con la misma semántica **edge-triggered, una vez por PID** que el
  hook, y solo en un disparo de **presencia** (`for`/`cpu`/`memory`/`io`) — nunca
  en un disparo `gone`, que no tiene nada que señalar. Cada envío de señal se
  registra como un evento `kill` (o `kill-failed`) visible en la actividad del
  watch.
- `then.dry_run: true` y el modo pánico **suprimen** el kill (se emite en su lugar
  un evento `dry-run` / `panic-suppressed`), igual que el hook y notify.
- `kill` puede ir solo (un watch de kill puro) o acompañar a un `hook` y/o
  `notify`. **Solo es válido en un `process` watch** (como `then.expand` es solo de
  storage). Como señala procesos reales, el daemon debe tener permiso para hacerlo
  (típicamente ejecutándose como root), y `name`/`user` deben acotarse con
  precisión — una coincidencia amplia mata cada PID coincidente que cruce la
  condición.

Se añadirán otros tipos de recursos como nuevos valores de `type` de comprobación usando
la misma estructura de watch/hook.

## Valores por defecto globales

Solo las partes por service de `defaults` se fusionan con un service: `stop_policy`,
`policy`, y `rule_window`. Los ajustes de ámbito de motor (`interval`,
`max_parallel_checks`, `max_parallel_operations`, `default_timeout`,
`operation_timeout`, `startup_delay`, `backend`, `user_lookup`,
`user_lookup_timeout`, `state_cache_size`) son configuración del daemon y nunca se
fusionan con un service.

`defaults.policy.cooldown` es **requerido y positivo**: cada service resuelto hereda un
cooldown de prevención de bucles a menos que lo sustituya.

`defaults.rule_window` es la **ventana de disparo alternativa** para cualquier regla que
no declare ni su propio `for` ni `within` (ver la sección de reglas). Acepta:

```yaml
defaults:
  rule_window:
    cycles: 1            # choose cycles or duration, not both
    # duration: 6m
    mode: consecutive    # consecutive (a `for` window) | within (a sliding window)
    # min_matches: 1     # mode: within only — optional, defaults to 1 (true at least once)
```

`cycles: 1` + `mode: consecutive` es también el valor por defecto integrado (disparar en
el momento en que la condición de una regla es verdadera), por lo que el `sermo.yml`
distribuido lleva este bloque solo como referencia comentada.
Sube `cycles` (p. ej. `3`) o establece `duration` (p. ej. `6m`) para requerir una
ventana consecutiva más larga antes de que se dispare cada regla sin ventana, o usa
`mode: within` con `min_matches` para una ventana deslizante. El propio `for`/`within` de
una regla siempre sustituye la alternativa, y como los otros valores por defecto por
service puede sustituirse por catalog service o service.

## Orden de resolución

Un service se resuelve en una definición plana, de menor precedencia primero:

1. Los `defaults` globales efectivos (partes por service).
2. El daemon `uses`, o la cadena `clone`, fusionado por encima.
3. Los campos propios del service (mayor precedencia).
4. Expansión de `${var}`, una vez, sobre el resultado fusionado.
5. Validación del service aplanado.

```
global defaults  <  daemon (uses) or clone source  <  service overrides
```

`uses` y `clone` se toman **sin expandir**, de modo que un clon puede sustituir una sola
variable y hacer que cada referencia `${var}` se resuelva al nuevo valor.

## Reglas de fusión

- Los escalares y las listas sobrescriben.
- Los mapas se fusionan recursivamente.
- Las secciones con nombre (`checks`, `preflight`, `postflight`, `processes`, `rules`)
  son mapas indexados por nombre, de modo que un hijo puede sustituir un campo de una
  entrada.
- Deshabilita una entrada heredada con `enabled: false`; elimínala con
  `delete: true`.

Los ejemplos trabajados (clonación, deshabilitación, múltiples instancias) viven en
[services](services.es.md#cloning).
Las plantillas de catálogo para versiones/instancias instaladas usan `%v`, `%n` y `%i`;
ver [versioned services](services.es.md#versioned-services).
Cuando las plantillas simples `%v` o `%n` también tienen un binario de slot activo sin un
sufijo, como `php` junto a `php8.4` o `python` junto a `python3`, Sermo materializa esa
entrada sin versión automáticamente. Las plantillas compuestas con tokens adicionales no
infieren un slot activo de `versions.from`; declara `versions.current_from` para entradas
de compatibilidad como `/usr/bin/java` junto al descubrimiento de versiones de Java.
`current_from` puede ser una ruta o una lista de rutas. Establece
`versions.unversioned: false` solo cuando el slot activo sin marcador o `current_from`
deba ignorarse. Un nombre materializado no debe colisionar con un documento explícito de
la misma categoría; la validación reporta eso como un error de configuración. Cuando una
plantilla usa `${current}`, los listados de inventario también marcan una entrada con
versión como actual cuando el wrapper de slot activo y esa entrada reportan el mismo
`version_short`.
`versions.from` puede ser una ruta/lista neutral respecto al backend, o un mapa con ramas
`systemd` y `openrc`. Las ramas de mapa son exclusivas: Sermo selecciona solo el backend
de init activo de `engine.backend` o `SERMO_BACKEND`, recurriendo al `${init}` detectado.
Las plantillas de catalog service deberían poner los tokens en `service:` en su lugar;
sus instancias de daemon se materializan a partir de unidades systemd/OpenRC activas,
mientras que las apps enlazadas poseen el descubrimiento y la validación de binarios.

## Variables de recurso de binario

Declara los candidatos ejecutables como una variable normal y selecciónalos a través de
`preflight.binary`:

```yaml
variables:
  binary:
    - /usr/bin/php-fpm${version}
    - /usr/sbin/php-fpm${version}
preflight:
  binary: { type: binary, path: "${binary}" }
```

La entrada de preflight de recurso reduce `${binary}` al primer candidato que coincida
con el tipo declarado. `binary` requiere un archivo ejecutable regular; `file` requiere
un archivo regular; `lockfile` requiere un archivo regular; `pidfile` requiere un archivo
regular; `socket` requiere un socket Unix. Si ninguno coincide actualmente, Sermo
mantiene el primer candidato no vacío de modo que el preflight de runtime reporte la ruta
incorrecta explícitamente en lugar de expandirse a una cadena vacía. Las rutas deben ser
absolutas tras el templating.

### Prefijo de búsqueda `${bindir}`

Cuando la única diferencia entre los candidatos es el directorio de binarios estándar,
usa el prefijo `${bindir}` en lugar de listarlos a mano. Se expande en tiempo de carga en
un candidato por directorio de búsqueda estándar, en orden:

```
/usr/bin → /usr/sbin → /usr/local/bin → /usr/local/sbin
```

Así que `binary: ${bindir}/mysqld` es la forma abreviada de:

```yaml
variables:
  binary:
    - /usr/bin/mysqld
    - /usr/sbin/mysqld
    - /usr/local/bin/mysqld
    - /usr/local/sbin/mysqld
```

`${bindir}` es un prefijo, no un valor independiente: siempre escribe
`${bindir}/<name>`. Se compone con plantillas `${version}`
(`${bindir}/php-fpm${version}`) y puede mezclarse con rutas explícitas dentro de una
lista cuando un binario también reside fuera de los directorios estándar. Como los
candidatos se resuelven al primero que existe, la ruta seleccionada es la instalada
independientemente del orden de búsqueda. Para binarios fuera de estos cuatro directorios
(p. ej. `/opt/...`, `/usr/lib/...`), mantén una ruta explícita.

Usa `variables.binary` más una entrada de preflight explícita para apps, daemons y
services. Las librerías usan el mismo patrón con `type: file`:

```yaml
name: glibc
variables:
  binary: /lib64/libc.so.6
preflight:
  file: { type: file, path: "${binary}" }
```

Las comprobaciones de comando también pueden declarar variables. `from: stdout` y
`trim: true` son los valores por defecto; `default` es opcional y de lo contrario vacío.
Cuando el comando tiene éxito, esos valores también se adjuntan al `data` del resultado.
Los nombres de comando integrados `version` y `version_short` ya exportan `version` y
`version_short`; un comando `version` también deriva `version_short` de stdout, de modo
que solo los valores especiales necesitan un `export:` explícito:

```yaml
preflight:
  api:
    type: command
    command: ["/usr/bin/tool", "api-version"]
    export:
      api: { regex: "API ([0-9]+)", default: "" }
```

## Variables

```yaml
variables:
  host: 127.0.0.1
  port: 8080
checks:
  http:
    type: http
    url: "http://${host}:${port}/health"
```

- Las variables son cadenas literales planas; un valor no debe contener a su vez otra
  `${var}` (pero `${env:...}` está permitido — ver abajo). Las plantillas de
  versión/instancia del catálogo pueden usar sus marcadores de posición de plantilla como
  `${version}` o `${n}` en las variables de ruta antes de la materialización.
- La expansión es una sola pasada: cualquier `${...}` que quede después es una variable
  indefinida y un error de validación.
- Los campos numéricos (`port`, `expect_status`) aceptan un int, una cadena entrecomillada
  o una `${var}`, y se parsean tras la expansión.

### Variables personalizadas globales (`defaults.variables`)

Declara las variables una vez bajo `defaults.variables` y úsalas como `${name}` **en
cualquier lugar** donde se expandan valores — cada service, daemon y entrada de host
`watches:`:

```yaml
defaults:
  policy: { cooldown: 5m }
  # remediation.shadow puts the service's remediation rules into observation-only
  # mode: full condition evaluation, window tracking (for/within), guard
  # evaluation and policy checks (cooldown, max_actions, backoff) still occur and
  # produce "shadow" events with rich detail about what Sermo would have done and
  # why (including suppressions). No operations are executed and the live
  # RemediationState is not advanced. Perfect for safely tuning rules before
  # going live. This does not affect host watches; put dry_run: true inside a
  # watch's then block to rehearse hooks/notifies/expand without executing them.
  # A per-service setting overrides the default.
  #   remediation: { shadow: true }
  remediation: { shadow: false }

  variables:
    custom_var1: /opt/myapp
    custom_var2: 8443
    libdir: [/usr/lib64, /usr/lib]   # list = first existing path
```

- **Precedencia:** la propia `variables.X` de un service prevalece sobre
  `defaults.variables.X`, que prevalece sobre las integradas (`${host}`, `${port}`,
  `${hostname}`, …). Así que un `host`/`port` personalizado sustituye la integrada para
  cada service que no establezca el suyo.
- **Nombres:** deben ser únicos (una clave YAML duplicada es un error de carga) y no
  deben ser un **nombre reservado** — las palabras clave de selección `all`/`none`/`default`
  y los tokens de runtime `date`/`event`/`action` se rechazan. `binary` está permitido y
  se resuelve a través de `preflight.binary` cuando lleva candidatos de ruta. Los nombres
  integrados (`host`, `port`, …) están permitidos y sustituyen la integrada (ver
  precedencia).
- Los valores soportan `${env:...}` y la primera ruta existente de la lista exactamente
  como las variables por service. No pueden contener otra `${var}` (sin anidamiento),
  como cualquier variable.
- Una `${custom_x}` indefinida es un error de validación en services **y** watches.

### Secretos del entorno

`${env:NAME}` se resuelve a la variable de entorno `NAME` **en cualquier lugar** de la
config — campos de service *y* los bloques globales (DSNs/webhooks de notifier, la
contraseña web, …) — de modo que los secretos nunca se escriben en el archivo:

```yaml
checks:
  api:
    type: http
    url: "https://api.example.com/health"
    headers:
      Authorization: "Bearer ${env:API_TOKEN}"   # read from the daemon's env

notifiers:
  ops:
    type: email
    dsn: "${env:SMTP_DSN}"
```

- Se soporta un valor por defecto estilo shell: `${env:NAME:-fallback}` usa `fallback`
  cuando `NAME` no está establecida o está vacía.
- Una variable no establecida se expande a su valor por defecto (o vacío) y **nunca** es
  un error de validación — pero si alimenta un campo requerido (un `dsn` de notifier, la
  `password` web), ese campo se lee entonces como faltante. Ejecuta `config validate` con
  el mismo entorno que el daemon (p. ej. el `EnvironmentFile` de systemd) para comprobar
  que los secretos se resuelven.
- A diferencia de `${var}`, `${env:...}` se resuelve por separado, por lo que también
  funciona en la config global (que no tiene sección `variables`) y dentro del valor de
  una variable.

## Validación

```sh
sermoctl config validate          # whole Sermo configuration
```

`config validate` sale con `78` en un error de configuración. Consulta
[rules](rules.es.md) para lo que cada sección puede contener.

## Diagnósticos

`config validate` comprueba que la configuración esté *bien formada*. Cuando
`engine.diagnostics` está establecido, `sermod` también ejecuta comprobaciones
programadas contra el **host en vivo** y añade cada snapshot al archivo de log.

Cada línea JSON incluye `time` (RFC3339), `errors`, `warnings` y un array `findings`.
Cada finding tiene `level` (`error` / `warning` / `info`), `scope` y `message`. Las
comprobaciones cubren:

- **Configuración** — cada problema de `config validate` (errores).
- **Alineación de intervalos** — los `interval` por comprobación que **no son un
  múltiplo de la resolución global** (`engine.interval`) o están por debajo de ella, de
  modo que serán redondeados (ver [intervalo por comprobación](#per-check-interval)).
- **Recursos del host** — cosas referenciadas que **no existen en este host**:
  interfaces de red (watches `net`), archivos/directorios (comprobaciones
  `storage`/`count`, watches `file`), **puntos de montaje** (una comprobación `storage`
  con condiciones de montaje cuya ruta no está actualmente montada), **dispositivos de
  bloques** (nombres `diskio` sin una entrada `/sys/class/block`; rutas de dispositivo
  `hdparm`/`smart`) y **PSI del kernel**
  (una comprobación `pressure` en un kernel sin `/proc/pressure` — `CONFIG_PSI=n` —
  que de otro modo nunca se dispararía silenciosamente).
- **Locks** — archivos de lock malformados bajo `<paths.runtime>/locks`.
- **Slots de operación** — uso del daemon en ejecución (`info` cuando algunos slots están
  en uso, `warning` cuando están saturados); ver también `GET /api/ops`.

Rota y conserva `engine.diagnostics` con las herramientas de logs de tu host; Sermo no
poda ese archivo.

Para reclamar el historial antiguo de la base de datos de estado intencionadamente, usa:

```sh
sermoctl state compact                  # normal 366-day retention, then VACUUM
sermoctl state compact --before 720h    # prune history older than 30 days
sermoctl state compact --before 2026-01-01T00:00:00Z
```

`state compact` elimina las filas antiguas en buckets de SLA, mediciones, métricas de
daemon, métricas de runtime de service y eventos, luego hace checkpoint y vacía la base
de datos de estado SQLite de modo que las páginas liberadas puedan volver al sistema de
archivos. Sin `--before`, aplica la misma ventana de retención de 366 días (~1 año) que
`sermod` aplica al arrancar.
