package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/operation"
)

func TestWebBackendOperationsReportsSlots(t *testing.T) {
	gate := NewOpGate(2, "")
	b := &WebBackend{opGate: gate}

	if ops := b.Operations(context.Background()); ops.InUse != 0 || ops.Total != 2 {
		t.Fatalf("idle ops = %+v", ops)
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
		if b.Operations(context.Background()).InUse == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("operation slot never reported as held")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(done)
}
