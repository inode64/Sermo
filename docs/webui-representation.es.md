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

`make web-e2e` valida esta representación en Chromium de escritorio y móvil,
incluyendo búsqueda global, acciones compactas por fila, estado de gráficas por
servicio, desbordamiento del viewport y reglas axe WCAG 2.2 AA contra fixtures
deterministas de la API.

## Reglas globales

- La interfaz web es un único documento embebido: `internal/web/index.html`.
- Los paneles de datos son tarjetas `<details>`. La página se desplaza como un todo; no añadas
  barras de desplazamiento locales por panel.
- Todo panel de datos lleva `class="panel"` (los estilos compartidos, como el
  atenuado de desconexión, apuntan a esa clase y no a una lista de ids). Los
  `<details>` de paneles de watches llevan además `data-panel="<clave>"` con el
  nombre de su entrada en el registro `watchPanels`; el renderizado, el routing
  de deep-links, la navegación de atención y el atajo de búsqueda `/` iteran ese
  registro. Los IDs estáticos, columnas, controles y textos proceden de
  `internal/web/src/watch-panels.json`, compartido por el builder Go del shell y
  el registro en runtime.
- Los servicios, contenedores, máquinas virtuales, aplicaciones, librerías y
  unidades de montaje agrupan por `category`; los paneles de watches agrupan por
  el tipo específico de cada panel.
- Un campo YAML `category` de nivel superior es la fuente de la categoría. Si está ausente,
  los servicios recurren a `service`, las aplicaciones a `app`, las watches de
  storage a `storage` y el resto de watches a `watch`.
- Los botones que cambian de estado usan la misma ruta segura de backend que `sermoctl`.
- Las marcas de tiempo se muestran en UTC, la convención canónica del daemon
  compartida con eventos y notificaciones; las marcas visibles en las vistas de
  eventos y actividad llevan la hora local del usuario como título al pasar el
  ratón.

## Fuentes de datos

| Área | Endpoint | Notas |
| --- | --- | --- |
| Usuario actual | `GET /api/whoami` | rol y permisos de acción; los controles de acción permanecen ocultos hasta que esta petición tiene éxito |
| Snapshot del dashboard | `GET /api/dashboard?since=WINDOW` | agregado de los paneles de servicio/runtime que se refrescan con frecuencia y provienen de una generación activa de configuración del daemon; incluye `generation` y las respuestas de datos del panel llevan `X-Sermo-Generation`, para que el navegador descarte una vista mezclada durante la recarga antes de renderizarla; el navegador vuelve a los endpoints individuales si no está disponible |
| Flujo de cambios | `GET /api/stream` | canal Server-Sent Events que empuja una señal `change` sin payload con cada evento del daemon; el dashboard refresca de inmediato y relaja su sondeo a una pasada lenta de reconciliación mientras está conectado, volviendo a la cadencia de sondeo configurada cuando el flujo no está disponible |
| Disponibilidad | `GET /readyz?verbose` | `status:` del daemon en la barra superior (`starting` / `ok` / …) |
| Servicios | `GET /api/services` | servicios de runtime configurados cargados por sermod (no el inventario de catálogo de `sermoctl services`); `status_observed_at` identifica la muestra real de estado de init que hay detrás de una fila cacheada |
| Expansión de servicio | `GET /api/services/{name}` | checks, información del proceso, reglas |
| Métricas de check del servicio | `GET /api/services/{name}/metrics?check=NAME[&metric=KEY]` | el detalle muestra la latencia cuando se omite `metric` y un gráfico por cada métrica numérica con nombre publicada por un check |
| Métricas de runtime del servicio | `GET /api/services/{name}/runtime` | historial persistido de CPU/memoria/IO del servicio, de solo lectura y muestreado exclusivamente por ciclos del worker; `current` es la última muestra publicada y las lecturas del panel nunca repiten el descubrimiento de procesos |
| SLA del servicio | `GET /api/services/{name}/sla` | historial de disponibilidad por minuto para la línea temporal de SLA del detalle del servicio y los clientes de la API; los ratios de SLA observado cuentan solo minutos monitorizados, así que el tiempo sin mediciones es un hueco, no caída |
| Eventos del servicio | `GET /api/services/{name}/events` | feed de eventos por servicio |
| Watches | `GET /api/watches` | watches de host y de service; `scope` los distingue y los nombres de watch de service usan `service:watch` |
| Aplicaciones | `GET /api/applications` | aplicaciones de catálogo instaladas; `observed_at` permanece fijo mientras el inventario de versión/estado se sirve desde caché |
| Librerías | `GET /api/libraries` | librerías de catálogo instaladas; `observed_at` permanece fijo mientras el inventario de fichero/versión se sirve desde caché |
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
SLA exponen sus horas de muestra reales, y las marcas de los segmentos SLA
permanecen ancladas a `observed_at`, en lugar de avanzar con el reloj del
navegador mientras están cacheadas.
Los refrescos son single-flight: las recargas automáticas, manuales y posteriores
a una acción nunca se ejecutan a la vez, y el siguiente intervalo automático
empieza cuando termina el refresco anterior.

