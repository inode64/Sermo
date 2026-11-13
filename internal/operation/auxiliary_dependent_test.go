package operation

import (
	"context"
	"errors"
	"slices"
	"testing"

	"sermo/internal/config"
)

type optionalManager struct {
	*fakeManager
	present     bool
	presenceErr error
	cancel      context.CancelFunc
}

func (m *optionalManager) UnitExists(context.Context, string) (bool, error) {
	if m.cancel != nil {
		m.cancel()
		return false, context.Canceled
	}
	return m.present, m.presenceErr
}

func TestNetworkdOptionalDependentLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name        string
		present     bool
		presenceErr error
		startErr    error
		cancel      bool
		wantOK      bool
		wantCalls   []string
	}{
		{name: "present", present: true, wantOK: true, wantCalls: []string{"stop storage", "stop mysqld", "reset mysqld", "start mysqld", "start storage"}},
		{name: "absent", wantOK: true, wantCalls: []string{"stop mysqld", "reset mysqld", "start mysqld"}},
		{name: "query denied", presenceErr: errors.New("permission denied"), wantCalls: []string{"stop mysqld", "reset mysqld", "start mysqld"}},
		{name: "query timeout", presenceErr: context.DeadlineExceeded, wantCalls: []string{"stop mysqld", "reset mysqld", "start mysqld"}},
		{name: "start failed", present: true, startErr: errors.New("failed dependency"), wantCalls: []string{"stop storage", "stop mysqld", "reset mysqld", "start mysqld", "start storage"}},
		{name: "canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.mgr.errOn = map[string]error{"start storage": tc.startErr}
			mgr := &optionalManager{fakeManager: h.mgr, present: tc.present, presenceErr: tc.presenceErr}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				mgr.cancel = cancel
			}
			e := h.engine()
			e.Manager = mgr
			e.Lifecycle.AuxiliaryUnits = []config.AuxiliaryUnit{{Unit: "storage", Optional: true, StartAfterPrimary: true}}
			result := e.Do(ctx, actionRestart)
			if result.OK() != tc.wantOK || !slices.Equal(mgr.calls, tc.wantCalls) {
				t.Fatalf("result=%+v calls=%v, want OK=%v calls=%v", result, mgr.calls, tc.wantOK, tc.wantCalls)
			}
			if h.released != 1 || len(h.emitted) != 1 {
				t.Fatalf("releases=%d audit=%d", h.released, len(h.emitted))
			}
		})
	}
}

func TestNetworkdDependentRestoredAfterReactivation(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.Manager = &optionalManager{fakeManager: h.mgr, present: true}
	e.Lifecycle.AuxiliaryUnits = []config.AuxiliaryUnit{{Unit: "storage", Optional: true, StartAfterPrimary: true}}
	var result Result
	if !e.startService(t.Context(), &result, true) || !slices.Equal(h.mgr.calls, []string{"start storage"}) {
		t.Fatalf("result=%+v calls=%v", result, h.mgr.calls)
	}
}
