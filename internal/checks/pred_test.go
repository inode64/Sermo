package checks

import "testing"

// The one predicate grammar every level check shares: *_pct accepts a number
// or a % suffix in 0..100, *_bytes requires a size suffix, the rest is numeric.
func TestParseLevelPredGrammar(t *testing.T) {
	preds, err := parseLevelPreds(map[string]any{
		"used_pct":   map[string]any{"op": ">=", "value": "85%"},
		"free_bytes": map[string]any{"op": "<", "value": "1G"},
	}, SwapUsageFields)
	if err != nil || len(preds) != 2 {
		t.Fatalf("preds = %v, err = %v", preds, err)
	}
	if preds[0].field != "used_pct" || preds[0].value != 85 {
		t.Errorf("used_pct = %+v, want value 85", preds[0])
	}
	if preds[1].field != "free_bytes" || preds[1].value != 1<<30 {
		t.Errorf("free_bytes = %+v, want value 1Gi", preds[1])
	}

	if _, err := parseLevelPreds(map[string]any{
		"used_pct": map[string]any{"op": ">=", "value": "150%"},
	}, SwapUsageFields); err == nil {
		t.Error("a percentage above 100 must error")
	}
	if _, err := parseLevelPreds(map[string]any{
		"free_bytes": map[string]any{"op": "<", "value": 1024},
	}, SwapUsageFields); err == nil {
		t.Error("a unitless byte size must error")
	}
	if _, err := parseLevelPreds(map[string]any{
		"load5": map[string]any{"op": ">", "value": "high"},
	}, LoadPredFields); err == nil {
		t.Error("a non-numeric plain value must error")
	}
	if _, err := parseLevelPreds(map[string]any{
		"load1": map[string]any{"op": ">", "value": "inf"},
	}, LoadPredFields); err == nil {
		t.Error("an infinite plain numeric predicate value must error")
	}
	if _, err := parseLevelPreds(map[string]any{
		"used_pct": map[string]any{"op": "<=", "value": "nan"},
	}, SwapUsageFields); err == nil {
		t.Error("a NaN percentage predicate value must error")
	}
}

func TestRequireSingleLevelPred(t *testing.T) {
	pred, errs := requireSingleLevelPred(map[string]any{
		DataKeyAvail: map[string]any{CheckKeyOp: "<", CheckKeyValue: 200},
	}, EntropyPredFields, "entropy check")
	if errs != "" {
		t.Fatalf("requireSingleLevelPred warning = %q", errs)
	}
	if pred.field != DataKeyAvail || pred.op != "<" || pred.value != 200 {
		t.Fatalf("requireSingleLevelPred = %+v", pred)
	}
	if _, errs := requireSingleLevelPred(map[string]any{}, ZombiePredFields, "zombies check"); errs == "" {
		t.Fatal("missing predicate must return a warning")
	}
}

func TestParseDeltaThresholdRejectsNonFinite(t *testing.T) {
	if _, _, err := parseDeltaThreshold(map[string]any{"op": ">", "value": "inf"}, "x"); err == "" {
		t.Error("delta inf must be rejected")
	}
	if _, _, err := parseDeltaThreshold(map[string]any{"op": ">=", "value": "NaN"}, "x"); err == "" {
		t.Error("delta nan must be rejected")
	}
}

// deltaOrZero is the clamp every stateful counter check relies on: a counter
// that went backwards (reset / reload / device re-plug) must yield 0, not a
// giant unsigned wraparound.
func TestDeltaOrZero(t *testing.T) {
	if got := deltaOrZero(150, 100); got != 50 {
		t.Fatalf("deltaOrZero(150,100) = %d, want 50", got)
	}
	if got := deltaOrZero(100, 100); got != 0 {
		t.Fatalf("deltaOrZero(100,100) = %d, want 0", got)
	}
	if got := deltaOrZero(40, 100); got != 0 {
		t.Fatalf("deltaOrZero(40,100) = %d, want 0 (clamped, not wraparound)", got)
	}
}

func TestParseLevelPredValuePercentBoundaries(t *testing.T) {
	// 0 and 100 are the inclusive bounds of a _pct value.
	for _, v := range []string{"0", "100", "90%"} {
		if got, err := parseLevelPredValue("used_pct", v); err != nil {
			t.Errorf("parseLevelPredValue(used_pct, %q) errored: %v (got %v)", v, err, got)
		}
	}
	// Just past the range is rejected.
	if _, err := parseLevelPredValue("used_pct", "101"); err == nil {
		t.Error("used_pct 101 must be rejected (> 100)")
	}
}
