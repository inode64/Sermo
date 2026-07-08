# Plan de refactor

Este documento convierte las reglas actuales de `AGENTS.md` y las skills locales
en una propuesta de refactor incremental para Sermo. La prioridad es reducir
literales conceptuales, duplicacion y deriva entre superficies sin cambiar el
comportamiento publico ni relajar las garantias de seguridad.

## Fuentes de verdad actuales

`AGENTS.md` define las reglas que deben guiar cualquier refactor:

- Trabajar en el checkout actual, preservar cambios ajenos y no commitear salvo
  peticion explicita.
- Reutilizar el owner existente antes de crear helpers o paquetes nuevos.
- Evitar magic literals en produccion; crear o reutilizar constantes para estados,
  tipos, modos, backends, claves YAML/JSON, unidades, umbrales, defaults y
  factores de conversion.
- Mantener un vocabulario unico para cada concepto; los nombres publicos de
  structs, YAML, JSON y APIs mandan.
- No cambiar la estructura publica de configuracion salvo migracion explicita.
- Mantener paths runtime bajo `/run` y normalizar `/var/run` solo como
  compatibilidad Linux/init.
- Mantener todas las acciones de servicio en `internal/operation`; `app/` y
  `cli/` no deben llamar backends ni enviar senales directamente.
- Usar APIs nativas o runners inyectables con timeout; no `os/exec` disperso.
- Mantener probes de `internal/conn` ligados a `cfg.Interface`.
- Actualizar docs y ejemplos cuando cambie comportamiento observable o config.
- Construir checks, watches, notifiers y rule actions desde builders centrales.
- Evitar trabajo caro en hot paths de `sermod`.
- Validar con `make check` para cambios Go/YAML antes de darlos por cerrados.

## Skills disponibles y uso propuesto

Las skills locales cubren el refactor por dominio:

- `sermo-go-implementation`: cambios Go en CLI, daemon, config, checks, locks,
  rules y operaciones.
- `golang-patterns`: refactors Go idiomaticos, simplificacion y organizacion.
- `golang-testing` y `sermo-test-engineer`: pruebas table-driven, resolucion de
  config, backends, rules, locks, guards, process discovery y operaciones seguras.
- `sermo-config-schema`: cambios de YAML/config, catalogo, services, watches,
  checks, guards, locks, rules y stop policies.
- `sermo-safety-review`: cualquier cambio que toque start/stop/restart/reload,
  signals, process matching, locks, preflight, guards o remediation.
- `sermo-rule-engine`: reglas, condition trees, windows, remediation y guards.
- `sermo-process-discovery`: pidfiles, `/proc`, cgroups, arboles de procesos,
  residuales y politicas de senales.
- `sermo-linux-service`: systemd/OpenRC, deteccion de backend, status y comandos
  de init.
- `sermo-docs-writer`: README, guias, ejemplos YAML, reglas, seguridad y runbooks.
- `sermo-project-architect`: cambios de arquitectura, boundaries, flujo de
  config, daemon/CLI y secuenciacion mayor.
- `sermo-packaging`, `sermo-profile-author`, `sermo-remote-testing` y
  `accessibility`: usar solo cuando el refactor toque packaging, perfiles de
  servicios, pruebas remotas o UI accesible.

## Estado actual del refactor

El trabajo aplicado hasta ahora se concentra en constantes y paths derivados:

- `metrics.PercentScale` centraliza la conversion de ratios a porcentaje.
- Paths de configuracion se derivan desde las claves dueñas en `internal/config`
  para `engine.*`, `paths.*`, `defaults.*`, `web.*`, `notifiers.*`,
  `watches.*`, `then.*`, `policy.*`, `stop_policy.*`, `processes.*`,
  `pidfiles.*`, `mount.*`, `variables.*`, `service.*`, `also_service.*`,
  `version.on_change.*` y `reload_on_change.paths`.
- Reload y control usan paths/labels derivados para `reload.*` y `control.*`.
- Acciones/status compartidos en web/API/eventos se derivan de `rules` y
  `operation` cuando el concepto coincide.
