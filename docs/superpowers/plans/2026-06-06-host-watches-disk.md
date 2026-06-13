# Host Watches (disk check + hook action) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. **All code modifications must use a git worktree** (see AGENTS.md "AI / agent workspaces"). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add host-level resource monitoring to Sermo, delivering disk-partition space end to end: a new `watch` unit, a `disk` check type, and a `hook` action that runs a local command when a threshold is crossed.

**Architecture:** A `watch` is a host-level unit (independent of services) defined in a new top-level `watches:` section of `sermo.yml`. Each watch owns one resource check (first type: `disk`), an optional `for`/`within` window (reusing `internal/rules`), and a `hook` action. The daemon builds watches and the scheduler runs them on their own goroutines alongside service workers. The threshold lives inside the `disk` check: `check.OK == true` means "threshold crossed", mirroring the existing `metric` check. To avoid the heavily overloaded "monitor" name (pause/resume, `MonitorStore`), the unit is called **`watch`**. Because watches emit `app.Event` and run through the scheduler, the `Watch` type, hook runner, and watch builder live in `internal/app`; only the `disk` check lives in `internal/checks`.

**Tech Stack:** Go 1.26, standard library (`syscall.Statfs`, `os/exec`), existing `internal/checks`, `internal/rules`, `internal/config`, `internal/app` packages.

**Spec:** `docs/superpowers/specs/2026-06-06-host-watches-disk-design.md`

---

## File Structure

**Create:**
- `internal/checks/disk.go` — `diskCheck` type, `DiskStats`, default `statfs` reader, numeric comparison helper.
- `internal/checks/disk_test.go` — disk check tests with injected stat source.
- `internal/app/hook.go` — `HookSpec`, `HookRunner` interface, default `os/exec` implementation (argv + env + timeout).
- `internal/app/hook_test.go` — hook runner tests.
- `internal/app/watch.go` — `Watch` struct and `RunCycle`.
- `internal/app/watch_test.go` — watch cycle tests (fires hook, respects window).
- `internal/app/watch_build.go` — `BuildWatches(cfg, deps, defaultInterval)`.
- `internal/app/watch_build_test.go` — builder tests.

**Modify:**
- `internal/checks/build.go` — add `DiskUsage` to `Deps`; add `case "disk"` to `buildCheck`.
- `internal/app/event.go` — add `Watch` field; route `hook`/`hook-failed` kinds at Info; update `Kind` doc.
- `internal/app/scheduler.go` — add a `cycler` interface, generalize the per-item loop, run watches.
- `internal/app/scheduler_test.go` — add a watch-runs test.
- `internal/config/validate.go` — add `validateWatches` called from `Validate`.
- `internal/config/validate30_test.go` (or a new `validate_watches_test.go`) — watch validation tests.
- `cmd/sermod/main.go` — build watches, pass to scheduler, relax the "no services" fatal gate to "no services and no watches".
- `configs/sermo.yml` — commented, disabled-by-default `watches` example.
- `docs/configuration.md` — "Host watches" section.
- `README.md` — one line noting host watches.

---

## Task 1: `disk` check type

**Files:**
- Create: `internal/checks/disk.go`
- Create: `internal/checks/disk_test.go`
- Modify: `internal/checks/build.go`

- [ ] **Step 1: Write the failing test**

Create `internal/checks/disk_test.go`:

```go
package checks

import (
	"context"
	"testing"
)

func fakeDisk(usedPct, freePct float64, freeBytes, totalBytes uint64) func(string) (DiskStats, error) {
	return func(string) (DiskStats, error) {
		return DiskStats{UsedPct: usedPct, FreePct: freePct, FreeBytes: freeBytes, TotalBytes: totalBytes}, nil
	}
}

func TestDiskCheckUsedPctBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "disk", service: ""},
		path:  "/",
		preds: []diskPred{{field: "used_pct", op: ">=", value: 90}},
		usage: fakeDisk(92, 8, 100, 1000),
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected OK (threshold crossed), got %+v", res)
	}
	if res.Data["used_pct"] != 92.0 || res.Data["path"] != "/" {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestDiskCheckUsedPctNotBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "disk"},
		path:  "/",
		preds: []diskPred{{field: "used_pct", op: ">=", value: 90}},
		usage: fakeDisk(50, 50, 500, 1000),
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK below threshold")
	}
}

func TestDiskCheckMultiPredAnd(t *testing.T) {
	// used_pct >= 90 AND free_pct < 5 -> only both true fires.
	c := diskCheck{
		base:  base{name: "disk"},
		path:  "/",
		preds: []diskPred{{"used_pct", ">=", 90}, {"free_pct", "<", 5}},
		usage: fakeDisk(92, 8, 80, 1000), // used crossed, free not (8 !< 5)
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK when one predicate fails (AND)")
	}
}

func TestDiskCheckStatError(t *testing.T) {
	c := diskCheck{
		base:  base{name: "disk"},
		path:  "/nope",
		preds: []diskPred{{"used_pct", ">=", 90}},
		usage: func(string) (DiskStats, error) { return DiskStats{}, context.DeadlineExceeded },
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK on stat error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/checks/ -run TestDiskCheck`
Expected: FAIL — `diskCheck`, `DiskStats`, `diskPred` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/checks/disk.go`:

```go
package checks

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

