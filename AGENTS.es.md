# Sermo — convenciones del proyecto

Este fichero contiene decisiones del repositorio específicas para agentes. No
es un segundo manual de usuario ni una copia de la configuración ejecutable.
Usa estas fuentes de verdad:

- comportamiento actual: código y tests;
- comportamiento y configuración públicos: `docs/` y ejemplos validados;
- comportamiento planificado: [TODO.es.md](TODO.es.md);
- comandos de validación: `Makefile` y workflows de CI;
- política de analizadores Go: `.golangci.yml`, `.custom-gcl.yml` y `.semgrep/`;
- comportamiento de la Web UI: `internal/web/src/`, sus tests y
  [docs/webui-representation.es.md](docs/webui-representation.es.md);
- seguridad operacional: `internal/operation`, `internal/process`,
  `internal/locks` y [docs/safety.es.md](docs/safety.es.md).

Si la prosa y la implementación discrepan, informa del desacuerdo, determina el
comportamiento esperado a partir de la petición y la evidencia ejecutable, y
actualiza todas las fuentes afectadas en el mismo cambio. No conserves una
contradicción tratando este fichero como más autoritativo que el código que
describe.

## AI / agent workflow — standard git commits

Antes de editar, ejecuta:

```sh
git status --short --branch
```

Trabaja en el checkout actual salvo que el usuario pida una rama. Conserva todos
los cambios relacionados o no, seguidos y sin seguimiento. Busca con `rg`,
extiende el propietario existente, añade tests enfocados y mantén el parche
acotado.

Ejecuta checks dirigidos durante el desarrollo. Termina con el menor gate
completo:

| Cambio | Comando final obligatorio |
|---|---|
| Solo Markdown | `make markdown-check` |
| Solo YAML | `make yaml-validate` |
| Go, scripts, Web UI, ficheros de build o cambios mixtos | `make check` |

`make check` es el único gate completo; el `Makefile` posee sus fases actuales.
No lo dupliques con una segunda pasada de `go build`, `make lint` o `go test`.
Tras editar `internal/web/src/`, ejecuta `make web` antes del gate final y mantén
el `internal/web/index.html` regenerado en el parche.

Haz commit solo cuando el usuario pida un commit, merge o integración
equivalente. Usa:

```text
<type>(<optional-scope>): <concise description>

Objective: <outcome>
Invariant: <behavior or safety property preserved>
Evidence: <checks and runtime validation actually run>
Limitations: <known boundary or None.>
```

Los tipos válidos son `feat`, `fix`, `refactor`, `test`, `docs`, `build`,
`chore`, `ci` y `perf`. No identifiques a un agente como autor. No hagas push ni
merge salvo petición explícita, y nunca dejes un staging parcial sin explicar.

Para trabajo en la flota, sigue la skill `sermo-remote-testing` y su flujo por
etapas. Una acción de operador es real incluso si la remediación del daemon está
configurada con `dry_run: true`; usa la ruta de acciones del CLI o dashboard de
Sermo e investiga cualquier fallo que Sermo no pueda reparar con seguridad.

## Reuse and shared behavior

Trata un ejemplo o test fallido como evidencia de un invariante, no como todo el
alcance. Busca rutas equivalentes en CLI, daemon, web, servicios y watches antes
de añadir lógica. Prefiere, por orden: reutilizar el propietario sin cambios,
extenderlo, añadir un pequeño helper privado a su lado y crear un paquete
compartido solo cuando la propiedad cruce fronteras de paquete.

No crees un segundo parser, validador, monitor, ruta de notificación o dispatcher
de acciones para el mismo concepto. Un check, opción o comportamiento de uso
general pertenece tanto a servicios como a host watches salvo que se documente
una limitación en el propietario del código y en la documentación de usuario.

## Constantes y valores repetidos

Reutiliza constantes tipadas y enums del paquete propietario. Nombra valores con
significado como estados, clases, claves de config, protocolos, unidades,
defaults, timeouts y umbrales aunque aparezcan una sola vez. Crea una constante
para repetición ordinaria cuando aparezca más de tres veces. No ocultes datos de
fixtures o un mensaje de error puntual en una constante sin beneficio de
corrección.

## Naming and terminology

