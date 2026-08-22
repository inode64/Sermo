# Asistentes (`sermoctl wizard`)

El wizard interactivo genera configuración de Sermo: un **watch** de host
(`volume`, `net`, `uplink`), un **servicio** monitorizado (`service`, `docker`,
`vm`) o una **unidad de montaje** respaldada por fstab (`mount`). Cada asistente vive en `internal/assist/` y se lanza desde
`internal/cli/wizard.go`.

Este documento define el **único flujo de preguntas que siguen todos los wizards** — presentes y
futuros. Existe para no repetir los mismos errores: órdenes divergentes,
preguntas hechas a mano, pedir un nombre donde debería conducir la detección, o preguntas de
notifier que no tienen salida. Cuando añadas o cambies un asistente, cíñete a este flujo
y a los invariantes de abajo, y actualiza este fichero en el mismo cambio.

## El flujo canónico

1. **Tipo de wizard.** `sermoctl wizard <type>` ejecuta ese asistente; sin tipo,
   el wizard los lista y pregunta (`selectAssistant`). Nunca exijas el tipo.
2. **Seleccionar targets detectados.** Cada asistente detecta lo que es elegible
   (servicios → primero catalog services instalados y activos, luego unidades
   activas opcionales sin catalog service; `docker` → contenedores de la API
   Docker local; `vm` → dominios libvirt/QEMU; `mount` → puntos de montaje de `/etc/fstab`;
   `volume` → volúmenes de almacenamiento montados ahora; `net`/`uplink` → interfaces)
   y los ofrece con `Prompt.MultiChoose`. **Nunca pidas al
   operador que escriba un nombre** — la identidad del target sale de la detección. El
   asistente de servicio termina el grupo del catálogo antes de preguntar por unidades
   sin catálogo.
3. **Propiedades por servicio (solo asistentes que generan servicios).** Para cada
   catalog service seleccionado, pregunta solo las propiedades que realmente difieren por
   servicio, como un override de puerto. El PID/proceso pertenece al catalog
   service bajo `catalog/services`, así que las entradas generadas de catalog service
   normalmente solo deben escribir `uses:` más overrides explícitos. Cuando se detectan
   ficheros de configuración, pregunta si añadir una entrada check-only `watches.config-files`
   que vigile esas rutas; usa un `interval: 60m` de entrada para que el ciclo
   normal del servicio no tenga que ralentizarse. Para unidades activas sin catalog service, pregunta la
   **fuente de PID** porque no hay catalog service del que heredar: una ruta de pidfile
   escribe `pidfile:`; sin pidfile, un ejecutable derivado de la unidad ofrece
   un selector `processes:`. Los asistentes de servicio Docker y VM escriben un
   bloque `control:` por servicio más un watch check-only de solo lectura Docker/libvirt; no
   preguntan selectores de proceso porque los backends de control dan la identidad.
4. **Lote.** Cuando se seleccionó más de un target, pregunta una vez si aplicar
   las respuestas compartidas siguientes a todos (`Prompt.Confirm`).
5. **Estado de monitor.** `Prompt.AskMonitorState` → `monitor: enabled | disabled |
   previous`. Las entradas de montaje que genera `sermoctl wizard mount` incluyen un
   check de storage con `mounted: true`, pero las opera
   `sermoctl mount|umount`; no preguntan ni escriben `monitor:`.
6. **Intervalo.** `Prompt.AskInterval` → `interval:` (en blanco hereda el intervalo
   global del engine). Los pasos 5–6 son `Prompt.AskMonitoring`; las unidades de montaje se lo saltan
   por la misma razón.
7. **Opciones propias del wizard.** En watches: umbrales (`volume`), métricas
   (`net`), sondas (`uplink`), la pregunta de **notifier** (`chooseNotifiers`),
   y el flag opcional de nivel de target `dry_run` cuando el watch generado tiene una
   acción automática real que omitir. En servicios: pregunta si las acciones automáticas
   deben arrancar en dry-run tras las respuestas compartidas de monitor/intervalo. En
  montajes: pregunta solo opciones de seguridad propias del mount, como si Sermo debe
  usar recuento de referencias; las elecciones de umount force/lazy/kill se hacen por acción de CLI/Web.
