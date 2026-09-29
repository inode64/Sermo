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
}

func (e lockedVolumeExpander) ExpandPath(ctx context.Context, path string, by int64) (volume.Result, error) {
	timeout := e.timeout
	if timeout <= 0 {
		timeout = DefaultEngineOperationTimeout
	}
	// The lock TTL equals the deadline, so the lock cannot expire while the
	// expansion it protects is still running.
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	handle, err := configureOperationLocker(e.runtimeDir, nil).Acquire(expandLockName(path), timeout)
	if err != nil {
		if _, held := errors.AsType[*locks.HeldError](err); held {
			return volume.Result{}, fmt.Errorf("storage path %q: %w", path, errExpandInProgress)
		}
		return volume.Result{}, fmt.Errorf("lock expansion of storage path %q: %w", path, err)
	}
	defer func() { _ = handle.Release() }()
	res, err := e.inner.ExpandPath(opCtx, path, by)
	if err != nil {
		return res, fmt.Errorf("expand storage path %q: %w", path, err)
	}
	return res, nil
}

// expandLockName keys the lock by the cleaned storage path; escaping keeps the
// path a single lock filename without collisions between similar paths.
func expandLockName(path string) string {
	return expandControlLockPrefix + url.PathEscape(filepath.Clean(path))
}