## Endpoints de acción

Toda petición que cambia de estado (cualquier método distinto de `GET`) debe
llevar la cabecera `X-Sermo-Csrf: 1`; sin ella el servidor responde `403`. Esta
guarda CSRF se aplica de forma incondicional —también en modo abierto sin
autenticación—, así que un cliente de la API debe enviarla siempre. Con la
autenticación web habilitada, estos endpoints son además solo para
administradores. Las acciones con un objetivo concreto también llevan la
`X-Sermo-Generation` actual; el servidor mantiene esa generación del backend
durante la acción y no ejecuta nada si falta la cabecera (`428`) o quedó
obsoleta tras una recarga (`412`). La UI se refresca antes de un reintento
posterior. Los demás códigos de estado estables son `401` (desafío de
autenticación), `403` (falta la cabecera CSRF o un invitado intenta escribir),
`421` (`Host` rechazado en modo abierto), `404` (objetivo desconocido) y `200`
con un cuerpo `{"ok": bool, "message": string}` para una acción atendida.

| Área | Endpoint | Notas |
| --- | --- | --- |
| Acción de servicio | `POST /api/services/{name}/{action}[?no_cascade=1]` | `monitor`, `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume`; `reload` se ofrece solo cuando el servicio informa `can_reload` desde soporte de reload del backend de init o desde un fallback `reload:` válido; `no_cascade` omite los objetivos de `also_apply` en start/stop/restart |
| Preflight de servicio | `POST /api/services/{name}/preflight` | ejecuta los checks de preflight sin cambiar el estado del servicio |
| Acción de watch | `POST /api/watches/{name}/{action}` | `monitor`, `unmonitor`, `expand`, `probe` (una muestra manual), más `pause`/`resume` de RAID, que ejecutan una operación de re-chequeo y verificación y requieren la cabecera `X-Sermo-Confirm` |
| Prueba de notifier | `POST /api/notifiers/{name}/test` | envía una notificación de prueba por el notifier nombrado tras confirmación |
| Acción de montaje | `POST /api/mounts/{name}/{action}[?force=1&lazy=1&kill=1]` | `mount`, `umount`, `alert`; `force=1` permite `umount -f`, `lazy=1` permite `umount -l` como último fallback y `kill=1` habilita señalización de blockers limitada por `kill_only_if`; `/` rechaza las rutas de desmontaje |
| Blockers de montaje | `GET /api/mounts/{name}/blockers` | escaneo read-only fresco de blockers de una unidad de montaje; a los guests se les redactan las líneas de comando como en `GET /api/mounts` |
| Liberación de lock | `POST /api/locks/{service}/release?name=NAME` | libera locks con nombre inactivos, obsoletos o caducados; los locks activos se rechazan |
| Limpieza de eventos | `POST /api/events/clear?before=TIME` | borra las filas persistidas de eventos/actividad; `before` acepta una duración positiva o un timestamp RFC3339 no futuro |
| Compactación de estado | `POST /api/state/compact?before=TIME` | poda el historial antiguo de SLA/métricas/eventos y compacta la base de datos de estado; equivale a `sermoctl state compact` |
| Modo pánico | `POST /api/panic/{action}` | `on` / `off`; suspensión (solo admin) a nivel de daemon de hooks, alertas y remediación automática |
| Recarga del daemon | `POST /api/reload` | solicita una recarga de configuración de `sermod` |

## Barra superior

