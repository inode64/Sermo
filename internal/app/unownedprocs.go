package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/pkgdb"
	"sermo/internal/process"
)

const (
	unownedMessagePrefix   = "unowned processes"
	unownedStateSlot       = "unowned-processes"
	unownedProcessRowLimit = 100
	unownedAutoKillMessage = "kill"
)

// procRate is the previous CPU sample of a listed process, for its CPU%.
type procRate struct {
	cpuTicks uint64
	at       time.Time
}

// unownedWatcher is the daemon side: it samples the host, classifies, keeps
// per-incarnation alert state, publishes the rows and — only through its
// declared kill_only_if selector — signals the findings it is authorized to.
type unownedWatcher struct {
	setProcessWatch
	classifier *unownedClassifier
	kill       *killSpec // nil: alert-only
	killer     pidKiller
	rates      map[pidIncidentKey]procRate
	// pending holds the findings signalled this cycle whose escalation is owed.
	// They are escalated together after the incident pass so a host-wide scan
	// waits the grace periods once, not once per finding.
	pending []*pendingKill
}

// buildUnownedProcessesWatch builds the host watch. Like process_policy it
// repeats the validator's refusals so an unchecked Config cannot widen the
// action set; unlike it, then.kill is accepted when kill_only_if authorizes it.
func buildUnownedProcessesWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	if err := rejectSetWatchActions(entry, config.UnownedProcessesActions); err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	classifier, err := newUnownedClassifier(checkEntry, unownedRuntimeFromDeps(deps))
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		checkType:    checks.CheckTypeUnownedProcesses,
		parseKill:    true,
		emptyMessage: "then " + config.UnownedProcessesActions.EmptyThenMessage(),
	})
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	if actions.kill != nil && !actions.kill.selector.Configured() {
		// The validator requires it; an unchecked Config must not widen the kill.
		return nil, watchSubjectPrefix + name + ": then.kill requires " + process.StopPolicyKeyKillOnlyIf + " with users and exe_any"
	}
	level := watchSeverityFor(checks.CheckTypeUnownedProcesses, entry, checkEntry)
	w := &unownedWatcher{
		setProcessWatch: newSetProcessWatch(name, checks.CheckTypeUnownedProcesses, unownedStateSlot, entry, checkEntry, actions, deps, level),
		classifier:      classifier,
		kill:            actions.kill,
		killer:          newPIDKiller(deps, name, classifier.rt.resolve),
	}
	// The classifier samples through the watch's own sampler so a test fake
	// serves both the cycle and the kill's re-verification.
	w.classifier.rt.sampler = w.sampler
	watch := newStatefulWatch(name, checks.CheckTypeUnownedProcesses, entry, deps, interval, w.runCycle)
	watch.Severity = level
	return watch, ""
}

func (w *unownedWatcher) runCycle(ctx context.Context) {
	now := w.clock()
	scan, ok := w.classifier.scan(ctx, now)
	if !ok {
		w.publishUnavailable(unownedMessagePrefix, nil)
		return
	}
	if ctx.Err() != nil {
		return
	}
	rows := w.rows(scan, now)
	if scan.usersUnresolved(len(w.classifier.users)) {
		// The users filter matched no account, so every process was excluded:
		// a scan of nothing is a failure to look, not a clean host.
		w.publishResult(checks.Result{OK: false, Message: unownedScanMessage(scan), Data: unownedScanData(scan, rows), Severity: w.severity})
		return
	}
	w.publishResult(checks.Result{
		OK:       len(scan.findings) == 0,
		Message:  unownedScanMessage(scan),
		Data:     unownedScanData(scan, rows),
		Severity: w.severity,
	})

	if observeOnlyCycle(ctx) {
		return
	}
	w.settle(ctx, len(scan.findings), unownedMessagePrefix+": none left", nil)
	keys := incidentKeys(scan.findings, func(f unownedFinding) ProcInfo { return f.info })
	w.incidents.cycle(now, keys,
		func(i int, notifyNow bool) { w.fire(ctx, scan.findings[i], notifyNow) },
		func(i int) { w.remind(ctx, scan.findings[i]) })
	w.killer.escalate(ctx, w.pending)
	w.pending = nil
}

// rows turns the findings into the published rows, deriving each one's CPU%
// from its previous sample and remembering this one for the next cycle.
func (w *unownedWatcher) rows(scan unownedScan, now time.Time) []checks.UnownedProcess {
	next := make(map[pidIncidentKey]procRate, len(scan.findings))
	rows := make([]checks.UnownedProcess, 0, min(len(scan.findings), unownedProcessRowLimit))
	for i := range scan.findings {
		info := scan.findings[i].info
		key := info.incidentKey()
		next[key] = procRate{cpuTicks: info.CPUTicks, at: now}
		if len(rows) >= unownedProcessRowLimit {
			continue
		}
		row := checks.UnownedProcess{
			PID: info.PID, StartTicks: info.StartTicks, User: info.User, UID: info.UID,
			Exe: info.Exe, ExeResolved: info.ExeOK, ExePrevious: info.ExePrev, RSS: info.RSS,
			Reason: scan.findings[i].reason(),
		}
		if prev, ok := w.rates[key]; ok {
			row.CPU, row.HasCPU = cpuPercent(prev.cpuTicks, info.CPUTicks, prev.at, now)
		}
		row.CanKill, row.KillReason = manualKillable(info)
		rows = append(rows, row)
	}
	w.rates = next
	return rows
}