- `config.DaemonPIDFilename` evita duplicar `sermod.pid` entre daemon y CLI.
- El build web tiene constantes locales para sus assets fuente.

Este estado no debe tratarse como una migracion de configuracion: los mensajes y
paths publicos siguen usando la misma forma visible.

## Propuesta de fases

### Fase 0: Estabilizar la rama actual

- Revisar el diff completo por ownership: `internal/config`, `internal/app`,
  `internal/web`, `internal/operation`, `internal/metrics`, `internal/cli`.
- Confirmar que no hay cambios de comportamiento ni de YAML publico.
- Mantener el commit pendiente hasta que se pida explicitamente.

Validacion:

```sh
go test ./internal/config
go test ./internal/app ./internal/checks ./internal/cli ./internal/config ./internal/dockerctl ./internal/metrics ./internal/operation ./internal/virt ./internal/web
make check
```

### Fase 1: Inventario final de literales conceptuales

- Buscar literales restantes por dominio y clasificarlos antes de tocar codigo.
- Extraer solo conceptos con owner claro o repeticion real.
- No convertir fixtures, ejemplos YAML realistas, strings de error locales de un
  solo uso, comentarios explicativos ni datos de tests.

Comandos utiles:

```sh
rg -n '"[a-z_]+(\.[a-z_]+)+"' internal --glob '!**/*_test.go'
rg -n '[^A-Za-z0-9_]100(\.0)?|\*\s*100|/\s*100' internal
```

Estado ejecutado:

- Literales con punto restantes en Go productivo: mayoritariamente constantes de
  protocolo o formato externo (`conn` extra keys, NUT variable names, RPC aliases,
  build metadata `vcs.*`, filenames de assets, `sermod.pid`, `sermo.db`). No se
  deben mover salvo que un owner los comparta realmente.
- Numeros `100` restantes en Go productivo: clasificados como divisores/clases
  HTTP, IDs de protocolo RPC/NFS/NSM/portmap, escalas de tiempo/procfs/SNMP/NTP,
  limites de validacion porcentual y defaults de UI/API. Los factores de
  porcentaje Go ya estan centralizados en `metrics.PercentScale`.
- Web frontend: `internal/web/src/app.js` todavia contiene conversiones
  porcentuales y segundos/milisegundos locales. Son candidatos de UI para un
  refactor posterior, pero cualquier cambio exige `make web` y revisar el HTML
  generado.
- Decision de alcance: fase 1 queda como inventario sin cambios de runtime. Las
  extracciones que merezcan codigo pasan a fases 2 y 3, donde se revisan helpers
  de paths y acciones/estados por owner.

### Fase 2: Consolidar helpers de paths sin sobregeneralizar

- Mantener helpers junto al owner cuando el path es local a un validador.
- Promover helpers a package-level solo cuando ya se usan en varios archivos del
  mismo paquete.
- Evitar un paquete generico de "paths" si solo une strings: añadiria coupling y
  no resolveria un problema real.

Candidatos actuales:

- Revaluar si los helpers de `internal/config` deben vivir en un archivo dedicado
  como `paths.go` solo cuando crezcan mas usos cruzados.
- Mantener `control.*`, `reload.*`, `watches.*` y `variables.*` cerca de sus
  validadores mientras no haya una API externa que los necesite.

Estado ejecutado:

- Los helpers de paths de validacion/configuracion cruzados se movieron a
  `internal/config/field_paths.go`.
- No se creo un paquete nuevo ni una abstraccion global. El owner sigue siendo
  `internal/config`.
- `control.*`, `reload.*`, `watches.*`, `variables.*`, `policy.*`, `web.*`,
  `notifiers.*`, `mount.*`, `pidfiles.*` y helpers relacionados conservan los
  mismos strings resultantes; el cambio es de ubicacion y ownership.

### Fase 3: Revisar constantes de estados, acciones y eventos

- Usar constantes tipadas de `rules`, `operation`, `servicemgr`, `checks`,
  `process` o `config` cuando el concepto sea exactamente el mismo.
