package app

import (
	"context"
	"errors"
	"os"
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

// verifiedSelfReapSignaler records the pidfd-bound delivery path, which carries
// the process generation the hygiene authorized.
type verifiedSelfReapSignaler struct {
	targets []process.Process
}

func (s *verifiedSelfReapSignaler) Signal(int, syscall.Signal) error {
	return errors.New("unverified numeric-PID delivery used")
}

func (s *verifiedSelfReapSignaler) SignalProcess(_ context.Context, target process.Process, _ syscall.Signal) error {
	s.targets = append(s.targets, target)
	return nil
}

// sermodCgroup models sermod's own service unit control group after a restart that
// left processes of the previous incarnation behind. Every listed PID still
// reports the unit as its own cgroup.
func sermodCgroup(procs string) func(string) ([]byte, error) {
	const own = "0::/system.slice/sermod.service\n"
	files := map[string]string{
		"/proc/self/cgroup": own,
		"/sys/fs/cgroup/system.slice/sermod.service/cgroup.procs": procs,
	}
	for field := range strings.FieldsSeq(procs) {
		files["/proc/"+field+"/cgroup"] = own
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
		return process.Identity{PID: pid, Exe: exe, ExeOK: true, StartTicks: uint64(pid) * 10, StartTicksOK: true}, true
	}
}

// TestSelfStrayHygieneRefusesAForeignUnit pins the guard that keeps the hygiene
// inside sermod's own unit. Run in a service cgroup named for something else — a
// CI agent, a container supervisor, a systemd-run wrapper — every sibling PID
// belongs to that something else, and this exact scenario SIGTERMed GitHub's
// runner agent from inside the test suite, killing the machine mid-job eleven
// runs straight.
func TestSelfStrayHygieneRefusesAForeignUnit(t *testing.T) {
	signaler := &selfReapSignaler{}
	foreign := func(path string) ([]byte, error) {
		files := map[string]string{
			"/proc/self/cgroup": "0::/system.slice/actions.runner.host.service" + "\n",
			"/sys/fs/cgroup/system.slice/actions.runner.host.service/cgroup.procs": "4242" + "\n" + "5000" + "\n" + "5001" + "\n",
		}
		if content, ok := files[path]; ok {
			return []byte(content), nil
		}
		return nil, os.ErrNotExist
	}
	hygiene := SelfStrayHygiene{
		ReadFile: foreign,
		Self:     4242,
		Signaler: signaler,
		Emit:     func(Event) { t.Fatal("a foreign unit must emit nothing") },
	}
	if n := hygiene.Run(); n != 0 {
		t.Fatalf("signalled %d processes of a foreign unit, want none", n)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("signals = %+v, want none: those processes belong to the foreign unit", signaler.calls)
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
		Identity: namedIdentity(map[int]string{5000: "/usr/bin/dbus-daemon"}),
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

func TestSelfStrayHygieneRequiresExactDaemonUnit(t *testing.T) {
	for _, tc := range []struct {
		unit string
		want int
	}{
		{unit: "sermod.service", want: 1},
		{unit: "sermod-helper.service"},
		{unit: "sermod2.service"},
		{unit: "sermod.service-helper.service"},
		{unit: "sermod@other.service"},
	} {
		t.Run(tc.unit, func(t *testing.T) {
			calls := &selfReapSignaler{}
			files := map[string]string{
				"/proc/self/cgroup": "0::/system.slice/" + tc.unit + "\n",
				"/proc/5000/cgroup": "0::/system.slice/" + tc.unit + "\n",
				"/sys/fs/cgroup/system.slice/" + tc.unit + "/cgroup.procs": "4242\n5000\n",
			}
			h := SelfStrayHygiene{
				Self: 4242, Signaler: calls,
				Identity: namedIdentity(map[int]string{5000: "/usr/bin/worker"}),
				ReadFile: func(path string) ([]byte, error) {
					if data, ok := files[path]; ok {
						return []byte(data), nil
					}
					return nil, os.ErrNotExist
				},
			}
			if got := h.Run(); got != tc.want || len(calls.calls) != tc.want {
				t.Fatalf("Run = %d, signals = %v; want %d", got, calls.calls, tc.want)
			}
		})
	}
}

// A leftover listed in cgroup.procs can exit on its own while earlier leftovers
// are signalled, and its PID can be recycled by an unrelated process outside
// the unit. Only the generation still inside sermod's cgroup may be signalled,
// and delivery is bound to that generation.
func TestSelfStrayHygieneSkipsRecycledPIDOutsideTheUnit(t *testing.T) {
	cgroups := sermodCgroup("4242\n5000\n5001\n")
	readFile := func(path string) ([]byte, error) {
		if path == "/proc/5001/cgroup" {
			return []byte("0::/user.slice/user-0.slice/session-7.scope\n"), nil
		}
		return cgroups(path)
	}
	signaler := &verifiedSelfReapSignaler{}
	var events []Event
	hygiene := SelfStrayHygiene{
		ReadFile: readFile,
		Self:     4242,
		Identity: namedIdentity(map[int]string{5000: "/usr/bin/dbus-daemon", 5001: "/usr/bin/bash"}),
		Signaler: signaler,
		Emit:     func(e Event) { events = append(events, e) },
	}

	if n := hygiene.Run(); n != 1 {
		t.Fatalf("signalled %d, want only the leftover still in the unit", n)
	}
	if len(signaler.targets) != 1 || signaler.targets[0].PID != 5000 {
		t.Fatalf("targets = %+v, want only pid 5000", signaler.targets)
	}
	if got := signaler.targets[0]; got.StartTicks != 50000 || got.Exe != "/usr/bin/dbus-daemon" || !got.ExeOK {
		t.Fatalf("target = %+v, want the generation whose membership was verified", got)
	}
	for _, e := range events {
		if strings.Contains(e.Message, "pid 5001") {
			t.Fatalf("recycled pid reported as a leftover: %+v", e)
		}
	}
}
