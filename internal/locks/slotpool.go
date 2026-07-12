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

const (
	// defaultSlotCount is the fallback process-wide operation concurrency.
	defaultSlotCount = 2

	slotLockFileNameFormat = "%d.slot"
	slotLockServiceFormat  = "slot-%d"

	// defaultSlotTTL is the safety-net lifetime stamped on a slot lock file. Owner
	// liveness already reclaims a slot when its holder exits; the TTL only bounds a
	// slot whose owner is alive-but-wedged (or survives a PID-reuse false positive),
	// matching the TTL the operation and named lockers set. It is deliberately far
	// larger than any real operation (themselves bounded by the operation lock TTL)
	// so a legitimately long operation is never reclaimed out from under itself.
	defaultSlotTTL = time.Hour

	// defaultAcquireRetryInterval spaces retries while all operation slots are busy.
	defaultAcquireRetryInterval = 20 * time.Millisecond
)

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

// NewSlotPool returns a pool over dir with the given capacity. <=0 defaults to
// defaultSlotCount.
func NewSlotPool(dir string, slots int) SlotPool {
	if slots <= 0 {
		slots = defaultSlotCount
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

// withDefaults returns a copy of p with every unset capacity/dependency filled
// in with its default, so the hot paths (InUse, Acquire) share one set of guards
// instead of each repeating the same nil/zero-value checks. The value receiver
// makes this a non-mutating copy.
func (p SlotPool) withDefaults() SlotPool {
	if p.Slots <= 0 {
		p.Slots = defaultSlotCount
	}
	if p.Proc == nil {
		p.Proc = OSProcessProber{}
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Self == nil {
		p.Self = selfIdentity
	}
	if p.Sleep == nil {
		p.Sleep = time.Sleep
	}
	return p
}

// InUse reports how many slots are currently held (active lock files).
func (p SlotPool) InUse() (int, error) {
	if p.Dir == "" {
		return 0, nil
	}
	p = p.withDefaults()
	inUse := 0
	for i := range p.Slots {
		path := filepath.Join(p.Dir, fmt.Sprintf(slotLockFileNameFormat, i))
		existing, err := readLockFile(path)
		if err != nil {
			// A missing or unreadable slot file is not held.
			continue
		}
		state, _ := classify(existing, p.Now(), p.Proc)
		if state == StateActive {
			inUse++
		}
	}
	return inUse, nil
}

// Acquire waits until a slot is available or ctx is cancelled.
func (p SlotPool) Acquire(ctx context.Context) (*SlotHandle, error) {
	p = p.withDefaults()
	if err := os.MkdirAll(p.Dir, lockDirMode); err != nil {
		return nil, fmt.Errorf("create op-slots dir %s: %w", p.Dir, err)
	}

	for {
		for i := range p.Slots {
			path := filepath.Join(p.Dir, fmt.Sprintf(slotLockFileNameFormat, i))
			h, err := p.tryAcquire(path, i, p.Proc, p.Now, p.Self)
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
			p.Sleep(defaultAcquireRetryInterval)
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
	for range maxAcquireAttempts {
		payload := lockFile{
			Service:         fmt.Sprintf(slotLockServiceFormat, slot),
			OwnerPID:        pid,
			OwnerStartTicks: ticks,
			CreatedAt:       now(),
			ExpiresAt:       now().Add(ttl),
		}
		if err := writeLockFileExclusive(path, payload); err == nil {
			return &SlotHandle{ownedLock{path: path, ownerPID: pid, ownerStartTicks: ticks}}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf(lockAcquireErrorFormat, path, err)
		}

		existing, rerr := readLockFile(path)
		if rerr != nil {
			if isRetryableLockRead(rerr) {
				continue // vanished or still being written; retry this slot
			}
			return nil, fmt.Errorf(lockAcquireErrorFormat, path, rerr)
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
