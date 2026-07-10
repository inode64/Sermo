# Representación de la interfaz web

Este archivo es un mapa editable de la interfaz web. Úsalo para describir cambios de
disposición en Markdown sencillo; la implementación se encuentra en `internal/web/index.html`.

Mantén los cambios concretos:

- título del panel
- controles
- columnas
- expansión de fila
- acciones
- estados vacíos
- ordenación / agrupación

## Reglas globales

- La interfaz web es un único documento embebido: `internal/web/index.html`.
- Los paneles de datos son tarjetas `<details>`. La página se desplaza como un todo; no añadas
  barras de desplazamiento locales por panel.
- Todo panel de datos lleva `class="panel"` (los estilos compartidos, como el
  atenuado de desconexión, apuntan a esa clase y no a una lista de ids). Los
  `<details>` de paneles de watches llevan además `data-panel="<clave>"` con el
  nombre de su entrada en el registro `watchPanels`; el renderizado, el routing
  de deep-links, la navegación de atención y el atajo de búsqueda `/` iteran ese
  registro, de modo que un panel de watches nuevo solo necesita su entrada en el
  registro y su bloque HTML.
- Los servicios, aplicaciones y watches (incluidas las watches de storage) pueden
  usar `category` para agrupar, filtrar o buscar según el panel.
- Un campo YAML `category` de nivel superior es la fuente de la categoría. Si está ausente,
  los servicios recurren a `service`, las aplicaciones a `app`, las watches de
  storage a `storage` y el resto de watches a `watch`.
- Los botones que cambian de estado usan la misma ruta segura de backend que `sermoctl`.

## Fuentes de datos

| Área | Endpoint | Notas |
| --- | --- | --- |
| Usuario actual | `GET /api/whoami` | rol y permisos de acción |
| Snapshot del dashboard | `GET /api/dashboard?since=WINDOW` | agregado de los paneles de servicio/runtime que se refrescan con frecuencia; el navegador vuelve a los endpoints individuales si no está disponible |
| Disponibilidad | `GET /readyz?verbose` | `status:` del daemon en la barra superior (`starting` / `ok` / …) |
| Servicios | `GET /api/services` | servicios de runtime configurados cargados por sermod (no el inventario de catálogo de `sermoctl services`); `status_observed_at` identifica la muestra real de estado de init que hay detrás de una fila cacheada |
| Expansión de servicio | `GET /api/services/{name}` | checks, información del proceso, reglas |
| Métricas de check del servicio | `GET /api/services/{name}/metrics?check=NAME[&metric=KEY]` | gráfico de latencia cuando se omite `metric`; serie de métrica numérica con nombre cuando está presente |
| Métricas de runtime del servicio | `GET /api/services/{name}/runtime` | historial persistido de CPU/memoria/IO del servicio, de solo lectura y muestreado exclusivamente por ciclos del worker |
| SLA del servicio | `GET /api/services/{name}/sla` | historial de disponibilidad por minuto para la línea temporal de SLA del detalle del servicio y los clientes de la API |
| Eventos del servicio | `GET /api/services/{name}/events` | feed de eventos por servicio |
| Watches de host | `GET /api/watches` | watches a nivel de host |
| Aplicaciones | `GET /api/applications` | aplicaciones de catálogo instaladas; `observed_at` permanece fijo mientras el inventario de versión/estado se sirve desde caché |
| Unidades de montaje | `GET /api/mounts` | watches de storage con `mount:` respaldadas por fstab |
| Notifiers | `GET /api/notifiers` | destinos de notifiers |
| Configuración del daemon | `GET /api/daemon` | configuración de engine/runtime |
| Métricas de proceso del daemon | `GET /api/daemon/metrics` | historial persistido de CPU/memoria/IO de sermod, de solo lectura y muestreado por el daemon independientemente de los clientes web |
| Métricas de host | `GET /api/host` | valores actuales de CPU, memoria y carga del host |
| Locks | `GET /api/locks` | locks de runtime con nombre |
| Eventos | `GET /api/events` | actividad de servicios/watches; admite `limit`, `service`, `watch`, `kind`, `status`, `only_errors` |
| Actividad reciente | `GET /api/activity` | resumen de eventos recientes |
| Recuentos de monitorización | `GET /api/monitoring` | recuentos de servicios monitorizados frente a pausados |
| Operaciones en vivo | `GET /api/ops` | slots de operaciones activas |

