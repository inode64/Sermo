package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
)

func TestStaggerOffsetSpreadsAcrossInterval(t *testing.T) {
	const interval = 100 * time.Millisecond
	// 4 targets fan out evenly: 0, 25, 50, 75ms — first immediate, none at a full
	// interval (so nobody collides with the next tick).
	want := []time.Duration{0, 25 * time.Millisecond, 50 * time.Millisecond, 75 * time.Millisecond}
	for i, w := range want {
		if got := staggerOffset(i, len(want), interval); got != w {
			t.Fatalf("staggerOffset(%d,4) = %v, want %v", i, got, w)
		}
	}
	// Degenerate cases stay at 0 (single target starts immediately; no division by zero).
	if staggerOffset(0, 1, interval) != 0 || staggerOffset(0, 0, interval) != 0 {
		t.Fatal("single/zero target must start immediately")
	}
}

// TestSchedulerGateWaitsForFirstCycles checks that with gateReady=true the daemon
// stays "starting" until every target (here two watches) has run its first cycle.
func TestSchedulerGateWaitsForFirstCycles(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendSystemd), 0, 0)
	settling := NewSettling(ready)
	settling.Reset([]string{SettlingWatchKey("a"), SettlingWatchKey("b")})
	var ran int32
	mkWatch := func(name string) *Watch {
		return &Watch{
			Name:     name,
			Settling: settling,
			Interval: time.Second, // slow; only the staggered first cycle runs in-window
			Check: checkFunc(func(context.Context) checks.Result {
				atomic.AddInt32(&ran, 1)
				return checks.Result{OK: true}
			}),
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 20 * time.Millisecond}.Run(ctx, nil, []*Watch{mkWatch("a"), mkWatch("b")}, nil, ready, false, true)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if atomic.LoadInt32(&ran) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("watches did not run their first cycle (ran=%d)", atomic.LoadInt32(&ran))
		case <-time.After(2 * time.Millisecond):
		}
	}
	// Give markFirstCycle a beat to flip readiness after the last RunCycle returns.
	for i := 0; i < 100; i++ {
		if ready.Report(context.Background()).Ready {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != TargetStateOK {
		t.Fatalf("gate should be ready after both first cycles: %+v", rep)
	}
	cancel()
	<-done
}

// checkFunc adapts a function to checks.Check for tests.
type checkFunc func(context.Context) checks.Result

func (f checkFunc) Name() string                          { return "fn" }
func (f checkFunc) Run(ctx context.Context) checks.Result { return f(ctx) }

func TestSchedulerRunsCyclesAndShutsDown(t *testing.T) {
	var a, b int32
	counter := func(n *int32) func(context.Context, checks.Deps) map[string]checks.Result {
		return func(context.Context, checks.Deps) map[string]checks.Result {
			atomic.AddInt32(n, 1)
			return nil
		}
	}
	workers := []*Worker{
		{Service: "a", Checks: counter(&a)},
		{Service: "b", Checks: counter(&b)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 15 * time.Millisecond}.Run(ctx, workers, nil, nil, nil, true, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not return after context cancellation")
	}

	if atomic.LoadInt32(&a) < 2 || atomic.LoadInt32(&b) < 2 {
		t.Fatalf("workers did not cycle repeatedly: a=%d b=%d", a, b)
	}
}

func TestSchedulerHonorsPerWorkerInterval(t *testing.T) {
	var fast, slow int32
	counter := func(n *int32) func(context.Context, checks.Deps) map[string]checks.Result {
		return func(context.Context, checks.Deps) map[string]checks.Result {
			atomic.AddInt32(n, 1)
			return nil
		}
	}
	workers := []*Worker{
		{Service: "fast", Interval: 10 * time.Millisecond, Checks: counter(&fast)},
		{Service: "slow", Checks: counter(&slow)}, // no override: uses the global interval
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 100 * time.Millisecond}.Run(ctx, workers, nil, nil, nil, true, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not return after context cancellation")
	}

	// The fast worker (10ms) must cycle several times more than the slow one,
	// which falls back to the 100ms global interval.
	if f, s := atomic.LoadInt32(&fast), atomic.LoadInt32(&slow); f <= s {
		t.Fatalf("per-worker interval ignored: fast=%d slow=%d (want fast > slow)", f, s)
	}
}

func TestSchedulerStartupDelayHoldsBeforeFirstCycle(t *testing.T) {
	var n int32
	workers := []*Worker{
		{Service: "a", Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			atomic.AddInt32(&n, 1)
			return nil
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 10 * time.Millisecond, StartupDelay: 60 * time.Millisecond}.Run(ctx, workers, nil, nil, nil, true, false)
		close(done)
	}()

	// During the startup delay no cycle must have run yet.
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&n); got != 0 {
		t.Fatalf("worker cycled during startup delay: got %d cycles, want 0", got)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not return after context cancellation")
	}

	if atomic.LoadInt32(&n) < 1 {
		t.Fatalf("worker never cycled after startup delay: got %d", n)
	}
}

func TestSchedulerStartupDelayInterruptedByShutdown(t *testing.T) {
	var n int32
	workers := []*Worker{
		{Service: "a", Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			atomic.AddInt32(&n, 1)
			return nil
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 10 * time.Millisecond, StartupDelay: time.Hour}.Run(ctx, workers, nil, nil, nil, true, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not return when cancelled during startup delay")
	}

	if got := atomic.LoadInt32(&n); got != 0 {
		t.Fatalf("worker cycled even though shutdown interrupted the startup delay: got %d", got)
	}
}

func TestSchedulerRunsWatches(t *testing.T) {
	var fired int32
	w := &Watch{
		Name:     "storage-root",
		Check:    stubCheck{name: "storage", ok: true},
		Interval: 15 * time.Millisecond,
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
			atomic.AddInt32(&fired, 1)
			return nil
		}),
		Hook: HookSpec{Command: []string{"/bin/true"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 15 * time.Millisecond}.Run(ctx, nil, []*Watch{w}, nil, nil, true, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not return")
	}
	if atomic.LoadInt32(&fired) < 2 {
		t.Fatalf("watch did not cycle repeatedly: %d", fired)
	}
}

func TestGateOperateSerializesAcrossWorkers(t *testing.T) {
	gate := NewOpGate(1, "")

	var mu sync.Mutex
	var inFlight, maxInFlight int
	body := func(ctx context.Context, _ string) operation.Result {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return operation.Result{Status: operation.ResultOK}
	}

	workers := make([]*Worker, 4)
	for i := range workers {
		w := &Worker{Service: "w", Operate: body}
		gateOperate(w, gate)
		workers[i] = w
	}

	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			w.Operate(context.Background(), "restart")
		}(w)
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Fatalf("max concurrent operations = %d, want 1 (global semaphore)", maxInFlight)
	}
}

