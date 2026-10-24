package operation

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/process"
)

// A residual sudo terminal is authorized by its own verified session evidence,
// with no configuration: only the exact sudo executable, only this terminal's
// frontend and monitor, revalidated before every escalation.
func TestResidualSudoSessionUsesVerifiedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		exe                           string
		monitorPID                    int
		changed, survives, probeError bool
		unknownTicks                  bool
		wantSignals                   int
		wantOK                        bool
		wantMessage                   string
	}{
		{name: "verified cleanup", exe: "/usr/bin/sudo", monitorPID: 97, wantSignals: 4, wantOK: true},
		{name: "legacy sudo path", exe: "/bin/sudo", monitorPID: 97, wantSignals: 4, wantOK: true},
		{name: "not sudo", exe: "/usr/bin/bash", monitorPID: 97, wantMessage: "is not sudo"},
		{name: "incomplete identity", exe: "/usr/bin/sudo", wantMessage: "incomplete sudo process identity"},
		{name: "unknown target generation", exe: "/usr/bin/sudo", monitorPID: 97, unknownTicks: true, wantMessage: "incomplete sudo process identity"},
		{name: "identity changed", exe: "/usr/bin/sudo", monitorPID: 97, changed: true, wantSignals: 2},
		{name: "survivor", exe: "/usr/bin/sudo", monitorPID: 97, survives: true, wantSignals: 4},
		{name: "unreadable", exe: "/usr/bin/sudo", monitorPID: 97, probeError: true, wantSignals: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			signaler := &recordingSignaler{}
			e.SessionSignaler = signaler
			e.Sleep = func(time.Duration) {}
			want := SessionBoundary{Residual: true, MonitorPID: tc.monitorPID, MonitorStartTicks: 1235, Exe: tc.exe, UID: 81}
			verified := 0
			e.SessionVerifier = func(context.Context, SessionTarget) (SessionBoundary, error) {
				verified++
				got := want
				if tc.changed && verified > 1 {
					got.MonitorStartTicks++
				}
				return got, nil
			}
			e.SessionExited = func(int, uint64) (bool, error) {
				if tc.probeError {
					return false, errors.New("unreadable")
				}
				return len(signaler.calls) == 4 && !tc.survives, nil
			}
			target := SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"}
			if tc.unknownTicks {
				target.StartTicks = 0
			}
			res := e.CloseSession(t.Context(), target)
			if res.OK() != tc.wantOK || len(signaler.calls) != tc.wantSignals || len(h.emitted) != 1 || len(h.mgr.calls) != 0 {
				t.Fatalf("result=%+v signals=%v events=%v", res, signaler.calls, h.emitted)
			}
			if tc.wantSignals == 4 && !strings.Contains(signaler.calls[2], "killed") {
				t.Fatalf("signals=%v", signaler.calls)
			}
			if tc.wantMessage != "" && !strings.Contains(res.Message, tc.wantMessage) {
				t.Fatalf("message = %q, want %q", res.Message, tc.wantMessage)
			}
		})
	}
}

func TestConnectedSessionSurvivalIsNotSuccess(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.SessionVerifier = func(context.Context, SessionTarget) (SessionBoundary, error) {
		return SessionBoundary{Exe: "/usr/lib/sshd-session", UID: 81}, nil
	}
	e.SessionSignaler = &recordingSignaler{}
	e.SessionExited = func(int, uint64) (bool, error) { return false, nil }
	e.OperationTimeout = 5 * time.Millisecond
	res := e.CloseSession(t.Context(), SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"})
	if res.OK() || !strings.Contains(res.Message, "did not exit") || len(h.emitted) != 1 {
		t.Fatalf("result=%+v", res)
	}
}

// Session delivery must use the identity-aware boundary that OSSignaler uses,
// not merely a fake accepting any numeric PID.
type sessionIdentitySignaler struct {
	t   *testing.T
	got process.Process
}

func (s *sessionIdentitySignaler) SignalProcess(_ context.Context, proc process.Process, sig syscall.Signal) error {
	if sig != syscall.SIGTERM {
		s.t.Fatalf("unexpected signal %v", sig)
	}
	s.got = proc
	return nil
}

func TestConnectedSessionCarriesExactSignalIdentity(t *testing.T) {
	for _, exe := range []string{"/usr/lib/sshd-session", ""} {
		t.Run(exe, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			signaler := &sessionIdentitySignaler{t: t}
			e.SessionSignaler = signaler
			e.SessionVerifier = func(context.Context, SessionTarget) (SessionBoundary, error) {
				return SessionBoundary{Exe: exe, UID: 81}, nil
			}
			e.SessionExited = func(int, uint64) (bool, error) { return true, nil }
			res := e.CloseSession(t.Context(), SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"})
			if res.OK() != (exe != "") || len(h.emitted) != 1 {
				t.Fatalf("result=%+v events=%v", res, h.emitted)
			}
			if exe == "" {
				if signaler.got.PID != 0 {
					t.Fatal("signalled without executable")
				}
			} else if got := signaler.got; got.PID != 96 || got.StartTicks != 1234 || got.Exe != exe || !got.ExeOK || got.UID != 81 {
				t.Fatalf("signal identity=%+v", got)
			}
		})
	}
}

func TestDeletedSessionCarriesPinnedFileEvidence(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	signaler := &sessionIdentitySignaler{t: t}
	e.SessionSignaler = signaler
	boundary := SessionBoundary{ExePrev: "/usr/lib64/misc/sshd-session", ExeFile: process.ExecutableFile{Device: 1, Inode: 99}, Cgroup: "0::/openrc.sshd"}
	e.SessionVerifier = func(context.Context, SessionTarget) (SessionBoundary, error) { return boundary, nil }
	e.SessionExited = func(int, uint64) (bool, error) { return true, nil }
	res := e.CloseSession(t.Context(), SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"})
	if !res.OK() || signaler.got.ExeOK || signaler.got.ExePrev != boundary.ExePrev || signaler.got.ExeFile != boundary.ExeFile || signaler.got.Cgroup != boundary.Cgroup {
		t.Fatalf("result=%+v identity=%+v", res, signaler.got)
	}
}
