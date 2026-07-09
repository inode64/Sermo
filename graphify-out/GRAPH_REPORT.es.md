# Informe de Grafo - Sermo  (2026-06-26)

## Verificación del corpus
- 645 archivos · ~614,103 palabras
- Veredicto: el corpus es lo bastante grande como para que la estructura de grafo aporte valor.

## Resumen
- 906 nodos · 1461 aristas · 58 comunidades (40 mostradas, 18 ligeras omitidas)
- Extracción: 100% EXTRACTED · 0% INFERRED · 0% AMBIGUOUS · INFERRED: 4 aristas (confianza media: 0.75)
- Coste de tokens: 0 entrada · 0 salida

## Frescura del grafo
- Construido a partir del commit: `40fb5d79`
- Ejecuta `git rev-parse HEAD` y compara para comprobar si el grafo está desactualizado.
- Ejecuta `graphify update .` tras cambios en el código (sin coste de API).

## Centros de comunidad (Navegación)
- [[_COMMUNITY_Community 0|Community 0]]
- [[_COMMUNITY_Community 1|Community 1]]
- [[_COMMUNITY_Community 2|Community 2]]
- [[_COMMUNITY_Community 3|Community 3]]
- [[_COMMUNITY_Community 4|Community 4]]
- [[_COMMUNITY_Community 5|Community 5]]
- [[_COMMUNITY_Community 6|Community 6]]
- [[_COMMUNITY_Community 7|Community 7]]
- [[_COMMUNITY_Community 8|Community 8]]
- [[_COMMUNITY_Community 9|Community 9]]
- [[_COMMUNITY_Community 10|Community 10]]
- [[_COMMUNITY_Community 11|Community 11]]
- [[_COMMUNITY_Community 13|Community 13]]
- [[_COMMUNITY_Community 14|Community 14]]
- [[_COMMUNITY_Community 17|Community 17]]
- [[_COMMUNITY_Community 19|Community 19]]
- [[_COMMUNITY_Community 20|Community 20]]
- [[_COMMUNITY_Community 21|Community 21]]
- [[_COMMUNITY_Community 23|Community 23]]
- [[_COMMUNITY_Community 24|Community 24]]
- [[_COMMUNITY_Community 25|Community 25]]
- [[_COMMUNITY_Community 26|Community 26]]
- [[_COMMUNITY_Community 27|Community 27]]
- [[_COMMUNITY_Community 33|Community 33]]
- [[_COMMUNITY_Community 80|Community 80]]
- [[_COMMUNITY_Community 121|Community 121]]
- [[_COMMUNITY_Community 155|Community 155]]
- [[_COMMUNITY_Community 169|Community 169]]
- [[_COMMUNITY_Community 170|Community 170]]
- [[_COMMUNITY_Community 171|Community 171]]
- [[_COMMUNITY_Community 172|Community 172]]
- [[_COMMUNITY_Community 182|Community 182]]
- [[_COMMUNITY_Community 183|Community 183]]
- [[_COMMUNITY_Community 184|Community 184]]
- [[_COMMUNITY_Community 187|Community 187]]
- [[_COMMUNITY_Community 189|Community 189]]
- [[_COMMUNITY_Community 190|Community 190]]
- [[_COMMUNITY_Community 191|Community 191]]
- [[_COMMUNITY_Community 199|Community 199]]
- [[_COMMUNITY_Community 204|Community 204]]
- [[_COMMUNITY_Community 206|Community 206]]
- [[_COMMUNITY_Community 208|Community 208]]
- [[_COMMUNITY_Community 211|Community 211]]
- [[_COMMUNITY_Community 212|Community 212]]
- [[_COMMUNITY_Community 213|Community 213]]
- [[_COMMUNITY_Community 214|Community 214]]
- [[_COMMUNITY_Community 215|Community 215]]
- [[_COMMUNITY_Community 216|Community 216]]
- [[_COMMUNITY_Community 217|Community 217]]
- [[_COMMUNITY_Community 218|Community 218]]
- [[_COMMUNITY_Community 219|Community 219]]
- [[_COMMUNITY_Community 220|Community 220]]
- [[_COMMUNITY_Community 221|Community 221]]
- [[_COMMUNITY_Community 222|Community 222]]
- [[_COMMUNITY_Community 223|Community 223]]
- [[_COMMUNITY_Community 224|Community 224]]
- [[_COMMUNITY_Community 225|Community 225]]
- [[_COMMUNITY_Community 228|Community 228]]

