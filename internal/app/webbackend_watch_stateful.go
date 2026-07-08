package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/web"
)

const (
	watchReadingFieldEntries = "entries"
	watchReadingKindDir      = "directory"
	watchReadingKindOther    = "other"
	watchReadingNumericBase  = 10
)

func (b *WebBackend) fileWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := "missing path"
		return nil, watchErrorReadings(msg), "file: " + msg
	}
	info, err := os.Lstat(path)
	if err != nil {
		msg := err.Error()
		if os.IsNotExist(err) {
			msg = "not found"
		}
		return nil, watchErrorReadings(msg), "file: " + msg
	}
	kind := fileKindLabel(info.Mode())
	readings := []web.WatchReading{
		{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: path},
		{Field: checks.DataKeyKind, Label: watchReadingLabelKind, Value: kind},
		{Field: checks.DataKeySize, Label: watchReadingLabelSize, Value: humanize.Bytes(uint64(info.Size()))},
		{Field: checks.DataKeyMode, Label: watchReadingLabelMode, Value: info.Mode().Perm().String()},
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		readings = append(readings, web.WatchReading{
			Field: checks.CheckKeyOwner, Label: watchReadingLabelOwner, Value: fmt.Sprintf("%d:%d", sys.Uid, sys.Gid),
		})
	}
	if cfgval.Bool(w.check[checks.CheckKeyRecursive]) && info.IsDir() {
		ctx, cancel := b.probeContext()
		defer cancel()
		n, err := checks.TallyEntries(ctx, path, checks.CountKindAny, true, b.probeTimeout())
		if err != nil {
			readings = append(readings, web.WatchReading{Field: watchReadingFieldEntries, Label: watchReadingLabelEntries, Error: err.Error()})
		} else {
			readings = append(readings, web.WatchReading{Field: watchReadingFieldEntries, Label: watchReadingLabelEntries, Value: strconv.Itoa(n)})
		}
	}
	return nil, readings, fmt.Sprintf("%s %s", path, kind)
}

func fileKindLabel(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return checks.CountKindSymlink
	case mode.IsRegular():
		return checks.CountKindFile
	case mode.IsDir():
		return watchReadingKindDir
	default:
		return watchReadingKindOther
	}
}

func (b *WebBackend) countWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := "missing path"
		return nil, watchErrorReadings(msg), "count: " + msg
	}
	kind := cfgval.AsString(w.check[checks.CheckKeyOf])
	if kind == "" {
		kind = checks.CountKindAny
	}
	recursive := cfgval.Bool(w.check[checks.CheckKeyRecursive])
	ctx, cancel := b.probeContext()
	defer cancel()
	n, err := checks.TallyEntries(ctx, path, kind, recursive, b.probeTimeout())
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
	return nil, readings, fmt.Sprintf("%d %s entries %s %s", n, kind, scope, path)
}

func (b *WebBackend) firewallRulesWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	backend := cfgval.AsString(w.check[checks.CheckKeyBackend])
	if backend == "" {
		backend = checks.FirewallBackendAuto
	}
	sampler := b.firewallSampler
	if sampler == nil {
		sampler = checks.DefaultFirewallRulesSampler
	}
	runner := b.execRunner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	ctx, cancel := b.probeContext()
	defer cancel()
	sample, err := sampler(ctx, backend, runner)
	if err != nil {
		msg := execx.FormatContextOrError(err, b.probeTimeout())
		return nil, watchErrorReadings(msg), "firewall: " + msg
	}
	minRules := uint64(1)
	if v, present := w.check[checks.CheckKeyMinRules]; present {
		if n, ok := cfgval.Int(v); ok && n >= 1 {
			minRules = uint64(n)
		}
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyBackend, Label: watchReadingLabelBackend, Value: sample.Backend},
		{Field: checks.DataKeyRules, Label: watchReadingLabelRules, Value: strconv.FormatUint(sample.Rules, watchReadingNumericBase)},
		{Field: checks.DataKeyMinRules, Label: watchReadingLabelMinRules, Value: strconv.FormatUint(minRules, watchReadingNumericBase)},
	}
	return nil, readings, fmt.Sprintf("firewall %s has %d rules", sample.Backend, sample.Rules)
}

