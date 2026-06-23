package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"

	"sermo/internal/cfgval"
	"sermo/internal/notify"
)

// fileCond is the set of attribute conditions a file watch evaluates per path.
// An empty set is invalid (rejected at build time).
type fileCond struct {
	sizeChange  bool    // fire when size differs from the previous cycle
	sizeOp      string  // edge-triggered size threshold op ("" = none)
	sizeValue   float64 // threshold comparand
	permChange  bool    // fire when the permission bits change
	ownerChange bool    // fire when the owning uid/gid changes
	onDelete    bool    // fire when a previously-seen path stops existing
}

func (c fileCond) any() bool {
	return c.sizeChange || c.sizeOp != "" || c.permChange || c.ownerChange || c.onDelete
}

// fileState is the remembered attributes of one path across cycles.
type fileState struct {
	size     int64
	perm     uint32 // st_mode & 07777: permission bits plus setuid/setgid/sticky
	uid, gid uint32
	breached bool // previous size-threshold result, for edge detection
}

// fileWatcher monitors a file or directory (optionally its whole subtree) for
// attribute changes and fires a hook once per change. It is stateful: it
// remembers each path's attributes across cycles and reports only transitions.
// The baseline is adopted silently on first sight, so a daemon start never fires.
type fileWatcher struct {
	name      string
	path      string
	recursive bool
	cond      fileCond
	hook      HookSpec
	notifiers []notify.Notifier
	dryRun    bool
	inPanic   func() bool
	runner    HookRunner
	emit      func(Event)

	baseline map[string]fileState
}

