package operation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sermo/internal/hostfs"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/strutil"
)

const runtimeDirectory = "/run"

// repairStalePIDFiles builds the manual-repair preparation shared by the CLI
// and Web UI. A pidfile is removable only when its service is failed/inactive,
// it is a regular file below the canonical runtime directory, and its exact PID
// is absent from the live process reader. Anything less conclusive fails
// closed. It never resets init state: the engine does that only after residual
// reconciliation proves no survivor remains (see Engine.resetRepairedState), so
// clearing a failed marker cannot hide a live daemon from the following start.
func repairStalePIDFiles(manager servicemgr.Manager, unit string, selectors []process.Selector, reader process.Reader, runtimeDir string) func(context.Context) ([]string, error) {
	paths := repairPIDFilePaths(selectors)
	if reader == nil {
		reader = process.OSReader{}
	}
	return func(ctx context.Context) ([]string, error) {
		return prepareRepair(ctx, manager, unit, paths, reader, runtimeDir)
	}
}

func prepareRepair(ctx context.Context, manager servicemgr.Manager, unit string, paths []string, reader process.Reader, runtimeDir string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("repair preparation context: %w", err)
	}
	if inv, ok := reader.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
	status, err := manager.Status(ctx, unit)
	if err != nil {
		return nil, fmt.Errorf("query init state: %w", err)
	}
	if status.Status != servicemgr.StatusFailed && status.Status != servicemgr.StatusInactive {
		return nil, fmt.Errorf("service is %s; repair requires failed or inactive state", status.Status)
	}
	return removeStalePIDFiles(ctx, paths, reader, runtimeDir)
}

func removeStalePIDFiles(ctx context.Context, paths []string, reader process.Reader, runtimeDir string) ([]string, error) {
	removed := make([]string, 0, len(paths))
	for _, path := range paths {
		wasRemoved, err := removeStalePIDFile(ctx, path, reader, runtimeDir)
		if err != nil {
			return nil, err
		}
		if wasRemoved {
			removed = append(removed, path)
		}
	}
	return removed, nil
}

func removeStalePIDFile(ctx context.Context, path string, reader process.Reader, runtimeDir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("repair preparation context: %w", err)
	}
	resolved, present, err := repairableRuntimePIDFile(path, runtimeDir)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	pid, err := hostfs.ReadPidfile(resolved)
	if err != nil {
		return false, fmt.Errorf("refusing to remove pidfile %q: %w", path, err)
	}
	alive, err := pidAlive(reader, pid)
	if err != nil {
		return false, fmt.Errorf("refusing to remove pidfile %q: cannot prove pid %d is gone: %w", path, pid, err)
	}
	if alive {
		return false, fmt.Errorf("refusing to remove pidfile %q: pid %d is running", path, pid)
	}
	// Unlink the file that was inspected and read, through pinned ancestors: a
	// directory swapped for a symlink since the check fails the removal instead
	// of redirecting it.
	if err := hostfs.RemoveNoSymlinkAncestors(resolved, false); err != nil {
		return false, fmt.Errorf("remove stale pidfile %q: %w", path, err)
	}
	return true, nil
}

// pidAlive reports whether pid is live. Absence counts only when it is proven:
// a failed read of that PID, or a process table that could not be read
// completely without it, is an error rather than "dead". The daemon's caching
// reader otherwise serves a snapshot whose read failures are dropped, and a
// live daemon missing from it would lose its pidfile.
func pidAlive(reader process.Reader, pid int) (bool, error) {
	if checked, ok := reader.(process.IdentityErrorReader); ok {
		_, present, err := checked.IdentityWithError(pid)
		if err != nil {
			return present, fmt.Errorf("read pid %d: %w", pid, err)
		}
		return present, nil
	}
	snapshot, err := process.Snapshot(reader)
	if _, present := snapshot[pid]; present {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("process table incomplete: %w", err)
	}
	return false, nil
}

func repairPIDFilePaths(selectors []process.Selector) []string {
	var paths []string
	for _, selector := range selectors {
		if selector.Type != process.SelectorPidfile {
			continue
		}
		paths = append(paths, selector.Paths...)
	}
	return strutil.Unique(paths)
}

// repairableRuntimePIDFile returns the symlink-free path of a pidfile below the
// runtime directory, and whether it exists. A path that is not clean is refused:
// the kernel resolves ".." after a symlink, so the lexical path and the file it
// names can differ.
func repairableRuntimePIDFile(path, runtimeDir string) (string, bool, error) {
	if filepath.Clean(path) != path {
		return "", false, fmt.Errorf("refusing pidfile %q: path is not clean", path)
	}
	runtimeRoot, err := filepath.EvalSymlinks(runtimeDir)
	if err != nil {
		return "", false, fmt.Errorf("resolve runtime directory %q: %w", runtimeDir, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve pidfile directory %q: %w", path, err)
	}
	relative, err := filepath.Rel(runtimeRoot, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("refusing pidfile %q outside runtime directory %q", path, runtimeRoot)
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	info, err := hostfs.Lstat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect pidfile %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("refusing non-regular pidfile %q", path)
	}
	return resolved, true, nil
}
