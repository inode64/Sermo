package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedCoredumps returns a sampler that yields the given totals on successive
// calls and then keeps returning the last one.
func scriptedCoredumps(totals ...uint64) CoredumpSamplerFunc {
	i := 0
	return func(context.Context) (uint64, error) {
		total := totals[i]
		if i < len(totals)-1 {
			i++
		}
		return total, nil
	}
}

func TestCoredumpCheckRun(t *testing.T) {
	tests := []struct {
		name      string
		totals    []uint64
		op        string
		value     float64
		wantFired []bool
		wantDumps uint64
	}{
		{
			// The first cycle only learns the total: dumps before sermod
			// started are history, not news.
			name: "baseline then one crash", totals: []uint64{9, 9, 10, 10},
			op: ">", value: 0, wantFired: []bool{false, false, true, false}, wantDumps: 0,
		},
		{
			// systemd-coredump.socket restarted: its count starts again, and
			// a smaller total is not a negative number of crashes.
			name: "counter restarts", totals: []uint64{40, 2, 3},
			op: ">", value: 0, wantFired: []bool{false, false, true}, wantDumps: 1,
		},
		{
			name: "burst threshold", totals: []uint64{0, 1, 6},
			op: ">", value: 3, wantFired: []bool{false, false, true}, wantDumps: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &coredumpCheck{name: "watch-coredump", timeout: time.Second, op: tc.op, value: tc.value, sampler: scriptedCoredumps(tc.totals...)}
			var res Result
			for cycle, want := range tc.wantFired {
				res = c.Run(context.Background())
				if res.OK != want || res.Unavailable {
					t.Fatalf("cycle %d: OK = %v unavailable = %v, want OK %v (%s)", cycle, res.OK, res.Unavailable, want, res.Message)
				}
			}
			last := tc.totals[len(tc.totals)-1]
			if res.Data[DataKeyDumps] != tc.wantDumps || res.Data[DataKeyTotal] != last {
				t.Fatalf("data = %+v, want dumps %d total %d", res.Data, tc.wantDumps, last)
			}
		})
	}
}

func TestCoredumpCheckFiresOnceAndNamesTheTotal(t *testing.T) {
	c := &coredumpCheck{name: "watch-coredump", op: ">", value: 0, sampler: scriptedCoredumps(9, 11, 11, 11)}
	c.Run(context.Background())
	res := c.Run(context.Background())
	if !res.OK || res.Message != "core dumps +2 (total 11)" {
		t.Fatalf("crash cycle = OK %v %q, want the two new dumps named with the total", res.OK, res.Message)
	}
	// Like oom, one crash is true for exactly one cycle: a `for:` window of two
	// or more could never be satisfied, so the watch holds it with `clear:`.
	for range 2 {
		if c.Run(context.Background()).OK {
			t.Fatal("a quiet counter must not keep firing")
		}
	}
}

func TestCoredumpCheckUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		sampler CoredumpSamplerFunc
		wantMsg string
	}{
		{
			name: "collector absent",
			sampler: func(context.Context) (uint64, error) {
				return 0, errors.New("find systemd unit systemd-coredump.socket: Unit systemd-coredump.socket not loaded.")
			},
			wantMsg: "coredump: find systemd unit systemd-coredump.socket",
		},
		{
			name: "core_pattern elsewhere",
			sampler: func(context.Context) (uint64, error) {
				return 0, errors.New(`kernel.core_pattern "core" does not pipe core dumps to systemd-coredump`)
			},
			wantMsg: "does not pipe core dumps",
		},
		{
			name: "bus does not answer",
			sampler: func(ctx context.Context) (uint64, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			},
			wantMsg: "timeout",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &coredumpCheck{name: "watch-coredump", timeout: 20 * time.Millisecond, op: ">", value: 0, sampler: tc.sampler}
			res := c.Run(context.Background())
			if res.OK || !res.Unavailable {
				t.Fatalf("OK = %v unavailable = %v, want an unavailable observation that never fires", res.OK, res.Unavailable)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			if c.primed {
				t.Fatal("an unavailable sample must not become the baseline")
			}
		})
	}
}

func TestPipesToCoredumpCollector(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"|/usr/lib/systemd/systemd-coredump %P %u %g %s %t %c %h %d %F\n", true},
		{"|/lib/systemd/systemd-coredump %P %u %g %s %t 9223372036854775808 %h", true},
		{"core", false},
		{"/var/crash/core.%e.%p", false},
		{"|/usr/share/apport/apport -p%p -s%s -c%c", false},
		{"|", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := pipesToCoredumpCollector(tc.pattern); got != tc.want {
			t.Errorf("pipesToCoredumpCollector(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func TestBuildCoredumpCheck(t *testing.T) {
	built, warns := Build(map[string]any{"c": map[string]any{"type": CheckTypeCoredump}}, Deps{CoredumpSampler: scriptedCoredumps(0, 1)})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("Build() = %d checks, warnings %v; want one check", len(built), warns)
	}
	built[0].Check.Run(context.Background())
	if !built[0].Check.Run(context.Background()).OK {
		t.Fatal("a bare coredump check must fire on any new dump (delta 1 > 0)")
	}
	if _, warns := Build(map[string]any{"c": map[string]any{"type": CheckTypeCoredump, "delta": map[string]any{"op": "=>", "value": 1}}}, Deps{}); len(warns) == 0 {
		t.Fatal("an invalid coredump delta op must be rejected")
	}
}
