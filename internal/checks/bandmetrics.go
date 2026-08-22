package checks

import (
	"sort"

	"sermo/internal/cfgval"
)

// BandPred is the {op, value} predicate whose truth means "this state is OK".
// It reuses the comparison vocabulary every threshold in this configuration
// speaks, so a band's OK condition reads exactly like the alert conditions
// beside it.
type BandPred struct {
	Op    string
	Value float64
}

// BandMetric is a state a check publishes as a number: 0 or 1 rather than a
// magnitude, "which" rather than "how much". It renders as an availability-style
// band — green while OK holds, affected while it does not — never as a line
// chart, because a line through a boolean draws slopes that never happened.
// Severity grades the failing colour: an error band's affected spans read red on
// the usual down-share scale, a warning band's cap at amber, because a RAID
// array rebuilding is a thing to watch, not an outage.
type BandMetric struct {
	Key      string
	Label    string   // panel title; the client falls back to Key
	OK       BandPred // evaluated against Result.Data[Key]
	Severity string   // SeverityError | SeverityWarning
}

// OKFor reports whether one sampled value satisfies this band's OK predicate.
func (m BandMetric) OKFor(v float64) bool {
	return cfgval.CompareFloat(v, m.OK.Op, m.OK.Value)
}

// bandMetrics maps a check type to the states it publishes as bands. Like
// graphMetrics it is a declaration the recorder, the API and the dashboard all
// read, so what is persisted, what is served and what is drawn cannot disagree.
// A key listed here must be a numeric field the check writes into Result.Data.
var bandMetrics = map[string][]BandMetric{
	CheckTypeReplication: {
		{Key: DataKeyIOStopped, Label: "IO thread", OK: BandPred{Op: cfgval.CompareOpEqual, Value: 0}, Severity: SeverityError},
		{Key: DataKeySQLStopped, Label: "SQL thread", OK: BandPred{Op: cfgval.CompareOpEqual, Value: 0}, Severity: SeverityError},
	},
	CheckTypeRAID: {
		{Key: DataKeyDegraded, Label: "Degraded arrays",
			OK: BandPred{Op: cfgval.CompareOpEqual, Value: 0}, Severity: SeverityError},
		{Key: DataKeyRecovering, Label: "Recovering arrays",
			OK: BandPred{Op: cfgval.CompareOpEqual, Value: 0}, Severity: SeverityWarning},
	},
}

// DeclaredBandMetrics resolves the band metrics of one configured check: the
// type's registry defaults, plus the file watch's derivation from its own size
// predicate, adjusted by the check's `bands:` block.
//
// The file derivation is what lets a dead-letter watch declare nothing at all:
// `size: {op: ">", value: 0}` already states when the watch fires, and the band
// is simply its negation — OK while size <= 0, and while the file is absent.
//
// The `bands:` block merges per metric and per field over those defaults: an
// entry that names only `severity:` keeps the default OK predicate, `false`
// removes a default band, and a key that is neither a default band nor a graph
// metric of the type is dropped here — config validation rejects it at load, so
// at runtime lenience cannot invent a series nothing writes. A graph-metric key
// converts that metric from a line chart to a band; DeclaredGraphMetrics
// callers exclude banded keys so the two presentations never coexist.
func DeclaredBandMetrics(checkType string, entry map[string]any) []BandMetric {
	byKey := map[string]BandMetric{}
	order := []string{}
	put := func(m BandMetric) {
		if _, seen := byKey[m.Key]; !seen {
			order = append(order, m.Key)
		}
		byKey[m.Key] = m
	}
	for _, m := range bandMetrics[checkType] {
		put(m)
	}
	if m, ok := fileSizeBand(checkType, entry); ok {
		put(m)
	}
	applyBandOverrides(checkType, entry, byKey, put)

	out := make([]BandMetric, 0, len(byKey))
	for _, key := range order {
		if m, ok := byKey[key]; ok {
			out = append(out, m)
		}
	}
	// Overrides may have deleted entries; order carries ghosts, so out is
	// compacted above. Keys added by overrides were appended in map order by
	// applyBandOverrides's sorted walk, keeping the result deterministic.
	return out
}

// fileSizeBand derives the one band a file watch keeps: the negation of its own
// size predicate. Absence is handled by the recorder (absent_ok decides the
// sample), not here — this only names the series and its OK condition.
func fileSizeBand(checkType string, entry map[string]any) (BandMetric, bool) {
	if checkType != CheckTypeFile {
		return BandMetric{}, false
	}
	pred, ok := entry[CheckKeySize].(map[string]any)
	if !ok {
		return BandMetric{}, false
	}
	op := cfgval.InvertCompareOp(cfgval.AsString(pred[CheckKeyOp]))
	value, numeric := cfgval.Float(pred[CheckKeyValue])
	if op == "" || !numeric {
		return BandMetric{}, false
	}
	return BandMetric{
		Key:      DataKeySize,
		Label:    "Size threshold",
		OK:       BandPred{Op: op, Value: value},
		Severity: SeverityError,
	}, true
}

// applyBandOverrides folds the check's `bands:` block into the resolved set.
func applyBandOverrides(checkType string, entry map[string]any, byKey map[string]BandMetric, put func(BandMetric)) {
	block, ok := entry[CheckKeyBands].(map[string]any)
	if !ok {
		return
	}
	keys := make([]string, 0, len(block))
	for key := range block {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := block[key]
		if disabled, isBool := raw.(bool); isBool {
			if !disabled {
				delete(byKey, key)
			}
			continue
		}
		override, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		base, exists := byKey[key]
		if !exists {
			base, exists = graphMetricAsBand(checkType, key)
			if !exists {
				continue
			}
		}
		if pred, ok := override[CheckKeyOK].(map[string]any); ok {
			op := cfgval.AsString(pred[CheckKeyOp])
			if value, numeric := cfgval.Float(pred[CheckKeyValue]); numeric && cfgval.IsCompareOp(op) {
				base.OK = BandPred{Op: op, Value: value}
			}
		}
		if severity := cfgval.AsString(override[CheckKeySeverity]); IsCheckSeverity(severity) {
			base.Severity = severity
		}
		put(base)
	}
}

// graphMetricAsBand promotes a declared graph metric of the type to a band
// seed, so a `bands:` entry can convert a line chart into a state band. The OK
// predicate has no sensible default for an arbitrary metric, so the override
// must supply one; until it does the seed's empty predicate holds for nothing,
// which config validation reports rather than letting a band record all-down.
func graphMetricAsBand(checkType, key string) (BandMetric, bool) {
	for _, m := range graphMetrics[checkType] {
		if m.Key == key {
			return BandMetric{Key: key, Label: m.Label, Severity: SeverityError}, true
		}
	}
	return BandMetric{}, false
}

// BandKeys returns the banded metric keys of one configured check, for the
// callers that must exclude them from the line-metric set.
func BandKeys(bands []BandMetric) map[string]bool {
	if len(bands) == 0 {
		return nil
	}
	out := make(map[string]bool, len(bands))
	for _, m := range bands {
		out[m.Key] = true
	}
	return out
}
