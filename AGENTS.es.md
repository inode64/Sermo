# Sermo — convenciones del proyecto

Este archivo describe el comportamiento, los invariantes y el flujo de trabajo de los agentes **actuales**. Las funcionalidades planificadas o
no implementadas (logs de acceso/eventos, acciones de regla `exec`, prioridades de
servicio, sinks de notificadores extra, modo de clúster, …) viven en [TODO.es.md](TODO.es.md) —
no trates los elementos TODO ausentes como huecos accidentales en el código.

## AI / agent workflow — standard git commits

Los agentes de IA, sub-agentes, sesiones de asistente y procesos automatizados de programación usan el
mismo flujo de trabajo de Git normal que un contribuidor humano en el checkout actual
del repositorio. Mantén el proceso simple: inspecciona el estado, haz las ediciones solicitadas,
ejecuta los checks relevantes, y luego haz commit o merge solo cuando el usuario pida ese
nivel de integración.

**Objetivos**
- Mantener una única fuente de verdad visible en el checkout del repositorio que el usuario está usando.
- Evitar colas de integración ocultas y pasos de limpieza extra.
- Hacer que cada cambio sea fácil de inspeccionar con `git status`, `git diff` y
  `git log` normales.
- Preservar las ediciones del usuario y el estado local no relacionado.

**Flujo de trabajo obligatorio**

1. Antes de editar, inspecciona la rama actual y el estado del directorio de trabajo:

   ```sh
   git status --short --branch
   ```

2. Trabaja directamente en el checkout actual a menos que el usuario pida explícitamente una
   rama separada. Si la rama actual no es apropiada para la tarea, pregunta
   o crea una rama local normal con un nombre claro antes de cambiar archivos.

3. Preserva los cambios no relacionados. Si los archivos ya tienen ediciones del usuario, léelos y trabaja
   con ellas en lugar de revertirlas o sobrescribirlas. Deja en paz los archivos no rastreados
   no relacionados.

4. Mantén las ediciones acotadas a la solicitud y a los límites de propiedad de este
   documento. Ejecuta tests específicos mientras desarrollas, y el **gate de validación
   completo antes de tratar cualquier cambio de código o YAML como terminado** (ver abajo) — no solo
   antes de hacer commit.

   **Gate de validación (ejecutar antes de finalizar una tarea, cada vez):**

   ```sh
   make check        # vet + full test suite; transitively runs `make validate`,
                     # i.e. `make lint` (fmt-check, staticcheck, revive,
                     # golangci-lint, govulncheck) AND `make yaml-validate`
                     # (yaml-fmt-check + yaml-lint)
   ```

   `make check` es el único comando que cubre todo. Ejecutar `go test
   ./...` y/o `go vet` solos **no** es suficiente: se salta `make lint`
   (staticcheck/revive/golangci/govulncheck) y `make yaml-validate`
   (yaml-fmt-check/yaml-lint), que detectan problemas que el toolchain de Go no detecta. Si
   solo tocaste YAML, `make yaml-validate` es el mínimo; para cualquier cambio de Go,
   ejecuta `make check`. Corrige cada problema reportado antes de reportar la tarea como hecha.

5. Haz commit cuando el usuario pida un commit, pida hacer merge a la rama principal,
   o la tarea incluya explícitamente el commit como parte del entregable:

   ```sh
   git add <changed-files>
   git commit -m "agent: <concise description of the change>"
   ```

6. Haz merge solo cuando el usuario pida explícitamente la integración. Antes de hacer merge,
   inspecciona los commits y el diff entrantes, resuelve los conflictos intencionadamente, y
   vuelve a ejecutar los checks relevantes después del merge.

**Prohibiciones**
- No sobrescribas, reviertas, reinicies ni descartes los cambios del usuario a menos que el usuario
  pida explícitamente esa acción destructiva exacta.
- No hagas push a `origin` a menos que el usuario pida explícitamente un push o PR.
- No dejes el repositorio en un estado parcialmente staged sin explicarlo.

**Relación con el resto de AGENTS.es.md**
Este flujo de trabajo es parte del "Small-change checklist". Cada implementación
debería empezar inspeccionando el estado del repositorio y terminar con un commit limpio
y testeado o con un estado del directorio de trabajo claramente reportado.

## Reuse and shared behavior

Por defecto, el cambio más pequeño que preserve el diseño actual. Antes de añadir
un helper, parser, validador, runner, builder o adaptador web/backend, busca
código existente que ya resuelva el mismo problema y extiéndelo cuando el
límite de propiedad se mantenga claro. No dupliques lógica de validación, parsing,
comparación, notificación, monitorización o dispatch de acciones entre `sermod`,
`sermoctl`, web, watches y servicios.

Usa este orden de preferencia:

1. Reutiliza un tipo, helper, builder o ruta de comando existente sin cambios.
2. Extiende el owner existente cuando el nuevo comportamiento pertenezca al mismo concepto.
3. Añade un pequeño helper privado junto al owner cuando elimine duplicación real.
4. Añade un nuevo package o abstracción solo cuando el comportamiento se comparta entre
   límites de packages y los owners existentes sean el lugar equivocado para ello.

No introduzcas una segunda forma de expresar el mismo concepto solo porque el nuevo
call site sea ligeramente diferente. Si el nuevo comportamiento necesita una ruta diferente,
documenta por qué en el punto de dispatch o validación.

Cuando un nuevo check, opción, flag de monitor, comportamiento de notificación o acción web sea
generalmente útil tanto para los `watches:` del host como para los servicios, impleméntalo para
ambas superficies en el mismo cambio a menos que haya una razón documentada para no hacerlo. Si
la funcionalidad aplica intencionadamente solo a una superficie, documenta esa limitación
donde viva la decisión de dispatch/validación y en la documentación de usuario (ver
Documentation lockstep).

## Naming and terminology

