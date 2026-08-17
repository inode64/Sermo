package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/process"
)

func straysDeps(strays ...process.Process) Deps {
	return Deps{Strays: func() []process.Process { return strays }}
}

func stray(pid int, exe string) process.Process {
	return process.Process{
		PID: pid, PPID: 1, UID: 0, User: "root",
		Exe: exe, ExeOK: true,
		Role: process.RoleMain, Source: process.SourceBackend, Stray: true,
	}
}

func TestStraysCheckOKWhenNothingUnaccountedFor(t *testing.T) {
	c, warn := buildStraysCheck(base{name: straysCheckTestName}, nil, straysDeps())
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("want OK with no strays, got %+v", res)
	}
	if got := res.Data[DataKeyValue]; got != float64(0) {
		t.Fatalf("want value 0, got %v", got)
	}
}

const straysCheckTestName = "strays"

// The message must name the executables: the count alone says something
// accumulated, the names say what — the difference between an alert and a
// diagnosis.
func TestStraysCheckReportsExecutablesAndCount(t *testing.T) {
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, nil, straysDeps(
		stray(163544, "/usr/bin/node"),
		stray(90697, "/usr/bin/dbus-daemon"),
	))
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("want not OK when strays exist")
	}
	for _, want := range []string{"/usr/bin/node", "/usr/bin/dbus-daemon", "sermoctl reap"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message %q must contain %q", res.Message, want)
		}
	}
	if got := res.Data[DataKeyValue]; got != float64(2) {
		t.Fatalf("want value 2, got %v", got)
	}
	if got, _ := res.Data[DataKeyPIDs].(string); got != "163544,90697" {
		t.Fatalf("want both pids, got %q", got)
	}
}

// A delegated process is the service's workload, kept alive on purpose. It must
// never be counted here, even if classification handed it over.
func TestStraysCheckExcludesDelegated(t *testing.T) {
	delegated := stray(300, "/usr/sbin/glusterfsd")
	delegated.Delegated = true
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, nil, straysDeps(delegated))
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("want OK: a delegated process is not a stray, got %+v", res)
	}
}

// Duplicate executables collapse and sort, so a service leaking several copies of
// one binary does not produce a reshuffling message every cycle.
func TestStraysCheckDeduplicatesExecutables(t *testing.T) {
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, nil, straysDeps(
		stray(2, "/usr/bin/b"),
		stray(1, "/usr/bin/a"),
		stray(3, "/usr/bin/a"),
	))
	res := c.Run(context.Background())
	if got, _ := res.Data[DataKeyPath].(string); got != "/usr/bin/a,/usr/bin/b" {
		t.Fatalf("want deduplicated sorted executables, got %q", got)
	}
}

// An unidentifiable process is exactly what this check exists to surface, so it
// must never drop out of the list for want of a resolvable exe.
func TestStraysCheckNamesProcessesWithoutAResolvableExe(t *testing.T) {
	unresolved := stray(400, "")
	unresolved.ExeOK = false
	unresolved.Cmdline = []string{"PM2", "God", "Daemon"}
	replaced := stray(401, "")
	replaced.ExeOK = false
	replaced.ExePrev = "/usr/bin/old"
	blank := stray(402, "")
	blank.ExeOK = false

	c, _ := buildStraysCheck(base{name: straysCheckTestName}, nil, straysDeps(unresolved, replaced, blank))
	res := c.Run(context.Background())
	got, _ := res.Data[DataKeyPath].(string)
	for _, want := range []string{"PM2 God Daemon", "/usr/bin/old", unnamedProcess} {
		if !strings.Contains(got, want) {
			t.Fatalf("executables %q must contain %q", got, want)
		}
	}
	if res.Data[DataKeyValue] != float64(3) {
		t.Fatalf("want all three counted, got %v", res.Data[DataKeyValue])
	}
}

// The count is graphed so a slow leak shows as a rising series; process_count
// moves with legitimate load too, so this is the series that says "unexplained".
func TestStraysCheckPublishesAGraphableCount(t *testing.T) {
	declared := GraphMetrics(CheckTypeStrays)
	if len(declared) != 1 || declared[0].Key != DataKeyCount {
		t.Fatalf("graph metrics = %+v, want one on %q", declared, DataKeyCount)
	}
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, nil, straysDeps(stray(1, "/usr/bin/a")))
	res := c.Run(context.Background())
	if _, ok := res.Data[declared[0].Key]; !ok {
		t.Fatalf("result data %v lacks the graphed key %q", res.Data, declared[0].Key)
	}
}

