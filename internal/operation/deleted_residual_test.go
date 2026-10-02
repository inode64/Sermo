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

func TestDeletedResidualLifecycle(t *testing.T) {
	t.Parallel()
	for _, backend := range []servicemgr.Backend{servicemgr.BackendSystemd, servicemgr.BackendOpenRC} {
		for _, tc := range []struct {
			name, action                                                        string
			inactive, disabled, unverified, denied, survive, kill, cancel, move bool
			want                                                                ResultStatus
		}{
			{name: "active restart", action: actionRestart, want: ResultOK},
			{name: "inactive restart", action: actionRestart, inactive: true, want: ResultOK},
			{name: "inactive start", action: actionStart, inactive: true, want: ResultOK},
			{name: "stop then start", action: actionStop, want: ResultOK},
			{name: "kill after term", action: actionRestart, kill: true, want: ResultOK},
			{name: "disabled before stop", action: actionRestart, disabled: true, want: ResultBlocked},
			{name: "disabled before start", action: actionStart, inactive: true, disabled: true, want: ResultOrphanProcesses},
			{name: "unverified before stop", action: actionRestart, unverified: true, want: ResultBlocked},
			{name: "unverified before start", action: actionStart, inactive: true, unverified: true, want: ResultOrphanProcesses},
			{name: "signal denied", action: actionRestart, denied: true, want: ResultOrphanProcesses},
			{name: "survives kill", action: actionRestart, survive: true, want: ResultOrphanProcesses},
			{name: "cancel cleanup", action: actionRestart, cancel: true, want: ResultFailed},
			{name: "moves to foreign service", action: actionRestart, move: true, want: ResultOrphanProcesses},
		} {
			t.Run(string(backend)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				h := defaultHarness()
				h.backend, h.mgr.stopped = string(backend), tc.inactive
				const mainExe, helperExe = "/opt/sermo-test/proxy", "/opt/sermo-test/pinger"
				reader := &countingPIDReader{ids: map[int]process.Identity{
					200: {PID: 200, PPID: 1, UID: 110, StartTicks: 10, StartTicksOK: true, ExePrev: helperExe, ExeFile: process.ExecutableFile{Device: 1, Inode: 20}, Cgroup: "0::/user.slice/session-1.scope"},
				}}
				if tc.unverified {
					id := reader.ids[200]
					id.ExeFile = process.ExecutableFile{}
					reader.ids[200] = id
				}
				selectors := []process.Selector{
					{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: mainExe, User: "proxy"},
					{Name: "pinger", Type: process.SelectorCommandMatch, Exe: helperExe, User: "proxy"},
				}
				resolve := func(name string) (uint32, bool) { return 110, name == "proxy" }
				d := process.Discoverer{Reader: reader, ResolveUser: resolve,
					ProcessOwnership: func(id process.Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, "mysqld") }}
				if backend == servicemgr.BackendSystemd {
					d.BackendPIDs = func() []int {
						if !h.mgr.stopped {
							return []int{100}
						}
						return nil
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				signaler := &reapSignaler{onSignal: func(pid int, sig syscall.Signal) {
					if tc.cancel {
						cancel()
						return
					}
					if tc.move {
						id := reader.ids[pid]
						id.Cgroup = "0::/system.slice/other.service"
						reader.ids[pid] = id
						return
					}
					if !tc.survive && (!tc.kill || sig == syscall.SIGKILL) {
						delete(reader.ids, pid)
					}
				}}
				if tc.denied {
					signaler.err = errors.New("permission denied fixture")
				}
				e := h.engine()
				e.Lifecycle.ProcessMode = config.ServiceProcessResident
				e.KillPolicy = process.EnableAutomaticReaping(process.KillPolicy{Automatic: !tc.disabled, TermTimeout: time.Millisecond, KillTimeout: time.Millisecond}, selectors)
				e.Reaper = process.Reaper{Signaler: signaler, ResolveUser: resolve}
				e.ObserveProcesses = func() (process.Observation, error) {
					if h.mgr.stopped {
						delete(reader.ids, 100)
					} else {
						reader.ids[100] = process.Identity{PID: 100, PPID: 1, UID: 110, StartTicks: 30, StartTicksOK: true, Exe: mainExe, ExeOK: true, Cgroup: "0::/system.slice/mysqld.service"}
					}
					return d.Observe(selectors)
				}
				e.Discover = func() ([]process.Process, error) { out, err := e.ObserveProcesses(); return out.Processes, err }
				e.DiscoverTracked = func(previous []process.Process) ([]process.Process, error) {
					out, err := e.ObserveProcesses()
					return out.RetainSurvivors(previous), err
				}
				res := e.Do(ctx, tc.action)
				if res.Status != tc.want {
					t.Fatalf("result=%+v calls=%v", res, h.mgr.calls)
				}
				if len(h.emitted) != 1 || h.released != 1 {
					t.Fatalf("events=%d releases=%d", len(h.emitted), h.released)
				}
				if tc.want == ResultBlocked && h.mgr.did("stop mysqld") {
					t.Fatal("predictable blocker stopped service")
				}
				if !res.OK() && h.mgr.did("start mysqld") {
					t.Fatal("started alongside a residual")
				}
				if tc.denied && !strings.Contains(res.AuditMessage(), "permission denied fixture") {
					t.Fatalf("signal error lost: %+v", res)
				}
				if res.OK() {
					if len(res.Signals) == 0 || strings.Contains(res.AuditMessage(), "pid=100 SIG") {
						t.Fatalf("wrong signal audit: %+v", res)
					}
					if _, exists := reader.ids[200]; exists {
						t.Fatal("reported success with helper alive")
					}
					if tc.action == actionStop {
						if started := e.Start(t.Context()); !started.OK() {
							t.Fatalf("separate start: %+v", started)
						}
					}
				}
			})
		}
	}
}
