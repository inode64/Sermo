package checks

import "testing"

func TestCompareValueContains(t *testing.T) {
	ok, err := compareValue("role:master connected", "contains", "master")
	if err != nil || !ok {
		t.Fatalf("contains hit = %v, err = %v", ok, err)
	}
	ok, err = compareValue("role:replica", "contains", "master")
	if err != nil || ok {
		t.Fatalf("contains miss = %v, err = %v", ok, err)
	}
}