- No derivar conceptos solo por coincidencia textual. Por ejemplo, un event kind
  y un YAML field solo deben compartir constante si representan el mismo
  contrato.
- Documentar excepciones cuando un string visible debe permanecer local.

Estado ejecutado:

- `internal/app/event.go`, `internal/web/server.go`, `internal/operation`,
  `internal/rules` y `internal/servicemgr` ya derivan las acciones/status que
  comparten contrato exacto.
- Se dejaron locales los textos que coinciden pero no son el mismo contrato:
  estados externos de Docker/libvirt/systemd, estados visuales del frontend,
  nombres JSON/DOM, estados de locks/mounts y mensajes/event kinds propios de
  cada superficie.
- Safety review: bajo riesgo. No se modifico ningun camino de operacion,
  guard/preflight/lock, kill/signal ni remediation; solo se documento la
  decision de no crear coupling por coincidencia textual.

### Fase 4: Validacion y tests por riesgo

- Para cambios de config: `go test ./internal/config`.
- Para web backend/API: `go test ./internal/app ./internal/web`.
- Para service operations, reload, locks o process discovery: activar
  `sermo-safety-review` y correr paquetes afectados junto con `make check`.
- Añadir tests solo si el refactor descubre comportamiento ambiguo o un bug; no
  añadir fixtures con vocabulario retirado.

Estado ejecutado:

- `go test ./internal/config`: pasa.
- `go test ./internal/app ./internal/web`: pasa.
- `make check`: pasa completo (`go vet`, `staticcheck`, `revive`,
  `golangci-lint`, `govulncheck`, `go test ./...`).
- No se añadieron tests nuevos porque las fases 1-3 no introdujeron
  comportamiento nuevo; fase 2 fue movimiento de helpers con cobertura existente.

### Fase 5: Documentacion lockstep

- No actualizar docs de usuario cuando el refactor no cambia comportamiento.
- Si se cambia una forma publica de config, actualizar docs, ejemplos y tests en
  el mismo parche.
- Si se introduce una excepcion o una razon de seguridad, documentarla en el
  owner y, si es usuario-facing, en `docs/`.

Estado ejecutado:

- No hubo cambios de estructura publica YAML/JSON, CLI, Web API ni comportamiento
  observable de operaciones.
- No se actualizaron README, `docs/` ni ejemplos porque el refactor solo movio
  literales/helpers internos y documento el plan.
- La excepcion relevante queda documentada aqui: los strings que coinciden entre
  superficies no se comparten si no representan el mismo contrato.

### Fase 6: Constantes del Web UI

- Extraer literales conceptuales del frontend sin cambiar el contrato del Web
  API ni la estructura DOM.
- Mantener las constantes dentro de `internal/web/src/app.js`, que es el owner de
  formato, tiempos, umbrales visuales y geometria de graficas del dashboard.
- Regenerar `internal/web/index.html` con `make web` despues de editar
  `internal/web/src/`.

Estado ejecutado:

- Se centralizaron factores de porcentaje, conversiones de segundos y
  milisegundos, ventanas moviles de 1h/24h/7d/30d/1y, umbrales visuales de uso,
  umbrales SLA, longitud de preview de eventos, ticks de refresco y geometria de
  graficas.
- `slaColor`, `slaWindowSpanMs`, `pctClamp`, formatters de duracion/edad,
  barras de uso, metric charts, SLA charts y previews de eventos consumen esos
  nombres en vez de numeros dispersos.
- No se cambio comportamiento publico; el HTML generado se actualizo solo como
  artefacto derivado del build web.
- Validacion ejecutada: `make web`, `go test ./internal/web` y `make check`
  pasan.

### Fase 7: Horizonte de retencion compartido

- Usar `state.DefaultHistoryRetention` como owner unico del horizonte historico
  retenido para SLA, metricas y eventos.
- Derivar desde ese owner el maximo de ventana que el Web API acepta para series
  historicas, evitando repetir `366 * 24h` en `internal/web`.

Estado ejecutado:

