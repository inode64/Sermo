package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/notify"
)

// fakeProcSampler returns a scripted sequence of samples, one per cycle. An
// entry in failCycles (aligned with cycles) marks a cycle whose process list
// could not be read, exercising the transient-failure path.
type fakeProcSampler struct {
	cycles     [][]ProcInfo
	failCycles []bool
	i          int
}

func (f *fakeProcSampler) Sample(ProcMatch) ([]ProcInfo, bool) {
	if f.i >= len(f.cycles) {
		if len(f.cycles) == 0 {
			return nil, true
		}
		return f.cycles[len(f.cycles)-1], true
	}
	idx := f.i
	out := f.cycles[idx]
	f.i++
	if idx < len(f.failCycles) && f.failCycles[idx] {
		return nil, false
	}
	return out, true
}

type procHarness struct {
	fired  []map[string]string
	events []Event
	clock  time.Time
}

func (h *procHarness) watcher(cond procCond, sampler ProcSampler) *procWatcher {
	return &procWatcher{
		name:  "pw",
		match: ProcMatch{Name: "worker"},
		cond:  cond,
		hook:  HookSpec{Command: []string{"/bin/true"}},
		runner: HookRunnerFunc(func(_ context.Context, _ []string, env map[string]string, _ time.Duration) error {
			h.fired = append(h.fired, env)
			return nil
		}),
		emit:    func(e Event) { h.events = append(h.events, e) },
		now:     func() time.Time { return h.clock },
		sampler: sampler,
	}
}

func (h *procHarness) tick(w *procWatcher, advance time.Duration) {
	h.clock = h.clock.Add(advance)
	w.runCycle(context.Background())
}

func TestProcWatchMinAgeEdge(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 42}}, {{PID: 42}}, {{PID: 42}}, {{PID: 42}},
	}}
	w := h.watcher(procCond{minAge: 90 * time.Second}, s)

	h.tick(w, 0)              // first sight, age 0
	h.tick(w, 30*time.Second) // age 30
	if len(h.fired) != 0 {
		t.Fatalf("fired before min age: %d", len(h.fired))
	}
	h.tick(w, 60*time.Second) // age 90 -> crosses, fire once
	h.tick(w, 60*time.Second) // age 150 -> still over, no re-fire (edge)
	if len(h.fired) != 1 {
		t.Fatalf("min-age fired %d times, want 1", len(h.fired))
	}
	if h.fired[0]["SERMO_PID"] != "42" {
		t.Fatalf("missing/var pid: %v", h.fired[0])
	}
}

func TestProcWatchSummaryUsesObservedThresholdValue(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	w := h.watcher(procCond{memOp: ">", memValue: 1000}, &fakeProcSampler{cycles: [][]ProcInfo{{{PID: 42, RSS: 2000}}}})
	w.summary = "worker memory ${value}, limit ${memory.value}"
	w.check = map[string]any{metrics.MetricMemory: map[string]any{checks.CheckKeyValue: 1000}}

	h.tick(w, 0)

	const want = "worker memory 2,000, limit 1,000"
	if len(h.fired) != 1 || h.fired[0][sermoEnvMessage] != want {
		t.Fatalf("hook env = %v, want summary %q", h.fired, want)
	}
}

func TestProcWatchPublishesSnapshot(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{
		{PID: 42, CPUTicks: 20, RSS: 100, IOBytes: 500, HasIO: true},
		{PID: 7, CPUTicks: 30, RSS: 200},
	}}}
	w := h.watcher(procCond{memOp: ">", memValue: 500}, s)
	w.match.User = "apache"
	var got checks.Result
	w.publish = func(watch, checkType string, res checks.Result) {
		if watch != "pw" || checkType != checks.CheckTypeProcess {
			t.Fatalf("publish target = %s/%s", watch, checkType)
		}
		got = res
	}

	h.tick(w, 0)

	if !got.OK || got.Data[watchReadingFieldProcess] != "worker" || got.Data[watchReadingFieldUser] != "apache" {
		t.Fatalf("published process snapshot = %+v", got)
	}
	if got.Data[watchReadingFieldMatches] != 2 || got.Data[checks.DataKeyPIDs] != "7, 42" {
		t.Fatalf("snapshot process list = %+v", got.Data)
	}
	if got.Data[watchReadingFieldRSS] != uint64(300) || got.Data[watchReadingFieldCPUTicks] != uint64(50) || got.Data[metrics.MetricIO] != uint64(500) {
		t.Fatalf("snapshot counters = %+v", got.Data)
	}
}

