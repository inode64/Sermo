# ICMP Host Watch (`icmp` check + per-metric hooks) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `icmp` watch check that monitors an external host's reachability (up/down change or fixed state) and round-trip latency (threshold or abrupt-change), with a **different hook per metric**, configured grouped-per-host.

**Architecture:** Mirrors the `net` watch: a grouped `icmp` entry names one host + a `metrics` map (`state`/`latency`), each with its own condition + `then.hook`. `BuildWatches` expands it into one `*Watch` per metric via a generalised `buildMetricWatches` (shared with `net`). The `icmp` check is **stateful across cycles** (pointer type, baseline for change detection) — safe because each watch ticks sequentially. The real ICMP probe (native, via `golang.org/x/net`) sits behind an **injectable sampler** so tests need no privileges/network.

**Tech Stack:** Go 1.26, NEW dependency `golang.org/x/net` (`icmp`+`ipv4`), existing `internal/checks`, `internal/app`, `internal/config`.

**Spec:** `docs/superpowers/specs/2026-06-06-icmp-host-watch-design.md`
**Builds on merged features:** disk + net interface watches.

---

## File Structure

**Create:**
- `internal/checks/icmp.go` — `PingSample`, `PingSamplerFunc`, `icmpCheck` (stateful pointer), default native ICMP sampler.
- `internal/checks/icmp_test.go` — icmp check tests with injected sampler.

**Modify:**
- `internal/checks/build.go` — add `PingSampler` to `Deps`; add `case "icmp"` to `buildCheck`.
- `internal/app/watch_build.go` — generalise `buildNetWatches` → `buildMetricWatches`; dispatch `net`+`icmp` to it.
- `internal/app/watch_build_test.go` — add icmp expansion test (net tests stay green).
- `internal/config/validate.go` — extract `validateStateMetric`; add `validateICMPCheck`; dispatch `case "icmp"`.
- `internal/config/validate_watches_test.go` — icmp validation tests.
- `go.mod`, `go.sum` — `golang.org/x/net`.
- `configs/sermo.yml`, `docs/configuration.md`, `README.md` — docs + example.

---

## Task 1: `icmp` check type (+ dependency)

**Files:**
- Create: `internal/checks/icmp.go`, `internal/checks/icmp_test.go`
- Modify: `internal/checks/build.go`, `go.mod`, `go.sum`

- [ ] **Step 1: Write the failing test**

Create `internal/checks/icmp_test.go`:

```go
package checks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func pinger(samples ...PingSample) PingSamplerFunc {
	i := 0
	return func(string, int, time.Duration) (PingSample, error) {
		s := samples[i]
		if i < len(samples)-1 {
			i++
		}
		return s, nil
	}
}

func TestICMPStateExpect(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", expect: "up",
		sampler: pinger(PingSample{Reachable: true})}
	res := c.Run(context.Background())
	if !res.OK || res.Data["value"] != "up" || res.Data["host"] != "h" {
		t.Fatalf("expect-up should fire when reachable: %+v", res)
	}
	c2 := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", expect: "up",
		sampler: pinger(PingSample{Reachable: false})}
	if c2.Run(context.Background()).OK {
		t.Fatal("expect-up must not fire when unreachable")
	}
}

func TestICMPStateOnChange(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", onChange: true,
		sampler: pinger(PingSample{Reachable: true}, PingSample{Reachable: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data["old"] != "up" || res.Data["new"] != "down" {
		t.Fatalf("state change should fire with old/new: %+v", res)
	}
}

func TestICMPLatencyThreshold(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasThreshold: true, op: ">", value: 100,
		sampler: pinger(PingSample{Reachable: true, RTTms: 150, RTTKnown: true})}
	if !c.Run(context.Background()).OK {
		t.Fatal("rtt 150 > 100 should fire")
	}
	c2 := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasThreshold: true, op: ">", value: 100,
		sampler: pinger(PingSample{Reachable: true, RTTms: 50, RTTKnown: true})}
	if c2.Run(context.Background()).OK {
		t.Fatal("rtt 50 should not fire")
	}
}

func TestICMPLatencyThresholdUnreachable(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasThreshold: true, op: ">", value: 0,
		sampler: pinger(PingSample{Reachable: false, RTTKnown: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("unreachable must not fire latency")
	}
}

func TestICMPLatencyChange(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasChange: true, delta: 50,
		sampler: pinger(
			PingSample{Reachable: true, RTTms: 20, RTTKnown: true},
			PingSample{Reachable: true, RTTms: 100, RTTKnown: true}, // |100-20|=80 > 50
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first reachable cycle primes")
	}
	if !c.Run(context.Background()).OK {
		t.Fatal("latency jump should fire")
	}
}

func TestICMPLatencyChangeUnreachableNoCorrupt(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasChange: true, delta: 50,
		sampler: pinger(
			PingSample{Reachable: true, RTTms: 20, RTTKnown: true},  // prime baseline 20
			PingSample{Reachable: false, RTTKnown: false},           // no fire, no baseline update
			PingSample{Reachable: true, RTTms: 25, RTTKnown: true},  // |25-20|=5 < 50 -> no fire
		)}
	c.Run(context.Background()) // prime
	if c.Run(context.Background()).OK {
		t.Fatal("unreachable cycle must not fire")
	}
	if c.Run(context.Background()).OK {
		t.Fatal("baseline must be preserved (25 vs primed 20, not vs unreachable)")
	}
}

func TestICMPSamplerError(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", expect: "up",
		sampler: func(string, int, time.Duration) (PingSample, error) { return PingSample{}, errors.New("boom") }}
	if c.Run(context.Background()).OK {
		t.Fatal("sampler error must not fire")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/checks/ -run TestICMP`
