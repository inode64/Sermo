package process

import (
	"os"
	"path/filepath"
	"testing"
)

// The OpenRC RabbitMQ unit after a bash upgrade: the init script leaves
// `sh rabbitmq-server` and `su` above the Erlang VM, os_mon runs its probes
// through `sh -s`, and every process is a cgroup member labelled main. Only the
// broker's own pidfile says beam.smp is the daemon, and it runs the installed
// version, so the replaced shells are auxiliary. Once beam.smp itself is
// replaced, it and anything running the same executable are not.
func TestStaleBinariesMarksLaunchersAndHelpersBesideTheDaemon(t *testing.T) {
	const beam = "/usr/lib64/erlang/erts-14.2.1/bin/beam.smp"
	wrapper := Process{PID: 10, Source: SourceBackend, Role: RoleMain, ExePrev: "/usr/bin/bash"}
	su := Process{PID: 11, PPID: 10, Source: SourceBackend, Role: RoleMain, ExeOK: true, Exe: "/usr/bin/su"}
	helper := Process{PID: 13, PPID: 12, Source: SourceBackend, Role: RoleMain, ExePrev: "/usr/bin/bash"}
	daemon := Process{PID: 12, PPID: 11, Source: SourceBackend, Role: RoleMain, MainPidfile: true, ExeOK: true, Exe: beam}

	d := Discoverer{}
	got := d.StaleBinariesIn([]Process{wrapper, su, daemon, helper}, nil)
	if len(got) != 2 || !got[0].Auxiliary || !got[1].Auxiliary {
		t.Fatalf("stale = %+v, want both shells auxiliary", got)
	}

	daemon.ExeOK, daemon.Exe, daemon.ExePrev = false, "", beam
	worker := Process{PID: 14, PPID: 12, Source: SourceBackend, Role: RoleMain, ExePrev: beam}
	got = d.StaleBinariesIn([]Process{wrapper, su, daemon, helper, worker}, nil)
	want := map[int]bool{10: true, 12: false, 13: true, 14: false}
	if len(got) != len(want) {
		t.Fatalf("stale = %+v, want %d entries", got, len(want))
	}
	for _, s := range got {
		if auxiliary, ok := want[s.PID]; !ok || s.Auxiliary != auxiliary {
			t.Fatalf("pid %d auxiliary = %v, want %v (%+v)", s.PID, s.Auxiliary, auxiliary, got)
		}
	}
}

// Without a main pidfile, or with one whose daemon's executable is unknown,
// nothing tells the daemon from its launcher, so every replaced binary keeps
// the restart it always had.
func TestStaleBinariesWithoutANamedDaemonKeepsEveryRestart(t *testing.T) {
	d := Discoverer{}
	for name, procs := range map[string][]Process{
		"no main pidfile": {
			{PID: 10, Source: SourceBackend, Role: RoleMain, ExePrev: "/usr/bin/bash"},
			{PID: 12, Source: SourceBackend, Role: RoleMain, ExeOK: true, Exe: "/usr/sbin/daemon"},
		},
		"unreadable daemon": {
			{PID: 10, Source: SourceBackend, Role: RoleMain, ExePrev: "/usr/bin/bash"},
			{PID: 12, Source: SourceBackend, Role: RoleMain, MainPidfile: true},
		},
	} {
		got := d.StaleBinariesIn(procs, nil)
		if len(got) != 1 || got[0].Auxiliary {
			t.Fatalf("%s: stale = %+v, want one restart-worthy entry", name, got)
		}
	}
}

// Discovery itself marks the main pidfile's daemon inside a backend-attributed
// unit, so the classification works on what the worker actually passes in.
func TestStaleBinariesClassifiesThroughDiscovery(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "rabbit.pid")
	if err := os.WriteFile(pidfile, []byte("12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			10: {PID: 10, UID: 0, ExePrev: "/usr/bin/bash"},
			11: {PID: 11, PPID: 10, UID: 0, ExeOK: true, Exe: "/usr/bin/su"},
			12: {PID: 12, PPID: 11, UID: 999, ExeOK: true, Exe: "/usr/lib64/erlang/erts-14.2.1/bin/beam.smp"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0, "rabbitmq": 999}),
		BackendPIDs: func() []int { return []int{10, 11, 12} },
	}
	got := d.StaleBinaries([]Selector{{Name: SelectorPidfile, Type: SelectorPidfile, Paths: []string{pidfile}}})
	if len(got) != 1 || got[0].PID != 10 || !got[0].Auxiliary {
		t.Fatalf("stale = %+v, want the launcher shell, auxiliary", got)
	}
}
