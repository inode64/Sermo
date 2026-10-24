package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/locks"
	"sermo/internal/mountctl"
	"sermo/internal/severity"
	"sermo/internal/state"
)

// Lock labels a LockReclaim names in its message.
const (
	lockLabelOperation = "operation lock"
	lockLabelMount     = "mount operation lock"
	lockLabelExpansion = "expansion lock for "
	lockLabelNamed     = "lock"
)

// configureOperationLocker returns the per-service internal operation locker with
// stale-lock reclaims logged through onReclaim (AGENTS.md).
func configureOperationLocker(runtimeDir string, onReclaim func(service, reason string)) locks.OperationLocker {
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(runtimeDir))
	locker.OnReclaim = onReclaim
	return locker
}

// withOperationLock runs run under the named operation lock, bounded by timeout
// (DefaultEngineOperationTimeout when non-positive). The lock TTL equals the
// deadline, so the lock cannot expire while run is still working. It returns
// only the acquire error, a *locks.HeldError while another operation holds the
// lock; run reports its own outcome. onReclaim, when non-nil, audits a
// reclaimed stale lock.
func withOperationLock(ctx context.Context, runtimeDir, lockName string, timeout time.Duration, onReclaim func(reason string), run func(context.Context)) error {
	if timeout <= 0 {
		timeout = DefaultEngineOperationTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	handle, err := configureOperationLocker(runtimeDir, reclaimReason(onReclaim)).Acquire(lockName, timeout)
	if err != nil {
		return fmt.Errorf("acquire %s: %w", lockName, err)
	}
	defer func() { _ = handle.Release() }()
	run(opCtx)
	return nil
}

// LockReclaim is one stale lock a daemon, web or sermoctl path reclaimed: the
// service or watch it guarded, which lock it was and why it was stale. Every
// reclaim is recorded as the same warning event, whichever path took it.
type LockReclaim struct {
	Service string
	Watch   string
	// Lock describes the lock in the message, e.g. "operation lock" or
	// "lock mysql.backup".
	Lock   string
	Reason string
}

// OperationLockReclaim describes a reclaimed operation lock of a service.
func OperationLockReclaim(service, reason string) LockReclaim {
	return LockReclaim{Service: service, Lock: lockLabelOperation, Reason: reason}
}

// NamedLockReclaim describes a reclaimed named runtime lock of a service.
func NamedLockReclaim(service, name, reason string) LockReclaim {
	return LockReclaim{Service: service, Lock: lockLabelNamed + " " + locks.LockID(service, name), Reason: reason}
}

// WatchLockReclaim describes a reclaimed operation lock of a host watch control
// (RAID, replication, storage expansion or mount).
func WatchLockReclaim(watch, reason string) LockReclaim {
	return LockReclaim{Watch: watch, Lock: lockLabelOperation, Reason: reason}
}

// MountLockReclaim describes a reclaimed mount operation lock of the storage
// watch spec belongs to.
func MountLockReclaim(spec mountctl.Spec, reason string) LockReclaim {
	return LockReclaim{Watch: spec.Name, Lock: lockLabelMount, Reason: reason}
}

// ExpansionLockReclaim describes a reclaimed storage expansion lock, reported
// against the watch whose expansion reclaimed it. The lock is keyed by path,
// which several watches may share, so the path names it.
func ExpansionLockReclaim(watch, path, reason string) LockReclaim {
	return LockReclaim{Watch: watch, Lock: lockLabelExpansion + path, Reason: reason}
}

// Message is the event message: "reclaimed stale <lock> (<reason>)". The
// operation lock wording is the one the event_log severity migration matches.
func (r LockReclaim) Message() string {
	return fmt.Sprintf("reclaimed stale %s (%s)", r.Lock, r.Reason)
}

func (r LockReclaim) event() Event {
	return Event{
		Service: r.Service,
		Watch:   r.Watch,
		Kind:    eventKindAlert,
		// The lock's holder died mid-operation: worth a look, not an outage.
		Severity: severity.Warning,
		Message:  r.Message(),
		Notice:   true,
	}
}

// LockReclaimEventRecord converts a reclaim into the persistent event shape
// sermoctl records, matching the event sermod emits for the same reclaim.
func LockReclaimEventRecord(r LockReclaim) state.EventRecord {
	return eventRecordFromLogged(LoggedEvent{Event: r.event()})
}

// reclaimReason adapts a reclaim callback that already knows what its lock
// guards to OperationLocker.OnReclaim; nil stays nil, so the locker reports
// nothing.
func reclaimReason(onReclaim func(reason string)) func(service, reason string) {
	if onReclaim == nil {
		return nil
	}
	return func(_, reason string) { onReclaim(reason) }
}

// lockReclaimEvent adapts the daemon event log to a reclaim callback keyed by
// what the lock guarded: describe names the reclaim from that subject and the
// stale reason. A nil emit yields nil, so the locker reports nothing.
func lockReclaimEvent[S any](emit func(Event), describe func(S, string) LockReclaim) func(S, string) {
	if emit == nil {
		return nil
	}
	return func(subject S, reason string) { emit(describe(subject, reason).event()) }
}

// operationLockReclaimEvent adapts the daemon event log to OperationLocker.OnReclaim.
func operationLockReclaimEvent(emit func(Event)) func(service, reason string) {
	return lockReclaimEvent(emit, OperationLockReclaim)
}

// mountLockReclaimEvent adapts the daemon event log to
// mountctl.Controller.OnLockReclaimed.
func mountLockReclaimEvent(emit func(Event)) func(spec mountctl.Spec, reason string) {
	return lockReclaimEvent(emit, MountLockReclaim)
}

// watchLockReclaimEvent adapts the daemon event log to the operation lock of
// one host watch's control; nil emit yields nil.
func watchLockReclaimEvent(emit func(Event), watch string) func(reason string) {
	if emit == nil {
		return nil
	}
	return func(reason string) { emit(WatchLockReclaim(watch, reason).event()) }
}
