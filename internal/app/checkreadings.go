package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/units"
	"sermo/internal/web"
)

const (
	watchReadingClockDecimals          = 3
	watchReadingClockPrecisionDecimals = 6
	watchReadingDefaultMetricDecimals  = 2
	watchReadingProgressDecimals       = 1

	watchReadingLabelAddresses         = "Addresses"
	watchReadingLabelAllocated         = "Allocated"
	watchReadingLabelAge               = "Age"
	watchReadingLabelArrays            = "Arrays"
	watchReadingLabelAvailable         = "Available"
	watchReadingLabelAwait             = "Await"
	watchReadingLabelBackend           = "Backend"
	watchReadingLabelBaselineCount     = "Baseline count"
	watchReadingLabelChipFilter        = "Chip filter"
	watchReadingLabelConfiguredPath    = "Configured path"
	watchReadingLabelCount             = "Count"
	watchReadingLabelCPUTicks          = "CPU ticks"
	watchReadingLabelCurrentSize       = "Current size"
	watchReadingLabelDaysLeft          = "Days left"
	watchReadingLabelDefaultRoutes     = "Default routes"
	watchReadingLabelDegraded          = "Degraded"
	watchReadingLabelDegradedArrays    = "Degraded arrays"
	watchReadingLabelDevice            = "Device"
	watchReadingLabelDNSNames          = "DNS names"
	watchReadingLabelEDAC              = "EDAC"
	watchReadingLabelEgress            = "Egress"
	watchReadingLabelEntries           = "Entries"
	watchReadingLabelError             = "Error"
	watchReadingLabelErrorsTotal       = "Errors total"
	watchReadingLabelExpires           = "Expires"
	watchReadingLabelFamily            = "Family"
	watchReadingLabelFree              = "Free"
	watchReadingLabelFreeBytes         = "Free bytes"
	watchReadingLabelFullAvg10         = "Full avg10"
	watchReadingLabelFullAvg60         = "Full avg60"
	watchReadingLabelFullAvg300        = "Full avg300"
	watchReadingLabelGateway           = "Gateway"
	watchReadingLabelGrowth            = "Growth"
	watchReadingLabelGrowthLimit       = "Growth limit"
	watchReadingLabelHealth            = "Health"
	watchReadingLabelHost              = "Host"
	watchReadingLabelInputs            = "Inputs"
	watchReadingLabelInterface         = "Interface"
	watchReadingLabelIO                = "IO total"
	watchReadingLabelInUse             = "In use"
	watchReadingLabelIssuer            = "Issuer"
	watchReadingLabelKeyBits           = "Key bits"
	watchReadingLabelKeyType           = "Key type"
	watchReadingLabelKind              = "Kind"
	watchReadingLabelLabelFilter       = "Label filter"
	watchReadingLabelLatency           = "Latency"
	watchReadingLabelLoad              = "Load"
	watchReadingLabelLogicalVolume     = "LV"
	watchReadingLabelMatches           = "Matches"
	watchReadingLabelModifiedAt        = "Modified at"
	watchReadingLabelMinRules          = "Min rules"
	watchReadingLabelMode              = "Mode"
	watchReadingLabelMountpoints       = "Mountpoints"
	watchReadingLabelOOMKills          = "OOM kills"
	watchReadingLabelOf                = "Of"
	watchReadingLabelOwner             = "Owner"
	watchReadingLabelPath              = "Path"
	watchReadingLabelPaths             = "Paths"
	watchReadingLabelPIDs              = "PIDs"
	watchReadingLabelPort              = "Port"
	watchReadingLabelProcess           = "Process"
	watchReadingLabelProtocol          = "Protocol"
	watchReadingLabelRead              = "Read"
	watchReadingLabelRecovering        = "Recovering"
	watchReadingLabelRequiredInterface = "Required interface"
	watchReadingLabelResource          = "Resource"
	watchReadingLabelResult            = "Result"
	watchReadingLabelReasons           = "Reasons"
	watchReadingLabelRSS               = "RSS total"
	watchReadingLabelRTT               = "RTT"
	watchReadingLabelRules             = "Rules"
	watchReadingLabelSample            = "Sample"
	watchReadingLabelServer            = "Server"
	watchReadingLabelSize              = "Size"
	watchReadingLabelSocket            = "Socket"
	watchReadingLabelSomeAvg10         = "Some avg10"
	watchReadingLabelSomeAvg60         = "Some avg60"
	watchReadingLabelSomeAvg300        = "Some avg300"
	watchReadingLabelSource            = "Source"
	watchReadingLabelSpeed             = "Speed"
	watchReadingLabelState             = "State"
	watchReadingLabelStatus            = "Status"
	watchReadingLabelStratum           = "Stratum"
	watchReadingLabelUsed              = "Used"
	watchReadingLabelUsedBytes         = "Used bytes"
	watchReadingLabelUtilization       = "Utilization"
	watchReadingLabelUser              = "User"
	watchReadingLabelValue             = "Value"
	watchReadingLabelVGFree            = "VG free"
	watchReadingLabelVGFreePct         = "VG free %"
	watchReadingLabelVGSize            = "VG size"
	watchReadingLabelVGUsed            = "VG used"
	watchReadingLabelVolumeGroup       = "VG"
	watchReadingLabelWindow            = "Window"
	watchReadingLabelWrite             = "Write"
	watchReadingLabelZombies           = "Zombies"
	watchReadingLabelLeap              = "Leap"
	watchReadingLabelOffset            = "Offset"
	watchReadingLabelOffsetAbs         = "Offset abs"
	watchReadingLabelPrecision         = "Precision"
	watchReadingLabelReferenceID       = "Reference ID"
	watchReadingLabelRootDelay         = "Root delay"
	watchReadingLabelRootDispersion    = "Root dispersion"
)

