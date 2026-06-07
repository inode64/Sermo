package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/diag"
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

func TestWebBackendDiagnosticsSaturatedSlots(t *testing.T) {
	dir := t.TempDir()
	gate := NewOpGate(1, dir)
	h1, err := gate.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer h1.Release()

	b := &WebBackend{
		opGate: gate,
		cfg: &config.Config{
			Global: config.Global{Raw: map[string]any{
				"engine": map[string]any{"interval": "30s"},
			}},
		},
		host: diag.OSHost{},
	}
	findings := b.Diagnostics(context.Background())
	var sat bool
	for _, f := range findings {
		if f.Scope == "operations" && strings.Contains(f.Message, "saturated (1/1 in use)") {
			sat = true
			if f.Level != "warning" {
				t.Fatalf("finding level = %q", f.Level)
			}
		}
	}
	if !sat {
		t.Fatalf("expected saturated slots finding, got %+v", findings)
	}
}

func TestOperationSlotFindings(t *testing.T) {
	if got := operationSlotFindings(0, 2); len(got) != 0 {
		t.Fatalf("idle = %+v", got)
	}
	if got := operationSlotFindings(1, 2); len(got) != 1 || got[0].Level != "info" ||
		got[0].Message != "operation slots 1/2 in use" {
		t.Fatalf("partial = %+v", got)
	}
	if got := operationSlotFindings(2, 2); len(got) != 1 || got[0].Level != "warning" ||
		!strings.Contains(got[0].Message, "saturated") {
		t.Fatalf("saturated = %+v", got)
	}
}

func TestWebBackendDiagnosticsPartialSlots(t *testing.T) {
	gate := NewOpGate(2, "")
	hold := make(chan struct{})
	go func() {
		gate.Run(context.Background(), "web", "restart", func(context.Context) operation.Result {
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
			t.Fatal("timed out waiting for one slot to be held")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	defer close(hold)

	b := &WebBackend{
		opGate: gate,
		cfg: &config.Config{
			Global: config.Global{Raw: map[string]any{
				"engine": map[string]any{"interval": "30s"},
			}},
		},
		host: diag.OSHost{},
	}
	var info bool
	for _, f := range b.Diagnostics(context.Background()) {
		if f.Scope == "operations" && f.Message == "operation slots 1/2 in use" {
			info = true
			if f.Level != "info" {
				t.Fatalf("level = %q", f.Level)
			}
		}
	}
	if !info {
		t.Fatal("expected partial slots info finding")
	}
}