package app

import (
	"context"
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