8. **Vista previa y aceptación.** Renderiza el YAML que se escribirá y confirma.
9. **Limpieza.** Ofrece borrar ficheros gestionados cuyo target **ya no se
   detecta** en el host (`planWizardWatchDeletes` / `planStaleServiceDeletes`
   / `planStaleMountDeletes`).

Los pasos 5–7 se recogen una vez y se reutilizan para todos los targets cuando se aceptó el paso 4;
si no, se preguntan por target. Las unidades de montaje solo recogen sus ajustes
propios del mount en esta forma compartida/por-target.

## Invariantes (no los rompas)

- **Solo prompts compartidos.** Usa los helpers `Prompt` de
  `internal/assist/prompt.go` y `common.go`; nunca hagas a mano una pregunta ni su
  re-prompt/EOF.
- **Sí/no forzado.** `Prompt.Confirm` obliga a un `y`/`n` explícito: una línea vacía
  vuelve a preguntar, no toma un default en silencio. (EOF aborta con
  `ErrInputClosed`, como todo prompt obligatorio.)
- **Sin teclear nombres en las elecciones.** La selección es por número, `all`, o el nombre
  de una opción existente. El wizard nunca inventa ni pide un nombre nuevo.
- **Vocabulario `all` / `none` / `default`.** `all` selecciona todo; `none`
  opta por salir; `default` hereda el ajuste global.
- **`none` y `default` son siempre seleccionables** — incluso cuando la config define
  cero notifiers. El wizard nunca debe bloquearse en la pregunta de notifier.
  - `none` → watch solo-monitor (`notify: [none]`: estado y eventos, sin
    entrega), siempre aceptado.
  - `default` → hereda el notify global cuando hay uno configurado; cuando no hay
    nada que heredar **degrada a solo-monitor** con una nota de una línea. Nunca
    debe volver a preguntar ni abortar. Esta lógica vive una vez en `chooseNotifiers`
    (`internal/assist/notify.go`) — no la reimplementes por asistente.

  Las configs escritas a mano tienen una forma extra: omitir por completo la clave `then`
  en un watch (o bloque por métrica) también es válido y produce comportamiento
  solo-alerta (estado firing visible en la UI web + eventos "firing" en logs, pero sin
  hook/notify ni herencia de globales). El wizard siempre genera un
  `then` explícito (usando `none` / `default` / nombres según lo elegido).
- **Monitor + intervalo en entradas monitorizadas.** Cada watch/servicio generado
  lleva las respuestas de los pasos 5/6 vía `Monitoring.apply`
  (`internal/assist/common.go`). Los ficheros de montaje no son entradas monitorizadas
  y no deben llevar `monitor:` ni `interval:`.
- **Metadatos de categoría del watch.** Los asistentes de watch emiten un `category`
  de primer nivel que coincide con la familia del target (`network`, `storage`, …) para que la Web UI
  pueda agrupar/filtrar los watches creados por el wizard igual que los docs escritos a mano.
- **Dry-run es de nivel de target.** Los asistentes de watch piden `dry_run: true` solo cuando
  el watch generado tiene un efecto automático real (`notify`, notify
  global heredado, `expand` nativo o `kill` nativo). Los asistentes de servicio preguntan lo
  mismo para acciones automáticas de servicio. El flag se escribe en la
  entrada del servicio o watch, nunca dentro de `then`.
- **El setup por lote de servicios evita ruido de puertos.** Cuando se seleccionan varios catalog services,
  el asistente de servicio pregunta si revisar overrides de puerto por servicio.
  El default es no: los servicios generados heredan los puertos del catálogo y el
  wizard pasa directo a las preguntas compartidas de monitor/intervalo/dry-run. Elige
  revisar solo cuando el host corre un servicio en un puerto que no es el del catálogo.
- **Atajos de interfaz.** Los asistentes de red aceptan la keyword `active` en el
  prompt multi-select de interfaces para elegir solo las no-loopback actualmente up.
  El asistente de uplink también acepta `default` cuando se detecta una interfaz de
  ruta por defecto; úsalo para generar checks de ruta/ping/DNS del
  egress a internet actual en vez de elegir a mano túneles o interfaces auxiliares.
- **La detección conduce la limpieza.** El paso 9 solo ofrece ficheros cuyo target está ausente
  de la detección actual; si la detección no está disponible no ofrece nada, así que un
  fichero válido nunca se propone para borrar.
