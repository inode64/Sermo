package operation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// After a stop request that reported an error, a service with no strict
// selector may be reset only on proof that every earlier generation exited and
// that its backend reports it stopped.
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
		// A native init service with no strict selector offers the same proof once
		// its init reports it stopped: the state from which a later start resets
		// with no proof at all.
		{name: "systemd exited and inactive", backend: servicemgr.BackendSystemd, state: servicemgr.StatusInactive},
		{name: "openrc exited and crashed", backend: servicemgr.BackendOpenRC, state: servicemgr.StatusInactive, states: []servicemgr.Status{servicemgr.StatusFailed}},
		{name: "native init still active", backend: servicemgr.BackendSystemd, state: servicemgr.StatusActive, wantError: true},
		{name: "openrc sees the exit late", backend: servicemgr.BackendOpenRC, state: servicemgr.StatusInactive, states: []servicemgr.Status{servicemgr.StatusActive, servicemgr.StatusActive, servicemgr.StatusFailed}},
		{name: "native init cancel while awaiting", backend: servicemgr.BackendOpenRC, state: servicemgr.StatusActive, cancelWait: true, wantError: true},
		{name: "native init survivor", backend: servicemgr.BackendOpenRC, state: servicemgr.StatusInactive, ids: map[int]process.Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantError: true},
		{name: "native init without previous process", backend: servicemgr.BackendOpenRC, state: servicemgr.StatusInactive, noPrevious: true, wantError: true},
		{name: "native init unknown previous generation", backend: servicemgr.BackendSystemd, state: servicemgr.StatusInactive, noTicks: true, wantError: true},
		{name: "native init strict identity uncertainty", backend: servicemgr.BackendSystemd, state: servicemgr.StatusInactive, strict: true, wantError: true},
		{name: "native init status error", backend: servicemgr.BackendOpenRC, state: servicemgr.StatusInactive, stateError: true, wantError: true},
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

// While the init still reads active, a process that appears is never reset away.
func TestAwaitInitStoppedRefusesAProcessThatAppears(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusActive
	e := h.engine()
	e.Backend = string(servicemgr.BackendOpenRC)
	d := process.Discoverer{Reader: &countingPIDReader{}}
	reads := 0
	e.ObserveTracked = func(previous []process.Process) (process.Observation, error) {
		reads++
		if reads > 1 {
			return process.Observation{Processes: []process.Process{{PID: 200, StartTicks: 30, Source: process.SourceBackend}}}, nil
		}
		return d.ObserveTracked(nil, previous)
	}
	previous := []process.Process{{PID: 100, StartTicks: 10, Source: process.SourceBackend}}
	err := e.resetStopped(t.Context(), true, previous...)
	if !errors.Is(err, errProcessesAppeared) || h.mgr.did("reset mysqld") {
		t.Fatalf("error=%v calls=%v, want the new process to refuse the reset", err, h.mgr.calls)
	}
}

