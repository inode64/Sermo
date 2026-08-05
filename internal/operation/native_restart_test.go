package operation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func nativeRestartEngine(h *harness) Engine {
	engine := h.engine()
	engine.RestartMode = config.RestartModeNative
	return engine
}

func TestNativeRestartUsesOneBackendAction(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	identityCalls := 0
	engine := nativeRestartEngine(h)
	engine.RestartIdentity = func(context.Context) (bool, string, error) {
		identityCalls++
		return true, "", nil
	}
	res := engine.Restart(context.Background())

	if res.Status != ResultOK || res.Message != "restart ok" {
		t.Fatalf("result = %+v, want restart ok", res)
	}
	if want := []string{"restart mysqld"}; !reflect.DeepEqual(h.mgr.calls, want) {
		t.Fatalf("manager calls = %v, want %v", h.mgr.calls, want)
	}
	if h.discoverCalls != 0 {
		t.Fatalf("residual discovery calls = %d, want 0", h.discoverCalls)
	}
	if identityCalls != 1 {
		t.Fatalf("restart identity calls = %d, want 1", identityCalls)
	}
	if h.released != 1 || len(h.emitted) != 1 {
		t.Fatalf("released = %d, emitted = %d; want one of each", h.released, len(h.emitted))
	}
}

func TestNewWiresNativeRestartPolicy(t *testing.T) {
	t.Parallel()

	engine, mgr := newInvalidTreeEngine(t, "containerd", "containerd", map[string]any{
		config.ServiceKeyRestartPolicy: map[string]any{config.RestartPolicyKeyMode: config.RestartModeNative},
	})
	res := engine.Restart(context.Background())

	if res.Status != ResultOK {
		t.Fatalf("result = %+v, want ok", res)
	}
	if want := []string{"restart containerd"}; !reflect.DeepEqual(mgr.calls, want) {
		t.Fatalf("manager calls = %v, want %v", mgr.calls, want)
	}
}

func TestNewInvalidRestartPolicyBlocksBeforeBackendAction(t *testing.T) {
	t.Parallel()

	engine, mgr := newInvalidTreeEngine(t, "containerd", "containerd", map[string]any{
		config.ServiceKeyRestartPolicy: map[string]any{config.RestartPolicyKeyMode: config.RestartModeNative},
		config.SectionControl:          map[string]any{"type": "docker"},
	})
	res := engine.Restart(context.Background())

	if res.Status != ResultFailed || !strings.Contains(res.Message, "not supported with control") {
		t.Fatalf("result = %+v, want fail-closed config error", res)
	}
	if len(mgr.calls) != 0 {
		t.Fatalf("manager calls = %v, want none", mgr.calls)
	}
}

func TestNativeRestartSafetyBarriers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*harness, *Engine)
		want  ResultStatus
	}{
		{
			name: "operation lock",
			setup: func(h *harness, _ *Engine) {
				h.lockErr = &locks.HeldError{Service: "mysql-main", Lock: locks.Lock{Path: "/run/sermo/ops/mysql-main.lock"}}
			},
			want: ResultBlocked,
		},
		{
			name: "runtime lock",
			setup: func(h *harness, _ *Engine) {
				h.named = []locks.Lock{{Service: "mysql-main", Name: "backup", State: locks.StateActive}}
			},
			want: ResultBlocked,
		},
		{
			name: "preflight",
			setup: func(h *harness, _ *Engine) {
				h.preflight = checks.Outcome{OK: false}
			},
			want: ResultPreflightFailed,
		},
		{
			name: "guard",
			setup: func(h *harness, _ *Engine) {
				h.guardBlocked = true
				h.guardReason = "backup running"
			},
			want: ResultBlocked,
		},
		{
			name: "restart identity",
			setup: func(_ *harness, engine *Engine) {
				engine.RestartIdentity = func(context.Context) (bool, string, error) {
					return false, "identity mismatch", nil
				}
			},
			want: ResultBlocked,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := defaultHarness()
			engine := nativeRestartEngine(h)
			tt.setup(h, &engine)
			res := engine.Restart(context.Background())
			if res.Status != tt.want {
				t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, tt.want)
			}
			if h.mgr.did("restart mysqld") {
				t.Fatalf("native backend restart ran past %s barrier", tt.name)
			}
		})
	}
}

func TestNativeRestartBackendFailureHasNoStagedFallback(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	h.mgr.restartErr = errors.New("backend refused")
	res := nativeRestartEngine(h).Restart(context.Background())

	if res.Status != ResultFailed || res.Message != "restart: backend refused" {
		t.Fatalf("result = %+v, want backend failure", res)
	}
	if want := []string{"restart mysqld"}; !reflect.DeepEqual(h.mgr.calls, want) {
		t.Fatalf("manager calls = %v, want %v", h.mgr.calls, want)
	}
}

func TestNativeRestartFailsWhenServiceIsFailedAfterAction(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	h.mgr.status = servicemgr.StatusFailed
	res := nativeRestartEngine(h).Restart(context.Background())

	if res.Status != ResultFailed || res.Message != "service failed after restart" {
		t.Fatalf("result = %+v, want failed health status", res)
	}
}

func TestNativeRestartPostflightFailureLeavesServiceRunning(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	h.postflight = checks.Outcome{OK: false, Results: []checks.Result{{Check: "http", OK: false}}}
	res := nativeRestartEngine(h).Restart(context.Background())

	if res.Status != ResultPostflightFailed || !h.mgr.did("restart mysqld") {
		t.Fatalf("result = %+v, calls = %v; want postflight failure after restart", res, h.mgr.calls)
	}
	if h.discoverCalls != 0 {
		t.Fatalf("residual discovery calls = %d, want 0", h.discoverCalls)
	}
}

func TestNativeRestartHonorsOperationTimeout(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	h.mgr.restartFunc = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	engine := nativeRestartEngine(h)
	engine.OperationTimeout = 20 * time.Millisecond
	res := engine.Restart(context.Background())

	if res.Status != ResultFailed || res.Message != "operation timed out during restart" {
		t.Fatalf("result = %+v, want restart timeout", res)
	}
	if h.discoverCalls != 0 {
		t.Fatalf("residual discovery calls = %d, want 0", h.discoverCalls)
	}
}

func TestNativeRestartNeverInvokesReaper(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{{PID: 4242}}}
	res := nativeRestartEngine(h).Restart(context.Background())

	if res.Status != ResultOK || h.discoverCalls != 0 {
		t.Fatalf("result = %+v, discovery calls = %d; native restart must bypass residual handling", res, h.discoverCalls)
	}
}

func TestNativeRestartLeavesAuxiliaryInitUnitsActive(t *testing.T) {
	t.Parallel()

	h := defaultHarness()
	engine := nativeRestartEngine(h)
	engine.AlsoUnits = []string{"docker.socket"}
	res := engine.Restart(context.Background())

	if res.Status != ResultOK {
		t.Fatalf("result = %+v, want ok", res)
	}
	if want := []string{"restart mysqld"}; !reflect.DeepEqual(h.mgr.calls, want) {
		t.Fatalf("manager calls = %v, want %v", h.mgr.calls, want)
	}
}
