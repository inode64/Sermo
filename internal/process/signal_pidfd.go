package process

import (
	"context"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// SignalProcess binds delivery to the previously authorized process generation.
// Opening the pidfd before reading identity prevents PID recycling during that
// read from redirecting the eventual signal. Unsupported kernels fail closed.
func (OSSignaler) SignalProcess(ctx context.Context, target Process, sig syscall.Signal) error {
	sender := pidfdSignaler{
		open:     unix.PidfdOpen,
		close:    unix.Close,
		identity: OSReader{}.Identity,
		send: func(fd int, sig syscall.Signal) error {
			return unix.PidfdSendSignal(fd, sig, nil, 0)
		},
	}
	return sender.signal(ctx, target, sig)
}

// SignalProcess delivers a signal using the previously authorized identity.
// Production OSSignaler verifies that identity through a pidfd; injected
// primitive signalers can model delivery without accessing host processes.
func SignalProcess(ctx context.Context, signaler Signaler, target Process, sig syscall.Signal) error {
	if verified, ok := signaler.(interface {
		SignalProcess(ctx context.Context, target Process, sig syscall.Signal) error
	}); ok {
		if err := verified.SignalProcess(ctx, target, sig); err != nil {
			return fmt.Errorf("deliver verified signal: %w", err)
		}
		return nil
	}
	if err := signaler.Signal(target.PID, sig); err != nil {
		return fmt.Errorf("deliver signal: %w", err)
	}
	return nil
}

type pidfdSignaler struct {
	open     func(int, int) (int, error)
	close    func(int) error
	identity signalTargetProbe
	send     func(int, syscall.Signal) error
}

func (s pidfdSignaler) signal(ctx context.Context, target Process, sig syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("signal cancelled: %w", err)
	}
	if target.PID <= 0 || target.StartTicks == 0 || !target.ExeOK || target.Exe == "" || target.Delegated {
		return fmt.Errorf("incomplete or unauthorized signal identity for pid %d", target.PID)
	}
	fd, err := s.open(target.PID, 0)
	if err != nil {
		return fmt.Errorf("open pidfd for pid %d: %w", target.PID, err)
	}
	defer func() { _ = s.close(fd) }()
	id, ok := s.identity(target.PID)
	if !ok || !id.StartTicksOK || id.StartTicks != target.StartTicks || !id.ExeOK || id.Exe != target.Exe || id.UID != target.UID {
		return fmt.Errorf("signal identity changed or unavailable for pid %d", target.PID)
	}
	if err := protectedSignalTarget(target.PID, sig, func(int) (Identity, bool) { return id, true }); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("signal cancelled: %w", err)
	}
	if err := s.send(fd, sig); err != nil {
		return fmt.Errorf("signal pid %d via pidfd: %w", target.PID, err)
	}
	return nil
}