Usa el campo del modelo público como vocabulario canónico en código,
comentarios, JSON, YAML y docs. No introduzcas casi-sinónimos para un concepto
existente. Cuando el término canónico colisione con un builtin de Go, conserva
el nombre público y usa el alias local establecido; por ejemplo, `Max` / `"max"`
usa `limit` en una variable local.

## Configuration structure changes

Para configuración propiedad de Sermo, prefiere una forma canónica y elimina la
anterior del parseo, validación, ejemplos, docs y tests en el mismo cambio. La
compatibilidad requiere una petición explícita o una necesidad externa o de
seguridad. No añadas fixtures que mantengan vivos nombres retirados.

El sugar de catálogo solo se permite cuando la resolución lo transforma al
árbol de runtime canónico y lo elimina. Documenta sugar nuevo en
[docs/services.es.md](docs/services.es.md).

## Runtime paths

Escribe rutas volátiles bajo `/run`, incluidos pidfiles, sockets y locks.
Normaliza rutas `/var/run/...` informadas por el host a `/run/...`; esta
compatibilidad Linux no es una segunda escritura de config Sermo. Resuelve
symlinks antes de añadir una ruta de catálogo o generada para que los aliases no
se conviertan en targets duplicados.

## Configuration file granularity

Usa un documento YAML de una clase de target por fichero. La clase procede del
subdirectorio de catálogo o del directorio configurado de servicios/watches, así
que omite campos `kind:` redundantes. Un fragmento notifier es la excepción
estrecha: su mapa superior `notifiers:` contiene exactamente una entrada con
nombre. Los bundles de referencia como `docs/sermo-all.yml` pueden agrupar
ejemplos del esquema.

Todos los directorios de watch clasificados (`watches/`, `networks/`,
`storages/`, `mounts/`) deben figurar en `paths.watches`; cada hermano `.local`
es la capa de override por host. Para validar desde el árbol fuente, construye
con `SERMO_DATADIR=$PWD make build` y usa `examples/sermo-dev.yml`.

## Catalog service scope

Un check de servicio describe el proceso del servicio. El estado de un recurso
del host que el servicio observa pertenece a un host watch; usa `reports: state`
o `reports: value` solo cuando el dato observado deba seguir visible sin afectar
a la salud o disponibilidad del servicio. `smartd` y sus watches de unidades
generados son el modelo de referencia.

## Catalog init and reload fallback verification

Para cambios de catálogo que afecten metadatos de init o `reload.signal`,
verifica cada backend systemd/OpenRC declarado y cada fallback. Un fallback de
señal OpenRC requiere un pidfile canónico más un selector de proceso con `exe` y
`user` exactos; en otro caso usa un `reload.command` argv o el reload nativo del
backend.

Ejecuta el contrato enfocado de catálogo antes del gate final:

```sh
go test ./internal/config -run 'TestRealCatalog(AllServicesValidate|ReloadServicesResolve)$' -count=1
```

El procedimiento completo del operador vive en
[docs/services.es.md](docs/services.es.md).

## Service operations

Todas las acciones de aplicación start, stop, restart, reload, resume y signal
pasan por `internal/operation`. El código CLI, daemon y web no debe llamar
directamente a backends de servicio ni señalar procesos. Las implementaciones
primitivas de backend/proceso y sus fakes son las únicas excepciones estrechas.

## Native by default

Prefiere la biblioteca estándar de Go o un módulo Go. Los comandos externos
necesarios usan el runner inyectable `execx`, arrays argv, un contexto y un
timeout explícito; el código de producción no usa shell ni llama directamente a
`os/exec`.

La única excepción del producto es `sermoctl lock … -- COMMAND`, que ejecuta
intencionadamente el argv foreground del operador con los streams estándar
heredados. No generalices esa excepción.

## Protocol probes: interface binding is mandatory

Toda sonda de protocolo de `internal/conn` respeta `cfg.Interface`. Los
built-ins y aliases se registran en `internal/conn/registry.go`; las sondas
registradas entran una vez al ejecutor compartido y obtienen el target preparado
mediante los helpers existentes. No añadas registro en init de paquete ni
dupliques endpoints por defecto.