// DiskStats is one filesystem's usage, computed from statfs.
type DiskStats struct {
	UsedPct    float64
	FreePct    float64
	FreeBytes  uint64
	TotalBytes uint64
}

// DiskUsageFunc reports usage for the filesystem containing path. Injected for
// tests; the default uses statfs.
type DiskUsageFunc func(path string) (DiskStats, error)

// diskPred is one threshold predicate on a computed disk field.
type diskPred struct {
	field string // used_pct | free_pct
	op    string // >= > <= < == !=
	value float64
}

// diskCheck passes (OK=true) when every predicate is satisfied, i.e. the
// threshold is crossed (section 12, mirrors metricCheck).
type diskCheck struct {
	base
	path  string
	preds []diskPred
	usage DiskUsageFunc
}

func (c diskCheck) Run(_ context.Context) Result {
	start := time.Now()
	usage := c.usage
	if usage == nil {
		usage = statfsUsage
	}
	st, err := usage(c.path)
	if err != nil {
		return c.result(false, fmt.Sprintf("statfs %s: %v", c.path, err), start)
	}
	values := map[string]float64{"used_pct": st.UsedPct, "free_pct": st.FreePct}
	ok := true
	for _, p := range c.preds {
		if !compareFloat(values[p.field], p.op, p.value) {
			ok = false
		}
	}
	res := c.result(ok, fmt.Sprintf("%s used %.1f%% free %.1f%%", c.path, st.UsedPct, st.FreePct), start)
	res.Data = map[string]any{
		"path":        c.path,
		"used_pct":    st.UsedPct,
		"free_pct":    st.FreePct,
		"free_bytes":  st.FreeBytes,
		"total_bytes": st.TotalBytes,
	}
	return res
}