// runCycle scans the watched path(s), compares each against its baseline, and
// fires one hook (and emits one event) per detected change. It is the Watch.Cycle
// implementation for a file watch.
func (w *fileWatcher) runCycle(ctx context.Context) {
	if w.baseline == nil {
		w.baseline = map[string]fileState{}
	}
	current := w.scan()

	paths := make([]string, 0, len(current))
	for p := range current {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic event order

	for _, p := range paths {
		if ctx.Err() != nil {
			return // shutting down: stop firing
		}
		cur := current[p]
		prev, known := w.baseline[p]
		w.baseline[p] = cur
		if known {
			w.diff(ctx, p, prev, cur)
		}
		// Unknown paths adopt the baseline silently (no event on first sight).
	}

	// Deletions: paths we tracked that are absent now.
	gone := make([]string, 0)
	for p := range w.baseline {
		if _, ok := current[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		if ctx.Err() != nil {
			return
		}
		if w.cond.onDelete {
			w.fire(ctx, p, "deleted", p+" no longer exists", map[string]string{
				"SERMO_OLD": strconv.FormatInt(w.baseline[p].size, 10),
			})
		}
		delete(w.baseline, p)
	}
}

// scan returns the current attributes of the watched path and, when recursive
// and the path is a directory, every entry in its subtree. Symlinks are stat'd
// as links (never followed), so a link is watched as itself, not its target.
func (w *fileWatcher) scan() map[string]fileState {
	out := map[string]fileState{}
	info, err := os.Lstat(w.path)
	if err != nil {
		return out // missing root: handled as a deletion against the baseline
	}
	out[w.path] = w.stateOf(info)
	if w.recursive && info.IsDir() {
		_ = filepath.WalkDir(w.path, func(p string, d fs.DirEntry, err error) error {
			if p == w.path {
				return nil // skip the root; it was already added above
			}
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped during best-effort recursive scans
			}
			fi, err := d.Info()
			if err != nil {
				return nil //nolint:nilerr // entries that disappear during the scan are skipped
			}
			out[p] = w.stateOf(fi)
			return nil
		})
	}
	return out
}

func (w *fileWatcher) stateOf(info fs.FileInfo) fileState {
	st := fileState{size: info.Size()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		st.perm = uint32(sys.Mode) & 0o7777
		st.uid, st.gid = sys.Uid, sys.Gid
	}
	if w.cond.sizeOp != "" {
		st.breached = cfgval.CompareFloat(float64(st.size), w.cond.sizeOp, w.cond.sizeValue)
	}
	return st
}

// diff fires a hook for each configured attribute that changed between prev and
// cur for one path.
func (w *fileWatcher) diff(ctx context.Context, path string, prev, cur fileState) {
	c := w.cond
	if c.sizeChange && cur.size != prev.size {
		w.fire(ctx, path, "size", fmt.Sprintf("%s size %d -> %d", path, prev.size, cur.size), map[string]string{
			"SERMO_OLD":  strconv.FormatInt(prev.size, 10),
			"SERMO_NEW":  strconv.FormatInt(cur.size, 10),
			"SERMO_SIZE": strconv.FormatInt(cur.size, 10),
		})
	}
	// Edge-triggered: fire only when the threshold is newly crossed.
	if c.sizeOp != "" && cur.breached && !prev.breached {
		val := strconv.FormatFloat(c.sizeValue, 'f', -1, 64)
		w.fire(ctx, path, "size_threshold",
			fmt.Sprintf("%s size %d %s %s", path, cur.size, c.sizeOp, val), map[string]string{
				"SERMO_SIZE":  strconv.FormatInt(cur.size, 10),
				"SERMO_OP":    c.sizeOp,
				"SERMO_VALUE": val,
			})
	}
	if c.permChange && cur.perm != prev.perm {
		w.fire(ctx, path, "permissions", fmt.Sprintf("%s permissions %04o -> %04o", path, prev.perm, cur.perm), map[string]string{
			"SERMO_OLD": fmt.Sprintf("%04o", prev.perm),
			"SERMO_NEW": fmt.Sprintf("%04o", cur.perm),
		})
	}
	if c.ownerChange && (cur.uid != prev.uid || cur.gid != prev.gid) {
		w.fire(ctx, path, "owner",
			fmt.Sprintf("%s owner %d:%d -> %d:%d", path, prev.uid, prev.gid, cur.uid, cur.gid), map[string]string{
				"SERMO_OLD": fmt.Sprintf("%d:%d", prev.uid, prev.gid),
				"SERMO_NEW": fmt.Sprintf("%d:%d", cur.uid, cur.gid),
			})
	}
}

// fire runs the watch's hook for one change and emits a matching event. A hook
// failure is reported but never aborts the cycle (other changes still fire).
func (w *fileWatcher) fire(ctx context.Context, path, change, msg string, extra map[string]string) {
	env := map[string]string{
		"SERMO_WATCH":      w.name,
		"SERMO_CHECK_TYPE": "file",
		"SERMO_PATH":       path,
		"SERMO_CHANGE":     change,
		"SERMO_MESSAGE":    msg,
	}
	for k, v := range extra {
		env[k] = v
	}
	if w.dryRun {
		w.emitEvent(Event{Watch: w.name, Kind: "dry-run", Message: watchDryRunMessage(w.hook, w.notifiers, nil) + ": " + msg})
		return
	}
	if w.inPanic != nil && w.inPanic() {
		w.emitEvent(Event{Watch: w.name, Kind: "panic-suppressed", Message: "panic mode: hook/notify suppressed: " + msg})
		return
	}
	if len(w.hook.Command) > 0 {
		runner := defaultHookRunner(w.runner)
		if err := w.hook.Run(ctx, runner, env); err != nil {
			w.emitEvent(Event{Watch: w.name, Kind: "hook-failed", Message: msg + ": " + err.Error()})
		} else {
			w.emitEvent(Event{Watch: w.name, Kind: "hook", Message: msg})
		}
	}
	dispatchNotify(ctx, w.notifiers, watchMessage(w.name, msg, env), w.name, w.emitEvent)
}

func (w *fileWatcher) emitEvent(e Event) {
	if w.emit != nil {
		w.emit(e)
	}
}
