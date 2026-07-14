# Seguridad

Las invariantes de seguridad de Sermo **no son configurables en YAML**. La validación rechaza
cualquier conmutador `security:` que intente desactivarlas.

## Invariantes estrictas

1. **Nunca iniciar, reiniciar, recargar ni reanudar si falla un preflight requerido.** Un
   fallo de preflight requerido bloquea la acción con `preflight_failed`.
2. **Nunca iniciar, detener, reiniciar, recargar ni reanudar si un guard bloquea la acción.**
   Los guards se evalúan antes de la remediación; una acción de remediación que un guard bloquea
   nunca se ejecuta.
3. **Los locks de runtime nombrados activos siempre bloquean las acciones de servicio.** El motor
   de operaciones comprueba `<runtime>/locks` automáticamente — no se necesita ninguna regla.
4. **Nunca SIGKILL por defecto.** `force_kill` es false salvo que se habilite explícitamente.
5. **Nunca matar por nombre de proceso.** Un kill requiere una coincidencia exacta en la
   ruta `/proc/<pid>/exe` resuelta **y** el UID real frente a un selector
   `kill_only_if` explícito. Un regex `processes.<name>.cmd` puede acotar el
   descubrimiento de procesos para binarios compartidos, pero el cmdline nunca autoriza un kill; un proceso
   cuyo exe no se puede resolver (permisos, o un binario `(deleted)`) nunca se
   mata — en su lugar se reporta como un residual.
6. **Nunca enviar señales terminadoras a PID 1 ni a procesos del kernel.**
   `SIGTERM`, `SIGKILL`, `SIGINT` y `SIGQUIT` se bloquean centralmente para PID 1
   y para kernel threads (`kthreadd`/hijos sin exe de userspace ni cmdline). Esto
   no es configurable; los residuales protegidos se reportan en su lugar.
7. **`force_kill: true` requiere `kill_only_if`** con tanto un selector `users`
   como un selector `exe_any`, cada uno no vacío.

## El motor de operaciones

Cada start/stop/restart/reload/resume — manual (`sermoctl`) o automático (`sermod`) —
pasa por el mismo motor:

1. Adquirir el lock interno de operación (`<runtime>/ops/<service>.lock`); un titular
   vivo falla rápido con código de salida `75` ("operation in progress").
2. Bloquear ante cualquier lock de runtime nombrado activo.
3. Ejecutar el preflight requerido (start/restart/reload/resume).
4. Bloquear si algún guard bloquea la acción.
5. Para stop/restart, detener, esperar `graceful_timeout`, descubrir procesos residuales.
6. Si quedan residuales y `force_kill` es false → `orphan_processes`; un restart fallido
   **no** inicia. Si es true, SIGTERM y luego SIGKILL solo a los procesos
   que coincidan exactamente con `kill_only_if`, redescubriendo entre pasos.
7. Tras un stop limpio (sin residuales), reconciliar el estado registrado del init con
   la realidad — `systemctl reset-failed` (systemd) o `rc-service … zap` (OpenRC) —
   para que un marcador persistente de failed/stuck no pueda contradecir los procesos reales.
   Best effort: nunca hace fallar un stop que ya tuvo éxito.
8. Para start/restart, iniciar y verificar el estado; para reload, recargar en sitio; para
   resume, reanudar el objetivo y verificar el estado. Ejecutar el postflight requerido para
   start/restart/reload/resume.

Un residual que Sermo no tiene permitido identificar y matar se **reporta, no se mata**:
un fallo limpio `orphan_processes` es más seguro que matar el proceso equivocado.

Contrato de implementación: el motor registra exactamente dos pasos diferidos —
emitir un evento del resultado final (registrado primero, de modo que se dispara en toda
ruta de salida), y liberar el lock de operación (registrado solo tras una adquisición
exitosa). Cualquier paso posterior puede retornar temprano; la limpieza nunca se repite por retorno,
y una operación bloqueada, fallida o con panic no puede filtrar el lock ni omitir su
evento. Estados de resultado: `ok`, `blocked`, `preflight_failed`,
`postflight_failed`, `failed`, `orphan_processes`. El motor no
implementa el cooldown por sí mismo — eso controla la *decisión* de actuar y se ejecuta en la
evaluación de reglas del daemon antes de invocar el motor, que es como las acciones manuales y
automáticas comparten un único motor mientras solo la remediación automática está limitada por tasa.

