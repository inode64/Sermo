package operation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestRestartWaitsForSystemdReplacementWithoutSignallingIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		afterTerm    bool
		neverReady   bool
		oldAlive     bool
		wrongUser    bool
		missingTicks bool
		readError    bool
		statusError  bool
		cancel       bool
		timeout      bool
		postflight   bool
		want         ResultStatus
	}{
		{name: "replacement still starting", want: ResultOK},
		{name: "replacement after residual TERM", afterTerm: true, want: ResultOK},
		{name: "readiness exhausted", neverReady: true, want: ResultFailed},
		{name: "old generation outside unit", oldAlive: true, want: ResultFailed},
		{name: "wrong replacement user", wrongUser: true, want: ResultFailed},
		{name: "unknown replacement generation", missingTicks: true, want: ResultFailed},
		{name: "incomplete snapshot", readError: true, want: ResultFailed},
		{name: "status error during wait", statusError: true, want: ResultFailed},
		{name: "cancelled wait", cancel: true, want: ResultFailed},
		{name: "operation deadline", timeout: true, want: ResultFailed},
		{name: "postflight remains required", postflight: true, want: ResultPostflightFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := defaultHarness()
			h.mgr.stopLeavesActive = true
			h.mgr.statusSteps = []servicemgr.Status{servicemgr.StatusActive, servicemgr.StatusUnknown, servicemgr.StatusUnknown}
			if tc.neverReady {
				h.mgr.status = servicemgr.StatusUnknown
			}
			h.postflight.OK = !tc.postflight
			old := process.Process{PID: 99, StartTicks: 10, UID: 1001, Exe: "/opt/mysqld", ExeOK: true, Source: process.SourceBackend}
			replacement := old
			replacement.PID, replacement.StartTicks = 100, 20
			h.discoverSteps = [][]process.Process{{replacement}}
			if tc.afterTerm {
				h.discoverSteps = [][]process.Process{{old}, {replacement}}
			}
			signaler := &recordingSignaler{}
			h.reaper = process.Reaper{Signaler: signaler, ResolveUser: func(string) (uint32, bool) { return 1001, true }}
			h.killPolicy = process.KillPolicy{
				ForceKill: true, TermTimeout: time.Millisecond,
				KillOnlyIf: process.NewKillSelector([]string{"mysql"}, []string{"/opt/mysqld"}),
			}
			e := h.engine()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var waits atomic.Int64
			e.Sleep = func(time.Duration) {
				waits.Add(1)
				if tc.timeout {
					<-ctx.Done()
				}
				if tc.cancel {
					cancel()
				}
				if tc.statusError {
					h.mgr.statusErr = errors.New("init status unavailable")
				}
			}
			reads := 0
			e.ObserveProcesses = func() (process.Observation, error) {
				reads++
				id := process.Identity{PID: 99, PPID: 1, UID: 1001, Exe: old.Exe, ExeOK: true, StartTicks: 10, StartTicksOK: true}
				ids, pids := map[int]process.Identity{99: id}, []int{99}
				if reads > 1 {
					if tc.readError {
						return process.Observation{}, errors.New("snapshot unavailable")
					}
					id.PID, id.StartTicks = 100, 20
					if tc.wrongUser {
						id.UID = 1002
					}
					if tc.missingTicks {
						id.StartTicks, id.StartTicksOK = 0, false
					}
					ids, pids = map[int]process.Identity{100: id}, []int{100}
					if tc.oldAlive {
						ids[99] = process.Identity{PID: 99, PPID: 1, UID: old.UID, Exe: old.Exe, ExeOK: true, StartTicks: old.StartTicks, StartTicksOK: true}
					}
				}
				d := process.Discoverer{
					Reader: &countingPIDReader{ids: ids}, BackendPIDs: func() []int { return pids },
					ResolveUser: func(string) (uint32, bool) { return 1001, true },
				}
				return d.Observe([]process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: old.Exe, User: "mysql"}})
			}
			res := e.Do(ctx, actionRestart)
			waitCount := waits.Load()
			if res.Status != tc.want || waitCount == 0 {
				t.Fatalf("result=%+v waits=%d, want %s after waiting", res, waitCount, tc.want)
			}
			if h.mgr.did("start mysqld") || h.mgr.did("reset mysqld") {
				t.Fatalf("replacement must not be started or reset: %v", h.mgr.calls)
			}
			wantSignals := 0
			if tc.afterTerm {
				wantSignals = 1
			}
			if len(signaler.calls) != wantSignals || tc.afterTerm && !strings.HasPrefix(signaler.calls[0], "99 ") {
				t.Fatalf("replacement must never be signalled: %v", signaler.calls)
			}
			if tc.want == ResultOK && !strings.Contains(res.Message, "systemd reactivated the same unit") {
				t.Fatalf("missing reactivation outcome: %+v", res)
			}
			if tc.neverReady && waitCount != postflightMaxAttempts-1 {
				t.Fatalf("unbounded readiness wait: %d", waitCount)
			}
			if h.released != 1 || len(h.emitted) != 1 {
				t.Fatalf("releases=%d events=%d", h.released, len(h.emitted))
			}
		})
	}
}