## Nodos dios (más conectados - tus abstracciones centrales)
1. `$()` - 350 aristas
2. `renderServiceDetail()` - 31 aristas
3. `load()` - 27 aristas
4. `Sermo — project conventions` - 24 aristas
5. `saveUIState()` - 22 aristas
6. `renderServices()` - 22 aristas
7. `_()` - 22 aristas
8. `Checks` - 21 aristas
9. `serviceRowParts()` - 20 aristas
10. `Web UI representation` - 19 aristas

## Conexiones sorprendentes (probablemente no las conocías)
- `main()` --calls--> `iter_yaml_files()`  [INFERRED]
  scripts/yaml_format_check.py → scripts/normalize_yaml_flow.py
- `canonicalize()` --calls--> `normalize_line()`  [INFERRED]
  scripts/yaml_format_check.py → scripts/normalize_yaml_flow.py

## Ciclos de importación
- Ninguno detectado.

## Comunidades (58 en total, 18 ligeras omitidas)

### Community 0 - "Community 0"
Cohesión: 0.12
Nodos (26): Path, apply_cfgval(), apply_lock(), main(), read(), write(), apply_spec(), main() (+18 más)

### Community 1 - "Community 1"
Cohesión: 0.03
Nodos (53): $(), activeSearchBox(), allApps, allEvents, allServices, allWatches, appCollapsedGroups, applyRefresh() (+45 más)

### Community 2 - "Community 2"
Cohesión: 0.09
Nodos (46): cpuBarMini(), cpuInline(), cpuTotalsLine(), daemonMetricSummary(), fmtBytes(), fmtMetricValue(), fmtNum(), fmtPct() (+38 más)

### Community 3 - "Community 3"
Cohesión: 0.13
Nodos (29): applyHash(), categoryCounts(), openPanelTarget(), openServiceExpansion(), render(), renderAppFilterCounts(), renderApps(), renderFilterButtonCounts() (+21 más)

### Community 4 - "Community 4"
Cohesión: 0.06
Nodos (32): `also_apply` — cascade to other services, `also_service` — auxiliary init units, App dependencies (`apps`), Auxiliary commands, Built-in variables, Catalog author checklist: init scripts and fallbacks, Categories, Cloning (+24 más)

### Community 5 - "Community 5"
Cohesión: 0.22
Nodos (15): applyUIStateToControls(), getWatchPanel(), initStaticHandlers(), keyboardShortcutsEnabled(), renderWatches(), renderWatchFilterCounts(), renderWatchPanel(), setWatchQuery() (+7 más)

### Community 6 - "Community 6"
Cohesión: 0.09
Nodos (37): act(), actWatch(), beginOperation(), clearEventLog(), compactState(), confirmWatchExpand(), ensureLiveOpsTimer(), fetchReadyReport() (+29 más)

### Community 7 - "Community 7"
Cohesión: 0.16
Nodos (10): _(), _$AI(), _$AR(), constructor(), createElement(), k(), O(), p() (+2 más)

### Community 8 - "Community 8"
Cohesión: 0.06
Nodos (57): appMatches(), appStateRank(), appStateText(), appStatusCell(), appStatusLabel(), capFirst(), categoryBadge(), categoryOf() (+49 más)

### Community 9 - "Community 9"
Cohesión: 0.25
Nodos (8): finishSvcRender(), getJSON(), loadDaemonMetrics(), refreshExpandedServiceDetails(), refreshExpandedServices(), setDaemonMetricWin(), setMetricWin(), syncWindowButtons()

### Community 10 - "Community 10"
Cohesión: 0.40
Nodos (5): loadMe(), updateActivityAdminControls(), updateEventAdminControls(), updatePanicControls(), updateStateCompactControls()

### Community 11 - "Community 11"
Cohesión: 0.40
Nodos (5): setAppSort(), setEvSort(), setSvcSort(), setWatchSort(), toggleSort()

