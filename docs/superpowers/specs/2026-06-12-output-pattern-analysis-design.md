# Output pattern analysis (`catalog/patterns/`) - historical design

Status: historical design. `catalog/patterns` and check `analyze:` blocks are
implemented; use [`docs/rules.md`](../../rules.md) and
[`docs/daemons.md`](../../daemons.md) for current operator-facing behavior.

## Goal

Classify the textual output of a check (or hook) into three states — **ok**
(green), **warning** (orange), **error** (red) — by matching it against ordered,
reusable, named pattern sets. Replaces the hard-coded token scanning of the
legacy `verifica_sistema.py` (`warnings()`/`errors()`/`warnings2()`), where a
service's version/config-test output was escalated to orange/red by substring
hits.

Patterns live in a new catalog category `catalog/patterns/` (reusable), and any
check/hook references them and can **add** local rules and **silence** inherited
ones by id.

## Severity model (no new state)

Sermo already has three effective states; patterns map onto them with no new
field:

| Pattern severity | Check Result | Color |
|---|---|---|
| `error`   | `OK=false`, required        | red    |
| `warning` | `OK=false`, `Optional=true` | orange |
| `ok` / none | `OK=true`                 | green  |

An `Optional` failing check is rendered as a **warning** (orange) by the
availability/SLA/health/web layers (`worker.go:211` `requiredChecksOK`,
`build.go:972` `Evaluate`, `webbackend.go:439`) — it does not count as a
service-down.

**Remediation caveat (corrected):** remediation rules (`internal/rules/eval.go`)
key on `res.OK` only — `Optional` is **not** consulted there. So a `warning`
(optional failure) *would* drive a `failed: <check>` rule if one referenced that
check, exactly like any optional check today. This design does **not** change
rule-eval semantics. Instead, the analyzed checks (version/config-test) are
simply **not wired to any remediation rule** (they never are in the current
catalog), so a warning there surfaces as orange in the notifier/web/SLA and
triggers no action — matching the legacy "orange = inform, don't restart". If an
operator deliberately wires a rule to an analyzed check, warnings flow through
that rule like any other optional failure; that is their choice, documented.

## Data model — `kind: patterns`

`categoryFromDir "patterns"` → `kind: patterns`, registered like apps/libs.

```yaml
kind: patterns
name: common
description: "Generic command-output signals shared by most services."
rules:
  - { id: backup-now, match: "BACK UP DATA NOW",          severity: error }
  - { id: too-many,   match: "(?i)too many connections",   severity: error }
  - { id: deprecated, match: "(?i)deprecated",             severity: warning }
  - { id: failed,     match: "(?i)\\bfailed\\b", stream: stderr, severity: warning }
```

Rule fields:
- `id` (required): stable identifier, unique within the resolved rule list; the
  handle a consumer uses in `silence:`.
- `match` (required): a **Go RE2 regular expression** evaluated against each
  output line (`(?i)` for case-insensitive, the legacy `lower().find` idiom).
- `severity` (required): `error` | `warning` | `ok`.
- `stream` (optional, default `both`): `stdout` | `stderr` | `both` — which
  captured stream the rule applies to.

`severity: ok` is a **whitelist**: a line it matches is benign and no later rule
can re-flag that line.

## Wiring — the `analyze:` block

**v1 surface: the `command` check only.** This fully covers the legacy
`verifica_sistema.py` use case (version + config-test output). Orthogonal to
`expect_exit` / `expect_stdout` (those stay; `analyze` adds severity-graded
classification).

**Hooks are a documented v1 limitation (not a free rider).** Although hooks also
capture stdout/stderr (`internal/app/hook.go`), they are **two-state** (`Run`
returns `error`; no `Optional`/warning lane) and live **outside** the
`internal/config` `Resolve` pipeline (they are parsed from
`Global.Raw["watches"]` in `internal/app/watch_build.go`), so `expandAnalyze`
does not reach them and there is no orange outcome to map a `warning` onto.
Extending `analyze` to hooks needs (a) a warning lane in the hook outcome model
and (b) a resolution path for the watches tree — deferred to a follow-up. This
limitation is recorded here and at the dispatch site per the both-surfaces rule
in `AGENTS.md`.

