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
4. **Nunca señalizar un residual no verificado.** `force_kill: auto` deriva
   autorización solo de selectores `processes:` con ejecutable exacto y usuario
   real; un selector marcado `delegated: true` nunca se señaliza y no aporta
   autorización alguna; `force_kill: false` desactiva la escalada.
5. **Nunca matar por nombre de proceso.** Un kill requiere una coincidencia exacta en la
   ruta `/proc/<pid>/exe` resuelta **y** el UID real frente a un selector
   `kill_only_if` explícito o una identidad estricta emparejada de `processes:`.
   Un regex `processes.<name>.cmd` acota tanto el descubrimiento como la
   identidad emparejada para binarios compartidos, de modo que un daemon y sus
   hijos de workload nunca colapsan en un mismo conjunto de kill; el cmdline solo
   restringe y nunca autoriza un kill por sí mismo. Un proceso cuyo exe no se
   puede resolver (permisos, o un binario `(deleted)`) nunca se mata — en su
   lugar se reporta como un residual.
6. **Nunca enviar señales terminadoras a PID 1 ni a procesos del kernel.**
   `SIGTERM`, `SIGKILL`, `SIGINT` y `SIGQUIT` se bloquean centralmente para PID 1
   y para kernel threads (`kthreadd`/hijos sin exe de userspace ni cmdline). Esto
   no es configurable; los residuales protegidos se reportan en su lugar.
7. **`force_kill: true` requiere `kill_only_if`** con tanto un selector `users`
   como un selector `exe_any`, cada uno no vacío. **`force_kill: auto`** no
   usa un fallback amplio: solo autoriza identidades estrictas de `processes:` y
   deja los servicios sin una como `orphan_processes`.
8. **El restart nativo no debilita las barreras comunes ni hace fallback.** Sigue
   exigiendo locks, preflight, guards, cualquier identidad de restart disponible,
   timeout y postflight; un `Restart` fallido del backend es una operación
   fallida, nunca un stop/start staged implícito.
9. **Un proceso stray nunca se señaliza sin su propia autorización.** Un miembro
   del control group que ningún selector reclama solo puede señalizarse con
   `sermoctl reap --apply`, y solo a través del selector `reap.kill_only_if` del
   propio servicio, comprobado por la misma barrera que cualquier otro kill.
   Ninguna acción de regla puede hacer reap, y un servicio sin bloque `reap:`
   reporta sus strays y no señaliza ninguno.
10. **`process_policy` solo observa y alerta.** No tiene runner de operaciones,
    backend de servicio ni ruta de señal. La validación permite únicamente
    `then.notify` y `then.notify_interval`; una infracción de política no puede
    reiniciar, reparar, matar ni cambiar de otro modo un proceso o servicio.
11. **Una red virtual libvirt con interfaces de guests vivas nunca se
    destruye.** El stop/restart de `control: libvirt-network` verifica cada
    dominio no apagado (incluidos los pausados — sus taps siguen conectados)
    contra el nombre de la red y su puente; cualquier conexión, o cualquier
    guest no verificable, bloquea la destrucción. Ninguna opción de
    configuración lo relaja.

## El motor de operaciones

Cada start/stop/restart/reload/resume — manual (`sermoctl`) o automático (`sermod`) —
pasa por el mismo motor. La acción solo manual `repair` también usa ese motor,
pero nunca puede ser una remediación automática:

1. Adquirir el lock interno de operación (`<runtime>/ops/<service>.lock`); un titular
   vivo falla rápido con código de salida `75` ("operation in progress").
2. Bloquear ante cualquier lock de runtime nombrado activo.
3. Ejecutar el preflight requerido (start/restart/reload/resume/repair).
4. Bloquear si algún guard bloquea la acción.
5. Ejecutar la fase del gestor de servicios correspondiente a la acción:
   - Antes de cualquiera de los dos modos de restart, un estado estable
     `inactive`/`failed` del backend junto con procesos de servicio no delegados
     supervivientes activa la reconciliación del init obsoleto bajo el
     `stop_policy` normal. Los estados `unknown` y transitorios nunca entran en
     el reaper. Los errores de consulta de estado, descubrimiento o reset
     devuelven `failed`, y cualquier superviviente devuelve
     `orphan_processes`; ninguna de esas rutas alcanza el restart del backend.
   - stop y restart con `restart_policy.mode: staged`: detener, esperar
     `graceful_timeout`, descubrir procesos residuales y aplicar la escalada de
     señales configurada. Un restart nunca inicia mientras queden residuales. La
     excepción estrecha de reactivación por socket no cambia: si un stop systemd
     aislado tuvo éxito, todos los residuales están atribuidos por el backend a
     esa misma unidad y la unidad ya está `active`, Sermo acepta la reactivación
     del backend y no ejecuta un segundo start.
   - restart con `restart_policy.mode: native`: tras el caso protegido de init
     obsoleto anterior, invocar un único `Restart` acotado del backend para la
     unidad principal. No hay una fase stop ordinaria de Sermo, limpieza de
     artefactos parados ni fallback staged. Las unidades `also_service`
     auxiliares permanecen activas.
   - `start`, `reload` y `resume`: ejecutar su acción acotada de backend existente.
