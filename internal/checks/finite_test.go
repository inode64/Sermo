package checks

import (
	"math"
	"testing"
	"time"
)

// TestNonFiniteReadingsAreNotNumeric pins that NaN and ±Inf never become a
// reading. strconv accepts "nan" and "inf", and a published NaN made the check
// snapshot unencodable, the service detail API answer 500 and the metric store
// roll back its batch.
func TestNonFiniteReadingsAreNotNumeric(t *testing.T) {
	for _, token := range []string{"nan", "NaN", "inf", "-Inf", "+infinity"} {
		if v, ok := firstNumericToken(token + "\n"); ok {
			t.Errorf("firstNumericToken(%q) = %v, want no reading", token, v)
		}
	}
	if v, ok := firstNumericToken("17 messages\n"); !ok || v != 17 {
		t.Fatalf("firstNumericToken(17) = %v %v, want 17", v, ok)
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := NumericData(v); ok {
			t.Errorf("NumericData(%v) accepted a non-finite value", v)
		}
	}
	data := map[string]any{}
	finishScalarCompare(base{name: "sql"}, "sql", "NaN", newValueMatcher("!=", "0"), time.Now(), data)
	if _, ok := data[DataKeyValue]; ok {
		t.Fatalf("data = %v, want no value for a NaN result", data)
	}
}
