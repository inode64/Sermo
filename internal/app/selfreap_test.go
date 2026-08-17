package app

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	"sermo/internal/config"
	"sermo/internal/process"
)

type selfReapSignal struct {
	pid int
	sig syscall.Signal
}

type selfReapSignaler struct {
	calls []selfReapSignal
	err   error
}

func (s *selfReapSignaler) Signal(pid int, sig syscall.Signal) error {
	s.calls = append(s.calls, selfReapSignal{pid: pid, sig: sig})
	return s.err
}

// sermodCgroup models sermod's own service unit control group after a restart that
// left two processes of the previous incarnation behind.
func sermodCgroup(procs string) func(string) ([]byte, error) {
	files := map[string]string{
		"/proc/self/cgroup": "0::/system.slice/sermod.service\n",
		"/sys/fs/cgroup/system.slice/sermod.service/cgroup.procs": procs,
	}
	return func(path string) ([]byte, error) {
		data, ok := files[path]
		if !ok {
			return nil, errors.New("no such file: " + path)
		}
		return []byte(data), nil
	}
}

func namedIdentity(exes map[int]string) func(int) (process.Identity, bool) {
	return func(pid int) (process.Identity, bool) {
		exe, ok := exes[pid]
		if !ok {
			return process.Identity{}, false
		}
		return process.Identity{PID: pid, Exe: exe, ExeOK: true}, true
	}
}

func TestSelfStrayHygieneTerminatesLeftovers(t *testing.T) {
	signaler := &selfReapSignaler{}
	var events []Event
	hygiene := SelfStrayHygiene{
		ReadFile: sermodCgroup("4242\n5000\n5001\n"),
		Self:     4242,
		Identity: namedIdentity(map[int]string{5000: "/usr/bin/dbus-daemon", 5001: "/usr/bin/dbus-daemon"}),
		Signaler: signaler,
		Emit:     func(e Event) { events = append(events, e) },
	}

	if n := hygiene.Run(); n != 2 {
		t.Fatalf("signalled %d, want 2", n)
	}
	if len(signaler.calls) != 2 {
		t.Fatalf("signals = %+v, want two", signaler.calls)
	}
	for _, call := range signaler.calls {
		if call.pid == 4242 {
			t.Fatal("sermod must never signal itself")
		}
		if call.sig != syscall.SIGTERM {
			t.Fatalf("sent %v, want SIGTERM only", call.sig)
		}
	}
	if len(events) != 2 {
		t.Fatalf("emitted %d event(s), want one per signalled process", len(events))
	}
	if !strings.Contains(events[0].Message, "/usr/bin/dbus-daemon") || !strings.Contains(events[0].Message, "sermod.service") {
		t.Fatalf("event message = %q, want the leftover's exe and unit", events[0].Message)
	}
	if events[0].Action != eventActionReapOwnStrays || events[0].Kind != eventKindKill {
		t.Fatalf("event = %+v, want a kill event for own strays", events[0])
	}
}

func TestSelfStrayHygieneSkipsPID1AndAloneDaemon(t *testing.T) {
	signaler := &selfReapSignaler{}
	hygiene := SelfStrayHygiene{
		ReadFile: sermodCgroup("1\n4242\n"),
		Self:     4242,
		Signaler: signaler,
	}

	if n := hygiene.Run(); n != 0 {
		t.Fatalf("signalled %d, want 0", n)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("signals = %+v, want none", signaler.calls)
	}
}

// Outside a service unit cgroup sermod shares its scope with the operator's shell
// and sshd, so nothing may be signalled at all.
func TestSelfStrayHygieneDoesNothingOutsideAServiceUnit(t *testing.T) {
	signaler := &selfReapSignaler{}
	hygiene := SelfStrayHygiene{
		ReadFile: func(path string) ([]byte, error) {
			if path == "/proc/self/cgroup" {
				return []byte("0::/user.slice/user-0.slice/session-7.scope\n"), nil
			}
			return []byte("10\n11\n4242\n"), nil
		},
		Self:     4242,
		Signaler: signaler,
	}

	if n := hygiene.Run(); n != 0 {
		t.Fatalf("signalled %d, want 0", n)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("signalled inside a login session: %+v", signaler.calls)
	}
}

func TestSelfStrayHygieneReportsSignalFailure(t *testing.T) {
	var events []Event
	hygiene := SelfStrayHygiene{
		ReadFile: sermodCgroup("4242\n5000\n"),
		Self:     4242,
		Signaler: &selfReapSignaler{err: errors.New("operation not permitted")},
		Emit:     func(e Event) { events = append(events, e) },
	}

	if n := hygiene.Run(); n != 0 {
		t.Fatalf("signalled %d, want 0 when delivery fails", n)
	}
	if len(events) != 1 || events[0].Kind != eventKindKillFailed {
		t.Fatalf("events = %+v, want one kill-failed event", events)
	}
	if !strings.Contains(events[0].Message, "pid 5000") {
		t.Fatalf("event message = %q, want the failing pid", events[0].Message)
	}
}

func TestSelfStrayHygieneEnabledUnlessDisabled(t *testing.T) {
	if !ReapOwnStraysEnabled(nil) {
		t.Fatal("startup hygiene must be on by default")
	}
	cfg := &config.Config{}
	cfg.Global.Raw = map[string]any{
		config.SectionEngine: map[string]any{config.EngineKeyReapOwnStrays: false},
	}
	if ReapOwnStraysEnabled(cfg) {
		t.Fatal("engine.reap_own_strays: false must turn it off")
	}
}