```yaml
checks:
  config:
    type: command
    command: ["/usr/bin/named-checkconf"]
    analyze:
      use: [common, named]      # inherit catalog pattern sets, in order
      silence: [deprecated]     # drop inherited rules by id
      rules:                    # service-local rules, evaluated FIRST (precedence)
        - { id: zone-ok, match: "(?i)loaded serial", severity: ok }
```

- `use` (optional): list of `catalog/patterns` set names, applied in order.
- `silence` (optional): ids removed from the inherited rules.
- `rules` (optional): service-local rules, evaluated **before** the inherited
  sets so the service has precedence — a local `ok` whitelist (or stricter rule)
  overrides an inherited rule on the same line (first-match-wins). This was
  confirmed necessary by real-fleet output (e.g. silencing the standalone
  `testparm` "lock/pid directory does not exist" artifact while keeping genuine
  deprecation warnings).

Resolved rule list = (`rules`) ++ (`use` sets concatenated in order, minus
`silence` ids) — local rules first for service precedence. An empty/absent
`analyze` is inert (current behavior unchanged).

## Evaluation semantics

Per **line**, **first matching rule wins** (firewall-style, predictable, order is
explicit). The check's severity = the **maximum** over all lines
(`error > warning > ok`). The `ok` whitelist is **per line only**: a line whose
first match is `ok` contributes `ok` and cannot be re-flagged, but a *different*
line can still be `warning`/`error` — `ok` is not a global suppressor. The
matched rule's `id` and the offending line are written to `Result.Message` and
`Result.Data` (`pattern_id`, `pattern_line`, `pattern_severity`) so the
notifier/web shows *why* it is orange/red — the legacy tooltip equivalent.

**Precedence:** exit-code → `expect_*` → `analyze`. The analyzer runs only after
the exit-code check and any `expect_stdout`/`expect_stderr` pass (the early
returns in `commandCheck.Run`, `types.go`). So `analyze` cannot reclassify a hard
exit-code or `expect_*` failure (those are already red); it only grades an
otherwise-passing command's output. This is intentional and documented.

The `command` check, after the exit-code check and any `expect_*`, runs the
analyzer over stdout/stderr:
- max severity `error` → `Result{OK:false}` (required).
- max severity `warning` → `Result{OK:false, Optional:true}`.
- otherwise unchanged.

Harness change (one line, `internal/checks/check.go:78`):
`res.Optional = res.Optional || b.Optional` — lets a check escalate its own
failure to a warning without losing the statically-configured optional flag.
(Today the line unconditionally overwrites with the static value.)

## Reuse

- Same RE2 engine, but **pre-compile one `*regexp.Regexp` per rule at build
  time** (in `buildCommandCheck`), not `compareValue` per line — the analyzer
  matches N rules × M lines every cycle and `compareValue` recompiles on each
  call (`compare.go:41`). The analyzer is a small new helper
  (`internal/checks/analyze.go`) holding the compiled rules; it does not touch
  the `OutputMatcher`/`expect_*` path.
- `use:` resolution mirrors `expandApps`/`expandRestartOnChange` in
  `internal/config/resolve.go` — a new `expandAnalyze` that resolves set names
  against `c.Patterns` and inlines the concrete rule list into the check entry.
  It must be added to **both** resolution call sites (`Resolve` ~line 30 and
  `resolveDoc` ~line 257) and run after `expandTree` (so `${var}` in `match`/`use`
  are already substituted); order relative to `expandApps` does not matter.

### Rule-id uniqueness

- Within a single `catalog/patterns` set: ids unique (validated at load).
- In the **resolved** list for a check (`use` sets concatenated − `silence` +
  local `rules`): ids must be unique. A collision between two `use` sets, or a
  local rule reusing an inherited id without silencing it first, is the error
  `expandAnalyze` reports. `silence` referencing an id absent from the inherited
  set is also an error (catch typos).

