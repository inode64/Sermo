# Sermo Architecture

This document describes, with diagrams, how Sermo works end to end: the daemon
and its signals, catalog resolution, the pipeline of an operation (with
preflight, guards and locks), lock states and the monitoring cycle.

The diagrams are faithful to the code; the anchor files and functions are cited
at the end of each section to keep them in sync.

---

## 1. General architecture

A single daemon (`sermod`) loads the configuration and the catalog, builds a
**fleet** of *Workers* (one per service) and *Watches* (one per host or app
resource), and runs them in a loop. The CLI (`sermoctl`) and the Web UI talk to
the daemon over HTTP and signals. Service actions always go through
`operation.Engine`, which coordinates locks, preflight, guards and the init
manager.

```mermaid
flowchart TB
  subgraph clients["Clients"]
    CLI["sermoctl (CLI)"]
    BROWSER["Browser (Web UI)"]
  end

  subgraph daemon["sermod (daemon)"]
    SIG{"Signals"}
    MON["Monitor<br/>(generates and reloads)"]
    SCH["Scheduler<br/>(loop every Interval)"]
    WEB["web.Server"]
    subgraph fleet["Fleet"]
      W["Worker (1 per service)"]
      WA["Watch (1 per resource/app)"]
    end
  end

  subgraph cfg["Config + packaged catalog"]
    CAT[("catalog/<br/>services · apps · libs · patterns")]
    USR[("/etc/sermo/<br/>sermo.yml + services")]
    RES["Resolve → Resolved.Tree"]
  end

  subgraph backends["Execution"]
    OP["operation.Engine"]
    LK["locks (oplock + scanner)"]
    SM["servicemgr (systemd/openrc)"]
    ST[("state store<br/>SLA · events · metrics")]
    NT["notifiers (email/slack/teams)"]
  end

  CLI -- "SIGHUP (daemon reload)" --> SIG
  CLI -- "HTTP /api" --> WEB
  BROWSER --> WEB
  WEB -- "/reload → own SIGHUP" --> SIG

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

> Anchors: `cmd/sermod/main.go`, `internal/app/monitor.go`,
> `internal/app/scheduler.go`, `internal/app/worker.go`, `internal/app/watch.go`.

---

## 2. Catalog resolution (services / apps / libs / patterns)

The packaged catalog (loaded from the directory compiled into the binary) and
the user config are combined in three stages: **Load** (registers every
document), **applyOSSelectors** (collapses the `os:` blocks according to the
detected OS) and **Resolve** (merges defaults + catalog + user override, expands
variables and sections). The result is a flat `Resolved.Tree` consumed by the
init manager, the checks, the rules and process discovery.

```mermaid
flowchart LR
  subgraph files["YAML files"]
    S["catalog/services/*.yml"]
    A["catalog/apps/*.yml"]
    L["catalog/libs/*.yml"]
    P["catalog/patterns/*.yml"]
    U["/etc/sermo/services/*.yml<br/>(uses: catalogservice)"]
  end

  files --> LOAD["1 · Load<br/>registries: CatalogServices/Apps/Libraries/Services"]
  LOAD --> OSC["2 · applyOSSelectors<br/>collapseOS(${os})<br/>SERMO_OS → /etc/os-release → linux"]
  OSC --> MERGE["3 · Resolve → mergedService<br/>defaults + catalog + user override"]
  MERGE --> VARS["expansionVariables<br/>${name} ${port} ${user} ${config} ${X_binary}"]
  VARS --> EXP["expandApps · expandRestartOnChange<br/>expandAnalyze · expandPidfile/Socket/Lockfile"]
  EXP --> TREE[("Resolved.Tree")]

  TREE --> SC["ServiceCandidates(tree, backend)<br/>→ real init units"]
  SC --> SMc["servicemgr"]
  TREE --> CHKc["checks engine"]
  TREE --> RULc["rules engine"]
  TREE --> PROCc["process discovery"]

  A -. "service apps: [app]" .-> S
  L -. "service restart_on_change.libraries: [lib]" .-> S
  P -. "checks.*.analyze.use: [pattern]" .-> S