| Elemento | Representación actual |
| --- | --- |
| Marca | `Sermo` con punto de estado |
| Rol | etiqueta admin / solo lectura |
| Buscar target | autocompletado único sobre services, watches, aplicaciones y mounts cargados; la selección limpia solo los filtros de ese panel y abre el target |
| Refresco | selector con intervalo de refresco, botón de refresco manual |
| Notificaciones | campana de notificaciones del navegador (opt-in); con el permiso concedido, los objetivos que empiezan a fallar generan una única notificación agrupada mientras la pestaña está oculta |
| Estado | antigüedad del último refresco completo, errores de conexión o lista de paneles que conservan datos anteriores tras un refresco parcial; `#statusbar` termina con el `uptime:` del host y luego el `status:` del daemon (`ok` / `starting` / …) como una cola emparejada |
| Estado del sistema | identidad del host, tipo de host, resumen de daemon/backend/runtime |

Notas editables:

- Mantén la barra superior compacta y fija.
- No muevas los controles operativos a bloques hero de estilo marketing.
- Los controles de refresco deben permanecer visibles en pantallas estrechas.
- `Ctrl+K`/`Cmd+K` enfoca la búsqueda global de targets. Usa el snapshot actual
  del dashboard y no realiza otra petición.
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

Las señales incluyen servicios en fallo, watches disparados, aplicaciones instaladas
en fallo, errores recientes y problemas de disponibilidad (incluido
`shutting_down`). Un elemento de servicios en fallo abre el panel de Servicios con el
filtro `failed`; un elemento de watches disparados abre Watches con el filtro
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
bajo `catalog/services`. Consulta [cli.md](cli.es.md#inventario-de-catálogo).

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
| Actions | botones icono compactos e individuales para start/stop, restart, reload, resume y monitor/unmonitor; reload se desactiva cuando `can_reload` es false; el diálogo de confirmación de start/stop/restart ofrece **skip also_apply** cuando `also_apply` está definido |
| Fijar | una estrella por fila sube los servicios elegidos a lo alto del panel (y de su grupo), persistida localmente con el resto del estado de la UI |

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

Ambos paneles exponen los mismos controles de agrupación y plegado por categoría que Services.

## Expansión de fila de servicio

Compartida por los paneles Services, Containers y Virtual machines:

| Área | Contenido |
| --- | --- |
| Datos generales | estado, categoría, unidad/backend, uptime, intervalo, política, locks, último evento, próxima remediación, estado de remediación y totales del proceso; mientras la insignia de la fila sea `starting`, la expansión puede mostrar todavía el backend de init en bruto (`inactive`) y muestras de check en curso del ciclo de solo observación |
| Gráficos | línea temporal de SLA a ancho completo seguida de gráficos de latencia, CPU, memoria e IO; cada servicio persiste su propia ventana temporal y check de latencia; los servicios `no_resident_process` muestran solo SLA porque no tienen runtime de procesos para graficar |
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
(filtros, ordenación y operaciones en vivo) usan el detalle cacheado. Una
respuesta tardía de una selección de gráfica anterior se ignora en lugar de
sobrescribir las gráficas actuales del servicio.

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

## Panel de librerías instaladas

Section id: `libraries-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Installed libraries` más el recuento total |
| Iconos del título | agrupar por categoría, contraer/expandir todos los grupos |
| Controles | búsqueda, selector de categoría, filtros de estado, recuento mostrado |
| Filtros de estado | all, ok, warning, failed |
| Ordenación | Library, Category, Status, Version |
| Visibilidad | oculto cuando no se devuelve ningún fichero de librería instalado |
| Agrupación | filas de grupo por categoría, contraíbles |

Columnas: Library (nombre para mostrar), Category, Status (estado de inspección y
antigüedad de la sonda) y Version (versión corta cuando está disponible). Al expandir
una fila se muestran origen de versión, ubicación del fichero, permisos, usuario,
grupo y estado completo. Las librerías no muestran SLA ni eventos de aplicación.

Estado vacío:

- `No libraries match the filter.`

## Panel de unidades de montaje

Section id: `mounts-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Mount units` más el recuento total |
| Visibilidad | oculto cuando no se devuelven unidades de montaje configuradas |
| Iconos del título | agrupar por grupo del mount, contraer/expandir todos los grupos (ocultos cuando solo hay un grupo) |
| Controles | búsqueda por texto del mount, selector de grupo cuando hay más de uno, filtros de estado (`all`, `active`, `inactive`) |
| Agrupación | filas plegables por grupo del mount |

Columnas:

| Columna | Significado |
| --- | --- |
| Name | nombre para mostrar, con fallback al nombre del mount |
| Group | etiqueta de categoría/grupo del mount |
| Path | ruta de montaje configurada; añade `mounting` o `unmounting` mientras una operación está en curso |
| Mounted | estado de montaje en vivo |
| Refcount | refcount de runtime de Sermo, o `off` |
| Processes | lista compacta de procesos que usan actualmente la ruta de montaje |
| Users | usuarios únicos de esos procesos |
| State | insignia active/inactive/error, o `mounting`/`unmounting` mientras una operación está en curso |
| Actions | icono compacto mount/umount solo para admin más alert; las filas montadas abren un único diálogo de umount con opciones force/lazy/kill-blockers; los botones de esa fila se deshabilitan mientras una operación de montaje está en curso; `/` renderiza este flujo de desmontaje deshabilitado |

Todas las cabeceras salvo Actions son ordenables.
`GET /api/mounts` incluye un resumen read-only cacheado de blockers para la tabla
y un objeto `operation` opcional (`action`, `state`, `started_at`, `message`) cuando
el daemon está montando o desmontando esa unidad.
Antes de `umount` o `alert`, la UI consulta `GET /api/mounts/{name}/blockers` y
muestra una lista fresca de procesos para la ruta. El diálogo de umount muestra
siempre la tabla de blockers; `kill blockers` solo se habilita cuando
`has_kill_policy` y `can_kill` son true, y solo las filas marcadas como
`killable` pueden señalizarse. `alert` envía un mensaje TTY nativo a los
usuarios con sesión que bloquean el montaje. Para `path: /`, `GET /api/mounts`
devuelve `can_umount: false`; la Web UI deshabilita los botones del flujo de
desmontaje y la API rechaza `umount?kill=1` sin escanear blockers ni enviar
señales.

## Panel de watches

Section id: `watches-section`

`Watches` contiene tanto watches de host como watches de service. Cada fila
muestra su `scope`; los nombres de watches de service usan `service:watch` y se
ejecutan como parte del worker de ese service, no de forma independiente como
los watches de host.

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
| Iconos del título | agrupar por tipo del panel, contraer/expandir todos los grupos de tipo (ocultos cuando solo hay un grupo) |
| Controles | búsqueda, filtro de tipo (por panel, ver abajo), filtros de estado, recuento mostrado |
| Filtro de tipo | `all ... types` específico del panel más los valores distintos presentes actualmente en ese panel; Storage filtra por tipo de sistema de archivos (todos sus watches comparten un mismo tipo de check), Certificate watches por algoritmo de clave pública; el selector se oculta cuando solo hay un valor |
| Agrupación | filas plegables por el mismo tipo específico del panel usado por el filtro de tipo |
| Filtros de estado | all, disabled, ok, starting, failed |
| Búsqueda | display name, nombre crudo, categoría, tipo, resumen, intervalo, polaridad, estado/comando del hook, nombres de notifiers, estado de expand/dry-run/monitorización y condiciones |
| Ordenación | cada columna de datos salvo Actions es ordenable de forma independiente dentro de su tabla de tipo; cada tabla empieza por Name ascendente |
| Visibilidad | oculto cuando no hay watches configurados para el subconjunto de ese panel |

Los watches se agrupan en System, Storage, Network y Security y después se
dividen en una tabla por tipo de check. Cada tabla termina en Last checked, Last
activity, State y Actions; no usa una columna genérica Summary. Last checked es
la última muestra completada por el ciclo del daemon o manual, mientras que Last
activity es un evento.

| Tipo de check | Columnas específicas |
| --- | --- |
| `storage` | Name, Usage, Filesystem, Mount point; filtra por filesystem si hay más de uno |
| `file` | Name, Path, edad actual, límite de edad configurado |
| `net` | Name, interfaz, enlace, velocidad, errores |
| `hdparm` | Name, dispositivo, lectura buffered, lectura cached |
| `lvm` | Name, salud, VG, LV, tamaño de VG, libre en VG, motivos |
| `smart` | Name, dispositivo, salud, temperatura, desgaste, tiempo encendido formateado |
| `diskio` | Name, dispositivo, utilización, lectura, escritura, await |
| `cert` | Name, origen, días restantes, caducidad, emisor |
| `raid` | Name, array, tamaño, degradado, recuperando |
| Otros tipos | Name y su valor vivo principal |

Estas columnas leen las lecturas actuales publicadas por el último ciclo del
daemon y rehidratadas desde estado persistente tras reiniciar el daemon. La edad
de file es el valor ya formateado que usa `older_than`; un `summary` configurado
del check sustituye las columnas de edad y límite por Summary; las comprobaciones SQL de
servicio exponen el escalar observado como `Value` y la comparación efectiva
como `Condition`, por lo que un resultado como `51 > 50` se ve sin analizar el
texto de eventos.

Columnas compartidas:

| Columna | Significado |
| --- | --- |
| Name | nombre para mostrar, con fallback al nombre, capitalizado |
| Last checked | última muestra completada por el ciclo del daemon o manual |
| Last activity | último evento del watch, como un probe manual, notificación o remediación |
| State | estado normalizado del watch: `disabled` cuando config/monitor state lo excluye de comprobaciones activas, `starting` antes de la primera muestra monitorizada, `failed` para un fallo activo y `ok` en el resto; el trabajo activo del dispositivo tiene prioridad como `testing`, `recovering`, `rebuilding`, `repairing`, `moving` o `merging` |
| Actions | acción principal admitida y menú adicional para monitor/unmonitor |

Mientras se ejecuta una muestra manual de `hdparm`, `lvm`, `raid` o `smart`,
State muestra la etiqueta ámbar **checking**, el tiempo transcurrido y el estado
de salud previo. La acción queda desactivada hasta terminar. Events registra el
inicio y el resultado final con su duración. La UI sólo muestra porcentaje cuando
el check aporta progreso real; una sonda sin esa fuente usa el contador de tiempo
en vez de un porcentaje inventado.

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
| Controles | selectores guiados de service, watch, kind, status y rango temporal; selectores absolutos de fecha/hora desde/hasta; only errors, agrupar acciones opcional, restablecer filtros, corte `before` opcional, limpiar log (admin) |
| Tabla | filas cronológicas por defecto; agrupación opcional en cliente por acción |
| Límite | últimos eventos coincidentes; **load older** continúa con un cursor de ID estable |

Notas editables:

- Las opciones de service/watch siguen los targets conocidos y kind/status usan
  el vocabulario de eventos del daemon. Los rangos temporales solicitan `since`
  al backend. Los selectores absolutos desde/hasta (hora local) aplican sus
  límites exactos en el cliente; un "desde" definido acota además la petición
  al servidor, ya que el `since` de la API solo acepta duraciones. Escape o
  **restablecer filtros** limpia todos los filtros. La
  casilla `only errors` vuelve a cargar al cambiar. La agrupación permanece en
  el cliente, es opcional y está desactivada por defecto; la cronología en bruto
  es la vista predeterminada.
- El estado de expansión usa el ID persistido del evento. Cargar filas más
  antiguas añade una página por cursor sin duplicar eventos ni desplazar las
  filas abiertas.
- **clear log** (solo admin) llama a `POST /api/events/clear` tras confirmación,
  igual que `sermoctl events clear`. Un campo opcional **before** pasa
  `?before=TIME` (duración positiva o RFC3339 no futuro) para podar solo las
  filas más antiguas.
- El filtro `kind` cubre los tipos de evento emitidos: `action`, `suppressed`,
  `panic-suppressed`, `alert`, `error`, `firing`, `recovered`, `dry-run`,
  `reload` (una recarga de configuración correcta del daemon en ejecución),
  `hook`/`hook-failed`, `notify`/`notify-failed`/`notify-suppressed`,
  `expand`/`expand-skipped`/`expand-failed`, `kill`/`kill-failed`, y `cascade`
  (una operación de servicio activada mediante una acción en cascada).

## Panel de notifiers

Section id: `notifiers-section`

| Parte | Representación actual |
| --- | --- |
| Título | `Notifiers` más el recuento total |
| Visibilidad | oculto cuando no hay notifiers configurados |
| Columnas | Name, Type, Destination, Watches, State, Actions |
| Acciones | Un administrador puede enviar un mensaje claramente marcado como prueba por un notifier habilitado. |

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
on|off|status`. Consulta [cli.md](cli.es.md#modo-pánico).

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

Services / Watches / Installed applications / Installed libraries / Events / Notifiers /
Daemon settings / Runtime locks / Service detail /
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
