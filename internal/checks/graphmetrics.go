package checks

import (
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
)

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

const (
	// tenthsDecimals is the precision of a percentage or a rate: a tenth is the
	// finest step worth reading off a utilisation figure.
	tenthsDecimals = 1
	// hundredthsDecimals is the precision of a load average and a stall share,
	// where the interesting movement happens below a tenth.
	hundredthsDecimals = 2
	// millisecondDecimals keeps a clock offset readable to the millisecond, the
	// scale at which NTP drift is worth seeing.
	millisecondDecimals = 3

	// graphMetricLabelUsed is the share-in-use label every capacity check carries.
	graphMetricLabelUsed = "Used"
)

// graphMetrics maps a check type to the metrics it records over time. Giving a
// check graphs is just adding an entry here and writing the numeric value into
// Result.Data under Key — the recorder, store and web graph it generically, so
// this is reusable by any check (and service).
var graphMetrics = map[string][]GraphMetric{
	CheckTypeHdparm:  {{Key: fieldRead, Unit: metrics.MetricUnitMegabytesPerSecond, Decimals: tenthsDecimals}, {Key: fieldCached, Unit: metrics.MetricUnitMegabytesPerSecond, Decimals: tenthsDecimals}},
	CheckTypeSensors: {{Key: sensorTemp, Unit: metrics.MetricUnitCelsius, Label: "Hottest temp", Decimals: tenthsDecimals}, {Key: sensorFan, Unit: metrics.MetricUnitRPM, Label: "Slowest fan"}, {Key: sensorVoltage, Unit: metrics.MetricUnitVolt, Label: "Lowest voltage", Decimals: voltageReadingDecimals}},
	CheckTypeSmart: {
		{Key: fieldTemperature, Unit: metrics.MetricUnitCelsius, Label: "Temperature"},
		{Key: fieldReallocated, Unit: metrics.MetricUnitNone, Label: "Reallocated sectors"},
		{Key: fieldPendingSectors, Unit: metrics.MetricUnitNone, Label: "Pending sectors"},
		{Key: fieldCRCErrors, Unit: metrics.MetricUnitNone, Label: "Link CRC errors"},
		{Key: fieldMediaErrors, Unit: metrics.MetricUnitNone, Label: "Media errors"},
		{Key: fieldWear, Unit: metrics.MetricUnitPercent, Label: "Wear"},
		{Key: fieldPowerOnHours, Unit: metrics.MetricUnitHours, Label: "Power-on time"},
	},
	CheckTypeStorCLI:          hardwareRAIDGraphMetrics,
	CheckTypeSSACLI:           hardwareRAIDGraphMetrics,
	CheckTypeEDAC:             {{Key: fieldCE, Unit: metrics.MetricUnitNone, Label: "Correctable"}, {Key: fieldUE, Unit: metrics.MetricUnitNone, Label: "Uncorrectable"}},
	CheckTypeUsers:            {{Key: DataKeyCount, Unit: metrics.MetricUnitUsers}},
	CheckTypeSSHIdle:          {{Key: DataKeyCount, Unit: metrics.MetricUnitSessions, Label: "Idle sessions"}, {Key: DataKeyProtectedCount, Unit: metrics.MetricUnitSessions, Label: "Protected sessions"}},
	CheckTypeTerminalSessions: {{Key: DataKeyCount, Unit: metrics.MetricUnitSessions, Label: "Sessions"}, {Key: DataKeyAttached, Unit: metrics.MetricUnitSessions, Label: "Attached"}, {Key: DataKeyDetached, Unit: metrics.MetricUnitSessions, Label: "Detached"}},
	CheckTypeProcessCount:     {{Key: DataKeyCount, Unit: metrics.MetricUnitProcesses}},
	// Strays graph for the reason the count exists at all: a leak is visible as
	// accumulation over hours, not as one sample. The service's own process_count
	// and memory series already move with a leak — but they move with legitimate
	// load too, so only this series says the growth is unexplained.
	CheckTypeStrays: {{Key: DataKeyCount, Unit: metrics.MetricUnitProcesses, Label: "Strays"}},

	CheckTypeTCPConnections: {{Key: DataKeyCount, Unit: metrics.MetricUnitConnections, Label: "Connections"}},
	// The OOM killer's per-cycle kill count: a spike names the moment memory ran
	// out, which the level alert alone timestamps but cannot shape.
	CheckTypeOOM: {{Key: DataKeyKills, Unit: metrics.MetricUnitNone, Label: "OOM kills"}},
	// One round-trip sample per cycle of an icmp latency watch; the state form
	// publishes no latency and is scoped out per entry below.
	CheckTypeICMP: {{Key: DataKeyLatencyMS, Unit: metrics.MetricUnitMilliseconds, Label: "Latency", Decimals: hundredthsDecimals}},
	// The one raid number that is a magnitude rather than a state: the kernel's
	// mismatch_cnt after a check/repair pass.
	CheckTypeRAID:        {{Key: DataKeyRaidMismatchCount, Unit: metrics.MetricUnitNone, Label: "Mismatch count"}},
	CheckTypeReplication: {{Key: DataKeyBehindSeconds, Unit: metrics.MetricUnitSeconds, Label: "Behind source"}},

	// The level checks: the figures an operator writes thresholds against. A check
	// that computes a number every cycle and compares it to a limit is exactly a
	// check whose number is worth a graph — the limit says when to look, the graph
	// says what led there, and both read from the same Result.Data field.
	//
	// Only fields the check actually writes into Data are listed, and complements
	// are deliberately left out: free_pct is a hundred minus used_pct, a total is a
	// constant, and the 60s/300s pressure averages are running means of the 10s
	// one. Graphing those would add panels without adding anything to read.
	CheckTypeStorage: {
		{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals},
		{Key: DataKeyUsedBytes, Unit: metrics.MetricUnitBytes, Label: "Used bytes"},
		{Key: DataKeyInodesUsedPct, Unit: metrics.MetricUnitPercent, Label: "Inodes used", Decimals: tenthsDecimals},
	},
	CheckTypeSwap: {{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals}},
	CheckTypeMemory: {
		{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals},
		{Key: DataKeyAvailableBytes, Unit: metrics.MetricUnitBytes, Label: "Available"},
	},
	CheckTypeLoad: {
		{Key: DataKeyLoad1, Unit: metrics.MetricUnitNone, Label: "Load 1m", Decimals: hundredthsDecimals},
		{Key: DataKeyLoad5, Unit: metrics.MetricUnitNone, Label: "Load 5m", Decimals: hundredthsDecimals},
		{Key: DataKeyLoad15, Unit: metrics.MetricUnitNone, Label: "Load 15m", Decimals: hundredthsDecimals},
	},
	CheckTypePressure: {
		{Key: PressureFieldSomeAvg10, Unit: metrics.MetricUnitPercent, Label: "Some stalled", Decimals: hundredthsDecimals},
		{Key: PressureFieldFullAvg10, Unit: metrics.MetricUnitPercent, Label: "Fully stalled", Decimals: hundredthsDecimals},
	},
	CheckTypeDiskIO: {
		{Key: DiskIOFieldUtilPct, Unit: metrics.MetricUnitPercent, Label: "Utilisation", Decimals: tenthsDecimals},
		{Key: DiskIOFieldReadBytes, Unit: metrics.MetricUnitBytesPerSecond, Label: "Read"},
		{Key: DiskIOFieldWriteBytes, Unit: metrics.MetricUnitBytesPerSecond, Label: "Write"},
		{Key: DiskIOFieldAwaitMs, Unit: metrics.MetricUnitMilliseconds, Label: "Await", Decimals: hundredthsDecimals},
	},
	CheckTypeFDS: {
		{Key: DataKeyAllocated, Unit: metrics.MetricUnitNone, Label: "Allocated"},
		{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals},
	},
	CheckTypePIDs: {
		{Key: DataKeyCount, Unit: metrics.MetricUnitProcesses, Label: "In use"},
		{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals},
	},
	CheckTypeConntrack: {
		{Key: DataKeyCount, Unit: metrics.MetricUnitConnections, Label: "Entries"},
		{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals},
	},
	CheckTypeInotify: {
		{Key: DataKeyUsedPct, Unit: metrics.MetricUnitPercent, Label: graphMetricLabelUsed, Decimals: tenthsDecimals},
		{Key: DataKeyInstances, Unit: metrics.MetricUnitNone, Label: "Instances"},
	},
	CheckTypeZombies:       {{Key: DataKeyZombies, Unit: metrics.MetricUnitProcesses, Label: "Zombies"}},
	CheckTypeFailedUnits:   {{Key: DataKeyCount, Unit: metrics.MetricUnitNone, Label: "Failed units"}},
	CheckTypeCount:         {{Key: DataKeyCount, Unit: metrics.MetricUnitNone, Label: "Count"}},
	CheckTypeFirewallRules: {{Key: DataKeyRules, Unit: metrics.MetricUnitNone, Label: "Rules"}},
	CheckTypeSize:          {{Key: DataKeyCurrentBytes, Unit: metrics.MetricUnitBytes, Label: "Size"}},
	// Signed, not the absolute offset the check compares: a clock that runs fast
	// and one that runs slow are different faults, and the sign is what says which.
	CheckTypeClock: {{Key: DataKeyOffsetSeconds, Unit: metrics.MetricUnitSeconds, Label: "Offset", Decimals: millisecondDecimals}},
	CheckTypeLVM: {
		{Key: DataKeyLVMFreePct, Unit: metrics.MetricUnitPercent, Label: "Free", Decimals: tenthsDecimals},
		{Key: DataKeyLVMThinDataPct, Unit: metrics.MetricUnitPercent, Label: "Thin data used", Decimals: tenthsDecimals},
		{Key: DataKeyLVMThinMetadataPct, Unit: metrics.MetricUnitPercent, Label: "Thin metadata used", Decimals: tenthsDecimals},
	},
}

