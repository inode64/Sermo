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

### Fase 3: Revisar constantes de estados, acciones y eventos

- Usar constantes tipadas de `rules`, `operation`, `servicemgr`, `checks`,
  `process` o `config` cuando el concepto sea exactamente el mismo.
- No derivar conceptos solo por coincidencia textual. Por ejemplo, un event kind
  y un YAML field solo deben compartir constante si representan el mismo
  contrato.
- Documentar excepciones cuando un string visible debe permanecer local.

### Fase 4: Validacion y tests por riesgo

- Para cambios de config: `go test ./internal/config`.
- Para web backend/API: `go test ./internal/app ./internal/web`.
- Para service operations, reload, locks o process discovery: activar
  `sermo-safety-review` y correr paquetes afectados junto con `make check`.
- Añadir tests solo si el refactor descubre comportamiento ambiguo o un bug; no
  añadir fixtures con vocabulario retirado.

### Fase 5: Documentacion lockstep

- No actualizar docs de usuario cuando el refactor no cambia comportamiento.
- Si se cambia una forma publica de config, actualizar docs, ejemplos y tests en
  el mismo parche.
- Si se introduce una excepcion o una razon de seguridad, documentarla en el
  owner y, si es usuario-facing, en `docs/`.

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