const (
	watchReadingUnitBits              = metrics.MetricUnitBits
	watchReadingUnitMegabitsPerSecond = metrics.MetricUnitMegabitsPerSecond
	watchReadingUnitSeconds           = "s"
	maxWatchReadingDuration           = time.Duration(1<<63 - 1)
)

// readingBuilder accumulates the WatchReading list a *CheckReadings builder
// derives from one check's persisted data map, collapsing the repeated
// "read key, skip when absent, format, append" steps.
type readingBuilder struct {
	data map[string]any
	out  []web.WatchReading
}

func readingsFrom(data map[string]any) *readingBuilder {
	return &readingBuilder{data: data}
}

// add appends a reading with an already-formatted value; empty values are skipped.
func (rb *readingBuilder) add(field, label, value string) *readingBuilder {
	if value != "" {
		rb.out = append(rb.out, web.WatchReading{Field: field, Label: label, Value: value})
	}
	return rb
}

// addString appends the field's string value when present.
func (rb *readingBuilder) addString(field, label string) *readingBuilder {
	return rb.add(field, label, cfgval.String(rb.data[field]))
}

// addInt appends the field's integer value when present.
func (rb *readingBuilder) addInt(field, label string) *readingBuilder {
	if v, ok := cfgval.Int(rb.data[field]); ok {
		rb.out = append(rb.out, web.WatchReading{Field: field, Label: label, Value: strconv.Itoa(v)})
	}
	return rb
}

// addIntMetric appends the field's integer value with a unit suffix.
func (rb *readingBuilder) addIntMetric(field, label, unit string) *readingBuilder {
	if v, ok := cfgval.Int(rb.data[field]); ok {
		rb.out = append(rb.out, web.WatchReading{Field: field, Label: label, Value: watchReadingIntMetricValue(int64(v), unit)})
	}
	return rb
}

// addBytes appends the field's non-negative byte count, humanized.
func (rb *readingBuilder) addBytes(field, label string) *readingBuilder {
	if v, ok := uintField(rb.data[field]); ok {
		rb.out = append(rb.out, web.WatchReading{Field: field, Label: label, Value: humanize.IBytes(v)})
	}
	return rb
}

// addMetric appends the field's float value formatted with decimals and unit.
func (rb *readingBuilder) addMetric(field, label string, decimals int, unit string) *readingBuilder {
	if v, ok := cfgval.Float(rb.data[field]); ok {
		rb.out = append(rb.out, web.WatchReading{Field: field, Label: label, Value: watchReadingMetricValue(v, decimals, unit)})
	}
	return rb
}

// readings returns the accumulated list.
func (rb *readingBuilder) readings() []web.WatchReading { return rb.out }

