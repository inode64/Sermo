# Timeouts de las sondas de versión — plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que un timeout puntual de una sonda de versión deje de producir alertas de
aplicación y errores en las reglas `restart-on-change-*-version`, y que las sondas
dejen de saturar el host en ráfagas.

**Architecture:** Cuatro palancas independientes: (1) catálogo — sustituir la sonda
lenta de `salt-minion`; (2) `appinspect` — medir la duración, marcar el timeout de
forma tipada y limitar la concurrencia global de sondas; (3) `internal/app` — tolerar
un timeout aislado conservando la última muestra buena y exigiendo dos ciclos
seguidos para disparar; (4) Web UI — servir Aplicaciones desde las muestras en vez
de volver a sondear todo el catálogo en cada carga.

**Tech Stack:** Go (stdlib), catálogo YAML, `make check`.

---

## Diagnóstico (flota, 2026-09-29)

Revisión de solo lectura por SSH de los 16 hosts (`sermod` activo en todos, versiones
`2cd323bb` y `90df03a5`).

**¿Se cuelgan las aplicaciones?** No en el sentido de quedarse colgadas: en ningún host
quedan hijos de `sermod`, procesos en estado D ni procesos huérfanos de sondas. `execx`
mata el grupo de procesos al vencer el timeout y funciona. Lo que ocurre es que **las
sondas superan su timeout de 10 s** y cada vez que pasa se genera ruido:

| Host | Timeouts de sonda (histórico del log) | Patrón |
|---|---|---|
| eros2 | ~140 (salt-minion 16, firehol 9, certbot 7, 16 sondas JVM, tomcat 6…) | 52 entre 00:00 y 00:09 |
| fw1 | 28 (salt-minion 17, certbot 9) | 22:0x y 19:0x |
| insca | 10 (grafana y 8 JVM en el mismo milisegundo) | ráfaga única |
| k2keu2, k2keu3, fr3 | 1–2 (pmie_farm, kafka, alloy, rabbitmq) | aislados |
| resto (10 hosts) | 0 | — |

Cada timeout produce:

- un evento `firing` de la app (con notificación en el flanco de subida, porque los app
  watches tienen `FireOnFail` y ninguna ventana `for`), seguido de `recovered` cinco
  minutos después (122 `recovered` en eros2);
- un `ERROR ... rule=restart-on-change-<app>-version ... timeout after 10s` en el
  servicio que depende de la app (`tomcat-9-guacamole`, `salt-minion`, `unifi`,
  `nebula-nebula0`, `go2rtc`, `snmpd`, `squid`…). Falla de forma segura (no reinicia),
  pero es ruido en ERROR.

**Causas medidas:**

1. **`salt-minion --version` tarda 5,3–6,1 s en reposo** en fw1, insca y eros2 (el 55–60 %
   de su presupuesto). No es CPU (`user 0,47 s`, `real 5,5 s`): `strace` muestra que forkea
   un hijo cuyo hilo hace un `clock_nanosleep` fijo de 5 s. Cualquier carga lo lleva a
   10 s. Alternativas medidas en fw1: `python3.13 -c "import salt.version"` → **173 ms**;
   `salt-call --version` → 1,4 s; `salt --version` → 0,3–0,7 s.
2. **Parones del host, no de la app.** En eros2 caen a la vez sondas nativas que en
   reposo tardan milisegundos (`go version` 44 ms, `mongod --version` 102 ms, `php -v`
   386 ms, `mariadb --version`). Coincide con `cron.daily` (logrotate a las 00:00:05)
   en una VM con disco rotacional, `/` al 94 % y 3 GiB de swap en uso. Sermo no puede
   evitar el parón, pero sí no convertirlo en alerta.
3. **Ráfagas autoinfligidas.** `internal/app/scheduler.go` escalona el primer ciclo de
   todos los watches en un único `engine.interval` (30 s) y después cada uno repite a
   su `artifact_interval` (5 min) desde que termina, así que ~80–90 sondas por host
   (eros2: 83 apps, 10 arranques de JVM) siguen cayendo en la misma ventana de ~30 s
   cada 5 minutos, sin límite de concurrencia. Además, cada carga de la página
   Aplicaciones de la Web UI vuelve a sondear **todo** el catálogo
   (`webbackend_catalog.go`, paralelismo 4) aunque los watches ya tengan muestra.
