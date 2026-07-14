package process

import (
	"context"
	"os/exec"
	"os/user"
	"syscall"
	"testing"
	"time"
)

func TestParseSignal(t *testing.T) {
	paddedUSR1 := " usr1 "
	cases := map[string]syscall.Signal{
		"HUP":      syscall.SIGHUP,
		"sighup":   syscall.SIGHUP,
		"SIGHUP":   syscall.SIGHUP,
		paddedUSR1: syscall.SIGUSR1,
		"USR2":     syscall.SIGUSR2,
	}
	for in, want := range cases {
		got, err := ParseSignal(in)
		if err != nil {
			t.Errorf("ParseSignal(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSignal(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseSignal("BOGUS"); err == nil {
		t.Error("ParseSignal(BOGUS) must error on an unknown name")
	}
}

func TestProtectedSignalTarget(t *testing.T) {
	probe := func(pid int) (Identity, bool) {
		switch pid {
		case 2:
			return Identity{PID: 2, ExeOK: false}, true
		case 22:
			return Identity{PID: 22, PPID: 2, ExeOK: false}, true
		case 23:
			return Identity{PID: 23, PPID: 2, Exe: "/usr/bin/app", ExeOK: true, Cmdline: []string{"/usr/bin/app"}}, true
		default:
			return Identity{}, false
		}
	}
	tests := []struct {
		name    string
		pid     int
		sig     syscall.Signal
		wantErr bool
	}{
		{name: "pid 1 term", pid: 1, sig: syscall.SIGTERM, wantErr: true},
		{name: "pid 1 hup allowed", pid: 1, sig: syscall.SIGHUP},
		{name: "pid 2 kernel root", pid: 2, sig: syscall.SIGTERM, wantErr: true},
		{name: "kernel child", pid: 22, sig: syscall.SIGKILL, wantErr: true},
		{name: "normal pid with ppid 2", pid: 23, sig: syscall.SIGTERM},
		{name: "non terminating signal to kernel child", pid: 22, sig: syscall.SIGHUP},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := protectedSignalTarget(tc.pid, tc.sig, probe)
			if (err != nil) != tc.wantErr {
				t.Fatalf("protectedSignalTarget(%d, %v) err = %v, wantErr %v", tc.pid, tc.sig, err, tc.wantErr)
			}
		})
	}
}

type sigCall struct {
	pid int
	sig syscall.Signal
}

type recSignaler struct {
	calls []sigCall
}

func (r *recSignaler) Signal(pid int, sig syscall.Signal) error {
	r.calls = append(r.calls, sigCall{pid, sig})
	return nil
}

func (r *recSignaler) sigsFor(pid int) []syscall.Signal {
	var out []syscall.Signal
	for _, c := range r.calls {
		if c.pid == pid {
			out = append(out, c.sig)
		}
	}
	return out
}

// scriptedRediscover returns successive residual snapshots, repeating the last.
type scriptedRediscover struct {
	steps [][]Process
	i     int
}

func (s *scriptedRediscover) next() []Process {
	if s.i >= len(s.steps) {
		if len(s.steps) == 0 {
			return nil
		}
		return s.steps[len(s.steps)-1]
	}
	step := s.steps[s.i]
	s.i++
	return step
}

func killableProc(pid int) Process {
	return Process{PID: pid, UID: 110, Exe: testExe, ExeOK: true}
}

var killPolicy = KillPolicy{
	ForceKill:  true,
	KillOnlyIf: KillSelector{Users: []string{"mysql"}, ExeAny: []string{testExe}},
}

func newReaper(sig Signaler, steps [][]Process) Reaper {
	sr := &scriptedRediscover{steps: steps}
	return Reaper{
		Rediscover:  sr.next,
		Signaler:    sig,
		ResolveUser: fakeUsers(map[string]uint32{"mysql": 110}),
		Sleep:       func(time.Duration) {},
	}
}

func TestReapNoResidualsIsOK(t *testing.T) {
	res := Reaper{}.Reap(context.Background(), nil, killPolicy)
	if !res.OK() || len(res.Signalled) != 0 {
		t.Fatalf("empty residuals: %+v", res)
	}
}

func TestReapForceKillFalseSignalsNothing(t *testing.T) {
	sig := &recSignaler{}
	r := newReaper(sig, nil)
	res := r.Reap(context.Background(), []Process{killableProc(100)}, KillPolicy{ForceKill: false})
	if res.OK() {
		t.Fatal("residuals present must not be OK")
	}
	if len(sig.calls) != 0 {
		t.Fatalf("force_kill=false must not signal, got %v", sig.calls)
	}
	if len(res.Remaining) != 1 {
		t.Fatalf("remaining = %+v", res.Remaining)
	}
}

func TestReapTermSucceeds(t *testing.T) {
	sig := &recSignaler{}
	// After SIGTERM, the process is gone.
	r := newReaper(sig, [][]Process{{}})
	res := r.Reap(context.Background(), []Process{killableProc(100)}, killPolicy)
	if !res.OK() {
		t.Fatalf("expected OK, remaining=%+v", res.Remaining)
	}
	if sigs := sig.sigsFor(100); len(sigs) != 1 || sigs[0] != syscall.SIGTERM {
		t.Fatalf("pid 100 signals = %v, want [SIGTERM] only (no SIGKILL)", sigs)
	}
}

