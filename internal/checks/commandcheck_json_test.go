package checks

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

const (
	vinylPanicPath = `counters["MGT.child_panic"].value`
	vinylDiedPath  = `counters["MGT.child_died"].value`
)

// vinylstat renders `vinylstat -1 -j -I MGT.child_panic -I MGT.child_died` in
// the shape the real tool prints. A negative died omits that counter.
func vinylstat(panics, died int) string {
	counter := func(name, desc string, value int) string {
		return fmt.Sprintf(`%q:{"description":%q,"flag":"c","format":"i","value":%d}`, name, desc, value)
	}
	counters := counter("MGT.child_panic", "Child process panic", panics)
	if died >= 0 {
		counters += "," + counter("MGT.child_died", "Child process died (signal)", died)
	}
	return `{"version":1,"timestamp":"2026-11-04T11:01:30","counters":{` + counters + "}}\n"
}

func vinylResult(panics, died int) execx.Result {
	return execx.Result{Stdout: vinylstat(panics, died)}
}

// buildVinylCheck builds the catalog's child-crash watch over a scripted runner
// and a clock the test advances.
func buildVinylCheck(t *testing.T, runner execx.Runner, now *time.Time) commandCheck {
	t.Helper()
	built, warns := Build(map[string]any{"crashes": map[string]any{
		"type":         "command",
		"command":      []any{"/usr/bin/vinylstat", "-1", "-j", "-I", "MGT.child_panic", "-I", "MGT.child_died"},
		"max_increase": map[string]any{vinylPanicPath: 0, vinylDiedPath: 0},
		"within":       "15m",
	}}, Deps{Runner: runner, DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("build: built=%d warns=%v", len(built), warns)
	}
	c := built[0].Check.(commandCheck)
	c.clock = func() time.Time { return *now }
	return c
}

func TestCommandMaxIncreaseFailsOnCounterGrowth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := buildVinylCheck(t, execxtest.Queued(vinylResult(3, 3), vinylResult(3, 3), vinylResult(4, 4), vinylResult(4, 4)), &now)

	// A counter already above zero is history, not an event: the first cycle
	// only baselines and an unchanged one passes.
	for range 2 {
		if r := c.Run(context.Background()); !r.OK || r.Data[vinylPanicPath+DataKeyIncreaseSuffix] != 0 || r.Data[DataKeyWindow] != "15m0s" {
			t.Fatalf("an unchanged counter should pass with +0: %+v", r)
		}
		now = now.Add(time.Minute)
	}
	r := c.Run(context.Background())
	want := "exit 0; " + vinylDiedPath + " grew by 1 in 2m0s (max_increase 0): 4 now"
	if r.OK || r.Unavailable || r.Message != want {
		t.Fatalf("one new child death should fail:\n got %q (unavailable %v)\nwant %q", r.Message, r.Unavailable, want)
	}
	if r.Data[vinylPanicPath+DataKeyIncreaseSuffix] != 1 || r.Data[vinylDiedPath+DataKeyIncreaseSuffix] != 1 {
		t.Fatalf("growth data = %+v", r.Data)
	}
	now = now.Add(16 * time.Minute)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("once the rise leaves the window the check recovers: %+v", r)
	}
}

// A restarted child zeroes its counters. The higher pre-restart sample stays
// the baseline until it slides out of within, so growth reads 0 meanwhile —
// the documented blind spot — and a later rise is caught again.
func TestCommandMaxIncreaseCounterReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := buildVinylCheck(t, execxtest.Queued(vinylResult(5, 0), vinylResult(0, 0), vinylResult(1, 0), vinylResult(2, 0)), &now)
	for i, step := range []time.Duration{0, time.Minute, time.Minute} {
		now = now.Add(step)
		if r := c.Run(context.Background()); !r.OK || r.Data[vinylPanicPath+DataKeyIncreaseSuffix] != 0 {
			t.Fatalf("cycle %d: a reset must not read as growth while the old baseline is in the window: %+v", i, r)
		}
	}
	now = now.Add(15 * time.Minute)
	if r := c.Run(context.Background()); r.OK || !strings.Contains(r.Message, vinylPanicPath+" grew by 1") {
		t.Fatalf("growth after the reset should fail once the old baseline left the window: %+v", r)
	}
}

