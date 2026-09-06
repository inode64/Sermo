package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/units"
	"sermo/internal/web"
)

func (b *WebBackend) watchCheckDeps() checks.Deps {
	return watchInlineDeps(Deps{
		DefaultTimeout: b.defaultTimeout,
		ExecxRunner:    b.execRunner,
		RaidSampler:    b.raidSampler,
	})
}

// probeWatchResult runs one fresh standalone sample. It deliberately does not
// use the configured Watch instance, so a manual probe cannot alter a
// stateful check's scheduler baseline or dispatch watch rules/actions.
func (b *WebBackend) probeWatchResult(ctx context.Context, w *webWatch) (checks.Result, error) {
	if w == nil || len(w.check) == 0 {
		return checks.Result{}, errors.New("watch has no check configuration")
	}
	if w.checkType == checks.CheckTypeSmart {
		return b.startSmartShortTest(ctx, w)
	}
	check, err := checks.BuildInline(w.name, maps.Clone(w.check), b.watchCheckDeps())
	if err != nil {
		return checks.Result{}, fmt.Errorf("build check: %w", err)
	}
	probeCtx, cancel := b.probeContext(ctx, w.check)
	defer cancel()
	if w.checkType == checks.CheckTypeDiskIO {
		return b.probeDiskIORates(probeCtx, check)
	}
	return check.Run(probeCtx), nil
}

// probeDiskIORates opens the window a disk I/O sample needs. Its rates are the
// delta between two readings of cumulative counters, so a single run only
// baselines and reports "diskio <device> baseline" with no rates at all — which
// is why a manual probe of one used to answer nothing. Sampling twice around a
// bounded pause gives the operator a real window. The check instance is the
// standalone one built for this probe, so the scheduler's own baseline is
// untouched, and an idle disk honestly reports zeroes rather than silence.
func (b *WebBackend) probeDiskIORates(ctx context.Context, check checks.Check) (checks.Result, error) {
	if baseline := check.Run(ctx); baseline.Unavailable {
		return baseline, nil
	}
	select {
	case <-ctx.Done():
		return checks.Result{}, fmt.Errorf("sample disk I/O rate window: %w", ctx.Err())
	case <-time.After(b.diskIOProbeWindow()):
	}
	return check.Run(ctx), nil
}

// diskIOProbeWindow is how long a manual disk I/O probe watches the counters:
// long enough for a busy device to register, short enough to answer a click.
func (b *WebBackend) diskIOProbeWindow() time.Duration {
	if b.diskIOWindow > 0 {
		return b.diskIOWindow
	}
	return defaultDiskIOProbeWindow
}

func (b *WebBackend) startSmartShortTest(ctx context.Context, w *webWatch) (checks.Result, error) {
	device := cfgval.String(w.check[checks.CheckKeyDevice])
	if device == "" {
		return checks.Result{}, errors.New("smart check requires a device")
	}
	probeCtx, cancel := b.probeContext(ctx, w.check)
	defer cancel()
	if err := checks.StartSmartShortTest(probeCtx, b.execRunner, device, b.probeTimeout(w.check)); err != nil {
		return checks.Result{}, fmt.Errorf("start SMART short self-test on %s: %w", device, err)
	}
	message := fmt.Sprintf("smart %s short self-test started", device)
	return checks.Result{
		Check:   w.name,
		OK:      true,
		Message: message,
		Data: map[string]any{
			checks.DataKeyDevice:      device,
			checks.DataKeyDeviceState: checks.DeviceStateTesting,
			checks.DataKeyResult:      "short self-test started",
		},
	}, nil
}

// defaultDiskIOProbeWindow is the rate window a manual disk I/O probe opens when
// the backend declares none.
const defaultDiskIOProbeWindow = 2 * time.Second

func watchErrorReadings(message string) []web.WatchReading {
	return []web.WatchReading{{Field: watchReadingFieldSample, Label: watchReadingLabelSample, Error: message}}
}

// probeTimeout bounds one manual probe. The check's own `timeout:` is the budget
// the operator declared for exactly this work, so it wins here as it does in the
// daemon cycle; engine.default_timeout is only the fallback for a check that
// declares none. Taking the smaller of the two used to cancel a slow probe early
// and then report the configured deadline, so a `timeout: 30s` hdparm watch
// failed at ten seconds claiming thirty — and every manual probe of a spinning
// disk failed while its scheduled cycle succeeded.
func (b *WebBackend) probeTimeout(check map[string]any) time.Duration {
	return checkProbeTimeout(check, b.defaultTimeout, b.operationTimeout)
}

