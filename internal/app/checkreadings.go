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
	case "hdparm", "smart":
		return metricCheckReadings(checkType, data)
	default:
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

func humanizeSigned(n int64) string {
	if n < 0 {
		return "-" + humanize.Bytes(uint64(-n))
	}
	return humanize.Bytes(uint64(n))
}
