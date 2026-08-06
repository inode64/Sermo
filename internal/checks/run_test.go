package checks

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

type runConcurrency struct {
	active  atomic.Int64
	peak    atomic.Int64
	start   chan string
	release <-chan struct{}
}

func (c *runConcurrency) enter(name string) {
	active := c.active.Add(1)
	for peak := c.peak.Load(); active > peak && !c.peak.CompareAndSwap(peak, active); peak = c.peak.Load() {
	}
	c.start <- name
}

func (c *runConcurrency) leave() { c.active.Add(-1) }

type blockingRunCheck struct {
	name        string
	concurrency *runConcurrency
}

func (c blockingRunCheck) Name() string { return c.name }
func (c blockingRunCheck) Run(context.Context) Result {
	c.concurrency.enter(c.name)
	defer c.concurrency.leave()
	<-c.concurrency.release
	return Result{Check: c.name, OK: true}
}

func TestRunRespectsParallelLimitAndResultOrder(t *testing.T) {
	const checkCount = 7
	tests := []struct {
		name        string
		maxParallel int
		wantPeak    int64
	}{
		{name: "unbounded", maxParallel: 0, wantPeak: checkCount},
		{name: "single worker", maxParallel: 1, wantPeak: 1},
		{name: "three workers", maxParallel: 3, wantPeak: 3},
		{name: "more workers than checks", maxParallel: checkCount + 2, wantPeak: checkCount},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			concurrency := &runConcurrency{
				start:   make(chan string, checkCount),
				release: release,
			}
			built := make([]Built, checkCount)
			for i := range built {
				name := fmt.Sprintf("check-%d", i)
				built[i] = Built{Check: blockingRunCheck{name: name, concurrency: concurrency}}
			}

			done := make(chan []Result, 1)
			go func() { done <- Run(context.Background(), built, tc.maxParallel) }()
			for range tc.wantPeak {
				<-concurrency.start
			}
			close(release)
			results := <-done

			if peak := concurrency.peak.Load(); peak != tc.wantPeak {
				t.Fatalf("peak concurrency = %d, want %d", peak, tc.wantPeak)
			}
			for i, result := range results {
				want := fmt.Sprintf("check-%d", i)
				if result.Check != want || !result.OK {
					t.Fatalf("result[%d] = %+v, want ordered successful %q", i, result, want)
				}
			}
		})
	}
}

func TestRunBoundedUsesFixedWorkerSet(t *testing.T) {
	const (
		checkCount  = 128
		workerCount = 3
	)
	baseline := runtime.NumGoroutine()
	release := make(chan struct{})
	concurrency := &runConcurrency{
		start:   make(chan string, checkCount),
		release: release,
	}
	built := make([]Built, checkCount)
	for i := range built {
		built[i] = Built{Check: blockingRunCheck{
			name:        fmt.Sprintf("check-%d", i),
			concurrency: concurrency,
		}}
	}

	done := make(chan struct{})
	go func() {
		Run(context.Background(), built, workerCount)
		close(done)
	}()
	for range workerCount {
		<-concurrency.start
	}
	// Give the runner time to create any eager goroutines. A semaphore-based
	// implementation creates one per check here; a worker pool stays near N.
	time.Sleep(20 * time.Millisecond)
	added := runtime.NumGoroutine() - baseline
	if added > workerCount+4 {
		close(release)
		<-done
		t.Fatalf("bounded run added %d goroutines, want at most %d", added, workerCount+4)
	}
	close(release)
	<-done
}

type benchmarkRunCheck struct{ name string }

func (c benchmarkRunCheck) Name() string { return c.name }
func (c benchmarkRunCheck) Run(context.Context) Result {
	return Result{Check: c.name, OK: true}
}

func BenchmarkRunBounded(b *testing.B) {
	checks := make([]Built, 100)
	for i := range checks {
		checks[i] = Built{Check: benchmarkRunCheck{name: fmt.Sprintf("check-%d", i)}}
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		Run(ctx, checks, 4)
	}
}
