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
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	return check.Run(probeCtx), nil
}

func (b *WebBackend) startSmartShortTest(ctx context.Context, w *webWatch) (checks.Result, error) {
	device := cfgval.String(w.check[checks.CheckKeyDevice])
	if device == "" {
		return checks.Result{}, errors.New("smart check requires a device")
	}
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	if err := checks.StartSmartShortTest(probeCtx, b.execRunner, device, b.probeTimeout()); err != nil {
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

func watchErrorReadings(message string) []web.WatchReading {
	return []web.WatchReading{{Field: watchReadingFieldSample, Label: watchReadingLabelSample, Error: message}}
}

func (b *WebBackend) probeTimeout() time.Duration {
	timeout := b.defaultTimeout
	if timeout <= 0 {
		timeout = b.operationTimeout
	}
	return timeout
}

func (b *WebBackend) probeContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := b.probeTimeout()
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
	if w.disabled || w.serviceScoped || !manualProbeCheckType(w.checkType) {
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
		b.watchSnapshots.Publish(name, w.checkType, result)
	}
	snap := CheckSnapshot{OK: result.OK, Condition: result.Condition, Optional: result.Optional, Skipped: result.Skipped, Message: result.Message, Data: result.Data}
	readings := watchSnapshotReadings(w.checkType, snap)
	summary := watchSnapshotSummary(snap, readings)
	ok := result.Healthy()
	kind, status := eventKindAction, eventStatusOK
	eventMessage := manualProbeCompletedMessage(summary, duration)
	if !ok {
		kind, status = eventKindError, eventStatusFailed
		eventMessage = manualProbeFailedMessage(summary, duration)
	}
	b.emitWatchMonitorEvent(name, eventActionProbe, kind, status, eventMessage)
	return web.ActionResult{OK: ok, Message: summary, Readings: readings}
}

func manualProbeCheckType(checkType string) bool {
	switch checkType {
	case checks.CheckTypeHdparm, checks.CheckTypeLVM, checks.CheckTypeRAID, checks.CheckTypeSmart:
		return true
	default:
		return false
	}
}

func (b *WebBackend) watchLastCheckedAt(w *webWatch) time.Time {
	if b.watchSnapshots == nil || w == nil {
		return time.Time{}
	}
	var latest time.Time
	for _, snap := range b.watchSnapshots.Get(w.name, w.checkType) {
		if snap.Ran && watchSnapshotMetricConfigured(w, snap) && snap.At.After(latest) {
			latest = snap.At
		}
	}
	return latest
}