```

**Composition:** a *service* links *apps* with `apps: [..]` (merging their
preflight and variables), and can tie restarts to library changes or app
versions with `restart_on_change.libraries` / `restart_on_change.apps`;
*patterns* are referenced in `checks.*.analyze.use: [..]` to parse check output.

**OS selectors:** `collapseOS` resolves `os: { ubuntu: {...}, debian: {...},
default: {...} }` at any depth. Example: on Ubuntu, the systemd unit for `dhcpd`
is rewritten to `isc-dhcp-server`. The OS is detected from `SERMO_OS` → `ID=` in
`/etc/os-release` → `linux`.

> Anchors: `internal/config/loader.go`, `internal/config/osselect.go`
> (`applyOSSelectors`/`collapseOS`), `internal/config/resolve.go`
> (`Resolve`/`mergedService`), `internal/config/model.go` (`ServiceCandidates`,
> `CategoryService`/`CategoryApp`/`CategoryLibrary`/`CategoryPatterns`).

---

## 3. The pipeline of an operation (preflight · guard · locks)

Every action on a service (`start`/`stop`/`restart`/`reload`/`resume`) enters
through `Engine.Do` and is orchestrated by `Engine.run`. The order is strict:
the **operation lock** is acquired (serializes per service), the **named locks**
are checked (external work in progress), **preflight** runs, the **guards** are
evaluated and only then is the init manager invoked; at the end **postflight**
runs. On any exit path the event is emitted.

```mermaid
flowchart TD
  REQ["Action: start / stop / restart / reload / resume<br/>(sermoctl or Web UI → defaultOperate)"] --> DO["Engine.Do → Engine.run(plan)"]
  DO --> CE{"ConfigError?"}
  CE -- "yes" --> RF1["ResultFailed"]
  CE -- "no" --> L1["2 · AcquireLock (oplock, per service)"]
  L1 -- "held (active)" --> B1["ResultBlocked<br/>operation in progress"]
  L1 -- "ok (release on defer)" --> L2["3 · Named runtime locks (scanner)"]
  L2 -- "active lock present" --> B2["ResultBlocked<br/>blocked by active runtime lock"]
  L2 -- "free" --> PF{"4 · Preflight<br/>(only start/restart/reload/resume)"}
  PF -- "required check fails" --> RPF["ResultPreflightFailed<br/>(does not touch the service)"]
  PF -- "ok" --> GD{"5 · Guard rules<br/>if: failed/active of a check → block"}
  GD -- "blocks" --> B3["ResultBlocked: «message»"]
  GD -- "passes" --> ACT["6-9 · Service Manager<br/>Stop/Start/Reload/Resume (systemd/openrc)"]
  ACT -- "error" --> RF2["ResultFailed"]
  ACT -- "ok" --> POST{"10 · Postflight"}
  POST -- "fails" --> RPOST["ResultPostflightFailed"]
  POST -- "ok" --> OK["ResultOK"]

  B1 --> EMIT
  B2 --> EMIT
  B3 --> EMIT
  RF1 --> EMIT
  RF2 --> EMIT
  RPF --> EMIT
  RPOST --> EMIT
  OK --> EMIT["defer Emit(result)<br/>action / suppressed / error<br/>→ event + log + notification"]
```

- **Operation lock** (`oplock`): serializes start/stop/restart of the same
  service; if it is held by another active operation, it returns `ResultBlocked`.
- **Named locks**: represent external work (e.g. a backup that took a named
  lock). While they are **active** they block the service's actions.
- **Preflight**: checks that must pass *before* touching the service (e.g.
  `dhcpd` validates its config with `preflight: { config: { type: command, ... } }`).
  A failure aborts without running the action.
- **Guard**: `type: guard` rules with `blocks: [restart, start]` and an
  `if: { failed: { check: X } }` condition. They are evaluated **at that moment**
  (not over a window) against the check cache; the first one that fires blocks
  with its `message`.

> Anchors: `internal/operation/engine.go` (`Do`/`run`),
> `internal/operation/build.go` (`sectionRunner`, `guardClosure`),
> `internal/rules/eval.go` (`Guard`/`Evaluator.Eval`).

---

## 4. Lock states (`classify`)

`classify` decides a lock's state in a fixed order: first expiry (TTL), then
owner liveness and finally PID-reuse detection (comparing the process's *start
ticks*). Only **active** locks block actions; **expired**/**stale** ones are
reclaimable.

```mermaid
flowchart TD
  C["classify(lock, now, ProcessProber)"] --> Q1{"now ≥ expires_at?"}
  Q1 -- "yes" --> EXP["StateExpired<br/>(TTL elapsed · reclaimable)"]
  Q1 -- "no" --> Q2{"owner_pid>0 and Alive(pid)?<br/>(syscall.Kill(pid,0))"}
  Q2 -- "not alive" --> STD["StateStale: dead owner"]
  Q2 -- "alive" --> Q3{"StartTicks(pid) == owner_start_ticks?<br/>(/proc/pid/stat field 22)"}
  Q3 -- "no match" --> STR["StateStale: pid reuse"]
  Q3 -- "match" --> ACTV["StateActive<br/>(blocks actions)"]

  ACTV -. "ReleaseInactive → rejected" .-> KEEP["kept"]
  EXP -. "reclaimStale → deletes" .-> GONE["released"]
  STD -. "reclaimStale → deletes" .-> GONE
  STR -. "reclaimStale → deletes" .-> GONE
