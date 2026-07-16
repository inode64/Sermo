package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/units"
	"sermo/internal/web"
)

const (
	watchReadingFieldEntries = "entries"
	watchReadingNumericBase  = 10
	fileWatchReadingsPerPath = 6
)

func (b *WebBackend) fileWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	paths, err := config.FileWatchPaths(w.check)
	if err != nil {
		msg := watchMissingPathMessage
		return nil, watchErrorReadings(msg), "file: " + msg
	}
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	readings := make([]web.WatchReading, 0, len(paths)*fileWatchReadingsPerPath)
	summaries := make([]string, 0, len(paths))
	for _, path := range paths {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			msg := statErr.Error()
			if os.IsNotExist(statErr) {
				msg = "not found"
			}
			readings = append(readings, web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: path, Error: msg})
			summaries = append(summaries, path+": "+msg)
			continue
		}
		data := checks.FileResultData(path, info)
		data[checks.DataKeyAge] = units.HumanizeDuration(now.Sub(info.ModTime()).Round(time.Second))
		var entriesErr error
		if cfgval.Bool(w.check[checks.CheckKeyRecursive]) && info.IsDir() {
			probeCtx, cancel := b.probeContext(ctx)
			n, countErr := checks.TallyEntries(probeCtx, path, checks.CountKindAny, true, cfgval.Bool(w.check[checks.CheckKeyIncludeHidden]), b.probeTimeout())
			cancel()
			if countErr != nil {
				entriesErr = countErr
			} else {
				data[watchReadingFieldEntries] = n
			}
		}
		readings = append(readings, checkReadings(checks.CheckTypeFile, data)...)
		if entriesErr != nil {
			readings = append(readings, web.WatchReading{Field: watchReadingFieldEntries, Label: watchReadingLabelEntries, Error: entriesErr.Error()})
		}
		summaries = append(summaries, fmt.Sprintf("%s %s", path, data[checks.DataKeyKind]))
	}
	return nil, readings, strings.Join(summaries, displayListSeparator)
}

func (b *WebBackend) countWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := watchMissingPathMessage
		return nil, watchErrorReadings(msg), "count: " + msg
	}
	kind := cfgval.AsString(w.check[checks.CheckKeyOf])
	if kind == "" {
		kind = checks.CountKindAny
	}
	recursive := cfgval.Bool(w.check[checks.CheckKeyRecursive])
	includeHidden := cfgval.Bool(w.check[checks.CheckKeyIncludeHidden])
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	n, err := checks.TallyEntries(probeCtx, path, kind, recursive, includeHidden, b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "count: " + msg
	}
	scope := "in"
	if recursive {
		scope = "under"
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: path},
		{Field: checks.DataKeyOf, Label: watchReadingLabelOf, Value: kind},
		{Field: checks.DataKeyCount, Label: watchReadingLabelCount, Value: strconv.Itoa(n)},
	}
	if m, ok := w.check[checks.CheckKeyDelta].(map[string]any); ok {
		readings = append(readings, web.WatchReading{
			Field: checks.CheckKeyDelta,
			Label: watchReadingLabelGrowthLimit,
			Value: watchConditionText(web.WatchCondition{
				Field: checks.CheckKeyDelta,
				Op:    cfgval.AsString(m[checks.CheckKeyOp]),
				Value: cfgval.String(m[checks.CheckKeyValue]),
			}),
		})
	}
	if within := cfgval.String(w.check[checks.CheckKeyWithin]); within != "" {
		readings = append(readings, web.WatchReading{
			Field: checks.CheckKeyWithin, Label: watchReadingLabelWindow, Value: within,
		})
	}
	return nil, readings, fmt.Sprintf("%d %s entries %s %s", n, kind, scope, path)
}

