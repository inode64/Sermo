package checks

import (
	"fmt"
	"math"
	"strings"

	"sermo/internal/cfgval"
)

// levelPred is one {op, value} threshold predicate on a named field of a level
// check (storage, swap usage, fds, conntrack, load, sensors, hdparm, smart, raid,
// edac). Every level check shares this type and the parse grammar below.
type levelPred struct {
	field string
	op    string // >= > <= < == !=
	value float64
}

// Shared metric field names reused across level checks in both their predicate
// lists and their result data maps.
const (
	// fieldUsedPct is the "% used" field (storage, swap, memory, fds, pids, conntrack).
	fieldUsedPct = "used_pct"
	// fieldFreePct is the "% free" field (storage, swap).
	fieldFreePct = "free_pct"
	// fieldFreeBytes is the "free bytes" field (storage, swap).
	fieldFreeBytes = "free_bytes"
	// fieldFree is the "free slots" field of the count checks (fds, pids, conntrack).
	fieldFree = "free"
	// fieldAvailablePct is the "% available" field of a memory check.
	fieldAvailablePct = "available_pct"
	// fieldAvailableBytes is the "available bytes" field of a memory check.
	fieldAvailableBytes = "available_bytes"
	// fieldLoad1, fieldLoad5 and fieldLoad15 are the load-average fields of a load check.
	fieldLoad1  = "load1"
	fieldLoad5  = "load5"
	fieldLoad15 = "load15"
	// diskio per-cycle rate fields.
	fieldUtilPct    = "util_pct"
	fieldReadBytes  = "read_bytes"
	fieldWriteBytes = "write_bytes"
	fieldAwaitMs    = "await_ms"
	// pressure (PSI) stall-percentage fields for the some/full lines.
	fieldSomeAvg10  = "some_avg10"
	fieldSomeAvg60  = "some_avg60"
	fieldSomeAvg300 = "some_avg300"
	fieldFullAvg10  = "full_avg10"
	fieldFullAvg60  = "full_avg60"
	fieldFullAvg300 = "full_avg300"
	// storage space/inode fields.
	fieldUsedBytes     = "used_bytes"
	fieldInodesUsedPct = "inodes_used_pct"
	fieldInodesFreePct = "inodes_free_pct"
	fieldInodesFree    = "inodes_free"
	// fieldTotalBytes is the total-size field shared by storage, memory and swap.
	fieldTotalBytes = "total_bytes"
	// raid array-health fields.
	fieldDegraded   = "degraded"
	fieldRecovering = "recovering"
	fieldArrays     = "arrays"
	// smart attribute fields.
	fieldTemperature  = "temperature"
	fieldReallocated  = "reallocated"
	fieldWear         = "wear"
	fieldPowerOnHours = "power_on_hours"
	// edac ECC error-count fields (correctable / uncorrectable).
	fieldCE = "ce"
	fieldUE = "ue"
	// hdparm timing fields (buffered disk read / cached read).
	fieldRead   = "read"
	fieldCached = "cached"
	// fieldOld and fieldNew are the before/after values a change-detection check
	// (net/icmp state/speed/address, command on_change) reports in its data map.
	fieldOld = "old"
	fieldNew = "new"
	// fieldMetric is the result data-map key naming the sub-metric a multi-metric
	// check (net/icmp/swap) reported.
	fieldMetric = "metric"
	// fieldValue is the result data-map key holding the breaching number a hook
	// sees as SERMO_VALUE. Every level/stateful check publishes it. (This is the
	// output key; the config-input threshold is entry[CheckKeyValue], left as-is.)
	fieldValue = "value"
	// fieldHost and fieldPort are the target-identity result data-map keys of the
	// endpoint checks (icmp, ports, cert, connection-protocol probes).
	fieldHost = "host"
	fieldPort = "port"
	// fieldTotal is the result data-map key holding a cumulative counter total
	// (oom kills, net errors) or a target-set size (ports).
	fieldTotal = "total"
)

// LevelFieldUsedPct is the public `used_pct` predicate/data field.
const LevelFieldUsedPct = fieldUsedPct

// LevelFieldFreePct is the public `free_pct` predicate/data field.
const LevelFieldFreePct = fieldFreePct

// LevelFieldUsedBytes is the public `used_bytes` predicate/data field.
const LevelFieldUsedBytes = fieldUsedBytes

// LevelFieldFreeBytes is the public `free_bytes` predicate/data field.
const LevelFieldFreeBytes = fieldFreeBytes

// DiskIOFieldUtilPct is the public disk utilization predicate/data field.
const DiskIOFieldUtilPct = fieldUtilPct

// DiskIOFieldReadBytes is the public disk read-rate predicate/data field.
const DiskIOFieldReadBytes = fieldReadBytes

// DiskIOFieldWriteBytes is the public disk write-rate predicate/data field.
const DiskIOFieldWriteBytes = fieldWriteBytes

// DiskIOFieldAwaitMs is the public disk await-time predicate/data field.
const DiskIOFieldAwaitMs = fieldAwaitMs

