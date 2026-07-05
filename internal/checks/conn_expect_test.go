package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/conn"
)

// probeReturning is an injected probe that always returns res.
func probeReturning(res conn.Result) func(context.Context, conn.Config) (conn.Result, error) {
	return func(context.Context, conn.Config) (conn.Result, error) { return res, nil }
}

func connCheckWithExpect(expect []jsonAssertion, res conn.Result) connCheck {
	return connCheck{
		base:   base{name: "c", timeout: time.Second},
		proto:  fakeProto{},
		cfg:    conn.Config{Host: "h", Port: 1},
		probe:  probeReturning(res),
		expect: expect,
	}
}

func TestConnExpectExtraField(t *testing.T) {
	res := conn.Result{Extra: map[string]string{"answers": "3", "rcode": "NOERROR"}}

	// answers > 0 holds.
	c := connCheckWithExpect([]jsonAssertion{{path: "answers", op: ">", value: "0"}}, res)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("answers > 0 should pass: %s", r.Message)
	}
	// answers > 5 fails (probe still succeeded, but the assertion does not hold).
	c = connCheckWithExpect([]jsonAssertion{{path: "answers", op: ">", value: "5"}}, res)
	if r := c.Run(context.Background()); r.OK {
		t.Fatal("answers > 5 should fail")
	}
	// rcode == NOERROR (string equality).
	c = connCheckWithExpect([]jsonAssertion{{path: "rcode", op: "==", value: "NOERROR"}}, res)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("rcode == NOERROR should pass: %s", r.Message)
	}
}

func TestConnExpectVersionRegexAndMissing(t *testing.T) {
	res := conn.Result{Version: "8.0.36", Extra: map[string]string{}}

	c := connCheckWithExpect([]jsonAssertion{{path: "version", op: "=~", value: `^8\.`}}, res)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("version =~ ^8. should pass: %s", r.Message)
	}
	// A field that the probe does not expose fails clearly.
	c = connCheckWithExpect([]jsonAssertion{{path: "stratum", op: "<", value: "3"}}, res)
	r := c.Run(context.Background())
	if r.OK {
		t.Fatal("missing field should fail")
	}
	if got := r.Message; got == "" {
		t.Fatal("expected a descriptive message for the missing field")
	}

	// All assertions must hold (AND): one failing fails the check.
	c = connCheckWithExpect([]jsonAssertion{
		{path: "version", op: "=~", value: `^8\.`},
		{path: "version", op: "==", value: "9.9"},
	}, res)
	if r := c.Run(context.Background()); r.OK {
		t.Fatal("a failing assertion in the list should fail the check")
	}
}

func TestConnExpectLatency(t *testing.T) {
	res := conn.Result{Version: "1.0"}

	// A generous ceiling passes and latency_ms is exposed in the data.
	c := connCheckWithExpect(nil, res)
	c.latencyOp, c.latencyValue = "<", "100000"
	r := c.Run(context.Background())
	if !r.OK {
		t.Fatalf("latency under 100s should pass: %s", r.Message)
	}
	if _, ok := r.Data["latency_ms"]; !ok {
		t.Fatalf("data should carry latency_ms: %v", r.Data)
	}

	// latency < 0 is impossible -> deterministic failure.
	c = connCheckWithExpect(nil, res)
	c.latencyOp, c.latencyValue = "<", "0"
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
	if cc := built[0].Check.(connCheck); cc.latencyOp != "<" || cc.latencyValue != "800" {
		t.Fatalf("latency = %q %q", cc.latencyOp, cc.latencyValue)
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