func (b *WebBackend) firewallRulesWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	backend := cfgval.AsString(w.check[checks.CheckKeyBackend])
	if backend == "" {
		backend = checks.FirewallBackendAuto
	}
	sampler := b.firewallSampler
	if sampler == nil {
		sampler = checks.DefaultFirewallRulesSampler
	}
	runner := b.execRunner
	runner = execx.RunnerOrDefault(runner)
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	sample, err := sampler(probeCtx, backend, runner)
	if err != nil {
		msg := execx.FormatContextOrError(err, b.probeTimeout())
		return nil, watchErrorReadings(msg), "firewall: " + msg
	}
	minRules := watchFirewallDefaultMinRules
	if v, present := w.check[checks.CheckKeyMinRules]; present {
		if n, ok := cfgval.Int(v); ok && n >= 1 {
			minRules = uint64(n)
		}
	}
	data := map[string]any{
		checks.DataKeyBackend:  sample.Backend,
		checks.DataKeyRules:    sample.Rules,
		checks.DataKeyMinRules: minRules,
	}
	return nil, checkReadings(checks.CheckTypeFirewallRules, data), fmt.Sprintf("firewall %s has %d rules", sample.Backend, sample.Rules)
}

func (b *WebBackend) sizeWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := watchMissingPathMessage
		return nil, watchErrorReadings(msg), "size: " + msg
	}
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	size, err := checks.SamplePathSize(probeCtx, path, cfgval.Bool(w.check[checks.CheckKeyIncludeHidden]), b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "size: " + msg
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: path},
		{Field: checks.DataKeyCurrentBytes, Label: watchReadingLabelCurrentSize, Value: humanize.IBytes(uint64(size))},
	}
	if growBy := cfgval.String(w.check[checks.CheckKeyGrowBy]); growBy != "" {
		readings = append(readings, web.WatchReading{Field: checks.CheckKeyGrowBy, Label: watchReadingLabelGrowthLimit, Value: growBy})
	}
	if within := cfgval.String(w.check[checks.CheckKeyWithin]); within != "" {
		readings = append(readings, web.WatchReading{Field: checks.CheckKeyWithin, Label: watchReadingLabelWindow, Value: within})
	}
	return nil, readings, fmt.Sprintf("%s size %s", path, humanize.IBytes(uint64(size)))
}

func (b *WebBackend) hdparmWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check[checks.CheckKeyDevice])
	if device == "" {
		msg := watchMissingDeviceMessage
		return nil, watchErrorReadings(msg), "hdparm: " + msg
	}
	wantCached, wantRead := false, false
	for _, field := range checks.HdparmPredFields {
		if _, ok := w.check[field].(map[string]any); ok {
			switch field {
			case checks.HdparmFieldCached:
				wantCached = true
			case checks.HdparmFieldRead:
				wantRead = true
			}
		}
	}
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	values, err := checks.SampleHdparm(probeCtx, b.execRunner, device, wantCached, wantRead, b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "hdparm: " + msg
	}
	const hdparmSummaryPartCapacity = 2

	parts := make([]string, 0, hdparmSummaryPartCapacity)
	for _, field := range []string{checks.HdparmFieldRead, checks.HdparmFieldCached} {
		if v, ok := values[field]; ok {
			parts = append(parts, fmt.Sprintf("%s=%.1f", field, v))
		}
	}
	return nil, checkReadings(checks.CheckTypeHdparm, checks.HdparmResultData(device, values)),
		fmt.Sprintf("hdparm %s %s MB/s", device, strings.Join(parts, " "))
}

func (b *WebBackend) smartWatchView(ctx context.Context, w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check[checks.CheckKeyDevice])
	if device == "" {
		msg := watchMissingDeviceMessage
		return nil, watchErrorReadings(msg), "smart: " + msg
	}
	probeCtx, cancel := b.probeContext(ctx)
	defer cancel()
	sample, err := checks.SampleSmart(probeCtx, b.execRunner, device, b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "smart: " + msg
	}
	data := checks.SmartResultData(device, sample.Health, sample.SelfTestRunning, sample.Values)
	return nil, checkReadings(checks.CheckTypeSmart, data), fmt.Sprintf("smart %s health=%s", device, sample.Health)
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
