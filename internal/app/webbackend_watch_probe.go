package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/web"
)

func (b *WebBackend) watchCheckDeps() checks.Deps {
	return watchInlineDeps(Deps{
		DefaultTimeout:       b.defaultTimeout,
		ExecxRunner:          b.execRunner,
		StorageUsage:         b.storageUsage,
		MountSampler:         b.mountSampler,
		NetSampler:           b.netSampler,
		PingSampler:          b.pingSampler,
		OomSampler:           b.oomSampler,
		FdsSampler:           b.fdsSampler,
		PidsSampler:          b.pidsSampler,
		PressureSampler:      b.pressureSampler,
		ConntrackSampler:     b.conntrackSampler,
		EntropySampler:       b.entropySampler,
		ZombieSampler:        b.zombieSampler,
		DiskIOSampler:        b.diskIOSampler,
		SensorSampler:        b.sensorSampler,
		RaidSampler:          b.raidSampler,
		EdacSampler:          b.edacSampler,
		RouteSampler:         b.routeSampler,
		FirewallRulesSampler: b.firewallSampler,
	})
}

func (b *WebBackend) probeWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil || len(w.check) == 0 {
		return nil, nil, ""
	}
	res, err := b.probeWatchResult(ctx, w)
	if err != nil {
		msg := err.Error()
		checkType := "watch"
		if w != nil {
			checkType = w.checkType
		}
		return nil, watchErrorReadings(msg), checkType + ": " + msg
	}
	readings := checkReadings(w.checkType, res.Data)
	if len(readings) == 0 && res.Message != "" {
		readings = []web.WatchReading{{Field: watchReadingFieldResult, Label: watchReadingLabelResult, Value: res.Message}}
	}
	if !res.Healthy() && res.Message != "" {
		readings = append([]web.WatchReading{{Field: watchReadingFieldError, Label: watchReadingLabelError, Error: res.Message}}, readings...)
	}
	summary := res.Message
	if summary == "" && len(readings) > 0 {
		if readings[0].Error != "" {
			summary = readings[0].Error
		} else {
			summary = readings[0].Value
		}
	}
	return nil, readings, summary
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
	return fmt.Sprintf("manual probe completed in %s: %s", formatInterval(duration), summary)
}

func manualProbeFailedMessage(summary string, duration time.Duration) string {
	return fmt.Sprintf("manual probe failed after %s: %s", formatInterval(duration), summary)
}
