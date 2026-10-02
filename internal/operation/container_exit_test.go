package operation

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestStoppedContainerRequiresGenerationAndBackendProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backend    servicemgr.Backend
		state      servicemgr.Status
		states     []servicemgr.Status
		ids        map[int]process.Identity
		noPrevious bool
		noSnapshot bool
		noTicks    bool
		strict     bool
		readError  bool
		stateError bool
		cancel     bool
		cancelWait bool
		known      bool
		wantError  bool
	}{
		{name: "old generations exited", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive},
		{name: "unrelated PID reuse", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, ids: map[int]process.Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
		{name: "survivor outside attribution", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, ids: map[int]process.Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantError: true},
		{name: "unknown previous generation", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, noTicks: true, wantError: true},
		{name: "missing snapshot", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, noSnapshot: true, wantError: true},
		{name: "no previous process", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, noPrevious: true, wantError: true},
		{name: "strict identity uncertainty", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, strict: true, wantError: true},
		{name: "snapshot error", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, readError: true, wantError: true},
		{name: "status error", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, stateError: true, wantError: true},
		{name: "still active", backend: servicemgr.BackendDocker, state: servicemgr.StatusActive, wantError: true},
		{name: "delayed inactive state", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, states: []servicemgr.Status{servicemgr.StatusActive}},
		{name: "known absence with delayed state", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, states: []servicemgr.Status{servicemgr.StatusActive}, known: true},
		{name: "cancel while awaiting inactive", backend: servicemgr.BackendDocker, state: servicemgr.StatusActive, cancelWait: true, wantError: true},
		{name: "failed backend", backend: servicemgr.BackendDocker, state: servicemgr.StatusFailed, wantError: true},
		{name: "native init still needs selectors", backend: servicemgr.BackendSystemd, state: servicemgr.StatusInactive, wantError: true},
		{name: "parent cancellation", backend: servicemgr.BackendDocker, state: servicemgr.StatusInactive, cancel: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.mgr.status = tc.state
			h.mgr.statusSteps = tc.states
			if tc.stateError {
				h.mgr.statusErr = errors.New("inspect unavailable")
			}
			e := h.engine()
			e.Backend = string(tc.backend)
			d := process.Discoverer{Reader: &countingPIDReader{ids: tc.ids}}
			e.ObserveProcesses = func() (process.Observation, error) { return d.Observe(nil) }
			e.ObserveTracked = func(previous []process.Process) (process.Observation, error) {
				if tc.readError {
					return process.Observation{}, errors.New("snapshot unavailable")
				}
				out, err := d.ObserveTracked(nil, previous)
				out.IdentityRequired = tc.strict
				out.AbsenceKnown = tc.known
				return out, err
			}
			if tc.noSnapshot {
				e.ObserveTracked = nil
			}
			previous := []process.Process{{PID: 100, StartTicks: 10, Source: process.SourceBackend}}
			if tc.noTicks {
				previous[0].StartTicks = 0
			}
			if tc.noPrevious {
				previous = nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelWait {
				e.Sleep = func(time.Duration) { cancel() }
			}
			if tc.cancel {
				cancel()
			}
			err := e.resetStopped(ctx, true, previous...)
			if (err != nil) != tc.wantError || h.mgr.did("reset mysqld") == tc.wantError {
				t.Fatalf("error=%v calls=%v, want error=%v", err, h.mgr.calls, tc.wantError)
			}
		})
	}
}

func TestContainerGraceEndsOnlyWithExitAndInactiveProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ids        map[int]process.Identity
		active     bool
		noSnapshot bool
		noTicks    bool
		stateError bool
		wantWait   bool
	}{
		{name: "exited and inactive"},
		{name: "unrelated PID reuse", ids: map[int]process.Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
		{name: "survivor", ids: map[int]process.Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantWait: true},
		{name: "still active", active: true, wantWait: true},
		{name: "missing snapshot", noSnapshot: true, wantWait: true},
		{name: "unknown old generation", noTicks: true, wantWait: true},
		{name: "inspection error", stateError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.mgr.status = servicemgr.StatusInactive
			if tc.active {
				h.mgr.status = servicemgr.StatusActive
			}
			if tc.stateError {
				h.mgr.statusErr = errors.New("inspect unavailable")
			}
			e := h.engine()
			e.Backend = string(servicemgr.BackendDocker)
			d := process.Discoverer{Reader: &countingPIDReader{ids: tc.ids}}
			e.ObserveProcesses = func() (process.Observation, error) { return d.Observe(nil) }
			if !tc.noSnapshot {
				e.ObserveTracked = func(previous []process.Process) (process.Observation, error) {
					return d.ObserveTracked(nil, previous)
				}
			}
			before := process.Observation{Processes: []process.Process{{PID: 100, StartTicks: 10, Source: process.SourceBackend}}}
			if tc.noTicks {
				before.Processes[0].StartTicks = 0
			}
			var waited time.Duration
			e.Sleep = func(d time.Duration) { waited += d }
			err := e.waitGracefulStop(t.Context(), before, actionStop, time.Second)
			if (err != nil) != tc.stateError || (waited > 0) != tc.wantWait || waited > time.Second {
				t.Fatalf("waited=%v error=%v, want wait=%v error=%v", waited, err, tc.wantWait, tc.stateError)
			}
		})
	}
}