func TestCommandMaxIncreaseUnusableValueIsUnavailable(t *testing.T) {
	for name, stdout := range map[string]string{
		"missing key":       vinylstat(0, -1),
		"non-numeric value": strings.Replace(vinylstat(0, 0), `"Child process died (signal)","flag":"c","format":"i","value":0`, `"Child process died (signal)","flag":"c","format":"i","value":"n/a"`, 1),
		"beyond int range":  strings.Replace(vinylstat(0, 0), `"Child process died (signal)","flag":"c","format":"i","value":0`, `"Child process died (signal)","flag":"c","format":"i","value":18446744073709551615`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0)
			c := buildVinylCheck(t, execxtest.Fixed(execx.Result{Stdout: stdout}, nil), &now)
			r := c.Run(context.Background())
			if r.OK || !r.Unavailable || !strings.Contains(r.Message, "not available as a number for max_increase") {
				t.Fatalf("an unusable counter should be unavailable: %+v", r)
			}
			if _, present := r.Data[vinylDiedPath+DataKeyIncreaseSuffix]; present {
				t.Fatalf("an unusable counter must publish no growth: %+v", r.Data)
			}
			if r.Data[vinylPanicPath+DataKeyIncreaseSuffix] != 0 {
				t.Fatalf("the usable counter is still sampled: %+v", r.Data)
			}
		})
	}
}

// Output Sermo could not read as a counter report leaves every window alone:
// the next good sample is the baseline, not a rise from the bad cycle.
func TestCommandMaxIncreaseSkipsCyclesWithoutAReport(t *testing.T) {
	for _, tc := range []struct {
		name            string
		first           execx.Result
		err             error
		wantUnavailable bool
		wantMessage     string
	}{
		{name: "non-JSON stdout", first: execx.Result{Stdout: "VSM: could not open\n"}, wantMessage: "exit 0; stdout is not JSON"},
		{name: "unexpected exit", first: execx.Result{ExitCode: 1, Stdout: vinylstat(0, 0)}, wantMessage: "exit 1 (want 0)"},
		{name: "timeout", first: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: fmt.Errorf("run vinylstat: %w", context.DeadlineExceeded), wantUnavailable: true, wantMessage: "timeout after 1s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0)
			c := buildVinylCheck(t, execxtest.Fixed(tc.first, tc.err), &now)
			r := c.Run(context.Background())
			if r.OK || r.Unavailable != tc.wantUnavailable || !strings.Contains(r.Message, tc.wantMessage) {
				t.Fatalf("first cycle = %q (unavailable %v), want %q (unavailable %v)", r.Message, r.Unavailable, tc.wantMessage, tc.wantUnavailable)
			}
			if _, sampled := r.Data[DataKeyWindow]; sampled {
				t.Fatalf("a cycle without a report must not sample: %+v", r.Data)
			}
			c.runner = execxtest.Fixed(vinylResult(3, 3), nil)
			now = now.Add(time.Minute)
			if r := c.Run(context.Background()); !r.OK || r.Data[vinylPanicPath+DataKeyIncreaseSuffix] != 0 {
				t.Fatalf("the first report after it must only baseline: %+v", r)
			}
		})
	}
}