### Community 13 - "Community 13"
Cohesión: 0.17
Nodos (18): confirmAction(), confirmPreflightDisabledReason(), fmtAge(), fmtMonitorSource(), fmtRemain(), fmtSeconds(), fmtTime(), fmtUntilShort() (+10 más)

### Community 14 - "Community 14"
Cohesión: 0.14
Nodos (15): clearEventFilters(), eventFilterKey(), eventGroupKey(), eventSubject(), flushLoadEvents(), groupedEvents(), loadEvents(), renderGlobalEvents() (+7 más)

### Community 17 - "Community 17"
Cohesión: 0.05
Nodos (37): 1. Simplicity and Clarity, 2. Make the Zero Value Useful, 3. Accept Interfaces, Return Structs, Anti-Patterns to Avoid, Avoid Package-Level State, Avoid String Concatenation in Loops, Avoiding Goroutine Leaks, Concurrency Patterns (+29 más)

### Community 19 - "Community 19"
Cohesión: 0.06
Nodos (35): Accessibility (a11y), Accessible authentication (3.3.8) — new in 2.2, ARIA usage (4.1.2), Automated testing, Color contrast (1.4.3, 1.4.6), Common issues by impact, Conformance levels, Consistent help (3.2.6) — new in 2.2 (+27 más)

### Community 20 - "Community 20"
Cohesión: 0.06
Nodos (31): Basic Benchmarks, Basic Fuzz Test, Benchmark with Different Sizes, Benchmarks, Best Practices, Coverage Targets, Excluding Generated Code from Coverage, Fuzz Test with Multiple Inputs (+23 más)

### Community 21 - "Community 21"
Cohesión: 0.07
Nodos (25): Accessibility Code Patterns, ARIA tabs, Dragging movements, Error handling, Form labels, Live regions and notifications, Modal focus trap, Screen reader commands (+17 más)

### Community 23 - "Community 23"
Cohesión: 0.50
Nodos (3): mutate-one-cycle.sh script, mutate(), PATH

### Community 24 - "Community 24"
Cohesión: 0.67
Nodos (3): run-make-check-audited.sh script, PATH, run()

### Community 25 - "Community 25"
Cohesión: 0.33
Nodos (6): lockName(), lockReleaseButton(), lockReleaseDisabled(), lockReleaseDisabledReason(), lockReleaseHintId(), lockReleaseLabel()

### Community 33 - "Community 33"
Cohesión: 0.10
Nodos (28): bucketize(), detailDomId(), detailDomKey(), drawMetricChart(), drawSLAChart(), esc(), eventRows(), expansionCell() (+20 más)

### Community 187 - "Community 187"
Cohesión: 0.07
Nodos (27): Autofs, Cert, Check interdependencies (`requires` / `skip_when_changed`), Checks, Checks, conditions and rules, Count, Database connection (`mysql` / `mariadb`), Default route (`route`) (+19 más)

### Community 206 - "Community 206"
Cohesión: 0.04
Nodos (47): Authentication, Availability (SLA), Availability time series, Behind a reverse proxy (required to expose it), Binary resource variables, `${bindir}` search prefix, Configuration, `conntrack` — netfilter connection table (+39 más)

### Community 208 - "Community 208"
Cohesión: 0.06
Nodos (32): `also_apply` — cascade to other services, `also_service` — auxiliary init units, App dependencies (`apps`), Auxiliary commands, Built-in variables, Catalog author checklist: init scripts and fallbacks, Categories, Cloning (+24 más)

### Community 211 - "Community 211"
Cohesión: 0.05
Nodos (40): AI / agent workflow — standard git commits, Catalog init and reload fallback verification, Catalog: instanced systemd services, Central builders, Configuration file granularity, Configuration structure changes, Daemon performance discipline, Documentation lockstep (+32 más)

### Community 212 - "Community 212"
Cohesión: 0.10
Nodos (20): Action confirmation dialog, Action Endpoints, Attention required, Change template, Daemon / Engine settings panel, Data sources, Events panel, Global rules (+12 más)

### Community 213 - "Community 213"
Cohesión: 0.11
Nodos (17): Alert And Notification Safety, Cleanup, Complete Remote Installation Configuration, Core Rules, Final Report, Full Daemon Resource Observation, Local Preparation, Operation Test Safety (+9 más)

