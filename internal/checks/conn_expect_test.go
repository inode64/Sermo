package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/conn"
)

// probeReturning is an injected probe that always returns res.
func probeReturning(res conn.Result) func(context.Context, conn.Config) (conn.Result, error) {
	return func(context.Context, conn.Config) (conn.Result, error) { return res, nil }
}

func TestConnCheckPreservesProtocolFailureAsVerdict(t *testing.T) {
	c := connCheckWithExpect(nil, conn.Result{
		Failure: "rcpt_to rejected: 550 blocked",
		Extra:   map[string]string{"smtp_code": "550"},
	})
	res := c.Run(context.Background())
	if res.OK || res.Unavailable || !strings.Contains(res.Message, "550 blocked") {
		t.Fatalf("result = %+v", res)
	}
	if res.Data["smtp_code"] != "550" {
		t.Fatalf("structured rejection data = %+v", res.Data)
	}
}

func TestConnCheckAnyMatchContinuesAfterProtocolFailure(t *testing.T) {
	var calls []string
	c := connCheckWithExpect(nil, conn.Result{})
	c.ifaces = []string{"blocked", "accepted"}
	c.probe = func(_ context.Context, cfg conn.Config) (conn.Result, error) {
		calls = append(calls, cfg.Interface)
		if cfg.Interface == "blocked" {
			return conn.Result{
				Failure: "rcpt_to rejected: 550 blocked",
				Extra:   map[string]string{"smtp_code": "550"},
			}, nil
		}
		return conn.Result{Extra: map[string]string{"accepted": "true"}}, nil
	}

	res := c.Run(context.Background())
	if !res.OK || len(calls) != 2 {
		t.Fatalf("result = %+v, calls = %v", res, calls)
	}
	if res.Data["accepted"] != "true" {
		t.Fatalf("accepted result data = %+v", res.Data)
	}
	interfaces := res.Data[DataKeyInterfaces].(map[string]any)
	if interfaces["blocked"] != "rcpt_to rejected: 550 blocked" || interfaces["accepted"] != interfaceResultOK {
		t.Fatalf("interface results = %+v", interfaces)
	}
}

func TestConnCheckAllMatchStopsOnProtocolFailure(t *testing.T) {
	var calls []string
	c := connCheckWithExpect(nil, conn.Result{})
	c.ifaces = []string{"blocked", "accepted"}
	c.ifaceAll = true
	c.probe = func(_ context.Context, cfg conn.Config) (conn.Result, error) {
		calls = append(calls, cfg.Interface)
		if cfg.Interface == "blocked" {
			return conn.Result{
				Failure: "rcpt_to rejected: 550 blocked",
				Extra:   map[string]string{"smtp_code": "550"},
			}, nil
		}
		return conn.Result{Extra: map[string]string{"accepted": "true"}}, nil
	}

	res := c.Run(context.Background())
	if res.OK || res.Unavailable || len(calls) != 1 || calls[0] != "blocked" {
		t.Fatalf("result = %+v, calls = %v", res, calls)
	}
	if res.Data["smtp_code"] != "550" {
		t.Fatalf("rejection data = %+v", res.Data)
	}
	interfaces := res.Data[DataKeyInterfaces].(map[string]any)
	if interfaces["blocked"] != "rcpt_to rejected: 550 blocked" {
		t.Fatalf("interface results = %+v", interfaces)
	}
}

func TestConnCheckAnyMatchPrefersProtocolFailureOverTransportError(t *testing.T) {
	c := connCheckWithExpect(nil, conn.Result{})
	c.ifaces = []string{"blocked", "offline"}
	c.probe = func(_ context.Context, cfg conn.Config) (conn.Result, error) {
		if cfg.Interface == "blocked" {
			return conn.Result{
				Failure: "rcpt_to rejected: 451 throttled",
				Extra:   map[string]string{"smtp_code": "451"},
			}, nil
		}
		return conn.Result{}, errors.New("network unreachable")
	}

	res := c.Run(context.Background())
	if res.OK || res.Unavailable || !strings.Contains(res.Message, "451 throttled") {
		t.Fatalf("result = %+v", res)
	}
	if res.Data["smtp_code"] != "451" {
		t.Fatalf("rejection data = %+v", res.Data)
	}
	interfaces := res.Data[DataKeyInterfaces].(map[string]any)
	if interfaces["blocked"] != "rcpt_to rejected: 451 throttled" || interfaces["offline"] != "network unreachable" {
		t.Fatalf("interface results = %+v", interfaces)
	}
}

func connCheckWithExpect(expect []jsonAssertion, res conn.Result) connCheck {
	return connCheck{
		name: "c", timeout: time.Second,
		proto:  fakeProto{},
		cfg:    conn.Config{Host: "h", Port: 1},
		probe:  probeReturning(res),
		expect: expect,
	}
}

