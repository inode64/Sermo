package operation

import (
	"context"
	"errors"
	"strings"
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
	e.SessionVerifier = func(context.Context, SessionTarget) (SessionBoundary, error) { return SessionBoundary{}, nil }
	e.SessionSignaler = &recordingSignaler{}
	e.SessionExited = func(int, uint64) (bool, error) { return false, nil }
	e.OperationTimeout = 5 * time.Millisecond
	res := e.CloseSession(t.Context(), SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"})
	if res.OK() || !strings.Contains(res.Message, "did not exit") || len(h.emitted) != 1 {
		t.Fatalf("result=%+v", res)
	}
}