func (b *WebBackend) probeContext(parent context.Context, check map[string]any) (context.Context, context.CancelFunc) {
	timeout := b.probeTimeout(check)
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// beginWatchProbe marks one manual probe as active. A watch accepts only one
// such probe at a time: executing hdparm, SMART or LVM twice concurrently is
// unhelpful and needlessly adds load to the host.
func (b *WebBackend) beginWatchProbe(name string) (time.Time, bool) {
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	if b.probes == nil {
		b.probes = map[string]time.Time{}
	}
	if startedAt, found := b.probes[name]; found {
		return startedAt, false
	}
	startedAt := b.webNow()
	b.probes[name] = startedAt
	return startedAt, true
}

func (b *WebBackend) finishWatchProbe(name string) {
	b.probeMu.Lock()
	delete(b.probes, name)
	b.probeMu.Unlock()
}

func (b *WebBackend) watchProbeStartedAt(name string) (time.Time, bool) {
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	startedAt, found := b.probes[name]
	return startedAt, found
}

func manualProbeCompletedMessage(summary string, duration time.Duration) string {
	return fmt.Sprintf("manual probe completed in %s: %s", units.HumanizeDuration(duration), summary)
}

func manualProbeFailedMessage(summary string, duration time.Duration) string {
	return fmt.Sprintf("manual probe failed after %s: %s", units.HumanizeDuration(duration), summary)
}

// ProbeWatch runs and records a fresh check instance for a supported host watch.
// It does not dispatch watch actions, so an operator's manual probe cannot alter
// the scheduler's stateful baseline or trigger a rule, notification or remediation.
func (b *WebBackend) ProbeWatch(ctx context.Context, name string) web.ActionResult {
	w := b.watches[name]
	if w == nil {
		return web.ActionResult{Message: fmt.Sprintf(unknownWatchMessageFmt, name)}
	}
	if w.disabled || w.serviceScoped || !ManualProbeCheckType(w.checkType) {
		return web.ActionResult{Message: fmt.Sprintf("watch %q does not support manual probing", name)}
	}
	startedAt, started := b.beginWatchProbe(name)
	if !started {
		return web.ActionResult{Message: "manual probe already running since " + startedAt.Format(time.RFC3339)}
	}
	b.emitWatchMonitorEvent(name, eventActionProbe, eventKindAction, eventStatusRunning, eventMessageManualProbeStarted)
	defer b.finishWatchProbe(name)
	result, err := b.probeWatchResult(ctx, w)
	duration := max(b.webNow().Sub(startedAt), 0)
	if err != nil {
		summary := w.checkType + ": " + err.Error()
		b.emitWatchMonitorEvent(name, eventActionProbe, eventKindError, eventStatusFailed, manualProbeFailedMessage(summary, duration))
		return web.ActionResult{Message: summary, Readings: watchErrorReadings(err.Error())}
	}
	if b.watchSnapshots != nil {
		b.watchSnapshots.publishConfigured(name, w.checkType, result, w.configID)
	}
	snap := CheckSnapshot{
		Observation: result.Observation(), OK: result.OK, Condition: result.Condition,
		Optional: result.Optional, Skipped: result.Skipped, Unavailable: result.Unavailable,
		Message: result.Message, Data: result.Data, Severity: result.Severity,
	}
	severity := checks.ResolveSeverity(result.Severity, w.severityFor(cfgval.String(result.Data[checks.DataKeyMetric])))
	// A manual probe reports through an event message, not through a panel with a
	// gauge beside it, so the result line is the whole answer here.
	readings := watchSnapshotReadings(w.checkType, severity, snap, false)
	summary := watchSnapshotSummary(snap, readings)
	ok := result.Healthy()
	kind, status := eventKindAction, eventStatusOK
	eventMessage := manualProbeCompletedMessage(summary, duration)
	graded := ""
	if !ok {
		// A manual probe of an advisory watch reports the same way its cycle does,
		// and says so to its caller: announcing it as a failure would contradict
		// the amber the same result gets everywhere else.
		kind, status = eventKindError, eventStatusFailed
		if checks.IsWarning(severity) {
			kind, graded = eventKindWarning, severity
		}
		eventMessage = manualProbeFailedMessage(summary, duration)
	}
	b.emitWatchMonitorEvent(name, eventActionProbe, kind, status, eventMessage)
	return web.ActionResult{OK: ok, Message: summary, Readings: readings, Severity: graded}
}

// ManualProbeCheckType reports whether a watch of this check type answers a
// manual probe. It is exported because sermoctl gates the same command on the
// same list: two copies would drift, and the operator would be told a probe is
// unsupported by one and rejected by the other.
func ManualProbeCheckType(checkType string) bool {
	switch checkType {
	case checks.CheckTypeHdparm, checks.CheckTypeLVM, checks.CheckTypeRAID, checks.CheckTypeSmart,
		checks.CheckTypeStorCLI, checks.CheckTypeSSACLI, checks.CheckTypeDiskIO:
		return true
	default:
		return false
	}
}

// watchLastCheckedAt is the newest sample produced by this watch's current
// check configuration. Samples from a previous target are ignored even if
// they are still within the freshness window.
func (b *WebBackend) watchLastCheckedAt(w *webWatch) time.Time {
	if b.watchSnapshots == nil || w == nil {
		return time.Time{}
	}
	var latest time.Time
	for _, snap := range b.watchSnapshots.Get(w.name, w.checkType) {
		if snap.Ran && snapshotConfigMatches(w.configID, snap.ConfigID) && watchSnapshotMetricConfigured(w, snap) && snap.At.After(latest) {
			latest = snap.At
		}
	}
	return latest
}
