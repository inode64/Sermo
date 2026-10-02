package operation

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/servicemgr"
)

// activationManager models systemd reactivating a service while its socket
// remains open, then refusing to bind a stopped socket beside that live daemon.
type activationManager struct {
	*fakeManager
	listening bool
	active    bool
	cancel    context.CancelFunc
}

func (m *activationManager) Stop(_ context.Context, unit string) error {
	m.calls = append(m.calls, "stop "+unit)
	if unit == "mysqld.socket" {
		m.listening = false
		if m.cancel != nil {
			m.cancel()
			return context.Canceled
		}
	} else {
		m.active = m.listening
	}
	return nil
}

func (m *activationManager) Start(_ context.Context, unit string) error {
	m.calls = append(m.calls, "start "+unit)
	if unit == "mysqld.socket" {
		if m.active {
			return errors.New("socket service already active, refusing")
		}
		m.listening = true
	} else {
		m.active = true
	}
	return nil
}

func (m *activationManager) Status(context.Context, string) (servicemgr.ServiceStatus, error) {
	status := servicemgr.StatusInactive
	if m.active {
		status = servicemgr.StatusActive
	}
	return servicemgr.ServiceStatus{Status: status}, nil
}

func TestLifecycleDisablesSocketBeforePrimary(t *testing.T) {
	for _, action := range []string{actionStop, actionRestart} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			mgr := &activationManager{fakeManager: h.mgr, listening: true, active: true}
			e := h.engine()
			e.Manager = mgr
			e.Lifecycle.AuxiliaryUnits = []string{"mysqld.socket"}
			var result Result
			if action == actionStop {
				result = e.Stop(t.Context())
			} else {
				result = e.Restart(t.Context())
			}
			wantActive := action == actionRestart
			if !result.OK() || mgr.active != wantActive || mgr.listening != wantActive {
				t.Fatalf("result=%+v active=%v listening=%v calls=%v", result, mgr.active, mgr.listening, mgr.calls)
			}
			if h.released != 1 || len(h.emitted) != 1 {
				t.Fatalf("releases=%d audit events=%d", h.released, len(h.emitted))
			}
		})
	}
}

func TestAuxiliaryStopCancellationPreventsPrimaryStop(t *testing.T) {
	h := defaultHarness()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mgr := &activationManager{fakeManager: h.mgr, listening: true, active: true, cancel: cancel}
	e := h.engine()
	e.Manager = mgr
	e.Lifecycle.AuxiliaryUnits = []string{"mysqld.socket"}
	result := e.Restart(ctx)
	if result.OK() || h.mgr.did("stop mysqld") || h.mgr.did("start mysqld") || !mgr.active {
		t.Fatalf("result=%+v active=%v calls=%v", result, mgr.active, mgr.calls)
	}
	if len(result.Warnings) != 1 || h.released != 1 || len(h.emitted) != 1 {
		t.Fatalf("warnings=%v releases=%d audit events=%d", result.Warnings, h.released, len(h.emitted))
	}
}

func TestOpenRCAuxiliaryRetainsWrapOrder(t *testing.T) {
	h := defaultHarness()
	h.backend = string(servicemgr.BackendOpenRC)
	e := h.engine()
	e.Lifecycle.AuxiliaryUnits = []string{"helper.socket"}
	result := e.Stop(t.Context())
	if !result.OK() || len(h.mgr.calls) < 2 || h.mgr.calls[0] != "stop mysqld" || h.mgr.calls[1] != "stop helper.socket" {
		t.Fatalf("result=%+v calls=%v", result, h.mgr.calls)
	}
}
