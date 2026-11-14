package checks

import (
	"context"
	"errors"
	"fmt"
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

// noStoredDumps lists no dump files, so a test never reads the host's
// systemd-coredump storage.
func noStoredDumps() ([]string, error) { return nil, nil }

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
			c := &coredumpCheck{name: "watch-coredump", timeout: time.Second, op: tc.op, value: tc.value, sampler: scriptedCoredumps(tc.totals...), lister: noStoredDumps}
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
	c := &coredumpCheck{name: "watch-coredump", op: ">", value: 0, sampler: scriptedCoredumps(9, 11, 11, 11), lister: noStoredDumps}
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

func TestCoredumpCheckNamesTheCrashedProcesses(t *testing.T) {
	// Two dumps stored by systemd-coredump in the second after the previous
	// sample, plus one from an earlier crash that must not be named again.
	previous := time.Unix(1794528011, 500_000_000)
	files := []string{
		"core.pmdalinux.0.00bd5922a6744287812c69fedb73902d.239961.1794528012000000.zst",
		"core.pmdalinux.0.00bd5922a6744287812c69fedb73902d.232600.1794528012000000.zst",
		"core.pmdalinux.0.00bd5922a6744287812c69fedb73902d.223129.1794182424000000.zst",
		"not-a-core-file",
	}
	now := previous
	c := &coredumpCheck{
		name: "watch-coredump", op: ">", value: 0, sampler: scriptedCoredumps(698, 700),
		lister: func() ([]string, error) { return files, nil },
		clock:  func() time.Time { return now },
	}
	c.Run(context.Background())
	now = previous.Add(30 * time.Second)
	res := c.Run(context.Background())
	const want = "core dumps +2 (total 700): pmdalinux (pid 232600), pmdalinux (pid 239961)"
	if !res.OK || res.Message != want {
		t.Fatalf("crash cycle = OK %v %q, want %q", res.OK, res.Message, want)
	}

	// A burst names the first few; the count still says how many there were.
	burst := make([]string, 0, maxNamedCoredumps+2)
	for pid := 100; pid < 100+maxNamedCoredumps+2; pid++ {
		burst = append(burst, fmt.Sprintf("core.imap.8.00bd5922a6744287812c69fedb73902d.%d.%d.zst", pid, now.Add(time.Second).UnixMicro()))
	}
	files = burst
	c.sampler = scriptedCoredumps(707)
	now = now.Add(30 * time.Second)
	res = c.Run(context.Background())
	if !strings.HasSuffix(res.Message, "imap (pid 104), +2 more") {
		t.Fatalf("burst message = %q, want five names and the rest counted", res.Message)
	}

	// The alert never depends on naming: an unreadable store leaves the count.
	c.lister = func() ([]string, error) { return nil, errors.New("permission denied") }
	c.sampler = scriptedCoredumps(708)
	now = now.Add(30 * time.Second)
	res = c.Run(context.Background())
	if !res.OK || res.Message != "core dumps +1 (total 708)" {
		t.Fatalf("unreadable store = OK %v %q, want the bare count", res.OK, res.Message)
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
	built, warns := Build(map[string]any{"c": map[string]any{"type": CheckTypeCoredump}}, Deps{CoredumpSampler: scriptedCoredumps(0, 1), CoredumpLister: noStoredDumps})
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
