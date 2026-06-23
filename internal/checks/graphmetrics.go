package checks

// GraphMetric describes a numeric field a check publishes in its Result.Data for
// time-series graphing: the Data key and the unit shown in the UI.
type GraphMetric struct {
	Key  string
	Unit string
}

// graphMetrics maps a check type to the metrics it records over time. Giving a
// check graphs is just adding an entry here and writing the numeric value into
// Result.Data under Key — the recorder, store and web graph it generically, so
// this is reusable by any check (and service).
var graphMetrics = map[string][]GraphMetric{
	"hdparm":  {{Key: "read", Unit: "MB/s"}, {Key: "cached", Unit: "MB/s"}},
	"sensors": {{Key: "temp", Unit: "°C"}, {Key: "fan", Unit: "RPM"}},
	"smart":   {{Key: "temperature", Unit: "°C"}, {Key: "reallocated", Unit: ""}, {Key: "wear", Unit: "%"}, {Key: "power_on_hours", Unit: "h"}},
	"edac":    {{Key: "ce", Unit: ""}, {Key: "ue", Unit: ""}},
	"users":   {{Key: "count", Unit: "users"}},
}

// GraphMetrics returns the graphable metrics declared for a check type, or nil
// when the type publishes none.
func GraphMetrics(checkType string) []GraphMetric { return graphMetrics[checkType] }

// GraphMetricUnit returns the unit for a check type's metric key, or "".
func GraphMetricUnit(checkType, key string) string {
	for _, m := range graphMetrics[checkType] {
		if m.Key == key {
			return m.Unit
		}
	}
	return ""
}
