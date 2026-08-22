package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/cfgval"
)

// TestRaidBandsReplaceItsLineCharts pins the registry split: raid's degraded
// and recovering are state bands now, and no longer graph metrics — the two
// presentations of one state must never coexist.
func TestRaidBandsReplaceItsLineCharts(t *testing.T) {
	if g := GraphMetrics(CheckTypeRAID); len(g) != 0 {
		t.Fatalf("raid still declares graph metrics %+v; its states are bands", g)
	}
	bands := DeclaredBandMetrics(CheckTypeRAID, map[string]any{})
	if len(bands) != 2 {
		t.Fatalf("raid bands = %+v, want degraded and recovering", bands)
	}
	byKey := map[string]BandMetric{}
	for _, b := range bands {
		byKey[b.Key] = b
	}
	degraded, recovering := byKey[DataKeyDegraded], byKey[DataKeyRecovering]
	if degraded.Severity != SeverityError || recovering.Severity != SeverityWarning {
		t.Fatalf("severities = %q/%q, want error/warning: a rebuild is a thing to watch, not an outage", degraded.Severity, recovering.Severity)
	}
	if !degraded.OKFor(0) || degraded.OKFor(1) || degraded.OKFor(2) {
		t.Fatal("degraded OK must hold at exactly 0: two degraded arrays are not okay either")
	}
}

// TestBandsAreWrittenIntoResultData is the band counterpart of the graph-metric
// invariant: a declared band key must be a numeric field the check's result
// actually carries, because that field is the only thing the recorder samples.
func TestBandsAreWrittenIntoResultData(t *testing.T) {
	res := buildOneCheck(t, "c", CheckTypeRAID, map[string]any{"degraded": pred(">", 0)},
		Deps{DefaultTimeout: time.Second, Samplers: Samplers{
			RaidSampler: func() (RaidStatus, error) { return RaidStatus{Arrays: 2, Degraded: 1, Recovering: 1}, nil },
		}}).Run(context.Background())
	for _, band := range DeclaredBandMetrics(CheckTypeRAID, map[string]any{}) {
		v, ok := NumericData(res.Data[band.Key])
		if !ok {
			t.Fatalf("band %q not numeric in result data: %v", band.Key, res.Data)
		}
		if band.OKFor(v) {
			t.Fatalf("band %q reads OK on a degraded+recovering status (value %v)", band.Key, v)
		}
	}
}

// TestFileSizeBandDerivation pins the zero-config path for a dead-letter watch:
// the band is the negation of the watch's own size predicate.
func TestFileSizeBandDerivation(t *testing.T) {
	entry := map[string]any{
		CheckKeySize: map[string]any{CheckKeyOp: ">", CheckKeyValue: 0},
	}
	bands := DeclaredBandMetrics(CheckTypeFile, entry)
	if len(bands) != 1 || bands[0].Key != DataKeySize {
		t.Fatalf("file bands = %+v, want the one size band", bands)
	}
	band := bands[0]
	if band.OK.Op != cfgval.CompareOpLessEqual || band.OK.Value != 0 {
		t.Fatalf("derived OK = %s %v, want <= 0 (the negation of > 0)", band.OK.Op, band.OK.Value)
	}
	if !band.OKFor(0) || band.OKFor(1) {
		t.Fatal("size 0 must be OK and size 1 must not")
	}
	if got := DeclaredBandMetrics(CheckTypeFile, map[string]any{}); len(got) != 0 {
		t.Fatalf("a file watch without a size predicate has nothing to band: %+v", got)
	}
}

// TestBandOverridesMergeOverDefaults pins the `bands:` grammar: partial
// overrides keep the untouched fields, false removes, a graph metric converts,
// and an unknown key is dropped rather than invented.
func TestBandOverridesMergeOverDefaults(t *testing.T) {
	entry := map[string]any{
		CheckKeyBands: map[string]any{
			// partial: demote to amber, keep the default ==0 predicate
			DataKeyDegraded: map[string]any{CheckKeySeverity: SeverityWarning},
			// disable the default recovering band
			DataKeyRecovering: false,
			// unknown key: neither a default band nor a raid graph metric
			"ghost": map[string]any{CheckKeySeverity: SeverityError},
		},
	}
	bands := DeclaredBandMetrics(CheckTypeRAID, entry)
	if len(bands) != 1 {
		t.Fatalf("bands = %+v, want only the adjusted degraded", bands)
	}
	if bands[0].Key != DataKeyDegraded || bands[0].Severity != SeverityWarning {
		t.Fatalf("degraded override = %+v, want severity warning", bands[0])
	}
	if !bands[0].OKFor(0) || bands[0].OKFor(1) {
		t.Fatal("partial override must keep the default ==0 predicate")
	}

	// A graph metric converts to a band when the override supplies its OK.
	converted := DeclaredBandMetrics(CheckTypeLoad, map[string]any{
		CheckKeyBands: map[string]any{
			DataKeyLoad1: map[string]any{
				CheckKeyOK:       map[string]any{CheckKeyOp: "<", CheckKeyValue: 8},
				CheckKeySeverity: SeverityWarning,
			},
		},
	})
	if len(converted) != 1 || converted[0].Key != DataKeyLoad1 || !converted[0].OKFor(2) || converted[0].OKFor(9) {
		t.Fatalf("converted load1 band = %+v", converted)
	}
}