Las sondas stream marcan por `BindDialer`; los listeners de paquetes usan
`BindListenConfig`. Una biblioteca solo es aceptable si es exclusivamente codec
o acepta el dialer/conexión de Sermo. Rechaza bibliotecas que hagan I/O interno
sin hook, porque el routing por defecto violaría el invariante de interfaz.

Mantén locales las excepciones de transporte documentadas: el cliente datagrama
Unix de chronyd y la ruta DHCP por datagrama `IP_PKTINFO`. Un protocolo solo
puede reconstruir un endpoint cuando su formato de wire selecciona realmente un
target distinto, con la razón documentada en esa llamada.

## Documentation lockstep

Actualiza la documentación de usuario, ambas versiones de idioma y los ejemplos
útiles cuando cambie la configuración pública, CLI, checks, reglas, notifiers,
seguridad o comportamiento observable. `docs/configuration.es.md`,
`docs/rules.es.md`, `docs/services.es.md` y `examples/sermo.yml` son
propietarios, no una lista obligatoria para cambios ajenos.

Cuando un comportamiento sea planificado y no implementado, ponlo en
`TODO.es.md`; no lo describas aquí como actual.

## Documentation scope and style

Escribe para administradores Linux: explicaciones directas, YAML realista,
comandos copiables y notas de seguridad explícitas. Documenta comportamiento
público, razones de mantenimiento necesarias y excepciones de seguridad no
obvias. Enlaza al propietario en vez de copiar inventarios largos de
implementación o ajustes de herramientas.

## Central builders

Añade checks, watches, notifiers y acciones de reglas mediante el builder o
registro central propietario. Extiende `internal/checks/build.go`,
`internal/app/watch_build.go`, `internal/notify` o los builders de reglas en vez
de dispersar switches de construcción por los llamantes.

Los call sites de notifier solo referencian nombres configurados; un transporte
nuevo se construye y registra dentro de `internal/notify` junto con su
documentación de usuario.

## Timeout discipline

Toda operación bloqueante de comando, red, base de datos o fichero tiene un
timeout de la configuración del engine o de una constante con nombre. Los tests
pueden usar literales cortos para acotar el propio test. Nunca añadas una espera
de producción sin límite.

## Daemon performance discipline

Trata toda ruta del ciclo del daemon como hot. Reutiliza muestras dentro de un
ciclo, evita escaneos repetidos del host, allocations/sorts innecesarios y
trabajo bloqueante en secciones críticas del scheduler. Haz explícito y sujeto a
intervalo el trabajo caro; añade un benchmark cuando el coste a escala de flota
no sea obvio.

## Small-change checklist

- Inspecciona el estado Git y conserva cambios ajenos.
- Busca el propietario existente y las superficies equivalentes.
- Mantén estables los nombres públicos salvo migración explícita.
- Añade o actualiza tests enfocados y docs de usuario.
- Revisa timeout, coste del daemon e impacto de seguridad.
- Ejecuta el gate final obligatorio e informa del estado del árbol.

## Web UI cohesion

Las fuentes viven en `internal/web/src/`; `internal/web/index.html` se genera y
versiona. Los metadatos repetitivos de paneles watch pertenecen a
`internal/web/src/watch-panels.json`; el comportamiento ejecutable queda en los
propietarios JavaScript existentes. Ejecuta `make web` tras cualquier edición de
fuentes.

El render usa plantillas lit-html y reconciliación de listas completas. Compón
plantillas anidadas, deja que los bindings escapen valores y conserva la
interacción en la ruta de click delegada con `data-*`, no en handlers inline o de
eventos lit. El SVG construido como string solo puede escribir en su contenedor
dedicado. Conserva el contrato CSP existente del servidor.

Antes de añadir un visual, loader, formatter o control, encuentra la presentación
existente de ese concepto y reutilízala completa. Usa los patrones establecidos
de panel, tabla responsive y tokens de diseño; no introduzcas colores CSS
literales ni scroll horizontal de página. Los cambios web deben superar los
checks desktop/mobile y WCAG 2.2 AA existentes. Los contratos UI detallados
viven en [docs/webui-representation.es.md](docs/webui-representation.es.md).

## Wizard option selection

Todos los asistentes siguen [docs/wizards.es.md](docs/wizards.es.md). Usa
helpers `Prompt` compartidos, targets detectados y el flujo canónico de
monitor/interval; nunca inventes otro parser de prompts ni pidas un nombre de
target que la detección pueda proporcionar.

