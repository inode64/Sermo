package checks

import (
	"fmt"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
)

// levelPred is one {op, value} threshold predicate on a named field of a level
// check (disk, swap usage, fds, conntrack, load, sensors, hdparm, smart, raid,
// edac). Every level check shares this type and the parse grammar below.
type levelPred struct {
	field string
	op    string // >= > <= < == !=
	value float64
}

// Predicate field lists, one per level check. They are exported so config
// validation walks the same lists and both layers stay in step by construction.
var (
	// DiskPredFields are the space/inode predicates of a storage/disk check.
	DiskPredFields = []string{"used_pct", "free_pct", "used_bytes", "free_bytes", "inodes_used_pct", "inodes_free_pct", "inodes_free"}
	// SwapUsageFields are the predicates of a swap usage metric.
	SwapUsageFields = []string{"used_pct", "free_pct", "free_bytes"}
	// MemoryPredFields are the predicates of a memory check.
	MemoryPredFields = []string{"used_pct", "available_pct", "available_bytes"}
	// PressurePredFields are the predicates of a pressure (PSI) check: the
	// rolling stall percentages of the some/full lines.
	PressurePredFields = []string{"some_avg10", "some_avg60", "some_avg300", "full_avg10", "full_avg60", "full_avg300"}
	// DiskIOPredFields are the predicates of a diskio check: per-cycle rates
	// (read_bytes/write_bytes are bytes per second, so the size-suffix grammar
	// reads naturally, e.g. "50M").
	DiskIOPredFields = []string{"util_pct", "read_bytes", "write_bytes", "await_ms"}
	// FdsPredFields are the predicates of an fds check.
	FdsPredFields = []string{"used_pct", "free", "allocated"}
	// PidsPredFields are the predicates of a pids check.
	PidsPredFields = []string{"used_pct", "free", "count"}
	// ConntrackPredFields are the predicates of a conntrack check.
	ConntrackPredFields = []string{"used_pct", "free", "count"}
	// LoadPredFields are the predicates of a load check.
	LoadPredFields = []string{"load1", "load5", "load15"}
	// SensorPredFields are the predicates of a sensors check.
	SensorPredFields = []string{"temp", "fan", "voltage"}
	// HdparmPredFields are the predicates of an hdparm check.
	HdparmPredFields = []string{"read", "cached"}
	// SmartPredFields are the optional attribute predicates of a smart check.
	SmartPredFields = []string{"temperature", "reallocated", "wear", "power_on_hours"}
	// RaidPredFields are the optional predicates of a raid check.
	RaidPredFields = []string{"degraded", "recovering", "arrays"}
	// EdacPredFields are the optional predicates of an edac check.
	EdacPredFields = []string{"ce", "ue"}
	// EntropyPredFields is the single required predicate of an entropy check.
	EntropyPredFields = []string{"avail"}
	// ZombiePredFields is the single required predicate of a zombies check.
	ZombiePredFields = []string{"count"}
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
		op := cfgval.AsString(m["op"])
		if !cfgval.IsCompareOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := parseLevelPredValue(field, m["value"])
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

// parseDeltaThreshold parses a `delta` {op, value} mapping — the per-cycle
// counter threshold shared by net errors, swap io and oom. It returns a
// builder-style error string when the shape, op or value is invalid.
func parseDeltaThreshold(raw any, label string) (op string, value float64, errs string) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", 0, label + " requires a delta {op, value}"
	}
	op = cfgval.AsString(m["op"])
	if !cfgval.IsCompareOp(op) {
		return "", 0, label + " delta has an invalid op"
	}
	value, err := strconv.ParseFloat(cfgval.String(m["value"]), 64)
	if err != nil {
		return "", 0, label + " delta value must be numeric"
	}
	return op, value, ""
}

// levelPredsHold reports whether every predicate holds against values — the
// level-check AND. A field absent from values can never hold (how disk treats
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
	if strings.HasSuffix(field, "_bytes") {
		n, ok := cfgval.ByteSize(raw)
		if !ok {
			return 0, fmt.Errorf("%s value %q must include a size suffix (K, M, G or T; e.g. 10G)", field, value)
		}
		return float64(n), nil
	}
	if strings.HasSuffix(field, "_pct") {
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%"))
		val, err := strconv.ParseFloat(s, 64)
		if err != nil || val < 0 || val > 100 {
			return 0, fmt.Errorf("%s value %q must be a percentage in 0..100 (e.g. 90 or 90%%)", field, value)
		}
		return val, nil
	}
	val, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s value %q is not numeric", field, value)
	}
	return val, nil
}

// compareFloat evaluates one comparison via the shared cfgval vocabulary.
func compareFloat(a float64, op string, b float64) bool {
	return cfgval.CompareFloat(a, op, b)
}