Los nombres son vocabulario. Usa exactamente el mismo nombre para un concepto dado a través de
variables, parámetros, comentarios, campos de struct y docs.

Esta es la contraparte de nombres de "Reuse and shared behavior". Antes de elegir
un nombre, mira los structs que ya modelan el concepto (p. ej. `config.Service`,
`process.Selector`, `app.Event`). En caso de duda, trata el nombre del campo del
struct público o API como el único término canónico. Evita casi-sinónimos como
target/service, limit/max/cap o notify/notifier a menos que el código ya
los use para conceptos distintos.

La única excepción sancionada es una colisión con un builtin de Go: una local o
parámetro en minúscula no debe llamarse `max`, `min`, `cap`, `len`, etc. (el
lint `redefines-builtin-id` lo prohíbe). El término canónico sigue nombrando el
campo exportado y JSON (`Max`, `json:"max"`), y la local en minúscula toma un
alias documentado — `limit` para `max`. Así, el concepto de máximo del kernel es `Max` /
`"max"` en structs y en el wire, y `limit` en las locales de función (ver
`levelCountResult` en `internal/checks/check.go` y `countMeter` en
`internal/app/webbackend.go`). No "arregles" esas locales `limit` de vuelta a `max`.

## Configuration structure changes

Cuando la propia estructura de configuración pública de Sermo cambie, rompe la compatibilidad por
defecto y mantén una única ortografía canónica en código, docs, ejemplos y tests. No
preserves alias antiguos, parsers duales, campos deprecados, validadores solo de migración,
comentarios de compatibilidad, fixtures o tests para parámetros de config de Sermo eliminados
a menos que el usuario pida explícitamente compatibilidad, o la ortografía antigua
siga siendo un requisito de compatibilidad externa actual o un invariante de seguridad.

Antes de aplicar una nueva estructura, indica el alcance previsto al usuario: qué forma
YAML se está reemplazando, qué structs/builders/validadores se están eliminando o
reescribiendo, qué docs/ejemplos cambian, y qué necesitarán actualizar los operadores.
Después de que el cambio sea aceptado o solicitado, elimina la estructura anterior en el
mismo cambio: el parsing en runtime, la validación, los ejemplos, los docs de referencia, la guía
de agentes y los tests no deben mantener viva la forma antigua. Documenta las excepciones
explícitamente en el owner. Ejemplos de excepciones válidas son la compatibilidad Linux/init
como `/var/run` con metadatos normalizados a `/run`, invariantes de seguridad duros
como derivar los directorios de lock desde `paths.runtime`, y
**azúcar sintáctico de catálogo/servicios** que se desugara durante la resolución a la forma
canónica (nunca un segundo parser de runtime): `reload_on_change` y
`restart_on_change` en perfiles de catálogo y árboles de servicios se expanden a reglas de
remediación en `internal/config/resolve.go` y se eliminan del árbol resuelto.
No añadas nuevos parsers duales ni shims de migración para parámetros retirados; el nuevo azúcar sintáctico
debe seguir el mismo patrón de desugar unidireccional y estar documentado en
`docs/services.es.md`.

No añadas tests de regresión que alimenten un campo, alias u ortografía YAML eliminados
solo para afirmar que es rechazado. Esos fixtures mantienen vivo el vocabulario retirado.
Testea la forma canónica actual y, cuando la validación estricta necesite
cobertura, usa campos/tipos desconocidos genéricos en lugar de nombres de configuración
antiguos.

## Runtime paths

Usa `/run` para artefactos volátiles de runtime en perfiles de catálogo, configuración
generada, ejemplos y docs: pidfiles, sockets, metadatos de runtime de OpenRC,
directorios de runtime de Sermo y locks. No escribas nuevas rutas `/var/run` en la configuración
de Sermo. Los sistemas Linux modernos exponen `/var/run` como un symlink de compatibilidad
a `/run`, y los scripts de init antiguos, gestores de servicios o configs empaquetadas
pueden seguir reportando esa ortografía. Sermo debe seguir normalizando esas rutas provistas
por el host; esto es compatibilidad Linux/init, no una forma de configuración de Sermo obsoleta
a eliminar.

Cuando systemd, OpenRC o un archivo del host reporten un pidfile o socket bajo
`/var/run`, normalízalo a la ruta `/run/...` equivalente antes de escribirlo en un
catalog service, config de servicio generada o ejemplo de documentación.

Antes de añadir cualquier nueva ruta de runtime, comprueba si la ruta o uno de sus directorios
padre es un symlink (`readlink -f <path>` o `namei -l <path>`). Registra
la ruta destino canónica, no la ortografía del symlink, para que el catálogo no acumule
alias duplicados para el mismo pidfile o socket.

## Configuration file granularity

Usa un archivo YAML por target — un único documento de un solo kind por archivo,
nunca varios targets agrupados. El kind de un documento se deriva de dónde vive
(subdir del catálogo / `paths.services` / `paths.watches`), así que un `kind:`
de nivel superior es opcional y se omite. Los documentos de watch bajo cualquier
directorio listado en `paths.watches` usan `name:` de nivel superior más los
campos del watch; los fragmentos de notifier siguen usando un mapa `notifiers:`
de nivel superior, pero ese mapa debe contener exactamente una entrada con
nombre. Usa directorios de watch clasificados como `watches/`, `networks/`,
`storages/` y `mounts/` listándolos todos bajo `paths.watches`. La única
excepción es un bundle de referencia claramente etiquetado como
`docs/sermo-all.yml`, que agrupa ejemplos para validar el esquema completo en un
único lugar.

Para desarrollo y validación en el árbol de fuentes sin instalar bajo `/etc/sermo`,
compila con `SERMO_DATADIR=$PWD make build` y luego usa `examples/sermo-dev.yml`
(`paths.*` relativos al árbol `examples/` incluido). El catálogo empaquetado no
es un ajuste de `paths.*`; viene del directorio de catálogo compilado en el
binario. `examples/sermo.yml` apunta intencionadamente a ubicaciones instaladas.

