# Network Interface Watch (`net` check + per-metric hooks) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `net` watch check that monitors a network interface's state (up/down change or fixed state), link-speed change, and rx/tx error rate, with a **different hook per metric**, configured grouped-per-interface.

**Architecture:** A `net` watch entry names one interface and a `metrics` map (`state`/`speed`/`errors`), each metric with its own condition + `then.hook`. `BuildWatches` **expands** a net entry into one internal `*Watch` per metric (no scheduler/Watch-engine change). The `net` check is **stateful across cycles** (remembers the previous sample) to detect changes/deltas — safe because each watch ticks sequentially on its own goroutine. `hookEnv` is generalised to export every `Result.Data` key as `SERMO_<UPPER_KEY>`; the disk check gains a `value` key so its `SERMO_VALUE` survives the generalisation.

**Tech Stack:** Go 1.26, standard library (`net.Interfaces`, `/sys/class/net`), existing `internal/checks`, `internal/app`, `internal/config`.

**Spec:** `docs/superpowers/specs/2026-06-06-net-interface-watch-design.md`
**Builds on merged feature:** `docs/superpowers/specs/2026-06-06-host-watches-disk-design.md`

---

## File Structure

**Create:**
- `internal/checks/net.go` — `NetSample`, `NetSamplerFunc`, `netCheck` (pointer, stateful), default `/sys`+`net.Interfaces` sampler.
- `internal/checks/net_test.go` — net check tests with injected sampler.

**Modify:**
- `internal/checks/build.go` — add `NetSampler` to `Deps`; add `case "net"` to `buildCheck`.
- `internal/app/watch.go` — generalise `hookEnv` to a generic `SERMO_<UPPER_KEY>` mapping.
- `internal/app/watch_test.go` — replace the disk-specific `hookEnv` fallback test with a generic-mapping test (keep the firing test, which only checks SERMO_PATH/SERMO_CHECK_TYPE).
- `internal/checks/disk.go` — add a `value` key to disk `Result.Data` (used_pct if a used_pct predicate is set, else free_pct).
- `internal/checks/disk_test.go` — assert the `value` key.
- `internal/app/watch_build.go` — switch `BuildWatches` on `check.type`; add `buildNetWatches` expansion.
- `internal/app/watch_build_test.go` — net expansion tests.
- `internal/config/validate.go` — generalise `validateWatchHook` → `validateHookBlock(prefix, block, add)`; add `case "net"` with `validateNetCheck`.
- `internal/config/validate_watches_test.go` — net validation tests.
- `configs/sermo.yml`, `docs/configuration.md`, `README.md` — docs + example.

---

## Task 1: Generalise `hookEnv`

**Files:**
- Modify: `internal/app/watch.go`
- Modify: `internal/app/watch_test.go`

- [ ] **Step 1: Update the tests first**

In `internal/app/watch_test.go`, REPLACE `TestWatchHookEnvFallsBackToFreePct` with a generic-mapping test, and confirm `TestWatchFiresHookWhenConditionTrue` still asserts only `SERMO_WATCH`/`SERMO_PATH`/`SERMO_CHECK_TYPE` (leave it as-is). Add:

```go
func TestHookEnvMapsAllDataKeys(t *testing.T) {
	res := checks.Result{
		Check:   "net-eth0",
		Message: "eth0 state up->down",
		Data: map[string]any{
			"interface": "eth0",
			"metric":    "state",
			"old":       "up",
			"new":       "down",
			"value":     "down",
		},
	}
	env := hookEnv("net-eth0", "net", res)
	if env["SERMO_WATCH"] != "net-eth0" || env["SERMO_CHECK_TYPE"] != "net" || env["SERMO_MESSAGE"] != "eth0 state up->down" {
		t.Fatalf("base env wrong: %v", env)
	}
	for k, want := range map[string]string{
		"SERMO_INTERFACE": "eth0",
		"SERMO_METRIC":    "state",
		"SERMO_OLD":       "up",
		"SERMO_NEW":       "down",
		"SERMO_VALUE":     "down",
	} {
		if env[k] != want {
			t.Fatalf("env[%s] = %q, want %q (full: %v)", k, env[k], want, env)
		}
	}
}

func TestHookEnvDiskKeysStillWork(t *testing.T) {
	// Disk Data with a `value` key (set by the disk check) yields SERMO_PATH + SERMO_VALUE.
	res := checks.Result{Data: map[string]any{"path": "/", "value": 92.0, "used_pct": 92.0}}
	env := hookEnv("disk-root", "disk", res)
	if env["SERMO_PATH"] != "/" || env["SERMO_VALUE"] != "92" {
		t.Fatalf("disk env wrong: %v", env)
	}
}
```

