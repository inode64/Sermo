package locks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// maxAcquireAttempts bounds the reclaim/retry loop so a heavily contended lock
// fails fast as held rather than spinning.
const maxAcquireAttempts = 5

// HeldError is returned by Acquire when an active operation lock already exists.
// The operation engine maps it to a blocked result (exit code 75).
type HeldError struct {
	Service string
	Lock    Lock
}

func (e *HeldError) Error() string { return "operation in progress" }

// ownedLock is the owner-checked release shared by the operation lock and the
// slot pool: the file is removed only while it still carries this owner's
// identity; a lock reclaimed by someone else is left untouched.
type ownedLock struct {
	path            string
	ownerPID        int
	ownerStartTicks uint64
	released        bool
}

func (h *ownedLock) release() error {
	if h.released {
		return nil
	}
	current, err := readLockFile(h.path)
	if err != nil {
		if isMissingLock(err) {
			h.released = true
			return nil
		}
		return err
	}
	if current.OwnerPID == h.ownerPID && current.OwnerStartTicks == h.ownerStartTicks {
		if err := os.Remove(h.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("release lock %s: %w", h.path, err)
		}
	}
	h.released = true
	return nil
}

// Handle is an acquired operation lock owned by this process. Release removes it.
type Handle struct {
	ownedLock
}

// Release removes the lock, but only if it is still this owner's lock. Safe on
// a nil handle, so callers can defer it unconditionally.
func (h *Handle) Release() error {
	if h == nil {
		return nil
	}
	return h.release()
}

// OperationLocker acquires the internal operation lock that serializes
// start/stop/restart/reload/resume for one service. It lives under
// <paths.runtime>/ops, separate from named runtime locks.
type OperationLocker struct {
	Dir  string
	Proc ProcessProber
	Now  func() time.Time
	// Self reports the current process identity written into the lock.
	Self func() (pid int, startTicks uint64)
	// OnReclaim is called after a stale lock is reclaimed, with the reason
	// (expired, dead owner, pid reuse) so callers can log it.
	OnReclaim func(service, reason string)
}

// NewOperationLocker returns a locker over dir (<paths.runtime>/ops) using the
// real host for process probing, the wall clock and this process's identity.
func NewOperationLocker(dir string) OperationLocker {
	return OperationLocker{Dir: dir, Proc: OSProcessProber{}, Now: time.Now, Self: selfIdentity}
}

// Acquire atomically creates the operation lock for service with the given TTL.
// If an active lock already exists it returns *HeldError and never waits. A
// stale lock (expired TTL or dead/reused owner) is reclaimed and acquisition
// proceeds.
func (l OperationLocker) Acquire(service string, ttl time.Duration) (*Handle, error) {
	if err := validateIdentifier(lockIdentifierService, service, false); err != nil {
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
	self := l.Self
	if self == nil {
		self = selfIdentity
	}

	if err := os.MkdirAll(l.Dir, lockDirMode); err != nil {
		return nil, fmt.Errorf("create ops dir %s: %w", l.Dir, err)
	}

	path := filepath.Join(l.Dir, service+lockSuffix)
	pid, ticks := self()

	for range maxAcquireAttempts {
		payload := lockFile{
			Service:         service,
			OwnerPID:        pid,
			OwnerStartTicks: ticks,
			CreatedAt:       now(),
			ExpiresAt:       now().Add(ttl),
		}
		err := writeLockFileExclusive(path, payload)
		if err == nil {
			return &Handle{ownedLock{path: path, ownerPID: pid, ownerStartTicks: ticks}}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf(lockAcquireErrorFormat, path, err)
		}

		existing, rerr := readLockFile(path)
		if rerr != nil {
			if isRetryableLockRead(rerr) {
				continue // vanished or still being written; retry
			}
			return nil, fmt.Errorf(lockAcquireErrorFormat, path, rerr)
		}

		state, reason := classify(existing, now(), proc)
		if state == StateActive {
			return nil, &HeldError{Service: service, Lock: toLock(existing, path, state, reason)}
		}

		if reclaimStale(path, existing, proc, now) {
			if l.OnReclaim != nil {
				l.OnReclaim(service, reason)
			}
			continue // reclaimed; retry the exclusive create
		}
		// Could not reclaim: it changed under us. Re-classify; if it went active,
		// it is held, otherwise loop and try again.
		if cur, err := readLockFile(path); err == nil {
			if st, rs := classify(cur, now(), proc); st == StateActive {
				return nil, &HeldError{Service: service, Lock: toLock(cur, path, st, rs)}
			}
		}
	}

	return nil, &HeldError{Service: service, Lock: Lock{Service: service, Path: path, State: StateActive}}
}

// reclaimStale re-reads a lock, confirms it is still the same stale lock, and
// unlinks it. It returns false if the lock changed or turned active between the
// classify and the unlink. Shared by the
// operation and named lockers.
//
// The re-read → classify → unlink runs under an advisory lock on the containing
// directory so two contenders can never both reclaim the same stale lock. Without
// it, the unlink was unconditional: process A could classify a stale lock, then —
// after B reclaimed it and created a fresh lock at the same path — delete B's live
// lock, leaving both A and B believing they held it (mutual exclusion violated).
// The exclusive create (O_EXCL) outside this section stays safe: a remove only
// happens here, after verifying the file is still the expected stale lock.
func reclaimStale(path string, expected lockFile, proc ProcessProber, now func() time.Time) bool {
	if unlock, err := lockReclaimDir(path); err == nil {
		defer unlock()
	}
	current, err := readLockFile(path)
	if err != nil {
		return isMissingLock(err)
	}
	if current.OwnerPID != expected.OwnerPID ||
		current.OwnerStartTicks != expected.OwnerStartTicks ||
		!current.ExpiresAt.Equal(expected.ExpiresAt) {
		return false
	}
	if state, _ := classify(current, now(), proc); state == StateActive {
		return false
	}
	if err := os.Remove(path); err != nil {
		return isMissingLock(err)
	}
	return true
}

// lockReclaimDir takes an exclusive advisory lock on the directory holding path
// for the duration of a reclaim. flock is per-open-file-description and works
// across processes; the lock directory lives on tmpfs. Best-effort: if the
// directory cannot be opened or locked, reclaim proceeds unserialized (the prior
// behavior) rather than failing the acquire.
func lockReclaimDir(path string) (func(), error) {
	dir := filepath.Dir(path)
	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open lock directory %s: %w", dir, err)
	}
	if err := unix.Flock(int(d.Fd()), unix.LOCK_EX); err != nil {
		d.Close()
		return nil, fmt.Errorf("lock directory %s: %w", dir, err)
	}
	return func() {
		_ = unix.Flock(int(d.Fd()), unix.LOCK_UN)
		d.Close()
	}, nil
}

// writeLockFileExclusive creates path with O_CREAT|O_EXCL, writes the payload
// and fsyncs the file and its directory so a lock that exists is always complete
// after a crash. An existing file yields os.ErrExist.
func writeLockFileExclusive(path string, lf lockFile) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, lockFileMode)
	if err != nil {
		return fmt.Errorf("create lock %s: %w", path, err)
	}
	data, err := json.Marshal(lf)
	if err != nil {
		f.Close()
		return fmt.Errorf("marshal lock %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write lock %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync lock %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close lock %s: %w", path, err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir best-effort fsyncs a directory so a newly created lock is durable.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func selfIdentity() (int, uint64) {
	pid := os.Getpid()
	ticks, _ := OSProcessProber{}.StartTicks(pid)
	return pid, ticks
}
