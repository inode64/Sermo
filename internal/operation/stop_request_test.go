package operation

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// stopRequestManager models Docker waiting for PID 1 while the request context
// expires. The surviving process is independent of the HTTP request lifetime.
type stopRequestManager struct {
	*fakeManager
	request func(context.Context) error
}

func (m *stopRequestManager) Stop(ctx context.Context, unit string) error {
	m.calls = append(m.calls, "stop "+unit)
	return m.request(ctx)
}

func TestDockerStopRequestLeavesResidualBudget(t *testing.T) {
	for _, tc := range []struct {
		name           string
		action         string
		force          bool
		selector       bool
		exits          bool
		discoveryError bool
		globalTimeout  bool
		cancel         bool
		want           ResultStatus
		wantSignals    int
	}{
		{name: "stop escalates after request timeout", action: actionStop, force: true, selector: true, want: ResultOK, wantSignals: 2},
		{name: "restart escalates before start", action: actionRestart, force: true, selector: true, want: ResultOK, wantSignals: 2},
		{name: "disabled escalation leaves orphan", action: actionRestart, selector: true, want: ResultOrphanProcesses},
		{name: "missing selector leaves orphan", action: actionRestart, force: true, want: ResultOrphanProcesses},
		{name: "request timed out but process exited", action: actionRestart, exits: true, want: ResultOK},
		{name: "discovery failure blocks escalation", action: actionRestart, force: true, selector: true, discoveryError: true, want: ResultFailed},
		{name: "global deadline blocks escalation", action: actionRestart, force: true, selector: true, globalTimeout: true, want: ResultFailed},
		{name: "cancellation blocks escalation", action: actionRestart, force: true, selector: true, cancel: true, want: ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.Backend = string(servicemgr.BackendDocker)
			e.Lifecycle.ProcessMode = config.ServiceProcessResident
			e.OperationTimeout = time.Second
			e.KillPolicy = process.KillPolicy{GracefulTimeout: 5 * time.Millisecond, ForceKill: tc.force}
			if tc.selector {
				e.KillPolicy.KillOnlyIf = process.KillSelector{ExeAny: []string{"/opt/test/daemon"}, Users: []string{"root"}}
			}
			if tc.globalTimeout {
				e.KillPolicy.GracefulTimeout = time.Hour
				e.OperationTimeout = 5 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := false
			live := []process.Process{{PID: 100, UID: 0, Exe: "/opt/test/daemon", ExeOK: true, StartTicks: 1}}
			e.Manager = &stopRequestManager{fakeManager: h.mgr, request: func(requestCtx context.Context) error {
				if tc.cancel {
					cancel()
				}
				<-requestCtx.Done()
				finished = true
				if tc.exits {
					live = nil
					h.mgr.stopped = true
				}
				return requestCtx.Err()
			}}
			e.Discover = func() ([]process.Process, error) {
				if finished && tc.discoveryError {
					return nil, errors.New("unreadable process table")
				}
				if h.mgr.did("start mysqld") {
					return []process.Process{{PID: 101, UID: 0, Exe: "/opt/test/daemon", ExeOK: true, StartTicks: 2}}, nil
				}
				return live, nil
			}
			e.ObserveProcesses = func() (process.Observation, error) {
				procs, err := e.Discover()
				return process.Observation{Processes: procs, Trusted: len(procs) > 0, AbsenceKnown: len(procs) == 0}, err
			}
			signaler := &reapSignaler{onSignal: func(_ int, sig syscall.Signal) {
				if sig == syscall.SIGKILL {
					live = nil
					h.mgr.stopped = true
				}
			}}
			e.Reaper = process.Reaper{Signaler: signaler, ResolveUser: reapResolveUser}
			var graceWait time.Duration
			e.Sleep = func(d time.Duration) { graceWait += d }
			result := e.Do(ctx, tc.action)
			if result.Status != tc.want || len(signaler.calls) != tc.wantSignals {
				t.Fatalf("result=%+v signals=%v, want %s and %d signals", result, signaler.calls, tc.want, tc.wantSignals)
			}
			if h.mgr.did("start mysqld") != (tc.action == actionRestart && tc.want == ResultOK) {
				t.Fatalf("unexpected start: calls=%v result=%+v", h.mgr.calls, result)
			}
			if tc.action == actionStop && graceWait != 0 {
				t.Fatalf("request already consumed grace; waited again for %v", graceWait)
			}
			if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "stop command: request stop: context") || len(h.emitted) != 1 || h.released != 1 {
				t.Fatalf("lost warning, audit or lock: result=%+v events=%d releases=%d", result, len(h.emitted), h.released)
			}
		})
	}
}

func TestStopRequestDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend servicemgr.Backend
		grace   time.Duration
		want    time.Duration
	}{
		{name: "docker default", backend: servicemgr.BackendDocker, want: defaultDockerStopTimeout},
		{name: "docker explicit grace", backend: servicemgr.BackendDocker, grace: time.Minute, want: time.Minute},
		{name: "systemd keeps parent", backend: servicemgr.BackendSystemd, grace: time.Minute, want: time.Hour},
		{name: "openrc keeps parent", backend: servicemgr.BackendOpenRC, grace: time.Minute, want: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.Backend = string(tc.backend)
			e.KillPolicy.GracefulTimeout = tc.grace
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			var requestCtx context.Context
			e.Manager = &stopRequestManager{fakeManager: h.mgr, request: func(c context.Context) error {
				requestCtx = c
				deadline, ok := c.Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining > tc.want || remaining < tc.want-time.Second {
					t.Fatalf("request deadline in %v, want %v", remaining, tc.want)
				}
				return nil
			}}
			grace, err := e.stopPrimary(ctx)
			if err != nil || ctx.Err() != nil || grace < 0 {
				t.Fatalf("grace=%v err=%v parent=%v", grace, err, ctx.Err())
			}
			if tc.backend == servicemgr.BackendDocker {
				if requestCtx.Err() != context.Canceled || grace > tc.want || grace < tc.want-time.Second {
					t.Fatalf("request=%v remaining grace=%v", requestCtx.Err(), grace)
				}
			} else if grace != tc.grace || requestCtx != ctx {
				t.Fatalf("native request context or grace changed: %v", grace)
			}
		})
	}
}