Las cachés de estado de init, inspección de aplicaciones y líneas temporales de
SLA exponen sus horas de muestra reales. La UI muestra su antigüedad y las marcas
de los segmentos SLA permanecen ancladas a `observed_at`, en lugar de avanzar
con el reloj del navegador mientras están cacheadas.
Los refrescos son single-flight: las recargas automáticas, manuales y posteriores
a una acción nunca se ejecutan a la vez, y el siguiente intervalo automático
empieza cuando termina el refresco anterior.

## Endpoints de acción

Los endpoints que cambian de estado están protegidos por CSRF y son solo para administradores cuando
la autenticación web está habilitada.

| Área | Endpoint | Notas |
| --- | --- | --- |
| Acción de servicio | `POST /api/services/{name}/{action}[?no_cascade=1]` | `monitor`, `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume`; `reload` se ofrece solo cuando el servicio informa `can_reload` desde soporte de reload del backend de init o desde un fallback `reload:` válido; `no_cascade` omite los objetivos de `also_apply` en start/stop/restart |
| Preflight de servicio | `POST /api/services/{name}/preflight` | ejecuta los checks de preflight sin cambiar el estado del servicio |
| Acción de watch | `POST /api/watches/{name}/{action}` | `monitor`, `unmonitor`, `expand` |
| Acción de montaje | `POST /api/mounts/{name}/{action}[?kill=1]` | `mount`, `umount`, `blockers`, `alert`; `kill=1` habilita señalización de bloqueadores para `umount` solo si la política lo permite; `/` rechaza las rutas de desmontaje |
| Liberación de lock | `POST /api/locks/{service}/release?name=NAME` | libera locks con nombre inactivos, obsoletos o caducados; los locks activos se rechazan |
| Limpieza de eventos | `POST /api/events/clear?before=TIME` | borra las filas persistidas de eventos/actividad; `before` acepta una duración positiva o un timestamp RFC3339 no futuro |
| Compactación de estado | `POST /api/state/compact?before=TIME` | poda el historial antiguo de SLA/métricas/eventos y compacta la base de datos de estado; equivale a `sermoctl state compact` |
| Recarga del daemon | `POST /api/reload` | solicita una recarga de configuración de `sermod` |

## Barra superior

| Elemento | Representación actual |
| --- | --- |
| Marca | `Sermo` con punto de estado |
| Rol | etiqueta admin / solo lectura |
| Refresco | selector con intervalo de refresco, botón de refresco manual |
| Estado | antigüedad del último refresco completo, errores de conexión o lista de paneles que conservan datos anteriores tras un refresco parcial; `#statusbar` termina con el `uptime:` del host y luego el `status:` del daemon (`ok` / `starting` / …) como una cola emparejada |
| Estado del sistema | identidad del host, tipo de host, resumen de daemon/backend/runtime |

Notas editables:

- Mantén la barra superior compacta y fija.
- No muevas los controles operativos a bloques hero de estilo marketing.
- Los controles de refresco deben permanecer visibles en pantallas estrechas.
- La lectura `uptime:` de la línea de estado es el uptime del **host/servidor** (desde
  `/proc/uptime`, expuesto como `host_uptime` en `GET /api/daemon`), no el uptime del
  proceso sermod. El uptime del proceso sermod permanece en el panel del daemon y en
  `GET /livez?verbose`.
- El feedback de acciones (la línea de estado `#err`, ok/warn/err) permanece
  visible al menos ~5 segundos: el refresco del dashboard que dispara una acción
  completada no lo borra, de modo que un resultado como `umount failed: device
  busy` sigue siendo legible. Iniciar una nueva acción lo borra de inmediato, y
  el banner de desconexión queda exento — desaparece en el primer refresco
  exitoso.

## Tarjetas de resumen

