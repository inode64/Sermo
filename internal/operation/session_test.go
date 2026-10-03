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

func TestResidualSessionReusesReapAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		authorized, changed, survives, probeError bool
		wantSignals                               int
		wantOK                                    bool
	}{
		{name: "authorized cleanup", authorized: true, wantSignals: 4, wantOK: true},
		{name: "unconfigured", wantSignals: 0},
		{name: "identity changed", authorized: true, changed: true, wantSignals: 2},
		{name: "survivor", authorized: true, survives: true, wantSignals: 4},
		{name: "unreadable", authorized: true, probeError: true, wantSignals: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			signaler := &recordingSignaler{}
			e.SessionSignaler = signaler
			e.Sleep = func(time.Duration) {}
			want := SessionBoundary{Residual: true, MonitorPID: 97, MonitorStartTicks: 1235, Exe: "/usr/bin/sudo", UID: 81}
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
			if tc.authorized {
				e.ReapSelector = process.KillSelector{ExeAny: []string{"/usr/bin/sudo"}, Users: []string{"81"}}
			}
			res := e.CloseSession(t.Context(), SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"})
			if res.OK() != tc.wantOK || len(signaler.calls) != tc.wantSignals || len(h.emitted) != 1 || len(h.mgr.calls) != 0 {
				t.Fatalf("result=%+v signals=%v events=%v", res, signaler.calls, h.emitted)
			}
			if tc.wantSignals == 4 && !strings.Contains(signaler.calls[2], "killed") {
				t.Fatalf("signals=%v", signaler.calls)
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

func (s *sessionIdentitySignaler) Signal(int, syscall.Signal) error {
	s.t.Fatal("numeric signal bypassed session identity")
	return nil
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