## Catalog init and reload fallback verification

Cuando añadas o cambies un catalog service que dependa de metadatos de init o defina
`reload.signal`, verifica cada backend de init en su mapa `service:` y cada
fallback que Sermo pueda usar. No valides solo la distro donde el perfil fue
escrito por primera vez.

Para OpenRC, inspecciona el `/etc/init.d/<unit>` empaquetado real y el
`/etc/conf.d/<unit>` correspondiente buscando `reload()`, `pidfile`, `command`, `command_user`,
`start-stop-daemon --pidfile`, ajustes del supervisor y cualquier variable `*_PIDFILE`.
Para systemd, inspecciona la unidad y los metadatos de `systemctl show` (`CanReload`,
`MainPID`, `PIDFile`, `User`). Normaliza cualquier ruta `/var/run` reportada a
rutas `/run` canónicas antes de escribir el YAML del catálogo.

Cualquier `reload.signal` capaz de OpenRC debe tener un candidato `pidfile:` canónico
y un selector `processes:` con `exe` y `user` exactos, para que el
PID del pidfile pueda verificarse antes de que Sermo le envíe la señal. Si los scripts de init difieren por
distro, codifica los candidatos reales con una lista de rutas o una rama `os:`. Si un backend
no tiene un pidfile confiable ni un selector de identidad exacto, usa `reload.command` o
apóyate en la propia ruta de reload del backend en lugar de incluir un fallback de señal
inseguro.

Antes de finalizar tal cambio, ejecuta la validación real del catálogo para ambos
backends:

```sh
go test ./internal/config -run 'TestRealCatalog(AllServicesValidate|ReloadServicesResolve)$' -count=1
```

## Service operations

Las acciones de nivel de aplicación start, stop, restart, reload, resume o signal sobre un servicio
deben pasar por el package compartido `internal/operation` y su engine. No
llames a los backends directamente, no envíes señales desde `app/` o `cli/`, y no
saltes los locks, guards, preflight o política. La ruta de operación es la única
fuente de verdad para el control seguro de servicios.

Las excepciones acotadas son las implementaciones de backend/proceso que proveen las
APIs de operación primitivas, y los tests/fakes que prueban que esas primitivas funcionan. Mantén
esas primitivas pequeñas, inyectables y cubiertas por tests.

## Native by default

Evita comandos externos siempre que sea práctico; prefiere la biblioteca estándar de Go o una
alternativa de módulo de Go, a menos que la entrada requiera explícitamente una biblioteca o comando
de terceros. Cuando un comando externo sea genuinamente requerido (`systemctl`,
`rc-service`, checks `command` de usuario, hooks, …), el código de producción no debe
llamar a `os/exec` directamente: pasa por un runner `execx` inyectable con un
contexto y un timeout explícito, invocando un argv directamente — nunca un shell.
`execx` y los tests/fakes son las únicas excepciones.

La única excepción de producción es `sermoctl lock … -- COMMAND`: `lock wrap` ejecuta
el argv provisto por el operador con stdin/stdout/stderr heredados para que el proceso
envuelto se comporte como un trabajo de primer plano de shell normal; usa `exec.CommandContext`
directamente en `internal/cli/lock.go` (sin `execx`, sin timeout del engine — el comando
envuelto posee su propio ciclo de vida). No enrutes otras rutas de CLI o daemon a través de
este atajo.

## Protocol probes: interface binding is mandatory

Cada sonda de protocolo `internal/conn` debe honrar `cfg.Interface` — la interfaz
de red de egreso (Linux `SO_BINDTODEVICE`), configurada en hosts multi-homed para que una sonda
salga a través de un enlace específico. El `BindDialer(cfg.Interface)` compartido (y
`BindListenConfig` para packet sockets) es el único mecanismo; cada sonda hace dial
a través de él, directamente o vía `probeBanner`/`dialDeadline`/`dialConn`. Una sonda que
use silenciosamente el routing por defecto es un bug.

Esto restringe la adopción de un módulo de Go para "simplificar" un protocolo. Decide según dónde
hace su I/O la biblioteca:

1. **Biblioteca solo de códec (sin I/O)** — preferido. Mantén el dial a través de `BindDialer`
   y entrega los bytes a la biblioteca para construir/parsear. El interface binding queda
   intacto. Ejemplo: DNS usa `golang.org/x/net/dns/dnsmessage` puramente como un códec
   de wire sobre el dial UDP existente.
2. **Biblioteca que hace su propio I/O pero acepta un dialer o conexión personalizados** —
   aceptable. Enruta su dial a través de `BindDialer` mediante el hook de la biblioteca para que
   el binding se preserve. Ejemplo: NTP usa `github.com/beevik/ntp` a través de su
   callback `QueryOptions.Dialer`, que hace dial con `BindDialer(cfg.Interface)`.
3. **Biblioteca que hace dial internamente y no puede aceptar un dialer/conexión personalizados**
   — NO la adoptes: saltaría `SO_BINDTODEVICE` y rompería el interface
   binding. Mantén la sonda hecha a mano (y su transporte) en su lugar. Por eso
   la sonda DHCP mantiene su propio transporte raw-socket (`dhcp_linux.go`) en lugar de
   cambiar a una biblioteca cliente DHCP completa, aunque exista un módulo.

En resumen: el interface binding gana sobre la reducción de código. Un módulo solo vale la pena
adoptar cuando el binding sobrevive — de lo contrario nuestro propio código se queda. Registra la razón
en la sonda cuando una migración se omita intencionadamente.

## Documentation lockstep

Cuando cambies la configuración, añadas un tipo de check, notifier, acción de regla o
comportamiento observable, actualiza la documentación correspondiente, los ejemplos de catálogo
(cuando sea generalmente útil) y `docs/configuration.es.md`, `docs/rules.es.md` y la
documentación de servicios en el mismo cambio. Mantén los comentarios de `examples/sermo.yml` al día. El código
y los docs deben evolucionar juntos.

