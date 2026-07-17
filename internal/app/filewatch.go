package app

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/units"
)

// fileCond is the set of attribute conditions a file watch evaluates per path.
// An empty set is invalid (rejected at build time).
type fileCond struct {
	sizeChange  bool          // fire when size differs from the previous cycle
	sizeOp      string        // edge-triggered size threshold op ("" = none)
	sizeValue   float64       // threshold comparand
	permChange  bool          // fire when the permission bits change
	ownerChange bool          // fire when the owning uid/gid changes
	onDelete    bool          // fire when a previously-seen path stops existing
	olderThan   time.Duration // fire when the modification age crosses this threshold
}

func (c fileCond) any() bool {
	return c.sizeChange || c.sizeOp != "" || c.permChange || c.ownerChange || c.onDelete || c.olderThan > 0
}

// fileState is the remembered attributes of one path across cycles.
type fileState struct {
	size       int64
	kind       string
	perm       uint32 // st_mode & 07777: permission bits plus setuid/setgid/sticky
	uid, gid   uint32
	breached   bool // previous size-threshold result, for edge detection
	modifiedAt time.Time
	age        time.Duration
	older      bool
	olderFired bool
}

const (
	fileChangeDeleted       = "deleted"
	fileChangeSize          = "size"
	fileChangeSizeThreshold = "size_threshold"
	fileChangePermissions   = "permissions"
	fileChangeOwner         = "owner"
	fileChangeOlderThan     = "older_than"

	fileStatePermMask = 0o7777
)

// fileWatcher monitors configured files/directories (optionally their whole
// subtrees) for attribute changes and modification age. It is stateful: it
// remembers each path's attributes across cycles and reports only transitions.
// The baseline is adopted silently on first sight except for already-stale paths.
type fileWatcher struct {
	name          string
	paths         []string
	recursive     bool
	includeHidden bool
	cond          fileCond
	summary       string
	check         map[string]any
	hook          HookSpec
	notifiers     []notify.Notifier
	dryRun        bool
	inPanic       func() bool
	runner        HookRunner
	emit          func(Event)
	publish       func(string, string, checks.Result)
	now           func() time.Time

	baseline    map[string]fileState
	numberFiles int
}

// runCycle scans the watched paths, compares each against its baseline, and
// fires one hook (and emits one event) per detected change or age breach. It is
// the Watch.Cycle implementation for a file watch.
func (w *fileWatcher) runCycle(ctx context.Context) {
	if w.baseline == nil {
		w.baseline = map[string]fileState{}
	}
	now := w.clock()
	current := w.scan(now)
	w.numberFiles = fileWatchNumberFiles(current)
	defer w.publishSnapshot(current)

	paths := make([]string, 0, len(current))
	for p := range current {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic event order

	var stale []staleFile
	for _, p := range paths {
		if ctx.Err() != nil {
			return // shutting down: stop firing
		}
		cur := current[p]
		prev, known := w.baseline[p]
		if known && cur.older && prev.older {
			cur.olderFired = prev.olderFired
		}
		if !observeOnlyCycle(ctx) && cur.older && !cur.olderFired {
			stale = append(stale, staleFile{path: p, state: cur})
			cur.olderFired = true
		}
		w.baseline[p] = cur
		if known && !observeOnlyCycle(ctx) {
			w.diff(ctx, p, prev, cur)
		}
	}
	w.fireOlderThanBatch(ctx, stale)

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
		if w.cond.onDelete && !observeOnlyCycle(ctx) {
			w.fire(ctx, p, fileChangeDeleted, p+" no longer exists", map[string]string{
				sermoEnvOld: strconv.FormatInt(w.baseline[p].size, envFormatBase),
			})
		}
		delete(w.baseline, p)
	}
}

