package app

import (
	"context"
	"fmt"
	"strconv"
	"syscall"

	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/web"
)

const (
	unownedKillAction        = "kill"
	unownedKillLockPrefix    = "watch-"
	unownedManualKillMessage = "manual kill"
)

// unownedProcessRows projects the latest unowned_processes snapshot's rows for
// the dashboard. Any other watch type, a stale snapshot or one from another
// config generation yields nothing, so the table never shows processes the
// current watch did not list.
func (o watchObservation) unownedProcessRows(w *webWatch) []web.WatchProcess {
	if w.checkType != checks.CheckTypeUnownedProcesses || w.disabled {
		return nil
	}
	for _, snap := range o.snapshots {
		if o.watchSnapshotCurrent(w, snap) {
			return checks.UnownedProcessesFromData(snap.Data)
		}
	}
	return nil
}

// KillWatchProcess signals one process an unowned_processes watch listed. The
// request carries the PID and start ticks the dashboard displayed; they are
// evidence to revalidate, never authority. The backend re-samples the host,
// requires the same incarnation, an exact executable and that the process is
// still classified unowned by the watch's own rules, and only then signals it
// through the shared kill gate with a selector bound to that verified identity.
// A dry-run watch records what would be sent and signals nothing.
func (b *WebBackend) KillWatchProcess(ctx context.Context, name string, req web.WatchProcessKillRequest) web.ActionResult {
	w := b.watches[name]
	if w == nil {
		return b.unownedKillRefusal(name, fmt.Sprintf(unknownWatchMessageFmt, name))
	}
	if w.disabled || w.checkType != checks.CheckTypeUnownedProcesses {
		return b.unownedKillRefusal(name, fmt.Sprintf("watch %q is not an enabled unowned_processes watch", name))
	}
	if req.PID <= 0 || req.StartTicks == 0 {
		return b.unownedKillRefusal(name, "a process kill needs the listed pid and start_ticks")
	}
	classifier, err := newUnownedClassifier(w.check, b.unowned)
	if err != nil {
		return b.unownedKillRefusal(name, err.Error())
	}
	started := false
	ok, message := runLockedControl(ctx, b.cfg.Global.RuntimeDir(), unownedKillLockPrefix+name, "watch "+name, b.operationTimeout, func(opCtx context.Context) (bool, string) {
		started = true
		result := b.killUnownedProcess(opCtx, w, classifier, req)
		return result.OK, result.Message
	})
	if !started {
		return b.unownedKillRefusal(name, message)
	}
	return web.ActionResult{OK: ok, Message: message}
}

func (b *WebBackend) killUnownedProcess(ctx context.Context, w *webWatch, classifier *unownedClassifier, req web.WatchProcessKillRequest) web.ActionResult {
	info, present := classifier.current(ctx, b.webNow(), req.PID)
	if !present {
		return b.unownedKillRefusal(w.name, fmt.Sprintf("pid %d is no longer listed as unowned; refresh and try again", req.PID))
	}
	if info.StartTicks != req.StartTicks {
		return b.unownedKillRefusal(w.name, fmt.Sprintf("pid %d is now another process; refresh and try again", req.PID))
	}
	if killable, reason := manualKillable(info); !killable {
		return b.unownedKillRefusal(w.name, fmt.Sprintf("pid %d cannot be signalled: %s", req.PID, reason))
	}
	spec := manualKillSpec(req.Escalate)
	if w.dryRun {
		message := fmt.Sprintf("%s: would send %s to pid %d (%s)", watchDryRunMessagePrefix+unownedKillAction, signalLabel(spec), req.PID, info.Exe)
		emitSafe(b.emit, Event{Watch: w.name, Kind: eventKindDryRun, Action: unownedKillAction, Message: message})
		return web.ActionResult{OK: true, Message: message}
	}
	spec.selector = manualKillSelector(info)
	outcome := b.unownedKiller.forWatch(w.name).kill(ctx, killTarget{
		info: info, spec: spec, msg: unownedManualKillMessage,
		resample: func(pid int) (ProcInfo, bool) { return classifier.current(ctx, b.webNow(), pid) },
	})
	return web.ActionResult{OK: outcome.signalled && !outcome.survived, Message: outcome.message}
}

// unownedKillRefusal records a rejection before signalling is attempted. Once
// attempted, the shared kill path owns both success and failure events.
func (b *WebBackend) unownedKillRefusal(name, message string) web.ActionResult {
	b.emitWatchMonitorEvent(name, unownedKillAction, eventKindError, eventStatusFailed, message)
	return web.ActionResult{Message: message}
}

// manualKillSpec is how the dashboard signals: SIGTERM, escalated to SIGKILL
// only when the operator asked, with the watch kill action's default graces.
func manualKillSpec(escalate bool) killSpec {
	return killSpec{signal: syscall.SIGTERM, escalate: escalate, termTimeout: defaultWatchKillTermTimeout, killTimeout: defaultWatchKillTimeout}
}

// manualKillSelector binds a verified identity into a selector: the process's
// own real uid and exact executable, nothing wider. The uid goes in numerically
// so the kill gate compares the number the sampler read, and no name lookup —
// which may use another resolver than the sampler's — can disagree with it.
func manualKillSelector(info ProcInfo) process.KillSelector {
	return process.NewKillSelector([]string{strconv.FormatUint(uint64(info.UID), 10)}, []string{info.Exe})
}

func signalLabel(spec killSpec) string {
	if spec.escalate {
		return process.SignalName(spec.signal) + ", then SIGKILL after " + spec.termTimeout.String()
	}
	return process.SignalName(spec.signal)
}