Renderizadas por `renderOverview` a partir del estado ya cargado, sin solicitudes adicionales.

| Tipo de tarjeta | Contenido actual |
| --- | --- |
| Servicios activos | recuento / total para servicios en `started`, `collecting` o `monitored`; crítico cuando algún servicio está `failed`, aviso mientras algún servicio está `collecting`, neutral mientras algún objetivo se está asentando, en caso contrario activo; al hacer clic abre el filtro de servicios `failed`, `starting` o `collecting` cuando corresponde |
| Watches | recuento / total; crítico cuando algún watch está `failed`, neutral mientras algún objetivo se está asentando (el subtítulo nombra los watches, servicios o aplicaciones que están iniciando), en caso contrario silencioso; al hacer clic abre el filtro `starting`/`failed` correspondiente |
| Alertas | recuento de servicios en fallo, watches disparados, aplicaciones instaladas en fallo y locks activos, con un desglose por tipo; al hacer clic dirige a `failed-services`, `failed-watches`, `failed-apps` o `locks-section` por orden de prioridad |
| Monitorizado | servicios en estado `monitored` frente a servicios habilitados; aviso mientras haya servicios en `collecting`, neutral con subtítulo de asentamiento durante el arranque, al hacer clic abre el filtro de servicio relevante |
| Indicadores de host | memoria, carga, fds, pids, conntrack, etc. cuando están presentes |
| Volúmenes | un indicador por cada watch de almacenamiento montado, crítico cuando su watch está disparado |

Notas editables:

- Las tarjetas deben saltar al panel relacionado. Durante el asentamiento del arranque, las tarjetas
  Servicios activos y Watches abren el filtro `starting` en el panel que todavía tiene
  objetivos sin asentar (`starting-services`, `starting-watches` o `starting-apps`). Tras una
  recarga de configuración, la cabecera del daemon permanece en `ok` (sin favicon gris) incluso cuando
  algunos objetivos individuales siguen en `starting`.
- Las barras de uso permanecen en la parte inferior de cada tarjeta.
- No añadas texto explicativo dentro de las tarjetas.

## Atención requerida

| Elemento | Representación actual |
| --- | --- |
| Contenedor | visible solo cuando existen señales |
| Elementos | botones de advertencia / crítico |
| Comportamiento al hacer clic | abre el panel relacionado |

Las señales incluyen servicios en fallo, watches de host disparados, aplicaciones instaladas
en fallo, errores recientes y problemas de disponibilidad (incluido
`shutting_down`). Un elemento de servicios en fallo abre el panel de Servicios con el
filtro `failed`; un elemento de watches disparados abre Watches de host con el filtro
`failed` (objetivo `failed-watches`); un elemento de aplicaciones en fallo abre Aplicaciones
instaladas con el filtro `failed` (objetivo `failed-apps`). El progreso de arranque del daemon
permanece en la línea `status: starting` de la barra superior, no en este recuadro.

## Operaciones en vivo

| Elemento | Representación actual |
| --- | --- |
| Contenedor | visible mientras hay operaciones activas/recientes |
| Texto de slots | slots de operación en uso / total |
| Tarjetas | acción, servicio, estado, tiempo transcurrido, mensaje |

Local de la sesión para operaciones iniciadas desde el navegador actual; enriquecido con
`/api/ops` cuando está disponible.

## Panel de servicios

Section id: `services-section`

