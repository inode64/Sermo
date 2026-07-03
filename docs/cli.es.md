# CLI

`sermoctl` es la interfaz de operación y scripting. Ejecútalo sin argumentos
o con `--help` para ver el índice de comandos, y usa `sermoctl help COMMAND` o
`sermoctl COMMAND --help` para obtener uso, flags y ejemplos específicos.

## Flags raíz

```text
--config /etc/sermo/sermo.yml
--backend auto|systemd|openrc
--json
--quiet / -q
--timeout duration
--version / -V
--help / -h
```

Los flags globales pueden colocarse antes o después del comando. Los flags
específicos de cada comando se muestran con `sermoctl help COMMAND`.

## Flags del daemon sermod

`sermod` es el daemon de monitorización de larga ejecución. Las unidades
empaquetadas normalmente lo arrancan con la ruta de config estándar:

```bash
sermod run --config /etc/sermo/sermo.yml
```

Las ejecuciones manuales admiten estos flags:

```text
sermod run [--config PATH] [--catalog DIR ...] [--verbose|-v]
sermod version
sermod --version
```

- `--config PATH` carga el archivo de config global. El valor por defecto es
  `/etc/sermo/sermo.yml`. Usa la misma ruta con `sermoctl --config` al
  validar o recargar un árbol no estándar.
- `--catalog DIR` sobrescribe `paths.catalog` para este proceso del daemon. Es
  repetible y preserva el orden, de modo que las comprobaciones de desarrollo y
  empaquetado pueden ejecutarse contra directorios de catálogo del árbol de
  fuentes o preparados sin editar la config.
- `--verbose` / `-v` habilita el logging de depuración, incluyendo detalles de
  carga de config, sobrescrituras de catálogo, detección de backend y recuentos
  de destinos de monitorización.

Usa `sermoctl daemon reload` para pedir a un daemon en ejecución que vuelva a
leer el archivo de config con el que se arrancó; cambiar `--catalog` requiere
reiniciar `sermod` con el nuevo conjunto de flags.

## Superficie de comandos

```bash
sermoctl help [COMMAND]
sermoctl backend
sermoctl version
sermoctl status SERVICE
sermoctl is-active SERVICE
sermoctl watch status WATCH
sermoctl start SERVICE [--no-cascade]
sermoctl stop SERVICE [--no-cascade]
sermoctl restart SERVICE [--no-cascade]
sermoctl resume SERVICE
sermoctl reload SERVICE

sermoctl mount TARGET                 # TARGET is a configured mount name or absolute path
sermoctl umount TARGET
sermoctl mount status TARGET
sermoctl mount list

sermoctl preflight SERVICE
sermoctl processes SERVICE
sermoctl locks SERVICE
sermoctl monitor SERVICE
sermoctl unmonitor SERVICE

sermoctl panic on|off|status          # daemon-wide emergency switch (see Panic mode)

sermoctl config validate

sermoctl daemon reload                 # reload sermod config, not services

sermoctl services [all] [--long] [--notify NAME[,NAME]|all]   # catalog inventory, not runtime config
sermoctl apps [all] [--long]                                  # catalog apps (see Catalog inventory)
sermoctl libs [all] [--long]
sermoctl patterns

sermoctl sla [SERVICE]                  # service availability windows (all services, or one)
sermoctl sla --series SERVICE [--since DURATION]  # per-minute series; --since default 24h

sermoctl events [SERVICE] [--limit N]   # list recent events (global or for SERVICE)
sermoctl events clear [--before TIME]   # omit TIME to clear all; TIME may be RFC3339 or duration (e.g. 1h)
                                        # only events strictly before the timestamp are removed
sermoctl activity clear [--before TIME] # clears the same log shown as Recent activity

sermoctl state compact [--before TIME]  # prunes old history and vacuums the state database
                                        # omit TIME for normal 366-day retention; TIME may be RFC3339 or duration

sermoctl lock SERVICE [--name NAME] --reason REASON --ttl DURATION -- COMMAND...
sermoctl lock acquire SERVICE [--name NAME] --reason REASON --ttl DURATION
sermoctl lock release SERVICE [--name NAME]

sermoctl wizard
sermoctl wizard service|docker|vm|mount|volume|net|uplink
```

Ejemplos:

```bash
sermoctl help restart
sermoctl restart mysql-main
sermoctl services --notify ops-email
sermoctl daemon reload
sermoctl state compact --before 720h
```

## Modo pánico

El modo pánico es un interruptor de emergencia a nivel de todo el daemon para
ventanas de mantenimiento, ataques, denegación de servicio, malfunción del
sistema o sobrecarga. Mientras está activo, el daemon sigue ejecutando sus
comprobaciones (por lo que el estado permanece visible) pero **suspende todos los
hooks, las notificaciones de alerta y la remediación automática**. Las
operaciones manuales (`start`, `stop`, `restart`, `reload`, `resume`) siguen
disponibles, de modo que puedes gestionar los servicios a mano sin que el daemon
te interfiera.

```bash
sermoctl panic on        # suspend hooks, alerts and automatic remediation
sermoctl panic status    # show the current state (default when no argument)
sermoctl panic off       # resume normal operation
```

El flag se persiste en la base de datos de estado (`paths.state`), por lo que
sobrevive a los reinicios del daemon hasta que lo desactivas, y la CLI funciona
sin necesidad de tener habilitada la web UI. El daemon en ejecución detecta un
cambio en ~1 segundo. Mientras está activo, el estado del daemon reportado por
`/readyz` y la cabecera web muestran **`panic mode`**. En la web UI, el mismo
conmutador es el botón rojo **panic mode** del pie de página (pide confirmación
en ambos sentidos para no activarse por accidente). La CLI aplica el cambio
inmediatamente sin solicitar confirmación.