func TestReapEscalatesToKill(t *testing.T) {
	sig := &recSignaler{}
	// Survives SIGTERM (still present), gone after SIGKILL.
	r := newReaper(sig, [][]Process{{killableProc(100)}, {}})
	res := r.Reap(context.Background(), []Process{killableProc(100)}, killPolicy)
	if !res.OK() {
		t.Fatalf("expected OK, remaining=%+v", res.Remaining)
	}
	sigs := sig.sigsFor(100)
	if len(sigs) != 2 || sigs[0] != syscall.SIGTERM || sigs[1] != syscall.SIGKILL {
		t.Fatalf("pid 100 signals = %v, want [SIGTERM SIGKILL]", sigs)
	}
}

func TestReapSurvivesKillIsOrphan(t *testing.T) {
	sig := &recSignaler{}
	r := newReaper(sig, [][]Process{{killableProc(100)}, {killableProc(100)}})
	res := r.Reap(context.Background(), []Process{killableProc(100)}, killPolicy)
	if res.OK() {
		t.Fatal("survivor of SIGKILL must be reported as orphan")
	}
	if len(res.Remaining) != 1 || res.Remaining[0].PID != 100 {
		t.Fatalf("remaining = %+v", res.Remaining)
	}
}

func TestReapNeverSignalsNonKillable(t *testing.T) {
	sig := &recSignaler{}
	// A residual that does not match kill_only_if (wrong user) stays forever.
	orphan := Process{PID: 200, UID: 999, Exe: testExe, ExeOK: true}
	r := newReaper(sig, [][]Process{{orphan}, {orphan}})
	res := r.Reap(context.Background(), []Process{orphan}, killPolicy)
	if len(sig.calls) != 0 {
		t.Fatalf("non-killable residual must never be signalled, got %v", sig.calls)
	}
	if res.OK() || len(res.Remaining) != 1 {
		t.Fatalf("non-killable residual must be reported: %+v", res)
	}
}

func TestReapNeverSignalsProtectedPID1(t *testing.T) {
	sig := &recSignaler{}
	pid1 := killableProc(1)
	r := newReaper(sig, [][]Process{{pid1}, {pid1}})

	res := r.Reap(context.Background(), []Process{pid1}, killPolicy)

	if len(sig.calls) != 0 {
		t.Fatalf("protected pid 1 must never be signalled, got %v", sig.calls)
	}
	if res.OK() || len(res.Remaining) != 1 || res.Remaining[0].PID != 1 {
		t.Fatalf("protected pid 1 must remain as orphan, got %+v", res)
	}
}

func TestReapMixedKillableAndOrphan(t *testing.T) {
	sig := &recSignaler{}
	killable := killableProc(100)
	orphan := Process{PID: 200, UID: 999, Exe: testExe, ExeOK: true}
	// SIGTERM round sees both; only killable is signalled. After TERM the
	// killable is gone, the orphan remains.
	r := newReaper(sig, [][]Process{{orphan}})
	res := r.Reap(context.Background(), []Process{killable, orphan}, killPolicy)

	if got := sig.sigsFor(100); len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Fatalf("killable signals = %v, want [SIGTERM]", got)
	}
	if len(sig.sigsFor(200)) != 0 {
		t.Fatalf("orphan must not be signalled")
	}
	if res.OK() || len(res.Remaining) != 1 || res.Remaining[0].PID != 200 {
		t.Fatalf("expected orphan 200 remaining: %+v", res)
	}
	if len(res.Signalled) != 1 || res.Signalled[0] != 100 {
		t.Fatalf("signalled = %v, want [100]", res.Signalled)
	}
}

// TestReapRealSignalKillsChild exercises the real OSSignaler against a live
// child process, reaping the zombie via a concurrent Wait so liveness reflects
// reality.
func TestReapRealSignalKillsChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	pid := cmd.Process.Pid
	id, ok := OSReader{}.Identity(pid)
	if !ok || !id.ExeOK {
		t.Skipf("cannot read identity for pid %d", pid)
	}
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot read current user: %v", err)
	}

	p := Process{PID: pid, PPID: id.PPID, UID: id.UID, User: id.User, Exe: id.Exe, ExeOK: true}
	policy := KillPolicy{
		ForceKill:   true,
		TermTimeout: 500 * time.Millisecond,
		KillTimeout: 500 * time.Millisecond,
		KillOnlyIf:  KillSelector{Users: []string{u.Username}, ExeAny: []string{id.Exe}},
	}

	rediscover := func() []Process {
		if syscall.Kill(pid, 0) == nil {
			return []Process{p}
		}
		return nil
	}
	r := Reaper{Rediscover: rediscover, Signaler: OSSignaler{}, ResolveUser: OSUserResolver, Sleep: time.Sleep}

	res := r.Reap(context.Background(), []Process{p}, policy)
	if !res.OK() {
		t.Fatalf("child not reaped, remaining = %+v", res.Remaining)
	}
	if len(res.Signalled) != 1 || res.Signalled[0] != pid {
		t.Fatalf("signalled = %v, want [%d]", res.Signalled, pid)
	}
}