Lista las entradas de servicio **configuradas** desde la configuración cargada,
excluyendo contenedores Docker (`category: docker`) y máquinas virtuales
(`category: virtual-machine`), que se muestran en paneles propios. Esto no es
`sermoctl services`, que inventaría los perfiles de servicio del **catálogo**
bajo `catalog/services`. Consulta [cli.md](cli.es.md#catalog-inventory).

| Parte | Representación actual |
| --- | --- |
| Título | `Services` más el recuento total |
| Iconos del título | agrupar por categoría, contraer/expandir todos los grupos |
| Controles | búsqueda, selector de categoría, filtros de estado, recuento mostrado |
| Filtros de estado | all, disabled, stopped, started, starting, collecting, monitored, failed |
| Ordenación | Service, Category, State |
| Agrupación | filas de grupo por categoría, contraíbles |

Columnas:

| Columna | Significado |
| --- | --- |
| Service | nombre para mostrar, con fallback al nombre, capitalizado |
| Category | categoría YAML o fallback |
| State | estado de servicio normalizado único: `disabled`, `stopped`, `started`, `starting`, `collecting`, `monitored` o `failed` |
| Uptime | antigüedad del proceso de servicio más antiguo descubierto, cuando está disponible |
| CPU total | último uso de CPU de todo el árbol de procesos; vacío para servicios `no_resident_process` |
| Memory | última memoria residente del árbol de procesos; vacío para servicios `no_resident_process` |
| FDs | recuento de descriptores de archivo abiertos del árbol de procesos; vacío para servicios `no_resident_process` |
| IO R/W | bytes acumulados de lectura/escritura en disco del árbol de procesos; vacío para servicios `no_resident_process` |
| Actions | un botón start/stop según el estado, restart, reload, monitor/unmonitor; reload se desactiva cuando `can_reload` es false; el diálogo de confirmación de start/stop/restart ofrece **skip also_apply** cuando `also_apply` está definido |

## Paneles de contenedores y máquinas virtuales

Section ids: `containers-section`, `vms-section`

Los servicios de contenedores Docker y máquinas virtuales libvirt usan la misma
API de servicios y la misma expansión de fila que el panel Services, pero se
separan por categoría para el operador. Estos paneles mantienen la acción
`resume` porque los contenedores y VMs pausados pueden reanudarse mediante la
ruta de operación de servicios.

| Panel | Categoría origen | Acción extra |
| --- | --- | --- |
| Containers | `docker` | `resume` cuando el backend del contenedor informa `paused` |
| Virtual machines | `virtual-machine` | `resume` cuando el backend de VM informa `paused` |

## Expansión de fila de servicio

Compartida por los paneles Services, Containers y Virtual machines:

| Área | Contenido |
| --- | --- |
| Datos generales | estado, categoría, unidad/backend, uptime, intervalo, política, locks, último evento, próxima remediación, estado de remediación y totales del proceso; mientras la insignia de la fila sea `starting`, la expansión puede mostrar todavía el backend de init en bruto (`inactive`) y muestras de check en curso del ciclo de solo observación |
| Gráficos | línea temporal de SLA a ancho completo seguida de gráficos de latencia, CPU, memoria e IO; los servicios `no_resident_process` muestran solo SLA porque no tienen runtime de procesos para graficar |
| Procesos | tabla del árbol de procesos detectado a ancho completo, con los procesos hijos marcados en CMD y mantenidos bajo su padre; se omite cuando `no_resident_process` es true |
| Checks | checks configurados y resultado actual |
| Locks con nombre | estado de los locks de runtime |
| Reglas | estado de las reglas de remediación/alerta |
| Preflight | ejecutor de preflight en línea y resultados |
| Eventos | eventos de servicio retenidos recientes |

Las expansiones abiertas de servicio obtienen y renderizan por completo detalle
fresco una vez por refresco del dashboard; las subpeticiones de SLA, métricas,
runtime y eventos, además de los detalles abiertos de watches/aplicaciones,
deben terminar antes de adelantar `fully updated`. Los re-renders intermedios
(filtros, ordenación y operaciones en vivo) usan el detalle cacheado.

Estados vacíos:

- `No services.`
- `No services match the filter.`

## Panel de aplicaciones instaladas

Section id: `apps-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Installed applications` más el recuento total |
| Iconos del título | agrupar por categoría, contraer/expandir todos los grupos |
| Controles | búsqueda, selector de categoría, filtros de estado, recuento mostrado |
| Filtros de estado | all, ok, starting, warning, failed |
| Ordenación | Application, Category, Status, Version |
| Visibilidad | oculto cuando no se devuelven aplicaciones instaladas; las aplicaciones de catálogo sin un binario instalado nunca se listan y no muestran `starting` durante el asentamiento del daemon |
| Agrupación | filas de grupo por categoría, contraíbles |

Columnas:

| Columna | Significado |
| --- | --- |
| Application | nombre para mostrar, con fallback al nombre, capitalizado |
| Category | categoría YAML o fallback |
| Status | estado de inspección de la aplicación (`Ok`, `Starting` mientras el daemon se asienta, warning, failed) más la antigüedad de su sonda real |
| Version | versión corta, con fallback a la versión en bruto |

Expansión de fila:

| Campo | Significado |
| --- | --- |
| Version | salida completa de la versión |
| Version source | nombre de la aplicación proveedora cuando `version_from` suministró la versión |
| Category | categoría YAML o fallback |
| Location | ruta del binario resuelta |
| Permissions | cadena de modo |
| User | propietario del binario |
| Group | grupo del binario |
| Status | estado de inspección de la aplicación |

Estado vacío:

- `No applications match the filter.`

## Panel de unidades de montaje

Section id: `mounts-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Mount units` más el recuento total |
| Visibilidad | oculto cuando no se devuelven unidades de montaje configuradas |
| Controles | búsqueda por texto del mount, selector de grupo cuando hay más de uno, filtros de estado (`all`, `active`, `inactive`) |

Columnas:

| Columna | Significado |
| --- | --- |
| Name | nombre para mostrar, con fallback al nombre del mount |
| Group | etiqueta de categoría/grupo del mount |
| Path | ruta de montaje configurada |
| Mounted | estado de montaje en vivo |
| Refcount | refcount de runtime de Sermo, o `off` |
| Processes | lista compacta de procesos que usan actualmente la ruta de montaje |
| Users | usuarios únicos de esos procesos |
| State | insignia active/inactive/error |
| Actions | `mount` solo para admin; cuando está montado, `umount`, `alert` y `kill+umount`; `/` renderiza estos botones del flujo de desmontaje deshabilitados |

Todas las cabeceras salvo Actions son ordenables.
`GET /api/mounts` incluye un resumen read-only cacheado de blockers para la tabla.
Antes de `umount`, `alert` o `kill+umount`, la UI consulta
`POST /api/mounts/{name}/blockers` y muestra una lista fresca de procesos para la
ruta. `alert` envía un mensaje TTY nativo a los usuarios con sesión que bloquean
el montaje. `kill+umount` requiere que la política del mount marque al menos un
bloqueador actual como killable. Para `path: /`, `GET /api/mounts` devuelve
`can_umount: false`; la Web UI deshabilita los botones del flujo de desmontaje y
la API rechaza `umount?kill=1` sin escanear blockers ni enviar señales.

## Paneles de watches de host

Section ids: `storage-section`, `network-section`, `cert-section`,
`diskio-section`, `watches-section`

`Storage` contiene los watches de `storage`, `Network` contiene los watches `net`/`icmp`,
`Certificate watches` contiene los watches `cert`, `Disk I/O watches` contiene
los watches `diskio`, y `Host watches` contiene los tipos restantes de watch de host.

El resumen de un watch `storage` muestra la ruta, el sistema de archivos, el
punto de montaje y el espacio usado/libre, además del recuento de **archivos
abiertos** en ese sistema de archivos cuando existe (fds cuyo destino resuelve
bajo el montaje). Ese recuento viene de un escaneo `/proc/<pid>/fd` de todo el
host, compartido por todos los watches de storage y refrescado como máximo una
vez por minuto; es solo visual (sin umbral/alerta). La fila del listado de
servicios también muestra el recuento de descriptores abiertos (`fds`) de un
servicio en su propia columna, desde los mismos totales por proceso que ya
aparecen en el detalle del servicio.

| Parte | Representación actual |
| --- | --- |
| Título | nombre del panel más el recuento total del subconjunto de watches de ese panel |
| Controles | búsqueda, filtro de tipo (por panel, ver abajo), filtros de estado, recuento mostrado |
| Filtro de tipo | `all ... types` específico del panel más los valores distintos presentes actualmente en ese panel; Storage filtra por tipo de sistema de archivos (todos sus watches comparten un mismo tipo de check), Certificate watches por algoritmo de clave pública (el desplegable solo aparece con 3+ tipos de clave distintos), Disk I/O no tiene desplegable de tipo |
| Filtros de estado | all, disabled, ok, starting, failed |
| Búsqueda | display name, nombre crudo, categoría, tipo, resumen, intervalo, polaridad, estado/comando del hook, nombres de notifiers, estado de expand/dry-run/monitorización y condiciones |
| Ordenación | todas las cabeceras de columna excepto Actions son ordenables; Storage, Network, Certificate y Disk I/O ordenan por Name por defecto, Host watches mantiene el orden del servidor hasta que se pulsa una cabecera |
| Visibilidad | oculto cuando no hay watches configurados para el subconjunto de ese panel |

Columnas por panel (todos los paneles terminan con Last activity, State, Actions):

| Panel | Columnas específicas del panel |
| --- | --- |
| Storage | Name, Usage, Filesystem, Mount point |
| Network | Name, Type, Summary |
| Certificate watches | Name, Expires, Days left, Issuer, Key type |
| Disk I/O watches | Name, Device, Util%, Read / Write, Await |
| Host watches | Name, Type, Summary |

Las columnas de Certificate y Disk I/O leen las lecturas del watch publicadas
por el último ciclo de watches del daemon
(caducidad del certificado, días restantes, emisor y algoritmo de clave
pública; dispositivo, utilización, throughput de lectura/escritura y latencia
await).

Columnas compartidas:

| Columna | Significado |
| --- | --- |
| Name | nombre para mostrar, con fallback al nombre, capitalizado |
| Type | tipo de check |
| Summary | resumen de estado específico del watch |
| Last activity | última actividad de hook/notify |
| State | estado normalizado único del watch: `disabled` cuando config/monitor state lo excluye de comprobaciones activas, `starting` antes de la primera muestra monitorizada, `failed` para un fallo activo y `ok` en el resto |
| Actions | monitor/unmonitor y acciones admitidas |

Interval, polaridad (dispara en fallo / en umbral), hook y notifiers no son
columnas de la tabla; viven en la rejilla de config de la expansión de fila y
siguen siendo buscables.

Expansión de fila:

| Área | Contenido |
| --- | --- |
| Config | tipo, categoría, intervalo, dispara (en fallo / en umbral), estado, flag de monitorización, hook, notifiers, dry run |
| Readings | lecturas actuales del host, seguidas de las condiciones y umbrales del check |
| Activity | eventos recientes del watch |
| Expand | acción de expansión de almacenamiento cuando está configurada |

Estados vacíos:

- `No watches.`
- `No watches match the filter.`
- `No storage watches.`
- `No storage watches match the filter.`
- `No network watches.`
- `No network watches match the filter.`
- `No certificate watches.`
- `No certificate watches match the filter.`
- `No disk I/O watches.`
- `No disk I/O watches match the filter.`

## Panel de eventos

Section id: `events-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Events` más nota de eventos dry-run |
| Controles | service, watch, kind, status, only errors, acciones de grupo, restablecer filtros, corte `before` opcional, limpiar log (admin) |
| Tabla | filas de evento agrupadas por acción cuando está habilitado |
| Límite | últimos eventos coincidentes |

Notas editables:

- Los filtros de service/watch/kind/status se aplican a medida que el operador escribe (debounce de 300ms),
  igual que en los paneles de servicios y watches; Enter aplica inmediatamente, Escape
  o **restablecer filtros** limpia los campos de filtro. La casilla `only errors` vuelve
  a cargar al cambiar. La agrupación permanece en el cliente y es opcional; la cronología
  en bruto sigue siendo útil.
- **clear log** (solo admin) llama a `POST /api/events/clear` tras confirmación,
  igual que `sermoctl events clear`. Un campo opcional **before** pasa
  `?before=TIME` (duración positiva o RFC3339 no futuro) para podar solo las
  filas más antiguas.
- El filtro `kind` cubre los tipos de evento emitidos: `cycle`, `action`,
  `suppressed`, `alert`, `error`, `firing`, `recovered`, `dry-run`,
  `reload` (una recarga de configuración correcta del daemon en ejecución),
  `hook`/`hook-failed`, `notify`/`notify-failed`, `expand`/`expand-skipped`/`expand-failed`,
  y `cascade` (una operación de servicio activada mediante una acción en cascada).

## Panel de notifiers

Section id: `notifiers-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Notifiers` más el recuento total |
| Visibilidad | oculto cuando no hay notifiers configurados |
| Columnas | Name, Type, State |

Estado vacío:

- Panel oculto en lugar de una tabla vacía.

## Panel de configuración de daemon / engine

Section id: `daemon-section`

| Bloque | Campos |
| --- | --- |
| Daemon | Backend, Host type, Config, Runtime, State |
| Engine | Interval, Max parallel checks, Max parallel ops, Default timeout, Operation timeout, Startup delay |
| Runtime | Started, Uptime, Go version, Ready |
| Contadores de proceso | PID, CPU en vivo, memoria, IO, FDs, threads |
| Métricas de proceso | gráficos de CPU, memoria e IO con ventanas 1h/24h/7d/30d/1y |

Notas editables:

- Este panel es informativo. La recarga de configuración, **compact state** y el
  conmutador de **panic mode** están en el pie de página (solo admin).

### Modo pánico

El botón rojo **panic mode** del pie de página es el interruptor de emergencia de todo el daemon. Pide
confirmación (con un icono de advertencia) en ambos sentidos para que no se
active por accidente. Mientras el modo pánico está activo, el estado del daemon en la cabecera
muestra **`panic mode`** (rojo), aparece un banner bajo la cabecera, y el daemon
sigue monitorizando mientras suprime hooks, notificaciones de alerta y remediación
automática. El mismo conmutador está disponible desde la CLI como `sermoctl panic
on|off|status`. Consulta [cli.md](cli.es.md#panic-mode).

## Panel de actividad reciente

Section id: `activity-section`

| Campo | Significado |
| --- | --- |
| Service actions | recuento reciente de operaciones de servicio |
| Watch hooks | recuento reciente de hooks |
| Watch notifies | recuento reciente de notifiers |
| Errors | recuento reciente de errores |
| Last activity | resumen de la actividad más nueva |
| Actions | **clear log** (admin) — misma ruta `POST /api/events/clear` que el panel de eventos |

## Panel de locks de runtime

Section id: `locks-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Runtime Locks` más recuento |
| Visibilidad | oculto cuando no se devuelven locks |
| Acción de liberación | se muestra cuando el usuario puede actuar y el lock es liberable |

Columnas:

| Columna | Significado |
| --- | --- |
| Service | servicio bloqueado |
| Name | nombre del lock |
| State | active / stale / expired |
| TTL | TTL restante o configurado |
| Owner | información de PID/proceso del propietario |
| Created | hora de creación |
| Blocks | acciones bloqueadas |
| Reason | motivo suministrado por el operador |
| Action | botón de liberación cuando está permitido |

## Diálogo de confirmación de acción

Dialog id: `action-confirm`

| Parte | Representación actual |
| --- | --- |
| Cabecera | título de la acción y servicio |
| Cuerpo | advertencias de la acción, salida de preflight, contexto de lock/remediación |
| Pie | cancelar, ejecutar preflight, confirmar |

Nota de seguridad: este diálogo no debe eludir locks, guards, preflight ni los timeouts de
operación. Solo confirma acciones que siguen pasando por el motor de operaciones del backend.

## Plantilla de cambio

Copia esta sección al proponer un cambio en la interfaz web.

```markdown
## Proposed Web UI change

### Panel

Services / Host watches / Installed applications / Events / Notifiers /
Daemon settings / Recent activity / Runtime locks / Service detail /
Action dialog / Overview

### Title

Current:
Wanted:

### Controls

Current:
Wanted:

### Columns or fields

Keep:
Remove:
Add:
Rename:
Order:

### Grouping / sorting / filters

Current:
Wanted:

### Row expansion or detail view

Current:
Wanted:

### Actions

Current:
Wanted:
Safety notes:

### Empty states

Current:
Wanted:
```