func TestCommandExpectJSON(t *testing.T) {
	const dropped = `counters["MAIN.sess_dropped"].value`
	report := func(value string) string {
		return `{"version":1,"counters":{"MAIN.sess_dropped":{"flag":"c","format":"i","value":` + value + `}}}`
	}
	build := func(stdout string) commandCheck {
		built, warns := Build(map[string]any{"sessions": map[string]any{
			"type":        "command",
			"command":     []any{"/usr/bin/vinylstat", "-1", "-j", "-I", "MAIN.sess_dropped"},
			"expect_json": map[string]any{dropped: 0, "version": map[string]any{"op": ">=", "value": 1}},
		}}, Deps{Runner: execxtest.Fixed(execx.Result{Stdout: stdout}, nil), DefaultTimeout: time.Second})
		if len(warns) != 0 {
			t.Fatalf("build warns: %v", warns)
		}
		return built[0].Check.(commandCheck)
	}
	if r := build(report("0")).Run(context.Background()); !r.OK {
		t.Fatalf("matching JSON should pass: %+v", r)
	}
	r := build(report("2")).Run(context.Background())
	if want := `exit 0; json "counters[\"MAIN.sess_dropped\"].value" == "0" (got "2")`; r.OK || r.Unavailable || r.Message != want {
		t.Fatalf("mismatch = %q (unavailable %v), want %q", r.Message, r.Unavailable, want)
	}
	if r := build(`{"version":1,"counters":{}}`).Run(context.Background()); r.OK || r.Unavailable || !strings.Contains(r.Message, "missing") {
		t.Fatalf("a missing path fails like the http check: %+v", r)
	}
	if r := build("not json").Run(context.Background()); r.OK || r.Unavailable || !strings.Contains(r.Message, "stdout is not JSON") {
		t.Fatalf("non-JSON stdout fails: %+v", r)
	}
}

// expect_stdout is evaluated first, yet the growth window still samples the
// cycle it fails, and the failing result carries the reading.
func TestCommandMaxIncreaseSamplesWhenAnEarlierAssertionFails(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := buildVinylCheck(t, execxtest.Fixed(vinylResult(0, 0), nil), &now)
	c.stdout = mustOutputMatcher(t, "absent text")
	r := c.Run(context.Background())
	if r.OK || !strings.Contains(r.Message, "stdout") || r.Data[vinylPanicPath+DataKeyIncreaseSuffix] != 0 || r.Data[DataKeyOutput] == nil {
		t.Fatalf("expect_stdout failure should keep its message, output and the sampled growth: %+v", r)
	}
}

func mustOutputMatcher(t *testing.T, v any) OutputMatcher {
	t.Helper()
	m, warn := ParseOutputMatcher(v)
	if warn != "" {
		t.Fatal(warn)
	}
	return m
}

func TestBuildCommandCheckJSONOptionErrors(t *testing.T) {
	base := func(extra map[string]any) map[string]any {
		entry := map[string]any{"type": "command", "command": []any{"/usr/bin/vinylstat", "-1", "-j"}}
		maps.Copy(entry, extra)
		return entry
	}
	for name, tc := range map[string]struct {
		extra map[string]any
		want  string
	}{
		"expect_json not a mapping": {map[string]any{"expect_json": "x"}, "expect_json must be a mapping"},
		"expect_json bad path":      {map[string]any{"expect_json": map[string]any{"a[x]": 1}}, "double-quoted key"},
		"expect_json bad op":        {map[string]any{"expect_json": map[string]any{"a": map[string]any{"op": "=>", "value": 1}}}, "op"},
		"max_increase bad path":     {map[string]any{"max_increase": map[string]any{`a["x"]b`: 0}, "within": "10m"}, "followed by . or ["},
		"max_increase alone":        {map[string]any{"max_increase": map[string]any{"a": 0}}, "requires within"},
		"within alone":              {map[string]any{"within": "10m"}, "within requires max_increase"},
		"max_increase not mapping":  {map[string]any{"max_increase": 5, "within": "10m"}, "must be a mapping"},
		"negative bound":            {map[string]any{"max_increase": map[string]any{"a": -1}, "within": "10m"}, "non-negative integer"},
		"non-integer bound":         {map[string]any{"max_increase": map[string]any{"a": "lots"}, "within": "10m"}, "non-negative integer"},
		"zero within":               {map[string]any{"max_increase": map[string]any{"a": 0}, "within": "0s"}, "positive duration"},
	} {
		t.Run(name, func(t *testing.T) {
			_, warns := Build(map[string]any{"c": base(tc.extra)}, Deps{Runner: execxtest.Fixed(execx.Result{}, nil), DefaultTimeout: time.Second})
			if len(warns) != 1 || !strings.Contains(warns[0], "command check") || !strings.Contains(warns[0], tc.want) {
				t.Fatalf("warns = %v, want one containing %q", warns, tc.want)
			}
		})
	}
}
