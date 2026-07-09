# Arquitectura de Sermo

Este documento describe, con diagramas, cómo funciona Sermo de extremo a
extremo: el daemon y sus señales, la resolución del catálogo, el pipeline de una
operación (con preflight, guards y locks), los estados de los locks y el ciclo
de monitorización.

Los diagramas son fieles al código; los ficheros y funciones ancla se citan al
final de cada sección para mantenerlos sincronizados.

---

## 1. Arquitectura general

Un único daemon (`sermod`) carga la configuración y el catálogo, construye una
**flota** de *Workers* (uno por servicio) y *Watches* (uno por recurso de host o
app), y los ejecuta en bucle. El CLI (`sermoctl`) y la Web UI hablan con el
daemon vía HTTP y señales. Las acciones sobre servicios pasan siempre por
`operation.Engine`, que coordina locks, preflight, guards y el gestor de init.

```mermaid
flowchart TB
  subgraph clients["Clientes"]
    CLI["sermoctl (CLI)"]
    BROWSER["Navegador (Web UI)"]
  end

  subgraph daemon["sermod (daemon)"]
    SIG{"Señales"}
    MON["Monitor<br/>(genera y recarga)"]
    SCH["Scheduler<br/>(bucle cada Interval)"]
    WEB["web.Server"]
    subgraph fleet["Flota"]
      W["Worker (1 por servicio)"]
      WA["Watch (1 por recurso/app)"]
    end
  end

  subgraph cfg["Config + Catálogo empaquetado"]
    CAT[("catalog/<br/>services · apps · libs · patterns")]
    USR[("/etc/sermo/<br/>sermo.yml + services")]
    RES["Resolve → Resolved.Tree"]
  end

  subgraph backends["Ejecución"]
    OP["operation.Engine"]
    LK["locks (oplock + scanner)"]
    SM["servicemgr (systemd/openrc)"]
    ST[("state store<br/>SLA · eventos · métricas")]
    NT["notifiers (email/slack/teams)"]
  end

  CLI -- "SIGHUP (daemon reload)" --> SIG
  CLI -- "HTTP /api" --> WEB
  BROWSER --> WEB
  WEB -- "/reload → SIGHUP propio" --> SIG

  SIG -- "SIGTERM/SIGINT" --> MON
  SIG -- "SIGHUP" --> MON
  MON --> SCH --> W & WA

  CAT --> RES
  USR --> RES
  RES --> W & WA

  W --> OP
  W -- "checks + rules" --> ST
  W --> NT
  WA -- "checks" --> NT
  OP --> LK
  OP --> SM
  WEB --> ST
```

> Anclas: `cmd/sermod/main.go`, `internal/app/monitor.go`,
> `internal/app/scheduler.go`, `internal/app/worker.go`, `internal/app/watch.go`.

---

## 2. Resolución del catálogo (services / apps / libs / patterns)

El catálogo empaquetado (cargado desde el directorio compilado en el binario) y
la config de usuario se combinan en tres etapas: **Load** (registra todos los
documentos), **applyOSSelectors** (colapsa los bloques `os:` según el SO
detectado) y **Resolve** (fusiona defaults + catálogo + override de usuario,
expande variables y secciones). El resultado es un `Resolved.Tree` plano que
consumen el gestor de init, los checks, las rules y el descubrimiento de
procesos.

```mermaid
flowchart LR
  subgraph files["Ficheros YAML"]
    S["catalog/services/*.yml"]
    A["catalog/apps/*.yml"]
    L["catalog/libs/*.yml"]
    P["catalog/patterns/*.yml"]
    U["/etc/sermo/services/*.yml<br/>(uses: catalogservice)"]
  end

  files --> LOAD["1 · Load<br/>registries: CatalogServices/Apps/Libraries/Services"]
  LOAD --> OSC["2 · applyOSSelectors<br/>collapseOS(${os})<br/>SERMO_OS → /etc/os-release → linux"]
  OSC --> MERGE["3 · Resolve → mergedService<br/>defaults + catálogo + override usuario"]
  MERGE --> VARS["expansionVariables<br/>${name} ${port} ${user} ${config} ${X_binary}"]
  VARS --> EXP["expandApps · expandRestartOnChange<br/>expandAnalyze · expandPidfile/Socket/Lockfile"]
  EXP --> TREE[("Resolved.Tree")]

  TREE --> SC["ServiceCandidates(tree, backend)<br/>→ unidades init reales"]
  SC --> SMc["servicemgr"]
  TREE --> CHKc["checks engine"]
  TREE --> RULc["rules engine"]
  TREE --> PROCc["process discovery"]

  A -. "service apps: [app]" .-> S
  L -. "service restart_on_change.libraries: [lib]" .-> S
  P -. "checks.*.analyze.use: [pattern]" .-> S
```