// unownedScanMessage is the one-line result: counts and caveats only. The
// findings themselves are the published rows; repeating them here would put
// the same list in the row summary, the expansion header and the table.
func unownedScanMessage(scan unownedScan) string {
	message := fmt.Sprintf("%s: %d scanned, %d unowned", unownedMessagePrefix, scan.scanned, len(scan.findings))
	var caveats []string
	if !scan.unitAttribution {
		caveats = append(caveats, unownedCaveatNoAttribution)
	}
	if !scan.initNamespace {
		caveats = append(caveats, unownedCaveatInitNamespace)
	}
	if scan.unreadableNS > 0 {
		caveats = append(caveats, fmt.Sprintf(unownedCaveatNamespaceFmt, scan.unreadableNS))
	}
	if !scan.sessionRecords {
		caveats = append(caveats, unownedCaveatNoSessions)
	}
	if len(scan.unresolvedUsers) > 0 {
		caveats = append(caveats, unownedCaveatUnresolved+strings.Join(scan.unresolvedUsers, displayListSeparator))
	}
	if scan.packageDB == pkgdb.BackendNone {
		caveats = append(caveats, unownedCaveatNoPackageDB)
	}
	if scan.packageError != "" {
		caveats = append(caveats, "package database: "+scan.packageError)
	}
	if len(caveats) > 0 {
		message += " (" + strings.Join(caveats, unownedCaveatSeparator) + ")"
	}
	return message
}

func unownedScanData(scan unownedScan, rows []checks.UnownedProcess) map[string]any {
	return map[string]any{
		checks.DataKeyScanned:         scan.scanned,
		checks.DataKeyViolationCount:  len(scan.findings),
		checks.DataKeyViolations:      unownedFindingList(scan.findings),
		checks.DataKeyPIDs:            limitedDisplayList(scan.findings, func(f unownedFinding) string { return strconv.Itoa(f.info.PID) }),
		checks.DataKeyUnitAttribution: scan.attribution(),
		checks.DataKeyPackageDB:       string(scan.packageDB),
		checks.DataKeyProcesses:       rows,
	}
}

func unownedFindingList(findings []unownedFinding) string {
	return limitedDisplayList(findings, unownedFindingText)
}

func unownedFindingText(f unownedFinding) string {
	message := fmt.Sprintf("pid %d: %s", f.info.PID, f.reason())
	if exe := ownershipExecutable(f.info); exe != "" {
		message += " (" + exe + ")"
	}
	return message
}

func (w *unownedWatcher) message(f unownedFinding) (string, map[string]string) {
	message := unownedMessagePrefix + ": " + unownedFindingText(f)
	extra := map[string]string{sermoEnvPID: strconv.Itoa(f.info.PID)}
	if f.info.User != "" {
		extra[sermoEnvUser] = f.info.User
	}
	return message, w.env(message, extra)
}

// authorizedKill reports whether the declared selector lets the watch signal
// this finding on its own. Detection alone never does.
func (w *unownedWatcher) authorizedKill(f unownedFinding) bool {
	return w.kill != nil && w.kill.selector.Killable(f.info.asProcess(), w.classifier.rt.resolve)
}

// fire records a new finding and runs its actions through the shared
// dispatcher: dry run and panic mode suppress everything, the kill runs only
// for an authorized finding, and the notification goes out when notifyNow.
func (w *unownedWatcher) fire(ctx context.Context, f unownedFinding, notifyNow bool) {
	message, env := w.message(f)
	w.emitEvent(Event{Watch: w.name, Kind: eventKindFiring, Severity: w.severity, Message: message})
	killable := w.authorizedKill(f)
	if !killable {
		if notifyNow {
			w.notify(ctx, message, env)
		}
		return
	}
	var notifiers []notify.Notifier
	if notifyNow {
		notifiers = w.notifiers
	}
	spec := watchFireSpec{
		name:        w.name,
		notifiers:   notifiers,
		inPanic:     w.inPanic,
		dryRun:      w.dryRun,
		emit:        w.emitEvent,
		dryRunLabel: watchDryRunMessage(HookSpec{}, notifiers, config.WatchThenKeyKill),
		panicLabel:  "panic mode: notify/kill suppressed",
		severity:    w.severity,
		action: func() {
			// The first signal goes out now; the escalation joins the cycle's batch.
			resample := func(pid int) (ProcInfo, bool) { return w.classifier.current(ctx, w.clock(), pid) }
			pending, owed := w.killer.signal(ctx, killTarget{info: f.info, spec: *w.kill, resample: resample, msg: unownedAutoKillMessage})
			if owed {
				w.pending = append(w.pending, &pending)
			}
		},
	}
	if dispatchWatchFire(ctx, spec, message, env) && len(notifiers) > 0 {
		w.incidents.announce()
	}
}

func (w *unownedWatcher) remind(ctx context.Context, f unownedFinding) {
	message, env := w.message(f)
	w.notify(ctx, message, env)
}
