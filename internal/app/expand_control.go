package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"sermo/internal/locks"
	"sermo/internal/volume"
)

const expandControlLockPrefix = "expand-"

// errExpandInProgress reports that another expansion of the same storage path
// holds its operation lock.
var errExpandInProgress = errors.New("expansion already in progress")

// lockedVolumeExpander serializes expansions of one storage path under a named
// operation lock. The manual web action and the automatic then.expand both get
// their expander from configuredVolumeExpander, so they share this lock: each
// expansion resolves the volume group's free space on its own, and two that
// overlap would each grow the volume by expand.by. A held lock is reported, not
// waited for, so a repeated click cannot queue a second growth either.
type lockedVolumeExpander struct {
	inner      VolumeExpander
	runtimeDir string
	timeout    time.Duration
	// emit, when non-nil, audits a reclaimed stale expansion lock, attributed
	// to the storage watch withExpandWatch put on the context.
	emit func(Event)
}

// expandWatchKey carries the storage watch an expansion runs for.
type expandWatchKey struct{}

// withExpandWatch tags ctx with the storage watch an expansion runs for, so a
// stale expansion lock it reclaims is reported against that watch.
func withExpandWatch(ctx context.Context, watch string) context.Context {
	return context.WithValue(ctx, expandWatchKey{}, watch)
}

func (e lockedVolumeExpander) ExpandPath(ctx context.Context, path string, by int64) (volume.Result, error) {
	var reclaimed func(reason string)
	if e.emit != nil {
		watch, _ := ctx.Value(expandWatchKey{}).(string)
		reclaimed = func(reason string) { e.emit(ExpansionLockReclaim(watch, path, reason).event()) }
	}
	var res volume.Result
	var expandErr error
	err := withOperationLock(ctx, e.runtimeDir, expandLockName(path), e.timeout, reclaimed, func(opCtx context.Context) {
		res, expandErr = e.inner.ExpandPath(opCtx, path, by)
	})
	if _, held := errors.AsType[*locks.HeldError](err); held {
		return volume.Result{}, fmt.Errorf("storage path %q: %w", path, errExpandInProgress)
	}
	if err != nil {
		return volume.Result{}, fmt.Errorf("lock expansion of storage path %q: %w", path, err)
	}
	if expandErr != nil {
		return res, fmt.Errorf("expand storage path %q: %w", path, expandErr)
	}
	return res, nil
}

// expandLockName keys the lock by the cleaned storage path; escaping keeps the
// path a single lock filename without collisions between similar paths.
func expandLockName(path string) string {
	return expandControlLockPrefix + url.PathEscape(filepath.Clean(path))
}