// PressureFieldSomeAvg10 is the public PSI `some avg10` predicate/data field.
const PressureFieldSomeAvg10 = fieldSomeAvg10

// PressureFieldSomeAvg60 is the public PSI `some avg60` predicate/data field.
const PressureFieldSomeAvg60 = fieldSomeAvg60

// PressureFieldSomeAvg300 is the public PSI `some avg300` predicate/data field.
const PressureFieldSomeAvg300 = fieldSomeAvg300

// PressureFieldFullAvg10 is the public PSI `full avg10` predicate/data field.
const PressureFieldFullAvg10 = fieldFullAvg10

// PressureFieldFullAvg60 is the public PSI `full avg60` predicate/data field.
const PressureFieldFullAvg60 = fieldFullAvg60

// PressureFieldFullAvg300 is the public PSI `full avg300` predicate/data field.
const PressureFieldFullAvg300 = fieldFullAvg300

// HdparmFieldRead is the public buffered-read predicate/data field.
const HdparmFieldRead = fieldRead

// HdparmFieldCached is the public cached-read predicate/data field.
const HdparmFieldCached = fieldCached

// SmartFieldTemperature is the public SMART temperature predicate/data field.
const SmartFieldTemperature = fieldTemperature

// SmartFieldReallocated is the public SMART reallocated-sector predicate/data field.
const SmartFieldReallocated = fieldReallocated

// SmartFieldWear is the public SMART wear predicate/data field.
const SmartFieldWear = fieldWear

// SmartFieldPowerOnHours is the public SMART power-on-hours predicate/data field.
const SmartFieldPowerOnHours = fieldPowerOnHours

// Predicate field lists, one per level check. They are exported so config
// validation walks the same lists and both layers stay in step by construction.
var (
	// StoragePredFields are the space/inode predicates of a storage check.
	StoragePredFields = []string{fieldUsedPct, fieldFreePct, fieldUsedBytes, fieldFreeBytes, fieldInodesUsedPct, fieldInodesFreePct, fieldInodesFree}
	// SwapUsageFields are the predicates of a swap usage metric.
	SwapUsageFields = []string{fieldUsedPct, fieldFreePct, fieldFreeBytes}
	// MemoryPredFields are the predicates of a memory check.
	MemoryPredFields = []string{fieldUsedPct, fieldAvailablePct, fieldAvailableBytes}
	// PressurePredFields are the predicates of a pressure (PSI) check: the
	// rolling stall percentages of the some/full lines.
	PressurePredFields = []string{
		PressureFieldSomeAvg10,
		PressureFieldSomeAvg60,
		PressureFieldSomeAvg300,
		PressureFieldFullAvg10,
		PressureFieldFullAvg60,
		PressureFieldFullAvg300,
	}
	// DiskIOPredFields are the predicates of a diskio check: per-cycle rates
	// (read_bytes/write_bytes are bytes per second, so the size-suffix grammar
	// reads naturally, e.g. "50M").
	DiskIOPredFields = []string{DiskIOFieldUtilPct, DiskIOFieldReadBytes, DiskIOFieldWriteBytes, DiskIOFieldAwaitMs}
	// FdsPredFields are the predicates of an fds check.
	FdsPredFields = []string{fieldUsedPct, fieldFree, DataKeyAllocated}
	// PidsPredFields are the predicates of a pids check.
	PidsPredFields = []string{fieldUsedPct, fieldFree, DataKeyCount}
	// ConntrackPredFields are the predicates of a conntrack check.
	ConntrackPredFields = []string{fieldUsedPct, fieldFree, DataKeyCount}
	// LoadPredFields are the predicates of a load check.
	LoadPredFields = []string{fieldLoad1, fieldLoad5, fieldLoad15}
	// UsersPredFields is the single required predicate of a users check.
	UsersPredFields = []string{DataKeyCount}
	// ProcessCountPredFields is the single required predicate of a process_count check.
	ProcessCountPredFields = []string{DataKeyCount}
	// SensorPredFields are the predicates of a sensors check.
	SensorPredFields = []string{sensorTemp, sensorFan, sensorVoltage}
	// HdparmPredFields are the predicates of an hdparm check.
	HdparmPredFields = []string{HdparmFieldRead, HdparmFieldCached}
	// SmartPredFields are the optional attribute predicates of a smart check.
	SmartPredFields = []string{SmartFieldTemperature, SmartFieldReallocated, SmartFieldWear, SmartFieldPowerOnHours}
	// RaidPredFields are the optional predicates of a raid check.
	RaidPredFields = []string{fieldDegraded, fieldRecovering, fieldArrays}
	// LVMPredFields are capacity predicates for an LVM logical volume or volume group.
	LVMPredFields = []string{DataKeyLVMFreePct, DataKeyLVMThinDataPct, DataKeyLVMThinMetadataPct}
	// EdacPredFields are the optional predicates of an edac check.
	EdacPredFields = []string{fieldCE, fieldUE}
	// EntropyPredFields is the single required predicate of an entropy check.
	EntropyPredFields = []string{DataKeyAvail}
	// ZombiePredFields is the single required predicate of a zombies check.
	ZombiePredFields = []string{DataKeyCount}
)