var hardwareRAIDGraphMetrics = []GraphMetric{
	{Key: fieldTemperature, Unit: metrics.MetricUnitCelsius, Label: "Maximum temperature"},
	{Key: DataKeyRaidProgressPct, Unit: metrics.MetricUnitPercent, Label: "RAID operation progress", Decimals: tenthsDecimals},
	{Key: DataKeyHardwareRAIDMediaErrors, Unit: metrics.MetricUnitNone, Label: "Media errors"},
	{Key: DataKeyHardwareRAIDPredictiveFailures, Unit: metrics.MetricUnitNone, Label: "Predictive failures"},
	{Key: DataKeyHardwareRAIDSMARTAlerts, Unit: metrics.MetricUnitNone, Label: "SMART alerts"},
	{Key: DataKeyHardwareRAIDUncorrectableErrors, Unit: metrics.MetricUnitNone, Label: "Uncorrectable errors"},
}

// NumericData coerces one Result.Data value to a float64, reporting whether it
// was a number at all.
//
// Deliberately not cfgval.Float: that one also parses a numeric string, because a
// configured threshold may arrive from YAML as text. A result field is written by
// the check itself, so a string there is a label — a device name, a state — and
// reading "3" out of one would graph a word. Both the recorder and the reading
// rows select fields through this, so what is graphed and what is displayed can
// never disagree about which fields are numbers.
func NumericData(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case uint:
		return float64(t), true
	default:
		return 0, false
	}
}