## Catalog plumbing (follows apps/libs — full touch-point list)

The reviewer confirmed apps/libs is the right template but the list is longer
than "model + loader + resolve". Every site below must gain a `Patterns`/
`kindPatterns`/`CategoryPatterns` parallel:

- `internal/config/model.go`: `kindPatterns = "patterns"`, `CategoryPatterns`,
  cases in `kindForCategory` and `categoryFromDir`; `Config.Patterns
  map[string]*Document` + `Config.PatternNames []string`.
- `internal/config/loader.go`: allocate `Patterns: map[string]*Document{}` in the
  `Config{}` initializer (~line 55); `index`/`add` it (~line 311-320); include it
  in `DaemonsInCategory` (~line 324-339).
- `internal/config/validate.go` (**hard blockers** — without these every
  `kind: patterns` doc is rejected as "unknown kind"): add `kindPatterns` to the
  `counts` map (~line 143), to the kind switch (~line 158-166), and to the
  dup-detection kinds slice (~line 177).
- `internal/config/versions.go`: patterns need no version templates — leave them
  out of `materializeVersionTemplates`/`removeFromRegistry` as a **deliberate,
  commented** skip.
- `internal/config/resolve.go`: `catalogRegistry` case returning `c.Patterns`;
  `expandAnalyze` (both call sites, see Reuse).
- `internal/config/validate_checks.go`: validate `analyze` shape — valid
  `severity` (`error|warning|ok`), `stream` (`stdout|stderr|both`), compilable
  RE2 `match`, unique local ids. Validating `analyze.use` against known set names
  requires threading the pattern-set name set (or `*Config`) into the check
  validator (`validateSingleShotCheckFields` currently sees only the raw `entry`
  map) — a small plumbing change to call out.
- CLI: `sermoctl patterns [all] [--long]`. **Cannot** reuse `appinspect.List`
  (that probes a binary + runs a version command, which patterns have none);
  needs a bespoke, simpler lister. Dispatch added at `internal/cli/cli.go`
  (~line 198-201).

## Migration of `verifica_sistema.py`

Seed pattern sets from the legacy logic:
- `common.yml`: `deprecated`, `warning`, `failed`, `cannot`, `not loaded`,
  `with newline`, `[note]`, `warn:`, `unknown` → `warning`; `BACK UP DATA NOW`,
  `too many connections` → `error`.
- Service-specific: `smartd` (silence/relabel `failed smart`), `bacula`/`bareos`
  (the `-t` config tests), `named` (zone load notices).
Catalog `config`/version checks that benefit get `analyze: { use: [common] }`;
service-specific quirks become per-service `silence`/`rules`.

## Testing

- Analyzer unit tests: per-line first-match, severity max, `ok` whitelist,
  `stream` filtering, multi-line, invalid regex.
- Resolution tests: `use` ordering, `silence` removal, local-rule append,
  unknown set / unknown silence id / duplicate id errors.
- Harness test: optional escalation (`OK=false`+`Optional=true` → warning, no
  remediation).
- Catalog: a seeded `common.yml` + a service using it validates.

## Docs (`AGENTS.md` parity — same change)

Update in the same change: `docs/configuration.md` and `docs/rules.md` (the
`analyze` block + the `patterns` category), `docs/daemons.md` (patterns category
alongside apps/libs), and the example `configs/sermo.yml` if a seeded set is
shown. Note the hooks limitation where the dispatch decision lives.

## Out of scope (v1)

- **Hooks** (`then:` actions / watch hooks): need a warning lane + a watches-tree
  resolution path; deferred to a follow-up (see Wiring).
- Applying patterns to non-command check output (http body, conn probe text) —
  those have their own `expect`.
- A distinct fourth severity or a separate web "warning" lane beyond the existing
  optional-failure rendering.
- Capturing/scanning arbitrary daemon logs (this analyzes check/hook output, not
  log files).