func (w *fileWatcher) publishSnapshot(current map[string]fileState) {
	if w.publish == nil {
		return
	}
	if len(current) == 0 {
		result := checks.Result{
			Check:   w.name,
			OK:      false,
			Message: "file watch: no configured path found",
			Data:    map[string]any{checks.DataKeyPaths: w.paths},
		}
		w.publish(w.name, checks.CheckTypeFile, checks.ApplySummary(w.summary, w.check, result))
		return
	}
	root := firstFileWatchRoot(w.paths, current)
	data := map[string]any{
		checks.DataKeyPaths:      w.paths,
		checks.DataKeyKind:       root.kind,
		checks.DataKeySize:       root.size,
		checks.DataKeyMode:       fmt.Sprintf(fileModeFormat, root.perm),
		checks.CheckKeyOwner:     fmt.Sprintf(fileOwnerFormat, root.uid, root.gid),
		checks.DataKeyModifiedAt: root.modifiedAt.UTC().Format(time.RFC3339),
	}
	if len(w.paths) == 1 {
		data[checks.DataKeyPath] = w.paths[0]
	}
	if w.cond.olderThan > 0 {
		data[checks.DataKeyAge] = units.HumanizeDuration(root.age.Round(time.Second))
	}
	if w.recursive {
		entries := max(len(current)-1, 0)
		data[watchReadingFieldEntries] = entries
	}
	result := checks.Result{
		Check:   w.name,
		OK:      true,
		Message: fmt.Sprintf("%s size %d", firstFileWatchPath(w.paths), root.size),
		Data:    data,
	}
	if w.summary != "" {
		data[checks.DataKeyNumberFiles] = w.numberFiles
		if w.cond.olderThan > 0 {
			data[checks.DataKeyTrigger] = fileChangeOlderThan
			data[checks.DataKeyValue] = root.age
		}
	}
	w.publish(w.name, checks.CheckTypeFile, checks.ApplySummary(w.summary, w.check, result))
}

func fileWatchNumberFiles(current map[string]fileState) int {
	count := 0
	for _, state := range current {
		if state.kind == checks.CountKindFile {
			count++
		}
	}
	return count
}

// scan returns the current attributes of the watched path and, when recursive
// and the path is a directory, every entry in its subtree. Symlinks are stat'd
// as links (never followed), so a link is watched as itself, not its target.
func (w *fileWatcher) scan(now time.Time) map[string]fileState {
	out := map[string]fileState{}
	for _, root := range w.paths {
		info, err := os.Lstat(root)
		if err != nil {
			continue // missing roots are handled as deletions against the baseline
		}
		out[root] = w.stateOf(info, now)
		if w.recursive && info.IsDir() {
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
				if p == root {
					return nil // skip the root; it was already added above
				}
				if walkErr != nil {
					return nil //nolint:nilerr // unreadable entries are skipped during best-effort recursive scans
				}
				if !w.includeHidden && checks.IsHiddenDescendant(root, p, d) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				fi, infoErr := d.Info()
				if infoErr != nil {
					return nil //nolint:nilerr // entries that disappear during the scan are skipped
				}
				out[p] = w.stateOf(fi, now)
				return nil
			})
		}
	}
	return out
}

func (w *fileWatcher) stateOf(info fs.FileInfo, now time.Time) fileState {
	st := fileState{size: info.Size(), kind: checks.FileKind(info.Mode()), modifiedAt: info.ModTime()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		st.perm = uint32(sys.Mode) & fileStatePermMask
		st.uid, st.gid = sys.Uid, sys.Gid
	}
	if w.cond.sizeOp != "" {
		st.breached = cfgval.CompareFloat(float64(st.size), w.cond.sizeOp, w.cond.sizeValue)
	}
	if w.cond.olderThan > 0 {
		st.age = now.Sub(st.modifiedAt)
		st.older = st.age > w.cond.olderThan
	}
	return st
}

