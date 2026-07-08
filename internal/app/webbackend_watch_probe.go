package app

import (
	"maps"

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

func (b *WebBackend) probeWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil || len(w.check) == 0 {
		return nil, nil, ""
	}
	entry := maps.Clone(w.check)
	check, err := checks.BuildInline(w.name, entry, b.watchCheckDeps())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), w.checkType + ": " + msg
	}
	ctx, cancel := b.probeContext()
	defer cancel()
	res := check.Run(ctx)
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