func TestGateOperateReturnsOnShutdown(t *testing.T) {
	gate := NewOpGate(1, "")
	gate.mem <- struct{}{} // pre-fill: no slot available

	w := &Worker{Service: "w", Operate: func(context.Context, string) operation.Result {
		t.Fatal("inner Operate must not run when no slot and ctx is cancelled")
		return operation.Result{}
	}}
	gateOperate(w, gate)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := w.Operate(ctx, "restart")
	if res.Status != operation.ResultFailed || res.Message != "shutting down" {
		t.Fatalf("result = %+v, want failed/shutting down", res)
	}
}

// fakeEnvRunnerForScheduler verifies custom runner injection from scheduler -> watch -> hook.
type fakeEnvRunnerForScheduler struct {
	calls []struct {
		env  []string
		name string
		args []string
	}
}

func (f *fakeEnvRunnerForScheduler) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Result{}, nil
}
func (f *fakeEnvRunnerForScheduler) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	f.calls = append(f.calls, struct {
		env  []string
		name string
		args []string
	}{env, name, args})
	return execx.Result{ExitCode: 0}, nil
}

func TestSchedulerRunsWatchWithCustomInjectedRunnerVerifiesEnv(t *testing.T) {
	fake := &fakeEnvRunnerForScheduler{}
	w := &Watch{
		Name:       "storage-root",
		Check:      stubCheck{name: "storage", ok: true},
		Interval:   10 * time.Millisecond,
		Runner:     OSHookRunner{Runner: fake},
		Hook:       HookSpec{Command: []string{"/bin/custom-hook", "--alert"}, Timeout: 5 * time.Second},
		FireOnFail: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 10 * time.Millisecond}.Run(ctx, nil, []*Watch{w}, nil, nil, true, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduler did not stop in time")
	}

	if len(fake.calls) == 0 {
		t.Fatal("expected at least one call to custom execx runner from watch hook")
	}
	call := fake.calls[0]
	if call.name != "/bin/custom-hook" || len(call.args) != 1 || call.args[0] != "--alert" {
		t.Fatalf("bad argv: %s %v", call.name, call.args)
	}
	// Verify specific env from the stub check data
	found := false
	for _, e := range call.env {
		if e == "SERMO_WATCH=storage-root" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("custom runner did not receive expected SERMO_WATCH env: %v", call.env)
	}
}

func TestSchedulerGateCompletesWithInactiveWorker(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	settling := NewSettling(ready)
	settling.Reset([]string{SettlingServiceKey("web")})

	var checksRan int32
	w := &Worker{
		Service:  "web",
		Settling: settling,
		Interval: time.Second,
		CheckDeps: checks.Deps{
			Status: func(context.Context) (servicemgr.Status, error) {
				return servicemgr.StatusInactive, nil
			},
		},
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			atomic.AddInt32(&checksRan, 1)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Scheduler{Interval: 20 * time.Millisecond}.Run(ctx, []*Worker{w}, nil, nil, ready, false, true)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if ready.Report(context.Background()).Ready {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("gate never opened for inactive worker (checks ran %d times)", atomic.LoadInt32(&checksRan))
		case <-time.After(5 * time.Millisecond):
		}
	}
	if atomic.LoadInt32(&checksRan) != 0 {
		t.Fatalf("inactive worker ran checks %d times during observe-only, want 0", checksRan)
	}
	cancel()
	<-done
}