6. Tras un stop explícito limpio o la fase stop de un restart staged, reconciliar
   el estado registrado del init con la realidad — `systemctl reset-failed`
   (systemd) o `rc-service … zap` (OpenRC). Best effort: nunca hace fallar un stop
   que ya tuvo éxito.
7. Verificar el estado del backend cuando corresponda y ejecutar el postflight
   requerido para start/restart/reload/resume/repair.

`repair` es deliberadamente más limitado que una limpieza genérica. Primero
exige que el backend de init informe el servicio como failed o inactive. Después
solo puede eliminar un pidfile regular bajo `/run` cuyo PID exacto no exista en
`/proc`; un PID vivo, fichero malformado, enlace simbólico o ruta fuera de
`/run` falla de forma segura. Para una unidad failed también restablece el
marcador de error del backend de init mediante el mismo gestor antes del
arranque normal con guardas y postflight.

El **close SSH session** del panel es una operación manual separada del motor,
nunca una acción de regla ni remediación automática. Toma los mismos locks de
operación y nombrados, guards, timeout y ruta de un resultado/evento, pero no
reinicia ni ejecuta postflight del daemon SSH. Justo antes de la única señal,
Sermo vuelve a leer el terminal con login y su ascendencia `/proc` hasta un
ejecutable `sshd` configurado exacto y su usuario real, y exige el mismo
terminal, PID de sesión y ticks de inicio del proceso. Si falta esa frontera, el
terminal cambió o el PID se recicló, se rechaza. Un cierre correcto envía un único `SIGTERM` al proceso de
sesión; nunca escala a `SIGKILL`.
Un terminal SSH cuya ascendencia no puede verificarse sigue visible como
incidencia no disponible, pero no ofrece acción de cierre ni aporta PID a la API.

El check `terminal_sessions` es de solo observación: ejecuta una lista limitada
por argv de `tmux` o `screen` como la cuenta configurada explícitamente y no
controla sesiones. El cierre manual independiente de un origen vacío solo está
disponible para tmux con un socket explícito configurado. Comparte locks de
operación y nombrados, guards, timeout y un único evento; vuelve a listar el
origen exacto, exige un servidor vivo sin sesiones, ejecuta únicamente
`tmux -S SOCKET kill-server` como el usuario configurado y confirma que el
espacio desapareció. Si tmux deja un socket huérfano, elimina solo la misma
generación de socket capturada antes del cierre tras esa verificación
(identidad de inode más mtime, para no confundir un inode reciclado tras
unlink+recreate con el socket anterior) y conserva un socket recreado. Rechaza
el cierre si el servidor ya no existe o apareció una sesión.

Un residual que Sermo no tiene permitido identificar y matar se **reporta, no se mata**:
un fallo limpio `orphan_processes` es más seguro que matar el proceso equivocado.