Expected: FAIL — `icmpCheck`/`PingSample`/`PingSamplerFunc` undefined.

- [ ] **Step 3: Add the dependency**

Run: `go get golang.org/x/net@latest`
Expected: `go.mod`/`go.sum` updated with `golang.org/x/net`.

- [ ] **Step 4: Implement `internal/checks/icmp.go`**

```go
package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// PingSample is one ICMP observation of a host.
type PingSample struct {
	Reachable bool
	RTTms     float64
	RTTKnown  bool
}

// PingSamplerFunc probes a host with count echo requests bounded by timeout.
// Injected for tests; the default uses native ICMP via golang.org/x/net.
type PingSamplerFunc func(host string, count int, timeout time.Duration) (PingSample, error)

// icmpCheck watches one metric (state|latency) of one external host. Stateful
// across cycles (baseline for on:change / change), hence a pointer type; safe
// because a watch ticks sequentially on its own goroutine. OK==true means "fire".
type icmpCheck struct {
	base
	host    string
	count   int
	metric  string
	expect  string // state: "up"|"down"; "" means on-change
	onChange bool
	hasThreshold bool
	op       string
	value    float64
	hasChange bool
	delta    float64
	sampler  PingSamplerFunc

	primed    bool
	lastState string
	lastRTT   float64
}

func (c *icmpCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultPingSampler
	}
	s, err := sampler(c.host, c.count, c.timeout)
	if err != nil {
		return c.result(false, fmt.Sprintf("icmp %s: %v", c.host, err), start)
	}
	data := map[string]any{"host": c.host, "metric": c.metric}

	switch c.metric {
	case "state":
		state := "down"
		if s.Reachable {
			state = "up"
		}
		if c.expect != "" {
			data["value"] = state
			res := c.result(state == c.expect, fmt.Sprintf("%s %s (want %s)", c.host, state, c.expect), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastState = true, state
			res := c.result(false, fmt.Sprintf("%s state baseline %s", c.host, state), start)
			res.Data = data
			return res
		}
		changed := state != c.lastState
		data["old"], data["new"], data["value"] = c.lastState, state, state
		msg := fmt.Sprintf("%s state %s->%s", c.host, c.lastState, state)
		c.lastState = state
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case "latency":
		if !s.RTTKnown {
			res := c.result(false, fmt.Sprintf("%s unreachable (no rtt)", c.host), start)
			res.Data = data
			return res
		}
		if c.hasThreshold {
			data["value"] = s.RTTms
			met := compareFloat(s.RTTms, c.op, c.value)
			res := c.result(met, fmt.Sprintf("%s rtt %.1fms %s %.1f", c.host, s.RTTms, c.op, c.value), start)
			res.Data = data
			return res
		}
		// change mode
		if !c.primed {
			c.primed, c.lastRTT = true, s.RTTms
			res := c.result(false, fmt.Sprintf("%s rtt baseline %.1fms", c.host, s.RTTms), start)
			res.Data = data
			return res
		}
		diff := s.RTTms - c.lastRTT
		if diff < 0 {
			diff = -diff
		}
		changed := diff > c.delta
		data["old"], data["new"], data["value"] = c.lastRTT, s.RTTms, s.RTTms
		msg := fmt.Sprintf("%s rtt %.1f->%.1fms (|Δ|=%.1f > %.1f)", c.host, c.lastRTT, s.RTTms, diff, c.delta)
		c.lastRTT = s.RTTms
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	default:
		res := c.result(false, "unknown icmp metric "+c.metric, start)
		res.Data = data
		return res
	}
}

// defaultPingSampler sends count ICMPv4 echo requests via a raw socket
// (needs CAP_NET_RAW) and reports reachability + mean RTT in ms.
func defaultPingSampler(host string, count int, timeout time.Duration) (PingSample, error) {
	if count <= 0 {
		count = 3
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	addr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return PingSample{}, err
	}
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return PingSample{}, err
	}
	defer conn.Close()

	perPacket := timeout / time.Duration(count)
	if perPacket <= 0 {
		perPacket = time.Second
	}
	id := os.Getpid() & 0xffff
	var rtts []float64
	for seq := 0; seq < count; seq++ {
		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: 0,
			Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("sermo")},
		}
		b, err := msg.Marshal(nil)
		if err != nil {
			continue
		}
		sent := time.Now()
		_ = conn.SetWriteDeadline(time.Now().Add(perPacket))
		if _, err := conn.WriteTo(b, addr); err != nil {
			continue
		}
		reply := make([]byte, 1500)
		_ = conn.SetReadDeadline(time.Now().Add(perPacket))
		n, _, err := conn.ReadFrom(reply)
		if err != nil {
			continue
		}
		rm, err := icmp.ParseMessage(1, reply[:n]) // 1 = ICMPv4 protocol number
		if err != nil {
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			rtts = append(rtts, float64(time.Since(sent).Microseconds())/1000.0)
		}
	}
	if len(rtts) == 0 {
		return PingSample{}, nil
	}
	var sum float64
	for _, r := range rtts {
		sum += r
	}
	return PingSample{Reachable: true, RTTKnown: true, RTTms: sum / float64(len(rtts))}, nil
}
```