// diff fires a hook for each configured attribute that changed between prev and
// cur for one path.
func (w *fileWatcher) diff(ctx context.Context, path string, prev, cur fileState) {
	c := w.cond
	if c.sizeChange && cur.size != prev.size {
		w.fire(ctx, path, fileChangeSize, fmt.Sprintf("%s size %d -> %d", path, prev.size, cur.size), map[string]string{
			sermoEnvOld:  strconv.FormatInt(prev.size, envFormatBase),
			sermoEnvNew:  strconv.FormatInt(cur.size, envFormatBase),
			sermoEnvSize: strconv.FormatInt(cur.size, envFormatBase),
		})
	}
	// Edge-triggered: fire only when the threshold is newly crossed.
	if c.sizeOp != "" && cur.breached && !prev.breached {
		val := strconv.FormatFloat(c.sizeValue, envFloatFormat, envFloatPrecisionAuto, envFloatBits)
		w.fire(ctx, path, fileChangeSizeThreshold,
			fmt.Sprintf("%s size %d %s %s", path, cur.size, c.sizeOp, val), map[string]string{
				sermoEnvSize:  strconv.FormatInt(cur.size, envFormatBase),
				sermoEnvOp:    c.sizeOp,
				sermoEnvValue: val,
			})
	}
	if c.permChange && cur.perm != prev.perm {
		w.fire(ctx, path, fileChangePermissions, fmt.Sprintf("%s permissions %04o -> %04o", path, prev.perm, cur.perm), map[string]string{
			sermoEnvOld: fmt.Sprintf(fileModeFormat, prev.perm),
			sermoEnvNew: fmt.Sprintf(fileModeFormat, cur.perm),
		})
	}
	if c.ownerChange && (cur.uid != prev.uid || cur.gid != prev.gid) {
		w.fire(ctx, path, fileChangeOwner,
			fmt.Sprintf("%s owner %d:%d -> %d:%d", path, prev.uid, prev.gid, cur.uid, cur.gid), map[string]string{
				sermoEnvOld: fmt.Sprintf(fileOwnerFormat, prev.uid, prev.gid),
				sermoEnvNew: fmt.Sprintf(fileOwnerFormat, cur.uid, cur.gid),
			})
	}
}

// fileOlderThanListedPaths bounds how many stale paths an aggregated
// older-than event names; the rest collapse into a "(+N more)" suffix.
const fileOlderThanListedPaths = 5

// staleFile is one path that crossed older_than this cycle.
type staleFile struct {
	path  string
	state fileState
}

func (w *fileWatcher) fireOlderThan(ctx context.Context, path string, cur fileState) {
	w.fire(ctx, path, fileChangeOlderThan, w.olderThanMessage(path, cur), w.olderThanExtra(cur))
}

// fireOlderThanBatch fires the paths that crossed older_than this cycle. A
// single path keeps the classic per-file fire; several paths run the hook once
// per file (its SERMO_PATH contract) but emit ONE aggregated event and
// notification, so a directory of stale files cannot burst identical events
// with the same timestamp.
func (w *fileWatcher) fireOlderThanBatch(ctx context.Context, stale []staleFile) {
	if len(stale) == 0 {
		return
	}
	if len(stale) == 1 {
		w.fireOlderThan(ctx, stale[0].path, stale[0].state)
		return
	}
	if len(w.hook.Command) > 0 && !w.dryRun && !w.panicking() {
		for _, s := range stale {
			if ctx.Err() != nil {
				return
			}
			w.runOlderThanHook(ctx, s.path, s.state)
		}
	}
	msg := w.olderThanBatchMessage(stale)
	env := map[string]string{
		sermoEnvWatch:     w.name,
		sermoEnvCheckType: checks.CheckTypeFile,
		sermoEnvChange:    fileChangeOlderThan,
		sermoEnvMessage:   msg,
		sermoEnvValue:     w.cond.olderThan.String(),
	}
	// The hook already ran per file above, so the aggregated dispatch carries
	// no hook of its own.
	dispatchWatchFire(ctx, watchFireSpec{
		name:        w.name,
		runner:      w.runner,
		notifiers:   w.notifiers,
		inPanic:     w.inPanic,
		dryRun:      w.dryRun,
		emit:        w.emitEvent,
		dryRunLabel: watchDryRunMessage(w.hook, w.notifiers, nil),
		panicLabel:  "panic mode: hook/notify suppressed",
	}, msg, env)
}

func (w *fileWatcher) runOlderThanHook(ctx context.Context, path string, cur fileState) {
	msg := w.summaryMessage(path, fileChangeOlderThan, w.olderThanMessage(path, cur), w.olderThanExtra(cur))
	env := map[string]string{
		sermoEnvWatch:     w.name,
		sermoEnvCheckType: checks.CheckTypeFile,
		sermoEnvPath:      path,
		sermoEnvChange:    fileChangeOlderThan,
		sermoEnvMessage:   msg,
	}
	maps.Copy(env, w.olderThanExtra(cur))
	runner := defaultHookRunner(w.runner)
	if err := w.hook.Run(ctx, runner, env); err != nil {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindHookFail, Message: msg + ": " + err.Error()})
	} else {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindHook, Message: msg})
	}
}

