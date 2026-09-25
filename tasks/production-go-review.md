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
- [ ] **GO-005 — No convertir campos ausentes de meminfo en mediciones.**
  `internal/checks/memory.go`, `swap.go` y `internal/metrics/procfs.go`: los
  checks ignoraban las banderas Have*, confundiendo información ausente con
  memoria disponible cero o ausencia de swap. Rechazar muestras incompletas o
  incoherentes y un total de RAM cero; conservar el swap realmente deshabilitado.
- [ ] **GO-006 — No interpretar vmstat ilegible como actividad de swap cero.**
  `internal/checks/swap.go`: errores al abrir vmstat o contadores ausentes se
  convertían en cero y alteraban la línea base. Propagar los fallos y exigir
  ambos contadores; la consulta de capacidad no debe depender de vmstat.
- [ ] **GO-007 — Acotar las líneas recibidas por sondas de texto.**
  `internal/conn/conn.go`: ReadString podía reservar memoria sin límite ante
  una línea sin salto final. Limitar las líneas sin perder la distinción entre
  protocolos estrictos y banners que admiten EOF tras datos.