- `internal/web/server.go` importa `internal/state` y define
  `maxSeriesWindow = state.DefaultHistoryRetention`.
- La prueba existente `TestSeriesSinceParsing` sigue cubriendo que una ventana
  excesiva se capea al valor maximo.
- Validacion ejecutada: `go test ./internal/web` y `make check` pasan.

### Fase 8: Limites locales de eventos

- Nombrar los limites numericos de eventos que tienen politicas distintas por
  superficie, sin compartir constantes cuando el contrato no es el mismo.
- Mantener separado el limite del listado CLI, el limite/cap del Web API y el
  numero de eventos que el resumen de actividad escanea internamente.

Estado ejecutado:

- `internal/cli/cli.go` usa `defaultEventsListLimit` para el listado
  `sermoctl events`.
- `internal/app/webbackend.go` usa `activitySummaryEventScanLimit` para el
  rollup del dashboard.
- No se cambiaron defaults publicos ni limites de API.
- Validacion ejecutada: `go test ./internal/app ./internal/cli` y `make check`
  pasan.

### Fase 9: Clases HTTP locales

- Nombrar el divisor de clase HTTP (`status / 100`) donde se usa en runtime.
- Mantener constantes locales porque el webhook solo clasifica exito de
  transporte y `checks` compara codigos configurados; no son el mismo contrato
  publico aunque usen la misma matematica.

Estado ejecutado:

- `internal/notify/webhook.go` usa constantes locales para divisor de clase HTTP
  y clase de exito.
- `internal/checks/types.go` usa una constante local para calcular la clase de
  estado en `statusMatcher`.
- Validacion ejecutada: `go test ./internal/notify ./internal/checks` y
  `make check` pasan.

### Fase 10: Dias fijos de historial y SLA

- Mantener `internal/state` como owner de la retencion historica y de las
  ventanas SLA persistidas.
- Nombrar los multiplicadores de dias fijos para que quede explicito que el
  ano/mes de SLA son ventanas rolling, no limites de calendario.

Estado ejecutado:

- `DefaultHistoryRetention` se deriva de `historyRetentionDays` y `hoursPerDay`.
- Los spans SLA de semana/mes/ano se derivan de `slaRollingWeekDays`,
  `slaRollingMonthDays` y `slaRollingYearDays`.
- `internal/web` sigue heredando el horizonte maximo a traves de
  `state.DefaultHistoryRetention`.
- Validacion ejecutada: `go test ./internal/state ./internal/web` y
  `make check` pasan.

### Fase 11: Ventana por defecto de series

- Mantener `internal/state` como owner del lookback por defecto usado cuando una
  consulta de series historicas omite `since`.
- Reutilizar el mismo valor desde CLI y Web sin cambiar el default publico de
  24h.

Estado ejecutado:

- `state.DefaultSeriesWindow` define el lookback normal de series.
- `sermoctl sla --series` y el Web API derivan sus defaults desde
  `state.DefaultSeriesWindow`.
- Validacion ejecutada: `go test ./internal/state ./internal/cli ./internal/web`
  y `make check` pasan.

### Fase 12: Constantes del informe HTML de servicios

- Mantener el HTML del informe de `sermoctl services --notify` en su owner
  actual, `internal/cli/services_report.go`.
- Nombrar colores, fuentes y formato de fecha usados repetidamente en el email
  HTML sin cambiar la salida ni introducir un sistema de plantillas nuevo.

Estado ejecutado:

- Se centralizaron colores, fuentes y layout de fecha del informe.
- Las cabeceras de tabla repetidas usan `writeReportHeaderCell`.
- El informe conserva el mismo contenido y estilos inline compatibles con email.
- Validacion ejecutada: `go test ./internal/cli` y `make check` pasan.

### Fase 13: Nombre del check de metricas del daemon

- Mantener `daemonMetricCheck` como nombre canonico del check logico que agrupa
  las metricas de `sermod`.
- No reutilizarlo para nombres de logger ni otros conceptos que solo comparten
  el texto `sermod`.

