package app

import (
	"fmt"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type webDiskIOState struct {
	primed   bool
	at       time.Time
	sample   checks.DiskIOSample
	rates    checks.DiskIORates
	hasRates bool
}

func (b *WebBackend) processWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	name := cfgval.AsString(w.check[checks.CheckKeyName])
	if name == "" {
		msg := watchMissingNameMessage
		return nil, watchErrorReadings(msg), "process: " + msg
	}
	user := cfgval.AsString(w.check[checks.CheckKeyUser])
	sampler := b.procSampler
	if sampler == nil {
		sampler = osProcSampler{userLookup: b.userLookup}
	}
	samples, _ := sampler.Sample(ProcMatch{Name: name, User: user})
	sort.Slice(samples, func(i, j int) bool { return samples[i].PID < samples[j].PID })

	data := processWatchData(name, user, samples)
	target := "process " + name
	if user != "" {
		target += " user " + user
	}
	summary := fmt.Sprintf("%s: %d matching process%s", target, len(samples), pluralSuffix(len(samples), "process"))
	if len(samples) > 0 {
		summary += fmt.Sprintf(", rss %d bytes", data[watchReadingFieldRSS])
	}
	return nil, checkReadings(checks.CheckTypeProcess, data), summary
}

func (b *WebBackend) autofsWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.mountSampler
	if sampler == nil {
		sampler = checks.DefaultMounts
	}
	mounts, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "autofs: " + msg
	}
	points := autofsMountpoints(mounts)
	readings := []web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelMountpoints, Value: strconv.Itoa(len(points))}}
	if len(points) > 0 {
		readings = append(readings, web.WatchReading{Field: checks.DataKeyMountpoints, Label: watchReadingLabelPaths, Value: strings.Join(points, displayListSeparator)})
	}
	if path := cfgval.AsString(w.check[checks.CheckKeyPath]); path != "" {
		pathState := watchReadingStateMissing
		if slices.Contains(points, path) {
			pathState = watchReadingStateActive
		}
		readings = append(readings,
			web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelConfiguredPath, Value: path},
			web.WatchReading{Field: watchReadingFieldState, Label: watchReadingLabelState, Value: pathState},
		)
		return nil, readings, fmt.Sprintf("autofs %s %s (%d mountpoint%s)", path, pathState, len(points), pluralSuffix(len(points), "mountpoint"))
	}
	return nil, readings, fmt.Sprintf("%d autofs mountpoint%s active", len(points), pluralSuffix(len(points), "mountpoint"))
}

func (b *WebBackend) diskIOWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check[checks.CheckKeyDevice])
	if device == "" {
		msg := watchMissingDeviceMessage
		return nil, watchErrorReadings(msg), "diskio: " + msg
	}
	sampler := b.diskIOSampler
	if sampler == nil {
		sampler = checks.SampleDiskIO
	}
	sample, err := sampler(device)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "diskio " + device + ": " + msg
	}
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	at := now()
	key := w.name + "\x00" + device

	b.diskIOMu.Lock()
	if b.diskIOState == nil {
		b.diskIOState = map[string]webDiskIOState{}
	}
	st := b.diskIOState[key]
	switch {
	case !st.primed:
		st = webDiskIOState{primed: true, at: at, sample: sample}
		b.diskIOState[key] = st
	case at.Sub(st.at) >= diskIORateMinWindow:
		next := webDiskIOState{primed: true, at: at, sample: sample}
		next.rates, next.hasRates = checks.CalculateDiskIORates(st.sample, sample, at.Sub(st.at))
		st = next
		b.diskIOState[key] = st
	}
	// Polls inside diskIORateMinWindow keep the previous baseline and serve
	// its last computed rates (st unchanged).
	b.diskIOMu.Unlock()

	if !st.hasRates {
		readings := []web.WatchReading{
			{Field: checks.DataKeyDevice, Label: watchReadingLabelDevice, Value: device},
			{Field: watchReadingFieldState, Label: watchReadingLabelState, Value: watchReadingStateBaseline},
		}
		return nil, readings, "diskio " + device + " baseline"
	}
	rates := st.rates
	return nil, checkReadings(checks.CheckTypeDiskIO, checks.DiskIOResultData(device, rates)),
		fmt.Sprintf("diskio %s util %.1f%% read %.0fB/s write %.0fB/s await %.1fms",
			device, rates.UtilPct, rates.ReadBytes, rates.WriteBytes, rates.AwaitMs)
}