Cuando una solicitud del usuario, un hallazgo de implementación o un comportamiento en runtime contradiga la
documentación actual, señala el desajuste explícitamente antes de tratar cualquiera de los dos
lados como autoritativo. Si el usuario acepta el comportamiento solicitado o el cambio
se implementa, actualiza la documentación en conflicto en el mismo patch; no
dejes docs describiendo el comportamiento antiguo.

## Documentation scope and style

Documenta solo lo que sea requerido por una de estas razones:

- El comportamiento de cara al usuario, la configuración, la CLI, la política de seguridad o el flujo de trabajo
  operativo cambió y debe mantenerse en lockstep con el código.
- Una regla de lint o un analizador requiere la documentación o justificación, como
  símbolos de Go exportados, justificación de `//nolint` o una excepción de seguridad.
- El requisito, invariante o excepción es necesario para usar, mantener o
  revisar el código de forma segura.

Mantén la documentación directa. Prefiere la explicación correcta más corta, enlaza a una
fuente de verdad existente en lugar de repetirla, y elimina prosa redundante al
editar texto cercano. No documentes pasos de implementación obvios solo para narrar
el código.

## Central builders

Los nuevos tipos de check, kinds de watch, notifiers y acciones de regla empiezan en las funciones
builder centrales (`internal/checks/build.go`, `internal/app/watch_build.go`,
`internal/notify/`, builders de reglas, etc.). No dupliques la lógica de construcción
ni añadas casos ad-hoc entre packages. Si aún no existe un builder central, crea
uno en el package owner en lugar de esparcir casos switch por los callers.

**Los notifiers** son transportes tipados y pluggable: registra un builder en
`internal/notify` indexado por `type` (`email`, `slack`, `teams`, …). Los call sites
referencian a los notifiers solo por nombre; añadir un transporte no debe requerir cambios
fuera de `internal/notify` y la documentación de usuario (`docs/configuration.es.md`).

## Timeout discipline

Cada operación bloqueante (comandos, red, base de datos, I/O, etc.) debe estar
acotada por un timeout tomado de la configuración del engine (vía `app.EngineDuration`
o `cfgval`) o una constante con nombre y documentada. Los literales de duración mágicos en
la lógica de aplicación están prohibidos. Los literales cortos son aceptables en tests cuando
acotan el propio test en lugar del comportamiento de producción.

## Daemon performance discipline

Trata cada ruta de código que se ejecuta dentro de `sermod` como sensible al rendimiento:
workers, checks, watches, evaluación de reglas, descubrimiento de procesos, muestreo de métricas,
persistencia de estado, refrescos del web-backend y rutas de reload/rebuild afectan todas al
daemon de larga duración. Optimiza estas rutas para velocidad y uso acotado de recursos
antes de añadir trabajo de conveniencia. Prefiere muestras cacheadas o compartidas sobre escaneos
de host repetidos en el mismo ciclo, evita asignaciones y ordenaciones evitables en bucles
calientes, mantén el trabajo bloqueante fuera de las secciones críticas del scheduler, y haz que las operaciones
costosas sean explícitas, rate-limited o acotadas por intervalo.

Cuando una nueva funcionalidad añada trabajo al ciclo del daemon, revisa su coste a escala normal de flota
y añade tests o benchmarks cuando el coste no sea obvio. Una pequeña ineficiencia en
un servicio/watch puede multiplicarse por cada target configurado y degradar la
latencia de monitorización, la responsividad web y el timing de remediación.

## Small-change checklist

Antes de finalizar cualquier cambio de código:

- **Disciplina de Git (agentes de IA):** Inspecciona `git status --short --branch` antes de
  editar, preserva los cambios de usuario no relacionados, haz commit solo cuando se solicite o cuando
  la tarea incluya el commit, y nunca hagas push a menos que se pida explícitamente.
- Busca el owner existente con `rg` antes de añadir un nuevo helper o switch.
- Mantén el patch cerca de ese owner; evita refactors no relacionados.
- Preserva los nombres de campo públicos de YAML, JSON, CLI y web a menos que el cambio sea
  explícitamente una migración.
- Añade o mueve tests cuando se encuentre un bug o comportamiento ambiguo.
- **Gate de validación — ejecuta `make check` antes de tratar cualquier cambio como completo**
  (el paso 4 del AI workflow lo detalla). `make check` = vet + tests completos, y
  ejecuta transitivamente `make lint` (fmt-check, staticcheck, revive, golangci-lint,
  govulncheck) y `make yaml-validate` (yaml-fmt-check + yaml-lint). Nunca
  sustituyas con un simple `go test ./...` / `go vet`: esos se saltan lint y yaml-lint y
  pierden problemas reales (p. ej. reglas de stutter/comment de revive). Corrige cada hallazgo antes de
  reportar como hecho.
- Para cambios de cara al daemon, comprueba el coste en runtime en el ciclo de estado estable
  y evita escaneos repetidos, llamadas bloqueantes o asignaciones evitables en rutas calientes.
- Actualiza docs y ejemplos en el mismo cambio cuando el comportamiento cambie.

## Web UI cohesion

**Las fuentes viven en `internal/web/src/`; `internal/web/index.html` es un artefacto generado,
committeado — nunca lo edites a mano.** El dashboard se escribe como un
shell (`src/index.html`), `src/styles.css`, y módulos ES (`src/app.js` y el
`src/vendor/lit-html.js` vendorizado). `make web` ejecuta el build de esbuild in-process
(`internal/web/build`, la API de Go — sin Node/npm) para bundlear + minificar en
`internal/web/index.html`, dejando los placeholders `{{CSP_NONCE}}`/`{{VERSION}}`
para que el servidor los rellene por request. **Después de editar cualquier cosa bajo
`internal/web/src/`, ejecuta `make web` y committea el `index.html` regenerado.**
`make web-check` (conectado a `validate`/`check`/CI, modelado sobre `fmt-check`) falla
si el archivo committeado está obsoleto.

