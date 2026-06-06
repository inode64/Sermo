package locks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// NamedLocker creates and releases named runtime locks under
// <paths.runtime>/locks (section 20). These guard against external work (e.g. a
// backup) and are checked automatically by the operation engine. Creating them
// (`sermoctl lock ...`) is post-MVP.
type NamedLocker struct {
	Dir  string
	Proc ProcessProber
	Now  func() time.Time
	Self func() (pid int, startTicks uint64)
}

// NewNamedLocker returns a locker over dir (<paths.runtime>/locks).
func NewNamedLocker(dir string) NamedLocker {
	return NamedLocker{Dir: dir, Proc: OSProcessProber{}, Now: time.Now, Self: selfIdentity}
}

// Hold acquires a named lock owned by this process, for the
// `lock SERVICE -- COMMAND` wrapper. Handle.Release unlinks it when COMMAND
// exits; the TTL bounds the lock if the wrapper is killed.
func (l NamedLocker) Hold(service, name, reason string, ttl time.Duration) (*Handle, error) {
	pid, ticks := l.identity()
	return l.acquire(service, name, reason, ttl, pid, ticks)
}

// Pin acquires a persistent named lock with no live owner, for
// `lock acquire`. It has no owner PID, so it stays active until its TTL elapses
// or `lock release` unlinks it. Returns the lock path.
func (l NamedLocker) Pin(service, name, reason string, ttl time.Duration) (string, error) {
	h, err := l.acquire(service, name, reason, ttl, 0, 0)
	if err != nil {
		return "", err
	}
	return h.path, nil
}

// Release unlinks a named lock explicitly, for `lock release`. A missing lock is
// not an error.
func (l NamedLocker) Release(service, name string) error {
	if err := os.Remove(l.path(service, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l NamedLocker) path(service, name string) string {
	file := service
	if name != "" {
		file = service + "." + name
	}
	return filepath.Join(l.Dir, file+lockSuffix)
}

func (l NamedLocker) identity() (int, uint64) {
	if l.Self != nil {
		return l.Self()
	}
	return selfIdentity()
}

func (l NamedLocker) acquire(service, name, reason string, ttl time.Duration, ownerPID int, ownerTicks uint64) (*Handle, error) {
	proc := l.Proc
	if proc == nil {
		proc = OSProcessProber{}
	}
	now := l.Now
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create locks dir %s: %w", l.Dir, err)
	}

	path := l.path(service, name)
	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		lf := lockFile{
			Service:         service,
			Name:            name,
			Reason:          reason,
			OwnerPID:        ownerPID,
			OwnerStartTicks: ownerTicks,
			CreatedAt:       now(),
			ExpiresAt:       now().Add(ttl),
		}
		err := writeLockFileExclusive(path, lf)
		if err == nil {
			return &Handle{path: path, ownerPID: ownerPID, ownerStartTicks: ownerTicks}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire %s: %w", path, err)
		}

		existing, rerr := readLockFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return nil, fmt.Errorf("acquire %s: %w", path, rerr)
		}
		state, staleReason := classify(existing, now(), proc)
		if state == StateActive {
			return nil, &HeldError{Service: service, Lock: toLock(existing, path, state, staleReason)}
		}
		if reclaimStale(path, existing, proc, now) {
			continue
		}
		if cur, err := readLockFile(path); err == nil {
			if st, rs := classify(cur, now(), proc); st == StateActive {
				return nil, &HeldError{Service: service, Lock: toLock(cur, path, st, rs)}
			}
		}
	}
	return nil, &HeldError{Service: service, Lock: Lock{Service: service, Name: name, Path: path, State: StateActive}}
}
