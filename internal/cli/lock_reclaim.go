package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/mountctl"
	"sermo/internal/state"
)

// lockReclaimNotRecordedFormat warns that a reclaim, already reported on
// stderr, could not be written to the state store.
const lockReclaimNotRecordedFormat = "stale lock reclaim not recorded: %v"

// lockReclaimLog collects the stale locks one sermoctl command reclaims. Each
// is reported on stderr when it happens; flushLockReclaims then records them as
// the same warning events sermod records for its own reclaims, so a reclaim is
// audited whichever path took it.
type lockReclaimLog struct {
	stderr   io.Writer
	reclaims []app.LockReclaim
}

func (a App) newLockReclaimLog() *lockReclaimLog {
	return &lockReclaimLog{stderr: a.Stderr}
}

func (l *lockReclaimLog) add(r app.LockReclaim) {
	printLockReclaim(l.stderr, r)
	l.reclaims = append(l.reclaims, r)
}

// printLockReclaim reports one reclaim on w, prefixed by the service or watch
// whose lock it was.
func printLockReclaim(w io.Writer, r app.LockReclaim) {
	if subject := cmp.Or(r.Service, r.Watch); subject != "" {
		fmt.Fprintf(w, "%s: %s\n", subject, r.Message())
		return
	}
	fmt.Fprintln(w, r.Message())
}

// named is a locks.NamedLocker.OnReclaim callback.
func (l *lockReclaimLog) named(service, name, reason string) {
	l.add(app.NamedLockReclaim(service, name, reason))
}

// watch returns the reclaim callback of a host watch control lock.
func (l *lockReclaimLog) watch(name string) func(reason string) {
	return func(reason string) { l.add(app.WatchLockReclaim(name, reason)) }
}

// mount is a mountctl.Controller.OnLockReclaimed callback.
func (l *lockReclaimLog) mount(spec mountctl.Spec, reason string) {
	l.add(app.MountLockReclaim(spec, reason))
}

// flushLockReclaims records every collected reclaim in the state store and
// empties the log. The reclaim already happened and was reported on stderr, so
// a store failure only warns instead of failing the command.
func (a App) flushLockReclaims(ctx context.Context, cfg *config.Config, log *lockReclaimLog) {
	if len(log.reclaims) == 0 {
		return
	}
	reclaims := log.reclaims
	log.reclaims = nil
	_ = withStateStore(ctx, cfg, func(err error) int {
		a.warnLockReclaimNotRecorded(err)
		return exitSuccess
	}, func(store *state.Store) int {
		a.recordLockReclaims(ctx, store, reclaims...)
		return exitSuccess
	})
}

// recordLockReclaims writes each reclaim to store as its warning event. The
// reclaim already happened and was reported, so a failed write only warns.
func (a App) recordLockReclaims(ctx context.Context, store manualActionRecorder, reclaims ...app.LockReclaim) {
	for _, r := range reclaims {
		if err := recordManualActionEvent(ctx, store, app.LockReclaimEventRecord(r)); err != nil {
			a.warnLockReclaimNotRecorded(err)
		}
	}
}

func (a App) warnLockReclaimNotRecorded(err error) {
	fmt.Fprintf(a.Stderr, cliWarningFormat, fmt.Sprintf(lockReclaimNotRecordedFormat, err))
}
