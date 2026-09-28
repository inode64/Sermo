package operation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestSystemdReactivationDoesNotHideOldProcessOutsideUnit(t *testing.T) {
	t.Parallel()
	h := defaultHarness()
	e := h.engine()
	old := process.Process{PID: 100, StartTicks: 10, Source: process.SourceBackend}
	d := process.Discoverer{
		Reader: &countingPIDReader{ids: map[int]process.Identity{
			100: {PID: 100, PPID: 1, UID: 1001, Exe: "/opt/mysqld", ExeOK: true, StartTicks: 10, StartTicksOK: true},
			101: {PID: 101, PPID: 1, UID: 1001, Exe: "/opt/mysqld", ExeOK: true, StartTicks: 20, StartTicksOK: true},
		}},
		BackendPIDs: func() []int { return []int{101} },
		ResolveUser: func(string) (uint32, bool) { return 1001, true },
	}
	e.ObserveProcesses = func() (process.Observation, error) {
		return d.Observe([]process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: "/opt/mysqld", User: "mysql"}})
	}
	after, err := e.ObserveProcesses()
	if err != nil || len(after.Processes) != 1 || after.Processes[0].PID != 101 {
		t.Fatalf("fixture must attribute only the new unit process: %+v, %v", after, err)
	}
	accepted, err := e.systemdReactivated(t.Context(), actionRestart, process.Observation{Trusted: true, Processes: []process.Process{old}}, after.Processes)
	if accepted || err == nil {
		t.Fatalf("old process is still alive outside the new unit tree: accepted=%v err=%v", accepted, err)
	}
}

func TestRediscoveryErrorPreventsKillEscalation(t *testing.T) {
	t.Parallel()
	h := defaultHarness()
	proc := process.Process{PID: 100, UID: 1001, Exe: "/opt/mysqld", ExeOK: true}
	h.discoverSteps = [][]process.Process{{proc}, {proc}}
	h.discoverErrs = []error{nil, errors.New("incomplete process table")}
	signaler := &recordingSignaler{}
	h.reaper = process.Reaper{Signaler: signaler, ResolveUser: func(string) (uint32, bool) { return 1001, true }}
	h.killPolicy = process.KillPolicy{ForceKill: true, KillOnlyIf: process.KillSelector{Users: []string{"mysql"}, ExeAny: []string{"/opt/mysqld"}}}
	res := h.restart(t)
	if res.Status != ResultFailed || h.mgr.did("start mysqld") {
		t.Fatalf("result=%+v calls=%v", res, h.mgr.calls)
	}
	for _, call := range signaler.calls {
		if strings.Contains(call, syscall.SIGKILL.String()) {
			t.Fatal("incomplete rediscovery must stop escalation")
		}
	}
}

func TestStopWarningsSurviveLaterFailure(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"wait", "start"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			h := defaultHarness()
			h.mgr.stopErr = errors.New("primary stop error")
			h.mgr.errOn = map[string]error{"stop mysqld.socket": errors.New("auxiliary stop error")}
			h.mgr.startErr = errors.New("start failed")
			e := h.engine()
			e.Lifecycle.AuxiliaryUnits = []string{"mysqld.socket"}
			e.ObserveProcesses = func() (process.Observation, error) { return process.Observation{AbsenceKnown: true}, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "wait" {
				e.KillPolicy.GracefulTimeout = time.Second
				e.Sleep = func(time.Duration) { cancel() }
			}
			res := e.Restart(ctx)
			if res.Status != ResultFailed || len(h.emitted) != 1 {
				t.Fatalf("result=%+v events=%d", res, len(h.emitted))
			}
			for _, message := range []string{"primary stop error", "auxiliary stop error"} {
				if !strings.Contains(strings.Join(res.Warnings, ";"), message) || !strings.Contains(h.emitted[0].Message, message) {
					t.Fatalf("lost %q in result/audit: %+v", message, res)
				}
			}
		})
	}
}