- [ ] **Step 5: Wire into the builder**

In `internal/checks/build.go`, add to `Deps` (after `NetSampler`):

```go
	// PingSampler probes a host via ICMP for `icmp` checks. Nil uses native ICMP.
	PingSampler PingSamplerFunc
```

Add `case "icmp"` to `buildCheck` (before `case "":`):

```go
	case "icmp":
		host := asString(entry["host"])
		if host == "" {
			return nil, "icmp check requires a host"
		}
		count := 3
		if v, ok := intField(entry["count"]); ok {
			if v <= 0 {
				return nil, "icmp count must be a positive integer"
			}
			count = v
		}
		metric := asString(entry["metric"])
		c := &icmpCheck{base: b, host: host, count: count, metric: metric, sampler: deps.PingSampler}
		switch metric {
		case "state":
			if exp := asString(entry["expect"]); exp != "" {
				if exp != "up" && exp != "down" {
					return nil, "icmp state expect must be up or down"
				}
				c.expect = exp
			} else if asString(entry["on"]) == "change" {
				c.onChange = true
			} else {
				return nil, "icmp state requires expect: up|down or on: change"
			}
		case "latency":
			if th, ok := entry["threshold"].(map[string]any); ok {
				op := asString(th["op"])
				if !validDiskOp(op) {
					return nil, "icmp latency threshold has an invalid op"
				}
				v, err := strconv.ParseFloat(scalarString(th["value"]), 64)
				if err != nil {
					return nil, "icmp latency threshold value must be numeric"
				}
				c.hasThreshold, c.op, c.value = true, op, v
			} else if ch, ok := entry["change"].(map[string]any); ok {
				d, err := strconv.ParseFloat(scalarString(ch["delta"]), 64)
				if err != nil {
					return nil, "icmp latency change delta must be numeric"
				}
				c.hasChange, c.delta = true, d
			} else {
				return nil, "icmp latency requires threshold {op, value} or change {delta}"
			}
		default:
			return nil, "icmp check metric must be state or latency"
		}
		return c, ""
```