// parseLevelPreds reads the {op, value} predicates present in entry among
// fields. Each value is parsed by its field's form — `*_bytes` requires a size
// suffix (K/M/G/T), `*_pct` accepts a number or a trailing % in 0..100, and
// anything else is plain numeric — so every level check shares one grammar.
// The result may be empty; checks that are meaningless without a threshold use
// requireLevelPreds instead.
func parseLevelPreds(entry map[string]any, fields []string) ([]levelPred, error) {
	var preds []levelPred
	for _, field := range fields {
		raw, ok := entry[field]
		if !ok {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping {op, value}", field)
		}
		op := cfgval.AsString(m[CheckKeyOp])
		if !cfgval.IsCompareOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := parseLevelPredValue(field, m[CheckKeyValue])
		if err != nil {
			return nil, err
		}
		preds = append(preds, levelPred{field: field, op: op, value: val})
	}
	return preds, nil
}

// requireLevelPreds is parseLevelPreds for the checks that require at least one
// predicate; it returns a builder-style error string naming the accepted fields
// when none is present.
func requireLevelPreds(entry map[string]any, fields []string, label string) ([]levelPred, string) {
	preds, err := parseLevelPreds(entry, fields)
	if err != nil {
		return nil, label + ": " + err.Error()
	}
	if len(preds) == 0 {
		return nil, label + ": requires at least one of " + strings.Join(fields, "/")
	}
	return preds, ""
}

// requireSingleLevelPred returns the only predicate of a check whose allowed
// field list contains exactly one entry.
func requireSingleLevelPred(entry map[string]any, fields []string, label string) (levelPred, string) {
	preds, errs := requireLevelPreds(entry, fields, label)
	if errs != "" {
		return levelPred{}, errs
	}
	return preds[0], ""
}

// parseDeltaThreshold parses a `delta` {op, value} mapping — the per-cycle
// counter threshold shared by net errors, swap io and oom. It returns a
// builder-style error string when the shape, op or value is invalid.
func parseDeltaThreshold(raw any, label string) (op string, value float64, errs string) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", 0, label + " requires a delta {op, value}"
	}
	op = cfgval.AsString(m[CheckKeyOp])
	if !cfgval.IsCompareOp(op) {
		return "", 0, label + " delta has an invalid op"
	}
	value, ok = cfgval.Float(m[CheckKeyValue])
	if !ok {
		return "", 0, label + " delta value must be numeric"
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return "", 0, label + " delta value must be a finite number"
	}
	return op, value, ""
}

// levelPredsHold reports whether every predicate holds against values — the
// level-check AND. A field absent from values can never hold (how storage treats
// inode predicates on an inode-less filesystem, and fds/pids treat an unknown
// kernel limit).
func levelPredsHold(preds []levelPred, values map[string]float64) bool {
	for _, p := range preds {
		v, known := values[p.field]
		if !known || !compareFloat(v, p.op, p.value) {
			return false
		}
	}
	return true
}

// firstPredValue returns the first predicate's reading — the breaching number a
// hook sees as SERMO_VALUE — or fallback when no predicate (or no reading)
// applies.
func firstPredValue(preds []levelPred, values map[string]float64, fallback float64) float64 {
	if len(preds) > 0 {
		if v, ok := values[preds[0].field]; ok {
			return v
		}
	}
	return fallback
}

// deltaOrZero is the shared counter-delta clamp every stateful check uses: a
// cumulative counter that went backwards (reset, device re-plug, module
// reload) yields a zero delta instead of a giant unsigned wraparound.
func deltaOrZero(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// parseLevelPredValue parses one predicate value by its field's form.
func parseLevelPredValue(field string, raw any) (float64, error) {
	value := cfgval.String(raw)
	if strings.HasSuffix(field, LevelFieldSuffixBytes) {
		n, ok := cfgval.ByteSize(raw)
		if !ok {
			return 0, fmt.Errorf("%s value %q must include a size suffix (K, M, G or T; e.g. 10G)", field, value)
		}
		return float64(n), nil
	}
	if strings.HasSuffix(field, LevelFieldSuffixPct) {
		val, ok := cfgval.Percent(raw)
		if !ok {
			return 0, fmt.Errorf("%s value %q must be a percentage in %s (e.g. 90 or 90%%)", field, value, cfgval.PercentRange())
		}
		return val, nil
	}
	val, ok := cfgval.Float(raw)
	if !ok {
		return 0, fmt.Errorf("%s value %q is not numeric", field, value)
	}
	if math.IsInf(val, 0) || math.IsNaN(val) {
		return 0, fmt.Errorf("%s value %q must be a finite number", field, value)
	}
	return val, nil
}

// compareFloat evaluates one comparison via the shared cfgval vocabulary.
func compareFloat(a float64, op string, b float64) bool {
	return cfgval.CompareFloat(a, op, b)
}