func TestStoppedArtifactWarningSurvivesStartFailure(t *testing.T) {
	t.Parallel()
	h := defaultHarness()
	h.mgr.startErr = errors.New("start failed")
	e := h.engine()
	artifact := filepath.Join(t.TempDir(), "daemon.pid")
	if err := os.WriteFile(artifact, []byte("100"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.StopArtifacts.PidfilePaths = []string{artifact}
	res := e.Restart(t.Context())
	if res.Status != ResultFailed || len(h.emitted) != 1 || !strings.Contains(h.emitted[0].Message, "stale "+artifact) {
		t.Fatalf("result=%+v events=%+v", res, h.emitted)
	}
}

func TestLifecycleRecovery(t *testing.T) {
	t.Parallel()
	live := []process.Process{{PID: 100, StartTicks: 10, Exe: "/opt/apache", ExeOK: true, Source: process.SourceBackend}}
	delegated := []process.Process{{PID: 200, StartTicks: 20, Delegated: true, Source: process.SourceBackend}}
	tests := []struct {
		name                               string
		action                             string
		initial, stopped, started          []process.Process
		initStopped                        bool
		stopErr, startErr, resetErr        bool
		uncertain, badIdentity, resetStuck bool
		replaced                           bool
		want                               ResultStatus
		wantStart, wantWarning             bool
	}{
		{name: "restart", action: actionRestart, initial: live, started: live, want: ResultOK, wantStart: true},
		{name: "stop", action: actionStop, initial: live, want: ResultOK},
		{name: "start stopped", action: actionStart, initStopped: true, started: live, want: ResultOK, wantStart: true},
		{name: "restart inactive with survivors", action: actionRestart, initStopped: true, initial: live, want: ResultOrphanProcesses},
		{name: "restart stale active", action: actionRestart, started: live, want: ResultOK, wantStart: true},
		{name: "start stale active", action: actionStart, started: live, want: ResultOK, wantStart: true},
		{name: "stop reports error but stops", action: actionStop, initial: live, stopErr: true, want: ResultOK, wantWarning: true},
		{name: "restart stop reports error but stops", action: actionRestart, initial: live, started: live, stopErr: true, want: ResultOK, wantStart: true, wantWarning: true},
		{name: "stop fails with survivors", action: actionRestart, initial: live, stopped: live, stopErr: true, want: ResultOrphanProcesses, wantWarning: true},
		{name: "start reports error but starts", action: actionStart, initStopped: true, started: live, startErr: true, want: ResultOK, wantStart: true, wantWarning: true},
		{name: "start fails and stays stopped", action: actionStart, initStopped: true, startErr: true, want: ResultFailed, wantStart: true},
		{name: "successful start without process", action: actionStart, initStopped: true, want: ResultFailed, wantStart: true},
		{name: "start with replaced executable is not healthy", action: actionStart, initStopped: true, started: live, badIdentity: true, replaced: true, want: ResultFailed, wantStart: true},
		{name: "start error with replaced executable is not recovered", action: actionStart, initStopped: true, started: live, badIdentity: true, replaced: true, startErr: true, want: ResultFailed, wantStart: true},
		{name: "reset fails after stop", action: actionRestart, initial: live, resetErr: true, want: ResultFailed},
		{name: "reset does not change init", action: actionRestart, initial: live, resetStuck: true, want: ResultFailed},
		{name: "uncertain absence", action: actionRestart, uncertain: true, want: ResultFailed},
		{name: "untrusted live process", action: actionRestart, initial: live, badIdentity: true, want: ResultBlocked},
		{name: "delegated survives", action: actionRestart, initial: append(slices.Clone(live), delegated...), stopped: delegated, started: append(slices.Clone(live), delegated...), want: ResultOK, wantStart: true},
	}
	for _, backend := range []servicemgr.Backend{servicemgr.BackendOpenRC, servicemgr.BackendSystemd} {
		for _, tt := range tests {
			t.Run(string(backend)+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				h := defaultHarness()
				h.backend = string(backend)
				h.mgr.stopped = tt.initStopped
				h.mgr.stopLeavesActive, h.mgr.resetKeepsState = tt.resetStuck, tt.resetStuck
				if tt.stopErr {
					h.mgr.stopErr = errors.New("stop fixture failed")
				}
				if tt.startErr {
					h.mgr.startErr = errors.New("start fixture failed")
				}
				if tt.resetErr {
					h.mgr.resetErr = errors.New("reset fixture failed")
				}
				engine := h.engine()
				engine.Lifecycle.ProcessMode = config.ServiceProcessResident
				engine.ObserveProcesses = func() (process.Observation, error) {
					procs := tt.initial
					if h.mgr.did("stop mysqld") || h.mgr.did("reset mysqld") {
						procs = tt.stopped
					}
					if h.mgr.did("start mysqld") {
						procs = tt.started
					}
					return process.Observation{Processes: procs, IdentityRequired: true, Trusted: len(nonDelegatedResiduals(procs)) > 0 && !tt.badIdentity, ReplacedExecutable: tt.replaced, AbsenceKnown: !tt.uncertain}, nil
				}
				engine.Discover = func() ([]process.Process, error) { out, err := engine.ObserveProcesses(); return out.Processes, err }
				res := engine.Do(context.Background(), tt.action)
				if res.Status != tt.want {
					t.Fatalf("result = %+v, want %s; calls=%v", res, tt.want, h.mgr.calls)
				}
				if h.mgr.did("start mysqld") != tt.wantStart {
					t.Fatalf("start calls = %v, want start %v", h.mgr.calls, tt.wantStart)
				}
				if (len(res.Warnings) > 0) != tt.wantWarning {
					t.Fatalf("warnings = %v, want warning %v", res.Warnings, tt.wantWarning)
				}
				if h.mgr.did("restart mysqld") {
					t.Fatal("backend restart must never run")
				}
				stops := 0
				for _, call := range h.mgr.calls {
					if call == "stop mysqld" {
						stops++
					}
				}
				if stops > 1 {
					t.Fatalf("reconciliation duplicated stop: %v", h.mgr.calls)
				}
				if len(h.emitted) != 1 || h.released != 1 {
					t.Fatalf("events=%d releases=%d", len(h.emitted), h.released)
				}
			})
		}
	}
}

func TestCancellationDuringStoppedObservationPreventsResetAndStart(t *testing.T) {
	t.Parallel()
	h := defaultHarness()
	engine := h.engine()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	engine.ObserveProcesses = func() (process.Observation, error) {
		if h.mgr.did("stop mysqld") {
			cancel()
		}
		return process.Observation{AbsenceKnown: true}, nil
	}
	res := engine.Restart(ctx)
	if res.Status != ResultFailed || h.mgr.did("reset mysqld") || h.mgr.did("start mysqld") {
		t.Fatalf("result=%+v calls=%v", res, h.mgr.calls)
	}
}

func TestCancellationDuringStartObservationCannotSucceed(t *testing.T) {
	t.Parallel()
	h := defaultHarness()
	h.mgr.stopped = true
	engine := h.engine()
	engine.Lifecycle.ProcessMode = config.ServiceProcessResident
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	engine.ObserveProcesses = func() (process.Observation, error) {
		cancel()
		return process.Observation{Trusted: true, Processes: []process.Process{{PID: 100}}}, nil
	}
	res := engine.Start(ctx)
	if res.Status != ResultFailed || !h.mgr.did("start mysqld") || len(h.emitted) != 1 {
		t.Fatalf("result=%+v calls=%v events=%v", res, h.mgr.calls, h.emitted)
	}
}

func TestStopDoesNotRecoverUnknownOrFailedObservation(t *testing.T) {
	t.Parallel()
	for _, discoveryErr := range []error{nil, errors.New("procfs unavailable")} {
		t.Run(fmtErrorName(discoveryErr), func(t *testing.T) {
			t.Parallel()
			h := defaultHarness()
			h.mgr.stopErr = errors.New("stop failed")
			e := h.engine()
			e.ObserveProcesses = func() (process.Observation, error) { return process.Observation{}, discoveryErr }
			res := e.Stop(context.Background())
			if res.Status != ResultFailed || h.mgr.did("reset mysqld") {
				t.Fatalf("result=%+v calls=%v", res, h.mgr.calls)
			}
		})
	}
}

func fmtErrorName(err error) string {
	if err == nil {
		return "unknown absence"
	}
	return "discovery failure"
}

func TestReconciliationRevalidatesBeforeReset(t *testing.T) {
	t.Parallel()
	h := defaultHarness()
	h.backend = string(servicemgr.BackendOpenRC)
	e := h.engine()
	e.Lifecycle.ProcessMode = config.ServiceProcessResident
	reads := 0
	e.ObserveProcesses = func() (process.Observation, error) {
		reads++
		out := process.Observation{IdentityRequired: true, AbsenceKnown: true}
		if reads > 1 {
			out.Processes = []process.Process{{PID: 100, StartTicks: 20}}
		}
		return out, nil
	}
	res := e.Restart(context.Background())
	if res.Status != ResultFailed || !strings.Contains(res.Message, "appeared") || len(h.mgr.calls) != 0 {
		t.Fatalf("result=%+v calls=%v", res, h.mgr.calls)
	}
}

func TestReplacedGeneration(t *testing.T) {
	t.Parallel()
	old := process.Process{PID: 100, StartTicks: 10, Source: process.SourceBackend}
	newPID := process.Process{PID: 101, StartTicks: 20, Source: process.SourceBackend}
	reusedPID := process.Process{PID: 100, StartTicks: 20, Source: process.SourceBackend}
	for _, tt := range []struct {
		name    string
		after   []process.Process
		want    bool
		wantErr bool
	}{
		{name: "unchanged", after: []process.Process{old}},
		{name: "new PID", after: []process.Process{newPID}, want: true},
		{name: "reused PID", after: []process.Process{reusedPID}, want: true},
		{name: "mixed generations", after: []process.Process{old, newPID}, wantErr: true},
		{name: "missing generation", after: []process.Process{{PID: 101, Source: process.SourceBackend}}, wantErr: true},
		{name: "empty observation", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := replacedGeneration([]process.Process{old}, tt.after)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("replaced = %v, err = %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