Estado ejecutado:

- `internal/app/daemonmetrics.go` usa `daemonMetricCheck` al devolver
  `web.MetricSeries`.
- Se dejaron locales los logger names de `internal/app/event.go`.
- Validacion ejecutada: `go test ./internal/app` y `make check` pasan.

### Fase 14: Mensaje requerido de ports

- Mantener la validacion de `ports` en `internal/config/validate_checks.go`.
- Nombrar el mensaje reutilizado para specs de puertos vacias, conservando el
  mismo ejemplo visible al operador.

Estado ejecutado:

- `validatePortSpec` usa `portSpecRequiredMessage` para los dos caminos que
  reportan una spec vacia.
- Validacion ejecutada: `go test ./internal/config` y `make check` pasan.

### Fase 15: Constantes de plantillas de notificacion

- Mantener el loader de plantillas en `internal/notify/template.go`.
- Nombrar el sufijo de archivo, los nombres internos de subtemplate y la opcion
  de `text/template` para claves ausentes.

Estado ejecutado:

- `LoadTemplate` usa `templateFileSuffix`.
- `parseTemplate` usa constantes para `:subject`, `:body` y
  `missingkey=zero`.
- Validacion ejecutada: `go test ./internal/notify` y `make check` pasan.

### Fase 16: Defaults de utmp compartidos

- Mantener los paths canonicos/fallback de utmp en `internal/utmp`, que es el
  owner del parser de sesiones.
- Reutilizarlos desde el notifier TTY/Wall sin exponer una slice mutable.

Estado ejecutado:

- `utmp.DefaultPaths` devuelve una copia de los paths por defecto en Linux y
  `nil` fuera de Linux.
- `internal/notify/tty_linux.go` usa `utmp.DefaultPaths()` en vez de duplicar
  `/run/utmp` y `/var/run/utmp`.
- Validacion ejecutada: `go test ./internal/utmp ./internal/notify` y
  `make check` pasan.

### Fase 17: Paths de os-release compartidos

- Mantener en `internal/config` el orden canonico de lectura de `os-release`,
  usado para selectores `${os}`.
- Reutilizar ese orden desde el backend web cuando muestra el nombre amigable
  del sistema operativo.

Estado ejecutado:

- `config.OSReleasePaths()` devuelve los paths de `os-release` en prioridad.
- `config.osReleaseID` y `app.osPrettyName` consumen esa funcion en vez de
  duplicar `/etc/os-release` y `/usr/lib/os-release`.
- Validacion ejecutada: `go test ./internal/config ./internal/app` y
  `make check` pasan.

### Fase 18: Directorios runtime de init compartidos

- Mantener los directorios runtime de systemd/OpenRC en `internal/servicemgr`,
  que es el owner de deteccion y normalizacion de init backends.
- Reutilizarlos desde `internal/config` para detectar el built-in `${init}`.

Estado ejecutado:

- `servicemgr.SystemdRuntimeDir` y `servicemgr.OpenRCRuntimeDir` exponen los
  directorios runtime usados por el detector.
- `config.detectInit` usa esas constantes en vez de duplicar `/run/systemd/system`
  y `/run/openrc`.
- Validacion ejecutada: `go test ./internal/config ./internal/servicemgr` y
  `make check` pasan.

### Fase 19: Directorio de daemons OpenRC derivado

- Mantener los paths OpenRC relacionados dentro de `internal/servicemgr`.
- Derivar el directorio de metadata de daemons desde el runtime dir OpenRC para
  evitar repetir el prefijo `/run/openrc`.

Estado ejecutado:

- `openRCDaemonsDir` se deriva de `openRCRuntimeDir + "/daemons"`.
- Validacion ejecutada: `go test ./internal/servicemgr` y `make check` pasan.

### Fase 20: Puerto TLS por defecto en checks

- Mantener los defaults de puertos de checks dentro de `internal/checks`.
- Reutilizar un unico puerto TLS por defecto para el check de certificado y el
  WebSocket seguro.

Estado ejecutado:

