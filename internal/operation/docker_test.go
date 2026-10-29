package operation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

type creatingManager struct {
	*fakeManager
	missing   bool
	createErr error
	verifyErr error
	created   bool
}

func (m *creatingManager) Status(ctx context.Context, unit string) (servicemgr.ServiceStatus, error) {
	if m.missing {
		return servicemgr.ServiceStatus{}, servicemgr.ErrNotFound
	}
	return m.fakeManager.Status(ctx, unit)
}

func (m *creatingManager) Create(ctx context.Context) (string, error) {
	m.calls = append(m.calls, "create")
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.createErr != nil {
		return "", m.createErr
	}
	m.missing, m.stopped, m.created = false, true, true
	return "new-container", nil
}

func (m *creatingManager) StartCreated(ctx context.Context, id string) error {
	return m.Start(ctx, id)
}

func (m *creatingManager) VerifyCreated(_ context.Context, id string) error {
	m.calls = append(m.calls, "verify "+id)
	return m.verifyErr
}

func TestDockerRestartStoppedOnlyStarts(t *testing.T) {
	h := defaultHarness()
	h.backend = string(servicemgr.BackendDocker)
	h.mgr.stopped = true
	e := h.engine()
	var guardAction string
	e.Guard = func(_ context.Context, action string) (string, string, error) {
		guardAction = action
		return "", "", nil
	}
	result := e.Do(t.Context(), actionRestart)
	if !result.OK() || result.Action != actionStart || result.RequestedAction != actionRestart || guardAction != actionRestart {
		t.Fatalf("result=%+v guard=%s", result, guardAction)
	}
	if !slices.Equal(h.mgr.calls, []string{"start mysqld"}) {
		t.Fatalf("stopped container was stopped again: %v", h.mgr.calls)
	}
	if len(h.emitted) != 1 || h.released != 1 || !strings.Contains(result.AuditMessage(), "requested restart") {
		t.Fatalf("audit=%+v released=%d", h.emitted, h.released)
	}
}

func TestDockerRecreationThroughOperation(t *testing.T) {
	for _, action := range []string{actionStart, actionRestart} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			h.backend = string(servicemgr.BackendDocker)
			m := &creatingManager{fakeManager: h.mgr, missing: true}
			e := h.engine()
			e.Manager = m
			result := e.Do(t.Context(), action)
			if !result.OK() || result.CreatedContainer != "new-container" || result.Action != actionStart {
				t.Fatalf("result=%+v", result)
			}
			if !slices.Equal(m.calls, []string{"create", "start new-container", "verify new-container"}) {
				t.Fatalf("calls=%v", m.calls)
			}
			if len(h.emitted) != 1 || h.released != 1 || !strings.Contains(result.AuditMessage(), "created container new-container") {
				t.Fatalf("audit=%+v released=%d", h.emitted, h.released)
			}
		})
	}
}

func TestDockerRecreationHonorsSafetyGates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*harness, *creatingManager)
		want    ResultStatus
		creates bool
	}{
		{"guard", func(h *harness, _ *creatingManager) { h.guardRule, h.guardReason = "backup", "running" }, ResultBlocked, false},
		{"guard error", func(h *harness, _ *creatingManager) { h.guardErr = errors.New("unavailable") }, ResultFailed, false},
		{"lock", func(h *harness, _ *creatingManager) { h.lockErr = errors.New("held") }, ResultFailed, false},
		{"preflight", func(h *harness, _ *creatingManager) { h.preflight = checks.Outcome{OK: false} }, ResultPreflightFailed, false},
		{"creation error", func(_ *harness, m *creatingManager) { m.createErr = errors.New("missing data") }, ResultFailed, true},
		{"replacement generation", func(_ *harness, m *creatingManager) { m.verifyErr = errors.New("replaced") }, ResultFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.backend = string(servicemgr.BackendDocker)
			m := &creatingManager{fakeManager: h.mgr, missing: true}
			tc.setup(h, m)
			e := h.engine()
			e.Manager = m
			result := e.Do(t.Context(), actionRestart)
			if result.Status != tc.want || slices.Contains(m.calls, "create") != tc.creates {
				t.Fatalf("result=%+v calls=%v", result, m.calls)
			}
			if m.createErr != nil && slices.Contains(m.calls, "start new-container") {
				t.Fatal("failed creation continued into start")
			}
			if len(h.emitted) != 1 {
				t.Fatalf("events=%+v", h.emitted)
			}
		})
	}
}

func TestDockerUncertainStateNeverStartsOrCreates(t *testing.T) {
	for _, state := range []servicemgr.Status{servicemgr.StatusPaused, servicemgr.StatusFailed, servicemgr.StatusUnknown} {
		t.Run(string(state), func(t *testing.T) {
			h := defaultHarness()
			h.backend = string(servicemgr.BackendDocker)
			h.mgr.status = state
			result := h.action(t, actionRestart)
			if result.Status != ResultBlocked || len(h.mgr.calls) != 0 {
				t.Fatalf("result=%+v calls=%v", result, h.mgr.calls)
			}
		})
	}
	h := defaultHarness()
	h.backend = string(servicemgr.BackendDocker)
	h.mgr.statusErr = errors.New("Docker is offline")
	if result := h.action(t, actionRestart); result.Status != ResultFailed || len(h.mgr.calls) != 0 {
		t.Fatalf("result=%+v calls=%v", result, h.mgr.calls)
	}
}

func TestDockerRepairDoesNotCreateBeforeCheckingRepairSupport(t *testing.T) {
	h := defaultHarness()
	h.backend = string(servicemgr.BackendDocker)
	m := &creatingManager{fakeManager: h.mgr, missing: true}
	e := h.engine()
	e.Manager = m
	result := e.Do(t.Context(), ActionRepair)
	if result.Status != ResultFailed || result.Action != ActionRepair || len(m.calls) != 0 {
		t.Fatalf("unsupported repair created a container: result=%+v calls=%v", result, m.calls)
	}
}