## Resolución del destino de servicio

Para un servicio configurado, `sermoctl status`, `is-active` y las operaciones
de servicio resuelven el mismo destino de control que usan `sermod` y la web UI.
Cuando `sermod` se ejecuta con `web` habilitado, `sermoctl status` prefiere el
estado calculado por el daemon (incluyendo `starting` durante el asentamiento de
arranque); si la API web es inalcanzable, recurre al backend de init más los
metadatos locales de monitorización, como antes. Los estados de servicio son:
`disabled`, `stopped`, `started` (backend activo pero no monitorizado),
`starting` (asentamiento de arranque/operación), `collecting` (activo y
monitorizado, pero las gráficas/indicadores aún no están completos),
`monitored` (activo, monitorizado y con observabilidad lista) y `failed`. Sin la
vista del daemon, un servicio configurado activo y monitorizado cae a
`collecting`; un servicio activo que no consta como monitorizado cae a
`started`. **`sermoctl is-active` es
diferente:** siempre sondea el backend de init (`active` / `inactive` /
`paused`) para obtener el código de salida y la salida en texto plano. Por tanto,
un servicio monitorizado que aún se está asentando con un backend inactivo
muestra `state=starting` en `status` pero sale con **1** en `is-active` hasta que
la unidad reporta active. La misma preferencia se aplica a `sermoctl watch status
WATCH` y a la columna STATUS de `sermoctl apps` para las aplicaciones instaladas
monitorizadas por el daemon. Las apps de catálogo cuyo binario no está instalado
se omiten de `sermoctl apps` y no participan en el asentamiento de arranque.
Sermo lee los candidatos `service:` del servicio, elige la primera unidad
conocida por el backend activo, y normaliza los nombres de systemd con `.service`
cuando es necesario.

Si el sondeo del backend no puede exponer una unidad de init configurada pero el
servicio aún tiene una semilla configurada utilizable, Sermo recurre a esa unidad
e imprime una advertencia, coincidiendo con el comportamiento daemon/web usado
para configuraciones históricas de servicios de init. No hay recurso alternativo
para destinos `control:` inválidos ni para un mapa `service:` por backend sin
candidato para el backend activo; esos son errores de configuración.

## Inventario de catálogo

`sermoctl services`, `sermoctl apps`, `sermoctl libs` y `sermoctl patterns`
listan las **definiciones de catálogo** distribuidas bajo `paths.catalog` (ver
[services.md](services.es.md)): qué perfiles están instalados, la versión que
reporta su comando de versión, y si se resuelven. Añade `all` para incluir
entradas cuyo binario o archivo de librería no está presente en el host.

Esto **no** es la lista de **destinos de runtime configurados** que `sermod`
monitoriza. Esos son los archivos de servicio bajo `paths.services` (y los
nombres coincidentes en el árbol de config global).

| Pregunta | Dónde buscar |
| --- | --- |
| ¿Qué perfiles de servicio de catálogo existen / están instalados? | `sermoctl services [all]` |
| ¿Qué apps / libs / conjuntos de patrones de catálogo existen? | `sermoctl apps`, `sermoctl libs`, `sermoctl patterns` |
| ¿Qué servicios están habilitados en *mi* config ahora mismo? | YAML bajo `paths.services`, o el panel **Services** de la web UI (`GET /api/services`) |
| Estado en vivo de un servicio configurado | `sermoctl status SERVICE`, `sermoctl is-active SERVICE` |
| Historial de disponibilidad de los servicios configurados | `sermoctl sla [SERVICE]` |

La web UI usa la misma división: **Services** muestra los servicios de runtime
configurados; **Applications** (`GET /api/applications`) es el inventario de apps
instaladas y se alinea con `sermoctl apps`, no con `sermoctl services`.

## Códigos de salida

```text
0   success / active / allowed
1   expected false condition, such as inactive or a failed check
2   internal or runtime error / backend not detected
64  usage error (bad flags or arguments)
75  temporarily blocked action, such as an active backup lock or guard
78  configuration invalid (syntax, schema or `config validate` failure)
```

La distinción entre `2` y `78`: usa `78` siempre que el problema esté en los
archivos de config que el operador puede corregir (sintaxis YAML, kind/name
faltante, variable desconocida, uses/clone sin resolver, fallo de `config
validate`). `2` es todo lo demás que no sea un false limpio (`1`), un error de
uso (`64`) o un bloqueo temporal (`75`): errores de E/S, backend no detectado,
un exec que no pudo lanzarse, un panic inesperado recuperado en el nivel
superior.

`is-active` mapea directamente: `0` active, `1` no active (incluyendo `paused`),
`2` error.

## Montajes

Las acciones de montaje se respaldan en fstab y usan archivos de montaje de
`/etc/sermo/mounts` por defecto. Un destino de ruta que no esté configurado se
sigue aceptando, pero usa valores por defecto seguros y debe existir en
`/etc/fstab`. Ver [unidades de montaje](configuration.es.md#mount-units).

`sermoctl wizard mount` lista los puntos de montaje declarados en `/etc/fstab` y
escribe archivos de montaje seguros bajo `paths.mounts`; no ejecuta mount ni
umount mientras genera la config.