4. **JVM duplicadas.** En eros2 e insca, `java-openjdk-17.0.20_p8` y
   `java-openjdk-bin-17.0.20_p8` resuelven al mismo binario
   (`/usr/lib/jvm/openjdk-bin-17/bin/java`): `java-%i-%v` casa `openjdk-bin-17` como
   `${instance}-bin-${version}` y como `${instance}-${version}`. Doble arranque de cada
   JVM por ronda.

**Históricos ya no activos (sin acción):** `db5.3_*` en insca (última vez 2026-07-15) y
`rpc-mountd` `WaitDelay expired` en fr3 (2026-07-30, una vez).

Evidencia bruta: scratchpad de la sesión (`fleet/out*.txt`), no versionada.

## Mapa de ficheros

| Fichero | Cambio |
|---|---|
| `catalog/apps/salt-minion.yml` | sonda de versión rápida vía `import salt.version` |
| `internal/appinspect/appinspect.go` | `Report.ProbeDuration`, `Report.TimedOut`, aviso de sonda lenta |
| `internal/appinspect/limiter.go` (nuevo) | semáforo global de sondas |
| `internal/app/librarywatch.go` | muestras toleran un timeout aislado; ventana `for: 2` |
| `internal/app/webbackend_catalog.go` | Aplicaciones desde muestras |
| `internal/config/versions.go` (o donde se expanden instancias) | deduplicar instancias con el mismo binario |
| `docs/` (páginas de apps/catálogo y de eventos) | documentar ventana y tolerancia |

---

### Task 1: sonda rápida para `salt-minion` — HECHA (2026-09-29)

`catalog/apps/salt-minion.yml` usa ahora `["${binary}", "--disable-keepalive", "--version"]`.
El retraso lo causa el bucle keepalive de `salt/scripts.py`: `--version` se ejecuta
dentro del proceso hijo `MinionKeepAlive`, cuyo hilo `suicide_when_without_parent`
duerme 5 s antes de que el proceso pueda salir. Con el flag se parsean las opciones
en el propio proceso. Medido: fw1 449 ms, bk1 113 ms, sarin (python3.14) 273 ms,
eros2 689 ms, kvm9 846 ms; salida idéntica (`salt-minion 3007.14 (Chlorine)`). Se
descartó `python -c "import salt.version"`: todos los hosts usan python-exec y
sarin tiene Salt en python3.14, así que fijar el intérprete era frágil.

---

### Task 2: duración y timeout tipado en `appinspect.Report`

**Files:**
- Modify: `internal/appinspect/appinspect.go` (`Report`, `versionProbeResult`, `runVersionProbe`)
- Test: `internal/appinspect/appinspect_test.go`

- [ ] **Step 1: Test que falla** (reutiliza `slowRunner`, que ya bloquea hasta el ctx)

```go
func TestInspectMarksProbeTimeout(t *testing.T) {
	cfg := configWithResolved(t, preflightResolved("/bin/true", "50ms")) // helper existente o equivalente
	r := InspectOne(context.Background(), slowRunner{}, cfg, "x")
	if !r.TimedOut {
		t.Fatalf("a probe that exceeds its timeout must set TimedOut: %+v", r)
	}
	if r.ProbeDuration < 50*time.Millisecond {
		t.Fatalf("ProbeDuration = %v, want >= timeout", r.ProbeDuration)
	}
}
```

- [ ] **Step 2:** `go test ./internal/appinspect -run TestInspectMarksProbeTimeout` → FAIL (campos inexistentes).

- [ ] **Step 3: Implementar**

```go
// Report
	// ProbeDuration is how long the version command took (zero when no probe ran).
	ProbeDuration time.Duration `json:"probe_duration,omitempty"`
	// TimedOut marks a version probe that exceeded its own deadline: the app was
	// not observed, which is not evidence that it is broken.
	TimedOut bool `json:"timed_out,omitempty"`
```

