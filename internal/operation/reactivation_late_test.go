package operation

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

type lateActivationManager struct {
	*fakeManager
	resetStatus servicemgr.Status
}

func (m *lateActivationManager) ResetState(_ context.Context, unit string) error {
	m.calls = append(m.calls, "reset "+unit)
	m.stopped, m.status = false, m.resetStatus
	return m.resetErr
}

func TestLateSystemdReactivation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resetErr   error
		status     servicemgr.Status
		waitForPID bool
		neverPID   bool
		wrongUser  bool
		oldAlive   bool
		cancel     bool
		timeout    bool
		stop       bool
		openrc     bool
		postflight bool
		want       ResultStatus
	}{
		{name: "activating during reset", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, want: ResultOK},
		{name: "PID appears after activating", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, waitForPID: true, want: ResultOK},
		{name: "active during final query", status: servicemgr.StatusActive, want: ResultOK},
		{name: "no replacement", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, neverPID: true, want: ResultFailed},
		{name: "wrong identity", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, wrongUser: true, want: ResultFailed},
		{name: "old generation outside unit", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, oldAlive: true, want: ResultFailed},
		{name: "permission failure is retained", resetErr: errors.New("permission denied"), status: servicemgr.StatusActive, want: ResultFailed},
		{name: "unknown state is retained", status: servicemgr.StatusUnknown, want: ResultFailed},
		{name: "stop requires inactivity", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, stop: true, want: ResultFailed},
		{name: "OpenRC excluded", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, openrc: true, want: ResultFailed},
		{name: "cancelled wait", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, waitForPID: true, cancel: true, want: ResultFailed},
		{name: "operation deadline", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, waitForPID: true, timeout: true, want: ResultFailed},
		{name: "postflight remains required", resetErr: servicemgr.ErrUnitActivating, status: servicemgr.StatusUnknown, postflight: true, want: ResultPostflightFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			if tc.openrc {
				h.backend = string(servicemgr.BackendOpenRC)
			}
			h.mgr.resetErr = tc.resetErr
			h.postflight.OK = !tc.postflight
			mgr := &lateActivationManager{fakeManager: h.mgr, resetStatus: tc.status}
			signaler := &recordingSignaler{}
			h.reaper.Signaler = signaler
			e := h.engine()
			e.Manager = mgr
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			waited := false
			e.Sleep = func(time.Duration) {
				waited = true
				mgr.status = servicemgr.StatusActive
				if tc.cancel {
					cancel()
				}
				if tc.timeout {
					<-ctx.Done()
				}
			}
			old := process.Identity{PID: 100, PPID: 1, UID: 1001, Exe: "/opt/mysqld", ExeOK: true, StartTicks: 10, StartTicksOK: true}
			replacement := old
			replacement.PID, replacement.StartTicks = 101, 20
			if tc.wrongUser {
				replacement.UID = 1002
			}
			e.ObserveProcesses = func() (process.Observation, error) {
				ids, pids := map[int]process.Identity{100: old}, []int{100}
				if mgr.did("stop mysqld") {
					ids, pids = map[int]process.Identity{}, nil
					if mgr.did("reset mysqld") && !tc.neverPID && (!tc.waitForPID || waited) {
						ids, pids = map[int]process.Identity{101: replacement}, []int{101}
						if tc.oldAlive {
							ids[100] = old
						}
					}
				}
				d := process.Discoverer{Reader: &countingPIDReader{ids: ids}, BackendPIDs: func() []int { return pids }, ResolveUser: func(string) (uint32, bool) { return 1001, true }}
				return d.Observe([]process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: "/opt/mysqld", User: "mysql"}})
			}
			action := actionRestart
			if tc.stop {
				action = actionStop
			}
			result := e.Do(ctx, action)
			if result.Status != tc.want {
				t.Fatalf("result=%+v calls=%v, want %s", result, mgr.calls, tc.want)
			}
			if mgr.did("start mysqld") || len(signaler.calls) > 0 {
				t.Fatalf("replacement disturbed: calls=%v signals=%v", mgr.calls, signaler.calls)
			}
			resets := 0
			for _, call := range mgr.calls {
				if call == "reset mysqld" {
					resets++
				}
			}
			if resets != 1 || len(h.emitted) != 1 || h.released != 1 {
				t.Fatalf("resets=%d audit=%d releases=%d", resets, len(h.emitted), h.released)
			}
		})
	}
}