// GraphMetrics returns the graphable metrics declared for a check type, or nil
// when the type publishes none.
func GraphMetrics(checkType string) []GraphMetric { return slices.Clone(graphMetrics[checkType]) }

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
	declared := GraphMetric{Key: DataKeyValue, Unit: unit, Label: graphMetricValueLabel, Decimals: tenthsDecimals}
	return append(append(make([]GraphMetric, 0, len(byType)+1), byType...), declared)
}

// graphMetricValueLabel names the scalar a check publishes under `unit:`.
const graphMetricValueLabel = "Value"

// graphMetricEntryScope narrows types whose metrics exist only in some of their
// configured forms: an icmp watch measures latency only when its `metric:` says
// so, and advertising the panel on a state watch would offer a series nothing
// writes. Keyed by metric key; absent means the metric always applies.
var graphMetricEntryScope = map[string]map[string]func(entry map[string]any) bool{
	CheckTypeICMP: {
		DataKeyLatencyMS: func(entry map[string]any) bool {
			return cfgval.AsString(entry[CheckKeyMetric]) == IcmpMetricLatency
		},
	},
}

// ResolvedGraphMetrics is the line-metric set of one configured check: the
// type's declared metrics plus its `unit:` scalar, scoped by the entry's own
// form and minus the keys the check has banded — a state draws as a band or a
// line, never both. The recorder, the payload and the read gates all resolve
// through this one function, so what is persisted, offered and served cannot
// disagree.
func ResolvedGraphMetrics(checkType, unit string, entry map[string]any) []GraphMetric {
	declared := DeclaredGraphMetrics(checkType, unit)
	banded := BandKeys(DeclaredBandMetrics(checkType, entry))
	scope := graphMetricEntryScope[checkType]
	out := declared[:0:0]
	for _, m := range declared {
		if banded[m.Key] {
			continue
		}
		if gate, scoped := scope[m.Key]; scoped && !gate(entry) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// DeclaredGraphMetricKey reports whether key is a graph metric the check type
// declares statically — the set a `bands:` block may convert to a state band.
func DeclaredGraphMetricKey(checkType, key string) bool {
	for _, m := range graphMetrics[checkType] {
		if m.Key == key {
			return true
		}
	}
	return false
}
