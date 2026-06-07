package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
)

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
		Scheduler{Interval: 15 * time.Millisecond}.Run(ctx, workers, nil)
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
		Scheduler{Interval: 100 * time.Millisecond}.Run(ctx, workers, nil)
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
		Scheduler{Interval: 10 * time.Millisecond, StartupDelay: 60 * time.Millisecond}.Run(ctx, workers, nil)
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
		Scheduler{Interval: 10 * time.Millisecond, StartupDelay: time.Hour}.Run(ctx, workers, nil)
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
		Name:     "disk-root",
		Check:    stubCheck{name: "disk", ok: true},
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
		Scheduler{Interval: 15 * time.Millisecond}.Run(ctx, nil, []*Watch{w})
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
	sem := make(chan struct{}, 1) // one global operation slot

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
		gateOperate(w, sem)
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
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // pre-fill: no slot available

	w := &Worker{Service: "w", Operate: func(context.Context, string) operation.Result {
		t.Fatal("inner Operate must not run when no slot and ctx is cancelled")
		return operation.Result{}
	}}
	gateOperate(w, sem)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := w.Operate(ctx, "restart")
	if res.Status != operation.ResultFailed || res.Message != "shutting down" {
		t.Fatalf("result = %+v, want failed/shutting down", res)
	}
}