(`92.0` stringifies to `92` via the `%v`→strconv path below; if your stringifier renders `92` for a float that's fine — see Step 3. If it renders `92.0`, adjust the assertion to `"92"` vs the actual; pick the stringifier in Step 3 so a whole float prints `92`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run 'TestHookEnv'`
Expected: FAIL (old fallback test gone / new tests reference behavior not present — SERMO_INTERFACE etc. not set).

- [ ] **Step 3: Generalise `hookEnv`**

In `internal/app/watch.go`, replace the whole `hookEnv` function with:

```go
// hookEnv builds the SERMO_* environment for a hook. Beyond the always-present
// SERMO_WATCH/CHECK_TYPE/MESSAGE, every Result.Data key is exported as
// SERMO_<UPPER_KEY> (non-alphanumerics become "_") so any check's metadata
// reaches the hook without per-type code.
func hookEnv(name, checkType string, res checks.Result) map[string]string {
	env := map[string]string{
		"SERMO_WATCH":      name,
		"SERMO_CHECK_TYPE": checkType,
		"SERMO_MESSAGE":    res.Message,
	}
	for k, v := range res.Data {
		env["SERMO_"+envKey(k)] = stringifyValue(v)
	}
	return env
}

// envKey uppercases a Data key and replaces any non-alphanumeric rune with "_".
func envKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// stringifyValue renders a Data value; whole floats print without a trailing .0.
func stringifyValue(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}
```

Update the imports of `internal/app/watch.go`: add `"strconv"` and `"strings"` (keep `"fmt"`).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/app/ -run 'TestHookEnv|TestWatchFires' -v`
Expected: PASS. Then `go test ./internal/app/` (note: disk's end-to-end SERMO_VALUE now depends on the disk check's `value` key, added in Task 2 — the app package tests here use stubs, so they pass independently).

- [ ] **Step 5: Commit**

```bash
git add internal/app/watch.go internal/app/watch_test.go
git commit -m "Generalise hookEnv to map all Data keys to SERMO_<KEY>"
```

---

## Task 2: Disk check emits a `value` key

**Files:**
- Modify: `internal/checks/disk.go`
- Modify: `internal/checks/disk_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/checks/disk_test.go`:

```go
func TestDiskCheckDataHasValueKey(t *testing.T) {
	// used_pct predicate -> value is used_pct.
	c := diskCheck{base: base{name: "d"}, path: "/", preds: []diskPred{{"used_pct", ">=", 90}}, usage: fakeDisk(92, 8, 80, 1000)}
	if v := c.Run(context.Background()).Data["value"]; v != 92.0 {
		t.Fatalf("value = %v, want 92.0 (used_pct)", v)
	}
	// only free_pct predicate -> value is free_pct.
	c2 := diskCheck{base: base{name: "d"}, path: "/", preds: []diskPred{{"free_pct", "<", 5}}, usage: fakeDisk(96, 4, 40, 1000)}
	if v := c2.Run(context.Background()).Data["value"]; v != 4.0 {
		t.Fatalf("value = %v, want 4.0 (free_pct)", v)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/checks/ -run TestDiskCheckDataHasValueKey`
Expected: FAIL (`value` key absent).

- [ ] **Step 3: Implement**

In `internal/checks/disk.go` `Run`, after building `res.Data`, add a `value` key keyed off predicate presence:

```go
	res.Data["value"] = st.UsedPct
	for _, p := range c.preds {
		if p.field == "free_pct" {
			res.Data["value"] = st.FreePct
		}
		if p.field == "used_pct" {
			res.Data["value"] = st.UsedPct
			break
		}
	}
```

(Precedence: used_pct wins when both are configured; otherwise free_pct if that's the configured predicate; default used_pct.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/checks/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/checks/disk.go internal/checks/disk_test.go
git commit -m "Add value key to disk check Data for generic hook env"
```

---

## Task 3: `net` check type

**Files:**
- Create: `internal/checks/net.go`
- Create: `internal/checks/net_test.go`
- Modify: `internal/checks/build.go`

- [ ] **Step 1: Write the failing test**

Create `internal/checks/net_test.go`:

```go
package checks

import (
	"context"
	"errors"
	"testing"
)

func sampler(samples ...NetSample) NetSamplerFunc {
	i := 0
	return func(string) (NetSample, error) {
		s := samples[i]
		if i < len(samples)-1 {
			i++
		}
		return s, nil
	}
}

func TestNetStateExpect(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", expect: "down",
		sampler: sampler(NetSample{State: "down"})}
	res := c.Run(context.Background())
	if !res.OK || res.Data["value"] != "down" || res.Data["interface"] != "eth0" {
		t.Fatalf("expect-down should fire: %+v", res)
	}
	c2 := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", expect: "down",
		sampler: sampler(NetSample{State: "up"})}
	if c2.Run(context.Background()).OK {
		t.Fatal("expect-down must not fire when up")
	}
}

func TestNetStateOnChange(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", onChange: true,
		sampler: sampler(NetSample{State: "up"}, NetSample{State: "down"})}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle must prime, not fire")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data["old"] != "up" || res.Data["new"] != "down" {
		t.Fatalf("state change should fire with old/new: %+v", res)
	}
	if c.Run(context.Background()).OK { // down -> down, no change
		t.Fatal("no change must not fire")
	}
}

func TestNetSpeedOnChange(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "speed", onChange: true,
		sampler: sampler(
			NetSample{SpeedMbps: 1000, SpeedKnown: true},
			NetSample{SpeedMbps: 100, SpeedKnown: true},
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes")
	}
	if !c.Run(context.Background()).OK {
		t.Fatal("speed change should fire")
	}
}

func TestNetSpeedUnknownDoesNotFire(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "speed", onChange: true,
		sampler: sampler(NetSample{SpeedKnown: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("unknown speed must not fire")
	}
}

func TestNetErrorsDelta(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "errors",
		counters: []string{"rx_errors", "tx_errors"}, op: ">", value: 100,
		sampler: sampler(
			NetSample{Counters: map[string]uint64{"rx_errors": 10, "tx_errors": 0}},
			NetSample{Counters: map[string]uint64{"rx_errors": 200, "tx_errors": 0}}, // +190
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes (no delta)")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data["value"] != uint64(190) {
		t.Fatalf("errors delta should fire with value 190: %+v", res)
	}
}

func TestNetErrorsCounterResetNoFire(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "errors",
		counters: []string{"rx_errors"}, op: ">", value: 0,
		sampler: sampler(
			NetSample{Counters: map[string]uint64{"rx_errors": 500}},
			NetSample{Counters: map[string]uint64{"rx_errors": 0}}, // reset -> delta 0
		)}
	c.Run(context.Background())
	if c.Run(context.Background()).OK {
		t.Fatal("counter reset must yield delta 0 (no fire)")
	}
}

func TestNetSamplerError(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", expect: "up",
		sampler: func(string) (NetSample, error) { return NetSample{}, errors.New("boom") }}
	if c.Run(context.Background()).OK {
		t.Fatal("sampler error must not fire")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/checks/ -run TestNet`
Expected: FAIL — `netCheck`/`NetSample`/`NetSamplerFunc` undefined.

- [ ] **Step 3: Implement `internal/checks/net.go`**

```go
package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// NetSample is one observation of a network interface.
type NetSample struct {
	State      string            // "up" | "down"
	SpeedMbps  int64
	SpeedKnown bool
	Counters   map[string]uint64 // statistics counters by name
}

// NetSamplerFunc observes an interface. Injected for tests; the default reads
// net.Interfaces() flags and /sys/class/net/<iface>.
type NetSamplerFunc func(iface string) (NetSample, error)

// netCheck watches one metric (state|speed|errors) of one interface. It is
// stateful across cycles (remembers the previous sample) and therefore a pointer
// type; this is safe because a watch ticks sequentially on its own goroutine.
// OK==true means "fire".
type netCheck struct {
	base
	iface   string
	metric  string
	expect  string // state: "up"|"down"; "" means on-change
	onChange bool  // state/speed change detection
	counters []string
	op       string
	value    float64
	sampler  NetSamplerFunc

	primed         bool
	lastState      string
	lastSpeed      int64
	lastErrTotal   uint64
}

func (c *netCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultNetSampler
	}
	s, err := sampler(c.iface)
	if err != nil {
		return c.result(false, fmt.Sprintf("net %s: %v", c.iface, err), start)
	}
	data := map[string]any{"interface": c.iface, "metric": c.metric}

	switch c.metric {
	case "state":
		if c.expect != "" {
			data["value"] = s.State
			res := c.result(s.State == c.expect, fmt.Sprintf("%s state %s (want %s)", c.iface, s.State, c.expect), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastState = true, s.State
			res := c.result(false, fmt.Sprintf("%s state baseline %s", c.iface, s.State), start)
			res.Data = data
			return res
		}
		changed := s.State != c.lastState
		data["old"], data["new"], data["value"] = c.lastState, s.State, s.State
		msg := fmt.Sprintf("%s state %s->%s", c.iface, c.lastState, s.State)
		c.lastState = s.State
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case "speed":
		if !s.SpeedKnown {
			res := c.result(false, fmt.Sprintf("%s speed unknown", c.iface), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastSpeed = true, s.SpeedMbps
			res := c.result(false, fmt.Sprintf("%s speed baseline %d", c.iface, s.SpeedMbps), start)
			res.Data = data
			return res
		}
		changed := s.SpeedMbps != c.lastSpeed
		data["old"], data["new"], data["value"] = c.lastSpeed, s.SpeedMbps, s.SpeedMbps
		msg := fmt.Sprintf("%s speed %d->%d", c.iface, c.lastSpeed, s.SpeedMbps)
		c.lastSpeed = s.SpeedMbps
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case "errors":
		var total uint64
		for _, name := range c.counters {
			total += s.Counters[name]
		}
		if !c.primed {
			c.primed, c.lastErrTotal = true, total
			res := c.result(false, fmt.Sprintf("%s errors baseline %d", c.iface, total), start)
			res.Data = data
			return res
		}
		var delta uint64
		if total > c.lastErrTotal {
			delta = total - c.lastErrTotal
		}
		c.lastErrTotal = total
		data["value"], data["total"] = delta, total
		met := compareFloat(float64(delta), c.op, c.value)
		res := c.result(met, fmt.Sprintf("%s errors +%d (total %d)", c.iface, delta, total), start)
		res.Data = data
		return res

	default:
		res := c.result(false, "unknown net metric "+c.metric, start)
		res.Data = data
		return res
	}
}

// defaultNetSampler reads interface flags and /sys/class/net/<iface>.
func defaultNetSampler(iface string) (NetSample, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return NetSample{}, err
	}
	state := "down"
	if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagRunning != 0 {
		state = "up"
	}
	sample := NetSample{State: state, Counters: map[string]uint64{}}

	if raw, err := os.ReadFile("/sys/class/net/" + iface + "/speed"); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && v >= 0 {
			sample.SpeedMbps, sample.SpeedKnown = v, true
		}
	}

	statDir := "/sys/class/net/" + iface + "/statistics"
	if entries, err := os.ReadDir(statDir); err == nil {
		for _, e := range entries {
			if raw, err := os.ReadFile(statDir + "/" + e.Name()); err == nil {
				if v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err == nil {
					sample.Counters[e.Name()] = v
				}
			}
		}
	}
	return sample, nil
}
```

- [ ] **Step 4: Wire into the builder**

In `internal/checks/build.go`, add to `Deps` (after `DiskUsage`):

```go
	// NetSampler observes a network interface for `net` checks. Nil uses /sys.
	NetSampler NetSamplerFunc
```

Add `case "net"` to `buildCheck` (before `case "":`):

```go
	case "net":
		iface := asString(entry["interface"])
		if iface == "" {
			return nil, "net check requires an interface"
		}
		metric := asString(entry["metric"])
		c := &netCheck{base: b, iface: iface, metric: metric, sampler: deps.NetSampler}
		switch metric {
		case "state":
			if exp := asString(entry["expect"]); exp != "" {
				if exp != "up" && exp != "down" {
					return nil, "net state expect must be up or down"
				}
				c.expect = exp
			} else if asString(entry["on"]) == "change" {
				c.onChange = true
			} else {
				return nil, "net state requires expect: up|down or on: change"
			}
		case "speed":
			if asString(entry["on"]) != "change" {
				return nil, "net speed requires on: change"
			}
			c.onChange = true
		case "errors":
			c.counters = stringArray(entry["counters"])
			if len(c.counters) == 0 {
				c.counters = []string{"rx_errors", "tx_errors"}
			}
			delta, ok := entry["delta"].(map[string]any)
			if !ok {
				return nil, "net errors requires a delta {op, value}"
			}
			op := asString(delta["op"])
			if !validDiskOp(op) {
				return nil, "net errors delta has an invalid op"
			}
			v, err := strconv.ParseFloat(scalarString(delta["value"]), 64)
			if err != nil {
				return nil, "net errors delta value must be numeric"
			}
			c.op, c.value = op, v
		default:
			return nil, "net check metric must be state, speed or errors"
		}
		return c, ""
```

Confirm `internal/checks/build.go` already imports `"strconv"` (it does, used by `intField`). `stringArray` and `validDiskOp` already exist in the package.

- [ ] **Step 5: Run to verify pass + commit**

Run: `go test ./internal/checks/ -v`
Expected: PASS (all net + disk tests).

```bash
git add internal/checks/net.go internal/checks/net_test.go internal/checks/build.go
git commit -m "Add net check type (state/speed/errors, stateful across cycles)"
```

---

## Task 4: `BuildWatches` net expansion

**Files:**
- Modify: `internal/app/watch_build.go`
- Modify: `internal/app/watch_build_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/app/watch_build_test.go`:

```go
func TestBuildWatchesExpandsNet(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-eth0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{
				"state": map[string]any{
					"on":   "change",
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}},
				},
				"errors": map[string]any{
					"delta": map[string]any{"op": ">", "value": 100},
					"then":  map[string]any{"hook": map[string]any{"command": []any{"/bin/err.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 2 {
		t.Fatalf("expected 2 expanded watches, got %d", len(watches))
	}
	cmds := map[string]bool{}
	for _, w := range watches {
		if w.CheckType != "net" || w.Name != "net-eth0" || w.Interval != 30*time.Second {
			t.Fatalf("unexpected watch: %+v", w)
		}
		cmds[w.Hook.Command[0]] = true
	}
	if !cmds["/bin/state.sh"] || !cmds["/bin/err.sh"] {
		t.Fatalf("expected distinct per-metric hooks, got %v", cmds)
	}
}

func TestBuildWatchesNetWarnsOnBadMetric(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-eth0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{
				"state": map[string]any{ // missing on/expect -> check build error
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/x.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected 0 watches and a warning, got %d / %v", len(watches), warns)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestBuildWatchesExpandsNet`
Expected: FAIL (net entries currently build one watch via the disk path and error on the hook/check).

- [ ] **Step 3: Implement the type switch + expansion**

In `internal/app/watch_build.go`, inside the `BuildWatches` loop, REPLACE the block that currently does `checkEntry, ok := entry["check"]...` through the `watches = append(...)` with a dispatch on check type. Concretely, after computing `interval` is fine, but simplest: replace the per-entry body after `if isDisabled(entry) { continue }` with:

```go
		checkEntry, ok := entry["check"].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+": missing check")
			continue
		}

		interval := defaultInterval
		if d := durationField(entry["interval"]); d > 0 {
			interval = d
		}

		switch stringField(checkEntry["type"]) {
		case "net":
			expanded, warns := buildNetWatches(name, entry, checkEntry, deps, interval)
			watches = append(watches, expanded...)
			warnings = append(warnings, warns...)
		default:
			w, warn := buildSingleWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		}
```

Extract the previous single-watch logic into `buildSingleWatch` (disk and any future 1:1 type):

```go
func buildSingleWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	check, err := checks.BuildInline(name, checkEntry, checks.Deps{DefaultTimeout: deps.DefaultTimeout})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	hook, err := parseHook(entry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	return &Watch{
		Name:      name,
		CheckType: stringField(checkEntry["type"]),
		Check:     check,
		Window:    rules.Rule{For: parseForField(entry["for"]), Within: parseWithinField(entry["within"])},
		Hook:      hook,
		Runner:    OSHookRunner{},
		Interval:  interval,
		Now:       deps.Now,
		Emit:      deps.Emit,
	}, ""
}

// buildNetWatches expands one net interface entry into one Watch per metric,
// each with its own check, window and hook (spec 2026-06-06-net-interface-watch).
func buildNetWatches(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) ([]*Watch, []string) {
	iface := stringField(checkEntry["interface"])
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		return nil, []string{"watch " + name + ": net check requires a non-empty metrics map"}
	}
	var out []*Watch
	var warns []string
	for _, key := range sortedWatchNames(metrics) {
		mEntry, ok := metrics[key].(map[string]any)
		if !ok {
			warns = append(warns, "watch "+name+".metrics."+key+": not a mapping")
			continue
		}
		ce := map[string]any{"type": "net", "interface": iface, "metric": key}
		for _, k := range []string{"on", "expect", "counters", "delta"} {
			if v, ok := mEntry[k]; ok {
				ce[k] = v
			}
		}
		check, err := checks.BuildInline(name, ce, checks.Deps{DefaultTimeout: deps.DefaultTimeout})
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		hook, err := parseHook(mEntry)
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		out = append(out, &Watch{
			Name:      name,
			CheckType: "net",
			Check:     check,
			Window:    rules.Rule{For: parseForField(mEntry["for"]), Within: parseWithinField(mEntry["within"])},
			Hook:      hook,
			Runner:    OSHookRunner{},
			Interval:  interval,
			Now:       deps.Now,
			Emit:      deps.Emit,
		})
	}
	return out, warns
}
```

Note: `parseHook(mEntry)` works unchanged because it reads `["then"]["hook"]`, which is exactly the per-metric shape. Remove the now-duplicated inline single-watch code from `BuildWatches` (it lives in `buildSingleWatch`).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/app/ -v`
Expected: PASS (existing disk build tests via `buildSingleWatch`, plus new net expansion tests).

- [ ] **Step 5: Commit**

```bash
git add internal/app/watch_build.go internal/app/watch_build_test.go
git commit -m "Expand net watches into one Watch per metric with its own hook"
```

---

## Task 5: Config validation for `net`

**Files:**
- Modify: `internal/config/validate.go`
- Modify: `internal/config/validate_watches_test.go`

- [ ] **Step 1: Write the failing test**

Append net cases to `internal/config/validate_watches_test.go`:

```go
func TestValidateWatchesNetGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"net-eth0": map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"metrics": map[string]any{
					"state":  map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
					"errors": map[string]any{"delta": map[string]any{"op": ">", "value": 100}, "then": map[string]any{"hook": map[string]any{"command": []any{"/y"}}}},
				},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesNetBad(t *testing.T) {
	hook := map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}}
	merge := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range hook {
			out[k] = v
		}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	cases := map[string]map[string]any{
		"no interface": {"check": map[string]any{"type": "net"}, "metrics": map[string]any{"state": merge(map[string]any{"on": "change"})}},
		"no metrics":   {"check": map[string]any{"type": "net", "interface": "eth0"}},
		"unknown metric": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"bogus": merge(map[string]any{"on": "change"})}},
		"bad state": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"state": merge(map[string]any{})}}, // no on/expect
		"bad errors op": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"errors": merge(map[string]any{"delta": map[string]any{"op": "=>", "value": 1}})}},
		"empty hook cmd": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"state": map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{}}}}}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"net-eth0": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run TestValidateWatchesNet`
Expected: FAIL (net not validated; bad cases produce no issues).

- [ ] **Step 3: Implement**

In `internal/config/validate.go`:

1. Generalise the hook validator. Replace `validateWatchHook(name, entry, add)` with a block-based one and a thin disk caller. Rename to `validateHookBlock(prefix string, block map[string]any, add func(string, ...any))` and update the disk call site:

```go
func validateHookBlock(prefix string, block map[string]any, add func(string, ...any)) {
	then, ok := block["then"].(map[string]any)
	if !ok {
		add("%s.then is required", prefix)
		return
	}
	hook, ok := then["hook"].(map[string]any)
	if !ok {
		add("%s.then.hook is required", prefix)
		return
	}
	list, ok := hook["command"].([]any)
	if !ok || len(list) == 0 {
		add("%s.then.hook.command must be a non-empty array", prefix)
	}
	if v, present := hook["timeout"]; present && !isPositiveDuration(scalarString(v)) {
		add("%s.then.hook.timeout %q must be a valid positive duration", prefix, scalarString(v))
	}
}
```

2. In `validateWatches`, extend the type switch and only validate the top-level hook for non-net types:

```go
		switch scalarString(check["type"]) {
		case "disk":
			validateDiskCheck(name, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "net":
			validateNetCheck(name, check, entry, add)
		case "":
			add("watches.%s.check.type is required", name)
		default:
			add("watches.%s.check.type %q is not supported", name, scalarString(check["type"]))
		}
```

(Remove the old unconditional `validateWatchHook(name, entry, add)` call — disk now calls `validateHookBlock` in its case; net validates per-metric hooks itself.)

3. Add `validateNetCheck`:

```go
// validateNetCheck validates a net interface watch: an interface and a non-empty
// metrics map, each metric with a valid condition and its own hook
// (spec 2026-06-06-net-interface-watch §4).
func validateNetCheck(name string, check, entry map[string]any, add func(string, ...any)) {
	if scalarString(check["interface"]) == "" {
		add("watches.%s.check.interface is required for a net check", name)
	}
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for a net check", name)
		return
	}
	for _, key := range sortedKeys(metrics) {
		prefix := fmt.Sprintf("watches.%s.metrics.%s", name, key)
		m, ok := metrics[key].(map[string]any)
		if !ok {
			add("%s must be a mapping", prefix)
			continue
		}
		switch key {
		case "state":
			exp := scalarString(m["expect"])
			onChange := scalarString(m["on"]) == "change"
			if exp == "" && !onChange {
				add("%s requires expect: up|down or on: change", prefix)
			} else if exp != "" && exp != "up" && exp != "down" {
				add("%s.expect must be up or down", prefix)
			}
		case "speed":
			if scalarString(m["on"]) != "change" {
				add("%s requires on: change", prefix)
			}
		case "errors":
			delta, ok := m["delta"].(map[string]any)
			if !ok {
				add("%s.delta {op, value} is required", prefix)
			} else {
				if !isValidDiskOp(scalarString(delta["op"])) {
					add("%s.delta has an invalid op %q", prefix, scalarString(delta["op"]))
				}
				if !isNumeric(scalarString(delta["value"])) {
					add("%s.delta value %q must be numeric", prefix, scalarString(delta["value"]))
				}
			}
			if c, present := m["counters"]; present {
				if list, ok := c.([]any); !ok || len(list) == 0 {
					add("%s.counters must be a non-empty list", prefix)
				}
			}
		default:
			add("%s is not a supported net metric (state, speed, errors)", prefix)
		}
		validateHookBlock(prefix, m, add)
		validateMetricWindow(prefix, m, add)
	}
}

