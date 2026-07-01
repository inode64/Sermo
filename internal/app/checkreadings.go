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
	case "cert":
		return certCheckReadings(data)
	case "count":
		return countCheckReadings(data)
	case "firewall_rules":
		return firewallCheckReadings(data)
	case "file", "file_exists":
		return fileCheckReadings(data)
	case "size":
		return sizeCheckReadings(data)
	case "tcp", "ports":
		return connCheckReadings(data)
	case "http", "https":
		return httpCheckReadings(data)
	case "storage", "swap", "memory", "load":
		return resourceCheckReadings(checkType, data)
	case "pressure":
		return pressureCheckReadings(data)
	case "diskio":
		return diskioCheckReadings(data)
	case "hdparm", "smart", "sensors", "edac":
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
	if v := cfgval.String(data["source"]); v != "" {
		out = append(out, web.WatchReading{Field: "source", Label: "Source", Value: v})
	}
	if v, ok := cfgval.Int(data["days_left"]); ok {
		out = append(out, web.WatchReading{Field: "days_left", Label: "Days left", Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data["not_after"]); v != "" {
		out = append(out, web.WatchReading{Field: "not_after", Label: "Expires", Value: v})
	}
	if v := cfgval.String(data["issuer"]); v != "" {
		out = append(out, web.WatchReading{Field: "issuer", Label: "Issuer", Value: v})
	}
	if v := cfgval.String(data["public_key_algorithm"]); v != "" {
		out = append(out, web.WatchReading{Field: "public_key_algorithm", Label: "Key type", Value: v})
	}
	if v, ok := cfgval.Int(data["key_bits"]); ok {
		out = append(out, web.WatchReading{Field: "key_bits", Label: "Key bits", Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data["subject"]); v != "" {
		out = append(out, web.WatchReading{Field: "subject", Label: "Subject", Value: v})
	}
	if names, ok := data["dns_names"].([]string); ok && len(names) > 0 {
		out = append(out, web.WatchReading{Field: "dns_names", Label: "DNS names", Value: strings.Join(names, ", ")})
	}
	return out
}

func countCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["path"]); v != "" {
		out = append(out, web.WatchReading{Field: "path", Label: "Path", Value: v})
	}
	if v := cfgval.String(data["of"]); v != "" {
		out = append(out, web.WatchReading{Field: "of", Label: "Of", Value: v})
	}
	if v, ok := cfgval.Int(data["count"]); ok {
		out = append(out, web.WatchReading{Field: "count", Label: "Count", Value: strconv.Itoa(v)})
	}
	return out
}

func firewallCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["backend"]); v != "" {
		out = append(out, web.WatchReading{Field: "backend", Label: "Backend", Value: v})
	}
	if v, ok := cfgval.Int(data["rules"]); ok {
		out = append(out, web.WatchReading{Field: "rules", Label: "Rules", Value: strconv.Itoa(v)})
	}
	if v, ok := cfgval.Int(data["min_rules"]); ok {
		out = append(out, web.WatchReading{Field: "min_rules", Label: "Min rules", Value: strconv.Itoa(v)})
	}
	return out
}

func fileCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["path"]); v != "" {
		out = append(out, web.WatchReading{Field: "path", Label: "Path", Value: v})
	}
	if v, ok := cfgval.Int(data["size"]); ok {
		out = append(out, web.WatchReading{Field: "size", Label: "Size", Value: humanize.Bytes(uint64(v))})
	}
	return out
}

func sizeCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["path"]); v != "" {
		out = append(out, web.WatchReading{Field: "path", Label: "Path", Value: v})
	}
	if v, ok := cfgval.Int(data["current_bytes"]); ok {
		out = append(out, web.WatchReading{Field: "current_bytes", Label: "Current size", Value: humanize.Bytes(uint64(v))})
	}
	if v, ok := cfgval.Int(data["growth_bytes"]); ok {
		out = append(out, web.WatchReading{Field: "growth_bytes", Label: "Growth", Value: humanizeSigned(int64(v))})
	}
	return out
}

func connCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["host"]); v != "" {
		out = append(out, web.WatchReading{Field: "host", Label: "Host", Value: v})
	}
	if v, ok := cfgval.Int(data["port"]); ok {
		out = append(out, web.WatchReading{Field: "port", Label: "Port", Value: strconv.Itoa(v)})
	}
	if v := cfgval.String(data["socket"]); v != "" {
		out = append(out, web.WatchReading{Field: "socket", Label: "Socket", Value: v})
	}
	if v := cfgval.String(data["protocol"]); v != "" {
		out = append(out, web.WatchReading{Field: "protocol", Label: "Protocol", Value: v})
	}
	if v, ok := cfgval.Int(data["latency_ms"]); ok {
		out = append(out, web.WatchReading{Field: "latency_ms", Label: "Latency", Value: strconv.Itoa(v) + " ms"})
	}
	return out
}

func httpCheckReadings(data map[string]any) []web.WatchReading {
	out := connCheckReadings(data)
	if v, ok := cfgval.Int(data["status"]); ok {
		out = append(out, web.WatchReading{Field: "status", Label: "Status", Value: strconv.Itoa(v)})
	}
	return out
}

func resourceCheckReadings(checkType string, data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["path"]); v != "" {
		out = append(out, web.WatchReading{Field: "path", Label: "Path", Value: v})
	}
	if v, ok := cfgval.Float(data["used_pct"]); ok {
		out = append(out, web.WatchReading{Field: "used_pct", Label: "Used", Value: fmt.Sprintf("%.2f%%", v)})
	}
	if v, ok := byteField(data["used_bytes"]); ok {
		out = append(out, web.WatchReading{Field: "used_bytes", Label: "Used bytes", Value: humanize.Bytes(v)})
	}
	if v, ok := byteField(data["free_bytes"]); ok {
		out = append(out, web.WatchReading{Field: "free_bytes", Label: "Free bytes", Value: humanize.Bytes(v)})
	}
	if v, ok := byteField(data["available_bytes"]); ok {
		out = append(out, web.WatchReading{Field: "available_bytes", Label: "Available", Value: humanize.Bytes(v)})
	}
	if v, ok := cfgval.Float(data["value"]); ok {
		label := "Value"
		switch checkType {
		case "load":
			label = "Load"
		case "memory":
			label = "Used"
		case "swap":
			label = "Free"
		}
		out = append(out, web.WatchReading{Field: "value", Label: label, Value: fmt.Sprintf("%.2f", v)})
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
	if v, ok := cfgval.Float(data["value"]); ok {
		out = append(out, web.WatchReading{Field: "value", Label: "Value", Value: fmt.Sprintf("%.2f%%", v)})
	}
	return out
}

func diskioCheckReadings(data map[string]any) []web.WatchReading {
	var out []web.WatchReading
	if v := cfgval.String(data["device"]); v != "" {
		out = append(out, web.WatchReading{Field: "device", Label: "Device", Value: v})
	}
	for _, field := range []struct {
		key, label, unit string
	}{
		{"util_pct", "Utilization", "%"},
		{"read_bytes", "Read", " B/s"},
		{"write_bytes", "Write", " B/s"},
		{"await_ms", "Await", " ms"},
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
	if v := cfgval.String(data["device"]); v != "" {
		out = append(out, web.WatchReading{Field: "device", Label: "Device", Value: v})
	}
	if v := cfgval.String(data["health"]); v != "" {
		out = append(out, web.WatchReading{Field: "health", Label: "Health", Value: v})
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
