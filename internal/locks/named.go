package locks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NamedLocker creates and releases named runtime locks under
// <paths.runtime>/locks (section 20). These guard against external work (e.g. a
// backup) and are checked automatically by the operation engine. The CLI uses it
// for `sermoctl lock` acquire/release/wrap operations.
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
	if err := validateLockIDs(service, name); err != nil {
		return err
	}
	if err := os.Remove(l.path(service, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReleaseInactive unlinks a named lock only when it is inactive (expired or
// stale). Active locks still represent ongoing external work and must not be
// cleared from the web UI.
func (l NamedLocker) ReleaseInactive(service, name string) (Lock, error) {
	if err := validateLockIDs(service, name); err != nil {
		return Lock{}, err
	}
	proc := l.Proc
	if proc == nil {
		proc = OSProcessProber{}
	}
	now := l.Now
	if now == nil {
		now = time.Now
	}
	path := l.path(service, name)
	existing, err := readLockFile(path)
	if err != nil {
		if isMissingLock(err) {
			return Lock{}, fmt.Errorf("lock %s not found", lockID(service, name))
		}
		return Lock{}, err
	}
	state, reason := classify(existing, now(), proc)
	lock := toLock(existing, path, state, reason)
	if lock.Service == "" {
		lock.Service = service
	}
	if lock.Name == "" {
		lock.Name = name
	}
	if state == StateActive {
		return lock, fmt.Errorf("lock %s is active; refusing release", lockID(service, name))
	}
	if reclaimStale(path, existing, proc, now) {
		return lock, nil
	}
	current, err := readLockFile(path)
	if err != nil {
		if isMissingLock(err) {
			return lock, nil
		}
		return lock, err
	}
	state, reason = classify(current, now(), proc)
	if state == StateActive {
		return toLock(current, path, state, reason), fmt.Errorf("lock %s became active; refusing release", lockID(service, name))
	}
	return toLock(current, path, state, reason), fmt.Errorf("lock %s changed while releasing; retry", lockID(service, name))
}

func (l NamedLocker) path(service, name string) string {
	file := service
	if name != "" {
		// Use a separator that validateIdentifier forbids in both a service and a
		// lock name ('\'), so (service, name) maps to a unique file. A '.'
		// separator collides: lock name "x" on service "a.b" and a bare lock for a
		// service literally named "a.b.x" would both resolve to a.b.x.lock.
		file = service + lockNameSep + name
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
	if err := validateLockIDs(service, name); err != nil {
		return nil, err
	}

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
			return &Handle{ownedLock{path: path, ownerPID: ownerPID, ownerStartTicks: ownerTicks}}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire %s: %w", path, err)
		}

		existing, rerr := readLockFile(path)
		if rerr != nil {
			if isRetryableLockRead(rerr) {
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

func validateLockIDs(service, name string) error {
	if err := validateIdentifier("service", service, false); err != nil {
		return err
	}
	return validateIdentifier("lock name", name, true)
}

func lockID(service, name string) string {
	parts := []string{service}
	if name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, ".")
}