func TestProcWatchPublishesSampleFailure(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{}}, failCycles: []bool{true}}
	w := h.watcher(procCond{onGone: true}, s)
	var got checks.Result
	w.publish = func(_, _ string, res checks.Result) { got = res }

	h.tick(w, 0)

	if got.OK || got.Message == "" || got.Data[watchReadingFieldProcess] != "worker" {
		t.Fatalf("sample failure snapshot = %+v", got)
	}
}

func TestProcWatchMemoryThreshold(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 7, RSS: 100}}, // below
		{{PID: 7, RSS: 900}}, // crosses
		{{PID: 7, RSS: 950}}, // still over -> no re-fire
		{{PID: 7, RSS: 100}}, // drops -> re-arm
		{{PID: 7, RSS: 900}}, // crosses again -> fire
	}}
	w := h.watcher(procCond{memOp: ">", memValue: 500}, s)
	for range 5 {
		h.tick(w, time.Second)
	}
	if len(h.fired) != 2 {
		t.Fatalf("memory threshold fired %d times, want 2", len(h.fired))
	}
	if h.fired[0]["SERMO_MEMORY"] != "900" {
		t.Fatalf("unexpected memory env: %v", h.fired[0])
	}
}

func TestProcWatchObserveOnlySkipsFire(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 7, RSS: 900}},
		{{PID: 7, RSS: 900}},
	}}
	w := h.watcher(procCond{memOp: ">", memValue: 500}, s)

	w.runCycle(withObserveOnly(context.Background(), true))
	if len(h.fired) != 0 {
		t.Fatalf("observe-only cycle fired %d times, want 0", len(h.fired))
	}
	h.tick(w, time.Second)
	if len(h.fired) != 1 {
		t.Fatalf("second cycle fired %d times, want 1", len(h.fired))
	}
}

func TestProcWatchDryRunSkipsHookAndNotify(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	n := &fakeNotifier{name: "ops"}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 7, RSS: 900}},
	}}
	w := h.watcher(procCond{memOp: ">", memValue: 500}, s)
	w.notifiers = []notify.Notifier{n}
	w.dryRun = true

	h.tick(w, time.Second)

	if len(h.fired) != 0 {
		t.Fatalf("dry-run must not execute hook, fired=%v", h.fired)
	}
	if len(n.msgs) != 0 {
		t.Fatalf("dry-run must not notify, got %d messages", len(n.msgs))
	}
	if len(h.events) != 1 || h.events[0].Kind != eventKindDryRun {
		t.Fatalf("expected one dry-run event, got %+v", h.events)
	}
}

func TestProcWatchCPURateNotReadyFirstCycle(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	// 1 CPU second across 1 wall second; NumCPU may vary, so use a high enough
	// jump that even on many cores it crosses a low threshold. With Δticks=100
	// (1s of CPU) over 1s wall on N cpus => 100/N %. Use threshold > 0 and few
	// cores is not guaranteed, so assert "fires once it has two samples" with a
	// 0-threshold instead of an absolute percentage.
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 9, CPUTicks: 0}},
		{{PID: 9, CPUTicks: 100}},
		{{PID: 9, CPUTicks: 100}}, // no CPU used -> 0% -> below, re-arm
		{{PID: 9, CPUTicks: 300}}, // uses CPU again
	}}
	w := h.watcher(procCond{cpuOp: ">", cpuValue: 0}, s)
	h.tick(w, time.Second) // first sight: no rate -> no fire even though configured
	if len(h.fired) != 0 {
		t.Fatalf("cpu fired on first cycle without a rate: %d", len(h.fired))
	}
	h.tick(w, time.Second) // rate computable, >0 -> fire
	h.tick(w, time.Second) // 0% -> re-arm
	h.tick(w, time.Second) // >0 again -> fire
	if len(h.fired) != 2 {
		t.Fatalf("cpu fired %d times, want 2", len(h.fired))
	}
	if _, ok := h.fired[0]["SERMO_CPU"]; !ok {
		t.Fatalf("SERMO_CPU missing from env: %v", h.fired[0])
	}
}