- **La config generada debe validar.** `internal/assist/contract_test.go`
  hace round-trip de la salida de cada builder por `config.Validate`. Manténlo en verde.

## Detección de PID (servicios)

`servicemgr.DetectProcInfo` deriva una ruta de pidfile estable, el ejecutable principal,
la línea de comando y el usuario de la definición init del servicio, best-effort (los
campos desconocidos vuelven `""`):

- **systemd**: `systemctl show` `PIDFile` y `ExecStart` (el token `path=`).
- **OpenRC**: el script init y su override `conf.d` — `pidfile=`, un
  `start-stop-daemon --pidfile`, `--exec`, `command=`, `command_user=`, y
  variables/defaults simples de OpenRC (`${RC_SVCNAME}`, `${VAR:-default}`).
  Se saltan las rutas construidas con `$` desconocidas; las opciones de runtime `/run/openrc/daemons/<unit>/001`
  pueden rellenar pidfiles/ejecutables dinámicos.

Las rutas de pidfile y socket detectadas deben escribirse con la grafía canónica `/run`.
Si el backend reporta `/var/run/...`, normalízala a `/run/...`
antes de añadirla a `catalog/services` o a un servicio generado sin catálogo.
Antes de guardar una ruta recién detectada, resuelve los symlinks en el host
(`readlink -f <path>` o `namei -l <path>`) y quédate con la ruta canónica destino.

`listInstalledCatalogServices` (`internal/cli/wizard_service.go`) rellena cada
`ServiceCandidate.Pidfile`/`Exe`/`Cmd`/`User`. Los catalog services usan esos hechos para
mejorar la definición del catalog service, no la entrada de servicio generada:
escriben `uses:` y heredan selectores de PID/proceso de `catalog/services`.
Las unidades activas sin catálogo escriben un `service: <unit>` escalar más una entrada
check-only básica `watches.service`, y su pregunta de PID viene rellenada desde la detección y solo
acepta rutas de pidfile absolutas.

Los wizards de servicio, Docker y VM escriben los ficheros de servicio generados
bajo un directorio `services/` cargado por `paths.services`.

Toda salida del wizard es un target por fichero. El wizard de volume genera un
documento de watch de storage por sistema de ficheros de almacenamiento montado bajo el directorio
`storages/`, incluyendo block devices locales y sistemas de ficheros de red/distribuidos
como NFS, Ceph y ZFS. No ofrece sistemas de ficheros pseudo/control como
`rpc_pipefs`. Cada documento usa `check.type: storage`.
Las unidades de montaje de primer nivel usan la misma superficie de watch: `sermoctl wizard mount`
lee `/etc/fstab`, escribe un fichero de watch de storage con un bloque `mount:` por
target bajo `mounts/`, añade ese directorio a `paths.watches`, y se
operan con `sermoctl mount|umount`.

## Añadir un wizard nuevo

1. Implementa `assist.Assistant` (`Name`, `Title`, `Run`) en `internal/assist/`.
2. Detecta targets y selecciona con `MultiChoose` (paso 2). Sin prompts de nombre.
3. Para entradas monitorizadas (watches y servicios), recoge monitor + intervalo con
   `Prompt.AskMonitoring`; inyecta con `Monitoring.apply` (pasos 5–6). Agrúpalos
   con `Prompt.Confirm` cuando hay >1. La salida del wizard de mount sigue siendo un documento
   de watch, pero se opera a mano con `sermoctl mount|umount`, así que
   se salta estos campos.
4. Pregunta notifiers (si hay) a través de `chooseNotifiers` (paso 7) — nunca dupliques
   su manejo de `none`/`default`. Si el asistente emite acciones de watch, usa
   `Prompt.AskWatchDryRun` en vez de improvisar `dry_run`.
5. Regístralo en `registry` (`internal/assist/assist.go`).
6. Si tiene targets de host, extiende `detectedTargetKeys` y la ruta de limpieza de
   su tipo de salida (`parseWatchFile`/`planWizardWatchDeletes` para documentos
   de watch, `planStaleMountDeletes` para ficheros de watch de mount con un bloque `mount:`,
   o los helpers de limpieza de servicios para ficheros de servicio) para que funcione la limpieza del paso 9.
7. Añade un test del asistente más un caso en `contract_test.go`.