**El renderizado usa lit-html.** Construye markup con `tpl\`...\`` (el tag `html`,
importado con alias `tpl`) y renderiza en un contenedor con `litRender(...)` (el
export `render`). lit-html escapa los bindings de texto/atributos, así que **no** envuelvas las
interpolaciones en `esc()` dentro de una plantilla, y nunca incrustes una cadena HTML cruda
en una plantilla `tpl` (se renderiza como texto escapado visible); compón con plantillas
anidadas y usa `nothing` para omitir un binding/atributo. lit-html hace diff del DOM
en su sitio, así que no hay capa manual de parcheo de filas — renderiza la lista completa y deja
que reconcilie. Los builders de hoja (`stateBadge`, `serviceStateBadge`, `categoryBadge`,
`usageBar`/`usageBarMini`, helpers de SLA) y los builders de fila de evento/servicio/watch/app/overview
devuelven `TemplateResult`s; los builders de gráficas SVG (`drawSLAChart`,
`drawMetricChart`) siguen siendo basados en string y se asignan a su propio contenedor vía
`innerHTML`. La interacción sigue cableada a través del handler global de click delegado
leyendo atributos `data-*` (`closestFrom`), **no** bindings `@event` de lit — esto
mantiene la postura CSP sin handlers inline (`server_test.go` lo afirma).

`internal/web/index_test.go` parsea el HTML generado estructuralmente con
`golang.org/x/net/html` (contrato del servidor, higiene CSP, anchors del shell) en lugar de
hacer matching de strings de fuente JS, así que sobrevive a la minificación y los renames.

**Antes de añadir o cambiar cualquier elemento de UI, encuentra el elemento existente que
ya resuelve el mismo problema y copia su estructura, clases y estilo
exactamente** — no inventes una forma paralela de hacer lo mismo. La cohesión entre
paneles es un requisito duro, no una preferencia.

Concretamente, cada panel de datos es un `<details id="{name}-section">` con un
`<summary>`, una fila flex opcional `#{name}-controls` (búsqueda + filtros + contador)
y una `<table class="{name}-table">` desnuda colocada directamente dentro del `<details>`.
No envuelvas las tablas de datos en contenedores de scroll; la página hace scroll como un todo
en lugar de atrapar un panel en su propia barra de scroll. Cuando introduzcas un patrón
genuinamente nuevo, documéntalo aquí para que el siguiente cambio pueda seguirlo.

Cuando añadas un host watch/check con datos útiles en runtime, cablea su ruta de Web UI
`Watch.Meter` o `Watch.Readings` en `internal/app/webbackend.go` y añade
un test de regresión de `webbackend`; no lo dejes visible solo como config estática o
condiciones configuradas. Los watches con estado (`file`, `process`, sondas basadas en tasa,
…) deben exponer la muestra en vivo que el daemon ya computa, no solo los umbrales
YAML.

La capa visual es un sistema de diseño basado en tokens (rediseño de junio de 2026):

- **Design tokens.** Todos los colores/radios/sombras vienen de propiedades CSS personalizadas en
  `:root` (`--bg`, `--panel`, `--text`, `--line`, `--ok`, `--warn`, `--crit`,
  `--info`, …) con un bloque de override `prefers-color-scheme: dark`. Nunca
  hardcodees un color en CSS nuevo — usa los tokens, derivando tintes con
  `color-mix(in srgb, var(--x) N%, transparent)`. (Los fills SVG inline emitidos por JS
  mantienen la paleta literal estilo GitHub, que se lee en ambos esquemas.)
- **Panel cards.** Cada sección `<details>` (más `#locks-section` y
  `#detail`) se estiliza como una card automáticamente — borde redondeado, sombra, el
  `<summary>` como header. Una nueva sección no necesita clases extra.
- **Overview tiles.** La banda `#overview` bajo la topbar es la capa de vistazo
  rápido: `renderOverview` (llamado desde `renderStatus`, sin requests extra)
  emite `<button class="tile" data-panel-target=…>` por signo vital, con
  acentos `t-ok`/`t-warn`/`t-crit` y gauges `usageBar` opcionales.
- **Status pills.** `.target-state` renderiza los estados como pills tintadas con un
  punto coloreado (`::before`, `currentColor`); `state-failed` pulsa. Los nuevos estados
  solo necesitan una clase de color `state-<name>`.
- **SLA timeline strip.** `renderSLATimeline(segments, window)` renderiza una
  banda contigua de disponibilidad estilo status-page — una celda `.sla-seg` por sub-span
  igual (más antiguo a la izquierda), coloreada por `slaColor`, rayada `.sla-gap` donde no
  se observó ciclo. `renderSLAWindows` la usa para cada ventana de SLA rodante;
  los ratios por segmento vienen del backend (`SLAWindow.Segments`). Reutilízala
  donde se necesite un historial de disponibilidad compacto.