- `defaultTLSPort` reemplaza el literal `443` duplicado.
- `cert` y `websocket` consumen el mismo default TLS.
- Validacion ejecutada: `go test ./internal/checks` y `make check` pasan.

### Fase 21: Constantes sysfs de interfaces de red

- Mantener el vocabulario de `/sys/class/net` en `internal/checks`, owner del
  muestreo de interfaces.
- Reutilizar desde el wizard las constantes de path, archivos y parseo de flags
  sysfs sin mover logica ni cambiar la salida generada.

Estado ejecutado:

- `checks` expone las constantes sysfs de interfaces que ya usaba su sampler.
- `sermoctl wizard` consume esas constantes en su fallback de descubrimiento de
  interfaces.
- Validacion ejecutada: `go test ./internal/checks ./internal/cli` y
  `make check` pasan.

### Fase 22: Sufijo systemd service compartido

- Mantener el vocabulario de unidades systemd en `internal/servicemgr`.
- Reutilizar el sufijo `.service` desde el wizard en vez de duplicarlo en CLI.

Estado ejecutado:

- `servicemgr.SystemdServiceSuffix` expone el sufijo ya usado por la
  normalizacion de unidades systemd.
- El wizard de servicios usa ese owner para deduplicar, comparar y derivar
  nombres desde unidades systemd.
- Validacion ejecutada: `go test ./internal/servicemgr ./internal/cli` y
  `make check` pasan.

### Fase 23: Vocabulario Linux `/proc/net` compartido

- Crear un owner pequeno para las constantes de las tablas Linux `/proc/net/*`
  que hoy consumen el wizard de servicios y el probe `dhclient`.
- Compartir rutas, indices de campos, estados codificados y constantes de parseo
  sin mover la logica de parsing ni cambiar mensajes de error.

Estado ejecutado:

- `internal/procnet` centraliza paths TCP/UDP, indices, estados y bases de
  parseo de sockets `/proc/net`.
- `internal/cli` y `internal/conn` consumen ese vocabulario comun.
- Validacion ejecutada: `go test ./internal/procnet ./internal/conn ./internal/cli`
  y `make check` pasan.

### Fase 24: Helpers procfs de procesos

- Mantener la construccion de paths `/proc/<pid>/...` en `internal/process`,
  owner de la lectura de identidades de procesos.
- Reutilizar esos helpers desde los scans de open files y blockers de mounts sin
  cambiar los criterios de matching ni la politica de senales.

Estado ejecutado:

- `process.PIDPath` y las constantes `ProcFileFD`, `ProcFileCWD` y
  `ProcFileRoot` reemplazan rutas `/proc/<pid>/...` reconstruidas localmente.
- `process.TrimDeletedSuffix` centraliza el sufijo kernel ` (deleted)` usado al
  interpretar symlinks procfs.
- Validacion ejecutada: `go test ./internal/process ./internal/app ./internal/mountctl`
  y `make check` pasan.

### Fase 25: Roots de recursos de checks compartidos

- Mantener roots de recursos Linux usados por checks en `internal/checks`.
- Reutilizarlos desde diagnosticos en vez de repetir `/proc/pressure` y
  `/sys/class/block`.

Estado ejecutado:

- `checks.ProcPressureRootPath` expone el root PSI que usa el pressure check.
- `checks.SysBlockPath` expone el root sysfs usado para validar dispositivos
  `diskio` en diagnosticos.
- Validacion ejecutada: `go test ./internal/checks ./internal/diag` y
  `make check` pasan.

### Fase 26: Helper `/proc/self` compartido

- Mantener la construccion de paths procfs en `internal/process`.
- Reutilizar `/proc/self/fd` desde CLI sin duplicar el path literal.

Estado ejecutado:

- `process.SelfPath` construye paths bajo `/proc/self`.
- La deteccion de terminal de `sermoctl` usa `process.SelfPath(process.ProcFileFD)`.
- Validacion ejecutada: `go test ./internal/process ./internal/cli` y
  `make check` pasan.

### Fase 27: Paths procfs por proceso en metricas