func (b *WebBackend) sensorsWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.sensorSampler
	if sampler == nil {
		sampler = checks.SampleSensors
	}
	readings, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "sensors: " + msg
	}
	chip := cfgval.AsString(w.check[checks.CheckKeyChip])
	label := cfgval.AsString(w.check[checks.CheckKeyLabel])
	values := checks.SummarizeSensors(readings, chip, label)
	out := checkReadings(checks.CheckTypeSensors, checks.SensorsResultData(values, chip, label))
	const sensorSummaryPartCapacity = 3

	parts := make([]string, 0, sensorSummaryPartCapacity)
	if values.HasTemp {
		parts = append(parts, fmt.Sprintf("temp=%.1fC", values.Temp))
	}
	if values.HasFan {
		parts = append(parts, fmt.Sprintf("fan=%.0fRPM", values.Fan))
	}
	if values.HasVoltage {
		parts = append(parts, fmt.Sprintf("voltage=%.2fV", values.Voltage))
	}
	if len(parts) == 0 {
		return nil, out, "sensors: no matching inputs"
	}
	return nil, out, "sensors " + strings.Join(parts, " ")
}

func (b *WebBackend) raidWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.raidSampler
	if sampler == nil {
		sampler = checks.SampleRaid
	}
	st, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "raid: " + msg
	}
	summary := fmt.Sprintf("raid: %d arrays, %d degraded, %d recovering", st.Arrays, st.Degraded, st.Recovering)
	if len(st.DegradedNames) > 0 {
		summary += " (" + strings.Join(st.DegradedNames, displayListSeparator) + ")"
	}
	return nil, checkReadings(checks.CheckTypeRAID, checks.RaidResultData(st)), summary
}

func (b *WebBackend) edacWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.edacSampler
	if sampler == nil {
		sampler = checks.SampleEdac
	}
	st, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "edac: " + msg
	}
	if !st.Present {
		msg := "no EDAC controllers"
		return nil, []web.WatchReading{{Field: checks.DataKeyPresent, Label: watchReadingLabelEDAC, Error: msg}}, "edac: " + msg
	}
	return nil, checkReadings(checks.CheckTypeEDAC, checks.EdacResultData(st)),
		fmt.Sprintf("edac: %d correctable, %d uncorrectable", st.CE, st.UE)
}

func (b *WebBackend) routeWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	family := cfgval.AsString(w.check[checks.CheckKeyFamily])
	if family == "" {
		family = checks.FamilyIPv4
	}
	iface := cfgval.AsString(w.check[checks.CheckKeyInterface])
	sampler := b.routeSampler
	if sampler == nil {
		sampler = checks.SampleRoutes
	}
	routes, err := sampler(family)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "route: " + msg
	}
	matched := matchingDefaultRoutes(routes, iface)
	readings := []web.WatchReading{
		{Field: checks.DataKeyFamily, Label: watchReadingLabelFamily, Value: family},
		{Field: checks.DataKeyRoutes, Label: watchReadingLabelDefaultRoutes, Value: strconv.Itoa(len(routes))},
	}
	if iface != "" {
		readings = append(readings, web.WatchReading{Field: checks.DataKeyInterface, Label: watchReadingLabelRequiredInterface, Value: iface})
	}
	if len(matched) > 0 {
		readings = append(readings, web.WatchReading{Field: checks.DataKeyEgress, Label: watchReadingLabelEgress, Value: matched[0].Iface})
		if matched[0].Gateway != "" {
			readings = append(readings, web.WatchReading{Field: checks.DataKeyGateway, Label: watchReadingLabelGateway, Value: matched[0].Gateway})
		}
	}
	switch {
	case len(matched) > 0 && matched[0].Gateway != "":
		return nil, readings, fmt.Sprintf("%s default route via %s (gw %s)", family, matched[0].Iface, matched[0].Gateway)
	case len(matched) > 0:
		return nil, readings, fmt.Sprintf("%s default route via %s", family, matched[0].Iface)
	case iface != "" && len(routes) > 0:
		return nil, readings, fmt.Sprintf("no %s default route via %s (%d elsewhere)", family, iface, len(routes))
	default:
		return nil, readings, "no " + family + " default route"
	}
}

