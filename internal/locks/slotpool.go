package locks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var errSlotBusy = errors.New("operation slot busy")

// SlotHandle is one acquired global operation slot.
type SlotHandle struct {
	path            string
	ownerPID        int
	ownerStartTicks uint64
	released        bool
}

// Release frees the slot for another operation.
func (h *SlotHandle) Release() error {
	if h == nil || h.released {
		return nil
	}
	current, err := readLockFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			h.released = true
			return nil
		}
		return err
	}
	if current.OwnerPID == h.ownerPID && current.OwnerStartTicks == h.ownerStartTicks {
		if err := os.Remove(h.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	h.released = true
	return nil
}

// SlotPool bounds how many service operations may run at once across processes
// (section 24). Slots live under <paths.runtime>/op-slots, separate from per-
// service operation locks.
type SlotPool struct {
	Dir   string
	Slots int
	Proc  ProcessProber
	Now   func() time.Time
	Self  func() (pid int, startTicks uint64)
	Sleep func(time.Duration)
}

// NewSlotPool returns a pool over dir with the given capacity. <=0 defaults to 2.
func NewSlotPool(dir string, slots int) SlotPool {
	if slots <= 0 {
		slots = 2
	}
	return SlotPool{
		Dir:   dir,
		Slots: slots,
		Proc:  OSProcessProber{},
		Now:   time.Now,
		Self:  selfIdentity,
		Sleep: time.Sleep,
	}
}

// Acquire waits until a slot is available or ctx is cancelled.
func (p SlotPool) Acquire(ctx context.Context) (*SlotHandle, error) {
	slots := p.Slots
	if slots <= 0 {
		slots = 2
	}
	proc := p.Proc
	if proc == nil {
		proc = OSProcessProber{}
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}
	self := p.Self
	if self == nil {
		self = selfIdentity
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create op-slots dir %s: %w", p.Dir, err)
	}

	for {
		for i := 0; i < slots; i++ {
			path := filepath.Join(p.Dir, fmt.Sprintf("%d.slot", i))
			h, err := p.tryAcquire(path, i, proc, now, self)
			if err == nil {
				return h, nil
			}
			if !errors.Is(err, errSlotBusy) {
				return nil, err
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			sleep(20 * time.Millisecond)
		}
	}
}

func (p SlotPool) tryAcquire(path string, slot int, proc ProcessProber, now func() time.Time, self func() (int, uint64)) (*SlotHandle, error) {
	pid, ticks := self()
	payload := lockFile{
		Service:         fmt.Sprintf("slot-%d", slot),
		OwnerPID:        pid,
		OwnerStartTicks: ticks,
		CreatedAt:       now(),
	}
	if err := writeLockFileExclusive(path, payload); err == nil {
		return &SlotHandle{path: path, ownerPID: pid, ownerStartTicks: ticks}, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("acquire %s: %w", path, err)
	}

	existing, rerr := readLockFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return p.tryAcquire(path, slot, proc, now, self)
		}
		return nil, fmt.Errorf("acquire %s: %w", path, rerr)
	}
	state, _ := classify(existing, now(), proc)
	if state != StateActive {
		if reclaimStale(path, existing, proc, now) {
			return p.tryAcquire(path, slot, proc, now, self)
		}
	}
	return nil, errSlotBusy
}