- Mantener los nombres y paths `/proc/<pid>/...` en `internal/process`.
- Reutilizarlos desde `internal/metrics` para lecturas por proceso, dejando los
  ficheros globales de `/proc` en el owner de metricas.

Estado ejecutado:

- `process` expone nombres procfs por proceso para `stat`, `statm`, `status`,
  `io`, `fd` y `task`.
- `metrics.OSReader` usa `process.PIDPath` para CPU, start time, RSS, swap, IO,
  FD count y thread count por PID.
- Validacion ejecutada: `go test ./internal/process ./internal/metrics` y
  `make check` pasan.

### Fase 28: Start ticks de locks via procfs compartido

- Mantener la ruta `/proc/<pid>/stat` en `internal/process`.
- Reutilizarla desde locks para leer `owner_start_ticks` sin cambiar la
  semantica de deteccion de locks stale.

Estado ejecutado:

- `locks.OSProcessProber.StartTicks` usa
  `process.PIDPath(pid, process.ProcFileStat)`.
- Se elimina el formato local `/proc/%d/stat`.
- Validacion ejecutada: `go test ./internal/locks ./internal/process` y
  `make check` pasan.

### Fase 29: Timeout libvirt compartido

- Mantener defaults de conexion libvirt en `internal/conn`, que ya expone socket
  y puerto para checks y control de VMs.
- Reutilizar el timeout por defecto desde `internal/virt`.

Estado ejecutado:

- `conn.DefaultLibvirtTimeout` expone el timeout fallback de conexiones libvirt.
- `virt.timeoutFromContext` consume ese default en vez de duplicar `10s`.
- Validacion ejecutada: `go test ./internal/conn ./internal/virt` y
  `make check` pasan.

### Fase 30: Factores binarios compartidos

- Crear un owner estrecho para factores de conversion numericos que cruzan
  paquetes y no pertenecen solo a metricas, checks, estado o libvirt.
- Reutilizarlo en lecturas procfs, swap, cache SQLite y memoria libvirt.

Estado ejecutado:

- `internal/units` define `BytesPerKiB` y `KiBPerMiB`.
- `internal/metrics`, `internal/checks`, `internal/state` e `internal/conn`
  consumen esos factores en vez de duplicar `1024`.
- Validacion ejecutada: `go test ./internal/units ./internal/metrics
  ./internal/checks ./internal/state ./internal/conn` y `make check` pasan.

### Fase 31: Unidades metricas reutilizadas en lecturas

- Mantener `internal/metrics` como owner de unidades canonicas de metricas.
- Reutilizar solo unidades con contrato identico desde las lecturas del Web
  backend, sin cambiar representaciones locales distintas.

Estado ejecutado:

- `internal/app/checkreadings.go` usa `metrics.MetricUnitMegabytesPerSecond`,
  `metrics.MetricUnitCelsius` y `metrics.MetricUnitRPM`.
- Se mantiene local `C` sin simbolo porque es una representacion distinta de la
  lectura actual.
- Validacion ejecutada: `go test ./internal/app` y `make check` pasan.

## Guardrails

- No cambiar YAML, JSON, CLI ni Web API publicos durante este refactor.
- No mover logica de seguridad fuera de `internal/operation`.
- No introducir compatibilidad dual ni aliases para config retirada.
- No pasar acciones automaticas por caminos distintos a los manuales.
- No convertir literales de tests/fixtures en constantes salvo que reduzca
  ambiguedad real.
- No ocultar textos de error de un solo uso detras de constantes si pierden
  claridad.
- No crear abstracciones globales por estetica; primero reutilizar owners.

## Criterio de terminado

Una fase se considera cerrada cuando:

- El diff queda limitado al owner previsto.
- No hay cambios ajenos revertidos ni artefactos temporales sin trackear.
- Los tests focales del paquete pasan.
- `make check` pasa cuando hay cambios Go/YAML.
- El resultado puede inspeccionarse con `git diff` sin tener que reconstruir el
  contexto mental de otra rama o cola oculta.