- **Value formatting (un tipo → un formatter).** Un tipo dado de valor debe
  renderizarse idénticamente en todas partes; nunca formatees a mano con `toFixed` desnudo, concatenación
  de strings o un `${value}` crudo. Cada tipo tiene un único helper canónico —
  enruta cada lectura de cara al usuario a través de él (esto es lo que evita que "2.1%"
  aparezca en otro sitio como "2.14%" o "234.5678 B/s"):
  - **Números** → `fmtNum(n, max=2)` (el formatter base; ≤`max` decimales,
    ceros finales eliminados, `—` cuando no es finito). Todos los demás helpers se construyen sobre él.
  - **Porcentajes** → `fmtPct(n)` (`fmtNum(n,2)+"%"`). Incluye CPU%, memoria %,
    saturación, SLA % — tiles, barras y lecturas de detalle lo usan todos.
  - **Bytes / tasas de bytes** → `fmtBytes(n)` (y `fmtBytes(n)+"/s"`); vía
    `fmtMetricValue(v, unit)` para series temporales con unidad etiquetada (`bytes`, `B/s`, `%`,
    `ms`, default).
  - **Duraciones** → `fmtUptime`/`fmtSeconds`/`shortDur`; **tiempo relativo** →
    `fmtRemain`/`fmtUntilShort`/`fmtAge`/`fmtSince`; **timestamps absolutos** →
    `fmtTime`.
  - **Gauges** → `usageBar` (gauge de host de ancho completo), `usageBarMini` (celdas de tabla
    densas), `cpuBarMini` (CPU normalizada por core). Clampea con `pctClamp`.
  `toFixed` desnudo está reservado **solo para geometría** — coordenadas de path SVG y anchos de
  barra CSS (`--usage-pct`, `--sla-pct`) mantienen su propia precisión fija. Cuando un
  valor necesite una representación que ningún helper cubra, añade o extiende un helper junto a
  los otros en lugar de formatear inline en el call site. Ver el comentario de banner de `fmtNum`
  en `internal/web/src/app.js`.

**CSP e inline styles:** `style-src` lleva deliberadamente `'unsafe-inline'`
**sin** un nonce — según CSP2, un nonce en la lista hace que los navegadores ignoren
`'unsafe-inline'` y eliminen silenciosamente cada atributo `style="…"` generado
(ocultación de secciones, anchos de gauge). No "endurezcas" style-src de vuelta a un nonce;
script-src sigue siendo nonce-strict (ver `securityHeaders` en
`internal/web/server.go`).

## Wizard option selection

El wizard interactivo (`sermoctl wizard`, `internal/assist`) sigue **un
flujo de preguntas canónico para cada asistente, presente y futuro** — documentado
en [docs/wizards.es.md](docs/wizards.es.md). Léelo antes de añadir o cambiar un
wizard; los invariantes de abajo no deben derivar por asistente.

Dirige cada selección a través de los helpers `Prompt` compartidos — nunca hagas a mano una
pregunta a medida. Los multi-selects usan `Prompt.MultiChoose` (números de ítem, la
keyword `all`, o el nombre de una opción); los menús con picks reservados usan
`Prompt.MultiChooseKeyword`. Muestra los targets detectados para elegir — **nunca pidas
al operador inventar un nombre**. Las preguntas sí/no van a través de `Prompt.Confirm`,
que **fuerza una respuesta explícita** (una línea vacía vuelve a preguntar; no toma
un default). El estado de monitor y el intervalo vienen del `Prompt.AskMonitoring`
compartido y se inyectan en cada entrada generada.

Reutiliza un vocabulario consistente de **all / none / default**: `all` selecciona
todo; `none` opta por salir (solo-monitor, `notify: [none]`); `default` hereda
el notify global. `none` y `default` son **siempre seleccionables, incluso con cero
notifiers configurados** — el wizard nunca se bloquea en la pregunta de notifier. Cuando
`default` no tiene nada que heredar (sin notify global) **degrada a
solo-monitor** con una nota de una línea; nunca debe volver a preguntar ni abortar (ver
`chooseNotifiers` en `internal/assist/notify.go`). El paso final previsualiza lo que
se escribirá, confirma, y ofrece eliminar archivos gestionados cuyo target ya
no se detecta. Mantén `docs/wizards.es.md`, `docs/configuration.es.md` y esta
sección en step cuando algo de esto cambie.

## Catalog: instanced systemd services

Cuando un catalog service apunte a una unidad de **instancia** de systemd (`unit@instance`), no
inventes una variable `${id}` escrita a mano que el operador deba recordar configurar —
deriva la instancia desde código, reutilizando la maquinaria existente:

- **Instancia única indexada por host** (p. ej. `ceph-mon@node1`, `ceph-mds@node1`):
  usa el `${hostname}` incorporado (el hostname corto) — `service:
  "ceph-mon@${hostname}"`. Resuelve con cero config por servicio; una variable `hostname`
  explícita o `SERMO_HOSTNAME` lo anula. `${hostname}` es la forma
  corta, distinta de `${host}` (el fallback de bind-address) — ver `docs/services.es.md`.
- **Multi-instancia numérica** (p. ej. un OSD por dispositivo, `ceph-osd@0..N`): haz que la
  app sea una plantilla `%n` (`name: ceph-osd%n`) con `versions: { from:
  "/var/lib/ceph/osd/ceph-${n}" }`, luego haz que el catalog service sea una plantilla `%n` que coincida
  y enlace `apps: ["ceph-osd${n}"]`. `internal/config/versions.go` hace glob de la
  ruta de descubrimiento de la app en el host y materializa un catalog service concreto por
  id descubierto, con `${n}` incrustado en `service: "ceph-osd@${n}"`. Limitación
  honesta: esto auto-descubre *definiciones* de catalog service; el operador aún
  habilita un servicio por instancia (Sermo monitoriza servicios, no catalog
  services).

Mantén `docs/services.es.md` (tabla de variables incorporadas) en step cuando añadas una incorporada.

## Go quality gates

Dos reglas, una batería:

- **Cada archivo Go debe estar limpio de `gofmt` después de cualquier modificación.** Un hook
  `PostToolUse` de Claude Code (`.claude/settings.json`) ejecuta `gofmt -w` en cada archivo
  `.go` editado; editando fuera de Claude Code, ejecútalo tú mismo (format-on-save del
  editor).
- **Cada cambio debe pasar la batería completa antes de hacer commit** (las herramientas
  analizan el módulo completo y son demasiado lentas por edición). `make test` y `make check`
  siempre ejecutan `fmt-check` y `make lint` primero vía el target `validate`; el
  Makefile encuentra las herramientas instaladas por Go en `~/go/bin` y da a las cachés de análisis
  estático un fallback escribible para agentes no interactivos:

