package mountctl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"sermo/internal/checks"
	"sermo/internal/mounts"
)

// Remount repairs a mount that is hung or missing: it force-detaches the
// filesystem mounted at spec.Path (MNT_FORCE aborts the requests a dead NFS
// server left pending, MNT_DETACH as the fallback), mounts it again — through
// /etc/fstab, or by touching the path when autofs owns it — and confirms the
// new mount answers. It never signals the processes using the mount and leaves
// the refcount alone: the mount is the same unit, only repaired.
func (c Controller) Remount(ctx context.Context, spec Spec) (Result, error) {
	if reason := UmountDisabledReason(spec.Path); reason != "" {
		res := disabledUmountResult(spec, reason)
		res.Action = ActionRemount
		return res, errors.New(reason)
	}
	return c.withLock(spec, func() (Result, error) {
		res := Result{Name: spec.Name, Path: spec.Path, Action: ActionRemount, Status: ResultFailed}
		table, err := c.sampleMounts()
		if err != nil {
			return res, err
		}
		automounted := underAutofs(table, spec.Path)
		if realMountAt(table, spec.Path) {
			lazy, err := c.detach(ctx, spec.Path)
			res.Forced, res.Lazy = true, lazy
			if err != nil {
				res.Mounted = true
				res.Message = err.Error()
				return res, err
			}
		}
		if err := c.mountAgain(ctx, spec.Path, automounted); err != nil {
			res.Message = err.Error()
			return res, err
		}
		if err := c.answers(spec.Path); err != nil {
			res.Message = fmt.Sprintf("%s still does not answer: %v", spec.Path, err)
			return res, errors.New(res.Message)
		}
		res.Status, res.Message, res.Mounted = ResultOK, remountMessage(res), true
		return res, nil
	})
}

// detach removes the filesystem mounted at path: a forced unmount first, then
// a lazy one if it is still there; lazy reports that the second was needed.
func (c Controller) detach(ctx context.Context, path string) (lazy bool, err error) {
	ferr := c.unmountWithin(ctx, path, unix.MNT_FORCE)
	if !c.stillMounted(path) {
		return false, nil
	}
	lerr := c.unmountWithin(ctx, path, unix.MNT_DETACH)
	if !c.stillMounted(path) {
		return true, nil
	}
	return true, fmt.Errorf("%s could not be detached: %w", path, errors.Join(ferr, lerr))
}

// stillMounted reports a filesystem at path; an unreadable mount table counts
// as mounted, so a read failure never passes for a successful detach.
func (c Controller) stillMounted(path string) bool {
	table, err := c.sampleMounts()
	return err != nil || realMountAt(table, path)
}

// unmountWithin runs one umount2(2) bounded by the command timeout: a forced
// unmount can itself wait on the server, and the cycle must not wait with it.
func (c Controller) unmountWithin(ctx context.Context, path string, flags int) error {
	unmount := c.Unmount
	if unmount == nil {
		unmount = unix.Unmount
	}
	done := make(chan error, 1)
	go func() { done <- unmount(path, flags) }()
	timer := time.NewTimer(c.commandTimeout())
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("umount2 %s: %w", path, err)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("umount2 %s: no answer within %s", path, c.commandTimeout())
	case <-ctx.Done():
		return fmt.Errorf("umount2 %s: %w", path, ctx.Err())
	}
}

// mountAgain mounts path from /etc/fstab, or lets autofs mount it on access.
func (c Controller) mountAgain(ctx context.Context, path string, automounted bool) error {
	inFstab, err := c.inFstab(path)
	if err != nil {
		return err
	}
	switch {
	case inFstab:
		return c.run(ctx, ActionMount, path)
	case automounted:
		// The probe that confirms the mount is the access that triggers it.
		return nil
	default:
		return fmt.Errorf("%s is neither declared in /etc/fstab nor under an autofs mount", path)
	}
}

func (c Controller) answers(path string) error {
	probe := c.Answers
	if probe == nil {
		probe = checks.StatfsAnswers
	}
	return probe(path, c.commandTimeout())
}

// realMountAt reports a filesystem mounted exactly at path, not counting the
// autofs trigger that may share the mountpoint.
func realMountAt(table []checks.Mount, path string) bool {
	m := checks.MountAtPath(table, path)
	return m != nil && m.FSType != checks.FSTypeAutofs
}

// underAutofs reports an autofs mount at path or at one of its parents (an
// indirect map such as /net), which mounts path again when it is accessed.
func underAutofs(table []checks.Mount, path string) bool {
	clean := filepath.Clean(path)
	for _, m := range table {
		if m.FSType == checks.FSTypeAutofs && mounts.PathUnder(clean, filepath.Clean(m.MountPoint)) {
			return true
		}
	}
	return false
}

func remountMessage(res Result) string {
	switch {
	case res.Lazy:
		return mountMessageRemounted + " after a lazy unmount"
	case res.Forced:
		return mountMessageRemounted + " after a forced unmount"
	default:
		return mountMessageMounted
	}
}