// `max` raises the failing bound without inverting polarity: OK still means
// healthy, so `failed:` keeps its meaning in a rule.
func TestStraysCheckMaxRaisesTheFailingBound(t *testing.T) {
	strays := []process.Process{stray(1, "/usr/bin/a"), stray(2, "/usr/bin/b"), stray(3, "/usr/bin/c")}
	for _, tc := range []struct {
		max    int
		wantOK bool
	}{{max: 5, wantOK: true}, {max: 3, wantOK: true}, {max: 2, wantOK: false}} {
		c, warn := buildStraysCheck(base{name: straysCheckTestName}, map[string]any{CheckKeyMax: tc.max}, straysDeps(strays...))
		if warn != "" {
			t.Fatalf("max %d: unexpected warning %s", tc.max, warn)
		}
		res := c.Run(context.Background())
		if res.OK != tc.wantOK {
			t.Fatalf("max %d: OK = %v (%s), want %v", tc.max, res.OK, res.Message, tc.wantOK)
		}
		if got := res.Data[DataKeyThreshold]; got != float64(tc.max) {
			t.Fatalf("max %d: threshold = %v, want it published for ${check.threshold}", tc.max, got)
		}
	}
}

func TestStraysCheckRejectsInvalidBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"negative max", map[string]any{CheckKeyMax: -1}, "max must be a non-negative integer"},
		{"zero increase", map[string]any{CheckKeyMaxIncrease: 0, CheckKeyWithin: "5m"}, "max_increase must be a positive integer"},
		{"increase without window", map[string]any{CheckKeyMaxIncrease: 3}, "requires within"},
		{"window without increase", map[string]any{CheckKeyWithin: "5m"}, "within requires max_increase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, warn := buildStraysCheck(base{name: straysCheckTestName}, tc.entry, straysDeps()); !strings.Contains(warn, tc.want) {
				t.Fatalf("warning = %q, want it to contain %q", warn, tc.want)
			}
		})
	}
}

// Growth is measured against the oldest sample still inside the window, so a rise
// that settles stops failing once the window moves past it — and a count that falls
// never reports a negative growth.
func TestStraysCheckMaxIncreaseUsesASlidingWindow(t *testing.T) {
	var strays []process.Process
	now := time.Unix(1_700_000_000, 0)
	c, warn := buildStraysCheck(
		base{name: straysCheckTestName},
		map[string]any{CheckKeyMaxIncrease: 2, CheckKeyWithin: "10m"},
		Deps{Strays: func() []process.Process { return strays }},
	)
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	check, ok := c.(straysCheck)
	if !ok {
		t.Fatalf("want a straysCheck, got %T", c)
	}
	check.clock = func() time.Time { return now }

	at := func(offset time.Duration, count int) Result {
		now = time.Unix(1_700_000_000, 0).Add(offset)
		strays = nil
		for i := range count {
			strays = append(strays, stray(1000+i, "/usr/bin/leak"))
		}
		return check.Run(context.Background())
	}

	if res := at(0, 1); !res.OK {
		t.Fatalf("first sample must be a baseline, got %+v", res)
	}
	if res := at(time.Minute, 3); !res.OK {
		t.Fatalf("growth of 2 is within max_increase 2, got %s", res.Message)
	}
	res := at(2*time.Minute, 6)
	if res.OK {
		t.Fatalf("growth of 5 must exceed max_increase 2, got %s", res.Message)
	}
	if got := res.Data[DataKeyGrowthCount]; got != 5 {
		t.Fatalf("growth = %v, want 5", got)
	}
	if got := res.Data[DataKeyBaselineCount]; got != 1 {
		t.Fatalf("baseline = %v, want the oldest sample in the window", got)
	}
	// Past the window the baseline moves up to the settled count.
	if res := at(20*time.Minute, 6); !res.OK {
		t.Fatalf("a settled count must stop failing once the window moved, got %s", res.Message)
	}
	// A count that falls is not negative growth.
	res = at(21*time.Minute, 2)
	if !res.OK {
		t.Fatalf("a falling count must not fail a growth bound, got %s", res.Message)
	}
	if got := res.Data[DataKeyGrowthCount]; got != 0 {
		t.Fatalf("growth = %v, want it clamped to 0", got)
	}
	// The baseline is still the count this window started from, even though the
	// published growth was clamped: the two are derived from one raw rise.
	if got := res.Data[DataKeyBaselineCount]; got != 6 {
		t.Fatalf("baseline = %v, want the pre-fall count 6", got)
	}
}

func TestStraysCheckNeedsDiscovery(t *testing.T) {
	if _, warn := buildStraysCheck(base{name: straysCheckTestName}, nil, Deps{}); warn == "" {
		t.Fatal("want a warning when process discovery is unavailable")
	}
}