## Limitación de tasa

Solo la remediación *automática* está limitada por tasa (`cooldown`, `max_actions`,
`backoff`). Las acciones manuales de `sermoctl` son deliberadas y no están sujetas a cooldown,
pero siguen sujetas a locks, guards y preflight.
El estado de la limitación de tasa de la remediación automática se almacena en `paths.state`, así que un
reinicio de `sermod` o un reboot del host no limpia el cooldown/backoff ni la
ventana de `max_actions`.

## Pausar la monitorización

`sermoctl unmonitor SERVICE` pausa la monitorización de un servicio; `monitor SERVICE`
la reanuda. Mientras está pausado, el daemon no ejecuta checks, reglas ni remediación para ese
servicio — útil durante el mantenimiento para que una parada deliberada no sea "remediada" por un
restart automático. La pausa se registra en el almacén de estado persistente bajo
`paths.state` (la tabla `monitor_state`), de modo que persiste entre reinicios del daemon
y reboots hasta que se limpie. `sermoctl status SERVICE` muestra
el único estado de operador `started` o `stopped` mientras la monitorización está pausada
(`"state": "started"`/`"stopped"` y `"paused": true` en `--json`). Pausar solo
afecta a la monitorización de Sermo; no detiene el servicio en sí, y las acciones manuales
de `sermoctl` siguen funcionando.

Un `stop` manual correcto desde `sermoctl` o la web UI también pausa la monitorización
cuando el service estaba monitorizado. La fila de estado registra que la pausa vino de
un stop manual, de modo que un `start` manual correcto posterior restaura la
monitorización solo en ese caso. Si el service ya estaba desmonitorizado antes del stop,
el start posterior conserva esa decisión del operador.

## Métricas del sistema