### Community 214 - "Community 214"
Cohesión: 0.08
Nodos (27): Catalog inventory, CLI, Command surface, Exit codes, Mounts, Panic mode, Root flags, sermod daemon flags (+19 más)

### Community 215 - "Community 215"
Cohesión: 0.15
Nodos (12): Categories and library restarts, Core principles, Document kinds, File granularity, Merge rules, Metrics, Output format, Required validation (+4 más)

### Community 216 - "Community 216"
Cohesión: 0.22
Nodos (8): Install paths, make install, OpenRC service, Output format, Packaging artifacts, Permissions, systemd unit, Tests/checks

### Community 217 - "Community 217"
Cohesión: 0.22
Nodos (8): Condition tree, Evaluation order, Output format, Rule model, Rule types, State, Testing, Windows

### Community 218 - "Community 218"
Cohesión: 0.25
Nodos (7): Coding rules, Context and timeout, Error messages, External command pattern, Output expectation, Package guidance, Test requirements

### Community 219 - "Community 219"
Cohesión: 0.25
Nodos (7): Backend detection, Common interface, Naming, Output format, Scope, Status normalization, Testing

### Community 220 - "Community 220"
Cohesión: 0.25
Nodos (7): Discovery sources, Output format, Prime directive, Residual process handling, Safe process identity, Stop policy validation, Tests

### Community 221 - "Community 221"
Cohesión: 0.29
Nodos (6): Audience, Examples, Output format, Required docs topics, Safety wording, Style

### Community 222 - "Community 222"
Cohesión: 0.29
Nodos (6): Common services, Locks, Output format, Required sections, Safety defaults by service class, Service goal

### Community 223 - "Community 223"
Cohesión: 0.29
Nodos (6): Acceptance, Fixtures, Must-not-do in tests, Output format, Required test areas, Test style

### Community 224 - "Community 224"
Cohesión: 0.33
Nodos (5): High-risk services, Mandatory safety checks, Output format, Red flags, Required tests

### Community 225 - "Community 225"
Cohesión: 0.40
Nodos (4): Architectural invariants, Output format, Responsibilities, Scope discipline

## Lagunas de conocimiento
- **433 nodo(s) aislado(s):** `Grading output with `analyze:` (pattern sets)`, `Service health conditions (version / state / config)`, `Egress interface (`interface`)`, `Check interdependencies (`requires` / `skip_when_changed`)`, `Ports` (+428 más)
  Estos tienen ≤1 conexión - posibles aristas faltantes o componentes sin documentar.
- **18 comunidades ligeras (<3 nodos) omitidas del informe** — ejecuta `graphify query` para explorar los nodos aislados.

## Preguntas sugeridas
_Preguntas que este grafo está en posición única para responder:_

- **¿Por qué `$()` conecta `Community 1` con `Community 33`, `Community 2`, `Community 3`, `Community 5`, `Community 6`, `Community 7`, `Community 8`, `Community 9`, `Community 10`, `Community 11`, `Community 13`, `Community 14`, `Community 25`?**
  _Alta centralidad de intermediación (0.159) - este nodo es un puente entre comunidades._
- **¿Por qué `Configuration` conecta `Community 206` con `Community 214`?**
  _Alta centralidad de intermediación (0.023) - este nodo es un puente entre comunidades._
- **¿Por qué `_()` conecta `Community 7` con `Community 1`?**
  _Alta centralidad de intermediación (0.017) - este nodo es un puente entre comunidades._
- **¿Qué conecta `Grading output with `analyze:` (pattern sets)`, `Service health conditions (version / state / config)`, `Egress interface (`interface`)` con el resto del sistema?**
  _Se encontraron 433 nodos débilmente conectados - posibles lagunas de documentación o aristas faltantes._
- **¿Debería dividirse `Community 0` en módulos más pequeños y enfocados?**
  _Puntuación de cohesión 0.12096774193548387 - los nodos de esta comunidad están débilmente interconectados._
- **¿Debería dividirse `Community 1` en módulos más pequeños y enfocados?**
  _Puntuación de cohesión 0.0311587147030185 - los nodos de esta comunidad están débilmente interconectados._
- **¿Debería dividirse `Community 2` en módulos más pequeños y enfocados?**
  _Puntuación de cohesión 0.09468599033816426 - los nodos de esta comunidad están débilmente interconectados._
