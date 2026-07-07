package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/web"
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
		out = append(out, web.WatchReading{Field: checks.DataKeySource, Label: "Source", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyDaysLeft]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyDaysLeft, Label: "Days left", Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data[checks.DataKeyNotAfter]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyNotAfter, Label: "Expires", Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyPublicKeyAlgorithm]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPublicKeyAlgorithm, Label: "Key type", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyKeyBits]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyKeyBits, Label: "Key bits", Value: strconv.Itoa(v)})
	}
	if names, ok := data[checks.DataKeyDNSNames].([]string); ok && len(names) > 0 {
		out = append(out, web.WatchReading{Field: checks.DataKeyDNSNames, Label: "DNS names", Value: strings.Join(names, ", ")})
	}
	if v := cfgval.String(data[checks.DataKeyIssuer]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyIssuer, Label: "Issuer", Value: v})
	}
	return out
}

func countCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: "Path", Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyOf]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyOf, Label: "Of", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyCount]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyCount, Label: "Count", Value: strconv.Itoa(v)})
	}
	return out
}

func firewallCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyBackend]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyBackend, Label: "Backend", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyRules]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyRules, Label: "Rules", Value: strconv.Itoa(v)})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyMinRules]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyMinRules, Label: "Min rules", Value: strconv.Itoa(v)})
	}
	return out
}

func fileCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: "Path", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeySize]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeySize, Label: "Size", Value: humanize.Bytes(uint64(v))})
	}
	return out
}

func sizeCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: "Path", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyCurrentBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyCurrentBytes, Label: "Current size", Value: humanize.Bytes(uint64(v))})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyGrowthBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyGrowthBytes, Label: "Growth", Value: humanizeSigned(int64(v))})
	}
	return out
}

func connCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyHost]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyHost, Label: "Host", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyPort]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyPort, Label: "Port", Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data[checks.DataKeySocket]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeySocket, Label: "Socket", Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyProtocol]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyProtocol, Label: "Protocol", Value: v})
	}
	if v, ok := cfgval.Int(data[checks.DataKeyLatencyMS]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyLatencyMS, Label: "Latency", Value: strconv.Itoa(v) + " ms"})
	}
	return out
}

func httpCheckReadings(data map[string]any) []web.WatchReading {
	out := connCheckReadings(data)
	if v, ok := cfgval.Int(data[checks.DataKeyStatus]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyStatus, Label: "Status", Value: strconv.Itoa(v)})
	}
	return out
}

func resourceCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyPath]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyPath, Label: "Path", Value: v})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyUsedPct]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyUsedPct, Label: "Used", Value: fmt.Sprintf("%.2f%%", v)})
	}
	if v, ok := byteField(data[checks.DataKeyUsedBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyUsedBytes, Label: "Used bytes", Value: humanize.Bytes(v)})
	}
	if v, ok := byteField(data[checks.DataKeyFreeBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyFreeBytes, Label: "Free bytes", Value: humanize.Bytes(v)})
	}
	if v, ok := byteField(data[checks.DataKeyAvailableBytes]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyAvailableBytes, Label: "Available", Value: humanize.Bytes(v)})
	}
	if v, ok := cfgval.Float(data[checks.DataKeyValue]); ok {
		label := "Value"
		switch checkType {
		case checks.CheckTypeLoad:
			label = "Load"
		case checks.CheckTypeMemory:
			label = "Used"
		case checks.CheckTypeSwap:
			label = "Free"
		}
		out = append(out, web.WatchReading{Field: checks.DataKeyValue, Label: label, Value: fmt.Sprintf("%.2f", v)})
	}
	return out
}

func pressureCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	for _, field := range []string{"some_avg10", "some_avg60", "some_avg300", "full_avg10", "full_avg60", "full_avg300"} {
		if v, ok := cfgval.Float(data[field]); ok {
			out = append(out, web.WatchReading{Field: field, Label: field, Value: fmt.Sprintf("%.2f%%", v)})
		}
	}
	if v, ok := cfgval.Float(data[checks.DataKeyValue]); ok {
		out = append(out, web.WatchReading{Field: checks.DataKeyValue, Label: "Value", Value: fmt.Sprintf("%.2f%%", v)})
	}
	return out
}

func diskioCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyDevice]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDevice, Label: "Device", Value: v})
	}
	for _, field := range []struct {
		key, label, unit string
	}{
		{checks.DiskIOFieldUtilPct, "Utilization", "%"},
		{checks.DiskIOFieldReadBytes, "Read", " B/s"},
		{checks.DiskIOFieldWriteBytes, "Write", " B/s"},
		{checks.DiskIOFieldAwaitMs, "Await", " ms"},
	} {
		if v, ok := cfgval.Float(data[field.key]); ok {
			out = append(out, web.WatchReading{
				Field: field.key, Label: field.label, Value: fmt.Sprintf("%.2f%s", v, field.unit),
			})
		}
	}
	return out
}

func metricCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data[checks.DataKeyDevice]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyDevice, Label: "Device", Value: v})
	}
	if v := cfgval.String(data[checks.DataKeyHealth]); v != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyHealth, Label: "Health", Value: v})
	}
	for _, m := range checks.GraphMetrics(checkType) {
		if v, ok := data[m.Key].(float64); ok {
			unit := m.Unit
			if unit != "" && !strings.HasPrefix(unit, " ") {
				unit = " " + unit
			}
			out = append(out, web.WatchReading{
				Field: m.Key, Label: m.Key, Value: fmt.Sprintf("%.0f%s", v, unit),
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

func humanizeSigned(n int64) string {
	if n < 0 {
		return "-" + humanize.Bytes(uint64(-n))
	}
	return humanize.Bytes(uint64(n))
}