Una métrica `scope: system` ("¿está la máquina bajo presión?") **no** es un disparador
sólido para reiniciar un único servicio, así que solo se permite en reglas `alert` — nunca en
reglas de remediación, ni directamente ni mediante una referencia de check. Véase
[Métricas](rules.es.md#metrics) para las listas de métricas `scope: service` y `scope: system`.

## Privilegios: el daemon se ejecuta como root

`sermod` está diseñado para **ejecutarse como root** (la unidad systemd empaquetada y el servicio
OpenRC lo hacen). Gestiona servicios pertenecientes a distintos usuarios y toca áreas
privilegiadas, así que varias funciones lo necesitan:

- **Control de servicios** — start/stop/restart/reload mediante systemd/OpenRC,
  start/stop/restart/resume de dominios de VM mediante libvirt cuando un servicio declara
  `control.type: libvirt`, y start/stop/restart/resume de contenedores Docker
  cuando declara `control.type: docker`.
- **Señalizar procesos de otros usuarios** — la política de stop recolecta procesos residuales
  que coinciden con el selector `kill_only_if`, a través de los UIDs.
- **Inspección de `/proc` entre usuarios** — resolver el `/proc/<pid>/exe` de un proceso,
  el estado y el IO por proceso (`/proc/<pid>/io`) del proceso de otro usuario.
- **Checks `icmp`** — abrir un socket ICMP en bruto necesita `CAP_NET_RAW` (root, o esa
  capability otorgada al binario).

Aun así **arranca sin privilegios**, pero esas funciones se degradan silenciosamente, así que
**registra una advertencia en el arranque** cuando no es root (`euid != 0`). Ejecútalo como root,
u otorga las capabilities específicas que necesites (p. ej. `CAP_NET_RAW` para ICMP,
`CAP_KILL`/`CAP_SYS_PTRACE` para señalización/inspección entre usuarios) si prefieres una
configuración de mínimo privilegio.

## Modelo de confianza

Dado que el daemon se ejecuta como root:

- **La configuración es entrada confiable, propiedad de root.** Los checks `command` y los `hook`s de watch
  ejecutan su `argv` **como root** (nunca mediante un shell). Mantén `/etc/sermo` escribible
  solo por root; cualquiera que pueda editarlo puede ejecutar código como root. Los secretos pertenecen al
  entorno (`${env:NAME}`), no al archivo.
- **La interfaz web** (cuando está habilitada) puede start/stop/restart/reload/resume servicios y
  monitor/unmonitor objetivos como root, así que está endurecida por defecto: **se enlaza a
  loopback** (`127.0.0.1`), soporta
  **autenticación** con un rol de invitado de solo lectura, requiere la cabecera **`X-Sermo-Csrf`**
  en cada petición que cambia estado (bloqueando la falsificación entre sitios desde un
  navegador), y establece timeouts HTTP. Habla HTTP plano, así que para alcanzarla desde fuera
  del host **debes** ponerla detrás de un reverse proxy con terminación TLS
  (nginx/Apache) — véase
  [detrás de un reverse proxy](configuration.es.md#behind-a-reverse-proxy-required-to-expose-it).
  Mantén `web.address` en loopback; nunca publiques el puerto directamente. El daemon registra
  una advertencia si la interfaz se ejecuta sin autenticación.
- **Sin shell, sin kills por nombre, sin SIGKILL por defecto** — véanse las invariantes
  estrictas de arriba; estas acotan lo que incluso una mala configuración puede hacer.

## Locks

Dos mecanismos de bloqueo complementarios protegen las operaciones:

1. **Locks de runtime nombrados** — archivos bajo `<paths.runtime>/locks` (por defecto
   `/run/sermo/locks`), nombrados `<service>[.<name>].lock`. El motor de operaciones
   bloquea automáticamente ante cualquiera activo; no se necesita ninguna regla. Creados por
   `sermoctl lock` (envolver un comando), `lock acquire` / `lock release`
   (véase [cli.es.md](cli.es.md)).
2. **Checks de lock externos controlados por un guard** — un check (`file_exists`,
   `process`, …) sobre una señal que Sermo *no* posee: un proceso de backup, un
   archivo de flag externo. Nunca apuntes tal check bajo `<paths.runtime>/locks` —
   eso duplica el mecanismo 1.

Un `lockfile:` creado por un servicio en el catálogo es diferente: es un health check
controlado para un artefacto de runtime regular, como `socket:`, y no bloquea
operaciones a menos que el operador también escriba una regla de guard explícita.

El **lock interno de operación** (`<paths.runtime>/ops/<service>.lock`)
serializa start/stop/restart/reload/resume para un servicio. Está deliberadamente fuera del
espacio de nombres de locks nombrados para que no pueda colisionar con un lock de usuario llamado `op`, nunca se
lista como un lock nombrado, y no puede ser liberado por `sermoctl lock release`. Un
titular vivo hace que una segunda operación falle rápido con código de salida `75` ("operation in
progress") — el motor nunca espera ni encola.

Los archivos de lock son JSON:

```json
{
  "service": "mysql",
  "name": "backup",
  "reason": "backup mysql",
  "owner_pid": 12345,
  "owner_start_ticks": 884512,
  "created_at": "2026-06-05T12:00:00Z",
  "expires_at": "2026-06-05T16:00:00Z"
}
```

`owner_start_ticks` es el tiempo de inicio del titular (campo 22 de
`/proc/<pid>/stat`), registrado para que un lock obsoleto pueda distinguirse de uno vivo
incluso tras la reutilización de PID.

Ciclo de vida:

- **Adquirir atómicamente** con `O_CREAT|O_EXCL`; escribir el JSON y hacer fsync del archivo
  y del directorio, de modo que un lock existente siempre está completo y es legible.
- Un lock está **obsoleto** (ignorado, recuperable) cuando su TTL ha vencido, su
  PID titular ha muerto, o el PID está vivo con un tiempo de inicio distinto (reutilización). Un lock vivo
  **nunca se sobrescribe silenciosamente**.
- **La recuperación se registra**: leer, confirmar que sigue obsoleto, desvincular, adquirir de nuevo;
  abortar si pasó a activo en el ínterin.
- La forma de envoltura desvincula el lock cuando el comando envuelto termina (por cualquier ruta);
  el TTL aún acota la vida del lock si el titular cae. Elige un TTL
  con seguridad por encima de la duración real del trabajo protegido — uno que expire
  a mitad de un backup desbloquearía indebidamente los restarts.

## Operaciones de montaje

Las unidades de montaje (cargadas desde documentos de watch de storage listados
en `paths.watches`, cuando definen `mount:`) son acciones manuales del operador expuestas por
`sermoctl mount|umount` y por el panel **Mount units** de la interfaz web; no
son remediación del ciclo del daemon. Aun así usan la misma postura de
seguridad:

- El origen, tipo y opciones de montaje provienen solo de `/etc/fstab`. Sermo ejecuta
  `mount <path>` / `umount <path>` con argv directamente y un timeout; nunca
  construye un comando de shell a partir de YAML.
- Cada objetivo tiene un lock de operación bajo `<paths.runtime>/mounts/ops`, de modo que dos
  llamadores no puedan competir por el mismo montaje.
- Con `mount.refcount: true` (el valor por defecto), `mount` incrementa un contador de runtime y
  `umount` lo decrementa; el desmontaje real se intenta solo cuando el contador
  llega a cero.
- Sermo nunca desmonta el filesystem raíz (`/`). CLI y Web/API rechazan
  `umount`, las alertas de blockers y la señalización de blockers para `/` antes de intentar
  cualquier `umount`, discovery de procesos o señal.
- Los desmontajes ocupados se reportan con los procesos que usan el montaje. Sermo no los
  señaliza a menos que el operador solicite explícitamente `sermoctl umount
  --kill-blockers` o marque `kill blockers` en la Web UI.
- La interfaz web puede enviar una alerta TTY nativa a los usuarios con sesión
  que sean propietarios de bloqueadores actuales. Usa el mismo notifier TTY en
  Go que las notificaciones normales; no ejecuta `wall`, `write` ni un shell.
- La señalización de blockers de montaje requiere `mount.stop_policy.kill_only_if`
  con selectores `users` y `exe_any` restrictivos. Solo se señalizan los blockers
  que coinciden con ese selector; cmdline es dato de visualización y nunca
  autoriza un kill.
- El desmontaje forzado y perezoso son opciones por acción: `--force` / Web
  `force` permite `umount -f`, y `--lazy` / Web `lazy` permite `umount -l` como
  último fallback.

## Identidad de proceso y coincidencia

Las decisiones de kill dependen de cómo se leen los hechos del proceso, así que esto es fijo:

- **Exe** es el objetivo resuelto de `/proc/<pid>/exe` — la ruta real absoluta
  del binario en ejecución. Se compara por **igualdad exacta** tras canonicalizar
  ambos lados; sin coincidencia por basename, prefijo o subcadena.
- **UID** es el UID real de `/proc/<pid>/status`; los selectores de usuario lo coinciden
  exactamente.
- **Los nombres de usuario/grupo se resuelven a IDs numéricos antes de la coincidencia.**
  `engine.user_lookup` controla esa resolución. Las compilaciones estáticas `CGO_ENABLED=0` pueden
  usar el modo `auto` por defecto para recurrir a `getent` para usuarios respaldados por NSS
  manteniendo el binario de Sermo estático. Si un nombre configurado no se puede
  resolver, el selector falla en cerrado y ningún proceso es coincidido ni señalizado por
  ese nombre. Los selectores numéricos UID/GID siguen siendo deterministas.
- **Cmdline** es normalmente dato de visualización/registro, pero un campo `processes.<name>.cmd`
  es un regex RE2 explícito sobre el argv unido. Úsalo solo para hacer el descubrimiento
  más específico cuando el mismo ejecutable corre varios roles, p. ej. envoltorios de Java o QEMU.
  El cmdline es falsificable, así que no satisface `kill_only_if` y no
  hace que un proceso sea matable por sí mismo.
- Un selector con varios campos (`exe`, `cmd`, `user`, `group`) requiere que **todos**
  coincidan.
- **Un exe irresoluble falla seguro**: si `/proc/<pid>/exe` no se puede leer o
  se resuelve a una ruta `(deleted)` (binario reemplazado por una actualización), el proceso
  no coincide con ningún selector de exe — se reporta como un residual con exe desconocido y
  nunca se señaliza.
- **PID 1 y los kernel threads están protegidos** frente a señales terminadoras
  aunque un selector o camino de señal futuro los alcanzara. Las señales de
  reload no terminadoras como `SIGHUP` no se bloquean por esta protección.
- **Los reloads por señal nativa usan el mismo modelo de identidad.** En OpenRC, o cualquier
  servicio sin `MainPID` del backend, el PID del pidfile se señaliza solo después de que
  coincida con un selector `processes:` con `exe` y `user` exactos. Los autores de catálogo
  deben verificar cada init script provisto, el fallback de pidfile y el selector de identidad
  juntos antes de declarar `reload.signal`.

Orden de descubrimiento: información del backend (MainPID/cgroup de systemd; status de OpenRC)
→ pidfiles configurados → selectores `processes:` → árbol de procesos hijos desde
`/proc`, deduplicado por PID.
Para los mapas `pidfiles:`, cada rol de pidfile debe estar respaldado por un selector
`processes:` con el mismo nombre y con `exe` y `user` exactos; el pidfile es evidencia, no
una autoridad basada solo en el nombre.

## Stop y escalada de señales

Los campos de `stop_policy` omitidos por un servicio de catálogo o servicio heredan de
`defaults.stop_policy`. La fase de stop de un stop/restart:

1. `Stop` del backend, esperar `graceful_timeout`, descubrir residuales.
2. Sin residuales → stop limpio.
3. Residuales con `force_kill: false` → `orphan_processes` (y un restart **no**
   inicia).
4. Residuales con `force_kill: true` → clasificar cada uno: MATABLE solo cuando
   cada campo de `kill_only_if` coincide (exe resuelto exacto **y** UID real;
   un exe irresoluble y los PIDs protegidos nunca son matables). SIGTERM al conjunto matable, esperar
   `term_timeout`, redescubrir; SIGKILL a lo que quede del conjunto matable, esperar
   `kill_timeout`, redescubrir. Un residual que nunca coincidió nunca se señaliza.
5. El resultado es `ok` solo cuando no queda ningún residual — ya sea que el
   superviviente fuera deliberadamente perdonado o sobreviviera al SIGKILL, el resultado es
   `orphan_processes` y lista cada proceso restante.

## Planificador y concurrencia

Cada servicio habilitado es monitorizado por su propio worker con un ticker independiente
a `engine.interval` (los `interval` por servicio lo anulan). Los workers nunca comparten un
ciclo: un restart de varios minutos en un servicio no puede bloquear la monitorización de
otro. Dentro de un servicio el ciclo es síncrono — checks, evaluación de reglas,
y luego como mucho una operación.

- **Solapamiento de ticks**: si el ciclo de un worker sigue ejecutándose cuando se dispara su siguiente
  tick, ese tick se **omite, no se encola** — una operación que se prolonga causa
  omisiones, nunca un atasco de ciclos de recuperación. Las omisiones son por servicio y se registran.
- **Jitter**: los workers arrancan con un pequeño offset por servicio para que los ticks se repartan
  a lo largo del intervalo.
- **Concurrencia acotada**: las operaciones de todos los servicios comparten el semáforo
  global (`engine.max_parallel_operations`); la ejecución de checks comparte un
  pool global separado (`engine.max_parallel_checks`). Un check que no consigue
  un slot espera — no se omite.
- **Apagado** (SIGTERM/SIGINT): dejar de iniciar ciclos, cancelar los contextos de los workers;
  una operación en curso observa la cancelación, su limpieza diferida libera
  el lock y emite el evento, y un servicio parcialmente detenido se deja como está —
  nunca se mata a la fuerza por el apagado.
- **El reload del daemon** valida la nueva configuración, intercambia workers/watches
  preservando el estado de runtime por servicio, y mantiene la generación en ejecución cuando
  la nueva configuración es inválida.