func compareFloat(a float64, op string, b float64) bool {
	switch op {
	case ">=":
		return a >= b
	case ">":
		return a > b
	case "<=":
		return a <= b
	case "<":
		return a < b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

// statfsUsage is the default DiskUsageFunc backed by statfs(2).
func statfsUsage(path string) (DiskStats, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return DiskStats{}, err
	}
	bsize := uint64(s.Bsize)
	total := s.Blocks * bsize
	free := s.Bavail * bsize // space available to unprivileged users
	used := total - s.Bfree*bsize
	var usedPct, freePct float64
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
		freePct = float64(free) / float64(total) * 100
	}
	return DiskStats{UsedPct: usedPct, FreePct: freePct, FreeBytes: free, TotalBytes: total}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/checks/ -run TestDiskCheck -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/checks/disk.go internal/checks/disk_test.go
git commit -m "Add disk check type with percentage thresholds"
```

---

## Task 2: Wire `disk` into the check builder

**Files:**
- Modify: `internal/checks/build.go`
- Modify: `internal/checks/disk_test.go` (add build test)

- [ ] **Step 1: Write the failing test**

Append to `internal/checks/disk_test.go`:

```go
func TestBuildDiskCheck(t *testing.T) {
	section := map[string]any{
		"d": map[string]any{
			"type":     "disk",
			"path":     "/",
			"used_pct": map[string]any{"op": ">=", "value": 90},
		},
	}
	built, warns := Build(section, Deps{DiskUsage: fakeDisk(92, 8, 80, 1000)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 {
		t.Fatalf("expected 1 built check, got %d", len(built))
	}
	if !built[0].Check.Run(context.Background()).OK {
		t.Fatal("expected disk check to fire above threshold")
	}
}

func TestBuildDiskCheckRejectsMissing(t *testing.T) {
	_, warns := Build(map[string]any{"d": map[string]any{"type": "disk"}}, Deps{})
	if len(warns) == 0 {
		t.Fatal("expected a warning for disk check without path/predicate")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/checks/ -run TestBuildDiskCheck`
Expected: FAIL — `Deps.DiskUsage` undefined / `disk` unsupported type.

- [ ] **Step 3: Write minimal implementation**

In `internal/checks/build.go`, add the field to `Deps` (after `Processes`):

```go
	// DiskUsage reports filesystem usage for `disk` checks. Nil uses statfs.
	DiskUsage DiskUsageFunc
```

Add a `case "disk"` to `buildCheck` (before `case "":`):

```go
	case "disk":
		path := asString(entry["path"])
		if path == "" {
			return nil, "disk check requires a path"
		}
		preds, err := parseDiskPreds(entry)
		if err != nil {
			return nil, "disk check: " + err.Error()
		}
		return diskCheck{base: b, path: path, preds: preds, usage: deps.DiskUsage}, ""
```

Add `parseDiskPreds` to `internal/checks/disk.go`:

```go
// parseDiskPreds reads the used_pct/free_pct predicates from a disk entry. At
// least one is required; each is {op, value}.
func parseDiskPreds(entry map[string]any) ([]diskPred, error) {
	var preds []diskPred
	for _, field := range []string{"used_pct", "free_pct"} {
		raw, ok := entry[field]
		if !ok {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping {op, value}", field)
		}
		op := asString(m["op"])
		if !validDiskOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := strconv.ParseFloat(scalarString(m["value"]), 64)
		if err != nil {
			return nil, fmt.Errorf("%s value %q is not numeric", field, scalarString(m["value"]))
		}
		preds = append(preds, diskPred{field: field, op: op, value: val})
	}
	if len(preds) == 0 {
		return nil, fmt.Errorf("requires at least one of used_pct/free_pct")
	}
	return preds, nil
}

func validDiskOp(op string) bool {
	switch op {
	case ">=", ">", "<=", "<", "==", "!=":
		return true
	default:
		return false
	}
}
```

Add `"strconv"` to the imports of `internal/checks/disk.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/checks/ -v`
Expected: PASS (all check tests).

- [ ] **Step 5: Commit**

```bash
git add internal/checks/build.go internal/checks/disk.go internal/checks/disk_test.go
git commit -m "Wire disk check into the check builder with predicate parsing"
```

---

## Task 3: Hook runner

**Files:**
- Create: `internal/app/hook.go`
- Create: `internal/app/hook_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/hook_test.go`:

```go
package app

import (
	"context"
	"testing"
	"time"
)

func TestHookRunnerPassesArgvEnvTimeout(t *testing.T) {
	var gotArgv []string
	var gotEnv map[string]string
	var gotTimeout time.Duration
	runner := HookRunnerFunc(func(_ context.Context, argv []string, env map[string]string, timeout time.Duration) error {
		gotArgv, gotEnv, gotTimeout = argv, env, timeout
		return nil
	})

	spec := HookSpec{Command: []string{"/bin/echo", "hi"}, Timeout: 5 * time.Second}
	err := spec.Run(context.Background(), runner, map[string]string{"SERMO_WATCH": "disk-root"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotArgv) != 2 || gotArgv[0] != "/bin/echo" {
		t.Fatalf("argv = %v", gotArgv)
	}
	if gotEnv["SERMO_WATCH"] != "disk-root" {
		t.Fatalf("env = %v", gotEnv)
	}
	if gotTimeout != 5*time.Second {
		t.Fatalf("timeout = %v", gotTimeout)
	}
}

func TestHookRunnerRejectsEmptyCommand(t *testing.T) {
	spec := HookSpec{}
	err := spec.Run(context.Background(), HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { return nil }), nil)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestHookRunner`
Expected: FAIL — `HookSpec`, `HookRunnerFunc` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/app/hook.go`:

```go
package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// HookSpec is a watch's hook action: a local command (argv, never a shell) run
// with a timeout when the watch condition fires (section 16, extension).
type HookSpec struct {
	Command []string
	Timeout time.Duration
}

// HookRunner executes a hook command with environment and a timeout.
type HookRunner interface {
	RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error
}

// HookRunnerFunc adapts a function to HookRunner.
type HookRunnerFunc func(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error

func (f HookRunnerFunc) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error {
	return f(ctx, argv, env, timeout)
}

// Run validates the spec and dispatches it through the runner.
func (h HookSpec) Run(ctx context.Context, runner HookRunner, env map[string]string) error {
	if len(h.Command) == 0 {
		return errors.New("hook has no command")
	}
	return runner.RunHook(ctx, h.Command, env, h.Timeout)
}

// OSHookRunner runs hooks via os/exec: argv only (no shell), the daemon's
// environment plus the provided SERMO_* variables, bounded by timeout.
type OSHookRunner struct{}

func (OSHookRunner) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd.Run()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestHookRunner -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/hook.go internal/app/hook_test.go
git commit -m "Add watch hook runner (argv + env + timeout, no shell)"
```

---

## Task 4: Event support for hooks

**Files:**
- Modify: `internal/app/event.go`
- Create test in: `internal/app/event_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/app/event_test.go`:

```go
package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogEmitterLogsHookAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	emit := SlogEmitter(logger)

	emit(Event{Watch: "disk-root", Kind: "hook", Message: "fired"})

	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "watch=disk-root") {
		t.Fatalf("hook event not logged at info with watch attr: %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestSlogEmitterLogsHook`
Expected: FAIL — `Event.Watch` undefined (and/or logged at Debug, not Info).

- [ ] **Step 3: Write minimal implementation**

In `internal/app/event.go`:

1. Add the `Watch` field and extend the `Kind` doc:

```go
type Event struct {
	Service string
	Watch   string // set for host-watch events (instead of Service)
	Kind    string // cycle | action | suppressed | alert | error | hook | hook-failed
	Rule    string
	Action  string
	Status  string
	Message string
}
```

2. In `SlogEmitter`, add the watch attr and route the new kinds. After the
`attrs := []any{"service", e.Service, "kind", e.Kind}` line, insert:

```go
		if e.Watch != "" {
			attrs = append(attrs, "watch", e.Watch)
		}
```

And change the switch to:

```go
		switch e.Kind {
		case "error", "hook-failed":
			logger.Error("sermod", attrs...)
		case "action", "alert", "suppressed", "hook":
			logger.Info("sermod", attrs...)
		default:
			logger.Debug("sermod", attrs...)
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestSlogEmitterLogsHook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/event.go internal/app/event_test.go
git commit -m "Add Watch field and hook event routing to the emitter"
```

---

## Task 5: `Watch` unit and cycle

**Files:**
- Create: `internal/app/watch.go`
- Create: `internal/app/watch_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/watch_test.go`:

```go
package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/rules"
)

// stubCheck returns a fixed result for the watch cycle tests.
type stubCheck struct {
	name string
	ok   bool
	data map[string]any
}

func (c stubCheck) Name() string { return c.name }
func (c stubCheck) Run(context.Context) checks.Result {
	return checks.Result{Check: c.name, OK: c.ok, Data: c.data}
}

func TestWatchFiresHookWhenConditionTrue(t *testing.T) {
	var calls int
	var env map[string]string
	w := &Watch{
		Name:      "disk-root",
		CheckType: "disk",
		Check:     stubCheck{name: "disk", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Hook:      HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(_ context.Context, _ []string, e map[string]string, _ time.Duration) error {
			calls++
			env = e
			return nil
		}),
	}
	w.RunCycle(context.Background())
	if calls != 1 {
		t.Fatalf("expected hook to fire once, got %d", calls)
	}
	if env["SERMO_WATCH"] != "disk-root" || env["SERMO_PATH"] != "/" || env["SERMO_CHECK_TYPE"] != "disk" {
		t.Fatalf("unexpected env: %v", env)
	}
}

func TestWatchDoesNotFireWhenConditionFalse(t *testing.T) {
	var calls int
	w := &Watch{
		Name:   "disk-root",
		Check:  stubCheck{name: "disk", ok: false},
		Hook:   HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { calls++; return nil }),
	}
	w.RunCycle(context.Background())
	if calls != 0 {
		t.Fatalf("expected no hook, got %d", calls)
	}
}

func TestWatchForWindowRequiresConsecutive(t *testing.T) {
	var calls int
	w := &Watch{
		Name:   "disk-root",
		Check:  stubCheck{name: "disk", ok: true},
		Window: rules.Rule{For: &rules.ForWindow{Cycles: 3}},
		Hook:   HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { calls++; return nil }),
	}
	w.RunCycle(context.Background()) // 1
	w.RunCycle(context.Background()) // 2
	if calls != 0 {
		t.Fatalf("fired before 3 consecutive cycles: %d", calls)
	}
	w.RunCycle(context.Background()) // 3 -> fire
	if calls != 1 {
		t.Fatalf("expected fire on 3rd cycle, got %d", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestWatch`
Expected: FAIL — `Watch` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/app/watch.go`:

```go
package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/rules"
)

// Watch monitors one host resource: each cycle it runs its check, advances its
// window, and fires its hook when the condition (check.OK) holds for the window.
// It is independent of services and does not use the operation engine.
type Watch struct {
	Name      string
	CheckType string // e.g. "disk"; for SERMO_CHECK_TYPE (Result.Check is the watch name)
	Check     checks.Check
	Window    rules.Rule // carries only For/Within; used by rules.WindowState.Fires
	Hook      HookSpec
	Runner    HookRunner
	Interval  time.Duration
	Now       func() time.Time
	Emit      func(Event)

	state rules.WindowState
}

// RunCycle runs the check, advances the window, and fires the hook on a firing
// cycle. An evaluation/hook error is emitted, never fatal.
func (w *Watch) RunCycle(ctx context.Context) {
	res := w.Check.Run(ctx)
	if !w.state.Fires(w.Window, res.OK) {
		return
	}
	runner := w.Runner
	if runner == nil {
		runner = OSHookRunner{}
	}
	env := hookEnv(w.Name, w.CheckType, res)
	if err := w.Hook.Run(ctx, runner, env); err != nil {
		w.emit(Event{Watch: w.Name, Kind: "hook-failed", Message: err.Error()})
		return
	}
	w.emit(Event{Watch: w.Name, Kind: "hook", Message: res.Message})
}

func (w *Watch) emit(e Event) {
	if w.Emit != nil {
		w.Emit(e)
	}
}

// hookEnv builds the SERMO_* environment for a hook from the check result.
// checkType is the configured check type (e.g. "disk"); res.Check is the watch
// name (base.name), so it must not be used for SERMO_CHECK_TYPE.
func hookEnv(name, checkType string, res checks.Result) map[string]string {
	env := map[string]string{
		"SERMO_WATCH":      name,
		"SERMO_CHECK_TYPE": checkType,
		"SERMO_MESSAGE":    res.Message,
	}
	if p, ok := res.Data["path"].(string); ok {
		env["SERMO_PATH"] = p
	}
	if v, ok := res.Data["used_pct"]; ok {
		env["SERMO_VALUE"] = fmt.Sprintf("%v", v)
	}
	return env
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestWatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/watch.go internal/app/watch_test.go
git commit -m "Add Watch unit with check/window/hook cycle"
```

---

## Task 6: `BuildWatches` from config

**Files:**
- Create: `internal/app/watch_build.go`
- Create: `internal/app/watch_build_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/watch_build_test.go`:

```go
package app

import (
	"testing"
	"time"

	"sermo/internal/config"
)

func cfgWithWatches(raw map[string]any) *config.Config {
	return &config.Config{Global: config.Global{Raw: map[string]any{"watches": raw}}}
}

func TestBuildWatchesBuildsDisk(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"disk-root": map[string]any{
			"check": map[string]any{
				"type":     "disk",
				"path":     "/",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	w := watches[0]
	if w.Name != "disk-root" || w.Interval != 30*time.Second {
		t.Fatalf("unexpected watch: %+v", w)
	}
	if w.Window.For == nil || w.Window.For.Cycles != 3 {
		t.Fatalf("for window not parsed: %+v", w.Window)
	}
	if len(w.Hook.Command) != 1 {
		t.Fatalf("hook command not parsed: %+v", w.Hook)
	}
}

func TestBuildWatchesSkipsDisabled(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"off": map[string]any{"enabled": false, "check": map[string]any{"type": "disk", "path": "/"}},
	})
	watches, _ := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 {
		t.Fatalf("expected disabled watch skipped, got %d", len(watches))
	}
}

func TestBuildWatchesWarnsOnBadCheck(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"bad": map[string]any{
			"check": map[string]any{"type": "disk"}, // missing path/predicate
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected 0 watches and a warning, got %d / %v", len(watches), warns)
	}
}
```

NOTE: confirm the `config.Config` / `config.Document` field names (`Global`, `Raw`) compile; adjust the helper if the struct differs.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestBuildWatches`
Expected: FAIL — `BuildWatches` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/app/watch_build.go`:

```go
package app

import (
	"fmt"
	"sort"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

// BuildWatches resolves the global `watches` section into runnable Watches.
// Disabled or malformed entries are skipped with a warning (like BuildWorkers).
func BuildWatches(cfg *config.Config, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	raw, ok := cfg.Global.Raw["watches"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	var watches []*Watch
	var warnings []string
	for _, name := range sortedWatchNames(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+" is not a mapping")
			continue
		}
		if isDisabled(entry) {
			continue
		}

		checkEntry, ok := entry["check"].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+": missing check")
			continue
		}
		check, err := checks.BuildInline(name, checkEntry, checks.Deps{
			DefaultTimeout: deps.DefaultTimeout,
			DiskUsage:      nil, // statfs default
		})
		if err != nil {
			warnings = append(warnings, "watch "+name+": "+err.Error())
			continue
		}

		hook, err := parseHook(entry)
		if err != nil {
			warnings = append(warnings, "watch "+name+": "+err.Error())
			continue
		}

		interval := defaultInterval
		if d := durationField(entry["interval"]); d > 0 {
			interval = d
		}

		watches = append(watches, &Watch{
			Name:      name,
			CheckType: stringField(checkEntry["type"]),
			Check:     check,
			Window:    rules.Rule{For: parseForField(entry["for"]), Within: parseWithinField(entry["within"])},
			Hook:      hook,
			Runner:    OSHookRunner{},
			Interval:  interval,
			Now:       deps.Now,
			Emit:      deps.Emit,
		})
	}
	return watches, warnings
}

func parseHook(entry map[string]any) (HookSpec, error) {
	then, ok := entry["then"].(map[string]any)
	if !ok {
		return HookSpec{}, fmt.Errorf("missing then")
	}
	hook, ok := then["hook"].(map[string]any)
	if !ok {
		return HookSpec{}, fmt.Errorf("then has no hook")
	}
	cmd := stringArray(hook["command"])
	if len(cmd) == 0 {
		return HookSpec{}, fmt.Errorf("hook requires a non-empty command")
	}
	return HookSpec{Command: cmd, Timeout: durationField(hook["timeout"])}, nil
}

func parseForField(v any) *rules.ForWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &rules.ForWindow{Cycles: intField(m["cycles"]), Mode: stringField(m["mode"])}
}

func parseWithinField(v any) *rules.WithinWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &rules.WithinWindow{Cycles: intField(m["cycles"]), MinMatches: intField(m["min_matches"])}
}

func sortedWatchNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
```

NOTE on helpers: this task introduces `stringArray`, `durationField`, `intField`, `stringField`. Check whether equivalents already exist in `internal/app` (they may not — `internal/checks` has its own). If absent, add small local helpers in `watch_build.go`:

```go
func stringArray(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func durationField(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func intField(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

func stringField(v any) string {
	s, _ := v.(string)
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestBuildWatches -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/watch_build.go internal/app/watch_build_test.go
git commit -m "Build watches from the global watches config section"
```

---

## Task 7: Scheduler runs watches

**Files:**
- Modify: `internal/app/scheduler.go`
- Modify: `internal/app/scheduler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/app/scheduler_test.go`:

```go
func TestSchedulerRunsWatches(t *testing.T) {
	var fired int32
	w := &Watch{
		Name:     "disk-root",
		Check:    stubCheck{name: "disk", ok: true},
		Interval: 15 * time.Millisecond,
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
			atomic.AddInt32(&fired, 1)
			return nil
		}),
		Hook: HookSpec{Command: []string{"/bin/true"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 15 * time.Millisecond}.Run(ctx, nil, []*Watch{w})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not return")
	}
	if atomic.LoadInt32(&fired) < 2 {
		t.Fatalf("watch did not cycle repeatedly: %d", fired)
	}
}
```

Also update the existing `Scheduler{...}.Run(ctx, workers)` calls in this file to the new signature `Run(ctx, workers, nil)`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestSchedulerRunsWatches`
Expected: FAIL — `Run` signature mismatch.

- [ ] **Step 3: Write minimal implementation**

In `internal/app/scheduler.go`:

1. Define a small interface near the top:

```go
// cycler is anything the scheduler ticks once per interval.
type cycler interface {
	RunCycle(ctx context.Context)
}
```

2. Change `Run` to accept watches and start them. New signature and body:

```go
func (s Scheduler) Run(ctx context.Context, workers []*Worker, watches []*Watch) {
	slots := s.OpSlots
	if slots <= 0 {
		slots = 2
	}
	sem := make(chan struct{}, slots)

	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	if s.StartupDelay > 0 {
		if !sleepCtx(ctx, s.StartupDelay) {
			return
		}
	}

	var wg sync.WaitGroup
	for i, w := range workers {
		gateOperate(w, sem)
		offset := time.Duration(int64(interval) * int64(i) / int64(len(workers)))
		wg.Add(1)
		go func(w *Worker, offset time.Duration) {
			defer wg.Done()
			runCycler(ctx, w, interval, offset)
		}(w, offset)
	}
	for _, wt := range watches {
		wi := wt.Interval
		if wi <= 0 {
			wi = interval
		}
		wg.Add(1)
		go func(wt *Watch, wi time.Duration) {
			defer wg.Done()
			runCycler(ctx, wt, wi, 0)
		}(wt, wi)
	}
	wg.Wait()
}
```

3. Rename `runWorker` to `runCycler` and make it take the `cycler` interface:

```go
func runCycler(ctx context.Context, c cycler, interval, offset time.Duration) {
	if offset > 0 {
		if !sleepCtx(ctx, offset) {
			return
		}
	}
	for {
		if ctx.Err() != nil {
			return
		}
		c.RunCycle(ctx)
		if !sleepCtx(ctx, interval) {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -v`
Expected: PASS (all app tests, including the updated existing scheduler tests).

- [ ] **Step 5: Commit**

```bash
git add internal/app/scheduler.go internal/app/scheduler_test.go
git commit -m "Run watches in the scheduler alongside service workers"
```

---

## Task 8: Config validation for watches

**Files:**
- Modify: `internal/config/validate.go`
- Create: `internal/config/validate_watches_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/validate_watches_test.go`. First confirm how existing
validation tests construct a `Config` and call `Validate` (read
`internal/config/validate30_test.go` for the exact helper/pattern) and mirror it.
Sketch:

```go
package config

import (
	"strings"
	"testing"
)

// validateRawGlobal builds a minimal-but-valid global config (Validate always
// requires defaults.policy.cooldown via validateGlobal) carrying the given raw
// sections, then returns all issues. Tests below filter to watch issues by
// substring since every issue is Scope "global".
func validateRawGlobal(t *testing.T, global map[string]any) []Issue {
	t.Helper()
	cfg := &Config{Global: Global{
		Raw:      global,
		Defaults: map[string]any{"policy": map[string]any{"cooldown": "5m"}},
	}}
	return Validate(cfg) // package function, not a method
}

// watchIssues returns only the issues whose message mentions "watches." so the
// always-present global checks (cooldown, etc.) don't mask watch validation.
func watchIssues(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if strings.Contains(i.Msg, "watches.") {
			out = append(out, i)
		}
	}
	return out
}

func TestValidateWatchesGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"disk-root": map[string]any{
				"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesBad(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown type": {"check": map[string]any{"type": "bogus"}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"disk no path": {"check": map[string]any{"type": "disk", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad op":       {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": "=>", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"empty cmd":    {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{}}}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"w": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}

func TestValidateWatchesMessageMentionsName(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{"disk-root": map[string]any{"check": map[string]any{"type": "disk"}}},
	})
	joined := ""
	for _, i := range watchIssues(issues) {
		joined += i.Msg
	}
	if !strings.Contains(joined, "disk-root") {
		t.Fatalf("issue should name the watch: %v", issues)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidateWatches`
Expected: FAIL — no watch validation yet (good config may pass, bad config produces no issues).

- [ ] **Step 3: Write minimal implementation**

In `internal/config/validate.go`, inside **`validateGlobal`** (NOT the top-level
`Validate`, which has no `raw`/`add` in scope) — after the `paths`/`security`
blocks, where `raw := cfg.Global.Raw` and the `add := func(...)` closure are in
scope (around validate.go:60-112) — add:

```go
	if watches, ok := raw["watches"].(map[string]any); ok {
		validateWatches(watches, add)
	}
```

Then add the function (place near the other section validators; match the
existing `add` closure signature — `add(format string, args ...any)`):

```go
// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a non-empty hook command (spec 2026-06-06-host-watches-disk).
func validateWatches(watches map[string]any, add func(string, ...any)) {
	for _, name := range sortedKeys(watches) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add("watches.%s must be a mapping", name)
			continue
		}
		if v, ok := entry["enabled"].(bool); ok && !v {
			continue
		}

		check, ok := entry["check"].(map[string]any)
		if !ok {
			add("watches.%s.check is required", name)
			continue
		}
		switch scalarString(check["type"]) {
		case "disk":
			validateDiskCheck(name, check, add)
		case "":
			add("watches.%s.check.type is required", name)
		default:
			add("watches.%s.check.type %q is not supported", name, scalarString(check["type"]))
		}

		validateWatchHook(name, entry, add)

		if v, present := entry["interval"]; present && !isPositiveDuration(scalarString(v)) {
			add("watches.%s.interval %q must be a valid positive duration", name, scalarString(v))
		}
	}
}

func validateDiskCheck(name string, check map[string]any, add func(string, ...any)) {
	if scalarString(check["path"]) == "" {
		add("watches.%s.check.path is required for a disk check", name)
	}
	preds := 0
	for _, field := range []string{"used_pct", "free_pct"} {
		raw, present := check[field]
		if !present {
			continue
		}
		preds++
		m, ok := raw.(map[string]any)
		if !ok {
			add("watches.%s.check.%s must be a mapping {op, value}", name, field)
			continue
		}
		if !isValidDiskOp(scalarString(m["op"])) {
			add("watches.%s.check.%s has an invalid op %q", name, field, scalarString(m["op"]))
		}
		if !isNumeric(scalarString(m["value"])) {
			add("watches.%s.check.%s value %q must be numeric", name, field, scalarString(m["value"]))
		}
	}
	if preds == 0 {
		add("watches.%s.check requires at least one of used_pct/free_pct", name)
	}
}

func validateWatchHook(name string, entry map[string]any, add func(string, ...any)) {
	then, ok := entry["then"].(map[string]any)
	if !ok {
		add("watches.%s.then is required", name)
		return
	}
	hook, ok := then["hook"].(map[string]any)
	if !ok {
		add("watches.%s.then.hook is required", name)
		return
	}
	list, ok := hook["command"].([]any)
	if !ok || len(list) == 0 {
		add("watches.%s.then.hook.command must be a non-empty array", name)
	}
	if v, present := hook["timeout"]; present && !isPositiveDuration(scalarString(v)) {
		add("watches.%s.then.hook.timeout %q must be a valid positive duration", name, scalarString(v))
	}
}

func isValidDiskOp(op string) bool {
	switch op {
	case ">=", ">", "<=", "<", "==", "!=":
		return true
	default:
		return false
	}
}

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}
```

Add `"strconv"` to the imports of `validate.go` if not already present, and
confirm `scalarString`, `sortedKeys`, `isPositiveDuration` exist in the package
(they do, per the current file).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestValidateWatches -v`
Expected: PASS. Then run `go test ./internal/config/` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_watches_test.go
git commit -m "Validate the watches config section"
```

---

## Task 9: Daemon wiring

**Files:**
- Modify: `cmd/sermod/main.go`

- [ ] **Step 1: Build watches and pass them to the scheduler**

In `cmd/sermod/main.go`, after `workers, warnings := app.BuildWorkers(cfg, deps)`
and its warning loop, add:

```go
	watches, watchWarnings := app.BuildWatches(cfg, deps, interval)
	for _, w := range watchWarnings {
		logger.Warn("build watches", "warning", w)
	}
```

Change the "nothing to do" gate from:

```go
	if len(workers) == 0 {
		logger.Error("no enabled services to monitor")
		return 2
	}
```

to:

```go
	if len(workers) == 0 && len(watches) == 0 {
		logger.Error("no enabled services or watches to monitor")
		return 2
	}
```

Update the start log and scheduler call:

```go
	logger.Info("sermod starting", "backend", detection.Backend, "services", len(workers), "watches", len(watches))
	scheduler := app.Scheduler{
		Interval:     interval,
		OpSlots:      engineInt(cfg, "max_parallel_operations", 2),
		StartupDelay: startupDelay,
	}
	scheduler.Run(ctx, workers, watches)
```

- [ ] **Step 2: Build and run the full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS, no vet errors.

- [ ] **Step 3: Commit**

```bash
git add cmd/sermod/main.go
git commit -m "Wire host watches into the sermod daemon"
```

---

## Task 10: Documentation and example config

**Files:**
- Modify: `configs/sermo.yml`
- Modify: `docs/configuration.md`
- Modify: `README.md`

- [ ] **Step 1: Add a disabled-by-default example to `configs/sermo.yml`**

Append at the end of `configs/sermo.yml`:

```yaml
# Host watches: monitor host resources (disk, …) independently of services and
# run a hook command when a threshold is crossed. Disabled by default.
watches:
  disk-root:
    enabled: false
    interval: 1m
    check:
      type: disk
      path: /
      used_pct: { op: ">=", value: 90 }
    for: { cycles: 3 }
    then:
      hook:
        command: [/usr/local/bin/sermo-alert-disk.sh, "/"]
        timeout: 10s
```

- [ ] **Step 2: Add a "Host watches" section to `docs/configuration.md`**

After the "Engine settings" section, add:

```markdown
## Host watches

`watches` monitor host-level resources independently of any service and run a
**hook** (a local command) when a threshold is crossed. They are daemon
configuration; they never merge into a service.

```yaml
watches:
  disk-root:
    enabled: true          # optional, default true
    interval: 1m           # optional, default engine.interval
    check:
      type: disk
      path: /
      used_pct: { op: ">=", value: 90 }   # check fires when crossed
    for: { cycles: 3 }     # optional window; reuses the rules engine
    then:
      hook:
        command: [/usr/local/bin/alert-disk.sh, "/"]
        timeout: 10s       # optional, default engine.default_timeout
```

The `disk` check reads filesystem usage for `path` and is true when every
present predicate (`used_pct` and/or `free_pct`, each `{op, value}` with
`op ∈ >=,>,<=,<,==,!=`) holds. When the condition holds for the `for`/`within`
window, the hook command runs (argv only, never a shell) with these environment
variables: `SERMO_WATCH`, `SERMO_CHECK_TYPE`, `SERMO_PATH`, `SERMO_VALUE`,
`SERMO_MESSAGE`.

Other resource types (network, file counts) will be added as new check `type`
values using the same watch/hook structure.
```
(Note: in the real file, use a normal fenced block — the nested fence here is for the plan only.)

- [ ] **Step 3: Add one line to `README.md`**

Under the `sermod` description in `README.md`, add a sentence noting that the
daemon also runs **host watches** (disk space and other host resources) that fire
a hook command when a threshold is crossed. Optionally add `/var` mention to the
layout section if appropriate.

- [ ] **Step 4: Validate the example config**

Run: `go run ./cmd/sermoctl --config configs/sermo.yml config validate`
Expected: exits 0 (the example watch is `enabled: false`; flip it to `true`
temporarily to confirm it also validates, then set back to `false`).

- [ ] **Step 5: Commit**

```bash
git add configs/sermo.yml docs/configuration.md README.md
git commit -m "Document host watches and add an example config"
```

---

## Final verification

- [ ] Run the full suite: `go build ./... && go vet ./... && go test ./...` — all green.
- [ ] `go run ./cmd/sermoctl --config configs/sermo.yml config validate` exits 0.
- [ ] Manual smoke (optional): set a `watches.disk-root` with `used_pct: {op: ">=", value: 0}` (always true), `command: [/bin/echo, fired]`, `enabled: true`, run `sermod run --config configs/sermo.yml` briefly and confirm a `kind=hook` log line.
- [ ] Confirm no service-path behavior changed (existing scheduler/worker tests still pass).

## Notes / risks

- **`config.Config` shape (confirmed):** `Config.Global` has type `config.Global` (NOT `config.Document`), and `config.Global` exposes `Raw map[string]any`. So `cfg.Global.Raw["watches"]` is correct, and test helpers build `config.Global{Raw: ...}`.
- **Validation entry point (confirmed):** validation is the package function `config.Validate(cfg *Config) []Issue` (NOT a method). It builds issues via the `add := func(format string, args ...any)` closure over a `raw` global map at `validate.go:60`. The new `validateWatches` call goes inside `Validate`.
- **Helper duplication:** `internal/app` may not already have `stringArray`/`intField`; Task 6 adds local copies. If they exist, reuse them instead (DRY).
- **`syscall.Statfs_t.Bsize` type** varies by platform; the cast `uint64(s.Bsize)` in Task 1 is correct for Linux (the only supported runtime per README).
