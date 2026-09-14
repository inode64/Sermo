package process

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

func TestPIDFDSignalChecksIdentity(t *testing.T) {
	target := Process{PID: 100, StartTicks: 42, UID: 110, Exe: testExe, ExeOK: true}
	valid := Identity{PID: 100, StartTicks: 42, StartTicksOK: true, UID: 110, Exe: testExe, ExeOK: true}
	tests := []struct {
		name        string
		change      func(*Identity)
		unavailable bool
		openErr     error
		sendErr     error
		cancel      bool
		wantSend    bool
		wantErr     bool
	}{
		{name: "matching generation", wantSend: true},
		{name: "recycled pid", change: func(id *Identity) { id.StartTicks++ }, wantErr: true},
		{name: "changed exe", change: func(id *Identity) { id.Exe = "/other" }, wantErr: true},
		{name: "changed user", change: func(id *Identity) { id.UID++ }, wantErr: true},
		{name: "unresolved exe", change: func(id *Identity) { id.ExeOK = false }, wantErr: true},
		{name: "unknown generation", change: func(id *Identity) { id.StartTicksOK = false }, wantErr: true},
		{name: "unreadable identity", unavailable: true, wantErr: true},
		{name: "unsupported pidfd", openErr: syscall.ENOSYS, wantErr: true},
		{name: "exited after verification", sendErr: syscall.ESRCH, wantSend: true, wantErr: true},
		{name: "cancel during verification", cancel: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened, closed, sent := false, false, false
			sender := pidfdSignaler{
				open: func(pid, flags int) (int, error) {
					if pid != target.PID || flags != 0 {
						t.Fatalf("open(%d, %d)", pid, flags)
					}
					opened = tc.openErr == nil
					return 7, tc.openErr
				},
				identity: func(int) (Identity, bool) {
					if !opened {
						t.Fatal("identity read before binding pidfd")
					}
					id := valid
					if tc.change != nil {
						tc.change(&id)
					}
					if tc.cancel {
						cancel()
					}
					return id, !tc.unavailable
				},
				close: func(fd int) error {
					if fd != 7 {
						t.Fatalf("close(%d)", fd)
					}
					closed = true
					return nil
				},
				send: func(fd int, sig syscall.Signal) error {
					if fd != 7 || sig != syscall.SIGTERM {
						t.Fatalf("send(%d, %v)", fd, sig)
					}
					sent = true
					return tc.sendErr
				},
			}
			err := sender.signal(ctx, target, syscall.SIGTERM)
			if (err != nil) != tc.wantErr || sent != tc.wantSend || closed != opened {
				t.Fatalf("err=%v sent=%v closed=%v opened=%v", err, sent, closed, opened)
			}
			for _, want := range []error{tc.openErr, tc.sendErr} {
				if want != nil && !errors.Is(err, want) {
					t.Fatalf("error %v does not preserve %v", err, want)
				}
			}
		})
	}
}

func TestPIDFDSignalRefusesIncompleteTarget(t *testing.T) {
	sender := pidfdSignaler{open: func(int, int) (int, error) {
		t.Fatal("opened pidfd for incomplete identity")
		return 0, nil
	}}
	if err := sender.signal(context.Background(), Process{PID: 100}, syscall.SIGTERM); err == nil {
		t.Fatal("accepted unknown generation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sender.signal(ctx, Process{PID: 100}, syscall.SIGTERM); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled signal: %v", err)
	}
}