func checkReadings(checkType string, data map[string]any) []web.WatchReading {
	if len(data) == 0 {
		return nil
	}
	switch checkType {
	case checks.CheckTypeCert:
		return certCheckReadings(data)
	case checks.CheckTypeClock:
		return clockCheckReadings(data)
	case checks.CheckTypeCount:
		return countCheckReadings(data)
	case checks.CheckTypeFirewallRules:
		return firewallCheckReadings(data)
	case checks.CheckTypeFile, checks.CheckTypeFileExists:
		return fileCheckReadings(data)
	case checks.CheckTypeProcess:
		return processCheckReadings(data)
	case checks.CheckTypeSize:
		return sizeCheckReadings(data)
	case checks.CheckTypeTCP, checks.CheckTypePorts:
		return connCheckReadings(data)
	case checks.CheckTypeHTTP, checks.URLSchemeHTTPS:
		return httpCheckReadings(data)
	case checks.CheckTypeStorage, checks.CheckTypeSwap, checks.CheckTypeMemory, checks.CheckTypeLoad:
		return resourceCheckReadings(checkType, data)
	case checks.CheckTypePressure:
		return pressureCheckReadings(data)
	case checks.CheckTypeDiskIO:
		return diskioCheckReadings(data)
	case checks.CheckTypeRAID:
		return raidCheckReadings(data)
	case checks.CheckTypeLVM:
		return lvmCheckReadings(data)
	case checks.CheckTypeNet:
		return netCheckReadings(data)
	case checks.CheckTypeSQL:
		return scalarQueryCheckReadings(data)
	case checks.CheckTypeSensors:
		return sensorsCheckReadings(data)
	case checks.CheckTypeHdparm, checks.CheckTypeSmart, checks.CheckTypeEDAC:
		return metricCheckReadings(checkType, data)
	default:
		if graphMetrics := checks.GraphMetrics(checkType); len(graphMetrics) > 0 {
			return metricCheckReadings(checkType, data)
		}
		return nil
	}
}

// netCheckReadings keeps the metric value that the net check compared visible
// after the daemon cycle, rather than requiring the dashboard to parse its
// human-oriented event message.
func netCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if iface := cfgval.String(data[checks.DataKeyInterface]); iface != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyInterface, Label: watchReadingLabelInterface, Value: iface})
	}
	metric := cfgval.String(data[checks.DataKeyMetric])
	value := cfgval.String(data[checks.DataKeyValue])
	switch metric {
	case checks.NetMetricState:
		if value != "" {
			out = append(out, web.WatchReading{Field: checks.NetMetricState, Label: watchReadingLabelState, Value: value})
		}
	case checks.NetMetricSpeed:
		if value != "" {
			out = append(out, web.WatchReading{Field: checks.NetMetricSpeed, Label: watchReadingLabelSpeed, Value: value + " " + watchReadingUnitMegabitsPerSecond})
		}
	case checks.NetMetricErrors:
		if value != "" {
			total := cfgval.String(data[checks.DataKeyTotal])
			if total != "" {
				value += " (total " + total + ")"
			}
			out = append(out, web.WatchReading{Field: checks.NetMetricErrors, Label: watchReadingLabelErrorsTotal, Value: value})
		}
	case checks.NetMetricAddress:
		if value != "" {
			out = append(out, web.WatchReading{Field: checks.NetMetricAddress, Label: watchReadingLabelAddresses, Value: value})
		}
	}
	return out
}

// scalarQueryCheckReadings exposes the scalar observed by a query check and
// its effective comparison without exposing the configured query text.
func scalarQueryCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).addString(checks.DataKeyResult, "Value")
	if op := cfgval.String(data[checks.DataKeyOp]); op != "" {
		if threshold := cfgval.String(data[checks.DataKeyThreshold]); threshold != "" {
			rb.add(checks.DataKeyThreshold, "Condition", op+" "+threshold)
		}
	}
	return rb.readings()
}

func lvmCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeyDeviceState, watchReadingLabelState).
		addMetric(checks.DataKeyProgressPct, "Progress", watchReadingProgressDecimals, metrics.MetricUnitPercent).
		addString(checks.DataKeyHealth, watchReadingLabelHealth).
		addString(checks.DataKeyVolumeGroup, watchReadingLabelVolumeGroup).
		addString(checks.DataKeyLogicalVolume, watchReadingLabelLogicalVolume)
	if _, ok := data[checks.DataKeyLVMReasons]; ok {
		value := cfgval.String(data[checks.DataKeyLVMReasons])
		if value == "" {
			value = watchReadingValueNone
		}
		rb.add(checks.DataKeyLVMReasons, watchReadingLabelReasons, value)
	}
	return rb.
		addBytes(checks.DataKeyLVMFreeBytes, watchReadingLabelVGFree).
		addBytes(checks.DataKeyLVMSizeBytes, watchReadingLabelVGSize).
		addBytes(checks.DataKeyLVMUsedBytes, watchReadingLabelVGUsed).
		addMetric(checks.DataKeyLVMFreePct, watchReadingLabelVGFreePct, watchReadingProgressDecimals, metrics.MetricUnitPercent).
		addMetric(checks.DataKeyLVMThinDataPct, "Thin data", watchReadingProgressDecimals, metrics.MetricUnitPercent).
		addMetric(checks.DataKeyLVMThinMetadataPct, "Thin metadata", watchReadingProgressDecimals, metrics.MetricUnitPercent).
		readings()
}

func raidCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeyDeviceState, watchReadingLabelState).
		addMetric(checks.DataKeyProgressPct, "Progress", watchReadingProgressDecimals, metrics.MetricUnitPercent).
		addString(checks.DataKeyArrays, watchReadingLabelArrays).
		addString(checks.DataKeyDegraded, watchReadingLabelDegraded).
		addString(checks.DataKeyRecovering, watchReadingLabelRecovering).
		addString(checks.DataKeyDegradedArrays, watchReadingLabelDegradedArrays).
		addString(checks.DataKeyArray, "Array").
		addString(checks.DataKeyRaidOperation, "Operation").
		addMetric(checks.DataKeyRaidProgressPct, "Rebuild progress", watchReadingProgressDecimals, metrics.MetricUnitPercent).
		addString(checks.DataKeyRaidMismatchCount, "Mismatch count")
	if size, ok := uintField(data[checks.DataKeyTotalBytes]); ok && size > 0 {
		rb.add(checks.DataKeyTotalBytes, watchReadingLabelSize, humanize.IBytes(size))
	}
	if details, ok := data[checks.DataKeyRaidMembers].([]checks.RaidArrayStatus); ok {
		for _, detail := range details {
			rb.add(watchReadingFieldRAIDArrayPrefix+detail.Name, detail.Name, raidArrayReading(detail))
		}
	}
	return rb.readings()
}

func raidArrayReading(detail checks.RaidArrayStatus) string {
	state := "good"
	if detail.Degraded {
		state = "degraded"
	}
	if detail.Operation == "" {
		return state
	}
	if detail.HasProgress {
		return fmt.Sprintf("%s · %s %.1f%%", state, detail.Operation, detail.ProgressPct)
	}
	return state + readingSummarySeparator + detail.Operation
}

func certCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeySource, watchReadingLabelSource).
		addInt(checks.DataKeyDaysLeft, watchReadingLabelDaysLeft).
		addString(checks.DataKeyNotAfter, watchReadingLabelExpires).
		addString(checks.DataKeyPublicKeyAlgorithm, watchReadingLabelKeyType).
		addInt(checks.DataKeyKeyBits, watchReadingLabelKeyBits)
	if names, ok := data[checks.DataKeyDNSNames].([]string); ok && len(names) > 0 {
		rb.add(checks.DataKeyDNSNames, watchReadingLabelDNSNames, strings.Join(names, displayListSeparator))
	}
	return rb.addString(checks.DataKeyIssuer, watchReadingLabelIssuer).readings()
}

func countCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeyPath, watchReadingLabelPath).
		addString(checks.DataKeyOf, watchReadingLabelOf).
		addInt(checks.DataKeyCount, watchReadingLabelCount).
		addInt(checks.DataKeyBaselineCount, watchReadingLabelBaselineCount)
	if v, ok := cfgval.Int(data[checks.DataKeyGrowthCount]); ok {
		rb.add(checks.DataKeyGrowthCount, watchReadingLabelGrowth, fmt.Sprintf("%+d", v))
	}
	return rb.addString(checks.DataKeyWindow, watchReadingLabelWindow).readings()
}