```sh
go build ./...                    # must pass
make check                      # vet, fmt-check, lint, yaml-validate, go test ./...
```

Toolchain de YAML (instalar una vez):

```sh
go install github.com/google/yamlfmt/cmd/yamlfmt@latest
pip install yamllint            # or pipx install yamllint
make yaml-fmt                   # format tracked YAML (catalog, examples, docs, …)
make yaml-validate              # yamlfmt -lint + yamllint (also runs via make validate)
```

El YAML del catálogo y de ejemplos usa **secuencias de bloque indentadas** (`proxy_binary:` y luego `  - path`
en la siguiente línea), configurado en `.yamlfmt` con `indentless_arrays: false`.
`yamllint` lo refleja con `indent-sequences: consistent`. Los flow maps inline usan llaves
espaciadas (`{ type: binary, path: "${binary}" }`); `make yaml-fmt` ejecuta `yamlfmt` y luego
`scripts/normalize_yaml_flow.py` porque yamlfmt elimina esos espacios interiores. Los comentarios
inline se rellenan con dos espacios (`pad_line_comments: 2` en `.yamlfmt`).

Notas de herramientas:

- **`make lint`** es el entrypoint canónico del analizador de Go. No prefijes a mano el
  `PATH` ni llames a los binarios del analizador uno por uno a menos que estés depurando el
  target de lint en sí. `govulncheck` puede necesitar acceso a red para refrescar la
  DB de vulnerabilidades; un fallo de red/DNS ahí es un problema de entorno, no un
  hallazgo de código.
- **`revive`** (`revive.toml`): conjunto de reglas por defecto menos `unused-parameter` (muchos
  métodos implementan interfaces cuyo `ctx` ignoran legítimamente). Documenta los
  nuevos símbolos exportados — la regla `exported` está activa.
- **`golangci-lint`** usa `.golangci.yml` (**formato v2** — el binario debe ser
  v2) para `gosec`, `bodyclose`, `copyloopvar`, `ineffassign`, `nilerr` y
  `wastedassign`.
  Las excepciones aceptadas de gosec viven en esa config: `G115`, y en fixtures de test
  `G306`/`G101`/`G703`. Los casos by-design (`G204` comandos configurados por el operador,
  escrituras `0644` intencionales, lecturas acotadas `args[i]`, `G118` de contexto de shutdown)
  se suprimen en el call site con `//nolint:gosec` más un comentario
  justificativo — prefiere eso sobre ampliar la config.

## Testing

Los tests son parte del cambio, no una ocurrencia tardía (ver el small-change
checklist). Imita el estilo existente de la suite en lugar de inventar uno.

- **Inyecta el seam; nunca toques el host desde la lógica bajo test.** Cada sonda
  que lee el sistema toma una función o interface inyectable, para que los tests corran
  sin `/proc`, sockets o servicios reales: los campos `*SamplerFunc` y los
  samplers `Deps` en los checks (`FdsSamplerFunc`, `MemorySamplerFunc`, …), la
  interface `metrics.Reader`, `execx.Runner`, `process.Signaler`, y la interface
  `Backend` de la web. Añade un seam con la misma forma cuando añadas una sonda.
- **Reutiliza los fakes existentes** — `fakeReader` (metrics), `fakeRunner`/
  `scriptRunner` (servicemgr), `fakeFds`/`fakeConntrack` (checks), `fakeBackend`
  (web). Copia su forma; no añadas un framework de mocking.
- **Subtests table-driven.** Expresa las variantes como un slice de casos dirigido por
  `t.Run(tc.name, …)`, el patrón dominante en la suite.
- **Una función por caso es boilerplate — funde la familia en una tabla.** Cuando
  varias funciones de test solo se diferencian en las entradas (un
  `Test<X>Registered` por probe que asegura puerto/usuario, un
  `Test<X>TimeoutMessage` por sampler), van en un único test table-driven con una
  fila por caso: misma cobertura, menos ruido y un sitio obvio donde añadir el
  siguiente caso. Amplía los tests ya consolidados — `TestProbeMetadata` (conn),
  `TestInterfaceBindingApplied` (conn), `TestSampleTimeouts` (checks) — en vez de
  añadir funciones hermanas.
- **Fija toda entrada que cambie el resultado, o fallará en otra máquina.** Un
  test que lee estado ambiental pasa en tu equipo y rompe en CI. Fija el SO con
  `detectedOS = "gentoo"` (o `SERMO_OS`) *antes* de `config.Load` cuando las
  aserciones dependan de selectores `os:`; inyecta `LoadConfig` (o pasa
  `--config`) para que un comando nunca lea el `/etc/sermo/sermo.yml` por defecto;
  y cuando la lógica toque `/proc`/loopback, instala un prober falso (ver el
  `fakeAliveProber` de los tests de locks) en vez de `t.Skip` — un test saltado
  no es un test que pasa.
- **Antes de borrar un test "redundante", demuestra que la aserción estricta
  sobrevive.** Un test concreto puede fijar una distinción que un caso de tabla
  oculta: una fila `slices.Equal(got, nil)` trata `nil` y `[]string{}` como
  iguales, así que una comprobación dedicada `got != nil` *no* es redundante.
  Confirma que el comportamiento exacto se sigue asegurando en otro sitio antes
  de quitar el test.
- **Separa la lógica pura del I/O para que sea testeable directamente** (p. ej.
  `parseMeminfoKB`, `parseOSReleasePrettyName`, `levelCountResult`). Esto sirve
  también a la regla de reuse.
- **Flujos dirigidos por prompt** (`internal/assist`) abortan ante entrada truncada vía
  `assist.Recover(&err)`; dirígelos con un `strings.NewReader` scripteado y
  afirma el resultado, como hacen los tests del wizard.
- Las duraciones mágicas cortas están bien en tests cuando acotan el propio test, no
  el comportamiento de producción (ver Timeout discipline).

## Security and safety invariants