func TestProcWatchEventPerPID(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 1, RSS: 100}, {PID: 2, RSS: 100}},
		{{PID: 1, RSS: 900}, {PID: 2, RSS: 900}}, // both cross -> 2 fires
	}}
	w := h.watcher(procCond{memOp: ">", memValue: 500}, s)
	h.tick(w, time.Second)
	h.tick(w, time.Second)
	if len(h.fired) != 2 || len(h.events) != 2 {
		t.Fatalf("want one event/hook per pid (2), got %d fires %d events", len(h.fired), len(h.events))
	}
	pids := map[string]bool{h.fired[0]["SERMO_PID"]: true, h.fired[1]["SERMO_PID"]: true}
	if !pids["1"] || !pids["2"] {
		t.Fatalf("expected a fire per pid, got %v", pids)
	}
}

func TestProcWatchEventUsesReadableAge(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{{PID: 42, RSS: 100}}, {{PID: 42, RSS: 100}}}}
	w := h.watcher(procCond{minAge: 25 * time.Hour, memOp: ">", memValue: 50}, s)

	h.tick(w, 0)
	h.tick(w, 25*time.Hour)

	if len(h.events) != 1 || h.events[0].Message != "worker pid 42 matches (age 25h, rss 100 B)" {
		t.Fatalf("process events = %+v", h.events)
	}
}

func TestProcWatchCombinedConditionsAND(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	// memory over threshold throughout, but age only crosses on the 3rd cycle.
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 5, RSS: 900}},
		{{PID: 5, RSS: 900}},
		{{PID: 5, RSS: 900}},
	}}
	w := h.watcher(procCond{minAge: 25 * time.Second, memOp: ">", memValue: 500}, s)
	h.tick(w, 0)              // age 0: memory ok but age not -> no fire
	h.tick(w, 10*time.Second) // age 10: no
	if len(h.fired) != 0 {
		t.Fatalf("fired before both conditions held: %d", len(h.fired))
	}
	h.tick(w, 20*time.Second) // age 30 >=25 AND memory -> fire
	if len(h.fired) != 1 {
		t.Fatalf("combined AND fired %d times, want 1", len(h.fired))
	}
}

func TestProcWatchGoneFires(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sampler    *fakeProcSampler
		ticks      int
		wantFired  int
		wantChange string
	}{
		{
			name: "gone and reappear fire twice",
			sampler: &fakeProcSampler{cycles: [][]ProcInfo{
				{{PID: 11}}, // present, adopt
				{},          // gone -> fire once
				{},          // still gone (state dropped) -> no re-fire
				{{PID: 11}}, // reappears, adopt -> no fire
				{},          // gone again -> fire
			}},
			ticks: 5, wantFired: 2, wantChange: "gone",
		},
		{
			name: "transient read failure does not fire",
			sampler: &fakeProcSampler{
				cycles: [][]ProcInfo{
					{{PID: 11}}, // present, adopt
					{},          // /proc unreadable this cycle -> must NOT fire gone
					{{PID: 11}}, // readable again, still present -> no fire
					{},          // genuinely gone -> fire once
				},
				failCycles: []bool{false, true, false, false},
			},
			ticks: 4, wantFired: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &procHarness{clock: time.Unix(1_000_000, 0)}
			w := h.watcher(procCond{onGone: true}, tc.sampler)
			for range tc.ticks {
				h.tick(w, time.Second)
			}
			if len(h.fired) != tc.wantFired {
				t.Fatalf("gone fired %d times, want %d", len(h.fired), tc.wantFired)
			}
			if tc.wantChange != "" && h.fired[0]["SERMO_CHANGE"] != tc.wantChange {
				t.Fatalf("unexpected gone change: %v", h.fired[0])
			}
			if h.fired[0]["SERMO_PID"] != "11" {
				t.Fatalf("unexpected gone env: %v", h.fired[0])
			}
		})
	}
}