func (b *WebBackend) netWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	iface := cfgval.AsString(w.check[checks.CheckKeyInterface])
	if iface == "" {
		msg := watchMissingInterfaceMessage
		return nil, watchErrorReadings(msg), "net: " + msg
	}
	sampler := b.netSampler
	if sampler == nil {
		sampler = checks.SampleNet
	}
	s, err := sampler(iface)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "net " + iface + ": " + msg
	}

	readings := []web.WatchReading{
		{Field: checks.DataKeyInterface, Label: watchReadingLabelInterface, Value: iface},
		{Field: checks.NetMetricState, Label: watchReadingLabelState, Value: s.State},
	}
	parts := []string{iface + " state " + s.State}
	if watchMetricEnabled(w.metrics, checks.NetMetricSpeed) {
		if s.SpeedKnown {
			readings = append(readings, web.WatchReading{Field: checks.NetMetricSpeed, Label: watchReadingLabelSpeed, Value: watchReadingIntMetricValue(s.SpeedMbps, watchReadingUnitMegabitsPerSecond)})
			parts = append(parts, fmt.Sprintf("speed %d Mbps", s.SpeedMbps))
		} else {
			readings = append(readings, web.WatchReading{Field: checks.NetMetricSpeed, Label: watchReadingLabelSpeed, Value: watchReadingValueUnknown})
			parts = append(parts, "speed "+watchReadingValueUnknown)
		}
	}
	if watchMetricEnabled(w.metrics, checks.NetMetricErrors) {
		total := netErrorTotal(w.metrics, s.Counters)
		readings = append(readings, web.WatchReading{Field: checks.NetMetricErrors, Label: watchReadingLabelErrorsTotal, Value: strconv.FormatUint(total, 10)})
		parts = append(parts, fmt.Sprintf("errors %d", total))
	}
	if watchMetricEnabled(w.metrics, checks.NetMetricAddress) {
		value := strings.Join(s.Addrs, displayListSeparator)
		if value == "" {
			value = watchReadingValueNone
		}
		readings = append(readings, web.WatchReading{Field: checks.NetMetricAddress, Label: watchReadingLabelAddresses, Value: value})
		parts = append(parts, fmt.Sprintf("%d address%s", len(s.Addrs), pluralSuffix(len(s.Addrs), "address")))
	}
	return nil, readings, strings.Join(parts, " · ")
}

func (b *WebBackend) icmpWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	host := cfgval.AsString(w.check[checks.CheckKeyHost])
	if host == "" {
		msg := "missing host"
		return nil, watchErrorReadings(msg), "icmp: " + msg
	}
	count := checks.DefaultPingCount
	if v, ok := cfgval.Int(w.check[checks.CheckKeyCount]); ok && v > 0 {
		count = v
	}
	timeout := cfgval.Duration(w.check[checks.CheckKeyTimeout])
	if timeout <= 0 {
		timeout = b.defaultTimeout
	}
	s, err := checks.SampleICMP(host, cfgval.StringList(w.check[checks.CheckKeyInterface]),
		cfgval.AsString(w.check[checks.CheckKeyInterfaceMatch]) == checks.InterfaceMatchAll, count, timeout, b.pingSampler)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "icmp " + host + ": " + msg
	}
	linkState := checks.NetStateDown
	if s.Reachable {
		linkState = checks.NetStateUp
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyHost, Label: watchReadingLabelHost, Value: host},
		{Field: checks.NetMetricState, Label: watchReadingLabelState, Value: linkState},
	}
	parts := []string{host + " " + linkState}
	if s.RTTKnown {
		readings = append(readings, web.WatchReading{Field: checks.IcmpMetricLatency, Label: watchReadingLabelRTT, Value: watchReadingMetricValue(s.RTTms, 1, metrics.MetricUnitMilliseconds)})
		parts = append(parts, fmt.Sprintf("rtt %.1f ms", s.RTTms))
	} else if watchMetricEnabled(w.metrics, checks.IcmpMetricLatency) {
		readings = append(readings, web.WatchReading{Field: checks.IcmpMetricLatency, Label: watchReadingLabelRTT, Value: watchReadingValueUnknown})
		parts = append(parts, "rtt "+watchReadingValueUnknown)
	}
	return nil, readings, strings.Join(parts, " · ")
}

func (b *WebBackend) oomWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.oomSampler
	if sampler == nil {
		sampler = checks.SampleOom
	}
	count, ok := sampler()
	if !ok {
		msg := "oom_kill counter unavailable"
		return nil, watchErrorReadings(msg), "oom: " + msg
	}
	return nil,
		[]web.WatchReading{{Field: checks.DataKeyTotal, Label: watchReadingLabelOOMKills, Value: strconv.FormatUint(count, 10)}},
		fmt.Sprintf("%d oom_kill total", count)
}

func (b *WebBackend) fdsWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	return countWatchView(countWatchViewSpec[checks.FdsSample]{
		kind:       checks.CheckTypeFDS,
		resource:   checks.CheckTypeFDS,
		usage:      "allocated",
		field:      checks.DataKeyCount,
		label:      watchReadingLabelAllocated,
		sampler:    b.fdsSampler,
		fallback:   checks.SampleFds,
		count:      func(sample checks.FdsSample) uint64 { return sample.Allocated },
		limit:      func(sample checks.FdsSample) uint64 { return sample.Max },
		formatRead: formatCountReading,
	})
}