1. Nunca mates procesos solo por nombre.
2. Nunca uses `SIGKILL` a menos que el catalog service o la definición de servicio lo permitan explícitamente.
3. Una política de `SIGKILL` debe incluir una cláusula restrictiva `kill_only_if`.
4. El matching de procesos debe validar al menos `exe` y `user`; prefiere `pidfile` o `cgroup` como evidencia adicional. `exe` es la ruta resuelta `/proc/<pid>/exe` coincidida exactamente (nunca argv[0]/cmdline, nunca un substring); un `exe` no resoluble nunca coincide. Ver `docs/safety.es.md` (identidad de proceso).
5. Nunca hagas start, stop, restart, reload o resume de un servicio cuando un guard que coincida
   bloquee la acción.
6. Nunca hagas start, restart, reload o resume cuando los checks de preflight requeridos fallen.
7. Nunca realices acciones de servicio sin un timeout.
8. Nunca entres en un bucle de restart. La remediación automática debe honrar el bloque
   `policy` resuelto por servicio; `policy.cooldown` es obligatorio y positivo después de
   la resolución de config, con max_actions/backoff opcionales; ver `docs/rules.es.md`
   (política de remediación). El cooldown lo decide la evaluación de reglas del daemon
   antes de que el engine compartido corra. Los comandos manuales del operador están exentos del
   cooldown pero siguen sujetos a locks, guards y preflight.
9. Siempre registra si una acción fue ejecutada o bloqueada, y por qué. Hoy eso
   significa eventos del daemon (`action`, `blocked`, `dry-run`, `suppressed`, …) vía el
   log de eventos in-process (web UI / `sermoctl activity`) y salida explícita de status de CLI
   para operaciones manuales. La exportación append-only de `access.log` / `event.log`
   es trabajo futuro ([TODO.es.md](TODO.es.md)); no saltes las rutas existentes de evento/CLI
   mientras añades esos sinks.
10. Los servicios de base de datos deben usar por defecto políticas de stop conservadoras.
11. La auto-remediación debe usar la misma ruta de operación segura que los comandos manuales de `sermoctl`.
12. Solo los residuales que coincidan exactamente con `kill_only_if` se señalan; un residual
    que no coincida (o tenga un exe no resoluble) se reporta, nunca se mata. Cualquier
    residual restante hace que el resultado sea `orphan_processes`, y un stop fallido no debe
    iniciar automáticamente el servicio a menos que la política lo permita explícitamente.
13. La remediación debe dispararse solo en métricas de alcance de servicio. Una métrica de todo el
    sistema (memoria total, CPU total, carga) nunca debe dirigir start, stop, restart, reload
    o resume para un servicio individual; solo puede dirigir una alerta.
14. Las condiciones de regla son predicados de solo lectura, evaluados como mucho una vez por ciclo. Una
    condición nunca debe mutar el estado del sistema; la mutación pertenece a las acciones.
15. Los locks se adquieren atómicamente (O_CREAT|O_EXCL) y están acotados por un TTL. Un lock se
    honra solo mientras está activo; un lock expirado, o uno cuyo PID owner está muerto
    (comprobado vía owner_start_ticks para sobrevivir a la reutilización de PID), es stale y debe
    reclamarse a través de una ruta registrada, nunca sobrescrito silenciosamente. Los archivos de
    lock de runtime con nombre usan `<service>[.<name>].lock` bajo `<paths.runtime>/locks`
    (por defecto `/run/sermo/locks`), gestionados por los comandos `sermoctl lock`
    (wrap / acquire / release). El lock de operación interno usa la
    ruta separada `<paths.runtime>/ops/<service>.lock` para que no pueda colisionar con
    un lock de usuario llamado `op`. `paths.locks` y `/etc/sermo/locks.d` no tienen
    semántica. Ver `docs/safety.es.md` (locks).
16. El scheduler ejecuta un worker independiente por servicio; una operación larga
    (un restart de varios minutos) en un servicio nunca debe bloquear la monitorización de
    otro. Nunca serialices todos los servicios a través de un único bucle. Los restarts masivos
    están acotados por un semáforo global de operación, y la ejecución concurrente de checks
    entre todos los servicios está acotada por `engine.max_parallel_checks` (un pool global
    separado). Ver `docs/safety.es.md` (scheduler y concurrencia).

## graphify

Este proyecto tiene un grafo de conocimiento en graphify-out/ con god nodes, estructura de comunidades y relaciones entre archivos.

Cuando el usuario escriba `/graphify`, invoca la herramienta `skill` con `skill: "graphify"` antes de hacer cualquier otra cosa.

Reglas:
- Para preguntas sobre la base de código, ejecuta primero `graphify query "<question>"` cuando
  `graphify-out/graph.json` exista. Usa `graphify path "<A>" "<B>"` para
  relaciones y `graphify explain "<concept>"` para conceptos enfocados. Estos
  devuelven un subgrafo acotado, normalmente mucho más pequeño que un grep crudo o un reporte completo.
- Los archivos `graphify-out/` sucios son esperables tras hooks o actualizaciones incrementales;
  los archivos de grafo sucios no son razón para saltar graphify. Solo salta graphify si la
  tarea es sobre salida de grafo obsoleta o incorrecta, o el usuario dice explícitamente que no
  lo use.
- Si `graphify-out/wiki/index.md` existe, úsalo para navegación amplia en lugar de
  navegación cruda de fuentes.
- `graphify-out/GRAPH_REPORT.md` y `graphify-out/graph.html` son exports locales
  (gitignored). No asumas que están presentes en el checkout; ejecuta
  `graphify update .` cuando se necesite un reporte legible por humanos, o quédate con
  query/path/explain para trabajo de agente.
- Después de modificar código, ejecuta `graphify update .` para mantener el grafo actualizado
  (solo AST, sin coste de API). Las entradas de query principales (`graph.json`, `manifest.json`,
  labels) pueden permanecer rastreadas.