func TestProcWatchGoneOnlyDoesNotFireOnPresence(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 1, RSS: 9_000_000}}, // present with high RSS, but only `gone` is set
		{{PID: 1, RSS: 9_000_000}},
	}}
	w := h.watcher(procCond{onGone: true}, s)
	h.tick(w, time.Second)
	h.tick(w, time.Second)
	if len(h.fired) != 0 {
		t.Fatalf("gone-only watch fired on presence: %d", len(h.fired))
	}
}

func TestProcWatchReusedPIDReArms(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 3, RSS: 900}}, // crosses -> fire
		{},                   // pid gone -> state dropped
		{{PID: 3, RSS: 900}}, // pid reused, still over -> fire again
	}}
	w := h.watcher(procCond{memOp: ">", memValue: 500}, s)
	for range 3 {
		h.tick(w, time.Second)
	}
	if len(h.fired) != 2 {
		t.Fatalf("reused pid fired %d times, want 2 (re-armed after exit)", len(h.fired))
	}
}

// TestProcWatchWithRealOSHookRunner exercises the real OSHookRunner (execx path)
// in a procWatcher using /bin/true.
func TestProcWatchWithRealOSHookRunner(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 42, RSS: 123}},
	}}
	// Use real OSHookRunner instead of harness Func to cover execx-backed default.
	w := &procWatcher{
		name:    "pw-real",
		match:   ProcMatch{Name: "worker"},
		cond:    procCond{memOp: ">", memValue: 100},
		hook:    HookSpec{Command: []string{"/bin/true"}, Timeout: time.Second},
		runner:  OSHookRunner{},
		now:     func() time.Time { return h.clock },
		emit:    func(e Event) { h.events = append(h.events, e) },
		sampler: s,
	}
	h.tick(w, time.Second)
	if len(h.events) != 1 || h.events[0].Kind != eventKindHook {
		t.Fatalf("expected one hook event via real OSHookRunner, got %d events: %v", len(h.events), h.events)
	}
}

// fakeEnvRunnerForProc is a minimal test double for verifying custom runner injection + env in proc context.
type fakeEnvRunnerForProc struct {
	calls []struct {
		env  []string
		name string
		args []string
	}
}

func (f *fakeEnvRunnerForProc) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Result{}, nil
}
func (f *fakeEnvRunnerForProc) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	f.calls = append(f.calls, struct {
		env  []string
		name string
		args []string
	}{env, name, args})
	return execx.Result{ExitCode: 0}, nil
}

func TestProcWatchWithCustomInjectedRunnerVerifiesEnv(t *testing.T) {
	fake := &fakeEnvRunnerForProc{}
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 99, RSS: 1000}},
	}}
	w := &procWatcher{
		name:    "pw-custom",
		match:   ProcMatch{Name: "worker", User: "root"},
		cond:    procCond{memOp: ">", memValue: 500},
		hook:    HookSpec{Command: []string{"/usr/bin/notify", "alert"}, Timeout: 42 * time.Second},
		runner:  OSHookRunner{Runner: fake},
		now:     func() time.Time { return h.clock },
		emit:    func(e Event) {},
		sampler: s,
	}
	h.tick(w, time.Second)

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 execx call, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.name != "/usr/bin/notify" || len(call.args) != 1 || call.args[0] != "alert" {
		t.Fatalf("wrong argv to custom runner: %s %v", call.name, call.args)
	}
	hasMem := false
	hasUser := false
	for _, e := range call.env {
		if e == "SERMO_MEMORY=1000" {
			hasMem = true
		}
		if e == "SERMO_USER=root" {
			hasUser = true
		}
	}
	if !hasMem || !hasUser {
		t.Fatalf("custom runner did not receive expected SERMO_ env from proc data + match: %v", call.env)
	}
}
