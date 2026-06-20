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

// defaultSlotTTL is the safety-net lifetime stamped on a slot lock file. Owner
// liveness already reclaims a slot when its holder exits; the TTL only bounds a
// slot whose owner is alive-but-wedged (or survives a PID-reuse false positive),
// matching the TTL the operation and named lockers set. It is deliberately far
// larger than any real operation (themselves bounded by the operation lock TTL)
// so a legitimately long operation is never reclaimed out from under itself.
const defaultSlotTTL = time.Hour

// SlotHandle is one acquired global operation slot.
type SlotHandle struct {
	ownedLock
}

// Release frees the slot for another operation, only while the slot file still
// carries this owner's identity. Safe on a nil handle.
func (h *SlotHandle) Release() error {
	if h == nil {
		return nil
	}
	return h.release()
}

// SlotPool bounds how many service operations may run at once across processes.
// Slots live under <paths.runtime>/op-slots, separate from per-
// service operation locks.
type SlotPool struct {
	Dir   string
	Slots int
	TTL   time.Duration // safety-net slot lifetime; <=0 uses defaultSlotTTL
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
		TTL:   defaultSlotTTL,
		Proc:  OSProcessProber{},
		Now:   time.Now,
		Self:  selfIdentity,
		Sleep: time.Sleep,
	}
}

// InUse reports how many slots are currently held (active lock files).
func (p SlotPool) InUse() (int, error) {
	slots := p.Slots
	if slots <= 0 {
		slots = 2
	}
	if p.Dir == "" {
		return 0, nil
	}
	proc := p.Proc
	if proc == nil {
		proc = OSProcessProber{}
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}
	inUse := 0
	for i := 0; i < slots; i++ {
		path := filepath.Join(p.Dir, fmt.Sprintf("%d.slot", i))
		existing, err := readLockFile(path)
		if err != nil {
			// A missing or unreadable slot file is not held.
			continue
		}
		state, _ := classify(existing, now(), proc)
		if state == StateActive {
			inUse++
		}
	}
	return inUse, nil
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
	ttl := p.TTL
	if ttl <= 0 {
		ttl = defaultSlotTTL
	}
	pid, ticks := self()
	// Bounded retry (no recursion): a slot file that vanishes between create and
	// read, or one we reclaim as stale, is retried in-place up to a fixed number
	// of attempts before yielding errSlotBusy so the caller tries the next slot —
	// a pathologically contended slot can no longer recurse until the stack
	// overflows.
	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		payload := lockFile{
			Service:         fmt.Sprintf("slot-%d", slot),
			OwnerPID:        pid,
			OwnerStartTicks: ticks,
			CreatedAt:       now(),
			ExpiresAt:       now().Add(ttl),
		}
		if err := writeLockFileExclusive(path, payload); err == nil {
			return &SlotHandle{ownedLock{path: path, ownerPID: pid, ownerStartTicks: ticks}}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire %s: %w", path, err)
		}

		existing, rerr := readLockFile(path)
		if rerr != nil {
			if isRetryableLockRead(rerr) {
				continue // vanished or still being written; retry this slot
			}
			return nil, fmt.Errorf("acquire %s: %w", path, rerr)
		}
		state, _ := classify(existing, now(), proc)
		if state == StateActive {
			return nil, errSlotBusy
		}
		if !reclaimStale(path, existing, proc, now) {
			return nil, errSlotBusy
		}
		// reclaimed; retry the exclusive create
	}
	return nil, errSlotBusy
}
