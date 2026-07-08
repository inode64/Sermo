package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
)

const (
	watchReadingLabelAddresses         = "Addresses"
	watchReadingLabelAllocated         = "Allocated"
	watchReadingLabelArrays            = "Arrays"
	watchReadingLabelAvailable         = "Available"
	watchReadingLabelAwait             = "Await"
	watchReadingLabelBackend           = "Backend"
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
	watchReadingLabelMatches           = "Matches"
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
	watchReadingLabelRSS               = "RSS total"
	watchReadingLabelRTT               = "RTT"
	watchReadingLabelRules             = "Rules"
	watchReadingLabelSample            = "Sample"
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
	watchReadingLabelUncorrectable     = "Uncorrectable"
	watchReadingLabelUsed              = "Used"
	watchReadingLabelUsedBytes         = "Used bytes"
	watchReadingLabelUtilization       = "Utilization"
	watchReadingLabelUser              = "User"
	watchReadingLabelValue             = "Value"
	watchReadingLabelVoltage           = "Lowest voltage"
	watchReadingLabelWindow            = "Window"
	watchReadingLabelWrite             = "Write"
	watchReadingLabelZombies           = "Zombies"
)

const (
	watchReadingUnitBits               = metrics.MetricUnitBits
	watchReadingUnitCelsius            = "C"
	watchReadingUnitCelsiusSymbol      = metrics.MetricUnitCelsius
	watchReadingUnitMegabytesPerSecond = metrics.MetricUnitMegabytesPerSecond
	watchReadingUnitMegabitsPerSecond  = metrics.MetricUnitMegabitsPerSecond
	watchReadingUnitRPM                = metrics.MetricUnitRPM
	watchReadingUnitVolt               = metrics.MetricUnitVolt
)

func checkReadings(checkType string, data map[string]any) []web.WatchReading {
	if len(data) == 0 {
		return nil
	}
	switch checkType {
	case checks.CheckTypeCert:
		return certCheckReadings(data)
	case checks.CheckTypeCount:
		return countCheckReadings(data)
	case checks.CheckTypeFirewallRules:
		return firewallCheckReadings(data)
	case checks.CheckTypeFile, checks.CheckTypeFileExists:
		return fileCheckReadings(data)
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
	case checks.CheckTypeHdparm, checks.CheckTypeSmart, checks.CheckTypeSensors, checks.CheckTypeEDAC:
		return metricCheckReadings(checkType, data)
	default:
		if metrics := checks.GraphMetrics(checkType); len(metrics) > 0 {
			return metricCheckReadings(checkType, data)
		}
		return nil
	}
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
	if v, ok := cfgval.Int(data[checks.DataKeySize]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeySize, Label: watchReadingLabelSize, Value: humanize.Bytes(uint64(v))})
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
	if v := cfgval.String(data[checks.DataKeyHealth]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyHealth, Label: watchReadingLabelHealth, Value: v})
	}
	for _, m := range checks.GraphMetrics(checkType) {
		if v, ok := data[m.Key].(float64); ok {
			out = append(out, web.WatchReading{
				Field: m.Key, Label: m.Key, Value: watchReadingMetricValue(v, 0, m.Unit),
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
