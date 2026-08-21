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

## Contenido

- [Reglas globales](#reglas-globales)
- [Fuentes de datos](#fuentes-de-datos)
- [Tira temporal de SLA](#tira-temporal-de-sla)
- [Endpoints de acción](#endpoints-de-acción)
- [Barra superior](#barra-superior)
- [Tarjetas de resumen](#tarjetas-de-resumen)
- [Atención requerida](#atención-requerida)
- [Operaciones en vivo](#operaciones-en-vivo)
- [Panel de servicios](#panel-de-servicios)
- [Paneles de contenedores y máquinas virtuales](#paneles-de-contenedores-y-máquinas-virtuales)
- [Expansión de fila de servicio](#expansión-de-fila-de-servicio)
- [Panel de sesiones](#panel-de-sesiones)
- [Panel de aplicaciones instaladas](#panel-de-aplicaciones-instaladas)
- [Panel de librerías instaladas](#panel-de-librerías-instaladas)
- [Panel de unidades de montaje](#panel-de-unidades-de-montaje)
- [Panel de watches](#panel-de-watches)
- [Panel de eventos](#panel-de-eventos)
- [Panel de notifiers](#panel-de-notifiers)
- [Panel de configuración de daemon / engine](#panel-de-configuración-de-daemon--engine)
  - [Modo pánico](#modo-pánico)
- [Panel de locks de runtime](#panel-de-locks-de-runtime)
- [Diálogo de confirmación de acción](#diálogo-de-confirmación-de-acción)
- [Plantilla de cambio](#plantilla-de-cambio)

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
| Flujo de cambios | `GET /api/stream` | canal Server-Sent Events que empuja una señal `change` sin payload con cada evento del daemon; el dashboard refresca de inmediato. Solo añade refrescos: el sondeo programado mantiene siempre la cadencia elegida en la barra superior, porque nada se empuja cuando cambia una muestra de métricas y las lecturas de host, servicios y watches dependen de ese sondeo |
| Disponibilidad | `GET /readyz?verbose` | `status:` del daemon en la barra superior (`starting` / `ok` / …) |
| Servicios | `GET /api/services` | servicios de runtime configurados cargados por sermod (no el inventario de catálogo de `sermoctl services`); `status_observed_at` identifica la muestra real de estado de init que hay detrás de una fila cacheada; `operation_active` es true mientras el motor mantiene el lock de operación del servicio, de modo que una acción lanzada desde cualquier cliente, `sermoctl` o la remediación automática se ve en curso y sus botones de acción siguen deshabilitados |
| Sesiones | `GET /api/sessions` | inventario global de SSH, tmux y screen; cada origen configurado presente informa `available`, `collecting` o `unavailable`; un servidor tmux disponible sin sesiones aparece como `empty`, mientras que un espacio tmux/screen ausente se omite; SSH usa la caché breve compartida del muestreador y tmux/screen solo leen muestras de `terminal_sessions` publicadas por el daemon |
| Expansión de servicio | `GET /api/services/{name}` | checks, información del proceso, reglas |
| Métricas de check del servicio | `GET /api/services/{name}/metrics?check=NAME[&metric=KEY]` | el detalle muestra la latencia cuando se omite `metric` y un gráfico por cada métrica numérica con nombre publicada por un check |
| Métricas de runtime del servicio | `GET /api/services/{name}/runtime` | historial persistido de CPU/memoria/IO del servicio, de solo lectura y muestreado exclusivamente por ciclos del worker; `current` es la última muestra publicada y las lecturas del panel nunca repiten el descubrimiento de procesos |
| SLA del servicio | `GET /api/services/{name}/sla[?check=NAME]` | historial de disponibilidad para la línea temporal de SLA del detalle del servicio, para la expansión de una aplicación que mapea a este servicio y para los clientes de la API, a la resolución a la que esa ventana está almacenada; `check` lo acota a uno de los checks del servicio, que es de donde la tabla de checks saca su tira, así que ambos ámbitos comparten una sola serie y un solo selector de ventana; un check que no emite veredicto no sirve puntos; los ratios de SLA observado cuentan solo minutos monitorizados, así que el tiempo sin mediciones es un hueco, no caída; cada punto lleva además `down_buckets`, los buckets de un minuto dentro de él que vieron un fallo |
| Eventos del servicio | `GET /api/services/{name}/events` | feed de eventos por servicio |
| Watches | `GET /api/watches` | watches de host y de service; `scope` los distingue y los nombres de watch de service usan `service:watch` |
| SLA de watch | `GET /api/watches/{name}/sla` | el mismo historial de disponibilidad que sirve la ruta de SLA de servicio, para una vigilancia de host cuya comprobación afirma disponibilidad; ambas comparten una sola ruta de serie, así que el uptime de una vigilancia se calcula exactamente igual que el de un servicio; una vigilancia que no guarda ninguna responde 404 en vez de una serie vacía que se leería como uptime medido |
| Aplicaciones | `GET /api/applications` | aplicaciones de catálogo instaladas; `observed_at` permanece fijo mientras el inventario de versión/estado se sirve desde caché |
| Librerías | `GET /api/libraries` | librerías de catálogo instaladas; `observed_at` permanece fijo mientras el inventario de fichero/versión se sirve desde caché |
| Unidades de montaje | `GET /api/mounts` | watches de storage con `mount:` respaldadas por fstab |
| Notifiers | `GET /api/notifiers` | destinos de notifiers |
| Configuración del daemon | `GET /api/daemon` | configuración de engine/runtime |
| Métricas de proceso del daemon | `GET /api/daemon/metrics` | historial persistido de CPU/memoria/IO de sermod, de solo lectura y muestreado por el daemon independientemente de los clientes web |
| Métricas de host | `GET /api/host` | valores actuales de CPU, memoria y carga del host |
| Locks | `GET /api/locks` | locks de runtime con nombre |
| Eventos | `GET /api/events` | página por cursor de actividad de servicios/watches; admite `limit`, `service`, `watch`, `kind`, `status`, `only_errors` |
| Actividad reciente | `GET /api/activity` | resumen de eventos recientes |
| Recuentos de monitorización | `GET /api/monitoring` | recuentos de servicios monitorizados frente a pausados |

Las cachés de estado de init e inspección de aplicaciones exponen sus horas de
muestra reales. La disponibilidad no se cachea así en absoluto: cada panel pide
la ventana en la que está y cada punto lleva el inicio de su propio bucket, así
que nada avanza con el reloj del navegador.
Los refrescos son single-flight: las recargas automáticas, manuales y posteriores
a una acción nunca se ejecutan a la vez, y el siguiente intervalo automático
empieza cuando termina el refresco anterior.

## Tira temporal de SLA

Las tiras de disponibilidad y la gráfica de SLA del detalle del servicio colorean
cada celda por **cuánto de ella estuvo caído**, no por su disponibilidad. El
historial almacenado guarda un intervalo de bucket por ventana ([Resolución del
historial almacenado](configuration.es.md#resolución-del-historial-almacenado)),
así que en las ventanas anchas una celda cubre horas o un día entero: un corte de
40 segundos dentro de una celda de un día es un 99.93% de disponibilidad, que
cualquier umbral de disponibilidad lee como sano. Colorear por la fracción caída en
su lugar mantiene ese corte visible.

Cinco bandas, con el verde reservado a exactamente cero — ninguna celda que
contenga un fallo puede leerse como sana, por poco de la celda que haya afectado:

| Fracción caída de la celda | Color |
|---|---|
| 0% | verde (`.sla-down-none`) |
| hasta 25% | ámbar (`.sla-down-low`) |
| hasta 50% | naranja (`.sla-down-mid`, mezcla `--warn`/`--crit`) |
| hasta 75% | rojo anaranjado (`.sla-down-high`, mezcla `--warn`/`--crit`) |
| hasta 100% | rojo (`.sla-down-full`) |

La banda dice cuánto de la celda se vio afectado, que es lo que separa un blip
breve de un corte de medio día. Una celda sin ninguna observación sigue siendo un
`.sla-gap` rayado, distinto de ambos: un hueco es tiempo sin monitorizar, no una
caída.

La disponibilidad la dibuja exactamente un panel dondequiera que aparezca. Una
vigilancia de host cuya comprobación afirma disponibilidad, y una aplicación que
mapea a un servicio monitorizado, reciben cada una **la propia sección de SLA del
detalle de servicio** — el mismo selector de ventana `1h / 24h / 7d / 30d / 1y`,
la misma línea de resumen y la misma línea temporal de `drawSLAChart` — y no una
segunda presentación, más pequeña, del mismo número. Solo cambia la serie: una
vigilancia lee `GET /api/watches/{name}/sla?since=`, una aplicación lee la de su
servicio, `GET /api/services/{name}/sla?since=`, porque su disponibilidad *es* la
de ese servicio. Una vigilancia de condición reporta `keeps_sla: false`, igual
que una aplicación sin servicio monitorizado detrás, y ninguna dibuja sección
alguna — alcanzar un umbral no es downtime, y un binario sin instalar no tiene
uptime.

El color nunca es el único portador de esto (WCAG 2.2 1.4.1): el `title` y el
`aria-label` de cada celda indican la disponibilidad, la fracción caída y cuántos
buckets de un minuto dentro de ella vieron un fallo, y la tabla de datos oculta
visualmente junto a cada tira los repite por sub-intervalo en una columna
`Affected`.

Los recuentos de incidentes vienen de esos buckets de un minuto en lugar del número
de puntos con fallos, así que tres minutos malos separados dentro de un mismo bucket
consolidado se informan como tres minutos afectados, no como uno.

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
| Acción de servicio | `POST /api/services/{name}/{action}[?no_cascade=1]` | `monitor`, `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume`, `repair`; `restart` es la acción principal para servicios failed/inactive, mientras `repair` es una alternativa secundaria solo manual que usa la recuperación segura de pidfile obsoleto y estado fallido de init antes de arrancar; `reload` se ofrece solo cuando el servicio informa `can_reload` desde soporte de reload del backend de init o desde un fallback `reload:` válido; `no_cascade` omite los objetivos de `also_apply` en start/stop/restart |
| Preflight de servicio | `POST /api/services/{name}/preflight` | ejecuta los checks de preflight sin cambiar el estado del servicio |
| Cerrar sesión SSH | `POST /api/services/{name}/sessions/{pid}/close?start_ticks=TICKS&terminal=PTS` | solo admin y con confirmación: cierre elegante de un terminal SSH mostrado; el backend redescubre el terminal, el ejecutable `sshd` configurado exacto y su usuario real, exige el mismo PID y ticks de inicio y solo envía `SIGTERM` |
| Cerrar sesión de terminal | `POST /api/services/{name}/terminal-sessions/{check}/close?multiplexer=TYPE&session=NAME&user=USER&identity=IDENTITY` | solo admin y con confirmación: cierre de una sesión tmux/screen; el backend vuelve a listar el espacio configurado de usuario/socket, exige la misma identidad de generación y ejecuta únicamente el argv exacto de cierre del cliente |
| Cerrar servidor tmux vacío | `POST /api/services/{name}/terminal-sessions/{check}/close-empty` | solo admin y con confirmación; solo aparece para un origen tmux presente, vacío y con socket explícito configurado. El backend confirma que sigue vacío, ejecuta el argv exacto `kill-server` de tmux, verifica que el espacio desapareció y elimina solo el socket huérfano sin cambios que pueda quedar |
| Acción de watch | `POST /api/watches/{name}/{action}` | `monitor`, `unmonitor`, `expand`, `probe` (una muestra manual), más `pause`/`resume` de RAID, que ejecutan una operación de re-chequeo y verificación y requieren la cabecera `X-Sermo-Confirm` |
| Prueba de notifier | `POST /api/notifiers/{name}/test` | envía una notificación de prueba por el notifier nombrado tras confirmación |
| Acción de montaje | `POST /api/mounts/{name}/{action}[?force=1&lazy=1&kill=1]` | `mount`, `umount`, `alert`; `force=1` permite `umount -f`, `lazy=1` permite `umount -l` como último fallback y `kill=1` habilita señalización de blockers limitada por `kill_only_if`; `/` rechaza las rutas de desmontaje |
| Blockers de montaje | `GET /api/mounts/{name}/blockers` | escaneo read-only fresco de blockers de una unidad de montaje; a los guests se les redactan las líneas de comando como en `GET /api/mounts` |
| Liberación de lock | `POST /api/locks/{service}/release?name=NAME` | libera locks con nombre inactivos, obsoletos o caducados; los locks activos se rechazan |
| Limpieza de eventos | `POST /api/events/clear?before=TIME` | borra las filas persistidas de eventos/actividad; `before` acepta una duración positiva o un timestamp RFC3339 no futuro |
| Compactación de estado | `POST /api/state/compact?before=TIME` | consolida y poda el historial almacenado a la retención configurada, luego compacta la base de datos de estado; `before` opcionalmente descarta lo que quede más antiguo que un corte explícito; equivale a `sermoctl state compact` |
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
| Sesiones | cuando se puede atribuir con seguridad un servicio SSH configurado, `sessions (console/SSH): X/Z` es el número de terminales locales y SSH con login; reemplaza el recuento anterior de usuarios distintos, así que tres terminales de la misma cuenta se ven como `0/3`, no como `1` |
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
| Servicios activos | recuento / total para servicios en `started`, `collecting`, `warning` o `monitored`; crítico cuando algún servicio está `failed`, aviso mientras algún servicio está `collecting` o `warning`, neutral mientras algún objetivo se está asentando, en caso contrario activo; al hacer clic abre el filtro de servicios `failed`, `starting`, `collecting` o `warning` cuando corresponde |
| Watches | recuento / total; crítico cuando algún watch está `failed`, neutral mientras algún objetivo se está asentando (el subtítulo nombra los watches, servicios o aplicaciones que están iniciando), en caso contrario silencioso; al hacer clic abre el filtro `starting`/`failed` correspondiente |
| Alertas | recuento de servicios en fallo, watches disparados, aplicaciones instaladas en fallo y locks activos, con un desglose por tipo; al hacer clic dirige a `failed-services`, `failed-watches`, `failed-apps` o `locks-section` por orden de prioridad |
| Monitorizado | servicios en estado `monitored` frente a servicios habilitados; aviso mientras haya servicios en `collecting` o `warning` (el subtítulo nombra los que requieren atención), neutral con subtítulo de asentamiento durante el arranque, al hacer clic abre el filtro de servicio relevante |
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
| Tarjetas | acción, servicio, estado, tiempo transcurrido, mensaje |

Local de la sesión para operaciones iniciadas desde el navegador actual.

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
| Filtros de estado | all, disabled, stopped, started, starting, collecting, monitored, warning, failed |
| Ordenación | Service, Category, State |
| Agrupación | filas de grupo por categoría, contraíbles |

Columnas:

| Columna | Significado |
| --- | --- |
| Service | nombre para mostrar, con fallback al nombre, capitalizado |
| Category | categoría YAML o fallback |
| State | estado de servicio normalizado único: `disabled`, `stopped`, `started`, `starting`, `collecting`, `monitored`, `warning` o `failed`; `warning` marca un servicio sano sin árbol de procesos atribuible o una carga con unidad de init fallida pero proceso vivo verificado y comprobaciones funcionales correctas; su razón en línea distingue los casos |
| Uptime | antigüedad del proceso de servicio más antiguo descubierto, cuando está disponible |
| CPU total | último uso de CPU de todo el árbol de procesos; vacío para servicios `no_resident_process` |
| Memory | última memoria residente del árbol de procesos; vacío para servicios `no_resident_process` |
| FDs | recuento de descriptores de archivo abiertos del árbol de procesos; vacío para servicios `no_resident_process` |
| IO R/W | bytes acumulados de lectura/escritura en disco del árbol de procesos; vacío para servicios `no_resident_process` |
| Strays | cuenta de miembros del control group que ningún selector reclama, del snapshot publicado del check `strays`; un guion cuando no hay ninguno. Si no es cero lleva un botón de reap para administradores — confirmado, y controlado en el servidor por el `reap.kill_only_if` del propio servicio. Se retira por debajo de 640px, donde la cuenta y su botón pasan a la expansión |
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
| Datos generales | una cuadrícula sin encabezado, primera área de la expansión: nombre, estado, categoría, unidad/backend, uptime, intervalo, política, locks, último evento, próxima remediación, estado de remediación y totales del proceso; mientras la insignia de la fila sea `starting`, la expansión puede mostrar todavía el backend de init en bruto (`inactive`) y muestras de check en curso del ciclo de solo observación |
| Gráficos | línea temporal de SLA a ancho completo seguida de gráficos de latencia, CPU, memoria e IO; cada servicio persiste su propia ventana temporal y check de latencia; los servicios `no_resident_process` muestran solo SLA porque no tienen runtime de procesos para graficar |
| Procesos | tabla del árbol de procesos detectado a ancho completo, con los procesos hijos marcados en CMD y mantenidos bajo su padre; **Max core** sigue a CPU e informa del uso máximo que ese proceso hizo de un solo core —su hilo más ocupado—, cuyo tooltip indica si el daemon lo midió por hilo o lo acotó con la tasa del proceso; la celda **Role** muestra `stray` para un miembro del control group que ningún selector reclama, en lugar del engañoso `main` de la semilla del backend; las advertencias de descubrimiento se listan encima, una por línea; se omite cuando `no_resident_process` es true |
| Checks | checks configurados y resultado actual; la columna SLA lleva la misma banda de disponibilidad que dibuja la línea temporal de SLA de Gráficas, sobre la ventana en la que esté el selector de esa sección, así que un tramo sin observar se ve como hueco rayado en ambas en vez de como un porcentaje plano en una |
| Locks con nombre | estado de los locks de runtime |
| Reglas | estado de las reglas de remediación/alerta |
| Preflight | ejecutor de preflight en línea y resultados |
| Eventos | eventos de servicio retenidos recientes |

La expansión complementa la fila en vez de repetirla: no lleva encabezado con el
nombre (la fila es el encabezado) ni una línea de resumen que reitere la cuadrícula.
Un campo de datos generales cuya lectura ya es una columna de la tabla se muestra
**solo en los anchos donde esa columna está oculta** — Categoría, Uptime, CPU total,
Memoria e IO R/W por debajo de 1420px; Último evento por debajo de 640px — así cada
lectura aparece exactamente una vez y un móvil no pierde nada. Nombre y Estado se
mantienen en todos los anchos como ancla de la expansión, y `FDs / Threads` nunca se
oculta porque la columna FDs no lleva el número de hilos. La cifra del hilo más
ocupado no se repite en la cuadrícula: pertenece a un proceso, así que la tabla de
procesos la lleva por fila (ver **Procesos** más abajo) en vez de flotar como un total
que esconde de qué proceso viene.

## Panel de sesiones

El panel superior Sessions combina terminales SSH interactivas con los espacios
de nombres configurados de tmux y GNU screen. La búsqueda cubre tipo, servicio,
usuario y sesión; los botones de tipo seleccionan SSH, tmux o screen. La tabla
muestra solo el usuario en la columna User y permite ordenar por tipo, usuario,
sesión, estado, idle, CPU, memoria o IO. El filtro de un tipo se oculta cuando no
hay sesiones activas de ese tipo, con el mismo comportamiento de filtros con
recuento que los demás paneles. Un origen configurado permanece visible bajo
`all` aunque no tenga sesiones, mientras que la espera de muestra y los errores
de muestreo usan estados distintos. Las filas atribuibles muestran idle y CPU,
memoria residente e IO de lectura/escritura del árbol de procesos. Un
administrador solo puede confirmar un cierre cuando el backend vuelve a validar
la identidad exacta de la sesión SSH o del multiplexor. Un origen cuya muestra
correcta está vacía usa una píldora roja `empty` y no tiene una identidad de
proceso que cerrar; su botón `close` solo oculta esa fila vacía en el navegador
actual. No ejecuta órdenes ni señales del multiplexor, y la fila reaparece
después de que ese origen publique una sesión activa.

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
| Availability | la sección de SLA del servicio, cuando `keeps_sla` marca una aplicación que mapea a un servicio monitorizado; ausente en caso contrario |

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

`Watches` contiene tanto watches de host como watches de service. El scope de
host es el predeterminado del panel, así que solo las filas de service marcan su
`scope` junto al nombre; esos nombres usan `service:watch` y se ejecutan como
parte del worker de ese service, no de forma independiente como los watches de
host. La expansión de cada fila muestra el valor completo de `scope`, y ambos
valores siguen siendo buscables.

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
| Filtros de estado | all, disabled, ok, starting, warning, failed |
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
| `hdparm` | Name, dispositivo, bus, lectura buffered, lectura cached |
| `lvm` | Name, salud, VG, LV, tamaño de VG, libre en VG, motivos |
| `smart` | Name, dispositivo, bus, salud, modelo, número de serie, firmware, WWN, medio, capacidad, temperatura, sectores reasignados/pendientes, errores CRC de enlace y de medio, desgaste, tiempo encendido formateado, ciclos de encendido, último autotest |
| `diskio` | Name, dispositivo, bus, utilización, lectura, escritura, await, leído total, escrito total (acumulados desde el arranque, para distinguir un disco ocioso de uno que nadie toca nunca) |
| `cert` | Name, origen, días restantes, caducidad, emisor |
| `raid` | Name, array, tamaño, degradado, recuperando |
| Otros tipos | Name y su valor vivo principal |

La columna de salud maneja dos vocabularios y colorea ambos: el check `lvm`
normaliza el suyo a `ok`/`error`, mientras que `smart` informa del veredicto de la
unidad con las palabras de smartctl. `ok` y `PASSED` se ven en verde; `unknown`
—una unidad que respondió sin veredicto— en ámbar; y todo lo demás, incluidos
`FAILED` y `missing`, en rojo. Un check sin lectura de salud muestra una raya.

Un dispositivo que dejó de responder conserva las filas que siguen siendo
ciertas: la identidad de la unidad, leída de sysfs, y las lecturas que llegó a
dar, cada una etiquetada `(last)` y fechada por una fila `Last seen`, de modo que
un valor histórico nunca pueda leerse como actual. Las últimas lecturas conocidas
viven en la memoria del check en ejecución, así que no están tras reiniciar el
daemon; la identidad, que recuerda el kernel, sí. Ver [Dispositivos
ausentes](rules.md#dispositivos-ausentes).

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
| State | estado normalizado del watch: `disabled` cuando config/monitor state lo excluye de comprobaciones activas, `starting` antes de la primera muestra monitorizada, `failed` para un fallo activo, `warning` para un fallo que el watch declaró aviso con `severity: warning` (fila ámbar, fuera del recuento de alertas) y `ok` en el resto; el trabajo activo del dispositivo tiene prioridad como `testing`, `recovering`, `rebuilding`, `repairing`, `moving` o `merging`, y un dispositivo que dejó de responder como `missing`, que se lee como fallo |
| Actions | acción principal admitida y menú adicional para monitor/unmonitor |

Mientras se ejecuta una muestra manual de `diskio`, `hdparm`, `lvm`, `raid` o `smart`,
State muestra la etiqueta ámbar **checking**, el tiempo transcurrido y el estado
de salud previo. La acción queda desactivada hasta terminar. Events registra el
inicio y el resultado final con su duración. La UI sólo muestra porcentaje cuando
el check aporta progreso real; una sonda sin esa fuente usa el contador de tiempo
en vez de un porcentaje inventado. La sonda se acota con el `timeout:` del propio
check, el mismo presupuesto que usa su ciclo programado, y sólo recurre a
`engine.default_timeout` para un check que no declare ninguno.

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
  `panic-suppressed`, `alert`, `error`, `warning` (lo que levanta un watch de
  aviso en lugar de `error` y `firing`), `firing`, `recovered`, `dry-run`,
  `reload` (una recarga de configuración correcta del daemon en ejecución),
  `hook`/`hook-failed`, `notify`/`notify-failed`/`notify-suppressed`,
  `expand`/`expand-skipped`/`expand-failed`, `kill`/`kill-failed`,
  `makestep`/`makestep-skipped`/`makestep-failed`, y `cascade`
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