func (b *WebBackend) pidsWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	return countWatchView(countWatchViewSpec[checks.PidsSample]{
		kind:       checks.CheckTypePIDs,
		resource:   checks.CheckTypePIDs,
		usage:      "in use",
		field:      checks.DataKeyCount,
		label:      watchReadingLabelInUse,
		sampler:    b.pidsSampler,
		fallback:   checks.SamplePids,
		count:      func(sample checks.PidsSample) uint64 { return sample.Threads },
		limit:      func(sample checks.PidsSample) uint64 { return sample.Max },
		formatRead: formatCountReading,
	})
}

func (b *WebBackend) pressureWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	resource := cfgval.AsString(w.check[checks.CheckKeyResource])
	if resource == "" {
		msg := "missing resource"
		return nil, watchErrorReadings(msg), "pressure: " + msg
	}
	sampler := b.pressureSampler
	if sampler == nil {
		sampler = checks.SamplePressure
	}
	s, err := sampler(resource)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "pressure " + resource + ": " + msg
	}
	summary := fmt.Sprintf("pressure %s some %.2f/%.2f/%.2f full %.2f/%.2f/%.2f",
		resource, s.Some.Avg10, s.Some.Avg60, s.Some.Avg300, s.Full.Avg10, s.Full.Avg60, s.Full.Avg300)
	return nil, checkReadings(checks.CheckTypePressure, checks.PressureResultData(resource, s)), summary
}

func (b *WebBackend) conntrackWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	return countWatchView(countWatchViewSpec[checks.ConntrackSample]{
		kind:       checks.CheckTypeConntrack,
		resource:   checks.CheckTypeConntrack,
		usage:      "entries",
		field:      checks.DataKeyCount,
		label:      watchReadingLabelCount,
		sampler:    b.conntrackSampler,
		fallback:   checks.SampleConntrack,
		count:      func(sample checks.ConntrackSample) uint64 { return sample.Count },
		limit:      func(sample checks.ConntrackSample) uint64 { return sample.Max },
		formatRead: formatEntriesReading,
	})
}

type countWatchViewSpec[T any] struct {
	kind       string
	resource   string
	usage      string
	field      string
	label      string
	sampler    func() (T, error)
	fallback   func() (T, error)
	count      func(T) uint64
	limit      func(T) uint64
	formatRead func(uint64) string
}

func countWatchView[T any](spec countWatchViewSpec[T]) (*web.WatchMeter, []web.WatchReading, string) {
	sampler := spec.sampler
	if sampler == nil {
		sampler = spec.fallback
	}
	sample, err := sampler()
	if err != nil {
		message := err.Error()
		return nil, watchErrorReadings(message), spec.resource + ": " + message
	}
	count := spec.count(sample)
	limit := spec.limit(sample)
	summary := fmt.Sprintf("%s %d %s", spec.resource, count, spec.usage)
	if limit > 0 {
		usedPct := float64(count) / float64(limit) * metrics.PercentScale
		summary = fmt.Sprintf("%s %d/%d %s (%.1f%%)", spec.resource, count, limit, spec.usage, usedPct)
	}
	if meter := countMeter(spec.kind, count, limit); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: spec.field, Label: spec.label, Value: spec.formatRead(count)}}, summary
}

func formatCountReading(count uint64) string { return strconv.FormatUint(count, 10) }

func formatEntriesReading(count uint64) string { return formatCountReading(count) + " entries" }

func (b *WebBackend) entropyWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.entropySampler
	if sampler == nil {
		sampler = checks.SampleEntropy
	}
	avail, ok := sampler()
	if !ok {
		msg := "entropy_avail unavailable"
		return nil, watchErrorReadings(msg), "entropy: " + msg
	}
	return nil,
		[]web.WatchReading{{Field: checks.DataKeyAvail, Label: watchReadingLabelAvailable, Value: watchReadingUintMetricValue(avail, watchReadingUnitBits)}},
		fmt.Sprintf("%d available bits", avail)
}

func (b *WebBackend) zombieWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.zombieSampler
	if sampler == nil {
		sampler = checks.SampleZombies
	}
	count, ok := sampler()
	if !ok {
		msg := "cannot read /proc"
		return nil, watchErrorReadings(msg), "zombies: " + msg
	}
	return nil,
		[]web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelZombies, Value: strconv.FormatUint(count, 10)}},
		fmt.Sprintf("%d zombie processes", count)
}
