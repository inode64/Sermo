package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/notify"
)

// fakeProcSampler returns a scripted sequence of samples, one per cycle.
type fakeProcSampler struct {
	cycles [][]ProcInfo
	i      int
}

func (f *fakeProcSampler) Sample(ProcMatch) []ProcInfo {
	if f.i >= len(f.cycles) {
		if len(f.cycles) == 0 {
			return nil
		}
		return f.cycles[len(f.cycles)-1]
	}
	out := f.cycles[f.i]
	f.i++
	return out
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

func TestParseProcIO(t *testing.T) {
	tests := []struct {
		name string
		data string
		want uint64
		ok   bool
	}{
		{
			name: "valid",
			data: "rchar: 1\nread_bytes: 10\nwrite_bytes: 25\ncancelled_write_bytes: 0\n",
			want: 35,
			ok:   true,
		},
		{
			name: "missing write bytes",
			data: "read_bytes: 10\n",
			ok:   false,
		},
		{
			name: "malformed read bytes",
			data: "read_bytes: nope\nwrite_bytes: 25\n",
			ok:   false,
		},
		{
			name: "malformed write bytes",
			data: "read_bytes: 10\nwrite_bytes: nope\n",
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcIO(tc.data)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("parseProcIO() = %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
			}
		})
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
	for i := 0; i < 5; i++ {
		h.tick(w, time.Second)
	}
	if len(h.fired) != 2 {
		t.Fatalf("memory threshold fired %d times, want 2", len(h.fired))
	}
	if h.fired[0]["SERMO_MEMORY"] != "900" {
		t.Fatalf("unexpected memory env: %v", h.fired[0])
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
	if len(h.events) != 1 || h.events[0].Kind != "dry-run" {
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

func TestProcWatchGone(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	s := &fakeProcSampler{cycles: [][]ProcInfo{
		{{PID: 11}}, // present, adopt
		{},          // gone -> fire once
		{},          // still gone (state dropped) -> no re-fire
		{{PID: 11}}, // reappears, adopt -> no fire
		{},          // gone again -> fire
	}}
	w := h.watcher(procCond{onGone: true}, s)
	for i := 0; i < 5; i++ {
		h.tick(w, time.Second)
	}
	if len(h.fired) != 2 {
		t.Fatalf("gone fired %d times, want 2", len(h.fired))
	}
	if h.fired[0]["SERMO_CHANGE"] != "gone" || h.fired[0]["SERMO_PID"] != "11" {
		t.Fatalf("unexpected gone env: %v", h.fired[0])
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
	for i := 0; i < 3; i++ {
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
	if len(h.events) != 1 || h.events[0].Kind != "hook" {
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