(`strconv`, `validDiskOp`, `scalarString`, `asString`, `intField` already exist in the package.)

- [ ] **Step 6: Tidy + run tests**

Run: `go mod tidy && go build ./... && go test ./internal/checks/ -v`
Expected: build OK; all icmp + existing checks tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/checks/icmp.go internal/checks/icmp_test.go internal/checks/build.go go.mod go.sum
git commit -m "Add icmp check type (state/latency via native ICMP, stateful)"
```

---

## Task 2: Generalise `buildNetWatches` → `buildMetricWatches`

**Files:**
- Modify: `internal/app/watch_build.go`
- Modify: `internal/app/watch_build_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/app/watch_build_test.go`:

```go
func TestBuildWatchesExpandsICMP(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"ping-gw": map[string]any{
			"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
			"metrics": map[string]any{
				"state": map[string]any{
					"on":   "change",
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}},
				},
				"latency": map[string]any{
					"threshold": map[string]any{"op": ">", "value": 100},
					"then":      map[string]any{"hook": map[string]any{"command": []any{"/bin/lat.sh"}}},
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
		if w.CheckType != "icmp" || w.Name != "ping-gw" {
			t.Fatalf("unexpected watch: %+v", w)
		}
		cmds[w.Hook.Command[0]] = true
	}
	if !cmds["/bin/state.sh"] || !cmds["/bin/lat.sh"] {
		t.Fatalf("expected distinct per-metric hooks, got %v", cmds)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestBuildWatchesExpandsICMP`
Expected: FAIL — icmp dispatches to `buildSingleWatch` (no top-level hook) and errors.

- [ ] **Step 3: Generalise the expansion**

In `internal/app/watch_build.go`, REPLACE `buildNetWatches` with the type-agnostic `buildMetricWatches`:

```go
// buildMetricWatches expands one multi-metric watch entry (net/icmp) into one
// Watch per metric, each with its own check, window and hook. The per-metric
// check entry is the watch's base check fields plus metric:<key> plus the
// metric block's condition keys (everything except then/for/within). Builder-set
// keys (type, host/interface, count, metric) take precedence over the block.
func buildMetricWatches(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) ([]*Watch, []string) {
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		return nil, []string{"watch " + name + ": " + stringField(checkEntry["type"]) + " check requires a non-empty metrics map"}
	}
	var out []*Watch
	var warns []string
	for _, key := range sortedWatchNames(metrics) {
		mEntry, ok := metrics[key].(map[string]any)
		if !ok {
			warns = append(warns, "watch "+name+".metrics."+key+": not a mapping")
			continue
		}
		ce := map[string]any{}
		for k, v := range mEntry { // condition keys
			switch k {
			case "then", "for", "within":
			default:
				ce[k] = v
			}
		}
		for k, v := range checkEntry { // base check fields win
			ce[k] = v
		}
		ce["metric"] = key

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
			CheckType: stringField(checkEntry["type"]),
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

In `BuildWatches`, change the dispatch `case "net":` to cover both:

```go
		switch stringField(checkEntry["type"]) {
		case "net", "icmp":
			expanded, warns := buildMetricWatches(name, entry, checkEntry, deps, interval)
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

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/app/ -v`
Expected: PASS — new icmp expansion test AND the existing net expansion tests (`TestBuildWatchesExpandsNet`, `TestBuildWatchesNetWarnsOnBadMetric`) still pass against the generalised function, plus disk build tests via `buildSingleWatch`.

- [ ] **Step 5: Commit**

```bash
git add internal/app/watch_build.go internal/app/watch_build_test.go
git commit -m "Generalise net expansion to buildMetricWatches (net + icmp)"
```

---

## Task 3: Config validation for `icmp`

**Files:**
- Modify: `internal/config/validate.go`
- Modify: `internal/config/validate_watches_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/validate_watches_test.go`:

```go
func TestValidateWatchesICMPGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"ping-gw": map[string]any{
				"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
					"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}, "then": map[string]any{"hook": map[string]any{"command": []any{"/y"}}}},
				},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesICMPBad(t *testing.T) {
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
		"no host":    {"check": map[string]any{"type": "icmp"}, "metrics": map[string]any{"state": merge(map[string]any{"on": "change"})}},
		"bad count":  {"check": map[string]any{"type": "icmp", "host": "h", "count": 0}, "metrics": map[string]any{"state": merge(map[string]any{"on": "change"})}},
		"no metrics": {"check": map[string]any{"type": "icmp", "host": "h"}},
		"unknown metric": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"bogus": merge(map[string]any{"on": "change"})}},
		"bad state": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"state": merge(map[string]any{})}},
		"latency neither": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"latency": merge(map[string]any{})}},
		"bad threshold op": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"latency": merge(map[string]any{"threshold": map[string]any{"op": "=>", "value": 1}})}},
		"bad change delta": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"latency": merge(map[string]any{"change": map[string]any{"delta": "abc"}})}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"ping-gw": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run TestValidateWatchesICMP`
Expected: FAIL — icmp not validated (`type "icmp" is not supported`, bad cases produce no issue).

- [ ] **Step 3: Implement**

In `internal/config/validate.go`:

1. Extract the shared state-metric validator (used by net + icmp). Add:

```go
// validateStateMetric validates a state metric condition shared by net/icmp:
// expect up|down OR on: change.
func validateStateMetric(prefix string, m map[string]any, add func(string, ...any)) {
	exp := scalarString(m["expect"])
	onChange := scalarString(m["on"]) == "change"
	if exp == "" && !onChange {
		add("%s requires expect: up|down or on: change", prefix)
	} else if exp != "" && exp != "up" && exp != "down" {
		add("%s.expect must be up or down", prefix)
	}
}
```

In `validateNetCheck`, replace the inline `state` case body with `validateStateMetric(prefix, m, add)`.

2. Add `case "icmp"` to the `validateWatches` switch:

```go
		case "icmp":
			validateICMPCheck(name, check, entry, add)
```

3. Add `validateICMPCheck`:

```go
// validateICMPCheck validates an icmp host watch: a host (+ optional positive
// count) and a non-empty metrics map, each metric with a valid condition and its
// own hook (spec 2026-06-06-icmp-host-watch §3).
func validateICMPCheck(name string, check, entry map[string]any, add func(string, ...any)) {
	if scalarString(check["host"]) == "" {
		add("watches.%s.check.host is required for an icmp check", name)
	}
	if v, present := check["count"]; present {
		if n, ok := scalarInt(v); !ok || n <= 0 {
			add("watches.%s.check.count must be a positive integer", name)
		}
	}
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for an icmp check", name)
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
			validateStateMetric(prefix, m, add)
		case "latency":
			th, hasT := m["threshold"].(map[string]any)
			ch, hasC := m["change"].(map[string]any)
			if !hasT && !hasC {
				add("%s requires threshold {op, value} or change {delta}", prefix)
			}
			if hasT && hasC {
				add("%s must set only one of threshold or change", prefix)
			}
			if hasT {
				if !isValidDiskOp(scalarString(th["op"])) {
					add("%s.threshold has an invalid op %q", prefix, scalarString(th["op"]))
				}
				if !isNumeric(scalarString(th["value"])) {
					add("%s.threshold value %q must be numeric", prefix, scalarString(th["value"]))
				}
			}
			if hasC {
				if !isNumeric(scalarString(ch["delta"])) {
					add("%s.change delta %q must be numeric", prefix, scalarString(ch["delta"]))
				}
			}
		default:
			add("%s is not a supported icmp metric (state, latency)", prefix)
		}
		validateHookBlock(prefix, m, add)
		validateMetricWindow(prefix, m, add)
	}
}
```

(`scalarInt`, `isValidDiskOp`, `isNumeric`, `scalarString`, `sortedKeys`, `fmt` all already exist/are imported.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/config/ -run TestValidateWatches -v`
Then: `go test ./internal/config/` (net validation still green via the shared `validateStateMetric`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_watches_test.go
git commit -m "Validate icmp host watches; share state-metric validation"
```

---

## Task 4: Docs, example config, and final verification

**Files:**
- Modify: `configs/sermo.yml`, `docs/configuration.md`, `README.md`

- [ ] **Step 1: Add a disabled-by-default example to `configs/sermo.yml`**

Append after the existing `net-eth0` entry (SIBLING key under the existing `watches:` map — do NOT create a second `watches:`):

```yaml
  ping-gw:
    enabled: false
    interval: 30s
    check: { type: icmp, host: 8.8.8.8, count: 3 }
    metrics:
      state:
        on: change
        then:
          hook:
            command: [/usr/local/bin/sermo-host-state.sh, "8.8.8.8"]
      latency:
        threshold: { op: ">", value: 100 }
        then:
          hook:
            command: [/usr/local/bin/sermo-host-latency.sh, "8.8.8.8"]
```

- [ ] **Step 2: Extend the "Host watches" section in `docs/configuration.md`**

After the `net` subsection, add an `icmp` subsection documenting: grouped-per-host shape; the two metrics (`state` on/expect, `latency` threshold OR change:{delta}); per-metric hooks; env vars a hook receives (`SERMO_HOST`, `SERMO_METRIC`, `SERMO_VALUE`, and `SERMO_OLD`/`SERMO_NEW` for change metrics); and that the daemon needs `CAP_NET_RAW` (or the `net.ipv4.ping_group_range` sysctl) for ICMP, IPv4-only this iteration. Normal fenced YAML block.

- [ ] **Step 3: Update `README.md`**

Extend the host-watches sentence to mention external hosts via ICMP (reachability + latency) alongside disk and network interfaces.

- [ ] **Step 4: Validate the example config**

Run: `go run ./cmd/sermoctl --config configs/sermo.yml config validate`
Expected: `OK`, exit 0. Then temporarily flip `ping-gw` `enabled: true`, re-run validate (must still be `OK`), then set back to `enabled: false`.

- [ ] **Step 5: Commit**

```bash
git add configs/sermo.yml docs/configuration.md README.md
git commit -m "Document icmp host watches and add example config"
```

---

## Final verification

- [ ] `go build ./... && go vet ./... && go test ./...` — all green.
- [ ] `go run ./cmd/sermoctl --config configs/sermo.yml config validate` exits 0.
- [ ] Net + disk watches unaffected: their check/build/validation tests still pass (net expansion now runs through `buildMetricWatches`; net state validation through the shared `validateStateMetric`).
- [ ] icmp expansion: one `ping-gw` config produces two watches with two distinct hooks.

## Notes / risks

- **New dependency:** `golang.org/x/net` is added in Task 1 (`go get` + `go mod tidy`). The default ICMP sampler is the only importer. Tests use the injected sampler, so they need no network/privileges.
- **Privileges:** the real probe uses a raw `ip4:icmp` socket → `sermod` needs `CAP_NET_RAW` (documented). The default sampler is not unit-tested (like statfs/net default samplers).
- **Builder generalisation:** Task 2 replaces `buildNetWatches` with `buildMetricWatches`. The generic "copy metric-block keys except then/for/within, then overlay base check fields + metric" is behaviour-equivalent to the old explicit net copy (net's `buildCheck` ignores unknown keys; base fields overlaid last so they win). Net expansion tests must stay green — they are the regression guard.
- **Latency change semantics:** `change: {delta}` fires on `|rtt − rtt_prev| > delta`; first reachable cycle primes; unreachable cycles neither fire nor update the baseline (so the baseline is the last *reachable* RTT). Covered by `TestICMPLatencyChangeUnreachableNoCorrupt`.
- **`compareFloat`/`validDiskOp` reuse:** shared from `internal/checks` (disk.go); do not redefine.
- **ICMP reply matching:** the default sampler accepts any echo reply within the deadline (does not strictly match ID/seq). Acceptable for MVP; a busy raw socket could occasionally attribute another ping's reply. Noted, not fixed.