func TestContainerGraceEndsOnlyWithExitAndInactiveProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ids        map[int]process.Identity
		active     bool
		failed     bool
		noSnapshot bool
		noTicks    bool
		stateError bool
		wantWait   bool
	}{
		{name: "exited and inactive"},
		{name: "unrelated PID reuse", ids: map[int]process.Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
		{name: "survivor", ids: map[int]process.Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantWait: true},
		{name: "still active", active: true, wantWait: true},
		{name: "failed after the stop signal", failed: true},
		{name: "missing snapshot", noSnapshot: true, wantWait: true},
		{name: "unknown old generation", noTicks: true, wantWait: true},
		{name: "inspection error", stateError: true},
	} {
		// A native init service without a strict selector has the same evidence
		// and the same rule: only a verified exit plus an inactive unit ends the wait.
		for _, backend := range []servicemgr.Backend{servicemgr.BackendDocker, servicemgr.BackendOpenRC, servicemgr.BackendSystemd} {
			t.Run(tc.name+"/"+string(backend), func(t *testing.T) {
				h := defaultHarness()
				h.mgr.status = servicemgr.StatusInactive
				if tc.active {
					h.mgr.status = servicemgr.StatusActive
				}
				if tc.failed {
					h.mgr.status = servicemgr.StatusFailed
				}
				if tc.stateError {
					h.mgr.statusErr = errors.New("inspect unavailable")
				}
				e := h.engine()
				e.Backend = string(backend)
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
}

// crashedAfterStop reports what OpenRC does when a stop request gives up but
// the daemon exits afterwards: the service still holds its started marker and
// reads crashed until that marker is reset.
// crashedAfterStop reads a unit failed, OpenRC's crashed, once its stop request
// has run. lateReads reads first still answer active, as OpenRC does while the
// daemon it lost is a zombie its parent has not reaped yet.
type crashedAfterStop struct {
	*fakeManager
	lateReads *int
}

func (m crashedAfterStop) Status(ctx context.Context, unit string) (servicemgr.ServiceStatus, error) {
	if !m.stopped && m.did("stop "+unit) && !m.did("start "+unit) {
		if m.lateReads != nil && *m.lateReads > 0 {
			*m.lateReads--
			return servicemgr.ServiceStatus{Status: servicemgr.StatusActive}, nil
		}
		return servicemgr.ServiceStatus{Status: servicemgr.StatusFailed}, nil
	}
	return m.fakeManager.Status(ctx, unit)
}

// A daemon that takes longer to exit than its init script waits makes the stop
// request fail although it stops. Without a strict selector the service still
// restarts in the same operation once every earlier generation is gone, with
// the init's error kept as a warning; a survivor still ends it as an orphan.
// An init that sees the exit a moment late (OpenRC with the killed daemon still
// a zombie) is waited for; one that never sees it still refuses the reset.
func TestRestartCompletesAfterAFailedStopRequestOnceEveryGenerationExited(t *testing.T) {
	for _, tc := range []struct {
		name      string
		survives  bool
		lateReads int
		want      ResultStatus
		wantStart bool
	}{
		{name: "process exits", want: ResultOK, wantStart: true},
		{name: "process survives", survives: true, want: ResultOrphanProcesses},
		{name: "init sees the exit late", lateReads: postflightMaxAttempts - 1, want: ResultOK, wantStart: true},
		{name: "init never sees the exit", lateReads: 100, want: ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.backend = string(servicemgr.BackendOpenRC)
			h.mgr.status = servicemgr.StatusActive
			h.mgr.stopErr = errors.New("start-stop-daemon: 1 process refused to stop")
			old := process.Process{PID: 100, StartTicks: 10, Exe: "/usr/bin/node", ExeOK: true, Source: process.SourceBackend}
			fresh := process.Process{PID: 200, StartTicks: 20, Exe: "/usr/bin/node", ExeOK: true, Source: process.SourceBackend}
			live := func() []process.Process {
				switch {
				case h.mgr.did("start mysqld"):
					return []process.Process{fresh}
				case h.mgr.did("stop mysqld") && !tc.survives:
					return nil
				default:
					return []process.Process{old}
				}
			}
			engine := h.engine()
			engine.Lifecycle.ProcessMode = config.ServiceProcessResident
			lateReads := tc.lateReads
			engine.Manager = crashedAfterStop{fakeManager: h.mgr, lateReads: &lateReads}
			engine.ObserveProcesses = func() (process.Observation, error) {
				return process.Observation{Processes: live()}, nil
			}
			engine.ObserveTracked = func(previous []process.Process) (process.Observation, error) {
				ids := map[int]process.Identity{}
				for _, proc := range live() {
					ids[proc.PID] = process.Identity{PID: proc.PID, StartTicks: proc.StartTicks, StartTicksOK: true}
				}
				out, err := process.Discoverer{Reader: &countingPIDReader{ids: ids}}.ObserveTracked(nil, previous)
				out.Processes = live()
				return out, err
			}
			engine.Discover = func() ([]process.Process, error) { return live(), nil }

			res := engine.Do(t.Context(), actionRestart)
			if res.Status != tc.want {
				t.Fatalf("result = %+v, want %s; calls=%v", res, tc.want, h.mgr.calls)
			}
			if h.mgr.did("start mysqld") != tc.wantStart {
				t.Fatalf("calls = %v, want start %v", h.mgr.calls, tc.wantStart)
			}
			if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "refused to stop") {
				t.Fatalf("warnings = %v, want the stop request error kept", res.Warnings)
			}
			if len(h.emitted) != 1 || h.released != 1 {
				t.Fatalf("events=%d releases=%d, want one audited outcome and the lock released", len(h.emitted), h.released)
			}
		})
	}
}
