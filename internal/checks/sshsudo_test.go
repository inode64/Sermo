package checks

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/utmp"
)

func TestResidualSudoSSHSession(t *testing.T) {
	const leaderPID, monitorPID = 301, 302
	for _, tc := range []struct {
		name   string
		mutate func(map[int]process.Identity, *utmp.Session, map[int]string)
		want   bool
	}{
		{name: "remote sudo with replaced workload", want: true},
		{name: "unknown session user", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.User = "unknown" }},
		{name: "root sudo session", want: true, mutate: func(p map[int]process.Identity, s *utmp.Session, _ map[int]string) {
			s.User = "0"
			for _, pid := range []int{leaderPID, monitorPID} {
				id := p[pid]
				id.UID = 0
				p[pid] = id
			}
		}},
		{name: "local sudo", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.Host = "" }},
		{name: "wrong utmp leader", mutate: func(_ map[int]process.Identity, s *utmp.Session, _ map[int]string) { s.PID++ }},
		{name: "wrong executable", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.Exe = "/tmp/sudo"
			p[leaderPID] = id
		}},
		{name: "wrong real user", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.UID = 1000
			p[leaderPID] = id
		}},
		{name: "deleted sudo", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.ExeOK = false
			p[leaderPID] = id
		}},
		{name: "missing start ticks", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.StartTicksOK = false
			p[leaderPID] = id
		}},
		{name: "reused leader PID", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[leaderPID]
			id.StartTicks = 500
			p[leaderPID] = id
		}},
		{name: "unrelated monitor", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[monitorPID]
			id.PPID = 1
			p[monitorPID] = id
		}},
		{name: "untrusted monitor", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[monitorPID]
			id.UID = 1000
			p[monitorPID] = id
		}},
		{name: "wrong monitor terminal", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) {
			id := p[monitorPID]
			id.TTY++
			p[monitorPID] = id
		}},
		{name: "different cgroup", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) { g[leaderPID] = "0::/unrelated\n" }},
		{name: "unreadable cgroup", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) { delete(g, leaderPID) }},
		{name: "root cgroup", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) {
			for pid := range g {
				g[pid] = "0::/\n"
			}
		}},
		{name: "unclean cgroup", mutate: func(_ map[int]process.Identity, _ *utmp.Session, g map[int]string) {
			for pid := range g {
				g[pid] = "0::/ssh/../sshd\n"
			}
		}},
		{name: "missing trusted sshd", mutate: func(p map[int]process.Identity, _ *utmp.Session, _ map[int]string) { delete(p, sshdPID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)
			session := utmp.Session{PID: leaderPID, User: "apache", Line: "pts/0", Host: "192.0.2.1"}
			p := map[int]process.Identity{
				sshdPID:    {PID: sshdPID, PPID: 1, Exe: "/opt/sermo-test/sshd", ExeOK: true},
				leaderPID:  {PID: leaderPID, PPID: 1, UID: 81, Exe: "/usr/bin/sudo", ExeOK: true, TTYOK: true, StartTicks: 100, StartTicksOK: true},
				monitorPID: {PID: monitorPID, PPID: leaderPID, UID: 81, Exe: "/usr/bin/sudo", ExeOK: true, TTY: testTTY, TTYOK: true, StartTicks: 101, StartTicksOK: true},
				303:        {PID: 303, PPID: monitorPID, ExePrev: "/usr/bin/mariadb", TTY: testTTY, TTYOK: true},
			}
			groups := map[int]string{sshdPID: "0::/openrc.sshd\n", leaderPID: "0::/openrc.sshd\n", monitorPID: "0::/openrc.sshd\n"}
			if tc.mutate != nil {
				tc.mutate(p, &session, groups)
			}
			reads := make(map[int]int)
			resolveUser := func(name string) (uint32, bool) {
				if name == "apache" {
					return 81, true
				}
				return testSSHLookup().ResolveUser(name)
			}
			evidence := sshSudoBoundary{snapshot: p, filters: mustSSHDFilters(t), resolveUser: resolveUser, cgroups: make(map[int]string), readFile: func(path string) ([]byte, error) {
				var pid int
				if _, err := fmt.Sscanf(path, "/proc/%d/cgroup", &pid); err != nil {
					return nil, err
				}
				reads[pid]++
				if data, ok := groups[pid]; ok {
					return []byte(data), nil
				}
				return nil, errors.New("unreadable")
			}}
			sample, err := sampleSSHSessions([]utmp.Session{session}, p, testSSHTerminal(now), now, evidence.filters, evidence.resolveUser, evidence.target)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(sample.SSH) == 1; got != tc.want {
				t.Fatalf("sample=%+v, want verified=%v", sample, tc.want)
			}
			if tc.want {
				got := sample.SSH[0]
				if !got.Residual || got.PID != leaderPID || got.StartTicks != 100 || got.User != session.User {
					t.Fatalf("residual=%+v", got)
				}
				if err := sample.VerifySSHSession(got); err != nil {
					t.Fatal(err)
				}
				got.StartTicks++
				if err := sample.VerifySSHSession(got); err == nil || !strings.Contains(err.Error(), "changed") {
					t.Fatalf("reused PID error=%v", err)
				}
			}
			for pid, count := range reads {
				if count != 1 {
					t.Fatalf("PID %d read %d times", pid, count)
				}
			}
		})
	}
}
