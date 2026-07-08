package checks

import "sermo/internal/metrics"

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
	CheckTypeHdparm:       {{Key: fieldRead, Unit: metrics.MetricUnitMegabytesPerSecond}, {Key: fieldCached, Unit: metrics.MetricUnitMegabytesPerSecond}},
	CheckTypeSensors:      {{Key: sensorTemp, Unit: metrics.MetricUnitCelsius}, {Key: sensorFan, Unit: metrics.MetricUnitRPM}},
	CheckTypeSmart:        {{Key: fieldTemperature, Unit: metrics.MetricUnitCelsius}, {Key: fieldReallocated, Unit: metrics.MetricUnitNone}, {Key: fieldWear, Unit: metrics.MetricUnitPercent}, {Key: fieldPowerOnHours, Unit: metrics.MetricUnitHours}},
	CheckTypeEDAC:         {{Key: fieldCE, Unit: metrics.MetricUnitNone}, {Key: fieldUE, Unit: metrics.MetricUnitNone}},
	CheckTypeUsers:        {{Key: DataKeyCount, Unit: metrics.MetricUnitUsers}},
	CheckTypeProcessCount: {{Key: DataKeyCount, Unit: metrics.MetricUnitProcesses}},
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