En `runVersionProbe`, rellenar `duration: res.Duration` y
`timedOut: errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil` (el deadline
es el de la sonda, no el del ciclo) y copiarlos al `Report` en `inspectResolved`.
Si `res.Duration > cmd.timeout/2` con resultado OK, `slog.Warn("slow version probe",
"app", name, "duration", d, "timeout", cmd.timeout)` — detecta el siguiente
`salt-minion` antes de que expire.

- [ ] **Step 4:** el test pasa; `go test ./internal/appinspect/...` PASS.

- [ ] **Step 5:** actualizar el listado largo de `sermoctl apps` si muestra columnas
de sonda, y la doc de `--json`.

---

### Task 3: límite global de concurrencia de sondas

**Files:**
- Create: `internal/appinspect/limiter.go`
- Modify: `internal/appinspect/appinspect.go:504-519` (`runProbeCommand`)
- Test: `internal/appinspect/limiter_test.go`

Motivo: hoy app watches, library watches y la Web UI lanzan sondas sin tope común;
eros2 arranca ~10 JVM + Python en la misma ventana.

- [ ] **Step 1: Test que falla**

```go
func TestProbeLimiterBoundsConcurrency(t *testing.T) {
	l := newProbeLimiter(2)
	var cur, peak atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			release, err := l.acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			if n := cur.Add(1); n > peak.Load() {
				peak.Store(n)
			}
			time.Sleep(5 * time.Millisecond)
			cur.Add(-1)
		})
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency = %d, want <= 2", peak.Load())
	}
}

func TestProbeLimiterHonoursCancel(t *testing.T) {
	l := newProbeLimiter(1)
	release, _ := l.acquire(context.Background())
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.acquire(ctx); err == nil {
		t.Fatal("acquire on a cancelled context must fail")
	}
}
```

- [ ] **Step 2:** FAIL (no existe `newProbeLimiter`).

- [ ] **Step 3: Implementar**

```go
package appinspect

import (
	"context"
	"runtime"
)

// probeLimiter bounds how many catalog probes run at once across app watches,
// library watches and the Web UI. Each probe may start a JVM or a Python
// interpreter; unbounded, a host with ~80 installed apps launched them in one
// burst every artifact interval and turned a slow disk into probe timeouts.
type probeLimiter chan struct{}

func newProbeLimiter(n int) probeLimiter { return make(probeLimiter, max(n, 1)) }

func (l probeLimiter) acquire(ctx context.Context) (func(), error) {
	select {
	case l <- struct{}{}:
		return func() { <-l }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// probeSlots is shared by every inspection in the process.
var probeSlots = newProbeLimiter(max(runtime.NumCPU()/2, 2))
```

En `runProbeCommand`, **adquirir antes** de llamar a `execx.RunProbe*`: el deadline de
la sonda se crea dentro de `execx.bounded`, así que la espera en cola no consume su
presupuesto. Un error de `acquire` se devuelve como `ExitCodeRunFailure` con el
`ctx.Err()` del ciclo (cancelación), no como timeout de la sonda.

- [ ] **Step 4:** PASS; `go test -race ./internal/appinspect/...`.

- [ ] **Step 5:** revisar con `sermo-safety-review` que la cola no bloquee el apagado
(el `acquire` respeta `ctx`) y que `catalogInspectionParallelism` (Web UI) siga
teniendo sentido o se retire en la Task 5.

---

### Task 4: un timeout aislado no dispara alerta ni error de regla

**Files:**
- Modify: `internal/app/librarywatch.go` (`artifactAppSample`, `StoreAppVersion`, `buildCatalogArtifactWatches`)
- Modify: `internal/app/appwatch.go` (`storeAppSample`)
- Test: `internal/app/librarywatch_test.go`, `internal/app/appwatch_test.go`

Semántica (fail-safe: `restart-on-change` nunca reinicia por una muestra no observada):

- Primer timeout tras una muestra OK → la muestra conserva versión y estado OK
  anteriores; se guarda `consecutiveTimeouts = 1`. `changedAppVersion` compara contra
  la versión buena → "sin cambio", sin ERROR.
