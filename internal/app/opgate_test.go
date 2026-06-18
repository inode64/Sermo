package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"sermo/internal/operation"
)

func TestOpGateRunSerializes(t *testing.T) {
	gate := NewOpGate(1, "")

	var mu sync.Mutex
	var inFlight, maxInFlight int
	fn := func(context.Context) operation.Result {
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

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate.Run(context.Background(), "web", "restart", fn)
		}()
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Fatalf("max concurrent operations = %d, want 1", maxInFlight)
	}
}

func TestOpGateUsage(t *testing.T) {
	gate := NewOpGate(2, "")

	inUse, total := gate.Usage()
	if inUse != 0 || total != 2 {
		t.Fatalf("idle Usage = (%d, %d), want (0, 2)", inUse, total)
	}

	done := make(chan struct{})
	go func() {
		gate.Run(context.Background(), "web", "restart", func(context.Context) operation.Result {
			<-done
			return operation.Result{Status: operation.ResultOK}
		})
	}()
	deadline := time.After(time.Second)
	for {
		inUse, total = gate.Usage()
		if inUse == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Usage never showed slot held: (%d, %d)", inUse, total)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(done)
}

func TestOpGateAcquireFailureShuttingDown(t *testing.T) {
	gate := NewOpGate(1, "")
	gate.mem <- struct{}{} // slot held

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := gate.Run(ctx, "web", "restart", func(context.Context) operation.Result {
		t.Fatal("inner fn must not run")
		return operation.Result{}
	})
	if res.Status != operation.ResultFailed || res.Message != "shutting down" {
		t.Fatalf("result = %+v, want failed/shutting down on cancel", res)
	}
}

func TestOpGateAcquireFailureSlotsBusyTimeout(t *testing.T) {
	gate := NewOpGate(1, "")
	hold := make(chan struct{})
	go func() {
		gate.Run(context.Background(), "a", "restart", func(context.Context) operation.Result {
			<-hold
			return operation.Result{Status: operation.ResultOK}
		})
	}()
	deadline := time.After(time.Second)
	for {
		if in, _ := gate.Usage(); in == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for slot to be held")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	res := gate.Run(ctx, "b", "restart", func(context.Context) operation.Result {
		t.Fatal("inner fn must not run")
		return operation.Result{}
	})
	close(hold)

	if res.Status != operation.ResultBlocked {
		t.Fatalf("status = %s, want blocked (must not consume remediation cooldown)", res.Status)
	}
	want := "operation slots busy (1/1); operation timeout exceeded"
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
}

func TestOpGateAcquireFailureMessage(t *testing.T) {
	gate := NewOpGate(2, "")
	gate.mem <- struct{}{}
	gate.mem <- struct{}{}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	msg := gate.acquireFailureMessage(ctx)
	if !strings.Contains(msg, "operation slots busy (2/2)") || !strings.Contains(msg, "timeout") {
		t.Fatalf("deadline+busy = %q", msg)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if got := gate.acquireFailureMessage(ctx2); got != "shutting down" {
		t.Fatalf("canceled = %q", got)
	}
}

func TestOpGateNilPassesThrough(t *testing.T) {
	var called bool
	var g *OpGate
	r := g.Run(context.Background(), "web", "restart", func(context.Context) operation.Result {
		called = true
		return operation.Result{Status: operation.ResultOK}
	})
	if !called || !r.OK() {
		t.Fatalf("nil gate should invoke fn directly: called=%v result=%+v", called, r)
	}
}