**Composición:** un *service* enlaza *apps* con `apps: [..]` (fusiona su preflight
y variables), y puede enlazar reinicios a cambios de librerías o versiones de app con
`restart_on_change.libraries` / `restart_on_change.apps`; los *patterns* se referencian
en `checks.*.analyze.use: [..]` para parsear la salida de los checks.

**Selectores de SO:** `collapseOS` resuelve `os: { ubuntu: {...}, debian: {...},
default: {...} }` a cualquier profundidad. Ejemplo: en Ubuntu, la unidad systemd
de `dhcpd` se reescribe a `isc-dhcp-server`. El SO se detecta por `SERMO_OS` →
`ID=` de `/etc/os-release` → `linux`.

> Anclas: `internal/config/loader.go`, `internal/config/osselect.go`
> (`applyOSSelectors`/`collapseOS`), `internal/config/resolve.go`
> (`Resolve`/`mergedService`), `internal/config/model.go` (`ServiceCandidates`,
> `CategoryService`/`CategoryApp`/`CategoryLibrary`/`CategoryPatterns`).

---

## 3. Pipeline de una operación (preflight · guard · locks)

Toda acción sobre un servicio (`start`/`stop`/`restart`/`reload`/`resume`) entra
por `Engine.Do` y la orquesta `Engine.run`. El orden es estricto: se adquiere el
**lock de operación** (serializa por servicio), se comprueban los **named locks**
(trabajo externo en curso), se corre **preflight**, se evalúan los **guards** y
solo entonces se invoca al gestor de init; al final corre **postflight**. En
cualquier salida se emite el evento.

```mermaid
flowchart TD
  REQ["Acción: start / stop / restart / reload / resume<br/>(sermoctl o Web UI → defaultOperate)"] --> DO["Engine.Do → Engine.run(plan)"]
  DO --> CE{"ConfigError?"}
  CE -- "sí" --> RF1["ResultFailed"]
  CE -- "no" --> L1["2 · AcquireLock (oplock, por servicio)"]
  L1 -- "retenido (activo)" --> B1["ResultBlocked<br/>operation in progress"]
  L1 -- "ok (release en defer)" --> L2["3 · Named runtime locks (scanner)"]
  L2 -- "hay lock activo" --> B2["ResultBlocked<br/>blocked by active runtime lock"]
  L2 -- "libre" --> PF{"4 · Preflight<br/>(solo start/restart/reload/resume)"}
  PF -- "check requerido falla" --> RPF["ResultPreflightFailed<br/>(no toca el servicio)"]
  PF -- "ok" --> GD{"5 · Guard rules<br/>if: failed/active de un check → block"}
  GD -- "bloquea" --> B3["ResultBlocked: «message»"]
  GD -- "pasa" --> ACT["6-9 · Service Manager<br/>Stop/Start/Reload/Resume (systemd/openrc)"]
  ACT -- "error" --> RF2["ResultFailed"]
  ACT -- "ok" --> POST{"10 · Postflight"}
  POST -- "falla" --> RPOST["ResultPostflightFailed"]
  POST -- "ok" --> OK["ResultOK"]

  B1 --> EMIT
  B2 --> EMIT
  B3 --> EMIT
  RF1 --> EMIT
  RF2 --> EMIT
  RPF --> EMIT
  RPOST --> EMIT
  OK --> EMIT["defer Emit(result)<br/>action / suppressed / error<br/>→ evento + log + notificación"]
```

- **Lock de operación** (`oplock`): serializa start/stop/restart de un mismo
  servicio; si está retenido por otra operación activa, devuelve `ResultBlocked`.
- **Named locks**: representan trabajo externo (p.ej. un backup que tomó un lock
  con nombre). Mientras estén **activos** bloquean las acciones del servicio.
- **Preflight**: checks que deben pasar *antes* de tocar el servicio (p.ej.
  `dhcpd` valida su config con `preflight: { config: { type: command, ... } }`).
  Un fallo aborta sin ejecutar la acción.
- **Guard**: reglas `type: guard` con `blocks: [restart, start]` y una condición
  `if: { failed: { check: X } }`. Se evalúan **en el momento** (no en ventana)
  contra la caché de checks; la primera que dispara bloquea con su `message`.

> Anclas: `internal/operation/engine.go` (`Do`/`run`),
> `internal/operation/build.go` (`sectionRunner`, `guardClosure`),
> `internal/rules/eval.go` (`Guard`/`Evaluator.Eval`).

---

## 4. Estados de un lock (`classify`)

