package checks

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/utmp"
)

func TestRecoverOrphanSSHSession(t *testing.T) {
	const leaderPID, peerPID, shellPID = 301, 302, 303
	for _, tc := range []struct {
		name   string
		mutate func(map[int]process.Identity, *utmp.Session, map[int]string)
		want   bool
	}{
		{name: "surviving session", want: true},
		{name: "verified deleted executable", want: true, mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.ExePrev = id.Exe
			id.Exe = ""
			id.ExeOK = false
			id.ExeFile = process.ExecutableFile{Device: 1, Inode: 99}
			p[leaderPID] = id
		}},
		{name: "unverified deleted executable", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.ExePrev = id.Exe
			id.Exe = ""
			id.ExeOK = false
			p[leaderPID] = id
		}},
		{name: "spoofed basename", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.Exe = "/tmp/sshd-session"
			p[leaderPID] = id
		}},
		{name: "wrong real UID", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.UID = 999
			p[leaderPID] = id
		}},
		{name: "not reparented", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.PPID = 42
			p[leaderPID] = id
		}},
		{name: "reused leader", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.StartTicks = 900
			p[leaderPID] = id
		}},
		{name: "no generation", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.StartTicksOK = false
			p[leaderPID] = id
		}},
		{name: "zombie", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.State = process.ProcStateZombie
			p[leaderPID] = id
		}},
		{name: "ancestry missing", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) { delete(p, peerPID) }},
		{name: "ancestry cycle", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[peerPID]
			id.PPID = shellPID
			p[peerPID] = id
		}},
		{name: "no listener", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) { delete(p, sshdPID) }},
		{name: "local terminal", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.Host = "" }},
		{name: "console", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.Line = "tty2" }},
		{name: "unknown user", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.User = "unknown" }},
		{name: "utmp wrong leader", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.PID = 42 }},
		{name: "unreadable cgroup", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) { delete(g, peerPID) }},
		{name: "another service", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) { g[leaderPID] = "0::/other" }},
		{name: "root cgroup", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) {
			for pid := range g {
				g[pid] = "0::/"
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			session := utmp.Session{PID: leaderPID, User: "0", Line: "pts/0", Host: "192.0.2.1"}
			p := map[int]process.Identity{
				sshdPID:   {PID: sshdPID, PPID: 1, Exe: "/opt/sermo-test/sshd", ExeOK: true},
				leaderPID: {PID: leaderPID, PPID: 1, Exe: "/usr/lib64/misc/sshd-session", ExeOK: true, StartTicks: 100, StartTicksOK: true, TTYOK: true},
				peerPID:   {PID: peerPID, PPID: leaderPID, Exe: "/usr/lib64/misc/sshd-session", ExeOK: true, StartTicks: 101, StartTicksOK: true, TTYOK: true},
				shellPID:  {PID: shellPID, PPID: peerPID, Exe: "/bin/bash", ExeOK: true, StartTicks: 102, StartTicksOK: true, TTY: testTTY, TTYOK: true},
			}
			groups := map[int]string{sshdPID: "0::/openrc.sshd", leaderPID: "0::/openrc.sshd", peerPID: "0::/openrc.sshd", shellPID: "0::/openrc.sshd"}
			if tc.mutate != nil {
				tc.mutate(p, &session, groups)
			}
			filters := mustSSHDFilters(t)
			filters[0].User = "0"
			evidence := sshSessionOwnership{snapshot: p, filters: filters, resolveUser: testSSHLookup().ResolveUser, cgroups: map[int]string{}, readFile: func(path string) ([]byte, error) {
				var pid int
				if _, err := fmt.Sscanf(path, "/proc/%d/cgroup", &pid); err != nil {
					return nil, err
				}
				if data, ok := groups[pid]; ok {
					return []byte(data), nil
				}
				return nil, errors.New("unreadable")
			}}
			sample := SSHSessionSample{Issues: []SSHSessionIssue{{Terminal: session.Line, Remote: session.Host != ""}}}
			evidence.recoverOrphanSessions(&sample, []utmp.Session{session}, testSSHTerminal(now), now)
			if got := len(sample.SSH) == 1; got != tc.want {
				t.Fatalf("sample=%+v, want recovered=%v", sample, tc.want)
			}
			if tc.want {
				got := sample.SSH[0]
				if got.PID != leaderPID || got.StartTicks != 100 || len(sample.Issues) != 0 || got.Cgroup != "0::/openrc.sshd" {
					t.Fatalf("identity=%+v", got)
				}
			} else if len(sample.Issues) != 1 {
				t.Fatal("lost unavailable evidence")
			}
		})
	}
}

func TestLocalGraphicalTerminalIsNotSSH(t *testing.T) {
	now := time.Now()
	snapshot := map[int]process.Identity{123: {PID: 123, PPID: 1, Exe: "/usr/libexec/gdm-wayland-session", ExeOK: true, TTY: testTTY, TTYOK: true}}
	sample := sampleSSHSessions([]utmp.Session{{PID: 123, User: "root", Line: "tty2", Host: ":0"}}, snapshot, testSSHTerminal(now), now, mustSSHDFilters(t), testSSHLookup().ResolveUser, nil)
	if len(sample.Issues) != 0 || sample.Console != 1 {
		t.Fatalf("sample=%+v", sample)
	}
}