Conserva el vocabulario compartido de notifier `all` / `none` / `default`.
`none` y `default` siguen disponibles sin notifiers configurados, y un default
heredado vacío degrada a monitor-only. Previsualiza y confirma los ficheros
generados, y ofrece limpiar solo targets cuya ausencia pruebe la detección.

## Catalog: instanced systemd services

Reutiliza la materialización de versiones/instancias en vez de pedir variables
ad-hoc al operador. Usa `${hostname}` para una instancia por host y `%n` /
`${n}` para instancias numéricas descubiertas. Los templates de catálogo
materializan definiciones; el operador aún habilita servicios concretos. Mantén
actualizadas las reglas de variables built-in y templates en
[docs/services.es.md](docs/services.es.md).

## Go quality gates

Escribe Go idiomático que supere el gate configurado sin supresiones nuevas.
`make check` es el comando final, `make lint` es el comando enfocado de
analizadores y `.golangci.yml` es la fuente completa de linters y exclusiones.
No copies su lista ni sus umbrales actuales en la prosa.

Usa `.custom-gcl.yml` para el build personalizado de golangci-lint y `.semgrep/`
para reglas de fronteras de llamada del repositorio. Cómo añadir una regla,
incluidos los fixtures positivo y negativo, está en
[.semgrep/README.md](.semgrep/README.md). Un `//nolint` necesario nombra el
analizador exacto y explica la razón de diseño; nunca debilites un gate ni
amplíes una exclusión para aterrizar un cambio.

El `Makefile` posee las fases YAML, Markdown, scripts, dependencias, web,
vulnerabilidades y cobertura. Corrige los hallazgos en su fuente; no sustituyas
el gate por un subconjunto escrito a mano.

## Testing

Sigue el estilo de tests existente en el paquete propietario. Prefiere subtests
table-driven, fakes existentes, seams inyectables y directorios temporales. Los
tests no deben operar servicios reales, señalar procesos del host ni depender
del estado ambiente de `/etc`, `/proc`, red o init.

Cubre los caminos de éxito, entrada inválida/insegura, bloqueo y timeout/error
relevantes para el cambio. Conserva distinciones como nil frente a vacío cuando
formen parte del contrato. Usa tests dirigidos durante el desarrollo y el gate
final del workflow antes de informar que se completó.

## Security and safety invariants

La política detallada y el comportamiento del operador viven en
[docs/safety.es.md](docs/safety.es.md). Todo cambio debe conservar estas
fronteras duras:

- las acciones de servicio usan `internal/operation`, un timeout explícito,
  locks de operación, guards y preflight requerido;
- la remediación automática usa la misma ruta, requiere un cooldown resuelto
  positivo y nunca se dispara desde una métrica de scope sistema;
- la autorización de procesos nunca usa solo nombre, basename, argv o cmdline;
  los procesos señalables requieren identidad exacta de `exe` resuelto y `user`;
- `SIGKILL` requiere `force_kill` explícito más `kill_only_if` restrictivo;
- los residuales sin match se informan como orphans y bloquean un start posterior;
- las condiciones son read-only y la mutación pertenece a las acciones;
- los locks con nombre y los locks de operación siguen separados, se adquieren
  atómicamente, están limitados por TTL y su reclaim es auditable;
- cada acción ejecutada o bloqueada registra un resultado auditable;
- un servicio lento no debe bloquear la monitorización de otro, mientras la
  concurrencia compartida de checks permanece acotada.

Los perfiles de catálogo de bases de datos siguen siendo conservadores. Ninguna
opción de config puede desactivar un invariante duro.

## graphify

`graphify-out/` es un grafo de conocimiento local ignorado por Git. Para
preguntas sobre la base de código, consulta un `graphify-out/graph.json`
existente antes de recorrer ampliamente las fuentes. Usa `graphify query`,
`graphify path` o `graphify explain` según corresponda y verifica los hallazgos
importantes en los ficheros propietarios.

Actualiza o reconstruye el grafo solo cuando falte o esté obsoleto para la
tarea. Nunca añadas al staging la salida generada, y no ejecutes una
actualización tras un cambio salvo que el trabajo posterior necesite un grafo
actual.