`classify` decide el estado de un lock en un orden fijo: primero el vencimiento
(TTL), luego la liveness del propietario y por último la detección de reuso de
PID (comparando los *start ticks* del proceso). Solo los locks **activos**
bloquean acciones; los **expired**/**stale** son reclamables.

```mermaid
flowchart TD
  C["classify(lock, now, ProcessProber)"] --> Q1{"now ≥ expires_at?"}
  Q1 -- "sí" --> EXP["StateExpired<br/>(TTL vencido · reclamable)"]
  Q1 -- "no" --> Q2{"owner_pid>0 y Alive(pid)?<br/>(syscall.Kill(pid,0))"}
  Q2 -- "no vivo" --> STD["StateStale: dead owner"]
  Q2 -- "vivo" --> Q3{"StartTicks(pid) == owner_start_ticks?<br/>(/proc/pid/stat campo 22)"}
  Q3 -- "no coincide" --> STR["StateStale: pid reuse"]
  Q3 -- "coincide" --> ACTV["StateActive<br/>(bloquea acciones)"]

  ACTV -. "ReleaseInactive → rechazado" .-> KEEP["se mantiene"]
  EXP -. "reclaimStale → borra" .-> GONE["liberado"]
  STD -. "reclaimStale → borra" .-> GONE
  STR -. "reclaimStale → borra" .-> GONE
```

**Tipos de lock:** `OperationLocker` (en `<RuntimeDir>/ops/`, serializa acciones),
`NamedLocker` (en `<RuntimeDir>/locks/`, `Hold`/`Pin`/`Release`/`ReleaseInactive`
para trabajo externo) y `Scanner` (lee y clasifica los locks para la UI y el
motor). El `ProcessProber` (interfaz `Alive`/`StartTicks`) abstrae el acceso a
`/proc` para detectar propietarios muertos y reuso de PID.

> Anclas: `internal/locks/lock.go` (`classify`, `ProcessProber`),
> `internal/locks/oplock.go`, `named.go`, `scanner.go`, `proc.go`.

---

## 5. Señales y ciclo de vida

El arranque carga config, detecta el gestor de init, abre el state store,
construye la flota, levanta el servidor web, escribe el pidfile y entra en el
bucle del `Scheduler`. **SIGHUP** (que envía `sermoctl daemon reload` o el
endpoint `/reload`) dispara una recarga sin parar el daemon: valida la nueva
config, captura el estado en curso, reconstruye la flota y lo restaura.
**SIGTERM/SIGINT** cancelan el contexto para un apagado ordenado.

```mermaid
flowchart TD
  subgraph boot["Arranque (main.run)"]
    A1["Load + Validate config"] --> A2["Detect servicemgr"]
    A2 --> A3["Instance lock (anti-duplicado)"]
    A3 --> A4["Abrir state store"]
    A4 --> A5["Build notifiers"]
    A5 --> A6["BuildWorkers / BuildWatches"]
    A6 --> A7["web.Server (goroutine)"]
    A7 --> A8["Escribir pidfile<br/>/run/sermo/sermod.pid"]
    A8 --> A9["Monitor.Run → Scheduler"]
  end

  A9 --> LOOP["Scheduler: stagger + runCycler<br/>cada Worker/Watch en su goroutine, cada Interval"]

  SH["SIGHUP<br/>(sermoctl daemon reload / web /reload)"] --> RELOAD["Monitor.Reload:<br/>validar nueva config → parar generación →<br/>capturar estado → rebuild → restaurar estado → nueva generación"]
  RELOAD --> LOOP

  STERM["SIGTERM / SIGINT"] --> SHUT["cancelar ctx → drenar workers/watches<br/>→ quitar pidfile → cerrar store"]
```

> Anclas: `cmd/sermod/main.go` (arranque, handlers de señal),
> `internal/app/monitor.go` (`Reload`), `internal/app/scheduler.go` (`Run`).

---

## 6. Ciclo de un Worker (por servicio)

Cada Worker, en cada tick del intervalo, ejecuta sus checks, registra SLA/health,
publica el estado para la Web y evalúa las reglas: la **remediación** actualiza
las ventanas de todas las reglas y ejecuta la primera que dispara (sujeta a guard
y a la política de cooldown/backoff), y las **alertas** notifican. El primer
ciclo tras arrancar/recargar es solo de observación.

```mermaid
flowchart LR
  RC["Worker.RunCycle"] --> P{"¿paused?"}
  P -- "sí" --> SKIP["skip"]
  P -- "no" --> SAMP["Sample métricas"]
  SAMP --> RUN["Run checks + applyGates"]
  RUN --> SLA["Record SLA / health"]
  SLA --> PUB["Publish a Web"]
  PUB --> REM["runRemediation:<br/>update windows → Guard? → Policy.Allow? → Operate/Cascade"]
  REM --> AL["runAlerts → notificaciones"]
  AL --> PS["PersistState"]
```

El mismo mecanismo de guard (`rules.Guard`) que protege el pipeline de operación
se aplica aquí antes de remediar. Los `checks` alimentan a la vez los
health-checks, el preflight/postflight y las condiciones de las reglas; los
notifiers son pluggables (email/slack/teams).

> Anclas: `internal/app/worker.go` (`RunCycle`, `runRemediation`, `runAlerts`),
> `internal/rules/eval.go` (`Guard`).