// validateMetricWindow validates a per-metric for/within window using the same
// rules as validateWatchWindow but with a metric-scoped prefix.
func validateMetricWindow(prefix string, m map[string]any, add func(string, ...any)) {
	if f, ok := m["for"].(map[string]any); ok {
		if c, _ := scalarInt(f["cycles"]); c <= 0 {
			add("%s.for.cycles must be a positive integer", prefix)
		}
	}
	if wn, ok := m["within"].(map[string]any); ok {
		if c, _ := scalarInt(wn["cycles"]); c <= 0 {
			add("%s.within.cycles must be a positive integer", prefix)
		}
		if raw, present := wn["min_matches"]; present {
			if mm, _ := scalarInt(raw); mm < 0 {
				add("%s.within.min_matches must be a non-negative integer", prefix)
			}
		}
	}
}
```

Confirm `"fmt"` is imported in `validate.go` (it is). `isValidDiskOp`, `isNumeric`, `scalarString`, `scalarInt`, `sortedKeys`, `isPositiveDuration` all already exist.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/config/ -run TestValidateWatches -v`
Then: `go test ./internal/config/` (no regressions — the disk validation tests still pass via the `disk` case calling `validateHookBlock`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_watches_test.go
git commit -m "Validate net interface watches with per-metric hooks"
```

---

## Task 6: Docs, example config, and final verification

**Files:**
- Modify: `configs/sermo.yml`
- Modify: `docs/configuration.md`
- Modify: `README.md`

- [ ] **Step 1: Add a disabled-by-default example to `configs/sermo.yml`**

Append after the existing `watches:` `disk-root` entry (same `watches:` map — add a sibling key; do NOT create a second `watches:` key):

```yaml
  net-eth0:
    enabled: false
    interval: 30s
    check: { type: net, interface: eth0 }
    metrics:
      state:
        on: change
        then:
          hook:
            command: [/usr/local/bin/sermo-net-state.sh, eth0]
      speed:
        on: change
        then:
          hook:
            command: [/usr/local/bin/sermo-net-speed.sh, eth0]
      errors:
        counters: [rx_errors, tx_errors]
        delta: { op: ">", value: 100 }
        then:
          hook:
            command: [/usr/local/bin/sermo-net-errors.sh, eth0]
```

- [ ] **Step 2: Extend the "Host watches" section in `docs/configuration.md`**

After the disk description, add a `net` subsection documenting: the grouped-per-interface shape, the three metrics and their conditions (`state` on/expect, `speed` on:change, `errors` counters+delta), that the hook is **per metric**, and the env vars a net hook receives (`SERMO_WATCH`, `SERMO_INTERFACE`, `SERMO_METRIC`, `SERMO_VALUE`, and `SERMO_OLD`/`SERMO_NEW` for change metrics). Also note the generalised rule: every check Data key is exported as `SERMO_<UPPER_KEY>`. Use a normal fenced YAML block (mirror the example above).

- [ ] **Step 3: Update `README.md`**

Extend the host-watches sentence to mention network interfaces (state/speed/errors) in addition to disk.

- [ ] **Step 4: Validate the example config**

Run: `go run ./cmd/sermoctl --config configs/sermo.yml config validate`
Expected: `OK`, exit 0. Then temporarily flip `net-eth0` `enabled: true`, re-run validate (must still be `OK`), then set back to `enabled: false`.

- [ ] **Step 5: Commit**

```bash
git add configs/sermo.yml docs/configuration.md README.md
git commit -m "Document net interface watches and add example config"
```

---

## Final verification

- [ ] `go build ./... && go vet ./... && go test ./...` — all green.
- [ ] `go run ./cmd/sermoctl --config configs/sermo.yml config validate` exits 0.
- [ ] Disk watches unaffected: existing disk check/build/validation tests still pass; disk hooks still get `SERMO_PATH` and `SERMO_VALUE`.
- [ ] Net expansion: one `net-eth0` config produces three watches with three distinct hooks.

## Notes / risks

- **Stateful check + scheduler:** the `*netCheck` keeps baseline across `Run` calls; this relies on the scheduler running each watch's cycles sequentially on one goroutine (verified in `scheduler.go` `runCycler`). Do not share a single `*netCheck` across watches — `buildNetWatches` builds a fresh one per metric via `BuildInline`.
- **`hookEnv` change is cross-cutting:** Task 1 changes env for ALL watches (disk too). Disk's `SERMO_VALUE` is preserved only once Task 2 lands (disk emits a `value` key). Keep Tasks 1 and 2 together when merging; the final verification asserts disk hooks still get `SERMO_VALUE`.
- **`buildSingleWatch` extraction:** Task 4 moves the existing disk single-watch code into `buildSingleWatch`. Ensure no behavior change for disk (same fields, same `parseHook(entry)`).
- **`stringArray`/`validDiskOp` reuse:** Task 3 reuses `stringArray` and `validDiskOp` from `internal/checks` (same package) — do not redefine them.
- **`net.FlagRunning`:** reflects carrier/operational state on Linux; combined with `FlagUp` per the spec's "up" definition. Default sampler is Linux-only, consistent with the project.
