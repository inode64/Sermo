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
	"sermo/internal/web"
)

func (b *WebBackend) fileWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check["path"])
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
		{Field: "path", Label: "Path", Value: path},
		{Field: "kind", Label: "Kind", Value: kind},
		{Field: "size", Label: "Size", Value: humanize.Bytes(uint64(info.Size()))},
		{Field: "mode", Label: "Mode", Value: info.Mode().Perm().String()},
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		readings = append(readings, web.WatchReading{
			Field: "owner", Label: "Owner", Value: fmt.Sprintf("%d:%d", sys.Uid, sys.Gid),
		})
	}
	if cfgval.Bool(w.check["recursive"]) && info.IsDir() {
		ctx, cancel := b.probeContext()
		defer cancel()
		n, err := checks.TallyEntries(ctx, path, "any", true, b.probeTimeout())
		if err != nil {
			readings = append(readings, web.WatchReading{Field: "entries", Label: "Entries", Error: err.Error()})
		} else {
			readings = append(readings, web.WatchReading{Field: "entries", Label: "Entries", Value: strconv.Itoa(n)})
		}
	}
	return nil, readings, fmt.Sprintf("%s %s", path, kind)
}

func fileKindLabel(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsRegular():
		return "file"
	case mode.IsDir():
		return "directory"
	default:
		return "other"
	}
}

func (b *WebBackend) countWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check["path"])
	if path == "" {
		msg := "missing path"
		return nil, watchErrorReadings(msg), "count: " + msg
	}
	kind := cfgval.AsString(w.check["of"])
	if kind == "" {
		kind = "any"
	}
	recursive := cfgval.Bool(w.check["recursive"])
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
		{Field: "path", Label: "Path", Value: path},
		{Field: "of", Label: "Of", Value: kind},
		{Field: "count", Label: "Count", Value: strconv.Itoa(n)},
	}
	return nil, readings, fmt.Sprintf("%d %s entries %s %s", n, kind, scope, path)
}

func (b *WebBackend) firewallRulesWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	backend := cfgval.AsString(w.check["backend"])
	if backend == "" {
		backend = "auto"
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
		msg := err.Error()
		return nil, watchErrorReadings(msg), "firewall: " + msg
	}
	minRules := uint64(1)
	if v, present := w.check["min_rules"]; present {
		if n, ok := cfgval.Int(v); ok && n >= 1 {
			minRules = uint64(n)
		}
	}
	readings := []web.WatchReading{
		{Field: "backend", Label: "Backend", Value: sample.Backend},
		{Field: "rules", Label: "Rules", Value: strconv.FormatUint(sample.Rules, 10)},
		{Field: "min_rules", Label: "Min rules", Value: strconv.FormatUint(minRules, 10)},
	}
	return nil, readings, fmt.Sprintf("firewall %s has %d rules", sample.Backend, sample.Rules)
}

func (b *WebBackend) sizeWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	path := cfgval.AsString(w.check["path"])
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
		{Field: "path", Label: "Path", Value: path},
		{Field: "current_bytes", Label: "Current size", Value: humanize.Bytes(uint64(size))},
	}
	if growBy := cfgval.String(w.check["grow_by"]); growBy != "" {
		readings = append(readings, web.WatchReading{Field: "grow_by", Label: "Growth limit", Value: growBy})
	}
	if within := cfgval.String(w.check["within"]); within != "" {
		readings = append(readings, web.WatchReading{Field: "within", Label: "Window", Value: within})
	}
	return nil, readings, fmt.Sprintf("%s size %s", path, humanize.Bytes(uint64(size)))
}

func (b *WebBackend) hdparmWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check["device"])
	if device == "" {
		msg := "missing device"
		return nil, watchErrorReadings(msg), "hdparm: " + msg
	}
	wantCached, wantRead := false, false
	for _, field := range checks.HdparmPredFields {
		if _, ok := w.check[field].(map[string]any); ok {
			switch field {
			case "cached":
				wantCached = true
			case "read":
				wantRead = true
			}
		}
	}
	ctx, cancel := b.probeContext()
	defer cancel()
	values, err := checks.SampleHdparm(ctx, b.execRunner, device, wantCached, wantRead)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "hdparm: " + msg
	}
	readings := []web.WatchReading{{Field: "device", Label: "Device", Value: device}}
	parts := make([]string, 0, 2)
	for _, field := range []string{"read", "cached"} {
		if v, ok := values[field]; ok {
			readings = append(readings, web.WatchReading{
				Field: field, Label: field, Value: fmt.Sprintf("%.1f MB/s", v),
			})
			parts = append(parts, fmt.Sprintf("%s=%.1f", field, v))
		}
	}
	return nil, readings, fmt.Sprintf("hdparm %s %s MB/s", device, strings.Join(parts, " "))
}

func (b *WebBackend) smartWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check["device"])
	if device == "" {
		msg := "missing device"
		return nil, watchErrorReadings(msg), "smart: " + msg
	}
	ctx, cancel := b.probeContext()
	defer cancel()
	sample, err := checks.SampleSmart(ctx, b.execRunner, device)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "smart: " + msg
	}
	readings := []web.WatchReading{
		{Field: "device", Label: "Device", Value: device},
		{Field: "health", Label: "Health", Value: sample.Health},
	}
	for _, field := range checks.SmartPredFields {
		if v, ok := sample.Values[field]; ok {
			label := field
			unit := ""
			switch field {
			case "temperature":
				unit = " °C"
			case "wear":
				unit = "%"
			}
			readings = append(readings, web.WatchReading{
				Field: field, Label: label, Value: fmt.Sprintf("%.0f%s", v, unit),
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