Contrato de implementación: el motor registra exactamente dos pasos diferidos —
emitir un evento del resultado final (registrado primero, de modo que se dispara en toda
ruta de salida), y liberar el lock de operación (registrado solo tras una adquisición
exitosa). Cualquier paso posterior puede retornar temprano; la limpieza nunca se repite por retorno,
y una operación bloqueada, fallida o con panic no puede filtrar el lock ni omitir su
evento. Estados de resultado: `ok`, `blocked`, `preflight_failed`,
`postflight_failed`, `failed`, `orphan_processes`. Un reload (SIGHUP) o un apagado
cancela una operación en vuelo, así que las esperas acotadas del motor reportan
`operation cancelled during <fase>` en lugar de un timeout: una acción interrumpida
no debe leerse como un servicio lento, y todo despliegue con `--with-config`
recarga el daemon. El motor no
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
[Métricas](rules.es.md#métricas) para las listas de métricas `scope: service` y `scope: system`.

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

- **`then.expand` y `then.makestep` se controlan por política.** Ambas cambian el host, así que se ejecutan como mucho una vez por
  `policy.cooldown`, y cada intento inicia el cooldown para que un objetivo que falla no
  se reintente en cada ciclo. `then.makestep` — que pide al chronyd local que salte la
  hora del sistema — además *exige* un cooldown positivo y actúa solo ante un exceso de
  desfase. **Nunca lo habilites en un host mon u osd de ceph**: un salto de reloj puede
  costarle el quórum a un monitor, así que allí alerta en su lugar.
- **La configuración es entrada confiable, propiedad de root.** Los checks `command` y los `hook`s de watch
  ejecutan su `argv` **como root** (nunca mediante un shell). Mantén `/etc/sermo` escribible
  solo por root; cualquiera que pueda editarlo puede ejecutar código como root. Los secretos pertenecen al
  entorno (`${env:NAME}`), no al archivo.
- **La interfaz web** (cuando está habilitada) puede start/stop/restart/reload/resume/repair servicios y
  monitor/unmonitor objetivos como root, así que está endurecida por defecto: **se enlaza a
  loopback** (`127.0.0.1`), soporta
  **autenticación** con un rol de invitado de solo lectura, requiere la cabecera **`X-Sermo-Csrf`**
  en cada petición que cambia estado (bloqueando la falsificación entre sitios desde un
  navegador), y establece timeouts HTTP. Habla HTTP plano, así que para alcanzarla desde fuera
  del host **debes** ponerla detrás de un reverse proxy con terminación TLS
  (nginx/Apache) — véase
  [detrás de un reverse proxy](configuration.es.md#detrás-de-un-proxy-inverso-requerido-para-exponerlo).
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
2. **Checks de lock externos controlados por un guard** — una comprobación (`file_exists`,
   `process`, …) sobre una señal que Sermo *no* posee: un proceso de backup, un
   archivo de flag externo. Nunca apuntes tal check bajo `<paths.runtime>/locks` —
   eso duplica el mecanismo 1.

Un `lockfile:` creado por un servicio en el catálogo es diferente: es un health check
controlado para un artefacto de runtime regular, como `socket:`, y no bloquea
operaciones a menos que el operador también escriba una regla de guard explícita.

El **lock interno de operación** (`<paths.runtime>/ops/<service>.lock`)
serializa start/stop/restart/reload/resume/repair para un servicio. Está deliberadamente fuera del
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
  nunca se señaliza. Sermo sí registra *qué* ruta ocupaba el binario borrado, para
  que la comprobación `stale_binary` pueda nombrarla, pero eso es sólo diagnóstico: ese
  proceso sigue sin resolver exe, no coincide con nada y nunca se señaliza. No
  hagas que una ruta borrada autorice emparejamiento ni kill.
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
`defaults.stop_policy`. La fase de stop de un stop explícito o de un restart
`staged`:

1. `Stop` del backend, esperar `graceful_timeout`, descubrir residuales.
2. Sin residuales → stop limpio.
3. Residuales con `force_kill: false` → `orphan_processes` (y un restart **no**
   inicia).
4. Residuales con `force_kill: true` o `auto` → clasificar cada uno: MATABLE
   solo cuando cada campo de `kill_only_if` coincide, o cuando coincide con una
   identidad estricta emparejada de `processes:` (exe resuelto exacto **y** UID
   real; un exe irresoluble y los PIDs protegidos nunca son matables). SIGTERM
   al conjunto matable, esperar `term_timeout`, redescubrir; SIGKILL a lo que
   quede del conjunto matable, esperar `kill_timeout`, redescubrir. Un residual
   que nunca coincidió nunca se señaliza.
5. El resultado es `ok` solo cuando no queda ningún residual — ya sea que el
   superviviente fuera deliberadamente perdonado o sobreviviera al SIGKILL, el resultado es
   `orphan_processes` y lista cada proceso restante.

## Procesos stray y `reap`

Un **stray** es un proceso que el backend de init atribuye al control group del
servicio y que ningún selector configurado reclama (sin coincidencia de
`processes:`, sin pidfile), que no es el proceso principal de la unidad y que no
forma parte del árbol de procesos vivo de ese principal.

La pertenencia al control group es la atribución del propio kernel, así que un
stray sí pertenece al servicio — Sermo simplemente no puede decir qué es. Excluir
el árbol del principal es lo que hace útil la etiqueta: los workers de un daemon
son sus descendientes, así que una unidad sana no produce ningún stray, mientras
que un proceso que llegó al control group sin una cadena de ascendencia hasta el
principal fue reparentado a PID 1. Esa es la firma de un resto — una sonda que se
demonizó, un hijo que el daemon nunca recogió, un superviviente de una
encarnación anterior.

Los strays aparecen en `sermoctl processes` como `stray=true`, en la tabla de
procesos del dashboard con `stray` en la columna Role, y como la comprobación inyectada
`strays` (ver [configuration.es.md](configuration.es.md)). Nada más cambia: un
stray se sigue descubriendo, sigue contando en los totales de procesos del servicio
y sigue siendo un residual de un stop como cualquier otro proceso.

### Un stop nunca hace reap

`reap.kill_only_if` **no** se consulta durante un stop, así que un restart nunca
limpia un stray. La fase de stop señaliza exactamente lo que autoriza
`stop_policy`, y un stray que no puede identificar se reporta, no se mata.

Que eso bloquee un restart depende del `KillMode` de la unidad:

- `KillMode=control-group` (el valor por defecto de systemd): el stop se lleva el
  control group entero, así que no sobrevive ningún stray para ser residual y nada
  cambia.
- `KillMode=process` / `none` (sshd, NetworkManager, los daemons de libvirt): los
  supervivientes quedan. Los que reclama un selector `delegated: true` están
  excluidos de los residuales por diseño; un stray no, así que termina la operación
  en `orphan_processes` con el servicio parado.
- `restart_policy: native`: el backend de init hace un restart atómico, así que no
  hay fase de residuales en absoluto.

En ese tercer caso el resultado nombra los strays y apunta al verbo que los limpia:

```console
$ sermoctl restart ssh
ssh restart orphan_processes
reason: 1 residual process(es) remain after stop (1 stray, unaccounted for by any
  selector; `sermoctl reap` lists them and, with reap.kill_only_if declared, clears them)
  residual pid=4711 exe=/usr/bin/tmux stray=true
```

El operador decide entonces: marcarlo `delegated: true` si la unidad lo mantiene
vivo a propósito, añadir un selector si es un rol que Sermo debería conocer, o
declarar `reap.kill_only_if` y limpiarlo. Que un stop hiciera reap por su cuenta
significaría remediación automática matando procesos que Sermo no puede nombrar, que
es el riesgo que este diseño rechaza.

`sermoctl reap SERVICE` los lista e informa de cuántos se señalizarían. No toma
ningún lock, no emite ningún evento y no toca nada.

`sermoctl reap SERVICE --apply` los señaliza por la vía normal de operaciones
—lock de operación, locks nombrados activos, guards, exactamente un evento— y no
relaja ninguna invariante:

- La autoridad viene solo del `reap.kill_only_if` del propio servicio, el mismo
  selector emparejado `users` + `exe_any` que usa `stop_policy`, comprobado por la
  misma barrera. Sin el bloque no hay nada autorizado, así que `--apply` reporta
  cada stray y no señaliza ninguno.
- Los procesos delegados, un exe irresoluble, PID 1 y los kernel threads se
  rechazan exactamente igual que durante un stop.
- La escalada es SIGTERM, `term_timeout`, redescubrir, SIGKILL, `kill_timeout`,
  redescubrir, usando los tiempos del propio `stop_policy` del servicio y
  releyendo `/proc` en vivo entre rondas.
- El resultado es `ok` solo cuando no queda ningún stray; uno perdonado o
  superviviente lo convierte en `orphan_processes` y lista lo que queda.

Ninguna acción de regla puede hacer reap. Hacer reap significa terminar un proceso
que Sermo no puede nombrar, y esa decisión se queda con el operador.

### Higiene propia de sermod al arrancar

sermod termina lo que encuentra en **su propio** control group de unidad de init
al arrancar, antes de haber lanzado nada él mismo — así que cualquier cosa que
haya ahí pertenece a una encarnación anterior que el sistema de init no limpió
(`KillMode=process` o `KillMode=none`). Es la única excepción a «toda
señalización de servicio pasa por el motor de operaciones», y es deliberadamente
estrecha:

- Solo el control group propio de sermod, y solo cuando ese grupo es una unidad
  **service** de systemd cuyo nombre es el del propio sermod. Arrancado desde un
  shell de login, sermod comparte su scope con el shell del operador y con sshd;
  ejecutado dentro de una unidad con nombre ajeno — el servicio de un agente de
  CI, un supervisor de contenedores, un envoltorio de systemd-run — los procesos
  vecinos pertenecen a ese otro. En ambos casos no hace nada en absoluto.
- Solo `SIGTERM`. Un resto que lo ignore se reporta y se deja en paz.
- Un evento por proceso señalizado.
- `engine.reap_own_strays: false` lo desactiva.

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
- **Concurrencia acotada**: cada servicio ejecuta como máximo una operación a la
  vez (el lock de operación entre procesos), y la remediación automática está
  limitada por el bloque `policy` obligatorio por servicio (cooldown,
  `max_actions`, backoff). La ejecución de checks comparte un pool global
  (`engine.max_parallel_checks`). Un check que no consigue un slot espera — no
  se omite.
- **Apagado** (SIGTERM/SIGINT): dejar de iniciar ciclos, cancelar los contextos de los workers;
  una operación en curso observa la cancelación, su limpieza diferida libera
  el lock y emite el evento, y un servicio parcialmente detenido se deja como está —
  nunca se mata a la fuerza por el apagado.
- **El reload del daemon** valida la nueva configuración, intercambia workers/watches
  preservando el estado de runtime por servicio, y mantiene la generación en ejecución cuando
  la nueva configuración es inválida.
