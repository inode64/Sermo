package operation

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestIfRunningInactiveSkipsBeforeHooks(t *testing.T) {
	for _, action := range []string{actionRestart, actionStop, actionReload} {
		for _, status := range []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusFailed, servicemgr.StatusPaused} {
			t.Run(action+"/"+string(status), func(t *testing.T) {
				h := defaultHarness()
				h.mgr.status = status
				e := h.engine()
				e.IfRunning = true
				e.Preflight = func(context.Context) checks.Outcome {
					t.Fatal("preflight ran for stopped service")
					return checks.Outcome{}
				}
				e.Guard = func(context.Context, string) (string, string, error) {
					t.Fatal("guard ran for stopped service")
					return "", "", nil
				}
				e.Discover = func() ([]process.Process, error) { t.Fatal("reconciliation ran for stopped service"); return nil, nil }
				out := e.Do(t.Context(), action)
				if out.Status != ResultSkipped || out.Relaunched || out.RecordsRemediation() || len(h.mgr.calls) != 0 {
					t.Fatalf("result=%+v calls=%v", out, h.mgr.calls)
				}
				if h.released != 1 || len(h.emitted) != 1 || h.emitted[0].Status != ResultSkipped {
					t.Fatalf("release=%d events=%+v", h.released, h.emitted)
				}
			})
		}
	}
}

func TestIfRunningActiveKeepsNormalOperation(t *testing.T) {
	for _, action := range []string{actionRestart, actionStop, actionReload} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.IfRunning = true
			out := e.Do(t.Context(), action)
			if !out.OK() || len(h.mgr.calls) == 0 || h.released != 1 || len(h.emitted) != 1 {
				t.Fatalf("result=%+v calls=%v released=%d emitted=%v", out, h.mgr.calls, h.released, h.emitted)
			}
		})
	}
}

func TestIfRunningRefusesUnknownAndQueryErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status servicemgr.Status
		err    error
		want   ResultStatus
	}{
		{name: "unknown", status: servicemgr.StatusUnknown, want: ResultFailed},
		{name: "query error", status: servicemgr.StatusInactive, err: errors.New("status unavailable"), want: ResultFailed},
		{name: "absent target", err: servicemgr.ErrNotFound, want: ResultSkipped},
		{name: "timeout", err: context.DeadlineExceeded, want: ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.mgr.status = tc.status
			h.mgr.statusErr = tc.err
			e := h.engine()
			e.IfRunning = true
			out := e.Do(t.Context(), actionRestart)
			if out.Status != tc.want || len(h.mgr.calls) != 0 || h.released != 1 || len(h.emitted) != 1 {
				t.Fatalf("result=%+v calls=%v release=%d events=%v", out, h.mgr.calls, h.released, h.emitted)
			}
		})
	}
}

func TestIfRunningResidentObservation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation process.Observation
		err         error
		want        ResultStatus
	}{
		{name: "confirmed absent", observation: process.Observation{AbsenceKnown: true}, want: ResultSkipped},
		{name: "uncertain absence", want: ResultFailed},
		{name: "discovery error", err: errors.New("proc unreadable"), want: ResultFailed},
		{name: "wrong identity", observation: process.Observation{IdentityRequired: true, Processes: []process.Process{{PID: 123}}}, want: ResultBlocked},
		{name: "trusted process", observation: process.Observation{IdentityRequired: true, Trusted: true, Processes: []process.Process{{PID: 123}}}, want: ResultOK},
		{name: "replaced executable", observation: process.Observation{IdentityRequired: true, ReplacedExecutable: true, Processes: []process.Process{{PID: 123}}}, want: ResultOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.IfRunning = true
			e.Lifecycle.ProcessMode = config.ServiceProcessResident
			e.ObserveProcesses = func() (process.Observation, error) {
				if h.mgr.stopped {
					return process.Observation{AbsenceKnown: true}, nil
				}
				return tc.observation, tc.err
			}
			out := e.Do(t.Context(), actionStop)
			if out.Status != tc.want {
				t.Fatalf("result=%+v want=%s", out, tc.want)
			}
			if tc.want != ResultOK && len(h.mgr.calls) != 0 {
				t.Fatalf("unexpected actions: %v", h.mgr.calls)
			}
		})
	}
}

func TestIfRunningRetainsSafetyBarriers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*harness)
		want  ResultStatus
	}{
		{name: "lock", setup: func(h *harness) {
			h.lockErr = &locks.HeldError{Service: "mysql-main", Lock: locks.Lock{Path: "/run/sermo/ops/mysql-main.lock"}}
		}, want: ResultBlocked},
		{name: "guard", setup: func(h *harness) { h.guardRule = "backup"; h.guardReason = "backup running" }, want: ResultBlocked},
		{name: "preflight", setup: func(h *harness) { h.preflight.OK = false }, want: ResultPreflightFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			tc.setup(h)
			e := h.engine()
			e.IfRunning = true
			out := e.Do(t.Context(), actionRestart)
			if out.Status != tc.want || len(h.mgr.calls) != 0 || len(h.emitted) != 1 {
				t.Fatalf("result=%+v calls=%v events=%v", out, h.mgr.calls, h.emitted)
			}
		})
	}
}

func TestIfRunningChecksStateUnderLock(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusActive
	e := h.engine()
	e.IfRunning = true
	e.AcquireLock = func(time.Duration) (func() error, error) {
		h.mgr.status = servicemgr.StatusInactive
		return func() error { h.released++; return nil }, nil
	}
	if out := e.Do(t.Context(), actionRestart); out.Status != ResultSkipped || len(h.mgr.calls) != 0 {
		t.Fatalf("result=%+v calls=%v", out, h.mgr.calls)
	}
}

func TestIfRunningRejectsUnsupportedActionAndMissingManager(t *testing.T) {
	for _, tc := range []struct {
		name, action   string
		missingManager bool
	}{
		{name: "start", action: actionStart}, {name: "no manager", action: actionStop, missingManager: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.IfRunning = true
			if tc.missingManager {
				e.Manager = nil
			}
			out := e.Do(t.Context(), tc.action)
			if out.Status != ResultFailed || len(h.mgr.calls) != 0 || h.released != 1 || len(h.emitted) != 1 {
				t.Fatalf("result=%+v calls=%v", out, h.mgr.calls)
			}
		})
	}
}

func TestIfRunningCancellationCannotSkip(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusInactive
	e := h.engine()
	e.IfRunning = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := e.Do(ctx, actionRestart)
	if out.Status != ResultFailed || len(h.mgr.calls) != 0 || h.released != 1 || len(h.emitted) != 1 {
		t.Fatalf("result=%+v calls=%v", out, h.mgr.calls)
	}
}

func TestIfRunningProcessFreeUsesBackendState(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.IfRunning = true
	e.Lifecycle.ProcessMode = config.ServiceProcessNone
	e.ObserveProcesses = func() (process.Observation, error) {
		t.Fatal("process-free service queried processes")
		return process.Observation{}, nil
	}
	if out := e.Do(t.Context(), actionReload); !out.OK() || !h.mgr.did("reload mysqld") {
		t.Fatalf("result=%+v calls=%v", out, h.mgr.calls)
	}
}
