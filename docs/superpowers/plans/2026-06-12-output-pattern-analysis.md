# Output Pattern Analysis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. **All code modifications must use a git worktree** (see AGENTS.md "AI / agent workspaces"). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Classify a `command` check's output into ok/warning/error by matching it against ordered, reusable, named pattern sets in `catalog/patterns/`, replacing the legacy `verifica_sistema.py` token scanning.

**Architecture:** A pure analyzer (`internal/checks/analyze.go`) holds compiled regex rules and grades stdout/stderr. The `command` check runs it after exit-code/`expect_*` and maps the worst severity to OK/Optional. A new catalog category `kind: patterns` is resolved by `expandAnalyze` (mirroring `expandApps`), which inlines a flat `analyze.rules` list so the checks package stays catalog-agnostic.

**Tech Stack:** Go, Go RE2 (`regexp`), existing Sermo catalog/loader/resolve/validate pipeline.

**Spec:** `docs/superpowers/specs/2026-06-12-output-pattern-analysis-design.md`

**Note on commits:** The project owner reviews before any commit (standing rule). The `Commit` steps below are real checkpoints, but the executor must **stage and pause for approval** rather than committing unattended unless the user has said to commit.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/checks/analyze.go` (new) | `Severity`, `analyzeRule`, `outputAnalyzer.Analyze`, `parseAnalyzer` (from a resolved `analyze.rules` list) |
| `internal/checks/analyze_test.go` (new) | analyzer unit tests |
| `internal/checks/check.go` (modify ~78) | allow a check to escalate its own result to optional (warning) |
| `internal/checks/build.go` (modify `buildCommandCheck`) | parse `analyze` → `*outputAnalyzer` on the command check |
| `internal/checks/types.go` (modify `commandCheck`) | run the analyzer after `expect_*`; map severity → OK/Optional |
| `internal/config/model.go` (modify) | `kindPatterns`, `CategoryPatterns`, `Config.Patterns`/`PatternNames`, kind/category maps |
| `internal/config/loader.go` (modify) | allocate/index/list the patterns registry; `categoryFromDir "patterns"` |
| `internal/config/validate.go` (modify) | `kindPatterns` in counts/kind-switch/dup-slice (load blockers) |
| `internal/config/resolve.go` (modify) | `catalogRegistry` case; `expandAnalyze` (both call sites) |
| `internal/config/validate_checks.go` (modify) | validate the `analyze` block (severity/stream/regex/ids/use-names) |
| `internal/cli/patterns.go` (new) + `internal/cli/cli.go` (modify) | `sermoctl patterns [all]` |
| `catalog/patterns/common.yml` (+ `smartd.yml`, `bacula.yml`, `named.yml`) (new) | seeded rule sets |
| `catalog/services/*.yml` (modify) | add `analyze: { use: [...] }` to config/version checks that benefit |
| `docs/configuration.md`, `docs/rules.md`, `docs/daemons.md` (modify) | document the `analyze` block + `patterns` category |

---

## Task 1: Harness — allow a check to escalate to warning

**Files:**
- Modify: `internal/checks/check.go:78`
- Test: `internal/checks/checks_test.go` (add one case)

- [ ] **Step 1: Write the failing test** — a check whose `Run` returns `OK:false, Optional:true` must keep `Optional=true` even when `Built.Optional=false`. There is **no** `fakeCheck` helper in this package today, so define a tiny one (it must implement both `Name()` and `Run(ctx) Result`, per `Check` at `check.go:34-37`):

```go
type fakeCheck struct{ res Result }

func (f fakeCheck) Name() string                  { return f.res.Check }
func (f fakeCheck) Run(context.Context) Result    { return f.res }

func TestRunRespectsCheckEscalatedOptional(t *testing.T) {
	built := []Built{{Check: fakeCheck{res: Result{Check: "c", OK: false, Optional: true}}, Optional: false}}
	got := Run(context.Background(), built, 0)
	if !got[0].Optional {
		t.Fatalf("a check that returns Optional:true must stay optional, got %+v", got[0])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/checks/ -run TestRunRespectsCheckEscalatedOptional -v`
Expected: FAIL (`Optional` is false — overwritten by `b.Optional`).

- [ ] **Step 3: Implement** — change `internal/checks/check.go:78`:

```go
		res := b.Check.Run(ctx)
		res.Optional = res.Optional || b.Optional
		results[i] = res
```

- [ ] **Step 4: Run to verify it passes + no regression**

Run: `go test ./internal/checks/ -run 'TestRunRespectsCheckEscalatedOptional|TestRunConcurrentPreservesOrderAndOptional' -v`
Expected: PASS (the existing optional test still passes: `false || true == true`).

- [ ] **Step 5: Commit** — `git add internal/checks/check.go internal/checks/checks_test.go && git commit -m "feat(checks): let a check escalate its own result to optional (warning)"`

---

## Task 2: The analyzer core (`analyze.go`)

**Files:**
- Create: `internal/checks/analyze.go`
- Test: `internal/checks/analyze_test.go`

- [ ] **Step 1: Write failing tests**

```go
package checks

import "testing"

func mustAnalyzer(t *testing.T, rules []any) *outputAnalyzer {
	t.Helper()
	a, warn := parseAnalyzer(map[string]any{"rules": rules})
	if warn != "" {
		t.Fatalf("parseAnalyzer: %s", warn)
	}
	return a
}

func rule(id, match, sev string, stream ...string) map[string]any {
	m := map[string]any{"id": id, "match": match, "severity": sev}
	if len(stream) > 0 {
		m["stream"] = stream[0]
	}
	return m
}

func TestAnalyzeMaxSeverityAndFirstMatch(t *testing.T) {
	a := mustAnalyzer(t, []any{
		rule("err", "(?i)BACK UP DATA NOW", "error"),
		rule("warn", "(?i)deprecated", "warning"),
	})
	sev, id, _ := a.Analyze("all fine\nfeature is deprecated\nBACK UP DATA NOW", "")
	if sev != SevError || id != "err" {
		t.Fatalf("sev=%v id=%q, want error/err", sev, id)
	}
}

func TestAnalyzeOkWhitelistsLine(t *testing.T) {
	// An ok rule earlier in the list suppresses a later warning on the same line.
	a := mustAnalyzer(t, []any{
		rule("benign", "(?i)deprecated option ignored", "ok"),
		rule("warn", "(?i)deprecated", "warning"),
	})
	if sev, _, _ := a.Analyze("deprecated option ignored", ""); sev != SevOK {
		t.Fatalf("ok rule must whitelist the line, got %v", sev)
	}
	// But a different deprecated line is still a warning.
	if sev, _, _ := a.Analyze("X is deprecated", ""); sev != SevWarning {
		t.Fatalf("a non-whitelisted line must warn, got %v", sev)
	}
}

func TestAnalyzeStreamScoping(t *testing.T) {
	a := mustAnalyzer(t, []any{rule("e", "boom", "error", "stderr")})
	if sev, _, _ := a.Analyze("boom", ""); sev != SevOK {
		t.Fatalf("stderr-scoped rule must ignore stdout, got %v", sev)
	}
	if sev, _, _ := a.Analyze("", "boom"); sev != SevError {
		t.Fatalf("stderr-scoped rule must match stderr, got %v", sev)
	}
}

func TestParseAnalyzerErrors(t *testing.T) {
	for _, tc := range []struct{ name string; rules []any; want string }{
		{"bad-regex", []any{rule("x", "(", "warning")}, "invalid"},
		{"bad-severity", []any{rule("x", "y", "fatal")}, "severity"},
		{"bad-stream", []any{rule("x", "y", "warning", "syslog")}, "stream"},
		{"dup-id", []any{rule("x", "a", "warning"), rule("x", "b", "error")}, "duplicate"},
		{"missing-id", []any{map[string]any{"match": "y", "severity": "warning"}}, "id"},
	} {
		if _, warn := parseAnalyzer(map[string]any{"rules": tc.rules}); warn == "" {
			t.Errorf("%s: expected a warning containing %q", tc.name, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/checks/ -run 'Analyze|ParseAnalyzer' -v`
Expected: FAIL (`parseAnalyzer`/`outputAnalyzer` undefined).

- [ ] **Step 3: Implement `internal/checks/analyze.go`**

```go
package checks

import (
	"fmt"
	"regexp"
	"strings"

	"sermo/internal/cfgval"
)

// Severity ranks a pattern match; higher is worse.
type Severity int

const (
	SevOK Severity = iota // benign / whitelist
	SevWarning
	SevError
)

func (s Severity) String() string {
	switch s {
	case SevError:
		return "error"
	case SevWarning:
		return "warning"
	default:
		return "ok"
	}
}

func parseSeverity(s string) (Severity, bool) {
	switch s {
	case "error":
		return SevError, true
	case "warning":
		return SevWarning, true
	case "ok":
		return SevOK, true
	default:
		return SevOK, false
	}
}

// analyzeRule is one compiled pattern rule.
type analyzeRule struct {
	id       string
	re       *regexp.Regexp
	severity Severity
	stream   string // "stdout" | "stderr" | "both"
}

// outputAnalyzer holds a check's resolved, compiled rule list.
type outputAnalyzer struct{ rules []analyzeRule }

// Active reports whether there is anything to analyze.
func (a *outputAnalyzer) Active() bool { return a != nil && len(a.rules) > 0 }

// Analyze classifies stdout/stderr. Per non-empty line, the first matching rule
// wins (an `ok` match whitelists that line); the check's severity is the max
// over all lines. It returns that severity and the id + line of the first rule
// that reached it (for the result message).
func (a *outputAnalyzer) Analyze(stdout, stderr string) (sev Severity, id, line string) {
	scan := func(text, stream string) {
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimRight(ln, "\r")
			if ln == "" {
				continue
			}
			for _, r := range a.rules {
				if r.stream != "both" && r.stream != stream {
					continue
				}
				if r.re.MatchString(ln) {
					if r.severity > sev {
						sev, id, line = r.severity, r.id, ln
					}
					break // first match wins for this line
				}
			}
		}
	}
	scan(stdout, "stdout")
	scan(stderr, "stderr")
	return sev, id, line
}

// parseAnalyzer reads a resolved `analyze` mapping (its `rules` list — `use`
// and `silence` are already consumed by expandAnalyze) into a compiled
// analyzer. It returns the analyzer (nil when absent) and a warning string
// ("" when valid) describing the first invalid rule.
func parseAnalyzer(v any) (*outputAnalyzer, string) {
	if v == nil {
		return nil, ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, "analyze must be a mapping"
	}
	raw, ok := m["rules"].([]any)
	if !ok || len(raw) == 0 {
		return nil, "" // inert: no rules
	}
	a := &outputAnalyzer{}
	seen := map[string]bool{}
	for i, item := range raw {
		rm, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("analyze rule %d must be a mapping", i)
		}
		id := cfgval.AsString(rm["id"])
		if id == "" {
			return nil, fmt.Sprintf("analyze rule %d is missing an id", i)
		}
		if seen[id] {
			return nil, fmt.Sprintf("analyze has a duplicate rule id %q", id)
		}
		seen[id] = true
		sev, ok := parseSeverity(cfgval.AsString(rm["severity"]))
		if !ok {
			return nil, fmt.Sprintf("analyze rule %q severity must be error, warning or ok", id)
		}
		stream := cfgval.AsString(rm["stream"])
		if stream == "" {
			stream = "both"
		}
		if stream != "both" && stream != "stdout" && stream != "stderr" {
			return nil, fmt.Sprintf("analyze rule %q stream must be stdout, stderr or both", id)
		}
		re, err := regexp.Compile(cfgval.AsString(rm["match"]))
		if err != nil {
			return nil, fmt.Sprintf("analyze rule %q has an invalid regex: %v", id, err)
		}
		a.rules = append(a.rules, analyzeRule{id: id, re: re, severity: sev, stream: stream})
	}
	return a, ""
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/checks/ -run 'Analyze|ParseAnalyzer' -v`
Expected: PASS.

- [ ] **Step 5: Commit** — `git add internal/checks/analyze.go internal/checks/analyze_test.go && git commit -m "feat(checks): add output pattern analyzer (regex rules -> severity)"`

---

## Task 3: Wire the analyzer into the `command` check

**Files:**
- Modify: `internal/checks/build.go` (`buildCommandCheck`)
- Modify: `internal/checks/types.go` (`commandCheck` struct + `Run`)
- Test: `internal/checks/conncheck_test.go` or the command-check test file (find the existing `commandCheck` tests first: `grep -rn "commandCheck\|buildCommandCheck" internal/checks/*_test.go`)

- [ ] **Step 1: Write failing tests** — a command exiting 0 whose stdout hits a warning rule → `OK:false, Optional:true`; an error rule → `OK:false, Optional:false`; no match → `OK:true`. The package's existing fake runner is `type fakeRunner struct{ result execx.Result }` (a single **positional** `execx.Result` field, `checks_test.go:322`) — import `sermo/internal/execx` in the test:

```go
func TestCommandCheckAnalyzeWarning(t *testing.T) {
	built, warns := Build(map[string]any{
		"cfgtest": map[string]any{
			"type":    "command",
			"command": []any{"true"},
			"analyze": map[string]any{"rules": []any{
				map[string]any{"id": "dep", "match": "(?i)deprecated", "severity": "warning"},
			}},
		},
	}, Deps{DefaultTimeout: time.Second, Runner: fakeRunner{execx.Result{Stdout: "X is deprecated\n"}}})
	if len(warns) != 0 {
		t.Fatalf("build warns=%v", warns)
	}
	res := built[0].Check.Run(context.Background())
	if res.OK || !res.Optional {
		t.Fatalf("warning pattern must give OK=false Optional=true, got %+v", res)
	}
	if res.Data["pattern_severity"] != "warning" || res.Data["pattern_id"] != "dep" {
		t.Fatalf("missing pattern data: %+v", res.Data)
	}
}
```
(Add an `error` variant — `fakeRunner{execx.Result{Stdout: "BACK UP DATA NOW\n"}}`, rule severity `error` — asserting `OK=false && !Optional`; and a clean variant asserting `OK=true`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/checks/ -run TestCommandCheckAnalyze -v`
Expected: FAIL (analyzer not wired; `analyze` ignored).

- [ ] **Step 3a: Implement — `buildCommandCheck` (`internal/checks/build.go`)** add after the `expect_stderr` parse, before constructing `commandCheck`:

```go
	analyzer, warn := parseAnalyzer(entry["analyze"])
	if warn != "" {
		return nil, "command check " + warn
	}
```
and pass it into the struct literal: `c := commandCheck{..., analyzer: analyzer}`.

- [ ] **Step 3b: Implement — `commandCheck` (`internal/checks/types.go`)** add the field:

```go
	analyzer *outputAnalyzer
```
and in `Run`, **after** the `expect_stdout`/`expect_stderr` checks pass and **before** the `onChange` block / final success return, insert:

```go
	if c.analyzer.Active() {
		sev, id, line := c.analyzer.Analyze(res.Stdout, res.Stderr)
		if sev != SevOK {
			r := c.result(false, fmt.Sprintf("exit %d; %s pattern %q: %s", res.ExitCode, sev, id, firstLine(line)), start)
			r.Optional = sev == SevWarning
			r.Data = map[string]any{"pattern_id": id, "pattern_severity": sev.String(), "pattern_line": line}
			return r
		}
	}
```

- [ ] **Step 4: Run to verify it passes + whole package**

Run: `go test ./internal/checks/ -run TestCommandCheckAnalyze -v && go test ./internal/checks/`
Expected: PASS, no regressions.

- [ ] **Step 5: Commit** — `git add internal/checks/build.go internal/checks/types.go internal/checks/*_test.go && git commit -m "feat(checks): run output analyzer in the command check, map severity to optional"`

---

## Task 4: Catalog category `kind: patterns` (load it)

**Files:**
- Modify: `internal/config/model.go`, `internal/config/loader.go`, `internal/config/validate.go`
- Test: `internal/config/loader_test.go` (or wherever catalog-load tests live; grep `CategoryLibrary` in `_test.go`)

- [ ] **Step 1: Write a failing test** — a `kind: patterns` doc under `catalog/patterns/` loads into `c.Patterns` and does not error as "unknown kind". Model it on the existing app/lib load test (grep `kind: app` / `categoryFromDir` in tests).

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/config/ -run Pattern -v` → FAIL (unknown kind / not indexed).

- [ ] **Step 3: Implement (mirror apps/libs at every site the spec lists):**
  - `model.go`: add `kindPatterns = "patterns"`; `CategoryPatterns = "patterns"`; cases in `kindForCategory` (→ `kindPatterns`) and `categoryFromDir` (`"patterns"` → `CategoryPatterns`); add `Patterns map[string]*Document` and `PatternNames []string` to `Config`.
  - `loader.go`: allocate `Patterns: map[string]*Document{}` in the `Config{}` literal; `index(c.Patterns, &c.PatternNames)` alongside the others; handle it in `add` and `DaemonsInCategory`.
  - `validate.go`: add `kindPatterns` to the `counts` map, the kind `switch`, and the dup-detection kinds slice.

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/config/ -run Pattern -v` and full `go test ./internal/config/`.

- [ ] **Step 5: Commit** — `git add internal/config/model.go internal/config/loader.go internal/config/validate.go internal/config/*_test.go && git commit -m "feat(config): add the patterns catalog category"`

---

## Task 5: Resolution — `expandAnalyze`

**Files:**
- Modify: `internal/config/resolve.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests** — a service whose check has `analyze: { use: [common], silence: [dep], rules: [{id: x,...}] }`, given a `common` patterns doc with rules `dep` + `note`, resolves to a check whose `analyze.rules` = `[note, x]` (dep removed, local appended), and `use`/`silence` are consumed. Add error cases: unknown set, `silence` id absent from inherited, duplicate id across use+local.

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/config/ -run Analyze -v` → FAIL.

- [ ] **Step 3: Implement**
  - `catalogRegistry` (resolve.go): add `case CategoryPatterns: return c.Patterns`.
  - Add `expandAnalyze(tree)` that walks `tree["checks"]` maps; for each check with an `analyze` map, builds the resolved `rules` list: for each name in `use`, resolve the patterns doc (`c.Patterns[name]`, error if missing), append its `rules` minus any id in `silence`; then append the check's local `rules`; error on a `silence` id never seen, or a duplicate id in the final list. Replace `analyze` with `{rules: [...]}` (drop `use`/`silence`). Mirror the error-collection style of `expandApps`.
  - Call `expandAnalyze` in **both** `Resolve` (~line 30, after `expandApps`/`expandRestartOnChange`) and `resolveDoc` (~line 257), after `expandTree`.

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/config/ -run Analyze -v` and full package.

- [ ] **Step 5: Commit** — `git add internal/config/resolve.go internal/config/config_test.go && git commit -m "feat(config): resolve analyze.use/silence into a flat rule list (expandAnalyze)"`

---

## Task 6: Static validation of the resolved `analyze.rules`

**Important (reviewer correction):** `validateSingleShotCheckFields` runs via
`validateResolved`, which operates on the tree **after** `cfg.Resolve` — i.e.
**after `expandAnalyze`**, so `use:`/`silence:` are already gone and only the flat
`rules` list remains. Therefore:
- Unknown-set / unknown-silence-id / cross-set duplicate-id detection belongs in
  **`expandAnalyze` (Task 5)** and surfaces through `Resolve`'s error list — **not**
  here. Do **not** thread `*Config`/`PatternNames` into the check validator.
- This task only statically validates the **resolved** `rules` shape so
  `config validate` catches a bad regex/severity/stream without building checks.

**Files:**
- Modify: `internal/config/validate_checks.go`
- Test: `internal/config/validate_checks_test.go` (or `conn_validate_test.go` style)

- [ ] **Step 1: Write failing tests** — a service check with `analyze: { rules: [...] }` containing an invalid `severity`, invalid `stream`, uncompilable `match` regex, or a duplicate local `id` each produce a `checks.<name>.analyze` issue; a valid block produces none. (Write `rules` directly — these tests exercise the post-resolution shape.)

- [ ] **Step 2: Run to verify it fails.**

- [ ] **Step 3: Implement** a `validateAnalyze(path string, entry map[string]any, add addFunc)` called from the command-check case in `validateSingleShotCheckFields`. It reads `entry["analyze"].(map[string]any)["rules"]` and checks each rule's `id` (present, unique within the list), `severity` (`error|warning|ok`), `stream` (`stdout|stderr|both` or empty), and `match` (compiles as RE2). No `*Config` parameter. If `analyze` is absent, it is a no-op.

- [ ] **Step 4: Run to verify it passes** + full `go test ./internal/config/`.

- [ ] **Step 5: Commit** — `git add internal/config/validate_checks.go internal/config/*_test.go && git commit -m "feat(config): statically validate resolved analyze.rules shape"`

---

## Task 7: CLI — `sermoctl patterns [all]`

**Files:**
- Create: `internal/cli/patterns.go`
- Modify: `internal/cli/cli.go` (dispatch ~line 198-201, usage string)
- Test: `internal/cli/*_test.go` (model on the apps/libs CLI test)

- [ ] **Step 1: Write a failing test** — `sermoctl patterns` lists pattern-set names + rule counts; `patterns all` includes empty/unused. (A bespoke lister — NOT `appinspect.List`.)

- [ ] **Step 2–4: Implement & verify** a small lister that iterates `cfg.PatternNames`, printing name + `len(rules)` + description; wire dispatch + usage. Run the CLI test.

- [ ] **Step 5: Commit** — `git add internal/cli/patterns.go internal/cli/cli.go internal/cli/*_test.go && git commit -m "feat(cli): add sermoctl patterns listing"`

---

## Task 8: Seed pattern sets + wire catalog services

**Files:**
- Create: `catalog/patterns/common.yml`, `catalog/patterns/smartd.yml`, `catalog/patterns/bacula.yml`, `catalog/patterns/named.yml`
- Modify: catalog services with a config/version check that benefits (e.g. `smartd`, `bacula-*`, `bareos-*`, `named`, and the broad `common` on config tests)

- [ ] **Step 1: Write `catalog/patterns/common.yml`** from the legacy tokens:

```yaml
kind: patterns
name: common
description: "Generic command-output signals (from verifica_sistema.py)."
rules:
  - { id: backup-now,  match: "BACK UP DATA NOW",          severity: error }
  - { id: too-many,    match: "(?i)too many connections",   severity: error }
  - { id: cannot,      match: "(?i)\\bcannot\\b",           severity: warning }
  - { id: failed,      match: "(?i)\\bfailed\\b",           severity: warning }
  - { id: deprecated,  match: "(?i)deprecated",             severity: warning }
  - { id: not-loaded,  match: "(?i)not loaded",             severity: warning }
  - { id: note,        match: "(?i)\\[note\\]",             severity: warning }
  - { id: warn,        match: "(?i)\\bwarn(ing)?\\b",       severity: warning }
  - { id: unknown,     match: "(?i)\\bunknown\\b",          severity: warning }
```

- [ ] **Step 2: Service-specific sets + `silence`** — e.g. `smartd.yml` and a `smartd` service whose check whitelists `failed smart` if that is expected noise; `bacula`/`named` similar. Keep each minimal and grounded in real output.

- [ ] **Step 3: Wire** `analyze: { use: [common] }` onto the config/version `command` checks where it adds value. Validate after each: `go run ./cmd/sermoctl --config <tmp catalog cfg> config validate`.

- [ ] **Step 4: Verify** the seeded sets load and a wired service validates (build a temp config like the earlier rounds; `config validate` → OK; `sermoctl patterns` lists them).

- [ ] **Step 5: Commit** — `git add catalog/patterns catalog/services && git commit -m "feat(catalog): seed common/service pattern sets and wire analyze into config checks"`

---

## Task 9: Docs + full verification

**Files:**
- Modify: `docs/configuration.md`, `docs/rules.md`, `docs/daemons.md`

- [ ] **Step 1: Document** the `analyze` block (use/silence/rules, severities, per-line first-match, exit→expect→analyze precedence) in `docs/configuration.md`/`docs/rules.md`, and the `patterns` category in `docs/daemons.md` (alongside apps/libs). Note the hooks v1 limitation.

- [ ] **Step 2: Full checklist** (per `CLAUDE.md`):

```sh
export PATH="$HOME/go/bin:$PATH"
gofmt -l ./internal ./cmd        # empty
go build ./... && go test ./...  # pass (note: the pre-existing internal/app TestWebBackendPropagates… failure is unrelated)
govulncheck ./...                # none
staticcheck ./...                # none
revive -config revive.toml ./... # none
golangci-lint run                # gosec: none
```

- [ ] **Step 3: Commit** — `git add docs && git commit -m "docs: document output pattern analysis and the patterns category"`

---

## Out of scope (v1)
- Hooks (two-state, outside the resolve pipeline) — documented follow-up.
- Pattern analysis of http/conn check text (they have their own `expect`).
- Scanning daemon log files (this analyzes check output only).