func TestConnExpectExtraField(t *testing.T) {
	res := conn.Result{Extra: map[string]string{"answers": "3", "rcode": "NOERROR"}}

	// answers > 0 holds.
	c := connCheckWithExpect([]jsonAssertion{{path: "answers", valueMatcher: newValueMatcher(">", "0")}}, res)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("answers > 0 should pass: %s", r.Message)
	}
	// answers > 5 fails (probe still succeeded, but the assertion does not hold).
	c = connCheckWithExpect([]jsonAssertion{{path: "answers", valueMatcher: newValueMatcher(">", "5")}}, res)
	if r := c.Run(context.Background()); r.OK {
		t.Fatal("answers > 5 should fail")
	}
	// rcode == NOERROR (string equality).
	c = connCheckWithExpect([]jsonAssertion{{path: "rcode", valueMatcher: newValueMatcher("==", "NOERROR")}}, res)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("rcode == NOERROR should pass: %s", r.Message)
	}
}

func TestConnExpectVersionRegexAndMissing(t *testing.T) {
	res := conn.Result{Version: "8.0.36", Extra: map[string]string{}}

	c := connCheckWithExpect([]jsonAssertion{{path: "version", valueMatcher: newValueMatcher("=~", `^8\.`)}}, res)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("version =~ ^8. should pass: %s", r.Message)
	}
	// A field that the probe does not expose fails clearly.
	c = connCheckWithExpect([]jsonAssertion{{path: "stratum", valueMatcher: newValueMatcher("<", "3")}}, res)
	r := c.Run(context.Background())
	if r.OK {
		t.Fatal("missing field should fail")
	}
	if got := r.Message; got == "" {
		t.Fatal("expected a descriptive message for the missing field")
	}

	// All assertions must hold (AND): one failing fails the check.
	c = connCheckWithExpect([]jsonAssertion{
		{path: "version", valueMatcher: newValueMatcher("=~", `^8\.`)},
		{path: "version", valueMatcher: newValueMatcher("==", "9.9")},
	}, res)
	if r := c.Run(context.Background()); r.OK {
		t.Fatal("a failing assertion in the list should fail the check")
	}
}

func TestConnExpectLatency(t *testing.T) {
	res := conn.Result{Version: "1.0"}

	// A generous ceiling passes and latency_ms is exposed in the data.
	c := connCheckWithExpect(nil, res)
	c.latencyAssertion = newValueMatcher("<", "100000")
	r := c.Run(context.Background())
	if !r.OK {
		t.Fatalf("latency under 100s should pass: %s", r.Message)
	}
	if _, ok := r.Data["latency_ms"]; !ok {
		t.Fatalf("data should carry latency_ms: %v", r.Data)
	}

	// latency < 0 is impossible -> deterministic failure.
	c = connCheckWithExpect(nil, res)
	c.latencyAssertion = newValueMatcher("<", "0")
	if r := c.Run(context.Background()); r.OK {
		t.Fatal("latency < 0 must fail")
	}
}

