# Revisión del Go de producción

Revisión iniciada sobre `c8bc1d0f`. Se excluyen los archivos `*_test.go` y
`execxtest` de la revisión. Cada tarea identifica un fallo observado por lectura
del código y su corrección; las comprobaciones estáticas no sustituyen a las
pruebas de ejecución. Este registro no afirma una auditoría exhaustiva.

- [x] **GO-001 — Propagar errores al leer respuestas de sondas HTTP.**
  `internal/conn/http_client.go`: un cuerpo parcial con JSON válido y un error
  de lectura se aceptaba como respuesta completa. Propagar el error antes de
  interpretar el cuerpo, manteniendo el límite de lectura existente.
- [x] **GO-002 — Respetar el estado HTTP de Prometheus.**
  `internal/conn/prometheus.go`: un HTTP 500 con JSON `status: success` podía
  declarar sano el servicio. Exigir HTTP 200 para una respuesta API reconocida;
  conservar el fallback cuando el contenido no corresponde al endpoint API.
- [x] **GO-003 — Acotar la respuesta RPC completa.**
  `internal/conn/nfs.go`: cada fragmento estaba limitado, pero la concatenación
  no. Limitar el tamaño acumulado antes de reservar y añadir otro fragmento.
- [x] **GO-004 — Acotar los registros FastCGI acumulados.**
  `internal/conn/fpm.go`: un peer podía enviar registros sin END_REQUEST y
  hacer crecer STDOUT/STDERR hasta agotar memoria. Acotar toda la respuesta,
  incluidos cabeceras, padding y registros no reconocidos.
- [x] **GO-005 — No convertir campos ausentes de meminfo en mediciones.**
  `internal/checks/memory.go`, `swap.go` y `internal/metrics/procfs.go`: los
  checks ignoraban las banderas Have*, confundiendo información ausente con
  memoria disponible cero o ausencia de swap. Rechazar muestras incompletas o
  incoherentes y un total de RAM cero; conservar el swap realmente deshabilitado.
- [x] **GO-006 — No interpretar vmstat ilegible como actividad de swap cero.**
  `internal/checks/swap.go`: errores al abrir vmstat o contadores ausentes se
  convertían en cero y alteraban la línea base. Propagar los fallos y exigir
  ambos contadores; la consulta de capacidad no debe depender de vmstat.
- [x] **GO-007 — Acotar las líneas recibidas por sondas de texto.**
  `internal/conn/conn.go`: ReadString podía reservar memoria sin límite ante
  una línea sin salto final. Limitar las líneas sin perder la distinción entre
  protocolos estrictos y banners que admiten EOF tras datos.

## Validación y límites

- `go vet` sobre los archivos de producción seleccionados por `go list` en
  `internal/conn`, `internal/checks` e `internal/metrics`: correcto.
- `staticcheck -tests=false -checks=all ./cmd/... ./internal/... ./tools/...`:
  correcto.
- `make fmt-check markdown-check` y `git diff --check`: correctos.
- `bin/custom-gcl run --tests=false ./cmd/... ./internal/... ./tools/...`:
  sin incidencias nuevas; mantiene dos avisos `unparam` en
  `internal/process/cache.go:99` y `internal/process/discover.go:75`. Ambos
  se reproducen en un checkout separado del commit inicial `c8bc1d0f`.
  No se han añadido exclusiones ni supresiones.

Los tests no se han revisado, modificado ni ejecutado. Tampoco se ha ejecutado
`make check`, que incluye tests; queda pendiente la validación de ejecución.
La revisión se ha centrado en sondas, lecturas de métricas y propagación de
errores, sin afirmar que todo el Go de producción esté libre de fallos.
