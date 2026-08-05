package checks

import "sermo/internal/metrics"

// GraphMetric describes a numeric field a check publishes in its Result.Data for
// time-series graphing: the Data key, the unit shown in the UI, and how the
// dashboard reading row presents it (Label falls back to Key; Decimals to 0).
type GraphMetric struct {
	Key      string
	Unit     string
	Label    string
	Decimals int
}

// voltageReadingDecimals keeps hundredths of a volt visible in reading rows,
// enough to spot a rail sagging.
const voltageReadingDecimals = 2

// graphMetrics maps a check type to the metrics it records over time. Giving a
// check graphs is just adding an entry here and writing the numeric value into
// Result.Data under Key — the recorder, store and web graph it generically, so
// this is reusable by any check (and service).
var graphMetrics = map[string][]GraphMetric{
	CheckTypeHdparm:       {{Key: fieldRead, Unit: metrics.MetricUnitMegabytesPerSecond, Decimals: 1}, {Key: fieldCached, Unit: metrics.MetricUnitMegabytesPerSecond, Decimals: 1}},
	CheckTypeSensors:      {{Key: sensorTemp, Unit: metrics.MetricUnitCelsius, Label: "Hottest temp", Decimals: 1}, {Key: sensorFan, Unit: metrics.MetricUnitRPM, Label: "Slowest fan"}, {Key: sensorVoltage, Unit: metrics.MetricUnitVolt, Label: "Lowest voltage", Decimals: voltageReadingDecimals}},
	CheckTypeSmart:        {{Key: fieldTemperature, Unit: metrics.MetricUnitCelsius}, {Key: fieldReallocated, Unit: metrics.MetricUnitNone}, {Key: fieldWear, Unit: metrics.MetricUnitPercent}, {Key: fieldPowerOnHours, Unit: metrics.MetricUnitHours}},
	CheckTypeEDAC:         {{Key: fieldCE, Unit: metrics.MetricUnitNone, Label: "Correctable"}, {Key: fieldUE, Unit: metrics.MetricUnitNone, Label: "Uncorrectable"}},
	CheckTypeUsers:        {{Key: DataKeyCount, Unit: metrics.MetricUnitUsers}},
	CheckTypeSSHIdle:      {{Key: DataKeyCount, Unit: metrics.MetricUnitSessions, Label: "Idle sessions"}, {Key: DataKeyProtectedCount, Unit: metrics.MetricUnitSessions, Label: "Protected sessions"}},
	CheckTypeProcessCount: {{Key: DataKeyCount, Unit: metrics.MetricUnitProcesses}},

	CheckTypeTCPConnections: {{Key: DataKeyCount, Unit: metrics.MetricUnitConnections, Label: "Connections"}},
}

// GraphMetrics returns the graphable metrics declared for a check type, or nil
// when the type publishes none.
func GraphMetrics(checkType string) []GraphMetric { return graphMetrics[checkType] }

// DeclaredGraphMetrics returns the graphable metrics for one configured check:
// the check type's static set, plus its own scalar result when the check
// declares a `unit:`. The per-check form exists because some types cannot
// declare a unit statically — a sql check's unit depends on its query, so five
// sensors on one service can report MiB, seconds and a bare count.
func DeclaredGraphMetrics(checkType, unit string) []GraphMetric {
	byType := GraphMetrics(checkType)
	if unit == "" {
		return byType
	}
	declared := GraphMetric{Key: DataKeyValue, Unit: unit, Label: graphMetricValueLabel, Decimals: 1}
	return append(append(make([]GraphMetric, 0, len(byType)+1), byType...), declared)
}

// graphMetricValueLabel names the scalar a check publishes under `unit:`.
const graphMetricValueLabel = "Value"

// GraphMetricUnit returns the unit for a check type's metric key, or "".
func GraphMetricUnit(checkType, key string) string {
	for _, m := range graphMetrics[checkType] {
		if m.Key == key {
			return m.Unit
		}
	}
	return ""
}