- Segundo timeout consecutivo → se guarda el estado de timeout (la regla vuelve a
  dar error visible, como hoy).
- Cualquier resultado no-timeout reinicia el contador.
- El app watch recibe `Window: rules.Rule{For: &rules.ForWindow{Cycles: 2}}`: sólo
  dispara (y notifica) si falla dos ciclos seguidos (~10 min). Aplica también a fallos
  que no son timeout; es aceptable y queda documentado.

- [ ] **Step 1: Tests que fallan**

```go
func TestStoreAppVersionKeepsLastGoodOnSingleTimeout(t *testing.T) {
	s := NewArtifactSamples()
	s.RegisterApp("salt-minion")
	s.StoreAppReport("salt-minion", appinspect.Report{Version: "salt-minion 3007.14", Status: appinspect.StatusOK})
	s.StoreAppReport("salt-minion", appinspect.Report{Status: "error: timeout after 10s", TimedOut: true})
	v, st, ok := s.AppVersion("salt-minion")
	if !ok || st != appinspect.StatusOK || v != "salt-minion 3007.14" {
		t.Fatalf("single timeout must keep last good sample, got %q %q %v", v, st, ok)
	}
	s.StoreAppReport("salt-minion", appinspect.Report{Status: "error: timeout after 10s", TimedOut: true})
	if _, st, _ := s.AppVersion("salt-minion"); st == appinspect.StatusOK {
		t.Fatal("second consecutive timeout must surface the timeout status")
	}
}

func TestStoreAppVersionTimeoutWithoutPriorSampleIsStored(t *testing.T) {
	s := NewArtifactSamples()
	s.RegisterApp("x")
	s.StoreAppReport("x", appinspect.Report{Status: "error: timeout after 10s", TimedOut: true})
	if _, st, ok := s.AppVersion("x"); !ok || st == appinspect.StatusOK {
		t.Fatal("a first-ever timeout has no good sample to keep and must be stored as-is")
	}
}
```

Y en `appwatch_test.go`, un `scriptedCheck` con `[fail, ok]` no debe emitir `firing`
ni notificar; con `[fail, fail]` sí, una sola vez (usa `TestAppWatchNotifiesOnceAndRecovers`
como plantilla, con `Window` de 2 ciclos).

- [ ] **Step 2:** FAIL.

- [ ] **Step 3: Implementar**

`StoreAppReport(name, report)` ya existe (Task 5) y `artifactAppSample` guarda el
`report` completo. Añadir `consecutiveTimeouts int` y adaptar el cuerpo:

```go
func (s *ArtifactSamples) StoreAppReport(name string, r appinspect.Report) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.appVersions[name]
	if r.TimedOut && prev.sampled && prev.report.Status == appinspect.StatusOK && prev.consecutiveTimeouts == 0 {
		// One missed observation under host load is not a version change nor a
		// broken app: keep the last good sample for rules, count the miss.
		prev.consecutiveTimeouts = 1
		s.appVersions[name] = prev
		return
	}
	next := artifactAppSample{report: r, sampled: true}
	if r.TimedOut {
		next.consecutiveTimeouts = prev.consecutiveTimeouts + 1
	}
	s.appVersions[name] = next
}
```

En `buildCatalogArtifactWatches`, tras `watch.FireOnFail = true`:

```go
		// A single failed sample is usually a probe that ran out of time on a
		// busy host; require it twice in a row before alerting.
		watch.Window = rules.Rule{For: &rules.ForWindow{Cycles: artifactFailureCycles}}
```

con `const artifactFailureCycles = 2`. Comprobar que el estado de ventana se persiste
bien (`watchstate.go:61` ya lo hace si `Window.For != nil`).

- [ ] **Step 4:** `go test -race ./internal/app/...` PASS.

- [ ] **Step 5: Docs** — en la página de aplicaciones/eventos de `docs/`, indicar que
una app dispara tras 2 ciclos fallidos seguidos y que un timeout aislado conserva la
versión anterior. `make markdown-check`.

---

### Task 5: la página Aplicaciones no vuelve a sondear — HECHA (2026-09-29)

