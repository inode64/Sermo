package operation

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/locks"
	"sermo/internal/process"
)

func TestSMBSessionCloseSafetyPath(t *testing.T) {
	for _, name := range []string{"exited", "guard", "lock", "named lock", "unavailable", "changed", "cancelled", "signal refused", "survivor", "exit error"} {
		t.Run(name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.OperationTimeout = 5 * time.Millisecond
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			signaler := &sessionIdentitySignaler{t: t}
			e.SessionSignaler = signaler
			verified := 0
			proc := process.Process{PID: 101, StartTicks: 1234, Exe: "/usr/sbin/smbd", ExeOK: true, UID: 0}
			e.SMBSessionVerifier = func(context.Context, SMBSessionTarget) (process.Process, error) {
				verified++
				if name == "changed" {
					return process.Process{}, errors.New("session changed")
				}
				if name == "cancelled" {
					cancel()
				}
				return proc, nil
			}
			switch name {
			case "guard":
				e.Guard = func(context.Context, string) (string, string, error) { return "guard", "busy", nil }
			case "lock":
				e.AcquireLock = func(time.Duration) (func() error, error) { return nil, errors.New("locked") }
			case "named lock":
				h.named = []locks.Lock{{Service: "mysql-main", Name: "backup", State: locks.StateActive}}
			case "unavailable":
				e.SMBSessionVerifier = nil
			case "signal refused":
				signaler.err = errors.New("pidfd identity changed")
			}
			e.SessionExited = func(pid int, ticks uint64) (bool, error) {
				if pid != proc.PID || ticks != proc.StartTicks {
					t.Fatal("exit probe lost process generation")
				}
				if name == "exit error" {
					return false, errors.New("unreadable")
				}
				return name != "survivor", nil
			}
			result := e.CloseSMBSession(ctx, SMBSessionTarget{Check: "sessions", Identity: "generation"})
			if result.OK() != (name == "exited") || len(h.emitted) != 1 || len(h.mgr.calls) != 0 {
				t.Fatalf("result=%+v events=%+v manager=%v", result, h.emitted, h.mgr.calls)
			}
			if (name == "guard" || name == "lock" || name == "named lock") && verified != 0 {
				t.Fatal("verification bypassed operation gates")
			}
			wantSignal := name == "exited" || name == "signal refused" || name == "survivor" || name == "exit error"
			if (signaler.got.PID != 0) != wantSignal {
				t.Fatalf("signal=%+v", signaler.got)
			}
		})
	}
}