func firewallCheckReadings(data map[string]any) []web.WatchReading {
	return readingsFrom(data).
		addString(checks.DataKeyBackend, watchReadingLabelBackend).
		addInt(checks.DataKeyRules, watchReadingLabelRules).
		addInt(checks.DataKeyMinRules, watchReadingLabelMinRules).
		readings()
}

func fileCheckReadings(data map[string]any) []web.WatchReading {
	return readingsFrom(data).
		addString(checks.DataKeyPath, watchReadingLabelPath).
		addString(checks.DataKeyKind, watchReadingLabelKind).
		addBytes(checks.DataKeySize, watchReadingLabelSize).
		addString(checks.DataKeyMode, watchReadingLabelMode).
		addString(checks.DataKeyModifiedAt, watchReadingLabelModifiedAt).
		addString(checks.DataKeyAge, watchReadingLabelAge).
		addString(checks.CheckKeyOwner, watchReadingLabelOwner).
		addInt(watchReadingFieldEntries, watchReadingLabelEntries).
		readings()
}

func processCheckReadings(data map[string]any) []web.WatchReading {
	return readingsFrom(data).
		addString(watchReadingFieldProcess, watchReadingLabelProcess).
		addString(watchReadingFieldUser, watchReadingLabelUser).
		addInt(watchReadingFieldMatches, watchReadingLabelMatches).
		addString(checks.DataKeyPIDs, watchReadingLabelPIDs).
		addIntMetric(watchReadingFieldRSS, watchReadingLabelRSS, metrics.MetricUnitBytes).
		addInt(watchReadingFieldCPUTicks, watchReadingLabelCPUTicks).
		addIntMetric(metrics.MetricIO, watchReadingLabelIO, metrics.MetricUnitBytes).
		readings()
}

func sizeCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeyPath, watchReadingLabelPath).
		addBytes(checks.DataKeyCurrentBytes, watchReadingLabelCurrentSize)
	if v, ok := cfgval.Int(data[checks.DataKeyGrowthBytes]); ok {
		rb.add(checks.DataKeyGrowthBytes, watchReadingLabelGrowth, checks.HumanizeSignedBytes(int64(v)))
	}
	return rb.readings()
}

func clockCheckReadings(data map[string]any) []web.WatchReading {
	return readingsFrom(data).
		addString(checks.DataKeyServer, watchReadingLabelServer).
		addInt(checks.DataKeyPort, watchReadingLabelPort).
		addMetric(checks.DataKeyOffsetSeconds, watchReadingLabelOffset, watchReadingClockDecimals, watchReadingUnitSeconds).
		addMetric(checks.DataKeyOffsetAbsSeconds, watchReadingLabelOffsetAbs, watchReadingClockDecimals, watchReadingUnitSeconds).
		addInt(checks.DataKeyStratum, watchReadingLabelStratum).
		addString(checks.DataKeyLeap, watchReadingLabelLeap).
		addMetric(checks.DataKeyPrecisionSeconds, watchReadingLabelPrecision, watchReadingClockPrecisionDecimals, watchReadingUnitSeconds).
		addMetric(checks.DataKeyRootDelayMS, watchReadingLabelRootDelay, watchReadingClockDecimals, metrics.MetricUnitMilliseconds).
		addMetric(checks.DataKeyRootDispersionMS, watchReadingLabelRootDispersion, watchReadingClockDecimals, metrics.MetricUnitMilliseconds).
		addString(checks.DataKeyReferenceID, watchReadingLabelReferenceID).
		readings()
}

func connCheckReadings(data map[string]any) []web.WatchReading {
	return readingsFrom(data).
		addString(checks.DataKeyHost, watchReadingLabelHost).
		addInt(checks.DataKeyPort, watchReadingLabelPort).
		addString(checks.DataKeySocket, watchReadingLabelSocket).
		addString(checks.DataKeyProtocol, watchReadingLabelProtocol).
		addIntMetric(checks.DataKeyLatencyMS, watchReadingLabelLatency, metrics.MetricUnitMilliseconds).
		readings()
}

func httpCheckReadings(data map[string]any) []web.WatchReading {
	out := connCheckReadings(data)
	if v, ok := cfgval.Int(data[checks.DataKeyStatus]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyStatus, Label: watchReadingLabelStatus, Value: strconv.Itoa(v)})
	}
	return out
}

func resourceCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeyPath, watchReadingLabelPath).
		addMetric(checks.DataKeyUsedPct, watchReadingLabelUsed, watchReadingDefaultMetricDecimals, metrics.MetricUnitPercent).
		addBytes(checks.DataKeyUsedBytes, watchReadingLabelUsedBytes).
		addBytes(checks.DataKeyFreeBytes, watchReadingLabelFreeBytes).
		addBytes(checks.DataKeyAvailableBytes, watchReadingLabelAvailable)
	label := watchReadingLabelValue
	switch checkType {
	case checks.CheckTypeLoad:
		label = watchReadingLabelLoad
	case checks.CheckTypeMemory:
		label = watchReadingLabelUsed
	case checks.CheckTypeSwap:
		label = watchReadingLabelFree
	}
	return rb.addMetric(checks.DataKeyValue, label, watchReadingDefaultMetricDecimals, "").readings()
}

// pressureFieldLabels maps PSI data fields to their dashboard labels.
var pressureFieldLabels = map[string]string{
	checks.PressureFieldSomeAvg10:  watchReadingLabelSomeAvg10,
	checks.PressureFieldSomeAvg60:  watchReadingLabelSomeAvg60,
	checks.PressureFieldSomeAvg300: watchReadingLabelSomeAvg300,
	checks.PressureFieldFullAvg10:  watchReadingLabelFullAvg10,
	checks.PressureFieldFullAvg60:  watchReadingLabelFullAvg60,
	checks.PressureFieldFullAvg300: watchReadingLabelFullAvg300,
}

func pressureCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).addString(checks.DataKeyResource, watchReadingLabelResource)
	for _, field := range checks.PressurePredFields {
		label := pressureFieldLabels[field]
		if label == "" {
			label = field
		}
		rb.addMetric(field, label, watchReadingDefaultMetricDecimals, metrics.MetricUnitPercent)
	}
	return rb.addMetric(checks.DataKeyValue, watchReadingLabelValue, watchReadingDefaultMetricDecimals, metrics.MetricUnitPercent).readings()
}

// diskioCheckReadings formats rates with the same precision as the check's own
// summary message: whole bytes per second, tenths of a millisecond for await.
func diskioCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).addString(checks.DataKeyDevice, watchReadingLabelDevice)
	for _, field := range []struct {
		key, label, unit string
		decimals         int
	}{
		{checks.DiskIOFieldUtilPct, watchReadingLabelUtilization, metrics.MetricUnitPercent, watchReadingDefaultMetricDecimals},
		{checks.DiskIOFieldReadBytes, watchReadingLabelRead, metrics.MetricUnitBytesPerSecond, 0},
		{checks.DiskIOFieldWriteBytes, watchReadingLabelWrite, metrics.MetricUnitBytesPerSecond, 0},
		{checks.DiskIOFieldAwaitMs, watchReadingLabelAwait, metrics.MetricUnitMilliseconds, 1},
	} {
		rb.addMetric(field.key, field.label, field.decimals, field.unit)
	}
	return rb.readings()
}

// sensorsCheckReadings prepends the matching-input count and the configured
// chip/label filters to the graphable sensor aggregates.
func sensorsCheckReadings(data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addInt(checks.DataKeyInputs, watchReadingLabelInputs).
		addString(checks.DataKeyChip, watchReadingLabelChipFilter).
		addString(checks.DataKeyLabel, watchReadingLabelLabelFilter)
	return append(rb.readings(), metricCheckReadings(checks.CheckTypeSensors, data)...)
}

func metricCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	rb := readingsFrom(data).
		addString(checks.DataKeyDevice, watchReadingLabelDevice).
		addString(checks.DataKeyResult, watchReadingLabelResult).
		addString(checks.DataKeyDeviceState, watchReadingLabelState).
		addString(checks.DataKeyHealth, watchReadingLabelHealth)
	for _, m := range checks.GraphMetrics(checkType) {
		v, ok := data[m.Key].(float64)
		if !ok {
			continue
		}
		value := watchReadingMetricValue(v, m.Decimals, m.Unit)
		if m.Unit == metrics.MetricUnitHours && v >= 0 && v <= float64(maxWatchReadingDuration)/float64(time.Hour) {
			value = units.HumanizeDuration(time.Duration(v * float64(time.Hour)))
		}
		label := m.Label
		if label == "" {
			label = m.Key
		}
		rb.add(m.Key, label, value)
	}
	return rb.readings()
}