**Files:**
- Modify: `internal/app/webbackend_catalog.go:33-75`
- Modify: `internal/app/librarywatch.go` (la muestra guarda el `appinspect.Report` completo)
- Test: `internal/app/webbackend_catalog_test.go` (o el test existente de `loadApplications`)

- [ ] **Step 1: Test que falla** — con un runner que cuenta invocaciones y muestras ya
guardadas para todas las apps, `loadApplications` debe devolver los items sin llamar
al runner (contador == 0). Apps sin muestra (aún `starting`) conservan el
comportamiento actual.

- [ ] **Step 2:** FAIL.

- [ ] **Step 3: Implementar** — `artifactAppSample` guarda el `Report` completo;
añadir `(*ArtifactSamples).AppReport(name) (appinspect.Report, bool)`;
`loadCatalogItems` usa la muestra cuando existe y sólo sondea las apps sin muestra.
Las librerías (sin watch con muestra de versión) siguen sondeando. Si la Task 3 deja
`catalogInspectionParallelism` sin función, retirarlo.

- [ ] **Step 4:** tests PASS; `make web` sólo si cambia `internal/web/src/`.

---

### Task 6: no duplicar instancias con el mismo binario (JVM) — HECHA (2026-09-29)

Implementada como `dedupeSameBinaryMatches` en `internal/config/versions.go`: para
apps y librerías, los matches descubiertos desde `binary:` con el mismo binario real
se reducen al primero (gana el patrón más específico, `java-openjdk-*`). Exentos: el
active-slot vacío y los servicios del catálogo (pools PHP-FPM, instancias Tomcat).
Los nombres `java-openjdk-bin-*` desaparecen en eros2 e insca.

**Files:**
- Investigar: `internal/config/versions.go` (placeholders `%i`/`%v`, línea ~40) y el
  descubrimiento de instancias de plantillas.
- Test: `internal/config/versions_test.go`

- [ ] **Step 1: Reproducir en test** un árbol temporal con
`/usr/lib/jvm/openjdk-bin-17 -> /opt/openjdk-bin-17.0.20_p8` y comprobar que hoy
salen `java-openjdk-17.0.20_p8` y `java-openjdk-bin-17.0.20_p8` con el mismo binario.

- [ ] **Step 2: Corregir** — al expandir una plantilla, si dos instancias resuelven al
mismo binario (`filepath.EvalSymlinks`), conservar la de menor ambigüedad (instancia
más corta, patrón más específico de la lista `binary:`) y descartar la otra. Decidir
el criterio con `sermo-config-schema`; es un cambio de nombres de app visibles, así que
revisar el historial de eventos/versiones por nombre y documentarlo.

- [ ] **Step 3:** tests PASS.

---

### Task 7: validación en flota

Skill `sermo-remote-testing`. Las Tasks 1–5 cambian comportamiento del daemon, así que
la validación es mutante: primero los 4 primeros hosts, luego el resto.

- [ ] `make check` en local.
- [ ] Desplegar a eros2, fw1, insca y k2keu3 (los que muestran el problema) según el
      modelo de instalación persistente que ya tienen, previa confirmación del usuario.
- [ ] Observar al menos 24 h incluyendo la medianoche de eros2 y las 22:00 de fw1:
      `grep -a 'timeout after' /var/log/sermod.log` sólo debe mostrar, como mucho,
      `slow version probe` (WARN) y timeouts **no** seguidos de `kind=firing`.
- [ ] Contar `kind=firing app=` y `rule=restart-on-change-.*-version` por host antes y
      después; esperado: 0 por timeouts aislados.
- [ ] `sermoctl apps --json`: `probe_duration` de salt-minion < 1 s; en eros2 el
      tiempo total de `sermoctl apps` (hoy ~39 s) debe bajar.

## Descartado

- **Subir los timeouts del catálogo.** Esconde el síntoma y alarga la ráfaga; con la
  Task 4 un timeout aislado ya no molesta.
- **Escalonar el primer ciclo de los app watches en su `artifact_interval`.** Retrasaría
  `readyz` hasta 5 minutos (el gate espera el primer ciclo de cada watch); el
  semáforo de la Task 3 resuelve la ráfaga sin ese coste.
