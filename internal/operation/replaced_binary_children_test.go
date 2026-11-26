package operation

import (
	"cmp"
	"context"
	"slices"
	"syscall"
	"testing"
	"time"

	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// A daemon that forks one worker per session (Exim) is restarted after an
// upgrade replaced its binary, while two sessions still run the deleted file.
// Discovery alone counts them as two daemon instances and refuses them. The stop
// saw them attributed to the service and they are still in its own cgroup, so
// they get the stop policy and the start follows. Without that evidence they
// stay refused, and a process first seen after the stop is never signalled.
func TestRestartTerminatesTrackedReplacedBinaryChildrenInUnitGroup(t *testing.T) {
	const (
		unit = "exim"
		exe  = "/opt/sermo-test/exim"
		uid  = 8
	)
	resolve := func(name string) (uint32, bool) { return uid, name == "mail" }
	for _, backend := range []servicemgr.Backend{servicemgr.BackendOpenRC, servicemgr.BackendSystemd} {
		unitCgroup := "0::/openrc." + unit
		if backend == servicemgr.BackendSystemd {
			unitCgroup = "0::/system.slice/" + unit + ".service"
		}
		deleted := func(pid, ppid int, ticks uint64, cgroup string) process.Identity {
			return process.Identity{PID: pid, PPID: ppid, UID: uid, StartTicks: ticks, StartTicksOK: true, ExePrev: exe,
				ExeFile: process.ExecutableFile{Device: 1, Inode: 77}, Cgroup: cgroup, State: "S"}
		}
		for _, tc := range []struct {
			name         string
			noForceKill  bool
			keepChildren bool
			sessionGroup string
			withLate     bool
			// lastIgnoresTerm makes the second session survive SIGTERM.
			lastIgnoresTerm bool
			want            ResultStatus
			wantSignals     []pidSignal
		}{
			{name: "sessions end on SIGTERM", want: ResultOK,
				wantSignals: []pidSignal{{200, syscall.SIGTERM}, {201, syscall.SIGTERM}}},
			{name: "auto kills a session that ignores SIGTERM", lastIgnoresTerm: true,
				want: ResultOK, wantSignals: []pidSignal{{200, syscall.SIGTERM}, {201, syscall.SIGTERM}, {201, syscall.SIGKILL}}},
			{name: "force_kill false only terminates", noForceKill: true, lastIgnoresTerm: true,
				want: ResultOrphanProcesses, wantSignals: []pidSignal{{200, syscall.SIGTERM}, {201, syscall.SIGTERM}}},
			{name: "sessions outside the unit group stay refused", sessionGroup: "0::/user.slice/session-1.scope", want: ResultOrphanProcesses},
			{name: "a process first seen after the stop is never signalled", withLate: true,
				want: ResultOrphanProcesses, wantSignals: []pidSignal{{200, syscall.SIGTERM}, {201, syscall.SIGTERM}}},
			{name: "keep_children keeps the sessions", keepChildren: true, want: ResultOK},
		} {
			t.Run(string(backend)+"/"+tc.name, func(t *testing.T) {
				sessionGroup := cmp.Or(tc.sessionGroup, unitCgroup)
				dir := t.TempDir()
				locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
				mgr := &fakeManager{status: servicemgr.StatusActive}
				table := &keepChildrenTable{mgr: mgr, daemon: deleted(100, 1, 10, unitCgroup),
					sessions: []process.Identity{deleted(200, 100, 20, sessionGroup), deleted(201, 100, 21, sessionGroup)},
					replacement: process.Identity{PID: 300, PPID: 1, UID: uid, StartTicks: 30, StartTicksOK: true, Exe: exe, ExeOK: true,
						Cgroup: unitCgroup, State: "S"},
					gone: map[int]bool{}}
				if tc.withLate {
					table.late = deleted(250, 1, 25, unitCgroup)
				}
				stopPolicy := map[string]any{
					process.StopPolicyKeyForceKill:    process.StopPolicyForceKillAuto,
					process.StopPolicyKeyKeepChildren: tc.keepChildren,
					process.StopPolicyKeyTermTimeout:  "1s",
					process.StopPolicyKeyKillTimeout:  "1s",
				}
				if tc.noForceKill {
					stopPolicy[process.StopPolicyKeyForceKill] = false
				}
				engine := New(Config{
					Service: unit,
					Unit:    unit,
					Backend: string(backend),
					Tree: map[string]any{
						process.SectionStopPolicy: stopPolicy,
						process.SectionProcesses: map[string]any{
							"main": map[string]any{process.SelectorKeyExe: exe, process.SelectorKeyUser: "mail"},
						},
					},
					Manager:    mgr,
					Locker:     &locker,
					Scanner:    locks.NewScanner(locks.RuntimeLocksDir(dir)),
					Discoverer: process.Discoverer{Reader: table, ResolveUser: resolve, BackendPIDs: table.openRCBackendPIDs},
					Sleep:      func(time.Duration) {},
				})
				signaler := &pidSignaler{onSignal: func(pid int, sig syscall.Signal) {
					if sig == syscall.SIGTERM && tc.lastIgnoresTerm && pid == 201 {
						return
					}
					table.gone[pid] = true
				}}
				engine.Reaper.Signaler = signaler
				engine.Reaper.ResolveUser = resolve

				res := engine.Do(context.Background(), actionRestart)
				if res.Status != tc.want {
					t.Fatalf("status = %q (%s), want %q; signals = %v", res.Status, res.AuditMessage(), tc.want, signaler.calls)
				}
				if !slices.Equal(signaler.calls, tc.wantSignals) {
					t.Fatalf("signals = %v, want %v", signaler.calls, tc.wantSignals)
				}
				if started := mgr.did("start " + unit); started != res.OK() {
					t.Fatalf("started = %v with status %q; calls = %v", started, res.Status, mgr.calls)
				}
			})
		}
	}
}