func (b *WebBackend) sizeWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := "missing path"
		return nil, watchErrorReadings(msg), "size: " + msg
	}
	ctx, cancel := b.probeContext()
	defer cancel()
	size, err := checks.SamplePathSize(ctx, path, b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "size: " + msg
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: path},
		{Field: checks.DataKeyCurrentBytes, Label: watchReadingLabelCurrentSize, Value: humanize.Bytes(uint64(size))},
	}
	if growBy := cfgval.String(w.check[checks.CheckKeyGrowBy]); growBy != "" {
		readings = append(readings, web.WatchReading{Field: checks.CheckKeyGrowBy, Label: watchReadingLabelGrowthLimit, Value: growBy})
	}
	if within := cfgval.String(w.check[checks.CheckKeyWithin]); within != "" {
		readings = append(readings, web.WatchReading{Field: checks.CheckKeyWithin, Label: watchReadingLabelWindow, Value: within})
	}
	return nil, readings, fmt.Sprintf("%s size %s", path, humanize.Bytes(uint64(size)))
}

func (b *WebBackend) hdparmWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check[checks.CheckKeyDevice])
	if device == "" {
		msg := "missing device"
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
	ctx, cancel := b.probeContext()
	defer cancel()
	values, err := checks.SampleHdparm(ctx, b.execRunner, device, wantCached, wantRead, b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "hdparm: " + msg
	}
	readings := []web.WatchReading{{Field: checks.DataKeyDevice, Label: watchReadingLabelDevice, Value: device}}
	parts := make([]string, 0, 2)
	for _, field := range []string{checks.HdparmFieldRead, checks.HdparmFieldCached} {
		if v, ok := values[field]; ok {
			readings = append(readings, web.WatchReading{
				Field: field, Label: field, Value: watchReadingMetricValue(v, 1, watchReadingUnitMegabytesPerSecond),
			})
			parts = append(parts, fmt.Sprintf("%s=%.1f", field, v))
		}
	}
	return nil, readings, fmt.Sprintf("hdparm %s %s MB/s", device, strings.Join(parts, " "))
}

func (b *WebBackend) smartWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check[checks.CheckKeyDevice])
	if device == "" {
		msg := "missing device"
		return nil, watchErrorReadings(msg), "smart: " + msg
	}
	ctx, cancel := b.probeContext()
	defer cancel()
	sample, err := checks.SampleSmart(ctx, b.execRunner, device, b.probeTimeout())
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "smart: " + msg
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyDevice, Label: watchReadingLabelDevice, Value: device},
		{Field: checks.DataKeyHealth, Label: watchReadingLabelHealth, Value: sample.Health},
	}
	for _, field := range checks.SmartPredFields {
		if v, ok := sample.Values[field]; ok {
			label := field
			unit := ""
			switch field {
			case checks.SmartFieldTemperature:
				unit = watchReadingUnitCelsiusSymbol
			case checks.SmartFieldWear:
				unit = metrics.MetricUnitPercent
			}
			readings = append(readings, web.WatchReading{
				Field: field, Label: label, Value: watchReadingMetricValue(v, 0, unit),
			})
		}
	}
	return nil, readings, fmt.Sprintf("smart %s health=%s", device, sample.Health)
}

func (b *WebBackend) probeTimeout() time.Duration {
	timeout := b.defaultTimeout
	if timeout <= 0 {
		timeout = b.operationTimeout
	}
	return timeout
}

func (b *WebBackend) probeContext() (context.Context, context.CancelFunc) {
	timeout := b.probeTimeout()
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}
