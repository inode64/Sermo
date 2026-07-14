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
	"sermo/internal/web"
)

const (
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
	watchReadingLabelCorrectable       = "Correctable"
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
	watchReadingLabelHottestTemp       = "Hottest temp"
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
	watchReadingLabelSlowestFan        = "Slowest fan"
	watchReadingLabelSocket            = "Socket"
	watchReadingLabelSomeAvg10         = "Some avg10"
	watchReadingLabelSomeAvg60         = "Some avg60"
	watchReadingLabelSomeAvg300        = "Some avg300"
	watchReadingLabelSource            = "Source"
	watchReadingLabelSpeed             = "Speed"
	watchReadingLabelState             = "State"
	watchReadingLabelStatus            = "Status"
	watchReadingLabelStratum           = "Stratum"
	watchReadingLabelUncorrectable     = "Uncorrectable"
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
	watchReadingLabelVoltage           = "Lowest voltage"
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
	watchReadingUnitBits               = metrics.MetricUnitBits
	watchReadingUnitCelsius            = "C"
	watchReadingUnitCelsiusSymbol      = metrics.MetricUnitCelsius
	watchReadingUnitMegabytesPerSecond = metrics.MetricUnitMegabytesPerSecond
	watchReadingUnitMegabitsPerSecond  = metrics.MetricUnitMegabitsPerSecond
	watchReadingUnitRPM                = metrics.MetricUnitRPM
	watchReadingUnitSeconds            = "s"
	watchReadingUnitVolt               = metrics.MetricUnitVolt
	maxWatchReadingDuration            = time.Duration(1<<63 - 1)
)

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
	case checks.CheckTypeHdparm, checks.CheckTypeSmart, checks.CheckTypeSensors, checks.CheckTypeEDAC:
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
	var out []web.WatchReading
	if result := cfgval.String(data[checks.DataKeyResult]); result != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyResult, Label: "Value", Value: result})
	}
	if op := cfgval.String(data[checks.DataKeyOp]); op != "" {
		if threshold := cfgval.String(data[checks.DataKeyThreshold]); threshold != "" {
			out = append(out, web.WatchReading{Field: checks.DataKeyThreshold, Label: "Condition", Value: op + " " + threshold})
		}
	}
	return out
}

func lvmCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if value := cfgval.String(data[checks.DataKeyDeviceState]); value != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDeviceState, Label: watchReadingLabelState, Value: value})
	}
	if progress, ok := cfgval.Float(data[checks.DataKeyProgressPct]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyProgressPct, Label: "Progress", Value: fmt.Sprintf("%.1f%%", progress)})
	}
	for _, item := range []struct{ field, label string }{{checks.DataKeyHealth, watchReadingLabelHealth}, {checks.DataKeyVolumeGroup, watchReadingLabelVolumeGroup}, {checks.DataKeyLogicalVolume, watchReadingLabelLogicalVolume}} {
		if value := cfgval.String(data[item.field]); value != "" {
			out = append(out, web.WatchReading{Field: item.field, Label: item.label, Value: value})
		}
	}
	if _, ok := data[checks.DataKeyLVMReasons]; ok {
		value := cfgval.String(data[checks.DataKeyLVMReasons])
		if value == "" {
			value = watchReadingValueNone
		}
		out = append(out, web.WatchReading{Field: checks.DataKeyLVMReasons, Label: watchReadingLabelReasons, Value: value})
	}
	for _, item := range []struct{ field, label string }{{checks.DataKeyLVMFreeBytes, watchReadingLabelVGFree}, {checks.DataKeyLVMSizeBytes, watchReadingLabelVGSize}, {checks.DataKeyLVMUsedBytes, watchReadingLabelVGUsed}} {
		if value, ok := byteField(data[item.field]); ok {
			out = append(out, web.WatchReading{Field: item.field, Label: item.label, Value: humanize.Bytes(value)})
		}
	}
	for _, item := range []struct{ field, label string }{{checks.DataKeyLVMFreePct, watchReadingLabelVGFreePct}, {checks.DataKeyLVMThinDataPct, "Thin data"}, {checks.DataKeyLVMThinMetadataPct, "Thin metadata"}} {
		if value, ok := cfgval.Float(data[item.field]); ok {
			out = append(out, web.WatchReading{Field: item.field, Label: item.label, Value: fmt.Sprintf("%.1f%%", value)})
		}
	}
	return out
}

func raidCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if value := cfgval.String(data[checks.DataKeyDeviceState]); value != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDeviceState, Label: watchReadingLabelState, Value: value})
	}
	if progress, ok := cfgval.Float(data[checks.DataKeyProgressPct]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyProgressPct, Label: "Progress", Value: fmt.Sprintf("%.1f%%", progress)})
	}
	for _, item := range []struct {
		key   string
		label string
	}{
		{checks.DataKeyArrays, watchReadingLabelArrays},
		{checks.DataKeyDegraded, watchReadingLabelDegraded},
		{checks.DataKeyRecovering, watchReadingLabelRecovering},
	} {
		if v := cfgval.String(data[item.key]); v != "" {
			out = append(out, web.WatchReading{Field: item.key, Label: item.label, Value: v})
		}
	}
	if array := cfgval.String(data[checks.DataKeyArray]); array != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyArray, Label: "Array", Value: array})
	}
	if operation := cfgval.String(data[checks.DataKeyRaidOperation]); operation != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyRaidOperation, Label: "Operation", Value: operation})
	}
	if progress, ok := cfgval.Float(data[checks.DataKeyRaidProgressPct]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyRaidProgressPct, Label: "Rebuild progress", Value: fmt.Sprintf("%.1f%%", progress)})
	}
	if mismatch := cfgval.String(data[checks.DataKeyRaidMismatchCount]); mismatch != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyRaidMismatchCount, Label: "Mismatch count", Value: mismatch})
	}
	if size, ok := byteField(data[checks.DataKeyTotalBytes]); ok && size > 0 {
		out = append(out, web.WatchReading{Field: checks.DataKeyTotalBytes, Label: watchReadingLabelSize, Value: humanize.Bytes(size)})
	}
	if details, ok := data[checks.DataKeyRaidMembers].([]checks.RaidArrayStatus); ok {
		for _, detail := range details {
			value := raidArrayReading(detail)
			out = append(out, web.WatchReading{Field: "raid_array_" + detail.Name, Label: detail.Name, Value: value})
		}
	}
	return out
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
	return state + " · " + detail.Operation
}

func certCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeySource]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeySource, Label: watchReadingLabelSource, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyDaysLeft]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyDaysLeft, Label: watchReadingLabelDaysLeft, Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data[checks.DataKeyNotAfter]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyNotAfter, Label: watchReadingLabelExpires, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyPublicKeyAlgorithm]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPublicKeyAlgorithm, Label: watchReadingLabelKeyType, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyKeyBits]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyKeyBits, Label: watchReadingLabelKeyBits, Value: strconv.Itoa(v)})
	}
	if names, ok := data[checks.DataKeyDNSNames].([]string); ok && len(names) > 0 {
		out = append(out, web.WatchReading{Field: checks.DataKeyDNSNames, Label: watchReadingLabelDNSNames, Value: strings.Join(names, displayListSeparator)})
	}
	if v := cfgval.String(data[checks.DataKeyIssuer]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyIssuer, Label: watchReadingLabelIssuer, Value: v})
	}
	return out
}

func countCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyOf]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyOf, Label: watchReadingLabelOf, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyCount]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyCount, Label: watchReadingLabelCount, Value: strconv.Itoa(v)})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyBaselineCount]); ok {
		out = append(out, web.WatchReading{
			Field: checks.DataKeyBaselineCount, Label: watchReadingLabelBaselineCount, Value: strconv.Itoa(v),
		})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyGrowthCount]); ok {
		out = append(out, web.WatchReading{
			Field: checks.DataKeyGrowthCount, Label: watchReadingLabelGrowth, Value: fmt.Sprintf("%+d", v),
		})
	}
	if v := cfgval.String(data[checks.DataKeyWindow]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyWindow, Label: watchReadingLabelWindow, Value: v})
	}
	return out
}

func firewallCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyBackend]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyBackend, Label: watchReadingLabelBackend, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyRules]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyRules, Label: watchReadingLabelRules, Value: strconv.Itoa(v)})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyMinRules]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyMinRules, Label: watchReadingLabelMinRules, Value: strconv.Itoa(v)})
	}
	return out
}

func fileCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyKind]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyKind, Label: watchReadingLabelKind, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeySize]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeySize, Label: watchReadingLabelSize, Value: humanize.Bytes(uint64(v))})
	}
	if v := cfgval.String(data[checks.DataKeyMode]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyMode, Label: watchReadingLabelMode, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyModifiedAt]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyModifiedAt, Label: watchReadingLabelModifiedAt, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyAge]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyAge, Label: watchReadingLabelAge, Value: v})
	}
	if v := cfgval.String(data[checks.CheckKeyOwner]); v != "" {
		out = append(out, web.WatchReading{Field: checks.CheckKeyOwner, Label: watchReadingLabelOwner, Value: v})
	}
	if v, ok := cfgval.Int(data[watchReadingFieldEntries]); ok {
		out = append(out, web.WatchReading{Field: watchReadingFieldEntries, Label: watchReadingLabelEntries, Value: strconv.Itoa(v)})
	}
	return out
}

func processCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[watchReadingFieldProcess]); v != "" {
		out = append(out, web.WatchReading{Field: watchReadingFieldProcess, Label: watchReadingLabelProcess, Value: v})
	}
	if v := cfgval.String(data[watchReadingFieldUser]); v != "" {
		out = append(out, web.WatchReading{Field: watchReadingFieldUser, Label: watchReadingLabelUser, Value: v})
	}
	if v, ok := cfgval.Int(data[watchReadingFieldMatches]); ok {
		out = append(out, web.WatchReading{Field: watchReadingFieldMatches, Label: watchReadingLabelMatches, Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data[checks.DataKeyPIDs]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPIDs, Label: watchReadingLabelPIDs, Value: v})
	}
	if v, ok := cfgval.Int(data[watchReadingFieldRSS]); ok {
		out = append(out, web.WatchReading{Field: watchReadingFieldRSS, Label: watchReadingLabelRSS, Value: fmt.Sprintf("%d %s", v, metrics.MetricUnitBytes)})
	}
	if v, ok := cfgval.Int(data[watchReadingFieldCPUTicks]); ok {
		out = append(out, web.WatchReading{Field: watchReadingFieldCPUTicks, Label: watchReadingLabelCPUTicks, Value: strconv.Itoa(v)})
	}
	if v, ok := cfgval.Int(data[metrics.MetricIO]); ok {
		out = append(out, web.WatchReading{Field: metrics.MetricIO, Label: watchReadingLabelIO, Value: fmt.Sprintf("%d %s", v, metrics.MetricUnitBytes)})
	}
	return out
}

func sizeCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyCurrentBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyCurrentBytes, Label: watchReadingLabelCurrentSize, Value: humanize.Bytes(uint64(v))})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyGrowthBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyGrowthBytes, Label: watchReadingLabelGrowth, Value: humanizeSigned(int64(v))})
	}
	return out
}

func clockCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyServer]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyServer, Label: watchReadingLabelServer, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyPort]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyPort, Label: watchReadingLabelPort, Value: strconv.Itoa(v)})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyOffsetSeconds]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyOffsetSeconds, Label: watchReadingLabelOffset, Value: watchReadingMetricValue(v, 3, watchReadingUnitSeconds)})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyOffsetAbsSeconds]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyOffsetAbsSeconds, Label: watchReadingLabelOffsetAbs, Value: watchReadingMetricValue(v, 3, watchReadingUnitSeconds)})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyStratum]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyStratum, Label: watchReadingLabelStratum, Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data[checks.DataKeyLeap]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyLeap, Label: watchReadingLabelLeap, Value: v})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyPrecisionSeconds]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyPrecisionSeconds, Label: watchReadingLabelPrecision, Value: watchReadingMetricValue(v, 6, watchReadingUnitSeconds)})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyRootDelayMS]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyRootDelayMS, Label: watchReadingLabelRootDelay, Value: watchReadingMetricValue(v, 3, metrics.MetricUnitMilliseconds)})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyRootDispersionMS]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyRootDispersionMS, Label: watchReadingLabelRootDispersion, Value: watchReadingMetricValue(v, 3, metrics.MetricUnitMilliseconds)})
	}
	if v := cfgval.String(data[checks.DataKeyReferenceID]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyReferenceID, Label: watchReadingLabelReferenceID, Value: v})
	}
	return out
}

func connCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyHost]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyHost, Label: watchReadingLabelHost, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyPort]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyPort, Label: watchReadingLabelPort, Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data[checks.DataKeySocket]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeySocket, Label: watchReadingLabelSocket, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyProtocol]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyProtocol, Label: watchReadingLabelProtocol, Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyLatencyMS]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyLatencyMS, Label: watchReadingLabelLatency, Value: watchReadingIntMetricValue(int64(v), metrics.MetricUnitMilliseconds)})
	}
	return out
}

func httpCheckReadings(data map[string]any) []web.WatchReading {
	out := connCheckReadings(data)
	if v, ok := cfgval.Int(data[checks.DataKeyStatus]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyStatus, Label: watchReadingLabelStatus, Value: strconv.Itoa(v)})
	}
	return out
}

func resourceCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelPath, Value: v})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyUsedPct]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyUsedPct, Label: watchReadingLabelUsed, Value: watchReadingMetricValue(v, 2, metrics.MetricUnitPercent)})
	}
	if v, ok := byteField(data[checks.DataKeyUsedBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyUsedBytes, Label: watchReadingLabelUsedBytes, Value: humanize.Bytes(v)})
	}
	if v, ok := byteField(data[checks.DataKeyFreeBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyFreeBytes, Label: watchReadingLabelFreeBytes, Value: humanize.Bytes(v)})
	}
	if v, ok := byteField(data[checks.DataKeyAvailableBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyAvailableBytes, Label: watchReadingLabelAvailable, Value: humanize.Bytes(v)})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyValue]); ok {
		label := watchReadingLabelValue
		switch checkType {
		case checks.CheckTypeLoad:
			label = watchReadingLabelLoad
		case checks.CheckTypeMemory:
			label = watchReadingLabelUsed
		case checks.CheckTypeSwap:
			label = watchReadingLabelFree
		}
		out = append(out, web.WatchReading{Field: checks.DataKeyValue, Label: label, Value: fmt.Sprintf("%.2f", v)})
	}
	return out
}

func pressureCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	for _, field := range checks.PressurePredFields {
		if v, ok := cfgval.Float(data[field]); ok {
			out = append(out, web.WatchReading{Field: field, Label: field, Value: watchReadingMetricValue(v, 2, metrics.MetricUnitPercent)})
		}
	}
	if v, ok := cfgval.Float(data[checks.DataKeyValue]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyValue, Label: watchReadingLabelValue, Value: watchReadingMetricValue(v, 2, metrics.MetricUnitPercent)})
	}
	return out
}

func diskioCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyDevice]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDevice, Label: watchReadingLabelDevice, Value: v})
	}
	for _, field := range []struct {
		key, label, unit string
	}{
		{checks.DiskIOFieldUtilPct, watchReadingLabelUtilization, metrics.MetricUnitPercent},
		{checks.DiskIOFieldReadBytes, watchReadingLabelRead, metrics.MetricUnitBytesPerSecond},
		{checks.DiskIOFieldWriteBytes, watchReadingLabelWrite, metrics.MetricUnitBytesPerSecond},
		{checks.DiskIOFieldAwaitMs, watchReadingLabelAwait, metrics.MetricUnitMilliseconds},
	} {
		if v, ok := cfgval.Float(data[field.key]); ok {
			out = append(out, web.WatchReading{
				Field: field.key, Label: field.label, Value: watchReadingMetricValue(v, 2, field.unit),
			})
		}
	}
	return out
}

func metricCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyDevice]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDevice, Label: watchReadingLabelDevice, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyResult]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyResult, Label: watchReadingLabelResult, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyDeviceState]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDeviceState, Label: watchReadingLabelState, Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyHealth]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyHealth, Label: watchReadingLabelHealth, Value: v})
	}
	for _, m := range checks.GraphMetrics(checkType) {
		if v, ok := data[m.Key].(float64); ok {
			value := watchReadingMetricValue(v, 0, m.Unit)
			if m.Unit == metrics.MetricUnitHours && v >= 0 && v <= float64(maxWatchReadingDuration)/float64(time.Hour) {
				value = formatInterval(time.Duration(v * float64(time.Hour)))
			}
			out = append(out, web.WatchReading{
				Field: m.Key, Label: m.Key, Value: value,
			})
		}
	}
	return out
}

func byteField(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	case float64:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}

func watchReadingIntMetricValue(value int64, unit string) string {
	if unit == "" {
		return strconv.FormatInt(value, 10)
	}
	return fmt.Sprintf("%d %s", value, unit)
}

func watchReadingUintMetricValue(value uint64, unit string) string {
	if unit == "" {
		return strconv.FormatUint(value, 10)
	}
	return fmt.Sprintf("%d %s", value, unit)
}

func watchReadingMetricValue(value float64, decimals int, unit string) string {
	if unit == "" {
		return fmt.Sprintf("%.*f", decimals, value)
	}
	if unit == metrics.MetricUnitPercent {
		return fmt.Sprintf("%.*f%s", decimals, value, unit)
	}
	return fmt.Sprintf("%.*f %s", decimals, value, unit)
}

func humanizeSigned(n int64) string {
	if n < 0 {
		return "-" + humanize.Bytes(uint64(-n))
	}
	return humanize.Bytes(uint64(n))
}