```

**Lock types:** `OperationLocker` (in `<RuntimeDir>/ops/`, serializes actions),
`NamedLocker` (in `<RuntimeDir>/locks/`, `Hold`/`Pin`/`Release`/`ReleaseInactive`
for external work) and `Scanner` (reads and classifies locks for the UI and the
engine). The `ProcessProber` (interface `Alive`/`StartTicks`) abstracts access
to `/proc` to detect dead owners and PID reuse.

> Anchors: `internal/locks/lock.go` (`classify`, `ProcessProber`),
> `internal/locks/oplock.go`, `named.go`, `scanner.go`, `proc.go`.

---

## 5. Signals and lifecycle

Startup loads config, detects the init manager, opens the state store, builds
the fleet, brings up the web server, writes the pidfile and enters the
`Scheduler` loop. **SIGHUP** (sent by `sermoctl daemon reload` or the `/reload`
endpoint) triggers a reload without stopping the daemon: it validates the new
config, captures the in-flight state, rebuilds the fleet and restores it.
**SIGTERM/SIGINT** cancel the context for an orderly shutdown.

```mermaid
flowchart TD
  subgraph boot["Startup (main.run)"]
    A1["Load + Validate config"] --> A2["Detect servicemgr"]
    A2 --> A3["Instance lock (anti-duplicate)"]
    A3 --> A4["Open state store"]
    A4 --> A5["Build notifiers"]
    A5 --> A6["BuildWorkers / BuildWatches"]
    A6 --> A7["web.Server (goroutine)"]
    A7 --> A8["Write pidfile<br/>/run/sermo/sermod.pid"]
    A8 --> A9["Monitor.Run → Scheduler"]
  end

  A9 --> LOOP["Scheduler: stagger + runCycler<br/>each Worker/Watch in its goroutine, every Interval"]

  SH["SIGHUP<br/>(sermoctl daemon reload / web /reload)"] --> RELOAD["Monitor.Reload:<br/>validate new config → stop generation →<br/>capture state → rebuild → restore state → new generation"]
  RELOAD --> LOOP

  STERM["SIGTERM / SIGINT"] --> SHUT["cancel ctx → drain workers/watches<br/>→ remove pidfile → close store"]
```

> Anchors: `cmd/sermod/main.go` (startup, signal handlers),
> `internal/app/monitor.go` (`Reload`), `internal/app/scheduler.go` (`Run`).

---

## 6. A Worker's cycle (per service)

Each Worker, on every interval tick, runs its checks, records SLA/health,
publishes the state for the Web and evaluates the rules: **remediation** updates
the windows of all rules and runs the first one that fires (subject to guard and
the cooldown/backoff policy), and **alerts** notify. The first cycle after
startup/reload is observation-only.

```mermaid
flowchart LR
  RC["Worker.RunCycle"] --> P{"paused?"}
  P -- "yes" --> SKIP["skip"]
  P -- "no" --> SAMP["Sample metrics"]
  SAMP --> RUN["Run checks + applyGates"]
  RUN --> SLA["Record SLA / health"]
  SLA --> PUB["Publish to Web"]
  PUB --> REM["runRemediation:<br/>update windows → Guard? → Policy.Allow? → Operate/Cascade"]
  REM --> AL["runAlerts → notifications"]
  AL --> PS["PersistState"]
```

The same guard mechanism (`rules.Guard`) that protects the operation pipeline
applies here before remediating. The `checks` feed the health-checks, the
preflight/postflight and the rule conditions all at once; the notifiers are
pluggable (email/slack/teams).

> Anchors: `internal/app/worker.go` (`RunCycle`, `runRemediation`, `runAlerts`),
> `internal/rules/eval.go` (`Guard`).