func TestBuildConnCheckExpect(t *testing.T) {
	// dns needs no user; expect mixes a scalar (==) and an {op,value}.
	built, warns := Build(map[string]any{
		"resolver": map[string]any{
			"type": "dns", "host": "1.1.1.1", "query": "example.com",
			"expect": map[string]any{
				"rcode":   "NOERROR",
				"answers": map[string]any{"op": ">", "value": 0},
			},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("dns check with expect should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if len(cc.expect) != 2 {
		t.Fatalf("expected 2 assertions, got %d", len(cc.expect))
	}

	// An invalid expect op warns.
	_, warns = Build(map[string]any{
		"resolver": map[string]any{
			"type": "dns", "expect": map[string]any{"answers": map[string]any{"op": "~~", "value": 0}},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 {
		t.Fatal("invalid expect op should warn")
	}
	_, warns = Build(map[string]any{
		"resolver": map[string]any{
			"type": "dns", "expect": map[string]any{"answers": map[string]any{"op": ">", "value": "abc"}},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 {
		t.Fatal("invalid expect value should warn")
	}

	// expect_latency is parsed onto the connCheck.
	built, warns = Build(map[string]any{
		"resolver": map[string]any{
			"type": "dns", "host": "1.1.1.1",
			"expect_latency": map[string]any{"op": "<", "value": 800},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("dns check with expect_latency should build: warns=%v", warns)
	}
	if cc := built[0].Check.(connCheck); cc.latencyAssertion.op != "<" || cc.latencyAssertion.value != "800" {
		t.Fatalf("latency = %q %q", cc.latencyAssertion.op, cc.latencyAssertion.value)
	}

	// An invalid expect_latency op warns.
	if _, warns := Build(map[string]any{
		"resolver": map[string]any{"type": "dns", "expect_latency": map[string]any{"op": "~~", "value": 1}},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("invalid expect_latency op should warn")
	}
	if _, warns := Build(map[string]any{
		"resolver": map[string]any{"type": "dns", "expect_latency": map[string]any{"op": "<", "value": "abc"}},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("invalid expect_latency value should warn")
	}
}

func keysResult(n string) conn.Result {
	return conn.Result{Extra: map[string]string{"keys": n}}
}

func TestConnMaxIncrease(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := connCheck{
		name: "c", timeout: time.Second,
		proto:     fakeProto{},
		cfg:       conn.Config{Host: "h", Port: 1},
		probe:     probeSeq(keysResult("1000"), keysResult("1400"), keysResult("1700"), keysResult("900"), keysResult("950")),
		increases: []connIncrease{{field: "keys", limit: 500, state: &counterWindow{}}},
		window:    10 * time.Minute,
		clock:     func() time.Time { return now },
	}

	// First cycle only baselines; +400 stays inside the bound.
	for _, want := range []int{0, 400} {
		r := c.Run(context.Background())
		if !r.OK || r.Data["keys_increase"] != want {
			t.Fatalf("growth %d should pass: %+v", want, r)
		}
		now = now.Add(time.Minute)
	}
	// +700 against the oldest sample in the window exceeds max_increase 500.
	r := c.Run(context.Background())
	if r.OK || r.Unavailable || !strings.Contains(r.Message, "keys grew by 700 in 2m0s (max_increase 500): 1700 now") {
		t.Fatalf("growth 700 should fail: %+v", r)
	}
	if r.Data["keys_increase"] != 700 || r.Data[DataKeyWindow] != "10m0s" {
		t.Fatalf("growth data = %+v", r.Data)
	}
	// A fall is not growth, and once the rise slides out of the window the
	// baseline is a later sample.
	now = now.Add(time.Minute)
	if r := c.Run(context.Background()); !r.OK || r.Data["keys_increase"] != 0 {
		t.Fatalf("a falling value should pass with +0: %+v", r)
	}
	now = now.Add(11 * time.Minute)
	if r := c.Run(context.Background()); !r.OK || r.Data["keys_increase"] != 50 {
		t.Fatalf("after the window moved on, growth is measured from the previous sample: %+v", r)
	}
}

func TestConnMaxIncreaseMissingFieldAndExpectPrecedence(t *testing.T) {
	inc := func() []connIncrease {
		return []connIncrease{{field: "keys", limit: 5, state: &counterWindow{}}}
	}
	c := connCheckWithExpect(nil, conn.Result{Extra: map[string]string{"keys": "n/a"}})
	c.increases, c.window = inc(), time.Minute
	if r := c.Run(context.Background()); r.OK || !r.Unavailable || !strings.Contains(r.Message, `field "keys" not available as a number`) {
		t.Fatalf("non-numeric field should be unavailable: %+v", r)
	}

	// A failing expect keeps its own message, and the window still samples.
	c = connCheckWithExpect([]jsonAssertion{{path: "keys", valueMatcher: newValueMatcher("<", "10")}}, keysResult("50"))
	c.increases, c.window = inc(), time.Minute
	r := c.Run(context.Background())
	if r.OK || !strings.Contains(r.Message, "not satisfied") || r.Data["keys_increase"] != 0 {
		t.Fatalf("expect failure should win and still sample: %+v", r)
	}
}

func TestBuildConnCheckMaxIncrease(t *testing.T) {
	built, warns := Build(map[string]any{
		"sessions": map[string]any{
			"type": "redis", "max_increase": map[string]any{"keys": 20000, "evicted_keys": 100}, "within": "10m",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("redis check with max_increase should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if len(cc.increases) != 2 || cc.increases[1].field != "keys" || cc.increases[1].limit != 20000 || cc.window != 10*time.Minute {
		t.Fatalf("increases = %+v window = %s", cc.increases, cc.window)
	}

	for name, entry := range map[string]map[string]any{
		"no within":      {"type": "redis", "max_increase": map[string]any{"keys": 1}},
		"within alone":   {"type": "redis", "within": "10m"},
		"not a mapping":  {"type": "redis", "max_increase": 5, "within": "10m"},
		"zero bound":     {"type": "redis", "max_increase": map[string]any{"keys": 0}, "within": "10m"},
		"empty mapping":  {"type": "redis", "max_increase": map[string]any{}, "within": "10m"},
		"bad within":     {"type": "redis", "max_increase": map[string]any{"keys": 1}, "within": "soon"},
		"negative bound": {"type": "redis", "max_increase": map[string]any{"keys": -3}, "within": "10m"},
	} {
		if _, warns := Build(map[string]any{"c": entry}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
			t.Errorf("%s: should warn", name)
		}
	}
}