func (w *fileWatcher) olderThanBatchMessage(stale []staleFile) string {
	listed := make([]string, 0, min(len(stale), fileOlderThanListedPaths))
	for i := range min(len(stale), fileOlderThanListedPaths) {
		listed = append(listed, stale[i].path)
	}
	msg := fmt.Sprintf("%d files older than %s: %s", len(stale), units.HumanizeDuration(w.cond.olderThan), strings.Join(listed, ", "))
	if extra := len(stale) - len(listed); extra > 0 {
		msg += fmt.Sprintf(" (+%d more)", extra)
	}
	return msg
}

func (w *fileWatcher) olderThanMessage(path string, cur fileState) string {
	return fmt.Sprintf("%s was modified at %s and is older than %s", path, cur.modifiedAt.UTC().Format(time.RFC3339), units.HumanizeDuration(w.cond.olderThan))
}

func (w *fileWatcher) olderThanExtra(cur fileState) map[string]string {
	return map[string]string{
		sermoEnvModifiedAt: cur.modifiedAt.UTC().Format(time.RFC3339),
		sermoEnvAgeSeconds: strconv.FormatInt(int64(cur.age.Seconds()), envFormatBase),
		sermoEnvValue:      w.cond.olderThan.String(),
	}
}

func (w *fileWatcher) panicking() bool {
	return w.inPanic != nil && w.inPanic()
}

func (w *fileWatcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func firstFileWatchRoot(paths []string, current map[string]fileState) fileState {
	for _, path := range paths {
		if state, ok := current[path]; ok {
			return state
		}
	}
	for _, state := range current {
		return state
	}
	return fileState{}
}

func firstFileWatchPath(paths []string) string {
	if len(paths) == 0 {
		return "file"
	}
	return paths[0]
}

// fire runs the watch's hook for one change and emits a matching event. A hook
// failure is reported but never aborts the cycle (other changes still fire).
func (w *fileWatcher) fire(ctx context.Context, path, change, msg string, extra map[string]string) {
	msg = w.summaryMessage(path, change, msg, extra)
	env := map[string]string{
		sermoEnvWatch:     w.name,
		sermoEnvCheckType: checks.CheckTypeFile,
		sermoEnvPath:      path,
		sermoEnvChange:    change,
		sermoEnvMessage:   msg,
	}
	maps.Copy(env, extra)
	dispatchWatchFire(ctx, watchFireSpec{
		name:        w.name,
		hook:        w.hook,
		runner:      w.runner,
		notifiers:   w.notifiers,
		inPanic:     w.inPanic,
		dryRun:      w.dryRun,
		emit:        w.emitEvent,
		dryRunLabel: watchDryRunMessage(w.hook, w.notifiers, nil),
		panicLabel:  "panic mode: hook/notify suppressed",
	}, msg, env)
}

func (w *fileWatcher) summaryMessage(path, change, message string, extra map[string]string) string {
	if w.summary == "" {
		return message
	}
	data := map[string]any{
		checks.DataKeyPath:        path,
		checks.DataKeyTrigger:     change,
		checks.DataKeyNumberFiles: w.numberFiles,
	}
	addSummaryAge(data, extra)
	if size, ok := extra[sermoEnvSize]; ok {
		if value, err := strconv.ParseInt(size, envFormatBase, envFloatBits); err == nil {
			data[checks.DataKeySize] = value
			data[checks.DataKeyValue] = value
		}
	}
	if value, ok := extra[sermoEnvValue]; ok {
		if _, found := data[checks.DataKeyValue]; !found {
			data[checks.DataKeyValue] = value
		}
	}
	return checks.ApplySummary(w.summary, w.check, checks.Result{Check: w.name, Message: message, Data: data}).Message
}

func (w *fileWatcher) emitEvent(e Event) {
	if w.emit != nil {
		w.emit(e)
	}
}
